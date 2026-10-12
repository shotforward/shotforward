// Package config loads and validates daemon process configuration.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	logLevelEnvironmentVariable        = "SHOTFORWARD_LOG_LEVEL"
	listenAddressEnvironmentVariable   = "SHOTFORWARD_LISTEN_ADDRESS"
	codexExecutableEnvironmentVariable = "SHOTFORWARD_CODEX_EXECUTABLE"
	pairingCodeEnvironmentVariable     = "SHOTFORWARD_PAIRING_CODE"
	cloudURLEnvironmentVariable        = "SHOTFORWARD_CLOUD_URL"
	cloudAPIURLEnvironmentVariable     = "SHOTFORWARD_CLOUD_API_URL"
	cloudWebURLEnvironmentVariable     = "SHOTFORWARD_CLOUD_WEB_URL"
	embeddedCodexEnvironmentVariable   = "SHOTFORWARD_EMBEDDED_CODEX_EXECUTABLE"
)

// LookupEnv matches os.LookupEnv and makes configuration loading deterministic
// in tests.
type LookupEnv func(key string) (string, bool)

// Config contains process-level daemon configuration.
//
// Product and project settings do not belong here. They will be loaded through
// their respective application services and repositories.
type Config struct {
	LogLevel                slog.Level
	ListenAddress           string
	CodexExecutable         string
	PairingCode             string
	CloudAPIURL             string
	CloudWebURL             string
	EmbeddedCodexExecutable string
	CloudProxyURL           string
	CloudProxyToken         string
}

// Default returns the daemon defaults used when no environment overrides exist.
func Default() Config {
	return Config{
		LogLevel:      slog.LevelInfo,
		ListenAddress: "127.0.0.1:0",
		CloudAPIURL:   "https://api.shotforward.com",
		CloudWebURL:   "https://shotforward.com",
	}
}

// Load reads process configuration from the environment and validates it.
func Load(lookupEnv LookupEnv) (Config, error) {
	cfg := Default()
	if lookupEnv == nil {
		return Config{}, fmt.Errorf("load config: environment lookup is required")
	}

	value, ok := lookupEnv(logLevelEnvironmentVariable)
	if !ok || strings.TrimSpace(value) == "" {
		return loadNetworkAndRuntime(cfg, lookupEnv)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(value))); err != nil {
		return Config{}, fmt.Errorf(
			"load config: %s must be one of debug, info, warn, or error: %w",
			logLevelEnvironmentVariable,
			err,
		)
	}
	cfg.LogLevel = level
	return loadNetworkAndRuntime(cfg, lookupEnv)
}

func loadNetworkAndRuntime(cfg Config, lookupEnv LookupEnv) (Config, error) {
	if value, ok := lookupEnv(listenAddressEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.ListenAddress = strings.TrimSpace(value)
	}
	if value, ok := lookupEnv(codexExecutableEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.CodexExecutable = strings.TrimSpace(value)
	}
	if value, ok := lookupEnv(pairingCodeEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.PairingCode = strings.ToUpper(strings.TrimSpace(value))
	}
	if value, ok := lookupEnv(cloudURLEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		legacyURL := strings.TrimRight(strings.TrimSpace(value), "/")
		cfg.CloudAPIURL, cfg.CloudWebURL = legacyURL, legacyURL
	}
	if value, ok := lookupEnv(cloudAPIURLEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.CloudAPIURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}
	if value, ok := lookupEnv(cloudWebURLEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.CloudWebURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}
	if value, ok := lookupEnv(embeddedCodexEnvironmentVariable); ok && strings.TrimSpace(value) != "" {
		cfg.EmbeddedCodexExecutable = strings.TrimSpace(value)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate prevents programmatic callers from bypassing the same process
// boundary checks applied to environment configuration.
func (cfg Config) Validate() error {
	if cfg.LogLevel != slog.LevelDebug && cfg.LogLevel != slog.LevelInfo && cfg.LogLevel != slog.LevelWarn && cfg.LogLevel != slog.LevelError {
		return fmt.Errorf("load config: %s must be one of debug, info, warn, or error", logLevelEnvironmentVariable)
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil || port == "" {
		return fmt.Errorf("load config: %s must be a loopback host and port", listenAddressEnvironmentVariable)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	portNumber, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("load config: %s must be a loopback host and valid port", listenAddressEnvironmentVariable)
	}
	if cfg.CodexExecutable != "" && (!filepath.IsAbs(cfg.CodexExecutable) || filepath.Clean(cfg.CodexExecutable) != cfg.CodexExecutable) {
		return fmt.Errorf("load config: %s must be a clean absolute path", codexExecutableEnvironmentVariable)
	}
	for variable, value := range map[string]string{
		cloudAPIURLEnvironmentVariable: cfg.CloudAPIURL,
		cloudWebURLEnvironmentVariable: cfg.CloudWebURL,
	} {
		parsedCloud, cloudErr := url.Parse(value)
		if cloudErr != nil || parsedCloud.Scheme != "https" || parsedCloud.Host == "" || parsedCloud.User != nil || parsedCloud.RawQuery != "" || parsedCloud.Fragment != "" {
			return fmt.Errorf("load config: %s must be an absolute HTTPS URL", variable)
		}
	}
	if cfg.EmbeddedCodexExecutable != "" && (!filepath.IsAbs(cfg.EmbeddedCodexExecutable) || filepath.Clean(cfg.EmbeddedCodexExecutable) != cfg.EmbeddedCodexExecutable) {
		return fmt.Errorf("load config: %s must be a clean absolute path", embeddedCodexEnvironmentVariable)
	}
	if cfg.PairingCode != "" && !validPairingCode(cfg.PairingCode) {
		return fmt.Errorf("load config: %s must contain exactly 6 ASCII letters or digits", pairingCodeEnvironmentVariable)
	}
	return nil
}

func validPairingCode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
