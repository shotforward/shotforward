package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrAgentSessionNotFound          = errors.New("Agent Session not found")
	ErrAgentTurnNotFound             = errors.New("Agent Turn not found")
	ErrAgentMCPBindingNotFound       = errors.New("Agent MCP Binding not found")
	ErrAgentInputOperationNotFound   = errors.New("Agent input operation not found")
	ErrAgentRecordConflict           = errors.New("Agent record conflicts with existing data")
	ErrAgentRevisionConflict         = errors.New("Agent record revision conflict")
	ErrAgentInputIdempotencyConflict = agent.ErrInputIdempotencyConflict
	ErrAgentSessionInactive          = agent.ErrSessionNotActive
)

type AgentRuntimeSettings struct {
	CodexExecutablePath string
	ActiveRuntimeKind   string
	Revision            int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (s *Store) RuntimeSettings(ctx context.Context) (AgentRuntimeSettings, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("read Agent Runtime settings: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("read Agent Runtime settings: %w", err)
	}
	return scanRuntimeSettings(database.QueryRowContext(ctx, `
SELECT codex_executable_path, active_runtime_kind, revision, created_at_ms, updated_at_ms
FROM agent_runtime_settings WHERE singleton_id = 1`))
}

func (s *Store) UpdateRuntimeExecutable(ctx context.Context, path string, expectedRevision int64, at time.Time) (AgentRuntimeSettings, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update Agent Runtime executable: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update Agent Runtime executable: %w", err)
	}
	if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path) {
		return AgentRuntimeSettings{}, errors.New("update Agent Runtime executable: path must be empty or absolute and clean")
	}
	if expectedRevision < 1 || validateTimestamp("updated at", at) != nil {
		return AgentRuntimeSettings{}, errors.New("update Agent Runtime executable: invalid revision or timestamp")
	}
	result, err := database.ExecContext(ctx, `
UPDATE agent_runtime_settings
SET codex_executable_path = ?, revision = revision + 1, updated_at_ms = ?
WHERE singleton_id = 1 AND revision = ?`, nullableString(path), toMilliseconds(at), expectedRevision)
	if err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update Agent Runtime executable: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update Agent Runtime executable: affected rows: %w", err)
	} else if rows != 1 {
		return AgentRuntimeSettings{}, ErrAgentRevisionConflict
	}
	return s.RuntimeSettings(ctx)
}

func (s *Store) UpdateActiveRuntime(ctx context.Context, kind string, expectedRevision int64, at time.Time) (AgentRuntimeSettings, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update active Agent Runtime: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update active Agent Runtime: %w", err)
	}
	if kind != "user_codex" && kind != "shotforward" {
		return AgentRuntimeSettings{}, errors.New("update active Agent Runtime: invalid Runtime kind")
	}
	if expectedRevision < 1 || validateTimestamp("updated at", at) != nil {
		return AgentRuntimeSettings{}, errors.New("update active Agent Runtime: invalid revision or timestamp")
	}
	result, err := database.ExecContext(ctx, `
UPDATE agent_runtime_settings
SET active_runtime_kind = ?, revision = revision + 1, updated_at_ms = ?
WHERE singleton_id = 1 AND revision = ?`, kind, toMilliseconds(at), expectedRevision)
	if err != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update active Agent Runtime: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
		return AgentRuntimeSettings{}, fmt.Errorf("update active Agent Runtime: affected rows: %w", rowsErr)
	} else if rows != 1 {
		return AgentRuntimeSettings{}, ErrAgentRevisionConflict
	}
	return s.RuntimeSettings(ctx)
}

func (s *Store) HasActiveAgentTurns(ctx context.Context) (bool, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return false, fmt.Errorf("inspect active Agent Turns: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return false, fmt.Errorf("inspect active Agent Turns: %w", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM agent_turns
WHERE status IN ('queued', 'running', 'waiting_user')`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect active Agent Turns: %w", err)
	}
	return count > 0, nil
}

func (s *Store) ActiveAgentTurnRuntime(ctx context.Context) (string, bool, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return "", false, fmt.Errorf("read active Agent Turn Runtime: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return "", false, fmt.Errorf("read active Agent Turn Runtime: %w", err)
	}
	var kind string
	err = database.QueryRowContext(ctx, `
SELECT s.runtime_kind
FROM agent_turns t
JOIN agent_sessions s ON s.id = t.agent_session_id
WHERE t.status IN ('queued', 'running', 'waiting_user')
ORDER BY t.created_at_ms ASC, t.id ASC
LIMIT 1`).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read active Agent Turn Runtime: %w", err)
	}
	return kind, true, nil
}

func scanRuntimeSettings(row rowScanner) (AgentRuntimeSettings, error) {
	var result AgentRuntimeSettings
	var path, activeRuntime sql.NullString
	var createdAtMS, updatedAtMS int64
	if err := row.Scan(&path, &activeRuntime, &result.Revision, &createdAtMS, &updatedAtMS); err != nil {
		return result, fmt.Errorf("scan Agent Runtime settings: %w", err)
	}
	if path.Valid {
		result.CodexExecutablePath = path.String
	}
	if activeRuntime.Valid {
		result.ActiveRuntimeKind = activeRuntime.String
	}
	result.CreatedAt = fromMilliseconds(createdAtMS)
	result.UpdatedAt = fromMilliseconds(updatedAtMS)
	return result, nil
}

func (s *Store) CreateAgentSession(ctx context.Context, session agent.AgentSession) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("create Agent Session: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("create Agent Session: %w", err)
	}
	if err := session.ValidateNew(); err != nil {
		return fmt.Errorf("create Agent Session: %w", err)
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO agent_sessions (
    id, project_id, episode_id, codex_thread_id, codex_session_id, model_id,
    cached_title, web_search_disabled, status, revision, archived_at_ms,
    runtime_kind,
    created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID.String(), session.ProjectID.String(), session.EpisodeID.String(),
		session.CodexThreadID, session.CodexSessionID, session.ModelID,
		nullableString(session.CachedTitle), boolInteger(session.WebSearchDisabled),
		string(session.Status), session.Revision, nullableTimeMilliseconds(session.ArchivedAt),
		normalizedRuntimeKind(session.RuntimeKind),
		toMilliseconds(session.CreatedAt), toMilliseconds(session.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create Agent Session: %w: %v", ErrAgentRecordConflict, err)
	}
	return nil
}

// CreateAgentSessionWithBinding publishes the Runtime mapping and its scoped
// MCP route atomically, so the scheduler can never observe a Session without a
// recoverable binding.
func (s *Store) CreateAgentSessionWithBinding(ctx context.Context, session agent.AgentSession, binding agent.MCPBinding) (_ error) {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("create Agent Session with binding: %w", err)
	}
	if validateContext(ctx) != nil || session.ValidateNew() != nil || binding.ValidateNew() != nil || binding.AgentSessionID != session.ID {
		return errors.New("create Agent Session with binding: invalid request")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create Agent Session with binding: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO agent_sessions (
    id, project_id, episode_id, codex_thread_id, codex_session_id, model_id,
    cached_title, web_search_disabled, status, revision, archived_at_ms,
    runtime_kind,
    created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID.String(), session.ProjectID.String(), session.EpisodeID.String(),
		session.CodexThreadID, session.CodexSessionID, session.ModelID,
		nullableString(session.CachedTitle), boolInteger(session.WebSearchDisabled),
		string(session.Status), session.Revision, nullableTimeMilliseconds(session.ArchivedAt),
		normalizedRuntimeKind(session.RuntimeKind),
		toMilliseconds(session.CreatedAt), toMilliseconds(session.UpdatedAt)); err != nil {
		return fmt.Errorf("create Agent Session with binding: %w: %v", ErrAgentRecordConflict, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO agent_mcp_bindings (
    id, agent_session_id, workspace_path, route_key, status, error_code,
    error_summary, revision, last_verified_at_ms, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, binding.ID.String(), binding.AgentSessionID.String(),
		binding.WorkspacePath, binding.RouteKey, string(binding.Status), failureCode(binding.Failure),
		failureSummary(binding.Failure), binding.Revision, nullableTimeMilliseconds(binding.LastVerifiedAt),
		toMilliseconds(binding.CreatedAt), toMilliseconds(binding.UpdatedAt)); err != nil {
		return fmt.Errorf("create Agent Session with binding: %w: %v", ErrAgentRecordConflict, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create Agent Session with binding: commit: %w", err)
	}
	return nil
}

func (s *Store) AgentSession(ctx context.Context, id agent.SessionID) (agent.AgentSession, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.AgentSession{}, fmt.Errorf("read Agent Session: %w", err)
	}
	if err := validateContext(ctx); err != nil || id.Validate() != nil {
		return agent.AgentSession{}, errors.New("read Agent Session: invalid request")
	}
	session, err := scanAgentSession(database.QueryRowContext(ctx, selectAgentSessionSQL+" WHERE id = ?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.AgentSession{}, ErrAgentSessionNotFound
	}
	if err != nil {
		return agent.AgentSession{}, fmt.Errorf("read Agent Session: %w", err)
	}
	return session, nil
}

func (s *Store) AgentSessions(ctx context.Context, projectID project.ProjectID, episodeID project.EpisodeID, includeArchived bool) ([]agent.AgentSession, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Agent Sessions: %w", err)
	}
	if err := validateContext(ctx); err != nil || projectID.Validate() != nil || episodeID.Validate() != nil {
		return nil, errors.New("list Agent Sessions: invalid request")
	}
	query := selectAgentSessionSQL + " WHERE project_id = ? AND episode_id = ?"
	if !includeArchived {
		query += " AND status <> 'archived'"
	}
	query += " ORDER BY updated_at_ms DESC, id ASC"
	rows, err := database.QueryContext(ctx, query, projectID.String(), episodeID.String())
	if err != nil {
		return nil, fmt.Errorf("list Agent Sessions: %w", err)
	}
	defer rows.Close()
	result := make([]agent.AgentSession, 0)
	for rows.Next() {
		session, err := scanAgentSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list Agent Sessions: %w", err)
		}
		result = append(result, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Agent Sessions: %w", err)
	}
	return result, nil
}

func (s *Store) SaveAgentSession(ctx context.Context, session agent.AgentSession, expectedRevision int64) error {
	if err := session.Validate(); err != nil || expectedRevision < 1 || session.Revision != expectedRevision+1 {
		return errors.New("save Agent Session: invalid value or revision")
	}
	existing, err := s.AgentSession(ctx, session.ID)
	if err != nil {
		return err
	}
	if existing.Revision != expectedRevision {
		return ErrAgentRevisionConflict
	}
	if err := existing.ValidateReplacement(session); err != nil {
		return ErrAgentRecordConflict
	}
	result, err := s.database.ExecContext(ctx, `
UPDATE agent_sessions
SET cached_title = ?, status = ?, revision = ?, archived_at_ms = ?, updated_at_ms = ?
WHERE id = ? AND revision = ?`, nullableString(session.CachedTitle), string(session.Status),
		session.Revision, nullableTimeMilliseconds(session.ArchivedAt), toMilliseconds(session.UpdatedAt),
		session.ID.String(), expectedRevision)
	return mapAgentUpdateResult(result, err)
}

const selectAgentSessionSQL = `
SELECT id, project_id, episode_id, codex_thread_id, codex_session_id, model_id,
       cached_title, web_search_disabled, status, revision, archived_at_ms,
       runtime_kind,
       created_at_ms, updated_at_ms
FROM agent_sessions`

func scanAgentSession(row rowScanner) (agent.AgentSession, error) {
	var value agent.AgentSession
	var id, projectID, episodeID, status string
	var cachedTitle sql.NullString
	var webSearchDisabled int
	var archivedAtMS sql.NullInt64
	var createdAtMS, updatedAtMS int64
	if err := row.Scan(&id, &projectID, &episodeID, &value.CodexThreadID, &value.CodexSessionID,
		&value.ModelID, &cachedTitle, &webSearchDisabled, &status, &value.Revision,
		&archivedAtMS, &value.RuntimeKind, &createdAtMS, &updatedAtMS); err != nil {
		return value, err
	}
	value.ID = agent.SessionID(id)
	value.ProjectID = project.ProjectID(projectID)
	value.EpisodeID = project.EpisodeID(episodeID)
	value.Status = agent.SessionStatus(status)
	value.WebSearchDisabled = webSearchDisabled == 1
	if cachedTitle.Valid {
		value.CachedTitle = cachedTitle.String
	}
	if archivedAtMS.Valid {
		at := fromMilliseconds(archivedAtMS.Int64)
		value.ArchivedAt = &at
	}
	value.CreatedAt = fromMilliseconds(createdAtMS)
	value.UpdatedAt = fromMilliseconds(updatedAtMS)
	if err := value.Validate(); err != nil {
		return agent.AgentSession{}, fmt.Errorf("stored Agent Session is invalid: %w", err)
	}
	return value, nil
}

func normalizedRuntimeKind(value string) string {
	if value == "" {
		return "user_codex"
	}
	return value
}

func (s *Store) CreateAgentTurn(ctx context.Context, turn agent.AgentTurn) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("create Agent Turn: %w", err)
	}
	if err := validateContext(ctx); err != nil || turn.ValidateNew() != nil {
		return errors.New("create Agent Turn: invalid request")
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO agent_turns (
    id, agent_session_id, model_id, codex_turn_id, client_operation_id,
    focus_scene_id, focus_clip_id, focus_asset_id, status, dispatch_state,
    error_code, error_summary, revision, created_at_ms, started_at_ms, completed_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		turn.ID.String(), turn.AgentSessionID.String(), turn.ModelID, nullableString(turn.CodexTurnID), turn.ClientOperationID,
		nullableString(turn.Focus.SceneID.String()), nullableString(turn.Focus.ClipID.String()), nullableString(turn.Focus.AssetID.String()),
		string(turn.Status), string(turn.Dispatch), failureCode(turn.Failure), failureSummary(turn.Failure),
		turn.Revision, toMilliseconds(turn.CreatedAt), nullableTimeMilliseconds(turn.StartedAt), nullableTimeMilliseconds(turn.CompletedAt))
	if err != nil {
		return fmt.Errorf("create Agent Turn: %w: %v", ErrAgentRecordConflict, err)
	}
	return nil
}

func (s *Store) AgentTurn(ctx context.Context, id agent.TurnID) (agent.AgentTurn, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.AgentTurn{}, fmt.Errorf("read Agent Turn: %w", err)
	}
	if err := validateContext(ctx); err != nil || id.Validate() != nil {
		return agent.AgentTurn{}, errors.New("read Agent Turn: invalid request")
	}
	turn, err := scanAgentTurn(database.QueryRowContext(ctx, selectAgentTurnSQL+" WHERE id = ?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.AgentTurn{}, ErrAgentTurnNotFound
	}
	if err != nil {
		return agent.AgentTurn{}, fmt.Errorf("read Agent Turn: %w", err)
	}
	return turn, nil
}

func (s *Store) AgentTurns(ctx context.Context, sessionID agent.SessionID) ([]agent.AgentTurn, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Agent Turns: %w", err)
	}
	if err := validateContext(ctx); err != nil || sessionID.Validate() != nil {
		return nil, errors.New("list Agent Turns: invalid request")
	}
	rows, err := database.QueryContext(ctx, selectAgentTurnSQL+" WHERE agent_session_id = ? ORDER BY created_at_ms ASC, id ASC", sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("list Agent Turns: %w", err)
	}
	defer rows.Close()
	result := make([]agent.AgentTurn, 0)
	for rows.Next() {
		turn, err := scanAgentTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("list Agent Turns: %w", err)
		}
		result = append(result, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Agent Turns: %w", err)
	}
	return result, nil
}

// QueuedAgentTurns returns the globally oldest inputs that are safe to start
// only while their plaintext remains in the scheduler's process memory.
func (s *Store) QueuedAgentTurns(ctx context.Context, limit int) ([]agent.AgentTurn, error) {
	return s.agentTurnsByQuery(ctx, " WHERE status = 'queued' AND dispatch_state = 'queued' ORDER BY created_at_ms ASC, id ASC LIMIT ?", limit)
}

// AgentTurnsForRecovery returns every non-terminal Turn. Callers must
// reconcile or interrupt these records before dispatching fresh work.
func (s *Store) AgentTurnsForRecovery(ctx context.Context, limit int) ([]agent.AgentTurn, error) {
	return s.agentTurnsByQuery(ctx, " WHERE dispatch_state <> 'terminal' ORDER BY created_at_ms ASC, id ASC LIMIT ?", limit)
}

func (s *Store) agentTurnsByQuery(ctx context.Context, suffix string, limit int) ([]agent.AgentTurn, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list scheduled Agent Turns: %w", err)
	}
	if err := validateContext(ctx); err != nil || limit < 1 || limit > 10_000 {
		return nil, errors.New("list scheduled Agent Turns: invalid request")
	}
	rows, err := database.QueryContext(ctx, selectAgentTurnSQL+suffix, limit)
	if err != nil {
		return nil, fmt.Errorf("list scheduled Agent Turns: %w", err)
	}
	defer rows.Close()
	result := make([]agent.AgentTurn, 0)
	for rows.Next() {
		turn, err := scanAgentTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("list scheduled Agent Turns: %w", err)
		}
		result = append(result, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scheduled Agent Turns: %w", err)
	}
	return result, nil
}

func (s *Store) SaveAgentTurn(ctx context.Context, turn agent.AgentTurn, expectedRevision int64) error {
	if err := turn.Validate(); err != nil || expectedRevision < 1 || turn.Revision != expectedRevision+1 {
		return errors.New("save Agent Turn: invalid value or revision")
	}
	existing, err := s.AgentTurn(ctx, turn.ID)
	if err != nil {
		return err
	}
	if existing.Revision != expectedRevision {
		return ErrAgentRevisionConflict
	}
	if err := existing.ValidateReplacement(turn); err != nil {
		return ErrAgentRecordConflict
	}
	result, err := s.database.ExecContext(ctx, `
UPDATE agent_turns
SET codex_turn_id = ?, status = ?, dispatch_state = ?, error_code = ?, error_summary = ?,
    revision = ?, started_at_ms = ?, completed_at_ms = ?
WHERE id = ? AND revision = ?`, nullableString(turn.CodexTurnID), string(turn.Status), string(turn.Dispatch),
		failureCode(turn.Failure), failureSummary(turn.Failure), turn.Revision,
		nullableTimeMilliseconds(turn.StartedAt), nullableTimeMilliseconds(turn.CompletedAt),
		turn.ID.String(), expectedRevision)
	return mapAgentUpdateResult(result, err)
}

const selectAgentTurnSQL = `
SELECT id, agent_session_id, model_id, codex_turn_id, client_operation_id,
       focus_scene_id, focus_clip_id, focus_asset_id, status, dispatch_state,
       error_code, error_summary, revision, created_at_ms, started_at_ms, completed_at_ms
FROM agent_turns`

func scanAgentTurn(row rowScanner) (agent.AgentTurn, error) {
	var value agent.AgentTurn
	var id, sessionID, status, dispatch string
	var codexTurnID, sceneID, clipID, assetID, errorCode, errorSummary sql.NullString
	var createdAtMS int64
	var startedAtMS, completedAtMS sql.NullInt64
	if err := row.Scan(&id, &sessionID, &value.ModelID, &codexTurnID, &value.ClientOperationID,
		&sceneID, &clipID, &assetID, &status, &dispatch, &errorCode, &errorSummary,
		&value.Revision, &createdAtMS, &startedAtMS, &completedAtMS); err != nil {
		return value, err
	}
	value.ID = agent.TurnID(id)
	value.AgentSessionID = agent.SessionID(sessionID)
	value.Status = agent.TurnStatus(status)
	value.Dispatch = agent.DispatchState(dispatch)
	if codexTurnID.Valid {
		value.CodexTurnID = codexTurnID.String
	}
	if sceneID.Valid {
		value.Focus.SceneID = project.SceneID(sceneID.String)
	}
	if clipID.Valid {
		value.Focus.ClipID = project.ClipID(clipID.String)
	}
	if assetID.Valid {
		value.Focus.AssetID = project.AssetID(assetID.String)
	}
	if errorCode.Valid || errorSummary.Valid {
		value.Failure = &agent.Failure{Code: errorCode.String, Summary: errorSummary.String}
	}
	value.CreatedAt = fromMilliseconds(createdAtMS)
	if startedAtMS.Valid {
		at := fromMilliseconds(startedAtMS.Int64)
		value.StartedAt = &at
	}
	if completedAtMS.Valid {
		at := fromMilliseconds(completedAtMS.Int64)
		value.CompletedAt = &at
	}
	if err := value.Validate(); err != nil {
		return agent.AgentTurn{}, fmt.Errorf("stored Agent Turn is invalid: %w", err)
	}
	return value, nil
}

func (s *Store) RecordAgentInputOperation(ctx context.Context, operation agent.InputOperation) (agent.InputOperation, bool, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("record Agent input operation: %w", err)
	}
	if err := validateContext(ctx); err != nil || operation.Validate() != nil {
		return agent.InputOperation{}, false, errors.New("record Agent input operation: invalid request")
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO agent_input_operations (
    agent_session_id, client_operation_id, request_fingerprint, action, agent_turn_id, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, operation.AgentSessionID.String(),
		operation.ClientOperationID, operation.RequestFingerprint, string(operation.Action),
		operation.AgentTurnID.String(), toMilliseconds(operation.CreatedAt))
	if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("record Agent input operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("record Agent input operation: affected rows: %w", err)
	}
	if rows == 1 {
		return operation, true, nil
	}
	existing, err := s.AgentInputOperation(ctx, operation.AgentSessionID, operation.ClientOperationID)
	if err != nil {
		return agent.InputOperation{}, false, err
	}
	if existing.RequestFingerprint != operation.RequestFingerprint {
		return agent.InputOperation{}, false, ErrAgentInputIdempotencyConflict
	}
	return existing, false, nil
}

// AdmitAgentInput atomically resolves idempotency, chooses steer versus queue,
// enforces both queue bounds, and (for a queued action) creates the Turn. The
// input operation retains its display text for durable conversation rendering;
// the fingerprint remains the idempotency identity.
func (s *Store) AdmitAgentInput(ctx context.Context, candidate agent.AgentTurn, requestFingerprint, inputText, suggestedTitle string) (_ agent.InputOperation, _ bool, returnErr error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: %w", err)
	}
	probe := agent.InputOperation{AgentSessionID: candidate.AgentSessionID, ClientOperationID: candidate.ClientOperationID, RequestFingerprint: requestFingerprint, InputText: inputText, Action: agent.InputQueueTurn, AgentTurnID: candidate.ID, CreatedAt: candidate.CreatedAt}
	invalidTitle := suggestedTitle != "" && (suggestedTitle != strings.TrimSpace(suggestedTitle) ||
		utf8.RuneCountInString(suggestedTitle) > 300 || strings.ContainsAny(suggestedTitle, "\r\n\x00"))
	if err := validateContext(ctx); err != nil || candidate.ValidateNew() != nil || probe.Validate() != nil || invalidTitle {
		return agent.InputOperation{}, false, errors.New("admit Agent input: invalid request")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: begin transaction: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()

	existing, err := scanAgentInputOperation(tx.QueryRowContext(ctx, `
SELECT request_fingerprint, input_text, action, agent_turn_id, created_at_ms
FROM agent_input_operations WHERE agent_session_id = ? AND client_operation_id = ?`, candidate.AgentSessionID.String(), candidate.ClientOperationID), candidate.AgentSessionID, candidate.ClientOperationID)
	if err == nil {
		if existing.RequestFingerprint != requestFingerprint {
			return agent.InputOperation{}, false, ErrAgentInputIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: commit idempotent result: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: read idempotency record: %w", err)
	}

	var sessionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ?`, candidate.AgentSessionID.String()).Scan(&sessionStatus); errors.Is(err, sql.ErrNoRows) {
		return agent.InputOperation{}, false, ErrAgentSessionNotFound
	} else if err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: read Session: %w", err)
	}
	if agent.SessionStatus(sessionStatus) != agent.SessionActive {
		return agent.InputOperation{}, false, ErrAgentSessionInactive
	}

	var activeTurnID string
	err = tx.QueryRowContext(ctx, `
SELECT id FROM agent_turns
WHERE agent_session_id = ? AND status IN ('running', 'waiting_user')
ORDER BY created_at_ms ASC, id ASC LIMIT 1`, candidate.AgentSessionID.String()).Scan(&activeTurnID)
	if err == nil {
		operation := probe
		operation.Action = agent.InputSteerTurn
		operation.AgentTurnID = agent.TurnID(activeTurnID)
		if err := insertAgentInputOperation(ctx, tx, operation); err != nil {
			return agent.InputOperation{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: commit steer: %w", err)
		}
		return operation, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: read active Turn: %w", err)
	}

	var globalActive, globalQueued, sessionQueued int
	if err := tx.QueryRowContext(ctx, `SELECT
    count(*) FILTER (WHERE status IN ('running', 'waiting_user')),
    count(*) FILTER (WHERE status = 'queued')
FROM agent_turns`).Scan(&globalActive, &globalQueued); err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: count global Turns: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_turns WHERE agent_session_id = ? AND status = 'queued'`, candidate.AgentSessionID.String()).Scan(&sessionQueued); err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: count Session Turns: %w", err)
	}
	snapshot := agent.SchedulingSnapshot{GlobalActive: globalActive, GlobalQueued: globalQueued, SessionQueued: sessionQueued}
	decision, err := snapshot.AdmitInput()
	if err != nil {
		return agent.InputOperation{}, false, err
	}
	if decision != agent.AdmissionQueueTurn {
		return agent.InputOperation{}, false, agent.ErrInvalidSchedulingSnapshot
	}
	if suggestedTitle != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE agent_sessions
SET cached_title = ?, revision = revision + 1, updated_at_ms = ?
WHERE id = ? AND NOT EXISTS (
    SELECT 1 FROM agent_turns WHERE agent_session_id = ?
)`, suggestedTitle, toMilliseconds(candidate.CreatedAt), candidate.AgentSessionID.String(), candidate.AgentSessionID.String()); err != nil {
			return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: cache first-input title: %w", err)
		}
	}
	if err := insertAgentTurn(ctx, tx, candidate); err != nil {
		return agent.InputOperation{}, false, err
	}
	if err := insertAgentInputOperation(ctx, tx, probe); err != nil {
		return agent.InputOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agent.InputOperation{}, false, fmt.Errorf("admit Agent input: commit queued Turn: %w", err)
	}
	return probe, true, nil
}

func insertAgentTurn(ctx context.Context, tx *sql.Tx, turn agent.AgentTurn) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO agent_turns (
    id, agent_session_id, model_id, codex_turn_id, client_operation_id,
    focus_scene_id, focus_clip_id, focus_asset_id, status, dispatch_state,
    error_code, error_summary, revision, created_at_ms, started_at_ms, completed_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		turn.ID.String(), turn.AgentSessionID.String(), turn.ModelID, nullableString(turn.CodexTurnID), turn.ClientOperationID,
		nullableString(turn.Focus.SceneID.String()), nullableString(turn.Focus.ClipID.String()), nullableString(turn.Focus.AssetID.String()),
		string(turn.Status), string(turn.Dispatch), failureCode(turn.Failure), failureSummary(turn.Failure),
		turn.Revision, toMilliseconds(turn.CreatedAt), nullableTimeMilliseconds(turn.StartedAt), nullableTimeMilliseconds(turn.CompletedAt))
	if err != nil {
		return fmt.Errorf("admit Agent input: insert Turn: %w: %v", ErrAgentRecordConflict, err)
	}
	return nil
}

func insertAgentInputOperation(ctx context.Context, tx *sql.Tx, operation agent.InputOperation) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO agent_input_operations (
    agent_session_id, client_operation_id, request_fingerprint, input_text, action, agent_turn_id, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?)`, operation.AgentSessionID.String(), operation.ClientOperationID,
		operation.RequestFingerprint, nullableString(operation.InputText), string(operation.Action), operation.AgentTurnID.String(), toMilliseconds(operation.CreatedAt))
	if err != nil {
		return fmt.Errorf("admit Agent input: insert idempotency record: %w: %v", ErrAgentRecordConflict, err)
	}
	return nil
}

func (s *Store) AgentInputOperation(ctx context.Context, sessionID agent.SessionID, clientOperationID string) (agent.InputOperation, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.InputOperation{}, fmt.Errorf("read Agent input operation: %w", err)
	}
	if err := validateContext(ctx); err != nil || sessionID.Validate() != nil || clientOperationID == "" {
		return agent.InputOperation{}, errors.New("read Agent input operation: invalid request")
	}
	value, err := scanAgentInputOperation(database.QueryRowContext(ctx, `
SELECT request_fingerprint, input_text, action, agent_turn_id, created_at_ms
FROM agent_input_operations WHERE agent_session_id = ? AND client_operation_id = ?`,
		sessionID.String(), clientOperationID), sessionID, clientOperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.InputOperation{}, ErrAgentInputOperationNotFound
	}
	if err != nil {
		return agent.InputOperation{}, fmt.Errorf("read Agent input operation: %w", err)
	}
	return value, nil
}

func (s *Store) AgentInputOperations(ctx context.Context, sessionID agent.SessionID) ([]agent.InputOperation, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Agent input operations: %w", err)
	}
	if err := validateContext(ctx); err != nil || sessionID.Validate() != nil {
		return nil, errors.New("list Agent input operations: invalid request")
	}
	rows, err := database.QueryContext(ctx, `
SELECT client_operation_id, request_fingerprint, input_text, action, agent_turn_id, created_at_ms
FROM agent_input_operations WHERE agent_session_id = ?
ORDER BY created_at_ms ASC, client_operation_id ASC`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("list Agent input operations: %w", err)
	}
	defer rows.Close()
	result := make([]agent.InputOperation, 0)
	for rows.Next() {
		var value agent.InputOperation
		var inputText sql.NullString
		var action, turnID string
		var createdAtMS int64
		if err := rows.Scan(&value.ClientOperationID, &value.RequestFingerprint, &inputText, &action, &turnID, &createdAtMS); err != nil {
			return nil, fmt.Errorf("list Agent input operations: %w", err)
		}
		value.AgentSessionID = sessionID
		value.Action = agent.InputAction(action)
		value.AgentTurnID = agent.TurnID(turnID)
		value.CreatedAt = fromMilliseconds(createdAtMS)
		if inputText.Valid {
			value.InputText = inputText.String
		}
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("list Agent input operations: stored value is invalid: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Agent input operations: %w", err)
	}
	return result, nil
}

func scanAgentInputOperation(row rowScanner, sessionID agent.SessionID, clientOperationID string) (agent.InputOperation, error) {
	var value agent.InputOperation
	var inputText sql.NullString
	var action, turnID string
	var createdAtMS int64
	if err := row.Scan(&value.RequestFingerprint, &inputText, &action, &turnID, &createdAtMS); err != nil {
		return agent.InputOperation{}, err
	}
	value.AgentSessionID = sessionID
	value.ClientOperationID = clientOperationID
	if inputText.Valid {
		value.InputText = inputText.String
	}
	value.Action = agent.InputAction(action)
	value.AgentTurnID = agent.TurnID(turnID)
	value.CreatedAt = fromMilliseconds(createdAtMS)
	if err := value.Validate(); err != nil {
		return agent.InputOperation{}, fmt.Errorf("stored Agent input operation is invalid: %w", err)
	}
	return value, nil
}

func (s *Store) CreateAgentMCPBinding(ctx context.Context, binding agent.MCPBinding) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("create Agent MCP Binding: %w", err)
	}
	if err := validateContext(ctx); err != nil || binding.ValidateNew() != nil {
		return errors.New("create Agent MCP Binding: invalid request")
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO agent_mcp_bindings (
    id, agent_session_id, workspace_path, route_key, status, error_code,
    error_summary, revision, last_verified_at_ms, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, binding.ID.String(), binding.AgentSessionID.String(),
		binding.WorkspacePath, binding.RouteKey, string(binding.Status), failureCode(binding.Failure),
		failureSummary(binding.Failure), binding.Revision, nullableTimeMilliseconds(binding.LastVerifiedAt),
		toMilliseconds(binding.CreatedAt), toMilliseconds(binding.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create Agent MCP Binding: %w: %v", ErrAgentRecordConflict, err)
	}
	return nil
}

func (s *Store) AgentMCPBinding(ctx context.Context, sessionID agent.SessionID) (agent.MCPBinding, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return agent.MCPBinding{}, fmt.Errorf("read Agent MCP Binding: %w", err)
	}
	if err := validateContext(ctx); err != nil || sessionID.Validate() != nil {
		return agent.MCPBinding{}, errors.New("read Agent MCP Binding: invalid request")
	}
	value, err := scanAgentMCPBinding(database.QueryRowContext(ctx, selectAgentMCPBindingSQL+" WHERE agent_session_id = ?", sessionID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.MCPBinding{}, ErrAgentMCPBindingNotFound
	}
	if err != nil {
		return agent.MCPBinding{}, fmt.Errorf("read Agent MCP Binding: %w", err)
	}
	return value, nil
}

func (s *Store) SaveAgentMCPBinding(ctx context.Context, binding agent.MCPBinding, expectedRevision int64) error {
	if err := binding.Validate(); err != nil || expectedRevision < 1 || binding.Revision != expectedRevision+1 {
		return errors.New("save Agent MCP Binding: invalid value or revision")
	}
	existing, err := s.AgentMCPBinding(ctx, binding.AgentSessionID)
	if err != nil {
		return err
	}
	if existing.Revision != expectedRevision {
		return ErrAgentRevisionConflict
	}
	if err := existing.ValidateReplacement(binding); err != nil {
		return ErrAgentRecordConflict
	}
	result, err := s.database.ExecContext(ctx, `
UPDATE agent_mcp_bindings
SET status = ?, error_code = ?, error_summary = ?, revision = ?,
    last_verified_at_ms = ?, updated_at_ms = ?
WHERE id = ? AND revision = ?`, string(binding.Status), failureCode(binding.Failure),
		failureSummary(binding.Failure), binding.Revision, nullableTimeMilliseconds(binding.LastVerifiedAt),
		toMilliseconds(binding.UpdatedAt), binding.ID.String(), expectedRevision)
	return mapAgentUpdateResult(result, err)
}

const selectAgentMCPBindingSQL = `
SELECT id, agent_session_id, workspace_path, route_key, status, error_code,
       error_summary, revision, last_verified_at_ms, created_at_ms, updated_at_ms
FROM agent_mcp_bindings`

func scanAgentMCPBinding(row rowScanner) (agent.MCPBinding, error) {
	var value agent.MCPBinding
	var id, sessionID, status string
	var errorCode, errorSummary sql.NullString
	var lastVerifiedAtMS sql.NullInt64
	var createdAtMS, updatedAtMS int64
	if err := row.Scan(&id, &sessionID, &value.WorkspacePath, &value.RouteKey, &status,
		&errorCode, &errorSummary, &value.Revision, &lastVerifiedAtMS, &createdAtMS, &updatedAtMS); err != nil {
		return value, err
	}
	value.ID = agent.MCPBindingID(id)
	value.AgentSessionID = agent.SessionID(sessionID)
	value.Status = agent.MCPBindingStatus(status)
	if errorCode.Valid || errorSummary.Valid {
		value.Failure = &agent.Failure{Code: errorCode.String, Summary: errorSummary.String}
	}
	if lastVerifiedAtMS.Valid {
		at := fromMilliseconds(lastVerifiedAtMS.Int64)
		value.LastVerifiedAt = &at
	}
	value.CreatedAt = fromMilliseconds(createdAtMS)
	value.UpdatedAt = fromMilliseconds(updatedAtMS)
	if err := value.Validate(); err != nil {
		return agent.MCPBinding{}, fmt.Errorf("stored Agent MCP Binding is invalid: %w", err)
	}
	return value, nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func failureCode(failure *agent.Failure) any {
	if failure == nil {
		return nil
	}
	return failure.Code
}

func failureSummary(failure *agent.Failure) any {
	if failure == nil {
		return nil
	}
	return failure.Summary
}

func mapAgentUpdateResult(result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("save Agent record: %w: %v", ErrAgentRecordConflict, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save Agent record: affected rows: %w", err)
	}
	if rows != 1 {
		return ErrAgentRevisionConflict
	}
	return nil
}
