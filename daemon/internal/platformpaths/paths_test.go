package platformpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveApplicationDirsLinuxDefaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix absolute path semantics are unavailable on Windows")
	}
	t.Parallel()

	dirs, err := resolveApplicationDirs("linux", "/home/alice", emptyEnvironment)
	if err != nil {
		t.Fatalf("resolveApplicationDirs() error = %v", err)
	}

	wantData := filepath.FromSlash("/home/alice/.local/share/shotforward")
	wantCache := filepath.FromSlash("/home/alice/.cache/shotforward")
	if dirs.Data != wantData {
		t.Fatalf("Data = %q, want %q", dirs.Data, wantData)
	}
	if dirs.Cache != wantCache {
		t.Fatalf("Cache = %q, want %q", dirs.Cache, wantCache)
	}
	if dirs.Logs != filepath.Join(wantData, "logs") {
		t.Fatalf("Logs = %q, want a directory under Data", dirs.Logs)
	}
	if dirs.Runtime != filepath.Join(wantData, "runtime") {
		t.Fatalf("Runtime = %q, want a directory under Data", dirs.Runtime)
	}
}

func TestResolveApplicationDirsLinuxUsesXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix absolute path semantics are unavailable on Windows")
	}
	t.Parallel()

	environment := map[string]string{
		"XDG_DATA_HOME":  "/data/alice",
		"XDG_CACHE_HOME": "/cache/alice",
	}
	dirs, err := resolveApplicationDirs("linux", "/home/alice", mapEnvironment(environment))
	if err != nil {
		t.Fatalf("resolveApplicationDirs() error = %v", err)
	}

	if dirs.Data != filepath.FromSlash("/data/alice/shotforward") {
		t.Fatalf("Data = %q", dirs.Data)
	}
	if dirs.Cache != filepath.FromSlash("/cache/alice/shotforward") {
		t.Fatalf("Cache = %q", dirs.Cache)
	}
}

func TestResolveApplicationDirsRejectsRelativeXDGPath(t *testing.T) {
	t.Parallel()

	_, err := resolveApplicationDirs("linux", "/home/alice", mapEnvironment(map[string]string{
		"XDG_DATA_HOME": "relative/data",
	}))
	if err == nil {
		t.Fatal("resolveApplicationDirs() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %q, want absolute path error", err)
	}
}

func TestResolveApplicationDirsMacOS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix absolute path semantics are unavailable on Windows")
	}
	t.Parallel()

	dirs, err := resolveApplicationDirs("darwin", "/Users/alice", emptyEnvironment)
	if err != nil {
		t.Fatalf("resolveApplicationDirs() error = %v", err)
	}
	if dirs.Data != filepath.FromSlash("/Users/alice/Library/Application Support/ShotForward") {
		t.Fatalf("Data = %q", dirs.Data)
	}
	if dirs.Cache != filepath.FromSlash("/Users/alice/Library/Caches/ShotForward") {
		t.Fatalf("Cache = %q", dirs.Cache)
	}
}

func TestResolveApplicationDirsWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics are required")
	}
	t.Parallel()

	localAppData := t.TempDir()
	dirs, err := resolveApplicationDirs("windows", "", mapEnvironment(map[string]string{
		"LOCALAPPDATA": localAppData,
	}))
	if err != nil {
		t.Fatalf("resolveApplicationDirs() error = %v", err)
	}
	wantData := filepath.Join(localAppData, "ShotForward")
	if dirs.Data != wantData {
		t.Fatalf("Data = %q, want %q", dirs.Data, wantData)
	}
	if dirs.Cache != filepath.Join(wantData, "Cache") {
		t.Fatalf("Cache = %q", dirs.Cache)
	}
}

func TestResolveApplicationDirsRejectsUnsupportedOS(t *testing.T) {
	t.Parallel()

	if _, err := resolveApplicationDirs("plan9", "/home/alice", emptyEnvironment); err == nil {
		t.Fatal("resolveApplicationDirs() error = nil, want an error")
	}
}

func TestApplicationDirsEnsure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dirs := ApplicationDirs{
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "data", "logs"),
		Runtime: filepath.Join(root, "data", "runtime"),
	}
	if err := dirs.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	for _, directory := range []string{dirs.Data, dirs.Cache, dirs.Logs, dirs.Runtime} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", directory, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", directory)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != privateDirectoryMode {
			t.Fatalf("permissions for %q = %o, want %o", directory, info.Mode().Perm(), privateDirectoryMode)
		}
	}
}

func TestNewProjectLayout(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "project")
	layout, err := NewProjectLayout(root)
	if err != nil {
		t.Fatalf("NewProjectLayout() error = %v", err)
	}

	if layout.Database != filepath.Join(root, "project.db") {
		t.Fatalf("Database = %q", layout.Database)
	}
	if layout.Staging != filepath.Join(root, ".shotforward", "staging") {
		t.Fatalf("Staging = %q", layout.Staging)
	}

	if err := layout.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}
	for _, directory := range []string{
		layout.Root,
		layout.Images,
		layout.Videos,
		layout.Audio,
		layout.Imports,
		layout.Exports,
		layout.Internal,
		layout.Staging,
	} {
		if info, statErr := os.Stat(directory); statErr != nil || !info.IsDir() {
			t.Fatalf("project directory %q was not created: info=%v err=%v", directory, info, statErr)
		}
	}
	if _, statErr := os.Stat(layout.Database); !os.IsNotExist(statErr) {
		t.Fatalf("project database unexpectedly exists: error = %v", statErr)
	}
}

func TestValidateProjectRelativePath(t *testing.T) {
	t.Parallel()

	valid := []string{
		"media/images/01/asset.png",
		"media/videos/clip-1.mp4",
		"imports/script.md",
	}
	for _, relative := range valid {
		if err := ValidateProjectRelativePath(relative); err != nil {
			t.Errorf("ValidateProjectRelativePath(%q) error = %v", relative, err)
		}

	}

	invalid := []string{
		"",
		".",
		"../outside",
		"media/../project.db",
		"media//image.png",
		"media\\image.png",
		"C:/outside.png",
		"/outside.png",
		"media/image.png/",
		"media/\x00image.png",
	}
	for _, relative := range invalid {
		if err := ValidateProjectRelativePath(relative); err == nil {
			t.Errorf("ValidateProjectRelativePath(%q) error = nil, want an error", relative)
		}
	}
}

func TestApplicationProjectFileReaderReturnsAlreadyOpenRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "media", "videos", "clip.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := (ApplicationProjectFileReader{}).OpenProjectFile(root, "media/videos/clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Content.Close()
	if opened.ByteSize != 5 || opened.ModifiedAt.IsZero() {
		t.Fatalf("opened Project file = %#v", opened)
	}
	if _, err := (ApplicationProjectFileReader{}).OpenProjectFile(root, "../outside.mp4"); err == nil {
		t.Fatal("escaping project path was accepted")
	}
}

func TestResolveProjectPathRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	t.Parallel()

	container := t.TempDir()
	root := filepath.Join(container, "project")
	outside := filepath.Join(container, "outside")
	for _, directory := range []string{filepath.Join(root, "media"), outside} {
		if err := os.MkdirAll(directory, privateDirectoryMode); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", directory, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "media", "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := ResolveProjectPath(root, "media/escape/image.png")
	if err == nil {
		t.Fatal("ResolveProjectPath() error = nil, want escaping symlink error")
	}
	if !strings.Contains(err.Error(), "escapes the project") {
		t.Fatalf("error = %q, want symlink escape error", err)
	}
}

func TestResolveProjectPathAllowsInternalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	t.Parallel()

	root := t.TempDir()
	realImages := filepath.Join(root, "media", "images")
	if err := os.MkdirAll(realImages, privateDirectoryMode); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(realImages, filepath.Join(root, "images-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	got, err := ResolveProjectPath(root, "images-link/new.png")
	if err != nil {
		t.Fatalf("ResolveProjectPath() error = %v", err)
	}
	canonicalImages, err := filepath.EvalSymlinks(realImages)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", realImages, err)
	}
	want := filepath.Join(canonicalImages, "new.png")
	if got != want {
		t.Fatalf("ResolveProjectPath() = %q, want %q", got, want)
	}
}

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func mapEnvironment(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
