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
	applied    bool
}

// Options 是构建 nftables 规则所需参数。
type Options struct {
	ListenPort  int      // TPROXY relay 监听端口
	FwMark      int      // 打给被代理流量的 fwmark
	LANIface    string   // 面向局域网的网卡
	BypassCIDRs []string // 直连网段
}

// New 创建 netfilter 管理器。
func New(opts Options) *Manager {
	return &Manager{
		tableName:  "lanproxy_gw",
		listenPort: opts.ListenPort,
		fwMark:     opts.FwMark,
		lanIface:   opts.LANIface,
		bypass:     opts.BypassCIDRs,
	}
}

// nftRulesetTemplate 定义 TPROXY 表。
//
// 说明:
//   - 只处理 prerouting 链上从 LAN 网卡转发进来的 TCP。
//   - 目的地址命中 bypass 集合(局域网/保留地址)时直接 return,走正常转发(直连)。
//   - 其余 TCP 使用 tproxy 重定向到本机 relay,并打 fwmark,配合策略路由回流到 lo。
//   - hook 优先级使用 mangle(-150),在 nat 之前,且不与 firewalld 的 filter/nat 表冲突。
const nftRulesetTemplate = `table inet {{.Table}} {
	set bypass4 {
		type ipv4_addr
		flags interval
		{{- if .Bypass}}
		elements = { {{.Bypass}} }
		{{- end}}
	}

	chain prerouting {
		type filter hook prerouting priority mangle; policy accept;
		{{- if .LANIface}}
		iifname != "{{.LANIface}}" return
		{{- end}}
		meta l4proto != tcp return
		ip daddr @bypass4 return
		meta l4proto tcp tproxy ip to 127.0.0.1:{{.ListenPort}} meta mark set {{.FwMark}} accept
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
		LANIface   string
		ListenPort int
		FwMark     int
	}{
		Table:      m.tableName,
		Bypass:     strings.Join(m.bypass, ", "),
		LANIface:   m.lanIface,
		ListenPort: m.listenPort,
		FwMark:     m.fwMark,
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
