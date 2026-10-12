package agentsession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

// IsolatedWorkspace creates one empty directory per Session. The directory is
// never the portable project root and contains no project configuration.
type IsolatedWorkspace struct{ root string }

func NewIsolatedWorkspace(root string) (*IsolatedWorkspace, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidConfiguration
	}
	return &IsolatedWorkspace{root: root}, nil
}

func (w *IsolatedWorkspace) Prepare(ctx context.Context, sessionID agent.SessionID) (string, error) {
	if w == nil || ctx == nil || ctx.Err() != nil || sessionID.Validate() != nil {
		return "", ErrInvalidRequest
	}
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return "", fmt.Errorf("prepare Agent workspace root: %w", err)
	}
	path := filepath.Join(w.root, sessionID.String())
	if filepath.Dir(path) != w.root {
		return "", ErrInvalidRequest
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", fmt.Errorf("prepare Agent workspace: %w", err)
	}
	return path, nil
}

// Restore recreates a missing isolated directory for a persisted Session while
// rejecting path changes and symlinks. It is intentionally separate from
// Prepare so new Session ID collisions still fail closed.
func (w *IsolatedWorkspace) Restore(ctx context.Context, sessionID agent.SessionID, path string) error {
	if w == nil || ctx == nil || ctx.Err() != nil || sessionID.Validate() != nil ||
		path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidRequest
	}
	expected := filepath.Join(w.root, sessionID.String())
	if path != expected || filepath.Dir(path) != w.root {
		return ErrInvalidRequest
	}
	if err := os.MkdirAll(w.root, 0o700); err != nil {
		return fmt.Errorf("restore Agent workspace root: %w", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("restore Agent workspace: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRequest
	}
	return nil
}

func (w *IsolatedWorkspace) Discard(path string) error {
	if w == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != w.root {
		return ErrInvalidRequest
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("discard Agent workspace: %w", err)
	}
	return nil
}
