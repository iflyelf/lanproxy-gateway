package app

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/iflyelf/lanproxy-gateway/internal/api"
	"github.com/iflyelf/lanproxy-gateway/internal/auth"
	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/device"
	"github.com/iflyelf/lanproxy-gateway/internal/logger"
	"github.com/iflyelf/lanproxy-gateway/internal/netfilter"
	"github.com/iflyelf/lanproxy-gateway/internal/relay"
	"github.com/iflyelf/lanproxy-gateway/internal/route"
	"github.com/iflyelf/lanproxy-gateway/internal/stats"
)

// App 编排网关的所有子系统:路由、netfilter、relay、设备扫描、WebUI。
type App struct {
	cfg *config.Config

	route     *route.Manager
	netfilter *netfilter.Manager
	relay     *relay.Relay
	scanner   *device.Scanner
	collector *stats.Collector
	apiSrv    *api.Server

	ctx    context.Context
	cancel context.CancelFunc
}

// New 根据配置构建 App。会自动探测 LAN 网卡(若未指定)。
func New(cfg *config.Config) (*App, error) {
	if cfg.LANInterface == "" {
		iface, err := detectDefaultInterface()
		if err != nil {
			return nil, fmt.Errorf("自动探测 LAN 网卡失败,请在配置中显式设置 lan_interface: %w", err)
		}
		cfg.LANInterface = iface
		logger.Infof("自动探测到 LAN 网卡: %s", iface)
	}

	collector := stats.New(500)
	authr, err := auth.New(cfg.Web.Username, cfg.Web.Password)
	if err != nil {
		return nil, fmt.Errorf("初始化认证失败: %w", err)
	}
	scanner := device.New(cfg.Device.DHCPLeaseFiles, cfg.Device.ScanIntervalSeconds)

	ctx, cancel := context.WithCancel(context.Background())

	return &App{
		cfg:       cfg,
		route:     route.New(cfg.TProxy.FwMark, cfg.TProxy.RouteTable, cfg.TProxy.EnableIPv6),
		netfilter: netfilter.New(netfilter.Options{
			ListenPort:   cfg.TProxy.ListenPort,
			FwMark:       cfg.TProxy.FwMark,
			LANIface:     cfg.LANInterface,
			BypassCIDRs:  cfg.TProxy.BypassCIDRs,
			BypassCIDRs6: cfg.TProxy.BypassCIDRs6,
			EnableIPv6:   cfg.TProxy.EnableIPv6,
			BlockQUIC:    cfg.TProxy.BlockQUIC,
		}),
		relay:     relay.New(cfg, collector),
		scanner:   scanner,
		collector: collector,
		apiSrv:    api.New(cfg, authr, collector, scanner),
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// Run 启动所有子系统并阻塞,直到 Stop 被调用或发生致命错误。
func (a *App) Run() error {
	logger.Infof("启动 relay...")
	if err := a.relay.Start(); err != nil {
		return fmt.Errorf("启动 relay 失败: %w", err)
	}

	logger.Infof("配置策略路由...")
	if err := a.route.Setup(); err != nil {
		a.relay.Stop()
		return fmt.Errorf("配置路由失败: %w", err)
	}

	logger.Infof("加载 nftables TPROXY 规则...")
	if err := a.netfilter.Setup(); err != nil {
		a.route.Restore()
		a.relay.Stop()
		return fmt.Errorf("加载 nftables 失败: %w", err)
	}

	logger.Infof("启动设备扫描...")
	a.scanner.Start(a.ctx)

	logger.Infof("启动流量采样...")
	go a.collector.StartSampling(a.ctx)

	logger.Infof("启动 WebUI: http://%s", a.cfg.Web.Listen)
	errCh := make(chan error, 1)
	go func() {
		if err := a.apiSrv.Start(); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-a.ctx.Done():
		return nil
	case err := <-errCh:
		return fmt.Errorf("WebUI 异常退出: %w", err)
	}
}

// Stop 优雅停止并清理所有系统状态。
//
// 顺序很重要:先摘除 nftables 规则(停止新流量进入 relay),再清理策略路由,
// 最后停 relay 让存量连接收尾。清理失败会重试,避免规则残留——残留的 TPROXY
// 规则会把 TCP 流量劫持到已失效的 relay 端口,进而导致 DNS(DoT/DoH/TCP 查询)
// 与其他 TCP 服务不可用。
func (a *App) Stop() {
	logger.Infof("正在停止网关并清理系统状态...")
	a.cancel()
	if a.apiSrv != nil {
		a.apiSrv.Stop()
	}
	a.scanner.Stop()

	// 先删 nftables 规则,阻断新流量进入 relay。失败时重试。
	if err := retry(3, 300*time.Millisecond, a.netfilter.Restore); err != nil {
		logger.Errorf("清理 nftables 失败(已重试): %v", err)
		// 兜底:无条件强制删表,避免残留拖垮 DNS 等 TCP 服务。
		if ferr := a.netfilter.ForceClean(); ferr != nil {
			logger.Errorf("强制清理 nftables 仍失败: %v(请手动执行 lanproxy-gateway clean)", ferr)
		} else {
			logger.Infof("已通过强制清理移除 nftables 表")
		}
	}

	// 再清理策略路由。失败时重试并兜底强制清理。
	if err := retry(3, 300*time.Millisecond, a.route.Restore); err != nil {
		logger.Errorf("清理路由失败(已重试): %v", err)
		a.route.ForceClean()
		logger.Infof("已通过强制清理移除策略路由")
	}

	a.relay.Stop()
	logger.Infof("已停止。")
}

// retry 执行 fn,失败则重试 attempts 次(含首次),间隔 delay。
func retry(attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return err
}

// detectDefaultInterface 通过默认路由探测出口网卡。
func detectDefaultInterface() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
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
				return iface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("未找到可用网卡")
}
