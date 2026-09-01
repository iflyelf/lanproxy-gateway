package relay

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/stats"
	"golang.org/x/net/proxy"
)

// bufferPool 复用小缓冲,降低高并发下的内存分配。
var bufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 16384) // 16KB
		return &b
	},
}

// Relay 监听 TPROXY 端口,接收被 nftables 拦截的 TCP 连接,
// 读取原始目标地址(SO_ORIGINAL_DST)后转发到上游代理,并统计流量。
type Relay struct {
	cfg       *config.Config
	collector *stats.Collector
	listener  net.Listener
	wg        sync.WaitGroup
	closeCh   chan struct{}
	activeConns atomic.Int64
}

// New 创建 relay 实例。
func New(cfg *config.Config, collector *stats.Collector) *Relay {
	return &Relay{
		cfg:       cfg,
		collector: collector,
		closeCh:   make(chan struct{}),
	}
}

// Start 启动 TPROXY 监听,并开始接受连接。必须以 root 或 CAP_NET_ADMIN 运行。
func (r *Relay) Start() error {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var seterr error
			if err := c.Control(func(fd uintptr) {
				// IP_TRANSPARENT 允许绑定非本机 IP,是 TPROXY 工作的前提(IPv4)。
				if err := syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
					seterr = fmt.Errorf("setsockopt IP_TRANSPARENT: %w", err)
					return
				}
				// IPV6_TRANSPARENT(SOL_IPV6=41, IPV6_TRANSPARENT=75)用于 IPv6 TPROXY。
				// 双栈监听套接字上同时设置以支持 IPv6;不支持时忽略错误。
				_ = syscall.SetsockoptInt(int(fd), 41, 75, 1)
			}); err != nil {
				return err
			}
			return seterr
		},
	}
	addr := fmt.Sprintf(":%d", r.cfg.TProxy.ListenPort)
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 TPROXY 端口 %s 失败(需要 root 权限): %w", addr, err)
	}
	r.listener = ln

	r.wg.Add(1)
	go r.acceptLoop()
	return nil
}

// Stop 停止监听并等待所有连接关闭。
func (r *Relay) Stop() error {
	close(r.closeCh)
	if r.listener != nil {
		r.listener.Close()
	}
	r.wg.Wait()
	return nil
}

func (r *Relay) acceptLoop() {
	defer r.wg.Done()
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			select {
			case <-r.closeCh:
				return
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		// 背压:达到连接数上限时拒绝新连接
		if max := r.cfg.TProxy.MaxConnections; max > 0 {
			if r.activeConns.Load() >= int64(max) {
				conn.Close()
				continue
			}
		}

		r.wg.Add(1)
		go r.handleConn(conn)
	}
}

func (r *Relay) handleConn(conn net.Conn) {
	defer r.wg.Done()
	defer conn.Close()
	r.activeConns.Add(1)
	defer r.activeConns.Add(-1)

	// 设置空闲超时(防连接泄漏)
	conn.SetDeadline(time.Now().Add(5 * time.Minute))

	srcIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	// TPROXY 下,LocalAddr 即为连接的原始目标地址(内核已透明保留)。
	dstIP, dstPortStr, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return
	}
	dstPort, _ := strconv.Atoi(dstPortStr)
	connID := r.collector.OpenConn(srcIP, dstIP, dstPort, "proxy")

	// 1. 先尝试代理
	upstream, upstreamType, err := r.dialUpstreamOrFallback(dstIP, dstPort)
	if err != nil {
		// 代理和回退都失败,记录失败连接
		r.collector.RecordConn(connID, srcIP, dstIP, dstPort, 0, 0, "failed", true)
		return
	}
	defer upstream.Close()

	// 清除初始超时,双向拷贝中动态设置
	conn.SetDeadline(time.Time{})
	upstream.SetDeadline(time.Time{})

	// 2. 双向拷贝(支持半关闭)
	tx, rx := r.bidirectionalCopy(conn, upstream)

	// 3. 记录连接(仅此一次)
	r.collector.RecordConn(connID, srcIP, dstIP, dstPort, tx, rx, upstreamType, false)
}

// dialUpstreamOrFallback 先尝试代理,失败且开启回退时改直连。
// 返回 conn, upstreamType("proxy"/"direct"), error
func (r *Relay) dialUpstreamOrFallback(dstIP string, dstPort int) (net.Conn, string, error) {
	// 先尝试代理
	conn, err := r.dialUpstream(dstIP, dstPort)
	if err == nil {
		return conn, "proxy", nil
	}

	// 代理失败,若开启回退则直连
	if !r.cfg.TProxy.FallbackDirect {
		return nil, "", fmt.Errorf("代理失败且未开启回退: %w", err)
	}

	target := net.JoinHostPort(dstIP, strconv.Itoa(dstPort))
	directConn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return nil, "", fmt.Errorf("代理失败且直连也失败: %w", err)
	}
	return directConn, "direct", nil
}

// dialUpstream 向上游代理拨号,支持 HTTP CONNECT 与 SOCKS5。
func (r *Relay) dialUpstream(dstIP string, dstPort int) (net.Conn, error) {
	target := net.JoinHostPort(dstIP, strconv.Itoa(dstPort))
	switch r.cfg.Upstream.Type {
	case "http":
		return r.dialHTTP(target)
	case "socks5":
		return r.dialSOCKS5(target)
	default:
		return nil, fmt.Errorf("未知的上游类型: %s", r.cfg.Upstream.Type)
	}
}

func (r *Relay) dialHTTP(target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", r.cfg.Upstream.Address, 5*time.Second)
	if err != nil {
		return nil, err
	}
	req := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Host: target},
		Host:   target,
		Header: make(http.Header),
	}
	if r.cfg.Upstream.Username != "" {
		req.SetBasicAuth(r.cfg.Upstream.Username, r.cfg.Upstream.Password)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}

	// 正确解析 HTTP 响应(避免分包/粘连问题)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取 HTTP CONNECT 响应失败: %w", err)
	}
	if resp.StatusCode != 200 {
		conn.Close()
		return nil, fmt.Errorf("HTTP CONNECT 失败: %s", resp.Status)
	}

	// 返回包装后的 conn(bufio.Reader 可能缓存了后续数据)
	return &bufferedConn{Conn: conn, r: br}, nil
}

func (r *Relay) dialSOCKS5(target string) (net.Conn, error) {
	var auth *proxy.Auth
	if r.cfg.Upstream.Username != "" {
		auth = &proxy.Auth{
			User:     r.cfg.Upstream.Username,
			Password: r.cfg.Upstream.Password,
		}
	}
	dialer, err := proxy.SOCKS5("tcp", r.cfg.Upstream.Address, auth, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return dialer.Dial("tcp", target)
}

// bidirectionalCopy 双向拷贝,支持半关闭,返回 (tx, rx) 字节数。
func (r *Relay) bidirectionalCopy(client, upstream net.Conn) (uint64, uint64) {
	var tx, rx atomic.Uint64
	done := make(chan struct{}, 2)

	// client -> upstream
	go func() {
		n := r.copyWithTimeout(upstream, client)
		tx.Store(n)
		if tc, ok := upstream.(*net.TCPConn); ok {
			tc.CloseWrite() // 半关闭
		}
		done <- struct{}{}
	}()

	// upstream -> client
	go func() {
		n := r.copyWithTimeout(client, upstream)
		rx.Store(n)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done
	return tx.Load(), rx.Load()
}

// copyWithTimeout 带动态超时的拷贝(空闲 2 分钟断开),优先用 splice 零拷贝。
func (r *Relay) copyWithTimeout(dst, src net.Conn) uint64 {
	// io.Copy 对 TCP↔TCP 自动走 splice,无需显式处理
	// 若退化成用户态拷贝,用池化缓冲
	type deadliner interface {
		SetReadDeadline(time.Time) error
	}
	
	var total uint64
	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)

	for {
		// 动态超时:每次读前重置
		if d, ok := src.(deadliner); ok {
			d.SetReadDeadline(time.Now().Add(2 * time.Minute))
		}
		
		n, err := src.Read(*buf)
		if n > 0 {
			total += uint64(n)
			if _, writeErr := dst.Write((*buf)[:n]); writeErr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return total
}

// bufferedConn 包装带缓冲的 conn,用于 HTTP CONNECT 响应后的数据透传。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (bc *bufferedConn) Read(p []byte) (int, error) {
	return bc.r.Read(p)
}
