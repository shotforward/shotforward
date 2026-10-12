package shotforwardcloud

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestProxyFromSettingsFileReloadsSavedProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-network.json")
	fallbackURL, _ := url.Parse("http://127.0.0.1:7000")
	proxy := proxyFromSettingsFile(path, func(*http.Request) (*url.URL, error) { return fallbackURL, nil })
	request, _ := http.NewRequest(http.MethodGet, "https://api.shotforward.com/v1/me", nil)

	got, err := proxy(request)
	if err != nil || got.String() != fallbackURL.String() {
		t.Fatalf("missing settings proxy = %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{\"proxy_url\":\"http://127.0.0.1:7890\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = proxy(request)
	if err != nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("saved settings proxy = %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{\"proxy_url\":\"socks5://localhost:1080\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = proxy(request)
	if err != nil || got.String() != "socks5://localhost:1080" {
		t.Fatalf("updated settings proxy = %v, %v", got, err)
	}
}

func TestProxyFromSettingsFileRejectsNonLoopbackAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-network.json")
	if err := os.WriteFile(path, []byte("{\"proxy_url\":\"http://proxy.example:8080\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallbackURL, _ := url.Parse("http://127.0.0.1:7000")
	proxy := proxyFromSettingsFile(path, func(*http.Request) (*url.URL, error) { return fallbackURL, nil })
	request, _ := http.NewRequest(http.MethodGet, "https://api.shotforward.com/v1/me", nil)
	got, err := proxy(request)
	if err != nil || got.String() != fallbackURL.String() {
		t.Fatalf("invalid settings fallback = %v, %v", got, err)
	}
}

func TestProxyFromSettingsFileSupportsExplicitDirectConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-network.json")
	if err := os.WriteFile(path, []byte("{\"proxy_url\":\"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := proxyFromSettingsFile(path, func(*http.Request) (*url.URL, error) { return nil, nil })
	request, _ := http.NewRequest(http.MethodGet, "https://api.shotforward.com/v1/me", nil)
	got, err := proxy(request)
	if err != nil || got != nil {
		t.Fatalf("direct connection proxy = %v, %v", got, err)
	}
}
