// Package webauth provides single-user password and cookie authentication.
package webauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	cookieName      = "server_session"
	sessionLifetime = 7 * 24 * time.Hour
	maxFailures     = 5
	failureWindow   = 5 * time.Minute
)

type authFile struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	CreatedAt time.Time `json:"created_at"`
}

type session struct {
	ExpiresAt time.Time
}

type failures struct {
	Count       int
	WindowStart time.Time
}

type Manager struct {
	path     string
	mu       sync.Mutex
	password string
	username string
	sessions map[string]session
	failures map[string]failures
}

func New(path string) (*Manager, error) {
	m := &Manager{path: path, sessions: make(map[string]session), failures: make(map[string]failures)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var stored authFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	if stored.Password == "" {
		return nil, errors.New("authentication file has no password")
	}
	if stored.Username == "" {
		stored.Username = "admin"
	}
	m.username = stored.Username
	m.password = stored.Password
	return m, nil
}

func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/status", m.handleStatus)
	mux.HandleFunc("/api/auth/register", m.handleRegister)
	mux.HandleFunc("/api/auth/login", m.handleLogin)
	mux.HandleFunc("/api/auth/logout", m.handleLogout)
	mux.HandleFunc("/api/auth/profile", m.handleProfile)
	mux.HandleFunc("/api/auth/change-credentials", m.handleChangeCredentials)
}

func (m *Manager) Middleware(next http.Handler, apiToken, webhookPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicAuth := r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/register" || r.URL.Path == "/api/auth/login"
		if r.URL.Path == "/healthz" || r.URL.Path == webhookPath || r.URL.Path == "/login" || publicAuth {
			next.ServeHTTP(w, r)
			return
		}
		if apiToken != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if provided == "" {
				provided = r.Header.Get("X-API-Key")
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(apiToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		if m.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	})
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	m.mu.Lock()
	registered := m.password != ""
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"registered": registered, "authenticated": m.authenticated(r)})
}

func (m *Manager) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	credentials, ok := readCredentials(w, r)
	if !ok {
		return
	}
	if !validUsername(credentials.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名需要 3-32 个字符，只能包含字母、数字、点、横线和下划线"})
		return
	}
	if len(credentials.Password) < 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码至少需要 10 个字符"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.password != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "管理员已注册"})
		return
	}
	if err := m.saveLocked(credentials.Username, credentials.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法发布认证配置"})
		return
	}
	m.username = credentials.Username
	m.password = credentials.Password
	m.issueSessionLocked(w)
	writeJSON(w, http.StatusCreated, map[string]bool{"success": true})
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ip := remoteIP(r)
	m.mu.Lock()
	if m.rateLimitedLocked(ip) {
		m.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "登录失败次数过多，请稍后再试"})
		return
	}
	password := m.password
	m.mu.Unlock()
	if password == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "请先创建管理员密码"})
		return
	}
	credentials, ok := readCredentials(w, r)
	if !ok {
		return
	}
	m.mu.Lock()
	username := m.username
	m.mu.Unlock()
	usernameMatch := subtle.ConstantTimeCompare([]byte(credentials.Username), []byte(username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(credentials.Password), []byte(password)) == 1
	if !usernameMatch || !passwordMatch {
		m.mu.Lock()
		m.recordFailureLocked(ip)
		m.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码错误"})
		return
	}
	m.mu.Lock()
	delete(m.failures, ip)
	m.issueSessionLocked(w)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (m *Manager) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	m.mu.Lock()
	username := m.username
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

func (m *Manager) handleChangeCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		Username        string `json:"username"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	if !validUsername(request.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名需要 3-32 个字符，只能包含字母、数字、点、横线和下划线"})
		return
	}
	if request.NewPassword != "" && len(request.NewPassword) < 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "新密码至少需要 10 个字符"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(m.password), []byte(request.CurrentPassword)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "当前密码错误"})
		return
	}
	password := m.password
	if request.NewPassword != "" {
		password = request.NewPassword
	}
	if err := m.saveLocked(request.Username, password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法保存新凭据"})
		return
	}
	m.username = request.Username
	m.password = password
	m.sessions = make(map[string]session)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (m *Manager) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[cookie.Value]
	if !ok || time.Now().After(s.ExpiresAt) {
		delete(m.sessions, cookie.Value)
		return false
	}
	return true
}

func (m *Manager) issueSessionLocked(w http.ResponseWriter) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	expires := time.Now().Add(sessionLifetime)
	m.sessions[token] = session{ExpiresAt: expires}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: expires, MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) rateLimitedLocked(ip string) bool {
	f := m.failures[ip]
	return f.Count >= maxFailures && time.Since(f.WindowStart) < failureWindow
}

func (m *Manager) recordFailureLocked(ip string) {
	f := m.failures[ip]
	if f.WindowStart.IsZero() || time.Since(f.WindowStart) >= failureWindow {
		f = failures{WindowStart: time.Now()}
	}
	f.Count++
	m.failures[ip] = f
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var request credentials
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return credentials{}, false
	}
	return request, true
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

func validUsername(username string) bool { return usernamePattern.MatchString(username) }

func (m *Manager) saveLocked(username, password string) error {
	data, err := json.MarshalIndent(authFile{Username: username, Password: password, CreatedAt: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	tempPath := m.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, m.path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
