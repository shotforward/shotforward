package platformpaths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

func TestApplicationProjectWorkspaceLifecycle(t *testing.T) {
	t.Parallel()

	workspace := ApplicationProjectWorkspace{}
	root := filepath.Join(t.TempDir(), "project")
	paths, err := workspace.Create(root)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", root, err)
	}
	if paths.Root != canonicalRoot || paths.Database != filepath.Join(canonicalRoot, "project.db") {
		t.Fatalf("Create() paths = %#v", paths)
	}
	opened, err := workspace.Open(root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if opened != paths {
		t.Fatalf("Open() paths = %#v, want %#v", opened, paths)
	}
	if _, err := workspace.Create(root); !errors.Is(err, projectapp.ErrProjectPathExists) {
		t.Fatalf("Create(existing) error = %v, want ErrProjectPathExists", err)
	}
	if err := workspace.CleanupCreated(paths); err != nil {
		t.Fatalf("CleanupCreated() error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Project root still exists after cleanup: %v", err)
	}
}

func TestApplicationProjectWorkspaceRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	workspace := ApplicationProjectWorkspace{}
	if _, err := workspace.Create("relative"); !errors.Is(err, projectapp.ErrProjectPathInvalid) {
		t.Fatalf("Create(relative) error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := workspace.Open(missing); !errors.Is(err, projectapp.ErrProjectPathNotFound) {
		t.Fatalf("Open(missing) error = %v", err)
	}

	if runtime.GOOS != "windows" {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Open(link); !errors.Is(err, projectapp.ErrProjectPathInvalid) {
			t.Fatalf("Open(symlink) error = %v, want ErrProjectPathInvalid", err)
		}
	}
}
