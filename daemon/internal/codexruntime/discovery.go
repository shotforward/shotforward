// Package codexruntime adapts the local Codex app-server to the Agent Runtime port.
package codexruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrNotInstalled = errors.New("Codex executable not installed")

// DiscoverExecutable resolves a user-selected executable before PATH and known
// per-user installation locations. It never invokes a shell.
func DiscoverExecutable(configured string) (string, error) {
	home, _ := os.UserHomeDir()
	return discoverExecutable(configured, runtime.GOOS, home, os.Getenv("LOCALAPPDATA"), exec.LookPath, os.Stat)
}

type lookPathFunc func(string) (string, error)
type statFunc func(string) (os.FileInfo, error)

func discoverExecutable(configured, goos, home, localAppData string, lookPath lookPathFunc, stat statFunc) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return validateExecutable(configured, goos, stat)
	}
	if found, err := lookPath("codex"); err == nil {
		if resolved, validateErr := validateExecutable(found, goos, stat); validateErr == nil {
			return resolved, nil
		}
	}

	var candidates []string
	switch goos {
	case "darwin":
		candidates = []string{
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, ".npm-global", "bin", "codex"),
			"/opt/homebrew/bin/codex",
			"/usr/local/bin/codex",
		}
	case "linux":
		candidates = []string{
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, ".npm-global", "bin", "codex"),
			"/usr/local/bin/codex",
			"/usr/bin/codex",
		}
	case "windows":
		if strings.TrimSpace(localAppData) != "" {
			candidates = []string{
				filepath.Join(localAppData, "Programs", "codex", "codex.exe"),
				filepath.Join(localAppData, "npm", "codex.cmd"),
			}
		}
	default:
		return "", fmt.Errorf("%w: unsupported operating system %q", ErrNotInstalled, goos)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if resolved, err := validateExecutable(candidate, goos, stat); err == nil {
			return resolved, nil
		}
	}
	return "", ErrNotInstalled
}

func validateExecutable(value, goos string, stat statFunc) (string, error) {
	if strings.TrimSpace(value) != value || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid Codex executable path")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve Codex executable: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := stat(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotInstalled, err)
	}
	if !info.Mode().IsRegular() || (goos != "windows" && info.Mode().Perm()&0o111 == 0) {
		return "", fmt.Errorf("%w: path is not an executable file", ErrNotInstalled)
	}
	return absolute, nil
}
