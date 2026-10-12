package shotforwardcloud

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// NewNetworkHTTPClient returns the cloud client used by the desktop daemon.
// The proxy file is shared with Codex Runtime settings and is read for every
// request so saving a new proxy takes effect without restarting ShotForward.
func NewNetworkHTTPClient(proxySettingsPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFromSettingsFile(proxySettingsPath, http.ProxyFromEnvironment)
	return &http.Client{Transport: transport, Timeout: 90 * time.Second}
}

func proxyFromSettingsFile(path string, fallback func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(request *http.Request) (*url.URL, error) {
		proxyURL, err := readLoopbackProxy(path)
		if err == nil && proxyURL != nil {
			return proxyURL, nil
		}
		if fallback == nil {
			return nil, nil
		}
		return fallback(request)
	}
}

func readLoopbackProxy(path string) (*url.URL, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored struct {
		ProxyURL string `json:"proxy_url"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	value := strings.TrimSpace(stored.ProxyURL)
	if value == "" {
		return nil, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" {
		return nil, errors.New("invalid local proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "socks5", "socks5h":
	default:
		return nil, errors.New("invalid local proxy scheme")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("local proxy must use a loopback address")
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Path = ""
	return parsed, nil
}
