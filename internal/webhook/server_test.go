package webhook

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRequireAPIToken(t *testing.T) {
	server := New(Config{APIToken: "test-token"})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := server.requireAPIToken(next)

	tests := []struct {
		name       string
		path       string
		authorizer func(*http.Request)
		want       int
	}{
		{name: "missing", path: "/api/transcode/tasks", want: http.StatusUnauthorized},
		{name: "bearer", path: "/api/transcode/tasks", authorizer: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer test-token")
		}, want: http.StatusNoContent},
		{name: "api key", path: "/api/transcode/tasks", authorizer: func(r *http.Request) {
			r.Header.Set("X-API-Key", "test-token")
		}, want: http.StatusNoContent},
		{name: "health is public", path: "/healthz", want: http.StatusNoContent},
		{name: "webhook is public", path: "/webhook", want: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authorizer != nil {
				tt.authorizer(req)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d", response.Code, tt.want)
			}
		})
	}
}

func TestGetFullPathStaysInsideInputDirectory(t *testing.T) {
	inputDir := t.TempDir()
	insideDir := filepath.Join(inputDir, "room")
	if err := os.Mkdir(insideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	insidePath := filepath.Join(insideDir, "recording.flv")
	if err := os.WriteFile(insidePath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := New(Config{InputDir: inputDir})
	got, err := server.GetFullPath("room/recording.flv")
	if err != nil {
		t.Fatalf("GetFullPath(valid) error = %v", err)
	}
	want, err := filepath.EvalSymlinks(insidePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GetFullPath(valid) = %q, want %q", got, want)
	}

	outsideName := filepath.Base(inputDir) + "-outside.flv"
	outsidePath := filepath.Join(filepath.Dir(inputDir), outsideName)
	if err := os.WriteFile(outsidePath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outsidePath) })

	for _, path := range []string{"../" + outsideName, "room/../../" + outsideName, outsidePath, "", "missing.flv"} {
		if _, err := server.GetFullPath(path); err == nil {
			t.Errorf("GetFullPath(%q) unexpectedly succeeded", path)
		}
	}
}

func TestWebhookOnlyAcceptsLoopbackClients(t *testing.T) {
	server := New(Config{})

	for _, tt := range []struct {
		name       string
		remoteAddr string
		want       int
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:12345", want: http.StatusNoContent},
		{name: "ipv6 loopback", remoteAddr: "[::1]:12345", want: http.StatusNoContent},
		{name: "lan client", remoteAddr: "192.168.1.10:12345", want: http.StatusForbidden},
		{name: "malformed", remoteAddr: "localhost", want: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
			req.RemoteAddr = tt.remoteAddr
			response := httptest.NewRecorder()
			server.handleWebhook(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d", response.Code, tt.want)
			}
		})
	}
}
