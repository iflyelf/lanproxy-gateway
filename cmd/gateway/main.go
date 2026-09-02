package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iflyelf/lanproxy-gateway/internal/app"
	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/logger"
	"github.com/iflyelf/lanproxy-gateway/internal/netfilter"
	"github.com/iflyelf/lanproxy-gateway/internal/route"
	"github.com/spf13/cobra"
)

var (
	// version 在构建时通过 -ldflags 注入。
	version = "dev"
	// cfgPath 是配置文件路径。
	cfgPath string
)

func main() {
	root := &cobra.Command{
		Use:     "lanproxy-gateway",
		Short:   "局域网透明代理网关(基于 nftables TPROXY,不依赖 eBPF/iptables)",
		Version: version,
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", "/etc/lanproxy-gateway/gateway.yaml", "配置文件路径")

	root.AddCommand(runCmd(), statusCmd(), configCmd(), cleanCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// runCmd 前台运行网关守护进程。
func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "前台运行网关守护进程",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("需要 root 权限运行(TPROXY 与策略路由依赖 CAP_NET_ADMIN)")
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			// 初始化日志(按天切割 + 保留 N 天 + 级别控制)。
			if err := logger.Init(logger.Options{
				Path:       cfg.Log.Path,
				Level:      logger.ParseLevel(cfg.Log.Level),
				MaxAgeDays: cfg.Log.MaxAgeDays,
				Console:    cfg.Log.Console,
			}); err != nil {
				return fmt.Errorf("初始化日志失败: %w", err)
			}
			defer logger.Close()

			a, err := app.New(cfg)
			if err != nil {
				return err
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			logger.Infof("lanproxy-gateway %s 启动中...", version)

			// Run 在独立协程中执行,主协程等待信号或运行错误。
			// 收到信号后同步执行 Stop(),确保 nftables/策略路由清理完成再退出,
			// 避免 cancel 导致 Run 提前返回、进程被杀而残留系统规则。
			runErr := make(chan error, 1)
			go func() { runErr <- a.Run() }()

			select {
			case sig := <-sigCh:
				logger.Infof("收到信号 %v,开始清理...", sig)
				a.Stop()
				return nil
			case err := <-runErr:
				// 运行异常退出时同样清理已施加的系统状态。
				a.Stop()
				return err
			}
		},
	}
}

// statusCmd 打印当前配置概要。
func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "显示配置概要与接入指引",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			// 探测 LAN 网卡与其 IPv4 地址、掩码,供接入指引直接显示实际值。
			ifaceName, lanIP, netmask := detectLAN(cfg.LANInterface)

			fmt.Printf("配置文件      : %s\n", cfgPath)
			if cfg.LANInterface == "" && ifaceName != "" {
				fmt.Printf("LAN 网卡      : %s (自动探测)\n", ifaceName)
			} else {
				fmt.Printf("LAN 网卡      : %s\n", orAuto(cfg.LANInterface))
			}
			if lanIP != "" {
				fmt.Printf("本机 LAN IP   : %s\n", lanIP)
			}
			fmt.Printf("上游代理      : %s (%s)\n", cfg.Upstream.Address, cfg.Upstream.Type)
			fmt.Printf("TPROXY 端口   : %d\n", cfg.TProxy.ListenPort)
			fmt.Printf("WebUI         : %s\n", webURL(cfg.Web.Listen, lanIP))
			fmt.Println()
			fmt.Println("设备接入方式(在每台设备上手动设置静态网络):")
			if lanIP != "" {
				fmt.Printf("  网关   : %s\n", lanIP)
				fmt.Printf("  DNS    : %s (由 smartdns 提供,端口 53)\n", lanIP)
			} else {
				fmt.Println("  网关   : 本机 LAN IP(未探测到,请手动确认)")
				fmt.Println("  DNS    : 本机 LAN IP (由 smartdns 提供,端口 53)")
			}
			if netmask != "" {
				fmt.Printf("  掩码   : %s\n", netmask)
			} else {
				fmt.Println("  掩码   : 255.255.255.0")
			}
			return nil
		},
	}
}

// cleanCmd 清理残留的系统规则(用于进程被 SIGKILL 后手动清理)。
func cleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "清理残留的 nftables 表与策略路由(进程被强杀后使用)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("需要 root 权限")
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			nf := netfilter.New(netfilter.Options{
				ListenPort: cfg.TProxy.ListenPort,
				FwMark:     cfg.TProxy.FwMark,
				LANIface:   cfg.LANInterface,
			})
			if err := nf.ForceClean(); err != nil {
				fmt.Printf("清理 nftables 出错: %v\n", err)
			} else {
				fmt.Println("已清理 nftables 表 inet lanproxy_gw")
			}
			rt := route.New(cfg.TProxy.FwMark, cfg.TProxy.RouteTable, cfg.TProxy.EnableIPv6)
			rt.ForceClean()
			fmt.Printf("已清理策略路由(fwmark %d / table %d)\n", cfg.TProxy.FwMark, cfg.TProxy.RouteTable)
			fmt.Println("清理完成。")
			return nil
		},
	}
}

// configCmd 生成默认配置文件。
func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "生成默认配置文件",
	}
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "在配置路径写入默认配置(不覆盖已存在文件)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(cfgPath); err == nil {
				return fmt.Errorf("配置文件已存在: %s", cfgPath)
			}
			dir := cfgPath[:len(cfgPath)-len("/gateway.yaml")]
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := config.Default().Save(cfgPath); err != nil {
				return err
			}
			fmt.Printf("已生成默认配置: %s\n", cfgPath)
			fmt.Println("请修改 web.username/web.password 后再启动服务。")
			return nil
		},
	}
	cmd.AddCommand(initCmd)
	return cmd
}

func orAuto(s string) string {
	if s == "" {
		return "(自动探测)"
	}
	return s
}

// detectLAN 探测 LAN 网卡的 IPv4 地址与掩码。
// 返回:网卡名、IP 地址、子网掩码(点分十进制)。
func detectLAN(configIface string) (string, string, string) {
	var targetIface string
	if configIface != "" {
		targetIface = configIface
	} else {
		// 自动探测:找第一个有 IPv4 地址的非 loopback 网卡
		ifaces, err := net.Interfaces()
		if err != nil {
			return "", "", ""
		}
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					targetIface = iface.Name
					break
				}
			}
			if targetIface != "" {
				break
			}
		}
	}
	if targetIface == "" {
		return "", "", ""
	}

	// 获取该网卡的 IPv4 地址与掩码
	iface, err := net.InterfaceByName(targetIface)
	if err != nil {
		return targetIface, "", ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return targetIface, "", ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			ip := ipnet.IP.String()
			mask := net.IP(ipnet.Mask).String()
			return targetIface, ip, mask
		}
	}
	return targetIface, "", ""
}

// webURL 将 listen 地址转换为可访问的 URL。
// 若 listen 是 0.0.0.0,用 lanIP 替换;否则保持原样。
func webURL(listen, lanIP string) string {
	if strings.HasPrefix(listen, "0.0.0.0:") && lanIP != "" {
		return "http://" + strings.Replace(listen, "0.0.0.0", lanIP, 1)
	}
	return "http://" + listen
}
