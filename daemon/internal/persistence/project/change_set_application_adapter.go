package projectstore

import (
	"context"
	"errors"
	"time"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var _ changesetapp.Store = (*applicationStore)(nil)

type ApplicationChangeSetResolver struct {
	Registry *projectapp.ProjectStoreRegistry
}

func NewApplicationChangeSetResolver(registry *projectapp.ProjectStoreRegistry) (*ApplicationChangeSetResolver, error) {
	if registry == nil {
		return nil, errors.New("create ChangeSet Store resolver: Project registry is required")
	}
	return &ApplicationChangeSetResolver{Registry: registry}, nil
}

func (r *ApplicationChangeSetResolver) ResolveChangeSetStore(ctx context.Context, projectID project.ProjectID) (changesetapp.Store, error) {
	if r == nil || r.Registry == nil || ctx == nil || projectID.Validate() != nil {
		return nil, errors.New("resolve ChangeSet Store: invalid request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, ok := r.Registry.ByProjectID(projectID)
	if !ok {
		return nil, projectapp.ErrProjectStoreNotOpen
	}
	store, ok := handle.Store.(changesetapp.Store)
	if !ok {
		return nil, errors.New("resolve ChangeSet Store: registered Store lacks ChangeSet support")
	}
	return store, nil
}

func (r *ApplicationChangeSetResolver) LocateChangeSet(ctx context.Context, id changeset.ChangeSetID) (project.ProjectID, error) {
	if r == nil || r.Registry == nil || ctx == nil || id.Validate() != nil {
		return "", changesetapp.ErrInvalidRequest
	}
	var found project.ProjectID
	for _, handle := range r.Registry.Handles() {
		store, ok := handle.Store.(changesetapp.Store)
		if !ok {
			continue
		}
		value, err := store.ChangeSet(ctx, id)
		if errors.Is(err, changesetapp.ErrChangeSetNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		if value.ProjectID != handle.ProjectID {
			return "", changesetapp.ErrChangeSetIdentityConflict
		}
		if found != "" {
			return "", changesetapp.ErrChangeSetIdentityConflict
		}
		found = handle.ProjectID
	}
	if found == "" {
		return "", changesetapp.ErrChangeSetNotFound
	}
	return found, nil
}

func (r *ApplicationChangeSetResolver) ChangeSetsForAgentSession(ctx context.Context, projectID project.ProjectID, sessionID agent.SessionID) ([]changeset.ChangeSet, error) {
	store, err := r.ResolveChangeSetStore(ctx, projectID)
	if err != nil {
		return nil, err
	}
	provider, ok := store.(interface {
		ChangeSetsForAgentSession(context.Context, project.ProjectID, agent.SessionID) ([]changeset.ChangeSet, error)
	})
	if !ok {
		return nil, errors.New("resolve Agent Session ChangeSets: registered Store lacks query support")
	}
	return provider.ChangeSetsForAgentSession(ctx, projectID, sessionID)
}

func (s *applicationStore) CreateChangeSet(ctx context.Context, value changeset.ChangeSet, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	result, replayed, err := s.store.CreateChangeSet(ctx, value, receipt)
	return result, replayed, mapChangeSetApplicationError(err)
}
func (s *applicationStore) ChangeSet(ctx context.Context, id changeset.ChangeSetID) (changeset.ChangeSet, error) {
	result, err := s.store.ChangeSet(ctx, id)
	return result, mapChangeSetApplicationError(err)
}
func (s *applicationStore) ChangeSetsForAgentSession(ctx context.Context, projectID project.ProjectID, sessionID agent.SessionID) ([]changeset.ChangeSet, error) {
	return s.store.ChangeSetsForAgentSession(ctx, projectID, sessionID)
}
func (s *applicationStore) AppendChangeSetOperations(ctx context.Context, id changeset.ChangeSetID, revision int64, operations []changeset.Operation, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	result, replayed, err := s.store.AppendChangeSetOperations(ctx, id, revision, operations, at, receipt)
	return result, replayed, mapChangeSetApplicationError(err)
}
func (s *applicationStore) FinalizeChangeSet(ctx context.Context, id changeset.ChangeSetID, revision int64, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	result, replayed, err := s.store.FinalizeChangeSet(ctx, id, revision, at, receipt)
	return result, replayed, mapChangeSetApplicationError(err)
}
func (s *applicationStore) ApplyChangeSet(ctx context.Context, id changeset.ChangeSetID, revision int64, conflictID changeset.ConflictID, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	result, replayed, err := s.store.ApplyChangeSet(ctx, id, revision, conflictID, at, receipt)
	return result, replayed, mapChangeSetApplicationError(err)
}
func (s *applicationStore) RejectChangeSet(ctx context.Context, id changeset.ChangeSetID, revision int64, reason string, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	result, replayed, err := s.store.RejectChangeSet(ctx, id, revision, reason, at, receipt)
	return result, replayed, mapChangeSetApplicationError(err)
}
func (s *applicationStore) DiscardChangeSetDraft(ctx context.Context, id changeset.ChangeSetID) error {
	return s.store.DiscardChangeSetDraft(ctx, id)
}
func (s *applicationStore) DiscardChangeSetDraftsForTurn(ctx context.Context, turnID agent.TurnID) error {
	return s.store.DiscardChangeSetDraftsForTurn(ctx, turnID)
}
func (s *applicationStore) ChangeSetOperations(ctx context.Context, id changeset.ChangeSetID, afterPosition, limit int) ([]changeset.Operation, error) {
	result, err := s.store.ChangeSetOperations(ctx, id, afterPosition, limit)
	return result, mapChangeSetApplicationError(err)
}

func mapChangeSetApplicationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrChangeSetNotFound):
		return errors.Join(changesetapp.ErrChangeSetNotFound, err)
	case errors.Is(err, ErrChangeSetRevisionConflict):
		return errors.Join(changesetapp.ErrChangeSetRevisionConflict, err)
	case errors.Is(err, ErrChangeSetStateConflict):
		return errors.Join(changesetapp.ErrChangeSetStateConflict, err)
	case errors.Is(err, ErrChangeSetOperationConflict):
		return errors.Join(changesetapp.ErrChangeSetOperationConflict, err)
	case errors.Is(err, ErrChangeSetConflicted):
		return errors.Join(changesetapp.ErrChangeSetConflicted, err)
	case errors.Is(err, ErrChangeSetPreconditionFailed):
		return errors.Join(changesetapp.ErrChangeSetPreconditionFailed, err)
	default:
		return err
	}
}
func (s *applicationStore) ChangeSetConflicts(ctx context.Context, id changeset.ChangeSetID) ([]changeset.Conflict, error) {
	return s.store.ChangeSetConflicts(ctx, id)
}
