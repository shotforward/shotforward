package studio

import (
	"context"
	"errors"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

// ApplicationRuntimeSelectionStore adapts the Studio preference row to the
// Runtime router without exposing persistence revisions outside this package.
type ApplicationRuntimeSelectionStore struct{ store *Store }

func NewApplicationRuntimeSelectionStore(store *Store) (*ApplicationRuntimeSelectionStore, error) {
	if store == nil {
		return nil, errors.New("create Agent Runtime selection store: Studio Store is required")
	}
	return &ApplicationRuntimeSelectionStore{store: store}, nil
}

func (s *ApplicationRuntimeSelectionStore) ActiveRuntime(ctx context.Context) (agentapp.RuntimeKind, error) {
	settings, err := s.store.RuntimeSettings(ctx)
	return agentapp.RuntimeKind(settings.ActiveRuntimeKind), err
}

func (s *ApplicationRuntimeSelectionStore) SetActiveRuntime(ctx context.Context, kind agentapp.RuntimeKind) error {
	for attempts := 0; attempts < 3; attempts++ {
		settings, err := s.store.RuntimeSettings(ctx)
		if err != nil {
			return err
		}
		if settings.ActiveRuntimeKind == string(kind) {
			return nil
		}
		_, err = s.store.UpdateActiveRuntime(ctx, string(kind), settings.Revision, time.Now().UTC())
		if !errors.Is(err, ErrAgentRevisionConflict) {
			return err
		}
	}
	return ErrAgentRevisionConflict
}

func (s *ApplicationRuntimeSelectionStore) HasActiveTurns(ctx context.Context) (bool, error) {
	return s.store.HasActiveAgentTurns(ctx)
}

func (s *ApplicationRuntimeSelectionStore) RuntimeWithActiveTurns(ctx context.Context) (agentapp.RuntimeKind, bool, error) {
	kind, found, err := s.store.ActiveAgentTurnRuntime(ctx)
	return agentapp.RuntimeKind(kind), found, err
}
