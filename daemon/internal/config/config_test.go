package config

import (
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

func TestLoadUsesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.ListenAddress != "127.0.0.1:0" {
		t.Fatalf("ListenAddress = %q, want random IPv4 loopback port", cfg.ListenAddress)
	}
	if cfg.CloudAPIURL != "https://api.shotforward.com" || cfg.CloudWebURL != "https://shotforward.com" {
		t.Fatalf("Cloud URLs = %q, %q", cfg.CloudAPIURL, cfg.CloudWebURL)
	}
	if cfg.CodexExecutable != "" || cfg.PairingCode != "" {
		t.Fatalf("CodexExecutable = %q, want empty auto-discovery value", cfg.CodexExecutable)
	}
}

func TestLoadReadsSeparateCloudAPIAndWebURLs(t *testing.T) {
	t.Parallel()
	cfg, err := Load(func(key string) (string, bool) {
		values := map[string]string{
			cloudAPIURLEnvironmentVariable: "https://api.example.test/",
			cloudWebURLEnvironmentVariable: "https://www.example.test/",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloudAPIURL != "https://api.example.test" || cfg.CloudWebURL != "https://www.example.test" {
		t.Fatalf("Cloud URLs = %q, %q", cfg.CloudAPIURL, cfg.CloudWebURL)
	}
}

func TestLoadReadsLocalHTTPAndRuntimeConfiguration(t *testing.T) {
	t.Parallel()

	executable := "/opt/shotforward/codex"
	if runtime.GOOS == "windows" {
		executable = `C:\ShotForward\codex.exe`
	}
	cfg, err := Load(func(key string) (string, bool) {
		values := map[string]string{
			listenAddressEnvironmentVariable:   "[::1]:43210",
			codexExecutableEnvironmentVariable: executable,
			pairingCodeEnvironmentVariable:     " ab12cd ",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddress != "[::1]:43210" || cfg.CodexExecutable != executable || cfg.PairingCode != "AB12CD" {
		t.Fatalf("Load() = %#v", cfg)
	}
}

func TestLoadRejectsInvalidPairingCode(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"SHORT", "TOO-LONG", "秘密12"} {
		_, err := Load(func(key string) (string, bool) {
			if key == pairingCodeEnvironmentVariable {
				return value, true
			}
			return "", false
		})
		if err == nil || !strings.Contains(err.Error(), pairingCodeEnvironmentVariable) {
			t.Fatalf("Load(%q) error = %v, want pairing code validation", value, err)
		}
	}
}

func TestLoadRejectsNonLoopbackListenAddress(t *testing.T) {
	t.Parallel()

	_, err := Load(func(key string) (string, bool) {
		if key == listenAddressEnvironmentVariable {
			return "0.0.0.0:8080", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), listenAddressEnvironmentVariable) {
		t.Fatalf("Load() error = %v, want loopback validation error", err)
	}
}

func TestLoadRejectsRelativeCodexExecutable(t *testing.T) {
	t.Parallel()

	_, err := Load(func(key string) (string, bool) {
		if key == codexExecutableEnvironmentVariable {
			return "bin/codex", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), codexExecutableEnvironmentVariable) {
		t.Fatalf("Load() error = %v, want executable validation error", err)
	}
}

func TestLoadReadsLogLevel(t *testing.T) {
	t.Parallel()

	cfg, err := Load(func(key string) (string, bool) {
		if key == logLevelEnvironmentVariable {
			return " debug ", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Parallel()

	_, err := Load(func(string) (string, bool) { return "verbose", true })
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), logLevelEnvironmentVariable) {
		t.Fatalf("Load() error = %q, want environment variable name", err)
	}
}

func TestLoadRequiresEnvironmentLookup(t *testing.T) {
	t.Parallel()

	if _, err := Load(nil); err == nil {
		t.Fatal("Load(nil) error = nil, want an error")
	}
}

func TestConfigValidateRejectsProgrammaticWildcard(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.ListenAddress = ":0"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), listenAddressEnvironmentVariable) {
		t.Fatalf("Validate() error = %v, want loopback validation error", err)
	}
}
