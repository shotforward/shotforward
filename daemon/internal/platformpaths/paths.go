// Package platformpaths defines every daemon-owned filesystem location.
//
// Keeping path construction here prevents transports, repositories, and domain
// code from inventing platform-specific locations or trusting stored paths.
package platformpaths

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

const privateDirectoryMode os.FileMode = 0o700

// LookupEnv matches os.LookupEnv and makes platform resolution deterministic in
// tests.
type LookupEnv func(key string) (string, bool)

// ApplicationDirs contains daemon-owned, per-user directories. Project data is
// deliberately excluded because every project lives in a user-selected root.
type ApplicationDirs struct {
	Data    string
	Cache   string
	Logs    string
	Runtime string
}

// ResolveApplicationDirs resolves the current operating system's standard
// per-user application directories without creating them.
func ResolveApplicationDirs() (ApplicationDirs, error) {
	var home string
	if runtime.GOOS != "windows" {
		resolvedHome, err := os.UserHomeDir()
		if err != nil {
			return ApplicationDirs{}, fmt.Errorf("resolve application directories: user home: %w", err)
		}
		home = resolvedHome
	}

	return resolveApplicationDirs(runtime.GOOS, home, os.LookupEnv)
}

func resolveApplicationDirs(goos, home string, lookupEnv LookupEnv) (ApplicationDirs, error) {
	if lookupEnv == nil {
		return ApplicationDirs{}, fmt.Errorf("resolve application directories: environment lookup is required")
	}

	var dataDir, cacheDir string
	switch goos {
	case "windows":
		localAppData, ok := nonEmptyEnvironment(lookupEnv, "LOCALAPPDATA")
		if !ok {
			return ApplicationDirs{}, fmt.Errorf("resolve application directories: LOCALAPPDATA is required on Windows")
		}
		dataDir = filepath.Join(localAppData, "ShotForward")
		cacheDir = filepath.Join(dataDir, "Cache")
	case "darwin":
		if strings.TrimSpace(home) == "" {
			return ApplicationDirs{}, fmt.Errorf("resolve application directories: user home is required on macOS")
		}
		dataDir = filepath.Join(home, "Library", "Application Support", "ShotForward")
		cacheDir = filepath.Join(home, "Library", "Caches", "ShotForward")
	case "linux":
		if value, ok := nonEmptyEnvironment(lookupEnv, "XDG_DATA_HOME"); ok {
			dataDir = filepath.Join(value, "shotforward")
		} else {
			if strings.TrimSpace(home) == "" {
				return ApplicationDirs{}, fmt.Errorf("resolve application directories: user home is required when XDG_DATA_HOME is unset")
			}
			dataDir = filepath.Join(home, ".local", "share", "shotforward")
		}

		if value, ok := nonEmptyEnvironment(lookupEnv, "XDG_CACHE_HOME"); ok {
			cacheDir = filepath.Join(value, "shotforward")
		} else {
			if strings.TrimSpace(home) == "" {
				return ApplicationDirs{}, fmt.Errorf("resolve application directories: user home is required when XDG_CACHE_HOME is unset")
			}
			cacheDir = filepath.Join(home, ".cache", "shotforward")
		}
	default:
		return ApplicationDirs{}, fmt.Errorf("resolve application directories: unsupported operating system %q", goos)
	}

	dirs := ApplicationDirs{
		Data:    filepath.Clean(dataDir),
		Cache:   filepath.Clean(cacheDir),
		Logs:    filepath.Join(dataDir, "logs"),
		Runtime: filepath.Join(dataDir, "runtime"),
	}
	if err := dirs.Validate(); err != nil {
		return ApplicationDirs{}, fmt.Errorf("resolve application directories: %w", err)
	}

	return dirs, nil
}

func nonEmptyEnvironment(lookupEnv LookupEnv, key string) (string, bool) {
	value, ok := lookupEnv(key)
	return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
}

// Validate checks that application directories are absolute and that the logs
// and runtime directories remain under the application data directory.
func (d ApplicationDirs) Validate() error {
	for label, value := range map[string]string{
		"data":    d.Data,
		"cache":   d.Cache,
		"logs":    d.Logs,
		"runtime": d.Runtime,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s directory is required", label)
		}
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s directory must be absolute: %q", label, value)
		}
	}

	if !lexicallyWithin(d.Data, d.Logs) {
		return fmt.Errorf("logs directory must be inside data directory")
	}
	if !lexicallyWithin(d.Data, d.Runtime) {
		return fmt.Errorf("runtime directory must be inside data directory")
	}

	return nil
}

// Ensure creates daemon-owned application directories with 0700 permissions on
// Unix and inherited user ACL semantics on Windows. It does not create
// databases or runtime descriptor files.
func (d ApplicationDirs) Ensure() error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("ensure application directories: %w", err)
	}

	for _, directory := range []string{d.Data, d.Cache, d.Logs, d.Runtime} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return fmt.Errorf("ensure application directories: %w", err)
		}
	}

	return nil
}

// ProjectLayout contains all paths owned by one portable ShotForward project.
type ProjectLayout struct {
	Root     string
	Database string
	Media    string
	Images   string
	Videos   string
	Audio    string
	Imports  string
	Exports  string
	Internal string
	Staging  string
}

// NewProjectLayout constructs a project layout without creating files.
func NewProjectLayout(root string) (ProjectLayout, error) {
	if strings.TrimSpace(root) == "" {
		return ProjectLayout{}, fmt.Errorf("create project layout: root is required")
	}
	if !filepath.IsAbs(root) {
		return ProjectLayout{}, fmt.Errorf("create project layout: root must be absolute: %q", root)
	}

	root = filepath.Clean(root)
	media := filepath.Join(root, "media")
	internal := filepath.Join(root, ".shotforward")

	return ProjectLayout{
		Root:     root,
		Database: filepath.Join(root, "project.db"),
		Media:    media,
		Images:   filepath.Join(media, "images"),
		Videos:   filepath.Join(media, "videos"),
		Audio:    filepath.Join(media, "audio"),
		Imports:  filepath.Join(root, "imports"),
		Exports:  filepath.Join(root, "exports"),
		Internal: internal,
		Staging:  filepath.Join(internal, "staging"),
	}, nil
}

// EnsureDirectories creates the project directory tree but does not create the
// project database.
func (p ProjectLayout) EnsureDirectories() error {
	expected, err := NewProjectLayout(p.Root)
	if err != nil {
		return fmt.Errorf("ensure project directories: %w", err)
	}
	if p != expected {
		return fmt.Errorf("ensure project directories: layout does not match root %q", p.Root)
	}

	for _, directory := range []string{
		p.Root,
		p.Media,
		p.Images,
		p.Videos,
		p.Audio,
		p.Imports,
		p.Exports,
		p.Internal,
		p.Staging,
	} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return fmt.Errorf("ensure project directories: %w", err)
		}
	}

	return nil
}

func ensurePrivateDirectory(directory string) error {
	if err := os.MkdirAll(directory, privateDirectoryMode); err != nil {
		return fmt.Errorf("create %q: %w", directory, err)
	}
	if err := os.Chmod(directory, privateDirectoryMode); err != nil {
		return fmt.Errorf("set permissions on %q: %w", directory, err)
	}
	return nil
}

// CanonicalProjectRoot resolves symbolic links for an existing project root.
// The returned path is suitable for project identity and locking.
func CanonicalProjectRoot(root string) (string, error) {
	layout, err := NewProjectLayout(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize project root: %w", err)
	}

	canonical, err := filepath.EvalSymlinks(layout.Root)
	if err != nil {
		return "", fmt.Errorf("canonicalize project root %q: %w", layout.Root, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize project root %q: %w", layout.Root, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize project root %q: %w", layout.Root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("canonicalize project root %q: not a directory", layout.Root)
	}

	return filepath.Clean(canonical), nil
}

// ValidateProjectRelativePath validates the portable, slash-separated path
// format stored in project.db.
func ValidateProjectRelativePath(relative string) error {
	if relative == "" {
		return fmt.Errorf("project-relative path is required")
	}
	if strings.ContainsRune(relative, 0) {
		return fmt.Errorf("project-relative path contains a null byte")
	}
	if strings.Contains(relative, "\\") {
		return fmt.Errorf("project-relative path must use forward slashes")
	}
	if strings.Contains(relative, ":") {
		return fmt.Errorf("project-relative path must not contain a volume or alternate stream")
	}
	if path.IsAbs(relative) {
		return fmt.Errorf("project-relative path must not be absolute: %q", relative)
	}

	cleaned := path.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("project-relative path escapes the project: %q", relative)
	}
	if cleaned != relative {
		return fmt.Errorf("project-relative path is not canonical: %q", relative)
	}

	return nil
}

// ResolveProjectPath resolves a stored project-relative path and verifies that
// existing symbolic-link components do not escape the canonical project root.
// Callers must still open the result immediately to minimize filesystem races.
func ResolveProjectPath(root, relative string) (string, error) {
	if err := ValidateProjectRelativePath(relative); err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}

	canonicalRoot, err := CanonicalProjectRoot(root)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	candidate := filepath.Join(canonicalRoot, filepath.FromSlash(relative))

	existing := candidate
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("resolve project path %q: %w", relative, statErr)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("resolve project path %q: no existing ancestor", relative)
		}
		existing = parent
	}

	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve project path %q: %w", relative, err)
	}
	remainder, err := filepath.Rel(existing, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve project path %q: %w", relative, err)
	}
	resolved := filepath.Clean(filepath.Join(resolvedExisting, remainder))
	if !lexicallyWithin(canonicalRoot, resolved) {
		return "", fmt.Errorf("resolve project path %q: symbolic link escapes the project", relative)
	}

	return resolved, nil
}

// ApplicationProjectFileReader opens a validated project-relative regular file
// and returns the already-open handle so transport code does not re-resolve it.
type ApplicationProjectFileReader struct{}

func (ApplicationProjectFileReader) OpenProjectFile(root, relative string) (projectapp.ProjectFile, error) {
	resolved, err := ResolveProjectPath(root, relative)
	if err != nil {
		return projectapp.ProjectFile{}, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return projectapp.ProjectFile{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return projectapp.ProjectFile{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 {
		_ = file.Close()
		return projectapp.ProjectFile{}, fmt.Errorf("project file is not a non-empty regular file")
	}
	return projectapp.ProjectFile{Content: file, ByteSize: info.Size(), ModifiedAt: info.ModTime()}, nil
}

func lexicallyWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
