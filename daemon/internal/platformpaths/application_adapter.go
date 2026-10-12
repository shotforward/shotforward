package platformpaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

// ApplicationProjectWorkspace implements the Application filesystem port for
// dedicated local project directories.
type ApplicationProjectWorkspace struct{}

func (ApplicationProjectWorkspace) Create(rootPath string) (projectapp.ProjectPaths, error) {
	root, err := validateApplicationRoot(rootPath)
	if err != nil {
		return projectapp.ProjectPaths{}, err
	}
	if _, err := os.Lstat(root); err == nil {
		return projectapp.ProjectPaths{}, projectapp.ErrProjectPathExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return projectapp.ProjectPaths{}, mapWorkspaceError("inspect new Project path", err)
	}

	parentInfo, err := os.Stat(filepath.Dir(root))
	if err != nil {
		return projectapp.ProjectPaths{}, mapWorkspaceError("inspect Project parent", err)
	}
	if !parentInfo.IsDir() {
		return projectapp.ProjectPaths{}, fmt.Errorf("%w: parent directory is unavailable", projectapp.ErrProjectPathInvalid)
	}
	if err := os.Mkdir(root, privateDirectoryMode); err != nil {
		return projectapp.ProjectPaths{}, mapWorkspaceError("create Project root", err)
	}
	layout, err := NewProjectLayout(root)
	if err != nil {
		return projectapp.ProjectPaths{}, cleanupWorkspaceFailure(root, err)
	}
	if err := layout.EnsureDirectories(); err != nil {
		return projectapp.ProjectPaths{}, cleanupWorkspaceFailure(root, err)
	}
	canonicalRoot, err := CanonicalProjectRoot(root)
	if err != nil {
		return projectapp.ProjectPaths{}, cleanupWorkspaceFailure(root, err)
	}
	canonicalLayout, err := NewProjectLayout(canonicalRoot)
	if err != nil {
		return projectapp.ProjectPaths{}, cleanupWorkspaceFailure(root, err)
	}
	return projectapp.ProjectPaths{Root: canonicalLayout.Root, Database: canonicalLayout.Database}, nil
}

func (ApplicationProjectWorkspace) Open(rootPath string) (projectapp.ProjectPaths, error) {
	root, err := validateApplicationRoot(rootPath)
	if err != nil {
		return projectapp.ProjectPaths{}, err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return projectapp.ProjectPaths{}, projectapp.ErrProjectPathNotFound
	}
	if err != nil {
		return projectapp.ProjectPaths{}, mapWorkspaceError("inspect Project path", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return projectapp.ProjectPaths{}, projectapp.ErrProjectPathInvalid
	}
	canonicalRoot, err := CanonicalProjectRoot(root)
	if err != nil {
		return projectapp.ProjectPaths{}, fmt.Errorf("%w: %v", projectapp.ErrProjectPathInvalid, err)
	}
	layout, err := NewProjectLayout(canonicalRoot)
	if err != nil {
		return projectapp.ProjectPaths{}, fmt.Errorf("%w: %v", projectapp.ErrProjectPathInvalid, err)
	}
	return projectapp.ProjectPaths{Root: layout.Root, Database: layout.Database}, nil
}

func (ApplicationProjectWorkspace) CleanupCreated(paths projectapp.ProjectPaths) error {
	return removeProjectRoot("clean up created Project", paths)
}

func (ApplicationProjectWorkspace) Delete(paths projectapp.ProjectPaths) error {
	return removeProjectRoot("delete Project", paths)
}

func removeProjectRoot(operation string, paths projectapp.ProjectPaths) error {
	root, err := validateApplicationRoot(paths.Root)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	layout, err := NewProjectLayout(root)
	if err != nil || paths.Database != layout.Database {
		return fmt.Errorf("%s: %w", operation, projectapp.ErrProjectPathInvalid)
	}
	if filepath.Dir(root) == root {
		return fmt.Errorf("%s: %w: filesystem root is forbidden", operation, projectapp.ErrProjectPathInvalid)
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return mapWorkspaceError("inspect created Project during cleanup", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s: %w", operation, projectapp.ErrProjectPathInvalid)
	}
	if err := os.RemoveAll(root); err != nil {
		return mapWorkspaceError("remove created Project", err)
	}
	return nil
}

func validateApplicationRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return "", projectapp.ErrProjectPathInvalid
	}
	cleaned := filepath.Clean(value)
	if cleaned != value {
		return "", projectapp.ErrProjectPathInvalid
	}
	return cleaned, nil
}

func cleanupWorkspaceFailure(root string, cause error) error {
	if removeErr := os.RemoveAll(root); removeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("remove partial Project directory: %w", removeErr))
	}
	return mapWorkspaceError("prepare Project workspace", cause)
}

func mapWorkspaceError(operation string, err error) error {
	category := projectapp.ErrProjectPathInvalid
	if errors.Is(err, os.ErrPermission) {
		category = projectapp.ErrProjectPathNotWritable
	}
	return fmt.Errorf("%s: %w: %w", operation, category, err)
}
