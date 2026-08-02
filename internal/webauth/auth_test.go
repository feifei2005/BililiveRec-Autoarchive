package webauth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSingleUserLifecycle(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "server-auth.json")
	manager, err := New(authPath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.RegisterRoutes(mux)
	mux.HandleFunc("/api/app/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := manager.Middleware(mux, "api-token", "/webhook")

	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	if got := request(http.MethodGet, "/api/app/test", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("protected API before registration = %d", got)
	}
	registered := request(http.MethodPost, "/api/auth/register", `{"username":"admin","password":"first-password"}`, nil)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register = %d: %s", registered.Code, registered.Body.String())
	}
	cookies := registered.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || !cookies[0].HttpOnly {
		t.Fatalf("registration cookie = %#v", cookies)
	}
	oldSession := cookies[0]
	if got := request(http.MethodGet, "/api/app/test", "", oldSession).Code; got != http.StatusNoContent {
		t.Fatalf("authenticated API = %d", got)
	}
	if got := request(http.MethodPost, "/api/auth/change-credentials", `{"username":"owner","currentPassword":"first-password","newPassword":"second-password"}`, oldSession).Code; got != http.StatusOK {
		t.Fatalf("change credentials = %d", got)
	}
	if got := request(http.MethodGet, "/api/app/test", "", oldSession).Code; got != http.StatusUnauthorized {
		t.Fatalf("old session after credential change = %d", got)
	}
	if got := request(http.MethodPost, "/api/auth/login", `{"username":"admin","password":"first-password"}`, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("old credentials = %d", got)
	}
	login := request(http.MethodPost, "/api/auth/login", `{"username":"owner","password":"second-password"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("new credentials = %d: %s", login.Code, login.Body.String())
	}
	newSession := login.Result().Cookies()[0]
	if got := request(http.MethodPost, "/api/auth/logout", "", newSession).Code; got != http.StatusOK {
		t.Fatalf("logout = %d", got)
	}
	if got := request(http.MethodGet, "/api/app/test", "", newSession).Code; got != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d", got)
	}
	if got := request(http.MethodPost, "/webhook", `{}`, nil).Code; got != http.StatusNoContent {
		t.Fatalf("public webhook = %d", got)
	}

	reloaded, err := New(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.username != "owner" || reloaded.password != "second-password" {
		t.Fatalf("persisted auth = username %q, password %q", reloaded.username, reloaded.password)
	}
}
