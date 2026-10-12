package studio

import (
	"context"
	"errors"

	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ApplicationAgentStore translates Studio persistence categories at the
// Agent Session application boundary.
type ApplicationAgentStore struct{ store *Store }

func NewApplicationAgentStore(store *Store) (*ApplicationAgentStore, error) {
	if store == nil {
		return nil, errors.New("create Application Agent Store: Studio Store is required")
	}
	return &ApplicationAgentStore{store: store}, nil
}

func (s *ApplicationAgentStore) CreateAgentSessionWithBinding(ctx context.Context, session agent.AgentSession, binding agent.MCPBinding) error {
	return mapAgentApplicationError(s.store.CreateAgentSessionWithBinding(ctx, session, binding))
}
func (s *ApplicationAgentStore) AgentSession(ctx context.Context, id agent.SessionID) (agent.AgentSession, error) {
	value, err := s.store.AgentSession(ctx, id)
	return value, mapAgentApplicationError(err)
}
func (s *ApplicationAgentStore) AgentSessions(ctx context.Context, projectID project.ProjectID, episodeID project.EpisodeID, archived bool) ([]agent.AgentSession, error) {
	value, err := s.store.AgentSessions(ctx, projectID, episodeID, archived)
	return value, mapAgentApplicationError(err)
}
func (s *ApplicationAgentStore) SaveAgentSession(ctx context.Context, session agent.AgentSession, revision int64) error {
	return mapAgentApplicationError(s.store.SaveAgentSession(ctx, session, revision))
}
func (s *ApplicationAgentStore) AgentTurns(ctx context.Context, id agent.SessionID) ([]agent.AgentTurn, error) {
	value, err := s.store.AgentTurns(ctx, id)
	return value, mapAgentApplicationError(err)
}
func (s *ApplicationAgentStore) AgentInputOperations(ctx context.Context, id agent.SessionID) ([]agent.InputOperation, error) {
	value, err := s.store.AgentInputOperations(ctx, id)
	return value, mapAgentApplicationError(err)
}
func (s *ApplicationAgentStore) AgentMCPBinding(ctx context.Context, id agent.SessionID) (agent.MCPBinding, error) {
	value, err := s.store.AgentMCPBinding(ctx, id)
	return value, mapAgentApplicationError(err)
}
func (s *ApplicationAgentStore) SaveAgentMCPBinding(ctx context.Context, binding agent.MCPBinding, revision int64) error {
	return mapAgentApplicationError(s.store.SaveAgentMCPBinding(ctx, binding, revision))
}

func mapAgentApplicationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrAgentSessionNotFound):
		return errors.Join(agentsession.ErrSessionNotFound, err)
	case errors.Is(err, ErrAgentRevisionConflict):
		return errors.Join(agentsession.ErrRevisionConflict, err)
	default:
		return err
	}
}
