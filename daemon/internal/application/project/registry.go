package projectapp

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ProjectHandle binds one stable Project identity and root to its open Store.
type ProjectHandle struct {
	ProjectID domain.ProjectID
	RootPath  string
	Store     ProjectStore
}

// ProjectStoreRegistry owns open project Store lifetimes within one daemon
// process. It is not a project state cache.
type ProjectStoreRegistry struct {
	mutex  sync.RWMutex
	byID   map[domain.ProjectID]ProjectHandle
	byRoot map[string]domain.ProjectID
}

func NewProjectStoreRegistry() *ProjectStoreRegistry {
	return &ProjectStoreRegistry{
		byID:   make(map[domain.ProjectID]ProjectHandle),
		byRoot: make(map[string]domain.ProjectID),
	}
}

// Register adds an open Store or returns the existing handle when the same
// Project and root are already registered. The caller remains responsible for
// closing an unused candidate Store.
func (r *ProjectStoreRegistry) Register(handle ProjectHandle) (ProjectHandle, bool, error) {
	if r == nil {
		return ProjectHandle{}, false, errors.New("project Store registry is required")
	}
	if err := handle.ProjectID.Validate(); err != nil {
		return ProjectHandle{}, false, fmt.Errorf("register project Store: %w", err)
	}
	if handle.RootPath == "" || !filepath.IsAbs(handle.RootPath) || filepath.Clean(handle.RootPath) != handle.RootPath || handle.Store == nil {
		return ProjectHandle{}, false, errors.New("register project Store: root path and Store are required")
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()
	if existing, ok := r.byID[handle.ProjectID]; ok {
		if existing.RootPath != handle.RootPath {
			return ProjectHandle{}, false, ErrProjectPathConflict
		}
		return existing, true, nil
	}
	if existingID, ok := r.byRoot[handle.RootPath]; ok && existingID != handle.ProjectID {
		return ProjectHandle{}, false, ErrProjectPathConflict
	}
	r.byID[handle.ProjectID] = handle
	r.byRoot[handle.RootPath] = handle.ProjectID
	return handle, false, nil
}

func (r *ProjectStoreRegistry) ByProjectID(projectID domain.ProjectID) (ProjectHandle, bool) {
	if r == nil {
		return ProjectHandle{}, false
	}
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	handle, ok := r.byID[projectID]
	return handle, ok
}

func (r *ProjectStoreRegistry) ByRootPath(rootPath string) (ProjectHandle, bool) {
	if r == nil {
		return ProjectHandle{}, false
	}
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	projectID, ok := r.byRoot[rootPath]
	if !ok {
		return ProjectHandle{}, false
	}
	handle, ok := r.byID[projectID]
	return handle, ok
}

// Handles returns a point-in-time copy for schedulers that coordinate work
// across every currently open portable project.
func (r *ProjectStoreRegistry) Handles() []ProjectHandle {
	if r == nil {
		return nil
	}
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	result := make([]ProjectHandle, 0, len(r.byID))
	for _, handle := range r.byID {
		result = append(result, handle)
	}
	return result
}

// CloseProject removes and closes one open Store.
func (r *ProjectStoreRegistry) CloseProject(projectID domain.ProjectID) error {
	if r == nil {
		return errors.New("close project Store: registry is required")
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	handle, ok := r.byID[projectID]
	if !ok {
		return ErrProjectStoreNotOpen
	}
	if err := handle.Store.Close(); err != nil {
		return fmt.Errorf("close project Store %q: %w", projectID, err)
	}
	delete(r.byID, projectID)
	delete(r.byRoot, handle.RootPath)
	return nil
}

// CloseAll closes every registered Store and empties the registry.
func (r *ProjectStoreRegistry) CloseAll() error {
	if r == nil {
		return nil
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	var closeErrors []error
	for projectID, handle := range r.byID {
		if err := handle.Store.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close project Store %q: %w", projectID, err))
			continue
		}
		delete(r.byID, projectID)
		delete(r.byRoot, handle.RootPath)
	}
	return errors.Join(closeErrors...)
}
