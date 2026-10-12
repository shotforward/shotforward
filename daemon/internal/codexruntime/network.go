package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

var proxyEnvironmentNames = map[string]struct{}{
	"http_proxy":  {},
	"https_proxy": {},
	"all_proxy":   {},
	"no_proxy":    {},
}

func normalizeProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", agentapp.ErrInvalidRuntimeNetworkSettings
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "socks5", "socks5h":
	default:
		return "", agentapp.ErrInvalidRuntimeNetworkSettings
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", agentapp.ErrInvalidRuntimeNetworkSettings
		}
	}
	if parsed.Port() == "" {
		return "", agentapp.ErrInvalidRuntimeNetworkSettings
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Path = ""
	return parsed.String(), nil
}

func readNetworkSettings(path string) (agentapp.RuntimeNetworkSettings, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return agentapp.RuntimeNetworkSettings{}, nil
	}
	if err != nil {
		return agentapp.RuntimeNetworkSettings{}, err
	}
	var stored struct {
		ProxyURL string `json:"proxy_url"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return agentapp.RuntimeNetworkSettings{}, err
	}
	proxyURL, err := normalizeProxyURL(stored.ProxyURL)
	if err != nil {
		return agentapp.RuntimeNetworkSettings{}, err
	}
	return agentapp.RuntimeNetworkSettings{ProxyURL: proxyURL}, nil
}

func writeNetworkSettings(path string, settings agentapp.RuntimeNetworkSettings) error {
	if path == "" {
		return nil
	}
	encoded, err := json.Marshal(struct {
		ProxyURL string `json:"proxy_url"`
	}{ProxyURL: settings.ProxyURL})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func environmentWithProxy(environment []string, proxyURL string) []string {
	if proxyURL == "" {
		return append([]string(nil), environment...)
	}
	result := make([]string, 0, len(environment)+7)
	noProxyValues := []string{"127.0.0.1", "localhost", "::1"}
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			result = append(result, entry)
			continue
		}
		lower := strings.ToLower(name)
		if lower == "no_proxy" {
			noProxyValues = append(noProxyValues, strings.Split(value, ",")...)
		}
		if _, managed := proxyEnvironmentNames[lower]; managed {
			continue
		}
		result = append(result, entry)
	}
	noProxy := strings.Join(uniqueNonEmpty(noProxyValues), ",")
	result = append(result, "NO_PROXY="+noProxy, "no_proxy="+noProxy)
	result = append(result,
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "ALL_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL, "all_proxy="+proxyURL,
	)
	return result
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Supervisor) NetworkSettings(context.Context) (agentapp.RuntimeNetworkSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.networkSettings, nil
}

func (s *Supervisor) ConfigureNetwork(ctx context.Context, settings agentapp.RuntimeNetworkSettings) error {
	if ctx == nil {
		return agentapp.ErrInvalidRuntimeNetworkSettings
	}
	proxyURL, err := normalizeProxyURL(settings.ProxyURL)
	if err != nil {
		return err
	}
	settings.ProxyURL = proxyURL
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errSupervisorClosed
	}
	if err := writeNetworkSettings(s.options.NetworkSettingsPath, settings); err != nil {
		return err
	}
	closeErr := s.stop(ctx, false)
	s.mu.Lock()
	s.options.Environment = environmentWithProxy(s.baseEnvironment, proxyURL)
	s.networkSettings = settings
	s.snapshot = agentapp.RuntimeSnapshot{
		Health:         agentapp.RuntimeDisconnected,
		FailureCode:    "codex_restarting",
		FailureSummary: "Codex Runtime is restarting with updated network settings",
	}
	// A cancelled reset must not permit a fresh generation while cleanup of the
	// old one is still outstanding. Retrying ConfigureNetwork can finish it.
	s.stopping = closeErr != nil
	s.mu.Unlock()
	return closeErr
}
