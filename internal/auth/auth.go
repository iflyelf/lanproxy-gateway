package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Authenticator 提供基于用户名/密码的登录与基于 cookie 的会话管理。
type Authenticator struct {
	username     string
	passwordHash []byte

	mu       sync.RWMutex
	sessions map[string]time.Time // token -> 过期时间

	ttl time.Duration
}

const cookieName = "lpg_session"

// New 创建认证器。password 若以 $2 开头视为 bcrypt 哈希,否则按明文处理并即时哈希。
func New(username, password string) (*Authenticator, error) {
	var hash []byte
	if strings.HasPrefix(password, "$2") {
		hash = []byte(password)
	} else {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		hash = h
	}
	return &Authenticator{
		username:     username,
		passwordHash: hash,
		sessions:     make(map[string]time.Time),
		ttl:          12 * time.Hour,
	}, nil
}

// Login 校验凭据,成功则签发会话 token。
func (a *Authenticator) Login(username, password string) (string, bool) {
	if subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) != 1 {
		return "", false
	}
	if err := bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)); err != nil {
		return "", false
	}
	token := randomToken()
	a.mu.Lock()
	a.sessions[token] = time.Now().Add(a.ttl)
	a.mu.Unlock()
	return token, true
}

// valid 判断 token 是否有效,并清理过期项。
func (a *Authenticator) valid(token string) bool {
	a.mu.RLock()
	exp, ok := a.sessions[token]
	a.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		a.mu.Lock()
		delete(a.sessions, token)
		a.mu.Unlock()
		return false
	}
	return true
}

// Logout 使 token 失效。
func (a *Authenticator) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// SetCookie 在响应中写入会话 cookie。
func (a *Authenticator) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

// ClearCookie 清除会话 cookie。
func (a *Authenticator) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// TokenFromRequest 从请求 cookie 中提取 token。
func (a *Authenticator) TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// Middleware 保护需要登录的接口;未认证返回 401。
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := a.TokenFromRequest(r)
		if token == "" || !a.valid(token) {
			http.Error(w, "未认证", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsAuthenticated 判断请求是否已登录。
func (a *Authenticator) IsAuthenticated(r *http.Request) bool {
	token := a.TokenFromRequest(r)
	return token != "" && a.valid(token)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
