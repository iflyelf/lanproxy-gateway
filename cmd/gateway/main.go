package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/iflyelf/lanproxy-gateway/internal/app"
	"github.com/iflyelf/lanproxy-gateway/internal/config"
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

	root.AddCommand(runCmd(), statusCmd(), configCmd())

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
			a, err := app.New(cfg)
			if err != nil {
				return err
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				a.Stop()
			}()

			log.Printf("lanproxy-gateway %s 启动中...", version)
			return a.Run()
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
			fmt.Printf("配置文件      : %s\n", cfgPath)
			fmt.Printf("LAN 网卡      : %s\n", orAuto(cfg.LANInterface))
			fmt.Printf("上游代理      : %s (%s)\n", cfg.Upstream.Address, cfg.Upstream.Type)
			fmt.Printf("TPROXY 端口   : %d\n", cfg.TProxy.ListenPort)
			fmt.Printf("WebUI         : http://%s\n", cfg.Web.Listen)
			fmt.Println()
			fmt.Println("设备接入方式(在每台设备上手动设置静态网络):")
			fmt.Println("  网关   : 本机 LAN IP")
			fmt.Println("  DNS    : 本机 LAN IP (由 smartdns 提供,端口 53)")
			fmt.Println("  掩码   : 255.255.255.0")
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
