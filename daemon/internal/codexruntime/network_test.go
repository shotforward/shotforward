package codexruntime

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

func TestNormalizeProxyURLAcceptsOnlyLoopbackHTTPAndSOCKS(t *testing.T) {
	for _, value := range []string{
		"http://127.0.0.1:7890",
		"http://localhost:8080",
		"socks5://[::1]:1080",
		"socks5h://127.0.0.1:1080",
	} {
		if _, err := normalizeProxyURL(value); err != nil {
			t.Fatalf("normalizeProxyURL(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{
		"https://proxy.example.com:443",
		"http://127.0.0.1",
		"http://user:secret@127.0.0.1:7890",
		"http://127.0.0.1:7890/path",
		"ftp://127.0.0.1:21",
	} {
		if _, err := normalizeProxyURL(value); !errors.Is(err, agentapp.ErrInvalidRuntimeNetworkSettings) {
			t.Fatalf("normalizeProxyURL(%q) error = %v, want invalid settings", value, err)
		}
	}
}

func TestEnvironmentWithProxyPreservesLoopbackBypass(t *testing.T) {
	environment := environmentWithProxy([]string{
		"PATH=/bin",
		"HTTPS_PROXY=http://old.example:8080",
		"NO_PROXY=studio.internal,localhost",
	}, "http://127.0.0.1:7890")
	for _, expected := range []string{
		"PATH=/bin",
		"HTTP_PROXY=http://127.0.0.1:7890",
		"HTTPS_PROXY=http://127.0.0.1:7890",
		"ALL_PROXY=http://127.0.0.1:7890",
		"NO_PROXY=127.0.0.1,localhost,::1,studio.internal",
	} {
		if !slices.Contains(environment, expected) {
			t.Fatalf("environment does not contain %q: %#v", expected, environment)
		}
	}
}

func TestEnvironmentWithoutOverridePreservesSystemProxy(t *testing.T) {
	base := []string{"PATH=/bin", "HTTPS_PROXY=http://system.proxy:8080"}
	got := environmentWithProxy(base, "")
	if !slices.Equal(got, base) {
		t.Fatalf("environmentWithProxy(empty) = %#v, want %#v", got, base)
	}
}

func TestNetworkSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-network.json")
	want := agentapp.RuntimeNetworkSettings{ProxyURL: "socks5://127.0.0.1:1080"}
	if err := writeNetworkSettings(path, want); err != nil {
		t.Fatalf("writeNetworkSettings() error = %v", err)
	}
	got, err := readNetworkSettings(path)
	if err != nil {
		t.Fatalf("readNetworkSettings() error = %v", err)
	}
	if got != want {
		t.Fatalf("readNetworkSettings() = %#v, want %#v", got, want)
	}
}

func TestConfigureNetworkPersistsAndRestartsTheSupervisor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-network.json")
	base := []string{"PATH=/bin", "HTTPS_PROXY=http://system.proxy:8080"}
	supervisor := &Supervisor{
		options:         Options{NetworkSettingsPath: path, Environment: append([]string(nil), base...)},
		baseEnvironment: append([]string(nil), base...),
		activeTurns:     make(map[string]string),
	}
	settings := agentapp.RuntimeNetworkSettings{ProxyURL: "http://127.0.0.1:7890"}
	if err := supervisor.ConfigureNetwork(context.Background(), settings); err != nil {
		t.Fatalf("ConfigureNetwork() error = %v", err)
	}
	if supervisor.networkSettings != settings {
		t.Fatalf("networkSettings = %#v, want %#v", supervisor.networkSettings, settings)
	}
	if !slices.Contains(supervisor.options.Environment, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("proxy was not applied: %#v", supervisor.options.Environment)
	}
	stored, err := readNetworkSettings(path)
	if err != nil || stored != settings {
		t.Fatalf("stored settings = %#v, err=%v", stored, err)
	}
	if supervisor.snapshot.FailureCode != "codex_restarting" || supervisor.stopping {
		t.Fatalf("restart state = %#v stopping=%v", supervisor.snapshot, supervisor.stopping)
	}
}
