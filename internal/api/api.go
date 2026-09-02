package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/lanproxy-gateway/internal/auth"
	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/device"
	"github.com/iflyelf/lanproxy-gateway/internal/logger"
	"github.com/iflyelf/lanproxy-gateway/internal/stats"
	"github.com/iflyelf/lanproxy-gateway/internal/web"
)

// Server 提供 WebUI 与 REST API。
type Server struct {
	cfg       *config.Config
	auth      *auth.Authenticator
	collector *stats.Collector
	scanner   *device.Scanner
	startedAt time.Time
	httpSrv   *http.Server
}

// New 创建 API 服务器。
func New(cfg *config.Config, a *auth.Authenticator, c *stats.Collector, s *device.Scanner) *Server {
	return &Server{
		cfg:       cfg,
		auth:      a,
		collector: c,
		scanner:   s,
		startedAt: time.Now(),
	}
}

// Start 在配置的地址上启动 HTTP 服务(阻塞前返回)。
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// 公共接口:登录/登出。
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)

	// 受保护接口。
	mux.Handle("/api/status", s.auth.Middleware(http.HandlerFunc(s.handleStatus)))
	mux.Handle("/api/devices", s.auth.Middleware(http.HandlerFunc(s.handleDevices)))
	mux.Handle("/api/device/remark", s.auth.Middleware(http.HandlerFunc(s.handleSetRemark)))
	mux.Handle("/api/connections", s.auth.Middleware(http.HandlerFunc(s.handleConnections)))
	mux.Handle("/api/traffic", s.auth.Middleware(http.HandlerFunc(s.handleTraffic)))
	mux.Handle("/api/logs", s.auth.Middleware(http.HandlerFunc(s.handleLogs)))

	// 静态前端。防缓存头避免更新后浏览器用旧缓存导致新旧不匹配空白页。
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		return err
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", noCache(fileServer))

	s.httpSrv = &http.Server{
		Addr:    s.cfg.Web.Listen,
		Handler: mux,
	}
	return s.httpSrv.ListenAndServe()
}

// Stop 优雅关闭 HTTP 服务。
func (s *Server) Stop() error {
	if s.httpSrv != nil {
		return s.httpSrv.Close()
	}
	return nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	token, ok := s.auth.Login(req.Username, req.Password)
	if !ok {
		http.Error(w, "用户名或密码错误", http.StatusUnauthorized)
		return
	}
	s.auth.SetCookie(w, token)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := s.auth.TokenFromRequest(r)
	if token != "" {
		s.auth.Logout(token)
	}
	s.auth.ClearCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

// StatusResponse 是 /api/status 的返回结构。
type StatusResponse struct {
	UptimeSeconds  int64        `json:"uptime_seconds"`
	Upstream       string       `json:"upstream"`
	UpstreamType   string       `json:"upstream_type"`
	LANInterface   string       `json:"lan_interface"`
	FallbackDirect bool         `json:"fallback_direct"`
	BlockQUIC      bool         `json:"block_quic"`
	EnableIPv6     bool         `json:"enable_ipv6"`
	MaxConnections int          `json:"max_connections"`
	Totals         stats.Totals `json:"totals"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		UptimeSeconds:  int64(time.Since(s.startedAt).Seconds()),
		Upstream:       s.cfg.Upstream.Address,
		UpstreamType:   s.cfg.Upstream.Type,
		LANInterface:   s.cfg.LANInterface,
		FallbackDirect: s.cfg.TProxy.FallbackDirect,
		BlockQUIC:      s.cfg.TProxy.BlockQUIC,
		EnableIPv6:     s.cfg.TProxy.EnableIPv6,
		MaxConnections: s.cfg.TProxy.MaxConnections,
		Totals:         s.collector.Totals(),
	}
	writeJSON(w, resp)
}

// DeviceView 是设备列表条目,附带主机名与自定义备注。
type DeviceView struct {
	stats.DeviceStat
	Hostname string `json:"hostname"`
	Remark   string `json:"remark"`
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devs := s.collector.Devices()
	out := make([]DeviceView, 0, len(devs))
	for _, d := range devs {
		out = append(out, DeviceView{
			DeviceStat: d,
			Hostname:   s.scanner.Hostname(d.IP),
			Remark:     s.scanner.Remark(d.IP),
		})
	}
	writeJSON(w, out)
}

// handleSetRemark 设置设备的自定义备注。请求体: {"ip": "...", "remark": "..."}
func (s *Server) handleSetRemark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IP     string `json:"ip"`
		Remark string `json:"remark"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" {
		http.Error(w, "ip is required", http.StatusBadRequest)
		return
	}
	// 限制备注长度,避免滥用
	req.Remark = strings.TrimSpace(req.Remark)
	if len([]rune(req.Remark)) > 64 {
		req.Remark = string([]rune(req.Remark)[:64])
	}
	s.scanner.SetRemark(req.IP, req.Remark)
	writeJSON(w, map[string]any{"ok": true, "ip": req.IP, "remark": req.Remark})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	recent := s.collector.Recent(200)
	writeJSON(w, recent)
}

func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	samples := s.collector.TrafficSamples()
	writeJSON(w, samples)
}

// LogsResponse 是 /api/logs 的返回结构。
type LogsResponse struct {
	File    string   `json:"file"`     // 当前日志文件路径
	Enabled bool     `json:"enabled"`  // 是否启用了文件日志
	Lines   []string `json:"lines"`    // 日志行(从旧到新)
	Level   string   `json:"level"`    // 当前配置的级别
}

// handleLogs 返回当天日志文件的尾部内容,便于远程排查。
// 查询参数:
//   lines  - 返回行数上限(默认 200,最大 2000)
//   level  - 仅返回包含该级别标记的行(DEBUG/INFO/WARN/ERROR)
//   q      - 关键字过滤(不区分大小写)
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 2000 {
		limit = 2000
	}

	// 先多读一些,过滤后再截断到 limit。
	readN := limit
	levelFilter := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("level")))
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if levelFilter != "" || keyword != "" {
		readN = limit * 10
		if readN > 20000 {
			readN = 20000
		}
	}

	lines, err := logger.Tail(readN)
	if err != nil {
		http.Error(w, "读取日志失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if levelFilter != "" || keyword != "" {
		filtered := make([]string, 0, len(lines))
		for _, ln := range lines {
			if levelFilter != "" && !strings.Contains(ln, "["+levelFilter+"]") {
				continue
			}
			if keyword != "" && !strings.Contains(strings.ToLower(ln), keyword) {
				continue
			}
			filtered = append(filtered, ln)
		}
		lines = filtered
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	file := logger.CurrentFile()
	writeJSON(w, LogsResponse{
		File:    file,
		Enabled: file != "",
		Lines:   lines,
		Level:   s.cfg.Log.Level,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// noCache 为静态资源添加防缓存头,避免二进制更新后浏览器用旧缓存导致 HTML/JS/CSS 版本不匹配。
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		h.ServeHTTP(w, r)
	})
}
