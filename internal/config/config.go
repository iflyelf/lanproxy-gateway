package config

import (
	"fmt"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是网关的完整运行配置。
type Config struct {
	// LANInterface 是面向局域网的网卡名(如 eth0)。留空则自动探测默认网卡。
	LANInterface string `yaml:"lan_interface"`

	// Upstream 是上游代理地址,通常指向 clash 容器,如 127.0.0.1:7890。
	Upstream UpstreamConfig `yaml:"upstream"`

	// TProxy 是透明代理相关的参数。
	TProxy TProxyConfig `yaml:"tproxy"`

	// Web 是管理页面(WebUI)相关配置。
	Web WebConfig `yaml:"web"`

	// Device 是设备发现相关配置。
	Device DeviceConfig `yaml:"device"`
}

// UpstreamConfig 描述上游代理。
type UpstreamConfig struct {
	// Type 是代理类型: http 或 socks5。
	Type string `yaml:"type"`
	// Address 是代理监听地址,如 127.0.0.1:7890。
	Address string `yaml:"address"`
	// Username/Password 用于需要认证的上游代理,可留空。
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// TProxyConfig 描述透明代理(TPROXY)参数。
type TProxyConfig struct {
	// ListenPort 是 relay 的 TPROXY 监听端口。
	ListenPort int `yaml:"listen_port"`
	// FwMark 是打给被代理流量的 fwmark 值。
	FwMark int `yaml:"fwmark"`
	// RouteTable 是策略路由使用的路由表编号。
	RouteTable int `yaml:"route_table"`
	// BypassCIDRs 是直连(不走代理)的目标网段(IPv4),通常包含局域网与保留地址。
	BypassCIDRs []string `yaml:"bypass_cidrs"`
	// BypassCIDRs6 是直连(不走代理)的目标网段(IPv6)。
	BypassCIDRs6 []string `yaml:"bypass_cidrs6"`
	// TCPOnly 为 true 时仅接管 TCP(默认)。UDP 交由 smartdns/直连处理。
	TCPOnly bool `yaml:"tcp_only"`
	// EnableIPv6 为 true 时同时接管 IPv6 TCP 流量。
	EnableIPv6 bool `yaml:"enable_ipv6"`
}

// WebConfig 描述管理页面。
type WebConfig struct {
	// Listen 是 WebUI 监听地址,如 0.0.0.0:8088。建议绑定 LAN 网段地址。
	Listen string `yaml:"listen"`
	// Username/Password 是登录凭据。Password 可为明文或 bcrypt 哈希(以 $2 开头视为哈希)。
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// SessionSecret 用于签发会话 cookie。留空则启动时随机生成。
	SessionSecret string `yaml:"session_secret"`
}

// DeviceConfig 描述设备发现。
type DeviceConfig struct {
	// ScanIntervalSeconds 是设备扫描间隔(秒)。
	ScanIntervalSeconds int `yaml:"scan_interval_seconds"`
	// DHCPLeaseFiles 是可选的 DHCP 租约文件路径列表,用于解析主机名。
	DHCPLeaseFiles []string `yaml:"dhcp_lease_files"`
}

// Default 返回一份带有合理默认值的配置。
func Default() *Config {
	return &Config{
		LANInterface: "",
		Upstream: UpstreamConfig{
			Type:    "http",
			Address: "127.0.0.1:7890",
		},
		TProxy: TProxyConfig{
			ListenPort: 12345,
			FwMark:     1,
			RouteTable: 100,
			BypassCIDRs: []string{
				"0.0.0.0/8",
				"10.0.0.0/8",
				"127.0.0.0/8",
				"169.254.0.0/16",
				"172.16.0.0/12",
				"192.168.0.0/16",
				"224.0.0.0/4",
				"240.0.0.0/4",
			},
			BypassCIDRs6: []string{
				"::1/128",
				"fc00::/7",
				"fe80::/10",
				"ff00::/8",
			},
			TCPOnly:    true,
			EnableIPv6: false,
		},
		Web: WebConfig{
			Listen:   "0.0.0.0:8088",
			Username: "admin",
			Password: "admin",
		},
		Device: DeviceConfig{
			ScanIntervalSeconds: 10,
			DHCPLeaseFiles: []string{
				"/var/lib/misc/dnsmasq.leases",
				"/var/lib/dnsmasq/dnsmasq.leases",
			},
		},
	}
}

// Load 从指定路径读取 YAML 配置。文件不存在时返回默认配置。
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save 将配置写入指定路径。
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	return nil
}

// Validate 校验配置的合法性。
func (c *Config) Validate() error {
	switch c.Upstream.Type {
	case "http", "socks5":
	default:
		return fmt.Errorf("upstream.type 必须为 http 或 socks5, 当前为 %q", c.Upstream.Type)
	}
	if c.Upstream.Address == "" {
		return fmt.Errorf("upstream.address 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Upstream.Address); err != nil {
		return fmt.Errorf("upstream.address 格式错误(应为 host:port): %w", err)
	}
	if c.TProxy.ListenPort <= 0 || c.TProxy.ListenPort > 65535 {
		return fmt.Errorf("tproxy.listen_port 非法: %d", c.TProxy.ListenPort)
	}
	if c.TProxy.FwMark <= 0 {
		return fmt.Errorf("tproxy.fwmark 必须为正整数")
	}
	if c.TProxy.RouteTable <= 0 {
		return fmt.Errorf("tproxy.route_table 必须为正整数")
	}
	for _, cidr := range c.TProxy.BypassCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("bypass_cidrs 中存在非法网段 %q: %w", cidr, err)
		}
	}
	for _, cidr := range c.TProxy.BypassCIDRs6 {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("bypass_cidrs6 中存在非法网段 %q: %w", cidr, err)
		}
	}
	if c.Web.Listen == "" {
		return fmt.Errorf("web.listen 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Web.Listen); err != nil {
		return fmt.Errorf("web.listen 格式错误(应为 host:port): %w", err)
	}
	if c.Web.Username == "" || c.Web.Password == "" {
		return fmt.Errorf("web.username 与 web.password 不能为空")
	}
	return nil
}
