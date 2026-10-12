package codexruntime

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiscoverExecutableOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configured := filepath.Join(root, "configured-codex")
	pathCodex := filepath.Join(root, "path-codex")
	if err := os.WriteFile(configured, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathCodex, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	lookPath := func(string) (string, error) { return pathCodex, nil }
	got, err := discoverExecutable(configured, runtime.GOOS, root, "", lookPath, os.Stat)
	if err != nil || got != configured {
		t.Fatalf("discover configured = %q, %v", got, err)
	}
	got, err = discoverExecutable("", runtime.GOOS, root, "", lookPath, os.Stat)
	if err != nil || got != pathCodex {
		t.Fatalf("discover PATH = %q, %v", got, err)
	}
}

func TestDiscoverExecutableAcceptsWindowsRegularFileWithoutUnixExecuteBits(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := discoverExecutable(path, "windows", "", "", func(string) (string, error) {
		return "", errors.New("missing")
	}, os.Stat)
	if err != nil || got != path {
		t.Fatalf("discover Windows executable = %q, %v", got, err)
	}
}

func TestDiscoverExecutableRejectsNonExecutable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := discoverExecutable(path, "linux", "", "", func(string) (string, error) {
		return "", errors.New("missing")
	}, os.Stat)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("error = %v", err)
	}
}
