package relay

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/stats"
	"golang.org/x/net/proxy"
)

// Relay 监听 TPROXY 端口,接收被 nftables 拦截的 TCP 连接,
// 读取原始目标地址(SO_ORIGINAL_DST)后转发到上游代理,并统计流量。
type Relay struct {
	cfg       *config.Config
	collector *stats.Collector
	listener  net.Listener
	wg        sync.WaitGroup
	closeCh   chan struct{}
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
				// IP_TRANSPARENT 允许绑定非本机 IP,是 TPROXY 工作的前提。
				if err := syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
					seterr = fmt.Errorf("setsockopt IP_TRANSPARENT: %w", err)
					return
				}
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
		r.wg.Add(1)
		go r.handleConn(conn)
	}
}

func (r *Relay) handleConn(conn net.Conn) {
	defer r.wg.Done()
	defer conn.Close()

	srcIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	// TPROXY 下,LocalAddr 即为连接的原始目标地址(内核已透明保留)。
	dstIP, dstPortStr, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return
	}
	dstPort, _ := strconv.Atoi(dstPortStr)
	connID := r.collector.OpenConn(srcIP, dstIP, dstPort, "proxy")
	defer func() {
		r.collector.CloseConn(connID, srcIP, dstIP, dstPort, 0, 0, "proxy", false)
	}()

	upstream, err := r.dialUpstream(dstIP, dstPort)
	if err != nil {
		return
	}
	defer upstream.Close()

	var tx, rx uint64
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(upstream, conn)
		tx = uint64(n)
		done <- struct{}{}
	}()
	n, _ := io.Copy(conn, upstream)
	rx = uint64(n)
	<-done

	r.collector.CloseConn(connID, srcIP, dstIP, dstPort, tx, rx, "proxy", false)
}

// dialUpstream 向上游代理拨号,支持 HTTP CONNECT 与 SOCKS5。
func (r *Relay) dialUpstream(dstIP string, dstPort int) (net.Conn, error) {
	target := fmt.Sprintf("%s:%d", dstIP, dstPort)
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
	// 读取 HTTP 响应头。
	br := make([]byte, 1024)
	n, err := conn.Read(br)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if n < 12 || string(br[:12]) != "HTTP/1.1 200" {
		conn.Close()
		return nil, fmt.Errorf("HTTP CONNECT 失败: %s", string(br[:n]))
	}
	return conn, nil
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
