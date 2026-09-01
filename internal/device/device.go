package device

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// Scanner 定期扫描 ARP 表与 DHCP 租约,为设备 IP 补充主机名。
type Scanner struct {
	mu        sync.RWMutex
	hostnames map[string]string // IP -> hostname
	lastSeen  map[string]time.Time // IP -> 最后活跃时间(用于清理)

	leaseFiles []string
	interval   time.Duration
	stopCh     chan struct{}
}

// New 创建设备扫描器。
func New(leaseFiles []string, intervalSec int) *Scanner {
	if intervalSec <= 0 {
		intervalSec = 10
	}
	return &Scanner{
		hostnames:  make(map[string]string),
		lastSeen:   make(map[string]time.Time),
		leaseFiles: leaseFiles,
		interval:   time.Duration(intervalSec) * time.Second,
		stopCh:     make(chan struct{}),
	}
}

// Start 启动后台扫描协程。
func (s *Scanner) Start(ctx context.Context) {
	go s.scanLoop(ctx)
}

// Stop 停止扫描。
func (s *Scanner) Stop() {
	close(s.stopCh)
}

// Hostname 返回指定 IP 的主机名,无则返回空串。
func (s *Scanner) Hostname(ip string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostnames[ip]
}

func (s *Scanner) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	// 首次立即执行。
	s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

func (s *Scanner) scan(ctx context.Context) {
	// 1. 从 DHCP 租约合并主机名。
	merged := make(map[string]string)
	for _, path := range s.leaseFiles {
		for ip, host := range s.parseDHCPLeases(path) {
			merged[ip] = host
		}
	}

	// 2. 从 ARP 表获取活跃 IP 列表。
	arpIPs := s.parseARP()

	// 3. 对未知主机名的 IP 并发反向 DNS(带超时、缓存、限并发)。
	s.mu.RLock()
	needResolve := []string{}
	for _, ip := range arpIPs {
		if _, ok := merged[ip]; !ok {
			if _, cached := s.hostnames[ip]; !cached {
				needResolve = append(needResolve, ip)
			}
		}
		s.lastSeen[ip] = time.Now()
	}
	s.mu.RUnlock()

	// 并发反向 DNS(最多 32 并发,每次超时 1s)
	if len(needResolve) > 0 {
		resolved := s.batchReverseDNS(ctx, needResolve)
		for ip, host := range resolved {
			merged[ip] = host
		}
	}

	// 4. 更新缓存,清理离线设备(1小时未见)
	s.mu.Lock()
	for ip, host := range merged {
		s.hostnames[ip] = host
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	for ip, t := range s.lastSeen {
		if t.Before(cutoff) {
			delete(s.hostnames, ip)
			delete(s.lastSeen, ip)
		}
	}
	s.mu.Unlock()
}

// batchReverseDNS 并发反向 DNS 查询,返回 IP -> hostname 映射。
func (s *Scanner) batchReverseDNS(ctx context.Context, ips []string) map[string]string {
	result := make(map[string]string)
	mu := sync.Mutex{}
	sem := semaphore.NewWeighted(32) // 最多 32 并发
	g, gctx := errgroup.WithContext(ctx)

	for _, ip := range ips {
		ip := ip
		g.Go(func() error {
			if err := sem.Acquire(gctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			// 每次查询超时 1s
			lookupCtx, cancel := context.WithTimeout(gctx, 1*time.Second)
			defer cancel()

			names, err := net.DefaultResolver.LookupAddr(lookupCtx, ip)
			if err == nil && len(names) > 0 {
				host := strings.TrimSuffix(names[0], ".")
				mu.Lock()
				result[ip] = host
				mu.Unlock()
			}
			return nil
		})
	}
	g.Wait()
	return result
}

func (s *Scanner) parseARP() []string {
	out, err := exec.Command("ip", "neigh", "show").Output()
	if err != nil {
		return nil
	}
	var ips []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 1 {
			if ip := net.ParseIP(fields[0]); ip != nil && ip.To4() != nil {
				ips = append(ips, fields[0])
			}
		}
	}
	return ips
}

func (s *Scanner) parseDHCPLeases(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m := make(map[string]string)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// dnsmasq 格式: <timestamp> <mac> <ip> <hostname> <client-id>
		fields := strings.Fields(sc.Text())
		if len(fields) >= 4 {
			ip := fields[2]
			host := fields[3]
			if host != "*" {
				m[ip] = host
			}
		}
	}
	return m
}
