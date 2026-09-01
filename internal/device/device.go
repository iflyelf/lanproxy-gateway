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
)

// Scanner 定期扫描 ARP 表与 DHCP 租约,为设备 IP 补充主机名。
type Scanner struct {
	mu        sync.RWMutex
	hostnames map[string]string // IP -> hostname

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

func (s *Scanner) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.scan()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.scan()
		}
	}
}

func (s *Scanner) scan() {
	ipHost := make(map[string]string)

	// 1. 解析 ARP 表(ip neigh),提取 IP。
	arpIPs := s.scanARP()
	for _, ip := range arpIPs {
		ipHost[ip] = ""
	}

	// 2. 解析 DHCP 租约文件,获取 hostname。
	for _, lf := range s.leaseFiles {
		leases := s.parseDHCPLeases(lf)
		for ip, host := range leases {
			if host != "" {
				ipHost[ip] = host
			}
		}
	}

	// 3. 对无 hostname 的 IP 做反向 DNS 查询(可选,限时)。
	for ip := range ipHost {
		if ipHost[ip] == "" {
			if names, err := net.LookupAddr(ip); err == nil && len(names) > 0 {
				ipHost[ip] = strings.TrimSuffix(names[0], ".")
			}
		}
	}

	s.mu.Lock()
	s.hostnames = ipHost
	s.mu.Unlock()
}

// Hostname 返回指定 IP 的主机名,无则返回空字符串。
func (s *Scanner) Hostname(ip string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostnames[ip]
}

// All 返回所有已知 IP -> hostname 映射。
func (s *Scanner) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]string, len(s.hostnames))
	for k, v := range s.hostnames {
		cp[k] = v
	}
	return cp
}

func (s *Scanner) scanARP() []string {
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
