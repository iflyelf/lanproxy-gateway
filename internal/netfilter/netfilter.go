package netfilter

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"text/template"
)

// Manager 使用 nftables(nft 命令)构建一个独立的 TPROXY 表。
// 该表名为 inet lanproxy_gw,与 firewalld 的表互不干扰,卸载时整表删除。
type Manager struct {
	tableName  string
	listenPort int
	fwMark     int
	lanIface   string
	bypass     []string
	bypass6    []string
	enableIPv6 bool
	applied    bool
}

// Options 是构建 nftables 规则所需参数。
type Options struct {
	ListenPort   int      // TPROXY relay 监听端口
	FwMark       int      // 打给被代理流量的 fwmark
	LANIface     string   // 面向局域网的网卡
	BypassCIDRs  []string // 直连网段(IPv4)
	BypassCIDRs6 []string // 直连网段(IPv6)
	EnableIPv6   bool     // 是否启用 IPv6 TPROXY
}

// New 创建 netfilter 管理器。
func New(opts Options) *Manager {
	return &Manager{
		tableName:  "lanproxy_gw",
		listenPort: opts.ListenPort,
		fwMark:     opts.FwMark,
		lanIface:   opts.LANIface,
		bypass:     opts.BypassCIDRs,
		bypass6:    opts.BypassCIDRs6,
		enableIPv6: opts.EnableIPv6,
	}
}

// nftRulesetTemplate 定义 TPROXY 表。
//
// 说明:
//   - 只处理 prerouting 链上从 LAN 网卡转发进来的 TCP。
//   - 目的地址命中 bypass 集合(局域网/保留地址)时直接 return,走正常转发(直连)。
//   - 其余 TCP 使用 tproxy 重定向到本机 relay,并打 fwmark,配合策略路由回流到 lo。
//   - hook 优先级使用 mangle(-150),在 nat 之前,且不与 firewalld 的 filter/nat 表冲突。
//   - IPv6 支持:当 EnableIPv6=true 时,同时处理 IPv6 TCP 流量。
const nftRulesetTemplate = `table inet {{.Table}} {
	set bypass4 {
		type ipv4_addr
		flags interval
		{{- if .Bypass}}
		elements = { {{.Bypass}} }
		{{- end}}
	}
	{{- if .EnableIPv6}}

	set bypass6 {
		type ipv6_addr
		flags interval
		{{- if .Bypass6}}
		elements = { {{.Bypass6}} }
		{{- end}}
	}
	{{- end}}

	chain prerouting {
		type filter hook prerouting priority mangle; policy accept;
		{{- if .LANIface}}
		iifname != "{{.LANIface}}" return
		{{- end}}
		meta l4proto != tcp return
		meta nfproto ipv4 ip daddr @bypass4 return
		meta nfproto ipv4 tproxy ip to 127.0.0.1:{{.ListenPort}} meta mark set {{.FwMark}} accept
		{{- if .EnableIPv6}}
		meta nfproto ipv6 ip6 daddr @bypass6 return
		meta nfproto ipv6 tproxy ip6 to [::1]:{{.ListenPort}} meta mark set {{.FwMark}} accept
		{{- end}}
	}
}`

func (m *Manager) render() (string, error) {
	tmpl, err := template.New("nft").Parse(nftRulesetTemplate)
	if err != nil {
		return "", err
	}
	data := struct {
		Table      string
		Bypass     string
		Bypass6    string
		LANIface   string
		ListenPort int
		FwMark     int
		EnableIPv6 bool
	}{
		Table:      m.tableName,
		Bypass:     strings.Join(m.bypass, ", "),
		Bypass6:    strings.Join(m.bypass6, ", "),
		LANIface:   m.lanIface,
		ListenPort: m.listenPort,
		FwMark:     m.fwMark,
		EnableIPv6: m.enableIPv6,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Setup 应用 nftables 规则。先删除同名旧表(幂等),再加载新表。
func (m *Manager) Setup() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return fmt.Errorf("未找到 nft 命令,请安装 nftables: %w", err)
	}
	// 幂等清理:忽略不存在的错误。
	_ = m.deleteTable()

	ruleset, err := m.render()
	if err != nil {
		return fmt.Errorf("渲染 nft 规则失败: %w", err)
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("加载 nft 规则失败: %v (%s)\n规则内容:\n%s", err, strings.TrimSpace(string(out)), ruleset)
	}
	m.applied = true
	return nil
}

// Restore 删除本程序创建的表。
func (m *Manager) Restore() error {
	if !m.applied {
		return nil
	}
	if err := m.deleteTable(); err != nil {
		return fmt.Errorf("删除 nft 表失败: %w", err)
	}
	m.applied = false
	return nil
}

func (m *Manager) deleteTable() error {
	cmd := exec.Command("nft", "delete", "table", "inet", m.tableName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Ruleset 返回渲染后的规则文本,便于调试与展示。
func (m *Manager) Ruleset() (string, error) {
	return m.render()
}
