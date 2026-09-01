package route

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Manager 负责开启 IP 转发与配置策略路由,使打了 fwmark 的流量走本地 TPROXY。
// 它记录了自己修改过的系统状态,以便 Restore 时恢复。
type Manager struct {
	fwMark     int
	routeTable int

	// forwardWasEnabled 记录进入前 ip_forward 是否已开启,用于恢复。
	forwardWasEnabled bool
	appliedForward    bool
	appliedRule       bool
	appliedRoute      bool
}

const ipForwardPath = "/proc/sys/net/ipv4/ip_forward"

// New 创建路由管理器。
func New(fwMark, routeTable int) *Manager {
	return &Manager{fwMark: fwMark, routeTable: routeTable}
}

// Setup 开启 IP 转发,并添加策略路由规则:
//   - ip rule: 匹配 fwmark 的报文查指定路由表
//   - ip route: 该表内将所有流量导向 lo(TPROXY 依赖 local 路由)
func (m *Manager) Setup() error {
	if err := m.enableForward(); err != nil {
		return err
	}
	if err := m.addRule(); err != nil {
		return err
	}
	if err := m.addRoute(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) enableForward() error {
	cur, err := os.ReadFile(ipForwardPath)
	if err != nil {
		return fmt.Errorf("读取 ip_forward 失败: %w", err)
	}
	m.forwardWasEnabled = strings.TrimSpace(string(cur)) == "1"
	if m.forwardWasEnabled {
		return nil
	}
	if err := os.WriteFile(ipForwardPath, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("开启 ip_forward 失败: %w", err)
	}
	m.appliedForward = true
	return nil
}

func (m *Manager) addRule() error {
	// 幂等:先尝试删除同规则,忽略错误,再添加。
	_ = run("ip", "rule", "del", "fwmark", fmt.Sprint(m.fwMark), "lookup", fmt.Sprint(m.routeTable))
	if err := run("ip", "rule", "add", "fwmark", fmt.Sprint(m.fwMark), "lookup", fmt.Sprint(m.routeTable)); err != nil {
		return fmt.Errorf("添加 ip rule 失败: %w", err)
	}
	m.appliedRule = true
	return nil
}

func (m *Manager) addRoute() error {
	_ = run("ip", "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprint(m.routeTable))
	if err := run("ip", "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprint(m.routeTable)); err != nil {
		return fmt.Errorf("添加 ip route 失败: %w", err)
	}
	m.appliedRoute = true
	return nil
}

// Restore 撤销由 Setup 施加的所有更改。尽力而为,收集所有错误。
func (m *Manager) Restore() error {
	var errs []string
	if m.appliedRoute {
		if err := run("ip", "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprint(m.routeTable)); err != nil {
			errs = append(errs, fmt.Sprintf("删除 ip route: %v", err))
		}
	}
	if m.appliedRule {
		if err := run("ip", "rule", "del", "fwmark", fmt.Sprint(m.fwMark), "lookup", fmt.Sprint(m.routeTable)); err != nil {
			errs = append(errs, fmt.Sprintf("删除 ip rule: %v", err))
		}
	}
	if m.appliedForward && !m.forwardWasEnabled {
		if err := os.WriteFile(ipForwardPath, []byte("0\n"), 0o644); err != nil {
			errs = append(errs, fmt.Sprintf("恢复 ip_forward: %v", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("路由恢复出现问题: %s", strings.Join(errs, "; "))
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
