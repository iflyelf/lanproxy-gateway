package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/iflyelf/lanproxy-gateway/internal/auth"
	"github.com/iflyelf/lanproxy-gateway/internal/config"
	"github.com/iflyelf/lanproxy-gateway/internal/device"
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
	mux.Handle("/api/connections", s.auth.Middleware(http.HandlerFunc(s.handleConnections)))

	// 静态前端。
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

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
	UptimeSeconds int64        `json:"uptime_seconds"`
	Upstream      string       `json:"upstream"`
	UpstreamType  string       `json:"upstream_type"`
	LANInterface  string       `json:"lan_interface"`
	Totals        stats.Totals `json:"totals"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Upstream:      s.cfg.Upstream.Address,
		UpstreamType:  s.cfg.Upstream.Type,
		LANInterface:  s.cfg.LANInterface,
		Totals:        s.collector.Totals(),
	}
	writeJSON(w, resp)
}

// DeviceView 是设备列表条目,附带主机名。
type DeviceView struct {
	stats.DeviceStat
	Hostname string `json:"hostname"`
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devs := s.collector.Devices()
	out := make([]DeviceView, 0, len(devs))
	for _, d := range devs {
		out = append(out, DeviceView{
			DeviceStat: d,
			Hostname:   s.scanner.Hostname(d.IP),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	recent := s.collector.Recent(200)
	writeJSON(w, recent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
