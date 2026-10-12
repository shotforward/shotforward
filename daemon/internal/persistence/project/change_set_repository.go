package projectstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrChangeSetNotFound           = errors.New("ChangeSet not found")
	ErrChangeSetRevisionConflict   = errors.New("ChangeSet revision conflict")
	ErrChangeSetStateConflict      = errors.New("ChangeSet state conflict")
	ErrChangeSetOperationConflict  = errors.New("ChangeSet operation key was reused with different input")
	ErrChangeSetPreconditionFailed = errors.New("ChangeSet precondition failed")
	ErrChangeSetConflicted         = errors.New("ChangeSet conflicted")
	ErrChangeSetBatchInvalid       = errors.New("ChangeSet operation batch is invalid")
)

func (s *Store) CreateChangeSet(ctx context.Context, value changeset.ChangeSet, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	if value.ValidateNew() != nil || receipt.Validate() != nil || receipt.Command != changeset.CommandBegin || receipt.ProjectID != value.ProjectID || receipt.ChangeSetID != value.ID || !receipt.CreatedAt.Equal(value.CreatedAt) {
		return changeset.ChangeSet{}, false, errors.New("create ChangeSet: invalid request")
	}
	var result changeset.ChangeSet
	var replay bool
	err := s.withTransaction(ctx, "create ChangeSet", func(tx *sql.Tx) error {
		found, existingID, err := replayBeginReceipt(ctx, tx, receipt)
		if err != nil {
			return err
		}
		if found {
			result, err = transactionChangeSet(ctx, tx, existingID)
			replay = true
			return err
		}
		projectID, err := transactionProjectID(ctx, tx)
		if err != nil {
			return err
		}
		if projectID != value.ProjectID {
			return errors.New("ChangeSet belongs to another Project")
		}
		if err := validateEpisodeOwner(ctx, tx, value.EpisodeID, value.ProjectID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO change_sets (change_set_id,project_id,episode_id,agent_session_id,agent_turn_id,codex_thread_id,codex_turn_id,title,summary,status,operation_count,revision,failure_code,failure_summary,created_at_ms,updated_at_ms,finalized_at_ms,resolved_at_ms) VALUES (?,?,?,?,?,?,?,?,?,'draft',0,1,'','',?,?,NULL,NULL)`, value.ID.String(), value.ProjectID.String(), value.EpisodeID.String(), value.Source.AgentSessionID.String(), value.Source.AgentTurnID.String(), value.Source.CodexThreadID, value.Source.CodexTurnID, value.Title, value.Summary, value.CreatedAt.UnixMilli(), value.UpdatedAt.UnixMilli())
		if err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		result = value
		return nil
	})
	return result, replay, err
}

func (s *Store) ChangeSet(ctx context.Context, id changeset.ChangeSetID) (changeset.ChangeSet, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return changeset.ChangeSet{}, fmt.Errorf("read ChangeSet: %w", err)
	}
	if validateContext(ctx) != nil || id.Validate() != nil {
		return changeset.ChangeSet{}, errors.New("read ChangeSet: invalid request")
	}
	value, err := scanChangeSet(database.QueryRowContext(ctx, changeSetSelect+` WHERE change_set_id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return changeset.ChangeSet{}, ErrChangeSetNotFound
	}
	if err != nil {
		return changeset.ChangeSet{}, fmt.Errorf("read ChangeSet: %w", err)
	}
	return value, nil
}

func (s *Store) ChangeSetsForAgentSession(ctx context.Context, projectID project.ProjectID, sessionID agent.SessionID) ([]changeset.ChangeSet, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Agent Session ChangeSets: %w", err)
	}
	if validateContext(ctx) != nil || projectID.Validate() != nil || sessionID.Validate() != nil {
		return nil, errors.New("list Agent Session ChangeSets: invalid request")
	}
	rows, err := database.QueryContext(ctx, changeSetSelect+` WHERE project_id=? AND agent_session_id=? AND status <> 'draft'
AND change_set_id IN (
  SELECT recent.change_set_id FROM change_sets recent
  WHERE recent.project_id=? AND recent.status <> 'draft'
  ORDER BY recent.updated_at_ms DESC,recent.change_set_id DESC LIMIT 200
)
ORDER BY updated_at_ms,change_set_id`, projectID.String(), sessionID.String(), projectID.String())
	if err != nil {
		return nil, fmt.Errorf("list Agent Session ChangeSets: %w", err)
	}
	defer rows.Close()
	result := make([]changeset.ChangeSet, 0)
	for rows.Next() {
		value, err := scanChangeSet(rows)
		if err != nil {
			return nil, fmt.Errorf("list Agent Session ChangeSets: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Agent Session ChangeSets: %w", err)
	}
	return result, nil
}

func (s *Store) AppendChangeSetOperations(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, operations []changeset.Operation, updatedAt time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	if id.Validate() != nil || expectedRevision < 1 || len(operations) == 0 || len(operations) > changeset.MaxAppendBatch || validateMutationTime(updatedAt) != nil || receipt.Validate() != nil || receipt.Command != changeset.CommandAppend || receipt.ChangeSetID != id || !receipt.CreatedAt.Equal(updatedAt) {
		return changeset.ChangeSet{}, false, ErrChangeSetBatchInvalid
	}
	for _, operation := range operations {
		if operation.Validate() != nil || operation.ChangeSetID != id {
			return changeset.ChangeSet{}, false, ErrChangeSetBatchInvalid
		}
	}
	var result changeset.ChangeSet
	var replay bool
	err := s.withTransaction(ctx, "append ChangeSet operations", func(tx *sql.Tx) error {
		found, err := replayReceipt(ctx, tx, receipt)
		if err != nil {
			return err
		}
		if found {
			result, err = transactionChangeSet(ctx, tx, id)
			replay = true
			return err
		}
		current, err := transactionChangeSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.ProjectID != receipt.ProjectID {
			return errors.New("ChangeSet belongs to another Project")
		}
		if current.Status != changeset.StatusDraft {
			return ErrChangeSetStateConflict
		}
		if current.Revision != expectedRevision {
			return ErrChangeSetRevisionConflict
		}
		if current.OperationCount+len(operations) > changeset.MaxOperations {
			return ErrChangeSetBatchInvalid
		}
		seen := make(map[string]struct{}, len(operations))
		for index, operation := range operations {
			if operation.Position != current.OperationCount+index+1 {
				return ErrChangeSetBatchInvalid
			}
			if _, exists := seen[operation.TargetKey()]; exists {
				return ErrChangeSetBatchInvalid
			}
			seen[operation.TargetKey()] = struct{}{}
			operation, err = resolveDormantAssetIdentity(ctx, tx, current.ProjectID, operation)
			if err != nil {
				return err
			}
			if conflict, err := validateOperationCurrent(ctx, tx, current, operation); err != nil {
				return err
			} else if conflict != nil {
				return fmt.Errorf("%w: %s", ErrChangeSetPreconditionFailed, conflict.code)
			}
			if err := insertChangeSetOperation(ctx, tx, operation, updatedAt); err != nil {
				return err
			}
		}
		resultCount := current.OperationCount + len(operations)
		write, err := tx.ExecContext(ctx, `UPDATE change_sets SET operation_count=?,revision=revision+1,updated_at_ms=? WHERE change_set_id=? AND status='draft' AND revision=?`, resultCount, updatedAt.UnixMilli(), id.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetRevisionConflict); err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		result, err = transactionChangeSet(ctx, tx, id)
		return err
	})
	return result, replay, err
}

func (s *Store) FinalizeChangeSet(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	if id.Validate() != nil || expectedRevision < 1 || validateMutationTime(at) != nil || receipt.Validate() != nil || receipt.Command != changeset.CommandFinalize || receipt.ChangeSetID != id || !receipt.CreatedAt.Equal(at) {
		return changeset.ChangeSet{}, false, errors.New("finalize ChangeSet: invalid request")
	}
	var result changeset.ChangeSet
	var replay bool
	err := s.withTransaction(ctx, "finalize ChangeSet", func(tx *sql.Tx) error {
		found, err := replayReceipt(ctx, tx, receipt)
		if err != nil {
			return err
		}
		if found {
			result, err = transactionChangeSet(ctx, tx, id)
			replay = true
			return err
		}
		current, err := transactionChangeSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.ProjectID != receipt.ProjectID {
			return errors.New("ChangeSet belongs to another Project")
		}
		if current.Status != changeset.StatusDraft || current.OperationCount == 0 {
			return ErrChangeSetStateConflict
		}
		if current.Revision != expectedRevision {
			return ErrChangeSetRevisionConflict
		}
		operations, err := transactionChangeSetOperations(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(operations) != current.OperationCount {
			return errors.New("ChangeSet operation count is inconsistent")
		}
		for _, operation := range operations {
			if conflict, err := validateOperationCurrent(ctx, tx, current, operation); err != nil {
				return err
			} else if conflict != nil {
				return fmt.Errorf("%w: %s", ErrChangeSetPreconditionFailed, conflict.code)
			}
		}
		conflict, err := validateStoryboardProposal(ctx, tx, current, operations)
		if err != nil {
			return err
		}
		if conflict != nil {
			return fmt.Errorf("%w: %s", ErrChangeSetPreconditionFailed, conflict.code)
		}
		if _, _, err := deriveHistoryScope(ctx, tx, current, operations); err != nil {
			return fmt.Errorf("%w: ChangeSet operations must have one semantic scope", ErrChangeSetPreconditionFailed)
		}
		write, err := tx.ExecContext(ctx, `UPDATE change_sets SET status='pending',revision=revision+1,updated_at_ms=?,finalized_at_ms=? WHERE change_set_id=? AND status='draft' AND revision=?`, at.UnixMilli(), at.UnixMilli(), id.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetRevisionConflict); err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		result, err = transactionChangeSet(ctx, tx, id)
		return err
	})
	return result, replay, err
}

func (s *Store) ApplyChangeSet(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, conflictID changeset.ConflictID, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	if id.Validate() != nil || conflictID.Validate() != nil || expectedRevision < 1 || validateMutationTime(at) != nil || receipt.Validate() != nil || receipt.Command != changeset.CommandApply || receipt.ChangeSetID != id || !receipt.CreatedAt.Equal(at) {
		return changeset.ChangeSet{}, false, errors.New("apply ChangeSet: invalid request")
	}
	var result changeset.ChangeSet
	var replay, conflicted bool
	err := s.withTransaction(ctx, "apply ChangeSet", func(tx *sql.Tx) error {
		found, err := replayReceipt(ctx, tx, receipt)
		if err != nil {
			return err
		}
		if found {
			result, err = transactionChangeSet(ctx, tx, id)
			replay = true
			conflicted = result.Status == changeset.StatusConflicted
			return err
		}
		current, err := transactionChangeSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.ProjectID != receipt.ProjectID {
			return errors.New("ChangeSet belongs to another Project")
		}
		if current.Status != changeset.StatusPending {
			return ErrChangeSetStateConflict
		}
		if current.Revision != expectedRevision {
			return ErrChangeSetRevisionConflict
		}
		operations, err := transactionChangeSetOperations(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, operation := range operations {
			conflict, err := validateOperationCurrent(ctx, tx, current, operation)
			if err != nil {
				return err
			}
			if conflict != nil {
				if err := recordChangeSetConflict(ctx, tx, current, operation.ID, conflictID, conflict.code, conflict.summary, expectedRevision, at); err != nil {
					return err
				}
				if err := insertReceipt(ctx, tx, receipt); err != nil {
					return err
				}
				result, err = transactionChangeSet(ctx, tx, id)
				conflicted = true
				return err
			}
		}
		conflict, err := validateStoryboardProposal(ctx, tx, current, operations)
		if err != nil {
			return err
		}
		if conflict != nil {
			if err := recordChangeSetConflict(ctx, tx, current, "", conflictID, conflict.code, conflict.summary, expectedRevision, at); err != nil {
				return err
			}
			result, err = transactionChangeSet(ctx, tx, id)
			conflicted = true
			return err
		}
		scope, historyResources, err := deriveHistoryScope(ctx, tx, current, operations)
		if err != nil {
			return err
		}
		beforeGuard, err := scopeGuardJSON(ctx, tx, scope, at)
		if err != nil {
			return err
		}
		isRevert := len(operations) == 1 && operations[0].ChangeSetRevert != nil
		if !isRevert {
			if err := prepareHistoryResources(ctx, tx, current.ID, historyResources, at); err != nil {
				return err
			}
		}
		for _, operation := range operations {
			if err := applyChangeSetOperation(ctx, tx, operation, at); err != nil {
				return err
			}
		}
		if hasStoryboardOperations(operations) {
			if err := finishStoryboardChangeSetApply(ctx, tx, current, operations, at); err != nil {
				return err
			}
		}
		if !isRevert {
			if err := activateHistoryResources(ctx, tx, current.ID, at); err != nil {
				return err
			}
			afterGuard, err := scopeGuardJSON(ctx, tx, scope, at)
			if err != nil {
				return err
			}
			if err := recordChangeSetScope(ctx, tx, current.ID, scope, beforeGuard, afterGuard, at); err != nil {
				return err
			}
		}
		if err := updateProjectTimestamp(ctx, tx, current.ProjectID, at); err != nil {
			return err
		}
		write, err := tx.ExecContext(ctx, `UPDATE change_sets SET status='applied',revision=revision+1,updated_at_ms=?,resolved_at_ms=? WHERE change_set_id=? AND status='pending' AND revision=?`, at.UnixMilli(), at.UnixMilli(), id.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetRevisionConflict); err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		result, err = transactionChangeSet(ctx, tx, id)
		return err
	})
	if err == nil && conflicted {
		err = ErrChangeSetConflicted
	}
	return result, replay, err
}

func (s *Store) RejectChangeSet(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, reason string, at time.Time, receipt changeset.CommandReceipt) (changeset.ChangeSet, bool, error) {
	if id.Validate() != nil || expectedRevision < 1 || validateMutationTime(at) != nil || receipt.Validate() != nil || receipt.Command != changeset.CommandReject || receipt.ChangeSetID != id || !receipt.CreatedAt.Equal(at) || !validChangeSetSummary(reason, true) {
		return changeset.ChangeSet{}, false, errors.New("reject ChangeSet: invalid request")
	}
	var result changeset.ChangeSet
	var replay bool
	err := s.withTransaction(ctx, "reject ChangeSet", func(tx *sql.Tx) error {
		found, err := replayReceipt(ctx, tx, receipt)
		if err != nil {
			return err
		}
		if found {
			result, err = transactionChangeSet(ctx, tx, id)
			replay = true
			return err
		}
		current, err := transactionChangeSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.ProjectID != receipt.ProjectID {
			return errors.New("ChangeSet belongs to another Project")
		}
		if current.Status != changeset.StatusPending {
			return ErrChangeSetStateConflict
		}
		if current.Revision != expectedRevision {
			return ErrChangeSetRevisionConflict
		}
		write, err := tx.ExecContext(ctx, `UPDATE change_sets SET status='rejected',revision=revision+1,failure_code='user_rejected',failure_summary=?,updated_at_ms=?,resolved_at_ms=? WHERE change_set_id=? AND status='pending' AND revision=?`, reason, at.UnixMilli(), at.UnixMilli(), id.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetRevisionConflict); err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		result, err = transactionChangeSet(ctx, tx, id)
		return err
	})
	return result, replay, err
}

func (s *Store) DiscardChangeSetDraft(ctx context.Context, id changeset.ChangeSetID) error {
	if validateContext(ctx) != nil || id.Validate() != nil {
		return errors.New("discard ChangeSet draft: invalid request")
	}
	return s.withTransaction(ctx, "discard ChangeSet draft", func(tx *sql.Tx) error {
		current, err := transactionChangeSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != changeset.StatusDraft {
			return ErrChangeSetStateConflict
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM change_sets WHERE change_set_id=? AND status='draft'`, id.String())
		if err != nil {
			return err
		}
		return requireOne(result, ErrChangeSetStateConflict)
	})
}

func (s *Store) DiscardChangeSetDraftsForTurn(ctx context.Context, turnID agent.TurnID) error {
	if validateContext(ctx) != nil || turnID.Validate() != nil {
		return errors.New("discard ChangeSet drafts for Turn: invalid request")
	}
	return s.withTransaction(ctx, "discard ChangeSet drafts for Turn", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM change_sets WHERE agent_turn_id=? AND status='draft'`, turnID.String())
		return err
	})
}

func (s *Store) ChangeSetOperations(ctx context.Context, id changeset.ChangeSetID, afterPosition, limit int) ([]changeset.Operation, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	if validateContext(ctx) != nil || id.Validate() != nil || afterPosition < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("list ChangeSet Operations: invalid request")
	}
	if _, err := s.ChangeSet(ctx, id); err != nil {
		return nil, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	values, err := transactionChangeSetOperations(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	result := make([]changeset.Operation, 0, min(limit, len(values)))
	for _, value := range values {
		if value.Position > afterPosition && len(result) < limit {
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Store) ChangeSetConflicts(ctx context.Context, id changeset.ChangeSetID) ([]changeset.Conflict, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	if validateContext(ctx) != nil || id.Validate() != nil {
		return nil, errors.New("list ChangeSet Conflicts: invalid request")
	}
	if _, err := s.ChangeSet(ctx, id); err != nil {
		return nil, err
	}
	rows, err := database.QueryContext(ctx, `SELECT conflict_id,change_set_id,operation_id,code,summary,detected_at_ms FROM change_set_conflicts WHERE change_set_id=? ORDER BY detected_at_ms,conflict_id`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []changeset.Conflict
	for rows.Next() {
		var idValue, changeSetValue, code, summary string
		var operationValue sql.NullString
		var detected int64
		if err := rows.Scan(&idValue, &changeSetValue, &operationValue, &code, &summary, &detected); err != nil {
			return nil, err
		}
		value := changeset.Conflict{ID: changeset.ConflictID(idValue), ChangeSetID: changeset.ChangeSetID(changeSetValue), OperationID: changeset.OperationID(operationValue.String), Code: code, Summary: summary, DetectedAt: time.UnixMilli(detected).UTC()}
		if value.Validate() != nil {
			return nil, ErrInvalidStoredProjectData
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

const changeSetSelect = `SELECT cs.change_set_id,cs.project_id,cs.episode_id,cs.agent_session_id,cs.agent_turn_id,cs.codex_thread_id,cs.codex_turn_id,cs.title,cs.summary,cs.status,cs.operation_count,cs.revision,cs.failure_code,cs.failure_summary,cs.created_at_ms,cs.updated_at_ms,cs.finalized_at_ms,cs.resolved_at_ms,
COALESCE((SELECT original_change_set_id FROM change_set_revert_operations r WHERE r.change_set_id=cs.change_set_id),''),
COALESCE((SELECT r.change_set_id FROM change_set_revert_operations r JOIN change_sets inverse ON inverse.change_set_id=r.change_set_id WHERE r.original_change_set_id=cs.change_set_id AND inverse.status='applied'),'')
FROM change_sets cs`

func transactionChangeSet(ctx context.Context, tx *sql.Tx, id changeset.ChangeSetID) (changeset.ChangeSet, error) {
	value, err := scanChangeSet(tx.QueryRowContext(ctx, changeSetSelect+` WHERE change_set_id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return changeset.ChangeSet{}, ErrChangeSetNotFound
	}
	return value, err
}

func scanChangeSet(row rowScanner) (changeset.ChangeSet, error) {
	var id, projectID, episodeID, sessionID, turnID, codexThread, codexTurn, title, summary, status, failureCode, failureSummary, revertsID, revertedByID string
	var count int
	var revision, created, updated int64
	var finalized, resolved sql.NullInt64
	if err := row.Scan(&id, &projectID, &episodeID, &sessionID, &turnID, &codexThread, &codexTurn, &title, &summary, &status, &count, &revision, &failureCode, &failureSummary, &created, &updated, &finalized, &resolved, &revertsID, &revertedByID); err != nil {
		return changeset.ChangeSet{}, err
	}
	value := changeset.ChangeSet{ID: changeset.ChangeSetID(id), ProjectID: project.ProjectID(projectID), EpisodeID: project.EpisodeID(episodeID), Source: changeset.Source{AgentSessionID: agentSessionID(sessionID), AgentTurnID: agentTurnID(turnID), CodexThreadID: codexThread, CodexTurnID: codexTurn}, Title: title, Summary: summary, Status: changeset.Status(status), OperationCount: count, Revision: revision, FailureCode: failureCode, FailureSummary: failureSummary, CreatedAt: time.UnixMilli(created).UTC(), UpdatedAt: time.UnixMilli(updated).UTC(), RevertsChangeSetID: changeset.ChangeSetID(revertsID), RevertedByChangeSetID: changeset.ChangeSetID(revertedByID)}
	if finalized.Valid {
		at := time.UnixMilli(finalized.Int64).UTC()
		value.FinalizedAt = &at
	}
	if resolved.Valid {
		at := time.UnixMilli(resolved.Int64).UTC()
		value.ResolvedAt = &at
	}
	if value.Validate() != nil {
		return changeset.ChangeSet{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

// Keep cross-database source IDs strongly typed without adding foreign keys to project.db.
func agentSessionID(value string) agent.SessionID { return agent.SessionID(value) }
func agentTurnID(value string) agent.TurnID       { return agent.TurnID(value) }

const operationSelect = `SELECT o.operation_id,o.change_set_id,o.position,o.operation_type,o.target_asset_id,o.target_clip_id,o.asset_prompt_before,o.asset_prompt_after,o.clip_first_before,o.clip_first_after,o.clip_last_before,o.clip_last_after,o.clip_video_mode,o.clip_video_before,o.clip_video_after,o.clip_asset_ids_before_json,o.clip_asset_ids_after_json,o.clip_asset_bindings_recorded,o.clip_reference_artifact_ids_before_json,o.clip_reference_artifact_ids_after_json,o.clip_reference_bindings_recorded,p.precondition_id,p.target_kind,p.target_id,p.expected_revision FROM change_set_operations o JOIN change_set_preconditions p ON p.operation_id=o.operation_id AND p.change_set_id=o.change_set_id`

func transactionChangeSetOperations(ctx context.Context, tx *sql.Tx, id changeset.ChangeSetID) ([]changeset.Operation, error) {
	rows, err := tx.QueryContext(ctx, operationSelect+` WHERE o.change_set_id=? ORDER BY o.position`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []changeset.Operation
	for rows.Next() {
		value, err := scanChangeSetOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	creations, err := tx.QueryContext(ctx, assetCreationSelect+` WHERE change_set_id=? ORDER BY position`, id.String())
	if err != nil {
		return nil, err
	}
	defer creations.Close()
	for creations.Next() {
		value, err := scanAssetCreationOperation(creations)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := creations.Err(); err != nil {
		return nil, err
	}
	creations.Close()
	restorations, err := tx.QueryContext(ctx, assetRestorationSelect+` WHERE change_set_id=? ORDER BY position`, id.String())
	if err != nil {
		return nil, err
	}
	for restorations.Next() {
		value, err := scanAssetRestorationOperation(restorations)
		if err != nil {
			restorations.Close()
			return nil, err
		}
		result = append(result, value)
	}
	if err := restorations.Err(); err != nil {
		restorations.Close()
		return nil, err
	}
	restorations.Close()
	storyboardRows, err := tx.QueryContext(ctx, storyboardCreationSelect+` WHERE change_set_id=? ORDER BY position`, id.String())
	if err != nil {
		return nil, err
	}
	defer storyboardRows.Close()
	for storyboardRows.Next() {
		value, err := scanStoryboardCreationOperation(storyboardRows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := storyboardRows.Err(); err != nil {
		return nil, err
	}
	storyboardRows.Close()
	scriptRows, err := tx.QueryContext(ctx, scriptOperationSelect+` WHERE change_set_id=? ORDER BY position`, id.String())
	if err != nil {
		return nil, err
	}
	for scriptRows.Next() {
		value, err := scanScriptOperation(scriptRows)
		if err != nil {
			scriptRows.Close()
			return nil, err
		}
		result = append(result, value)
	}
	if err := scriptRows.Err(); err != nil {
		scriptRows.Close()
		return nil, err
	}
	scriptRows.Close()
	revertRows, err := tx.QueryContext(ctx, revertOperationSelect+` WHERE change_set_id=?`, id.String())
	if err != nil {
		return nil, err
	}
	for revertRows.Next() {
		value, err := scanRevertOperation(revertRows)
		if err != nil {
			revertRows.Close()
			return nil, err
		}
		result = append(result, value)
	}
	if err := revertRows.Err(); err != nil {
		revertRows.Close()
		return nil, err
	}
	revertRows.Close()
	mergeRows, err := tx.QueryContext(ctx, clipMergeOperationSelect+` WHERE change_set_id=?`, id.String())
	if err != nil {
		return nil, err
	}
	for mergeRows.Next() {
		value, err := scanClipMergeOperation(mergeRows)
		if err != nil {
			mergeRows.Close()
			return nil, err
		}
		result = append(result, value)
	}
	if err := mergeRows.Err(); err != nil {
		mergeRows.Close()
		return nil, err
	}
	mergeRows.Close()
	sort.Slice(result, func(i, j int) bool { return result[i].Position < result[j].Position })
	return result, nil
}

const clipMergeOperationSelect = `SELECT operation_id,change_set_id,position,precondition_id,episode_id,expected_storyboard_revision,payload_json FROM change_set_clip_merge_operations`

func scanClipMergeOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, episodeID, payload string
	var position int
	var expected int64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &episodeID, &expected, &payload); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position,
		Precondition: changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetStoryboard, TargetID: episodeID, ExpectedRevision: expected}}
	var probe struct {
		Source json.RawMessage
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	if len(probe.Source) > 0 {
		value.Type, value.ClipSplit = changeset.SplitClip, &changeset.ClipSplit{}
		if err := json.Unmarshal([]byte(payload), value.ClipSplit); err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
	} else {
		value.Type, value.ClipMerge = changeset.MergeClips, &changeset.ClipMerge{}
		if err := json.Unmarshal([]byte(payload), value.ClipMerge); err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
	}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

const scriptOperationSelect = `SELECT operation_id,change_set_id,position,precondition_id,episode_id,expected_script_revision,before_markdown,after_markdown FROM change_set_script_operations`

func scanScriptOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, episodeID, before, after string
	var position int
	var expected int64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &episodeID, &expected, &before, &after); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position,
		Type: changeset.ReplaceEpisodeScript, Precondition: changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetEpisodeScript, TargetID: episodeID, ExpectedRevision: expected},
		EpisodeScript: &changeset.EpisodeScriptChange{EpisodeID: project.EpisodeID(episodeID), Before: before, After: after}}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

const revertOperationSelect = `SELECT operation_id,change_set_id,position,precondition_id,original_change_set_id,expected_change_set_revision FROM change_set_revert_operations`

func scanRevertOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, originalID string
	var position int
	var expected int64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &originalID, &expected); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position,
		Type: changeset.RevertChangeSet, Precondition: changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetChangeSet, TargetID: originalID, ExpectedRevision: expected},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: changeset.ChangeSetID(originalID)}}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

const storyboardCreationSelect = `SELECT operation_id,change_set_id,position,precondition_id,episode_id,expected_storyboard_revision,expected_script_revision,operation_type,payload_json FROM change_set_storyboard_operations`

func scanStoryboardCreationOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, episodeID, operationType, payload string
	var position int
	var storyboardRevision, scriptRevision int64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &episodeID, &storyboardRevision, &scriptRevision, &operationType, &payload); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position,
		Type: changeset.OperationType(operationType), Precondition: changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetStoryboard, TargetID: episodeID, ExpectedRevision: storyboardRevision}}
	switch value.Type {
	case changeset.CreateScene:
		value.SceneCreation = &changeset.SceneCreation{}
		if err := json.Unmarshal([]byte(payload), value.SceneCreation); err != nil {
			return changeset.Operation{}, err
		}
		value.SceneCreation.ExpectedScriptRevision = scriptRevision
	case changeset.CreateClip:
		value.ClipCreation = &changeset.ClipCreation{}
		if err := json.Unmarshal([]byte(payload), value.ClipCreation); err != nil {
			return changeset.Operation{}, err
		}
		value.ClipCreation.ExpectedScriptRevision = scriptRevision
	case changeset.AddClipAssetRef:
		value.ClipAssetRef = &changeset.ClipAssetReferenceCreation{}
		if err := json.Unmarshal([]byte(payload), value.ClipAssetRef); err != nil {
			return changeset.Operation{}, err
		}
		value.ClipAssetRef.ExpectedScriptRevision = scriptRevision
	default:
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

const assetCreationSelect = `SELECT operation_id,change_set_id,position,precondition_id,episode_id,expected_script_revision,asset_id,asset_identity_id,source_asset_id,source_asset_revision,inherited_media_version_id,asset_kind,asset_name,asset_prompt FROM change_set_asset_creations`

const assetRestorationSelect = `SELECT operation_id,change_set_id,position,precondition_id,asset_id,expected_asset_revision FROM change_set_asset_restorations`

func scanAssetRestorationOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, assetID string
	var position int
	var expected int64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &assetID, &expected); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{
		ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position, Type: changeset.RestoreEpisodeAsset,
		Precondition:     changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetAsset, TargetID: assetID, ExpectedRevision: expected},
		AssetRestoration: &changeset.AssetRestoration{AssetID: project.AssetID(assetID)},
	}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

func scanAssetCreationOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, preconditionID, episodeID, assetID, identityID, kind, name, prompt string
	var position int
	var expected int64
	var sourceID, inheritedID sql.NullString
	var sourceRevision sql.NullInt64
	if err := row.Scan(&operationID, &changeSetID, &position, &preconditionID, &episodeID, &expected, &assetID, &identityID, &sourceID, &sourceRevision, &inheritedID, &kind, &name, &prompt); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{
		ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position, Type: changeset.CreateEpisodeAsset,
		Precondition:  changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetEpisodeScript, TargetID: episodeID, ExpectedRevision: expected},
		AssetCreation: &changeset.AssetCreation{AssetID: project.AssetID(assetID), IdentityID: project.AssetIdentityID(identityID), SourceAssetID: project.AssetID(sourceID.String), SourceAssetRevision: sourceRevision.Int64, InheritedMediaVersionID: project.MediaVersionID(inheritedID.String), Kind: project.AssetKind(kind), Name: name, Prompt: prompt},
	}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

func scanChangeSetOperation(row rowScanner) (changeset.Operation, error) {
	var operationID, changeSetID, operationType, preconditionID, targetKind, targetID string
	var position int
	var expected int64
	var targetAsset, targetClip, assetBefore, assetAfter, firstBefore, firstAfter, lastBefore, lastAfter, videoMode, videoBefore, videoAfter sql.NullString
	var assetIDsBeforeJSON, assetIDsAfterJSON, referenceIDsBeforeJSON, referenceIDsAfterJSON string
	var assetBindingsRecorded, referenceBindingsRecorded int
	if err := row.Scan(&operationID, &changeSetID, &position, &operationType, &targetAsset, &targetClip, &assetBefore, &assetAfter, &firstBefore, &firstAfter, &lastBefore, &lastAfter, &videoMode, &videoBefore, &videoAfter, &assetIDsBeforeJSON, &assetIDsAfterJSON, &assetBindingsRecorded, &referenceIDsBeforeJSON, &referenceIDsAfterJSON, &referenceBindingsRecorded, &preconditionID, &targetKind, &targetID, &expected); err != nil {
		return changeset.Operation{}, err
	}
	value := changeset.Operation{ID: changeset.OperationID(operationID), ChangeSetID: changeset.ChangeSetID(changeSetID), Position: position, Type: changeset.OperationType(operationType), Precondition: changeset.Precondition{ID: changeset.PreconditionID(preconditionID), OperationID: changeset.OperationID(operationID), TargetKind: changeset.TargetKind(targetKind), TargetID: targetID, ExpectedRevision: expected}}
	if value.Type == changeset.UpdateAssetPrompt {
		value.AssetPrompt = &changeset.AssetPromptChange{AssetID: project.AssetID(targetAsset.String), Before: assetBefore.String, After: assetAfter.String}
	} else {
		beforeIDs, err := parseAssetIDJSON(assetIDsBeforeJSON)
		if err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
		afterIDs, err := parseAssetIDJSON(assetIDsAfterJSON)
		if err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
		referenceIDsBefore, err := parseArtifactIDJSON(referenceIDsBeforeJSON)
		if err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
		referenceIDsAfter, err := parseArtifactIDJSON(referenceIDsAfterJSON)
		if err != nil {
			return changeset.Operation{}, ErrInvalidStoredProjectData
		}
		value.ClipPrompts = &changeset.ClipPromptsChange{ClipID: project.ClipID(targetClip.String), FirstBefore: firstBefore.String, FirstAfter: firstAfter.String, LastBefore: lastBefore.String, LastAfter: lastAfter.String, VideoMode: project.ClipGenerationMode(videoMode.String), VideoBefore: videoBefore.String, VideoAfter: videoAfter.String, AssetIDsBefore: beforeIDs, AssetIDsAfter: afterIDs, AssetBindingsRecorded: assetBindingsRecorded != 0, ReferenceArtifactIDsBefore: referenceIDsBefore, ReferenceArtifactIDsAfter: referenceIDsAfter, ReferenceBindingsRecorded: referenceBindingsRecorded != 0}
	}
	if value.Validate() != nil {
		return changeset.Operation{}, ErrInvalidStoredProjectData
	}
	return value, nil
}

func marshalAssetIDJSON(ids []project.AssetID) (string, error) {
	values := make([]string, len(ids))
	for index, id := range ids {
		if err := id.Validate(); err != nil {
			return "", err
		}
		values[index] = id.String()
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}

func parseArtifactIDJSON(encoded string) ([]project.ArtifactID, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, err
	}
	result := make([]project.ArtifactID, 0, len(values))
	for _, value := range values {
		id, err := project.ParseArtifactID(value)
		if err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, nil
}

func marshalArtifactIDJSON(ids []project.ArtifactID) (string, error) {
	values := make([]string, len(ids))
	for index, id := range ids {
		if err := id.Validate(); err != nil {
			return "", err
		}
		values[index] = id.String()
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}

func parseAssetIDJSON(value string) ([]project.AssetID, error) {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil, err
	}
	ids := make([]project.AssetID, len(values))
	for index, raw := range values {
		id, err := project.ParseAssetID(raw)
		if err != nil {
			return nil, err
		}
		ids[index] = id
	}
	return ids, nil
}

func resolveDormantAssetIdentity(ctx context.Context, tx *sql.Tx, projectID project.ProjectID, operation changeset.Operation) (changeset.Operation, error) {
	if operation.AssetCreation == nil || operation.AssetCreation.SourceAssetID != "" {
		return operation, nil
	}
	creation := *operation.AssetCreation
	identityID, active, found, err := assetIdentityByName(ctx, tx, projectID, creation.Kind, creation.Name)
	if err != nil {
		return changeset.Operation{}, err
	}
	if found && !active {
		creation.IdentityID = identityID
		operation.AssetCreation = &creation
	}
	return operation, nil
}

func insertChangeSetOperation(ctx context.Context, tx *sql.Tx, value changeset.Operation, at time.Time) error {
	if value.ClipMerge != nil || value.ClipSplit != nil {
		payloadValue := any(value.ClipMerge)
		if value.ClipSplit != nil {
			payloadValue = value.ClipSplit
		}
		payload, err := json.Marshal(payloadValue)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO change_set_clip_merge_operations (operation_id,change_set_id,position,precondition_id,episode_id,expected_storyboard_revision,payload_json,created_at_ms) VALUES (?,?,?,?,?,?,?,?)`,
			value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.Precondition.TargetID, value.Precondition.ExpectedRevision, string(payload), at.UnixMilli())
		return err
	}
	if value.EpisodeScript != nil {
		_, err := tx.ExecContext(ctx, `INSERT INTO change_set_script_operations (operation_id,change_set_id,position,precondition_id,episode_id,expected_script_revision,before_markdown,after_markdown,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.EpisodeScript.EpisodeID.String(), value.Precondition.ExpectedRevision, value.EpisodeScript.Before, value.EpisodeScript.After, at.UnixMilli())
		return err
	}
	if value.ChangeSetRevert != nil {
		_, err := tx.ExecContext(ctx, `INSERT INTO change_set_revert_operations (operation_id,change_set_id,position,precondition_id,original_change_set_id,expected_change_set_revision,created_at_ms) VALUES (?,?,?,?,?,?,?)`, value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.ChangeSetRevert.OriginalChangeSetID.String(), value.Precondition.ExpectedRevision, at.UnixMilli())
		return err
	}
	if value.SceneCreation != nil || value.ClipCreation != nil || value.ClipAssetRef != nil {
		return insertStoryboardCreationOperation(ctx, tx, value, at)
	}
	if value.AssetCreation != nil {
		var sourceID, sourceRevision, inheritedID any
		if value.AssetCreation.SourceAssetID != "" {
			sourceID = value.AssetCreation.SourceAssetID.String()
			sourceRevision = value.AssetCreation.SourceAssetRevision
			inheritedID = value.AssetCreation.InheritedMediaVersionID.String()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO change_set_asset_creations (operation_id,change_set_id,position,precondition_id,episode_id,expected_script_revision,asset_id,asset_identity_id,source_asset_id,source_asset_revision,inherited_media_version_id,asset_kind,asset_name,asset_prompt,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.Precondition.TargetID, value.Precondition.ExpectedRevision, value.AssetCreation.AssetID.String(), value.AssetCreation.IdentityID.String(), sourceID, sourceRevision, inheritedID, string(value.AssetCreation.Kind), value.AssetCreation.Name, value.AssetCreation.Prompt, at.UnixMilli())
		return err
	}
	if value.AssetRestoration != nil {
		_, err := tx.ExecContext(ctx, `INSERT INTO change_set_asset_restorations (operation_id,change_set_id,position,precondition_id,asset_id,expected_asset_revision,created_at_ms) VALUES (?,?,?,?,?,?,?)`, value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.AssetRestoration.AssetID.String(), value.Precondition.ExpectedRevision, at.UnixMilli())
		return err
	}
	var targetAsset, targetClip, assetBefore, assetAfter, firstBefore, firstAfter, lastBefore, lastAfter, videoBefore, videoAfter any
	assetIDsBeforeJSON, assetIDsAfterJSON := "[]", "[]"
	referenceIDsBeforeJSON, referenceIDsAfterJSON := "[]", "[]"
	assetBindingsRecorded, referenceBindingsRecorded := 0, 0
	if value.AssetPrompt != nil {
		targetAsset = value.AssetPrompt.AssetID.String()
		assetBefore = value.AssetPrompt.Before
		assetAfter = value.AssetPrompt.After
	}
	if value.ClipPrompts != nil {
		targetClip = value.ClipPrompts.ClipID.String()
		firstBefore = value.ClipPrompts.FirstBefore
		firstAfter = value.ClipPrompts.FirstAfter
		lastBefore = value.ClipPrompts.LastBefore
		lastAfter = value.ClipPrompts.LastAfter
		videoBefore = value.ClipPrompts.VideoBefore
		videoAfter = value.ClipPrompts.VideoAfter
		before, err := marshalAssetIDJSON(value.ClipPrompts.AssetIDsBefore)
		if err != nil {
			return err
		}
		after, err := marshalAssetIDJSON(value.ClipPrompts.AssetIDsAfter)
		if err != nil {
			return err
		}
		assetIDsBeforeJSON, assetIDsAfterJSON = before, after
		if value.ClipPrompts.AssetBindingsRecorded {
			assetBindingsRecorded = 1
		}
		referenceBefore, err := marshalArtifactIDJSON(value.ClipPrompts.ReferenceArtifactIDsBefore)
		if err != nil {
			return err
		}
		referenceAfter, err := marshalArtifactIDJSON(value.ClipPrompts.ReferenceArtifactIDsAfter)
		if err != nil {
			return err
		}
		referenceIDsBeforeJSON, referenceIDsAfterJSON = referenceBefore, referenceAfter
		if value.ClipPrompts.ReferenceBindingsRecorded {
			referenceBindingsRecorded = 1
		}
	}
	var videoMode any
	if value.ClipPrompts != nil {
		videoMode = string(value.ClipPrompts.VideoMode)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO change_set_operations (operation_id,change_set_id,position,operation_type,target_asset_id,target_clip_id,asset_prompt_before,asset_prompt_after,clip_first_before,clip_first_after,clip_last_before,clip_last_after,clip_video_mode,clip_video_before,clip_video_after,clip_asset_ids_before_json,clip_asset_ids_after_json,clip_asset_bindings_recorded,clip_reference_artifact_ids_before_json,clip_reference_artifact_ids_after_json,clip_reference_bindings_recorded,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.ChangeSetID.String(), value.Position, string(value.Type), targetAsset, targetClip, assetBefore, assetAfter, firstBefore, firstAfter, lastBefore, lastAfter, videoMode, videoBefore, videoAfter, assetIDsBeforeJSON, assetIDsAfterJSON, assetBindingsRecorded, referenceIDsBeforeJSON, referenceIDsAfterJSON, referenceBindingsRecorded, at.UnixMilli())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO change_set_preconditions (precondition_id,operation_id,change_set_id,target_kind,target_id,expected_revision) VALUES (?,?,?,?,?,?)`, value.Precondition.ID.String(), value.ID.String(), value.ChangeSetID.String(), string(value.Precondition.TargetKind), value.Precondition.TargetID, value.Precondition.ExpectedRevision)
	return err
}

func insertStoryboardCreationOperation(ctx context.Context, tx *sql.Tx, value changeset.Operation, at time.Time) error {
	var temporaryRef string
	var parentRef, sceneID, clipID, assetID any
	var scriptRevision int64
	var payloadValue any
	switch value.Type {
	case changeset.CreateScene:
		temporaryRef, sceneID, scriptRevision, payloadValue = value.SceneCreation.Ref, value.SceneCreation.Scene.ID.String(), value.SceneCreation.ExpectedScriptRevision, value.SceneCreation
	case changeset.CreateClip:
		parent := value.ClipCreation.SceneRef
		if value.ClipCreation.ExistingSceneID != "" {
			parent = value.ClipCreation.ExistingSceneID.String()
		}
		temporaryRef, parentRef, sceneID, clipID, scriptRevision, payloadValue = value.ClipCreation.Ref, parent, value.ClipCreation.Clip.SceneID.String(), value.ClipCreation.Clip.ID.String(), value.ClipCreation.ExpectedScriptRevision, value.ClipCreation
	case changeset.AddClipAssetRef:
		temporaryRef, parentRef, clipID, assetID, scriptRevision, payloadValue = value.ClipAssetRef.ClipRef, value.ClipAssetRef.ClipRef, value.ClipAssetRef.ClipID.String(), value.ClipAssetRef.AssetID.String(), value.ClipAssetRef.ExpectedScriptRevision, value.ClipAssetRef
	default:
		return ErrChangeSetBatchInvalid
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO change_set_storyboard_operations (operation_id,change_set_id,position,precondition_id,episode_id,expected_storyboard_revision,expected_script_revision,operation_type,temporary_ref,parent_ref,scene_id,clip_id,asset_id,payload_json,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID.String(), value.ChangeSetID.String(), value.Position, value.Precondition.ID.String(), value.Precondition.TargetID, value.Precondition.ExpectedRevision, scriptRevision, string(value.Type), temporaryRef, parentRef, sceneID, clipID, assetID, string(payload), at.UnixMilli())
	return err
}

type operationConflict struct{ code, summary string }

func validateOperationCurrent(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation) (*operationConflict, error) {
	switch operation.Type {
	case changeset.ReplaceEpisodeScript:
		var projectID, markdown string
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT project_id,script_markdown,script_revision FROM episodes WHERE episode_id=?`, operation.EpisodeScript.EpisodeID.String()).Scan(&projectID, &markdown, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"target_missing", "Episode no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if projectID != owner.ProjectID.String() || operation.EpisodeScript.EpisodeID != owner.EpisodeID {
			return &operationConflict{"target_scope_changed", "Episode script is outside the ChangeSet scope"}, nil
		}
		if revision != operation.Precondition.ExpectedRevision {
			return &operationConflict{"script_revision_changed", "Episode script revision changed"}, nil
		}
		if markdown != operation.EpisodeScript.Before {
			return &operationConflict{"script_content_changed", "Episode script content changed"}, nil
		}
	case changeset.RevertChangeSet:
		return validateRevertCurrent(ctx, tx, owner, operation)
	case changeset.CreateScene, changeset.CreateClip, changeset.AddClipAssetRef:
		return validateStoryboardOperationCurrent(ctx, tx, owner, operation)
	case changeset.MergeClips:
		return validateClipMergeCurrent(ctx, tx, owner, operation)
	case changeset.SplitClip:
		return validateClipSplitCurrent(ctx, tx, owner, operation)
	case changeset.CreateEpisodeAsset:
		creation := operation.AssetCreation
		var projectID string
		var targetSequence int
		var scriptRevision int64
		err := tx.QueryRowContext(ctx, `SELECT project_id,sequence_number,script_revision FROM episodes WHERE episode_id=?`, operation.Precondition.TargetID).Scan(&projectID, &targetSequence, &scriptRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"target_missing", "Episode no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if projectID != owner.ProjectID.String() || operation.Precondition.TargetID != owner.EpisodeID.String() {
			return &operationConflict{"target_scope_changed", "Episode is outside the ChangeSet scope"}, nil
		}
		if scriptRevision != operation.Precondition.ExpectedRevision {
			return &operationConflict{"script_revision_changed", "Episode script revision changed"}, nil
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE asset_id=?`, creation.AssetID.String()).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 0 {
			return &operationConflict{"target_exists", "Proposed Asset ID already exists"}, nil
		}
		if creation.SourceAssetID == "" {
			var identityProject, identityKind, identityName string
			err := tx.QueryRowContext(ctx, `SELECT project_id,kind,name FROM asset_identities WHERE asset_identity_id=?`, creation.IdentityID.String()).Scan(&identityProject, &identityKind, &identityName)
			if err == nil {
				if identityProject != owner.ProjectID.String() || identityKind != string(creation.Kind) || identityName != creation.Name {
					return &operationConflict{"asset_identity_exists", "Proposed Asset Identity belongs to different data"}, nil
				}
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE asset_identity_id=? AND deleted_at_ms IS NULL`, creation.IdentityID.String()).Scan(&exists); err != nil {
					return nil, err
				}
				if exists != 0 {
					return &operationConflict{"asset_identity_exists", "An active Asset Identity with this kind and name already exists"}, nil
				}
				return nil, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_identities WHERE project_id=? AND kind=? AND name=?`, owner.ProjectID.String(), string(creation.Kind), creation.Name).Scan(&exists); err != nil {
				return nil, err
			}
			if exists != 0 {
				return &operationConflict{"asset_identity_exists", "An active Asset Identity with this kind and name already exists"}, nil
			}
			return nil, nil
		}
		var sourceProject, sourceEpisode, sourceIdentity, sourceKind, sourceName string
		var sourceSequence int
		var sourceRevision int64
		var sourceDeleted sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT a.project_id,a.episode_id,a.asset_identity_id,i.kind,i.name,e.sequence_number,a.revision,a.deleted_at_ms FROM assets a JOIN asset_identities i ON i.asset_identity_id=a.asset_identity_id JOIN episodes e ON e.episode_id=a.episode_id WHERE a.asset_id=?`, creation.SourceAssetID.String()).Scan(&sourceProject, &sourceEpisode, &sourceIdentity, &sourceKind, &sourceName, &sourceSequence, &sourceRevision, &sourceDeleted)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"source_missing", "Source Asset no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if sourceProject != owner.ProjectID.String() || sourceEpisode == owner.EpisodeID.String() || sourceSequence >= targetSequence || sourceDeleted.Valid {
			return &operationConflict{"source_invalid", "Source Asset is not an active earlier-Episode Asset"}, nil
		}
		if sourceRevision != creation.SourceAssetRevision || sourceIdentity != creation.IdentityID.String() || sourceKind != string(creation.Kind) || sourceName != creation.Name {
			return &operationConflict{"source_changed", "Source Asset or identity changed"}, nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE episode_id=? AND asset_identity_id=? AND deleted_at_ms IS NULL`, owner.EpisodeID.String(), creation.IdentityID.String()).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 0 {
			return &operationConflict{"asset_identity_already_present", "This Asset Identity is already present in the Episode"}, nil
		}
	case changeset.RestoreEpisodeAsset:
		var projectID, episodeID, identityID string
		var revision int64
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,asset_identity_id,revision,deleted_at_ms FROM assets WHERE asset_id=?`, operation.AssetRestoration.AssetID.String()).Scan(&projectID, &episodeID, &identityID, &revision, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"target_missing", "Asset no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if projectID != owner.ProjectID.String() || episodeID != owner.EpisodeID.String() {
			return &operationConflict{"target_scope_changed", "Asset is outside the ChangeSet Episode"}, nil
		}
		if !deleted.Valid {
			return &operationConflict{"target_not_deleted", "Asset is already active"}, nil
		}
		if revision != operation.Precondition.ExpectedRevision {
			return &operationConflict{"target_revision_changed", "Deleted Asset revision changed"}, nil
		}
		var activeIdentityCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE episode_id=? AND asset_identity_id=? AND deleted_at_ms IS NULL`, episodeID, identityID).Scan(&activeIdentityCount); err != nil {
			return nil, err
		}
		if activeIdentityCount != 0 {
			return &operationConflict{"asset_identity_already_present", "This Asset Identity is already active in the Episode"}, nil
		}
	case changeset.UpdateAssetPrompt:
		var projectID, episodeID, prompt string
		var revision int64
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,prompt,revision,deleted_at_ms FROM assets WHERE asset_id=?`, operation.AssetPrompt.AssetID.String()).Scan(&projectID, &episodeID, &prompt, &revision, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"target_missing", "Asset no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if projectID != owner.ProjectID.String() || episodeID != owner.EpisodeID.String() {
			return &operationConflict{"target_scope_changed", "Asset is outside the ChangeSet Episode"}, nil
		}
		if deleted.Valid {
			return &operationConflict{"target_deleted", "Asset is deleted"}, nil
		}
		if revision != operation.Precondition.ExpectedRevision {
			return &operationConflict{"target_revision_changed", "Asset revision changed"}, nil
		}
		if prompt != operation.AssetPrompt.Before {
			return &operationConflict{"target_content_changed", "Asset prompt changed"}, nil
		}
	case changeset.UpdateClipPrompts:
		var projectID, episodeID, first, last, firstVideo, firstLastVideo, referenceVideo string
		var revision int64
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,first_image_prompt,last_image_prompt,first_image_video_prompt,first_last_image_video_prompt,reference_video_prompt,revision,deleted_at_ms FROM clips WHERE clip_id=?`, operation.ClipPrompts.ClipID.String()).Scan(&projectID, &episodeID, &first, &last, &firstVideo, &firstLastVideo, &referenceVideo, &revision, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"target_missing", "Clip no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if projectID != owner.ProjectID.String() || episodeID != owner.EpisodeID.String() {
			return &operationConflict{"target_scope_changed", "Clip is outside the ChangeSet Episode"}, nil
		}
		if deleted.Valid {
			return &operationConflict{"target_deleted", "Clip is deleted"}, nil
		}
		if revision != operation.Precondition.ExpectedRevision {
			return &operationConflict{"target_revision_changed", "Clip revision changed"}, nil
		}
		prompts := project.ClipVideoPrompts{FirstImage: firstVideo, FirstLastImage: firstLastVideo, Reference: referenceVideo}
		if first != operation.ClipPrompts.FirstBefore || last != operation.ClipPrompts.LastBefore || prompts.ForMode(operation.ClipPrompts.VideoMode) != operation.ClipPrompts.VideoBefore {
			return &operationConflict{"target_content_changed", "Clip prompts changed"}, nil
		}
		if operation.ClipPrompts.AssetBindingsRecorded {
			currentAssetIDs, err := transactionClipAssetIDs(ctx, tx, operation.ClipPrompts.ClipID)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(currentAssetIDs, operation.ClipPrompts.AssetIDsBefore) {
				return &operationConflict{"target_content_changed", "Clip Asset bindings changed"}, nil
			}
		}
		if operation.ClipPrompts.ReferenceBindingsRecorded {
			currentReferenceIDs, err := transactionClipReferenceArtifactIDs(ctx, tx, operation.ClipPrompts.ClipID)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(currentReferenceIDs, operation.ClipPrompts.ReferenceArtifactIDsBefore) {
				return &operationConflict{"target_content_changed", "Clip uploaded-media bindings changed"}, nil
			}
		}
		for _, assetID := range operation.ClipPrompts.AssetIDsAfter {
			var assetProjectID, assetEpisodeID string
			var assetDeleted sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,deleted_at_ms FROM assets WHERE asset_id=?`, assetID.String()).Scan(&assetProjectID, &assetEpisodeID, &assetDeleted)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (assetDeleted.Valid || assetProjectID != owner.ProjectID.String() || assetEpisodeID != owner.EpisodeID.String()) {
				return &operationConflict{"asset_invalid", "Clip Asset binding is missing, deleted, or outside the ChangeSet Episode"}, nil
			}
			if err != nil {
				return nil, err
			}
		}
		for _, artifactID := range operation.ClipPrompts.ReferenceArtifactIDsAfter {
			var artifactProjectID, libraryEpisodeID string
			var libraryDeleted sql.NullInt64
			// Artifacts are shared across Episodes; validate this Episode's library entry.
			err := tx.QueryRowContext(ctx, `SELECT artifacts.project_id,episode_reference_media.episode_id,episode_reference_media.deleted_at_ms
FROM artifacts JOIN episode_reference_media ON episode_reference_media.artifact_id=artifacts.artifact_id
WHERE artifacts.artifact_id=? AND episode_reference_media.episode_id=?`, artifactID.String(), owner.EpisodeID.String()).Scan(&artifactProjectID, &libraryEpisodeID, &libraryDeleted)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (libraryDeleted.Valid || artifactProjectID != owner.ProjectID.String() || libraryEpisodeID != owner.EpisodeID.String()) {
				return &operationConflict{"reference_media_invalid", "Clip uploaded-media binding is missing, deleted, or outside the ChangeSet Episode"}, nil
			}
			if err != nil {
				return nil, err
			}
		}
	default:
		return nil, ErrInvalidStoredProjectData
	}
	return nil, nil
}

func validateRevertCurrent(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation) (*operationConflict, error) {
	original, err := transactionChangeSet(ctx, tx, operation.ChangeSetRevert.OriginalChangeSetID)
	if err != nil {
		return nil, err
	}
	if original.ProjectID != owner.ProjectID || original.EpisodeID != owner.EpisodeID || original.Status != changeset.StatusApplied ||
		original.Revision != operation.Precondition.ExpectedRevision || original.RevertedByChangeSetID != "" || original.RevertsChangeSetID != "" || original.ResolvedAt == nil {
		return &operationConflict{"undo_target_changed", "The original ChangeSet is not the latest reversible applied change"}, nil
	}
	guardConflict, scoped, err := scopedRevertGuard(ctx, tx, original.ID, owner.UpdatedAt)
	if err != nil || guardConflict != nil {
		return guardConflict, err
	}
	if !scoped {
		var later int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM change_sets later
WHERE later.episode_id=? AND later.status='applied' AND later.resolved_at_ms>?
AND later.change_set_id<>? AND NOT EXISTS (
  SELECT 1 FROM change_set_revert_operations r JOIN change_sets inverse ON inverse.change_set_id=r.change_set_id
  WHERE r.original_change_set_id=later.change_set_id AND inverse.status='applied'
) AND NOT EXISTS (
  SELECT 1 FROM change_set_revert_operations own WHERE own.change_set_id=later.change_set_id
	)`, original.EpisodeID.String(), original.ResolvedAt.UnixMilli(), owner.ID.String()).Scan(&later); err != nil {
			return nil, err
		}
		if later != 0 {
			return &operationConflict{"undo_has_newer_changes", "Newer applied changes must be undone first"}, nil
		}
	}
	operations, err := transactionChangeSetOperations(ctx, tx, original.ID)
	if err != nil {
		return nil, err
	}
	for _, applied := range operations {
		conflict, err := validateAppliedOperationForRevert(ctx, tx, original, applied, scoped)
		if err != nil || conflict != nil {
			return conflict, err
		}
	}
	return nil, nil
}

func validateAppliedOperationForRevert(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation, scoped bool) (*operationConflict, error) {
	switch operation.Type {
	case changeset.ReplaceEpisodeScript:
		var markdown string
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT script_markdown,script_revision FROM episodes WHERE episode_id=?`, operation.EpisodeScript.EpisodeID.String()).Scan(&markdown, &revision); err != nil {
			return nil, err
		}
		if (!scoped && revision != operation.Precondition.ExpectedRevision+1) || markdown != operation.EpisodeScript.After {
			return &operationConflict{"undo_target_changed", "Episode script changed after the original ChangeSet"}, nil
		}
	case changeset.UpdateAssetPrompt:
		var prompt string
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT prompt,revision,deleted_at_ms FROM assets WHERE asset_id=?`, operation.AssetPrompt.AssetID.String()).Scan(&prompt, &revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || (!scoped && revision != operation.Precondition.ExpectedRevision+1) || prompt != operation.AssetPrompt.After {
			return &operationConflict{"undo_target_changed", "Asset changed after the original ChangeSet"}, nil
		}
	case changeset.UpdateClipPrompts:
		var first, last, firstVideo, firstLastVideo, referenceVideo string
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT first_image_prompt,last_image_prompt,first_image_video_prompt,first_last_image_video_prompt,reference_video_prompt,revision,deleted_at_ms FROM clips WHERE clip_id=?`, operation.ClipPrompts.ClipID.String()).Scan(&first, &last, &firstVideo, &firstLastVideo, &referenceVideo, &revision, &deleted); err != nil {
			return nil, err
		}
		prompts := project.ClipVideoPrompts{FirstImage: firstVideo, FirstLastImage: firstLastVideo, Reference: referenceVideo}
		assetBindingsChanged := false
		referenceBindingsChanged := false
		if operation.ClipPrompts.AssetBindingsRecorded {
			assetIDs, err := transactionClipAssetIDs(ctx, tx, operation.ClipPrompts.ClipID)
			if err != nil {
				return nil, err
			}
			assetBindingsChanged = !slices.Equal(assetIDs, operation.ClipPrompts.AssetIDsAfter)
		}
		if operation.ClipPrompts.ReferenceBindingsRecorded {
			referenceIDs, err := transactionClipReferenceArtifactIDs(ctx, tx, operation.ClipPrompts.ClipID)
			if err != nil {
				return nil, err
			}
			referenceBindingsChanged = !slices.Equal(referenceIDs, operation.ClipPrompts.ReferenceArtifactIDsAfter)
		}
		if deleted.Valid || (!scoped && revision != operation.Precondition.ExpectedRevision+1) || first != operation.ClipPrompts.FirstAfter || last != operation.ClipPrompts.LastAfter || prompts.ForMode(operation.ClipPrompts.VideoMode) != operation.ClipPrompts.VideoAfter || assetBindingsChanged || referenceBindingsChanged {
			return &operationConflict{"undo_target_changed", "Clip changed after the original ChangeSet"}, nil
		}
	case changeset.CreateEpisodeAsset:
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM assets WHERE asset_id=?`, operation.AssetCreation.AssetID.String()).Scan(&revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || (!scoped && revision != 1) {
			return &operationConflict{"undo_target_changed", "Created Asset changed after the original ChangeSet"}, nil
		}
		var dependencies int
		if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM clip_asset_references WHERE asset_id=?) +
  (SELECT count(*) FROM assets WHERE derived_from_asset_id=? AND deleted_at_ms IS NULL)`, operation.AssetCreation.AssetID.String(), operation.AssetCreation.AssetID.String()).Scan(&dependencies); err != nil {
			return nil, err
		}
		if dependencies != 0 {
			return &operationConflict{"undo_target_in_use", "Created Asset is used by newer project content"}, nil
		}
		slotID, err := project.FixedMediaSlotID(operation.AssetCreation.AssetID.String(), project.MediaSlotAssetPrimaryImage)
		if err != nil {
			return nil, err
		}
		var current sql.NullString
		var versions, jobs int
		if err := tx.QueryRowContext(ctx, `SELECT current_media_version_id,
  (SELECT count(*) FROM media_versions WHERE media_slot_id=media_slots.media_slot_id),
  (SELECT count(*) FROM generation_jobs WHERE target_media_slot_id=media_slots.media_slot_id)
FROM media_slots WHERE media_slot_id=?`, slotID.String()).Scan(&current, &versions, &jobs); err != nil {
			return nil, err
		}
		expectedInherited := operation.AssetCreation.InheritedMediaVersionID.String()
		mediaMatchesChangeSet := expectedInherited == "" && !current.Valid && versions == 0 ||
			expectedInherited != "" && current.String == expectedInherited && versions == 1
		if jobs != 0 || !mediaMatchesChangeSet {
			return &operationConflict{"undo_external_effect", "Created Asset already has generated media or jobs"}, nil
		}
	case changeset.RestoreEpisodeAsset:
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM assets WHERE asset_id=?`, operation.AssetRestoration.AssetID.String()).Scan(&revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || (!scoped && revision != operation.Precondition.ExpectedRevision+1) {
			return &operationConflict{"undo_target_changed", "Restored Asset changed after the original ChangeSet"}, nil
		}
	case changeset.CreateScene:
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM scenes WHERE scene_id=?`, operation.SceneCreation.Scene.ID.String()).Scan(&revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || (!scoped && revision != 1) {
			return &operationConflict{"undo_target_changed", "Created Scene changed after the original ChangeSet"}, nil
		}
	case changeset.CreateClip:
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM clips WHERE clip_id=?`, operation.ClipCreation.Clip.ID.String()).Scan(&revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || (!scoped && revision != 1) {
			return &operationConflict{"undo_target_changed", "Created Clip changed after the original ChangeSet"}, nil
		}
		var effects int
		if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM media_slots WHERE owner_kind='clip' AND owner_id=? AND current_media_version_id IS NOT NULL) +
	  (SELECT count(*) FROM generation_jobs j JOIN media_slots s ON s.media_slot_id=j.target_media_slot_id WHERE s.owner_kind='clip' AND s.owner_id=?)`, operation.ClipCreation.Clip.ID.String(), operation.ClipCreation.Clip.ID.String()).Scan(&effects); err != nil {
			return nil, err
		}
		if effects != 0 {
			return &operationConflict{"undo_external_effect", "Created Clip already has generated media or jobs"}, nil
		}
	case changeset.MergeClips:
		merge := operation.ClipMerge
		var revision int64
		var deleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM clips WHERE clip_id=?`, merge.Replacement.ID.String()).Scan(&revision, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid || revision != 1 {
			return &operationConflict{"undo_target_changed", "Merged Clip changed after the original ChangeSet"}, nil
		}
		assetIDs, err := transactionClipAssetIDs(ctx, tx, merge.Replacement.ID)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(assetIDs, merge.AssetIDs) {
			return &operationConflict{"undo_target_changed", "Merged Clip Asset references changed"}, nil
		}
		for _, source := range merge.Sources {
			var sourceRevision int64
			var sourceDeleted sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM clips WHERE clip_id=?`, source.ID.String()).Scan(&sourceRevision, &sourceDeleted); err != nil {
				return nil, err
			}
			if !sourceDeleted.Valid || sourceRevision != source.Revision+1 {
				return &operationConflict{"undo_target_changed", "A source Clip changed after the merge"}, nil
			}
		}
		expectedCurrent := map[project.MediaSlotKind]string{
			project.MediaSlotClipFirstImage: "",
			project.MediaSlotClipLastImage:  "",
			project.MediaSlotClipVideo:      "",
			project.MediaSlotAudioPrimary:   "",
		}
		if merge.FirstFrame.SourceVersion != nil {
			expectedCurrent[project.MediaSlotClipFirstImage] = merge.FirstFrame.TargetVersionID.String()
		}
		if merge.LastFrame.SourceVersion != nil {
			expectedCurrent[project.MediaSlotClipLastImage] = merge.LastFrame.TargetVersionID.String()
		}
		for kind, expected := range expectedCurrent {
			slotID, err := project.FixedMediaSlotID(merge.Replacement.ID.String(), kind)
			if err != nil {
				return nil, err
			}
			var current sql.NullString
			var versions, jobs int
			if err := tx.QueryRowContext(ctx, `SELECT current_media_version_id,
  (SELECT count(*) FROM media_versions WHERE media_slot_id=media_slots.media_slot_id),
  (SELECT count(*) FROM generation_jobs WHERE target_media_slot_id=media_slots.media_slot_id)
FROM media_slots WHERE media_slot_id=?`, slotID.String()).Scan(&current, &versions, &jobs); err != nil {
				return nil, err
			}
			wantVersions := 0
			if expected != "" {
				wantVersions = 1
			}
			if current.String != expected || current.Valid != (expected != "") || versions != wantVersions || jobs != 0 {
				return &operationConflict{"undo_external_effect", "Merged Clip already has new media or jobs"}, nil
			}
		}
	case changeset.SplitClip:
		split := operation.ClipSplit
		var sourceRevision int64
		var sourceDeleted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM clips WHERE clip_id=?`, split.Source.ID.String()).Scan(&sourceRevision, &sourceDeleted); err != nil {
			return nil, err
		}
		if !sourceDeleted.Valid || sourceRevision != split.Source.Revision+1 {
			return &operationConflict{"undo_target_changed", "Source Clip changed after the split"}, nil
		}
		for index, replacement := range split.Replacements {
			var revision int64
			var deleted sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT revision,deleted_at_ms FROM clips WHERE clip_id=?`, replacement.Clip.ID.String()).Scan(&revision, &deleted); err != nil {
				return nil, err
			}
			if deleted.Valid || revision != 1 {
				return &operationConflict{"undo_target_changed", "A split Clip changed after the original ChangeSet"}, nil
			}
			assetIDs, err := transactionClipAssetIDs(ctx, tx, replacement.Clip.ID)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(assetIDs, replacement.AssetIDs) {
				return &operationConflict{"undo_target_changed", "A split Clip Asset reference changed"}, nil
			}
			expectedCurrent := map[project.MediaSlotKind]string{
				project.MediaSlotClipFirstImage: "", project.MediaSlotClipLastImage: "",
				project.MediaSlotClipVideo: "", project.MediaSlotAudioPrimary: "",
			}
			if index == 0 && split.FirstFrame.SourceVersion != nil {
				expectedCurrent[project.MediaSlotClipFirstImage] = split.FirstFrame.TargetVersionID.String()
			}
			if index == len(split.Replacements)-1 && split.LastFrame.SourceVersion != nil {
				expectedCurrent[project.MediaSlotClipLastImage] = split.LastFrame.TargetVersionID.String()
			}
			for kind, expected := range expectedCurrent {
				slotID, err := project.FixedMediaSlotID(replacement.Clip.ID.String(), kind)
				if err != nil {
					return nil, err
				}
				var current sql.NullString
				var versions, jobs int
				if err := tx.QueryRowContext(ctx, `SELECT current_media_version_id,
  (SELECT count(*) FROM media_versions WHERE media_slot_id=media_slots.media_slot_id),
  (SELECT count(*) FROM generation_jobs WHERE target_media_slot_id=media_slots.media_slot_id)
FROM media_slots WHERE media_slot_id=?`, slotID.String()).Scan(&current, &versions, &jobs); err != nil {
					return nil, err
				}
				wantVersions := 0
				if expected != "" {
					wantVersions = 1
				}
				if current.String != expected || current.Valid != (expected != "") || versions != wantVersions || jobs != 0 {
					return &operationConflict{"undo_external_effect", "A split Clip already has new media or jobs"}, nil
				}
			}
		}
	case changeset.AddClipAssetRef:
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clip_asset_references WHERE clip_id=? AND asset_id=? AND position=?`, operation.ClipAssetRef.ClipID.String(), operation.ClipAssetRef.AssetID.String(), operation.ClipAssetRef.Position).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return &operationConflict{"undo_target_changed", "Storyboard Asset reference changed after the original ChangeSet"}, nil
		}
	default:
		return &operationConflict{"undo_unsupported", "The original ChangeSet contains an operation that cannot be undone"}, nil
	}
	return nil, nil
}

func validateStoryboardOperationCurrent(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation) (*operationConflict, error) {
	var projectID, script string
	var scriptRevision, storyboardRevision int64
	err := tx.QueryRowContext(ctx, `SELECT project_id,script_markdown,script_revision,storyboard_revision FROM episodes WHERE episode_id=?`, operation.Precondition.TargetID).Scan(&projectID, &script, &scriptRevision, &storyboardRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return &operationConflict{"target_missing", "Episode no longer exists"}, nil
	}
	if err != nil {
		return nil, err
	}
	if projectID != owner.ProjectID.String() || operation.Precondition.TargetID != owner.EpisodeID.String() {
		return &operationConflict{"target_scope_changed", "Storyboard is outside the ChangeSet scope"}, nil
	}
	if storyboardRevision != operation.Precondition.ExpectedRevision {
		return &operationConflict{"storyboard_changed", "Storyboard changed after it was read"}, nil
	}
	var expectedScript int64
	switch operation.Type {
	case changeset.CreateScene:
		expectedScript = operation.SceneCreation.ExpectedScriptRevision
	case changeset.CreateClip:
		expectedScript = operation.ClipCreation.ExpectedScriptRevision
	case changeset.AddClipAssetRef:
		expectedScript = operation.ClipAssetRef.ExpectedScriptRevision
	}
	if scriptRevision != expectedScript {
		return &operationConflict{"script_revision_changed", "Episode script revision changed"}, nil
	}
	var maximumSceneOrderKey int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(position),0) FROM scenes WHERE episode_id=? AND deleted_at_ms IS NULL`, owner.EpisodeID.String()).Scan(&maximumSceneOrderKey); err != nil {
		return nil, err
	}
	switch operation.Type {
	case changeset.CreateScene:
		creation := operation.SceneCreation
		if maximumSceneOrderKey > math.MaxInt-project.StoryboardOrderStep || creation.Scene.Position != maximumSceneOrderKey+project.StoryboardOrderStep {
			return &operationConflict{"scene_order_invalid", "The ChangeSet must append exactly the next Scene"}, nil
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM scenes WHERE scene_id=?`, creation.Scene.ID.String()).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 0 {
			return &operationConflict{"target_exists", "Proposed Scene ID already exists"}, nil
		}
		if creation.Scene.Source != nil {
			source := creation.Scene.Source
			if source.EndByte > len(script) || source.StartByte < 0 || script[source.StartByte:source.EndByte] != source.Excerpt || source.ScriptRevision != scriptRevision {
				return &operationConflict{"script_source_changed", "Scene source range no longer matches the Episode script"}, nil
			}
		}
	case changeset.CreateClip:
		creation := operation.ClipCreation
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clips WHERE clip_id=?`, creation.Clip.ID.String()).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 0 {
			return &operationConflict{"target_exists", "Proposed Clip ID already exists"}, nil
		}
		if creation.ExistingSceneID != "" {
			var sceneProject, sceneEpisode string
			var deleted sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,deleted_at_ms FROM scenes WHERE scene_id=?`, creation.ExistingSceneID.String()).Scan(&sceneProject, &sceneEpisode, &deleted)
			if errors.Is(err, sql.ErrNoRows) {
				return &operationConflict{"scene_missing", "Target Scene no longer exists"}, nil
			}
			if err != nil {
				return nil, err
			}
			if sceneProject != owner.ProjectID.String() || sceneEpisode != owner.EpisodeID.String() || deleted.Valid {
				return &operationConflict{"scene_invalid", "Target Scene is deleted or outside the ChangeSet Episode"}, nil
			}
			if creation.Clip.Position <= 0 {
				return &operationConflict{"clip_order_invalid", "The proposed Clip position is invalid"}, nil
			}
			var occupied int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clips WHERE scene_id=? AND position=? AND deleted_at_ms IS NULL`, creation.ExistingSceneID.String(), creation.Clip.Position).Scan(&occupied); err != nil {
				return nil, err
			}
			if occupied != 0 {
				return &operationConflict{"clip_order_invalid", "The proposed Clip position is no longer available"}, nil
			}
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM change_set_storyboard_operations WHERE change_set_id=? AND operation_type='create_scene' AND temporary_ref=? AND scene_id=? AND position<?`, owner.ID.String(), creation.SceneRef, creation.Clip.SceneID.String(), operation.Position).Scan(&exists); err != nil {
				return nil, err
			}
			if exists != 1 {
				return &operationConflict{"scene_reference_invalid", "Clip Scene reference is missing or appears later"}, nil
			}
		}
	case changeset.AddClipAssetRef:
		creation := operation.ClipAssetRef
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM change_set_storyboard_operations WHERE change_set_id=? AND operation_type='create_clip' AND temporary_ref=? AND clip_id=? AND position<?`, owner.ID.String(), creation.ClipRef, creation.ClipID.String(), operation.Position).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return &operationConflict{"clip_reference_invalid", "Asset reference Clip is missing or appears later"}, nil
		}
		var assetProject, assetEpisode string
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,deleted_at_ms FROM assets WHERE asset_id=?`, creation.AssetID.String()).Scan(&assetProject, &assetEpisode, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"asset_missing", "Referenced Asset no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if assetProject != owner.ProjectID.String() || assetEpisode != owner.EpisodeID.String() || deleted.Valid {
			return &operationConflict{"asset_invalid", "Referenced Asset is deleted or outside the ChangeSet Episode"}, nil
		}
	}
	return nil, nil
}

func validateClipMergeCurrent(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation) (*operationConflict, error) {
	merge := operation.ClipMerge
	var projectID string
	var storyboardRevision int64
	err := tx.QueryRowContext(ctx, `SELECT project_id,storyboard_revision FROM episodes WHERE episode_id=?`, operation.Precondition.TargetID).Scan(&projectID, &storyboardRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return &operationConflict{"target_missing", "Episode no longer exists"}, nil
	}
	if err != nil {
		return nil, err
	}
	if projectID != owner.ProjectID.String() || operation.Precondition.TargetID != owner.EpisodeID.String() {
		return &operationConflict{"target_scope_changed", "Storyboard is outside the ChangeSet scope"}, nil
	}
	if storyboardRevision != operation.Precondition.ExpectedRevision {
		return &operationConflict{"storyboard_changed", "Storyboard changed after it was read"}, nil
	}
	var replacementExists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clips WHERE clip_id=?`, merge.Replacement.ID.String()).Scan(&replacementExists); err != nil {
		return nil, err
	}
	if replacementExists != 0 {
		return &operationConflict{"target_exists", "Merged Clip ID already exists"}, nil
	}
	ordered, err := orderedActiveClipIDs(ctx, tx, merge.Sources[0].SceneID, "")
	if err != nil {
		return nil, err
	}
	start := -1
	for index := 0; index+len(merge.Sources) <= len(ordered); index++ {
		matches := true
		for sourceIndex, source := range merge.Sources {
			if ordered[index+sourceIndex] != source.ID {
				matches = false
				break
			}
		}
		if matches {
			start = index
			break
		}
	}
	if start < 0 {
		return &operationConflict{"clip_order_changed", "Source Clips are no longer adjacent and ordered"}, nil
	}
	for _, source := range merge.Sources {
		current, err := transactionClip(ctx, tx, source.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return &operationConflict{"source_missing", "A source Clip no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if current.IsDeleted() || current.Revision != source.Revision || current.ProjectID != source.ProjectID || current.EpisodeID != source.EpisodeID || current.SceneID != source.SceneID {
			return &operationConflict{"source_changed", "A source Clip changed after it was read"}, nil
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM generation_jobs j JOIN media_slots s ON s.media_slot_id=j.target_media_slot_id
WHERE s.owner_kind='clip' AND s.owner_id=? AND j.state IN ('queued','running','stop_requested'))`, source.ID.String()).Scan(&active); err != nil {
			return nil, err
		}
		if active != 0 {
			return &operationConflict{"clip_generation_active", "A source Clip has an active generation job"}, nil
		}
	}
	for _, frame := range []changeset.ClipMergeFrame{merge.FirstFrame, merge.LastFrame} {
		var revision int64
		var current sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT revision,current_media_version_id FROM media_slots WHERE media_slot_id=?`, frame.SourceSlotID.String()).Scan(&revision, &current); err != nil {
			return nil, err
		}
		expectedCurrent := ""
		if frame.SourceVersion != nil {
			expectedCurrent = frame.SourceVersion.ID.String()
		}
		if revision != frame.ExpectedSourceSlotRevision || current.String != expectedCurrent || current.Valid != (frame.SourceVersion != nil) {
			return &operationConflict{"boundary_frame_changed", "A source boundary frame changed after it was read"}, nil
		}
		if frame.SourceVersion != nil {
			var artifactID string
			var deleted sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT artifact_id,deleted_at_ms FROM media_versions WHERE media_version_id=? AND media_slot_id=?`, frame.SourceVersion.ID.String(), frame.SourceSlotID.String()).Scan(&artifactID, &deleted); err != nil {
				return nil, err
			}
			if deleted.Valid || artifactID != frame.SourceVersion.ArtifactID.String() {
				return &operationConflict{"boundary_frame_changed", "A source boundary frame changed after it was read"}, nil
			}
		}
	}
	for _, assetID := range merge.AssetIDs {
		var assetProject, assetEpisode string
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,deleted_at_ms FROM assets WHERE asset_id=?`, assetID.String()).Scan(&assetProject, &assetEpisode, &deleted)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (assetProject != owner.ProjectID.String() || assetEpisode != owner.EpisodeID.String() || deleted.Valid) {
			return &operationConflict{"asset_invalid", "A merged Clip Asset is deleted or outside the Episode"}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func validateClipSplitCurrent(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operation changeset.Operation) (*operationConflict, error) {
	split := operation.ClipSplit
	var projectID string
	var storyboardRevision int64
	err := tx.QueryRowContext(ctx, `SELECT project_id,storyboard_revision FROM episodes WHERE episode_id=?`, operation.Precondition.TargetID).Scan(&projectID, &storyboardRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return &operationConflict{"target_missing", "Episode no longer exists"}, nil
	}
	if err != nil {
		return nil, err
	}
	if projectID != owner.ProjectID.String() || operation.Precondition.TargetID != owner.EpisodeID.String() {
		return &operationConflict{"target_scope_changed", "Storyboard is outside the ChangeSet scope"}, nil
	}
	if storyboardRevision != operation.Precondition.ExpectedRevision {
		return &operationConflict{"storyboard_changed", "Storyboard changed after it was read"}, nil
	}
	current, err := transactionClip(ctx, tx, split.Source.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return &operationConflict{"source_missing", "Source Clip no longer exists"}, nil
	}
	if err != nil {
		return nil, err
	}
	if current.IsDeleted() || current.Revision != split.Source.Revision || current.ProjectID != split.Source.ProjectID || current.EpisodeID != split.Source.EpisodeID || current.SceneID != split.Source.SceneID {
		return &operationConflict{"source_changed", "Source Clip changed after it was read"}, nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM generation_jobs j JOIN media_slots s ON s.media_slot_id=j.target_media_slot_id
WHERE s.owner_kind='clip' AND s.owner_id=? AND j.state IN ('queued','running','stop_requested'))`, split.Source.ID.String()).Scan(&active); err != nil {
		return nil, err
	}
	if active != 0 {
		return &operationConflict{"clip_generation_active", "Source Clip has an active generation job"}, nil
	}
	for _, replacement := range split.Replacements {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clips WHERE clip_id=?`, replacement.Clip.ID.String()).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 0 {
			return &operationConflict{"target_exists", "A split Clip ID already exists"}, nil
		}
		for _, assetID := range replacement.AssetIDs {
			var assetProject, assetEpisode string
			var deleted sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT project_id,episode_id,deleted_at_ms FROM assets WHERE asset_id=?`, assetID.String()).Scan(&assetProject, &assetEpisode, &deleted)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (assetProject != owner.ProjectID.String() || assetEpisode != owner.EpisodeID.String() || deleted.Valid) {
				return &operationConflict{"asset_invalid", "A split Clip Asset is deleted or outside the Episode"}, nil
			}
			if err != nil {
				return nil, err
			}
			var bound int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clip_asset_references WHERE clip_id=? AND asset_id=?)`, split.Source.ID.String(), assetID.String()).Scan(&bound); err != nil {
				return nil, err
			}
			if bound == 0 {
				return &operationConflict{"asset_invalid", "A split Clip Asset was not bound to the source Clip"}, nil
			}
		}
	}
	for _, frame := range []changeset.ClipMergeFrame{split.FirstFrame, split.LastFrame} {
		var revision int64
		var current sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT revision,current_media_version_id FROM media_slots WHERE media_slot_id=?`, frame.SourceSlotID.String()).Scan(&revision, &current); err != nil {
			return nil, err
		}
		expected := ""
		if frame.SourceVersion != nil {
			expected = frame.SourceVersion.ID.String()
		}
		if revision != frame.ExpectedSourceSlotRevision || current.String != expected || current.Valid != (frame.SourceVersion != nil) {
			return &operationConflict{"boundary_frame_changed", "Source boundary frame changed after it was read"}, nil
		}
		if frame.SourceVersion != nil {
			var artifactID string
			var deleted sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT artifact_id,deleted_at_ms FROM media_versions WHERE media_version_id=? AND media_slot_id=?`, frame.SourceVersion.ID.String(), frame.SourceSlotID.String()).Scan(&artifactID, &deleted); err != nil {
				return nil, err
			}
			if deleted.Valid || artifactID != frame.SourceVersion.ArtifactID.String() {
				return &operationConflict{"boundary_frame_changed", "Source boundary frame changed after it was read"}, nil
			}
		}
	}
	return nil, nil
}

func hasStoryboardOperations(operations []changeset.Operation) bool {
	for _, operation := range operations {
		if operation.SceneCreation != nil || operation.ClipCreation != nil || operation.ClipAssetRef != nil || operation.ClipMerge != nil || operation.ClipSplit != nil {
			return true
		}
	}
	return false
}

const minimumSceneAverageClipDurationSeconds int64 = 6

func validateStoryboardProposal(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operations []changeset.Operation) (*operationConflict, error) {
	if !hasStoryboardOperations(operations) {
		return nil, nil
	}
	if len(operations) == 1 && (operations[0].Type == changeset.MergeClips || operations[0].Type == changeset.SplitClip) {
		return nil, nil
	}
	scenes := make(map[string]changeset.SceneCreation)
	existingScenes := make(map[project.SceneID]struct{})
	clips := make(map[string]changeset.ClipCreation)
	refs := make(map[string][]changeset.ClipAssetReferenceCreation)
	var proposedDurationSeconds int64
	for _, operation := range operations {
		if operation.SceneCreation == nil && operation.ClipCreation == nil && operation.ClipAssetRef == nil {
			return &operationConflict{"storyboard_operation_mixed", "Storyboard append operations cannot be mixed with other ChangeSet operations"}, nil
		}
		if operation.Precondition.TargetID != owner.EpisodeID.String() {
			return &operationConflict{"storyboard_precondition_invalid", "Storyboard append operations must target this ChangeSet Episode"}, nil
		}
		if operation.SceneCreation != nil {
			scenes[operation.SceneCreation.Ref] = *operation.SceneCreation
		}
		if operation.ClipCreation != nil {
			clips[operation.ClipCreation.Ref] = *operation.ClipCreation
			if operation.ClipCreation.ExistingSceneID != "" {
				existingScenes[operation.ClipCreation.ExistingSceneID] = struct{}{}
			}
			seconds := operation.ClipCreation.Clip.PlannedDurationSeconds
			if seconds > math.MaxInt64-proposedDurationSeconds {
				return nil, ErrInvalidStoredProjectData
			}
			proposedDurationSeconds += seconds
		}
		if operation.ClipAssetRef != nil {
			refs[operation.ClipAssetRef.ClipRef] = append(refs[operation.ClipAssetRef.ClipRef], *operation.ClipAssetRef)
		}
	}
	newSceneTarget := len(scenes) == 1 && len(existingScenes) == 0
	existingSceneTarget := len(scenes) == 0 && len(existingScenes) == 1
	if len(clips) == 0 || (!newSceneTarget && !existingSceneTarget) {
		return &operationConflict{"storyboard_incomplete", "A Storyboard ChangeSet must target exactly one new or existing Scene and contain at least one Clip"}, nil
	}
	if proposedDurationSeconds < int64(len(clips))*minimumSceneAverageClipDurationSeconds {
		return &operationConflict{"clip_plan_too_fragmented", "A generated Scene must average at least 6 seconds per Clip; merge adjacent micro-beats instead of padding them"}, nil
	}
	clipPositions := make(map[string]map[int]struct{})
	for ref, clip := range clips {
		sceneKey := clip.SceneRef
		if clip.ExistingSceneID != "" {
			sceneKey = clip.ExistingSceneID.String()
			if _, exists := existingScenes[clip.ExistingSceneID]; !exists {
				return &operationConflict{"scene_reference_invalid", "Proposed Clip references a different existing Scene"}, nil
			}
		} else if _, exists := scenes[clip.SceneRef]; !exists {
			return &operationConflict{"scene_reference_invalid", "Proposed Clip references an unknown new Scene"}, nil
		}
		if clipPositions[sceneKey] == nil {
			clipPositions[sceneKey] = make(map[int]struct{})
		}
		if _, duplicate := clipPositions[sceneKey][clip.Clip.Position]; duplicate {
			return &operationConflict{"clip_order_invalid", "Proposed Clip positions are duplicated"}, nil
		}
		clipPositions[sceneKey][clip.Clip.Position] = struct{}{}
		assetPositions := make(map[int]struct{}, len(refs[ref]))
		assetIDs := make(map[project.AssetID]struct{}, len(refs[ref]))
		for _, assetRef := range refs[ref] {
			if _, duplicate := assetPositions[assetRef.Position]; duplicate || assetRef.Position > len(refs[ref]) {
				return &operationConflict{"asset_reference_order_invalid", "Clip Asset reference positions must be contiguous"}, nil
			}
			if _, duplicate := assetIDs[assetRef.AssetID]; duplicate {
				return &operationConflict{"asset_reference_duplicate", "Clip contains a duplicate Asset reference"}, nil
			}
			assetPositions[assetRef.Position] = struct{}{}
			assetIDs[assetRef.AssetID] = struct{}{}
		}
	}
	for sceneRef := range scenes {
		if len(clipPositions[sceneRef]) == 0 {
			return &operationConflict{"scene_empty", "Every proposed Scene must contain at least one Clip"}, nil
		}
	}
	for clipRef := range refs {
		if _, exists := clips[clipRef]; !exists {
			return &operationConflict{"clip_reference_invalid", "Asset reference targets an unknown Clip"}, nil
		}
	}
	var targetDurationMilliseconds, currentDurationSeconds int64
	if err := tx.QueryRowContext(ctx, `SELECT e.target_duration_ms,coalesce(sum(c.planned_duration_seconds),0)
FROM episodes e
LEFT JOIN clips c ON c.episode_id=e.episode_id AND c.deleted_at_ms IS NULL
WHERE e.episode_id=?
GROUP BY e.episode_id,e.target_duration_ms`, owner.EpisodeID.String()).Scan(&targetDurationMilliseconds, &currentDurationSeconds); err != nil {
		return nil, err
	}
	if currentDurationSeconds > math.MaxInt64-proposedDurationSeconds {
		return nil, ErrInvalidStoredProjectData
	}
	totalDurationSeconds := currentDurationSeconds + proposedDurationSeconds
	if totalDurationSeconds > math.MaxInt64/1000 || totalDurationSeconds*1000 > targetDurationMilliseconds {
		return &operationConflict{"storyboard_duration_exceeded", "The proposed Scene would exceed the Episode target duration; merge micro-beats instead of shortening every Clip"}, nil
	}
	return nil, nil
}

func applyChangeSetOperation(ctx context.Context, tx *sql.Tx, operation changeset.Operation, at time.Time) error {
	if operation.ChangeSetRevert != nil {
		return applyRevertChangeSet(ctx, tx, operation.ChangeSetRevert.OriginalChangeSetID, at)
	}
	if operation.EpisodeScript != nil {
		write, err := tx.ExecContext(ctx, `UPDATE episodes SET script_markdown=?,script_revision=script_revision+1,revision=revision+1,updated_at_ms=? WHERE episode_id=? AND script_revision=? AND script_markdown=?`, operation.EpisodeScript.After, at.UnixMilli(), operation.EpisodeScript.EpisodeID.String(), operation.Precondition.ExpectedRevision, operation.EpisodeScript.Before)
		if err != nil {
			return err
		}
		return requireOne(write, ErrChangeSetPreconditionFailed)
	}
	if operation.SceneCreation != nil {
		value := operation.SceneCreation.Scene
		value.CreatedAt, value.UpdatedAt = at, at
		return insertScene(ctx, tx, value)
	}
	if operation.ClipCreation != nil {
		value := operation.ClipCreation.Clip
		value.CreatedAt, value.UpdatedAt = at, at
		return insertClip(ctx, tx, value)
	}
	if operation.ClipAssetRef != nil {
		value := operation.ClipAssetRef
		_, err := tx.ExecContext(ctx, `INSERT INTO clip_asset_references (clip_id,asset_id,position) VALUES (?,?,?)`, value.ClipID.String(), value.AssetID.String(), value.Position)
		return err
	}
	if operation.ClipMerge != nil {
		return applyClipMerge(ctx, tx, *operation.ClipMerge, at)
	}
	if operation.ClipSplit != nil {
		return applyClipSplit(ctx, tx, *operation.ClipSplit, at)
	}
	if operation.AssetCreation != nil {
		creation := operation.AssetCreation
		ownerProject, err := transactionProjectID(ctx, tx)
		if err != nil {
			return err
		}
		if creation.SourceAssetID == "" {
			var identityExists int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM asset_identities WHERE asset_identity_id=?`, creation.IdentityID.String()).Scan(&identityExists); err != nil {
				return err
			}
			if identityExists == 0 {
				identity := project.AssetIdentity{ID: creation.IdentityID, ProjectID: ownerProject, Kind: creation.Kind, Name: creation.Name, Revision: 1, CreatedAt: at, UpdatedAt: at}
				if err := insertAssetIdentity(ctx, tx, identity); err != nil {
					return err
				}
			}
		}
		asset := project.Asset{ID: creation.AssetID, ProjectID: ownerProject, EpisodeID: project.EpisodeID(operation.Precondition.TargetID), IdentityID: creation.IdentityID, DerivedFromAssetID: creation.SourceAssetID, Prompt: creation.Prompt, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if asset.Validate() != nil {
			return ErrInvalidStoredProjectData
		}
		if err := insertAsset(ctx, tx, asset); err != nil {
			return err
		}
		if creation.SourceAssetID != "" {
			return inheritAssetCurrentMedia(ctx, tx, creation.SourceAssetID, creation.AssetID, creation.InheritedMediaVersionID, at)
		}
		return nil
	}
	if operation.AssetRestoration != nil {
		write, err := tx.ExecContext(ctx, `UPDATE assets SET deleted_at_ms=NULL,revision=revision+1,updated_at_ms=? WHERE asset_id=? AND revision=? AND deleted_at_ms IS NOT NULL`, at.UnixMilli(), operation.AssetRestoration.AssetID.String(), operation.Precondition.ExpectedRevision)
		if err != nil {
			return err
		}
		return requireOne(write, ErrChangeSetPreconditionFailed)
	}
	var result sql.Result
	var err error
	if operation.AssetPrompt != nil {
		result, err = tx.ExecContext(ctx, `UPDATE assets SET prompt=?,revision=revision+1,updated_at_ms=? WHERE asset_id=? AND revision=? AND deleted_at_ms IS NULL AND prompt=?`, operation.AssetPrompt.After, at.UnixMilli(), operation.AssetPrompt.AssetID.String(), operation.Precondition.ExpectedRevision, operation.AssetPrompt.Before)
	} else {
		column, columnErr := clipVideoPromptColumn(operation.ClipPrompts.VideoMode)
		if columnErr != nil {
			return columnErr
		}
		query := fmt.Sprintf(`UPDATE clips SET first_image_prompt=?,last_image_prompt=?,%s=?,revision=revision+1,updated_at_ms=? WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL AND first_image_prompt=? AND last_image_prompt=? AND %s=?`, column, column)
		result, err = tx.ExecContext(ctx, query, operation.ClipPrompts.FirstAfter, operation.ClipPrompts.LastAfter, operation.ClipPrompts.VideoAfter, at.UnixMilli(), operation.ClipPrompts.ClipID.String(), operation.Precondition.ExpectedRevision, operation.ClipPrompts.FirstBefore, operation.ClipPrompts.LastBefore, operation.ClipPrompts.VideoBefore)
	}
	if err != nil {
		return err
	}
	if err := requireOne(result, ErrChangeSetPreconditionFailed); err != nil {
		return err
	}
	if operation.ClipPrompts != nil && operation.ClipPrompts.AssetBindingsRecorded {
		if err := replaceClipAssetReferences(ctx, tx, operation.ClipPrompts.ClipID, operation.ClipPrompts.AssetIDsAfter); err != nil {
			return err
		}
	}
	if operation.ClipPrompts != nil && operation.ClipPrompts.ReferenceBindingsRecorded {
		return replaceClipReferenceArtifacts(ctx, tx, operation.ClipPrompts.ClipID, operation.ClipPrompts.ReferenceArtifactIDsAfter, at)
	}
	return nil
}

func applyClipMerge(ctx context.Context, tx *sql.Tx, merge changeset.ClipMerge, at time.Time) error {
	originalOrder, err := orderedActiveClipIDs(ctx, tx, merge.Sources[0].SceneID, "")
	if err != nil {
		return err
	}
	start := -1
	for index := 0; index+len(merge.Sources) <= len(originalOrder); index++ {
		matches := true
		for sourceIndex, source := range merge.Sources {
			if originalOrder[index+sourceIndex] != source.ID {
				matches = false
				break
			}
		}
		if matches {
			start = index
			break
		}
	}
	if start < 0 {
		return ErrChangeSetPreconditionFailed
	}
	for _, source := range merge.Sources {
		write, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1,updated_at_ms=?,deleted_at_ms=?,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), source.ID.String(), source.Revision)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
			return err
		}
	}
	replacement := merge.Replacement
	replacement.CreatedAt, replacement.UpdatedAt = at, at
	if err := insertClip(ctx, tx, replacement); err != nil {
		return err
	}
	for index, assetID := range merge.AssetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO clip_asset_references (clip_id,asset_id,position) VALUES (?,?,?)`, replacement.ID.String(), assetID.String(), index+1); err != nil {
			return err
		}
	}
	for index, source := range merge.Sources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO clip_derivations (clip_id,source_clip_id,position) VALUES (?,?,?)`, replacement.ID.String(), source.ID.String(), index+1); err != nil {
			return err
		}
	}
	for _, frame := range []changeset.ClipMergeFrame{merge.FirstFrame, merge.LastFrame} {
		if frame.SourceVersion == nil {
			continue
		}
		targetSlotID, err := project.FixedMediaSlotID(replacement.ID.String(), frame.Kind)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,1,0,0,1,?,?,NULL)`, frame.TargetVersionID.String(), targetSlotID.String(), frame.SourceVersion.ArtifactID.String(), at.UnixMilli(), at.UnixMilli()); err != nil {
			return err
		}
		write, err := tx.ExecContext(ctx, `UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=? AND current_media_version_id IS NULL`, frame.TargetVersionID.String(), at.UnixMilli(), targetSlotID.String())
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
			return err
		}
	}
	finalOrder := make([]project.ClipID, 0, len(originalOrder)-len(merge.Sources)+1)
	finalOrder = append(finalOrder, originalOrder[:start]...)
	finalOrder = append(finalOrder, replacement.ID)
	finalOrder = append(finalOrder, originalOrder[start+len(merge.Sources):]...)
	return reorderClipPositions(ctx, tx, merge.Sources[0].SceneID, finalOrder)
}

func applyClipSplit(ctx context.Context, tx *sql.Tx, split changeset.ClipSplit, at time.Time) error {
	originalOrder, err := orderedActiveClipIDs(ctx, tx, split.Source.SceneID, "")
	if err != nil {
		return err
	}
	index := slices.Index(originalOrder, split.Source.ID)
	if index < 0 {
		return ErrChangeSetPreconditionFailed
	}
	write, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1,updated_at_ms=?,deleted_at_ms=?,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), split.Source.ID.String(), split.Source.Revision)
	if err != nil {
		return err
	}
	if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
		return err
	}
	for _, replacement := range split.Replacements {
		clip := replacement.Clip
		clip.CreatedAt, clip.UpdatedAt = at, at
		if err := insertClip(ctx, tx, clip); err != nil {
			return err
		}
		for assetIndex, assetID := range replacement.AssetIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO clip_asset_references (clip_id,asset_id,position) VALUES (?,?,?)`, clip.ID.String(), assetID.String(), assetIndex+1); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO clip_derivations (clip_id,source_clip_id,position) VALUES (?,?,1)`, clip.ID.String(), split.Source.ID.String()); err != nil {
			return err
		}
	}
	if err := inheritClipSplitFrame(ctx, tx, split.Replacements[0].Clip.ID, split.FirstFrame, at); err != nil {
		return err
	}
	if err := inheritClipSplitFrame(ctx, tx, split.Replacements[len(split.Replacements)-1].Clip.ID, split.LastFrame, at); err != nil {
		return err
	}
	finalOrder := make([]project.ClipID, 0, len(originalOrder)-1+len(split.Replacements))
	finalOrder = append(finalOrder, originalOrder[:index]...)
	for _, replacement := range split.Replacements {
		finalOrder = append(finalOrder, replacement.Clip.ID)
	}
	finalOrder = append(finalOrder, originalOrder[index+1:]...)
	return reorderClipPositions(ctx, tx, split.Source.SceneID, finalOrder)
}

func inheritClipSplitFrame(ctx context.Context, tx *sql.Tx, target project.ClipID, frame changeset.ClipMergeFrame, at time.Time) error {
	if frame.SourceVersion == nil {
		return nil
	}
	targetSlotID, err := project.FixedMediaSlotID(target.String(), frame.Kind)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,1,0,0,1,?,?,NULL)`, frame.TargetVersionID.String(), targetSlotID.String(), frame.SourceVersion.ArtifactID.String(), at.UnixMilli(), at.UnixMilli()); err != nil {
		return err
	}
	write, err := tx.ExecContext(ctx, `UPDATE media_slots SET current_media_version_id=?,revision=revision+1,updated_at_ms=? WHERE media_slot_id=? AND current_media_version_id IS NULL`, frame.TargetVersionID.String(), at.UnixMilli(), targetSlotID.String())
	if err != nil {
		return err
	}
	return requireOne(write, ErrChangeSetPreconditionFailed)
}

func transactionClipAssetIDs(ctx context.Context, tx *sql.Tx, clipID project.ClipID) ([]project.AssetID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT asset_id FROM clip_asset_references WHERE clip_id=? ORDER BY position,asset_id`, clipID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []project.AssetID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := project.ParseAssetID(raw)
		if err != nil {
			return nil, ErrInvalidStoredProjectData
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func replaceClipAssetReferences(ctx context.Context, tx *sql.Tx, clipID project.ClipID, assetIDs []project.AssetID) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM clip_asset_references WHERE clip_id=?`, clipID.String()); err != nil {
		return err
	}
	for index, assetID := range assetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO clip_asset_references (clip_id,asset_id,position) VALUES (?,?,?)`, clipID.String(), assetID.String(), index+1); err != nil {
			return err
		}
	}
	return nil
}

func transactionClipReferenceArtifactIDs(ctx context.Context, tx *sql.Tx, clipID project.ClipID) ([]project.ArtifactID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id FROM clip_reference_media WHERE clip_id=? AND deleted_at_ms IS NULL ORDER BY created_at_ms,artifact_id`, clipID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []project.ArtifactID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := project.ParseArtifactID(raw)
		if err != nil {
			return nil, ErrInvalidStoredProjectData
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func replaceClipReferenceArtifacts(ctx context.Context, tx *sql.Tx, clipID project.ClipID, artifactIDs []project.ArtifactID, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE clip_reference_media SET deleted_at_ms=? WHERE clip_id=? AND deleted_at_ms IS NULL`, at.UnixMilli(), clipID.String()); err != nil {
		return err
	}
	for index, artifactID := range artifactIDs {
		createdAt := at.UnixMilli() + int64(index)
		result, err := tx.ExecContext(ctx, `UPDATE clip_reference_media SET created_at_ms=?,deleted_at_ms=NULL WHERE clip_id=? AND artifact_id=?`, createdAt, clipID.String(), artifactID.String())
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO clip_reference_media (clip_id,artifact_id,created_at_ms,deleted_at_ms) VALUES (?,?,?,NULL)`, clipID.String(), artifactID.String(), createdAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyRevertChangeSet derives the inverse from the immutable stored operations.
// Reversing in descending position order preserves parent/child relationships.
func applyRevertChangeSet(ctx context.Context, tx *sql.Tx, originalID changeset.ChangeSetID, at time.Time) error {
	operations, err := transactionChangeSetOperations(ctx, tx, originalID)
	if err != nil {
		return err
	}
	var scoped int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM change_set_scopes WHERE change_set_id=?`, originalID.String()).Scan(&scoped); err != nil {
		return err
	}
	hadStoryboard := hasStoryboardOperations(operations)
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		switch operation.Type {
		case changeset.ReplaceEpisodeScript:
			write, err := tx.ExecContext(ctx, `UPDATE episodes SET script_markdown=?,script_revision=script_revision+1,revision=revision+1,updated_at_ms=? WHERE episode_id=? AND script_markdown=?`, operation.EpisodeScript.Before, at.UnixMilli(), operation.EpisodeScript.EpisodeID.String(), operation.EpisodeScript.After)
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		case changeset.UpdateAssetPrompt:
			write, err := tx.ExecContext(ctx, `UPDATE assets SET prompt=?,revision=revision+1,updated_at_ms=? WHERE asset_id=? AND deleted_at_ms IS NULL AND prompt=?`, operation.AssetPrompt.Before, at.UnixMilli(), operation.AssetPrompt.AssetID.String(), operation.AssetPrompt.After)
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		case changeset.UpdateClipPrompts:
			column, err := clipVideoPromptColumn(operation.ClipPrompts.VideoMode)
			if err != nil {
				return err
			}
			query := fmt.Sprintf(`UPDATE clips SET first_image_prompt=?,last_image_prompt=?,%s=?,revision=revision+1,updated_at_ms=? WHERE clip_id=? AND deleted_at_ms IS NULL AND first_image_prompt=? AND last_image_prompt=? AND %s=?`, column, column)
			write, err := tx.ExecContext(ctx, query, operation.ClipPrompts.FirstBefore, operation.ClipPrompts.LastBefore, operation.ClipPrompts.VideoBefore, at.UnixMilli(), operation.ClipPrompts.ClipID.String(), operation.ClipPrompts.FirstAfter, operation.ClipPrompts.LastAfter, operation.ClipPrompts.VideoAfter)
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
			if operation.ClipPrompts.AssetBindingsRecorded {
				if err := replaceClipAssetReferences(ctx, tx, operation.ClipPrompts.ClipID, operation.ClipPrompts.AssetIDsBefore); err != nil {
					return err
				}
			}
			if operation.ClipPrompts.ReferenceBindingsRecorded {
				if err := replaceClipReferenceArtifacts(ctx, tx, operation.ClipPrompts.ClipID, operation.ClipPrompts.ReferenceArtifactIDsBefore, at); err != nil {
					return err
				}
			}
		case changeset.AddClipAssetRef:
			write, err := tx.ExecContext(ctx, `DELETE FROM clip_asset_references WHERE clip_id=? AND asset_id=? AND position=?`, operation.ClipAssetRef.ClipID.String(), operation.ClipAssetRef.AssetID.String(), operation.ClipAssetRef.Position)
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		case changeset.MergeClips:
			if err := revertClipMerge(ctx, tx, *operation.ClipMerge, at); err != nil {
				return err
			}
		case changeset.SplitClip:
			if err := revertClipSplit(ctx, tx, *operation.ClipSplit, at); err != nil {
				return err
			}
		case changeset.CreateClip:
			write, err := tx.ExecContext(ctx, `UPDATE clips SET deleted_at_ms=?,revision=revision+1,updated_at_ms=? WHERE clip_id=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), operation.ClipCreation.Clip.ID.String())
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		case changeset.CreateScene:
			write, err := tx.ExecContext(ctx, `UPDATE scenes SET deleted_at_ms=?,revision=revision+1,updated_at_ms=? WHERE scene_id=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), operation.SceneCreation.Scene.ID.String())
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		case changeset.CreateEpisodeAsset:
			slotID, err := project.FixedMediaSlotID(operation.AssetCreation.AssetID.String(), project.MediaSlotAssetPrimaryImage)
			if err != nil {
				return err
			}
			if operation.AssetCreation.InheritedMediaVersionID != "" {
				write, err := tx.ExecContext(ctx, `DELETE FROM media_versions WHERE media_version_id=? AND media_slot_id=?`, operation.AssetCreation.InheritedMediaVersionID.String(), slotID.String())
				if err != nil {
					return err
				}
				if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM media_slots WHERE media_slot_id=?`, slotID.String()); err != nil {
				return err
			}
			write, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE asset_id=? AND deleted_at_ms IS NULL`, operation.AssetCreation.AssetID.String())
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
			if operation.AssetCreation.SourceAssetID == "" {
				write, err = tx.ExecContext(ctx, `DELETE FROM asset_identities WHERE asset_identity_id=? AND NOT EXISTS (SELECT 1 FROM assets WHERE asset_identity_id=?)`, operation.AssetCreation.IdentityID.String(), operation.AssetCreation.IdentityID.String())
				if err != nil {
					return err
				}
			}
		case changeset.RestoreEpisodeAsset:
			write, err := tx.ExecContext(ctx, `UPDATE assets SET deleted_at_ms=?,revision=revision+1,updated_at_ms=? WHERE asset_id=? AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), operation.AssetRestoration.AssetID.String())
			if err != nil {
				return err
			}
			if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
				return err
			}
		default:
			return ErrInvalidStoredProjectData
		}
	}
	if scoped != 0 {
		if err := restoreHistoryResources(ctx, tx, originalID, at); err != nil {
			return err
		}
	}
	if hadStoryboard {
		write, err := tx.ExecContext(ctx, `UPDATE episodes SET storyboard_revision=storyboard_revision+1,revision=revision+1,updated_at_ms=? WHERE episode_id=(SELECT episode_id FROM change_sets WHERE change_set_id=?)`, at.UnixMilli(), originalID.String())
		if err != nil {
			return err
		}
		return requireOne(write, ErrChangeSetPreconditionFailed)
	}
	return nil
}

func revertClipMerge(ctx context.Context, tx *sql.Tx, merge changeset.ClipMerge, at time.Time) error {
	activeOrder, err := orderedActiveClipIDs(ctx, tx, merge.Replacement.SceneID, "")
	if err != nil {
		return err
	}
	index := slices.Index(activeOrder, merge.Replacement.ID)
	if index < 0 {
		return ErrChangeSetPreconditionFailed
	}
	write, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1,updated_at_ms=?,deleted_at_ms=?,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=1 AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), merge.Replacement.ID.String())
	if err != nil {
		return err
	}
	if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
		return err
	}
	maximum, err := maximumActiveClipOrderKey(ctx, tx, merge.Replacement.SceneID)
	if err != nil || maximum > math.MaxInt-(len(merge.Sources)+1)*project.StoryboardOrderStep {
		return ErrClipPositionConflict
	}
	temporaryBase := maximum + project.StoryboardOrderStep
	for index, source := range merge.Sources {
		// A trailing Clip may now occupy one of the source Clips' original
		// positions. Restore sources above the active range first, then let the
		// common reorder helper atomically put every Clip back in sequence.
		write, err := tx.ExecContext(ctx, `UPDATE clips SET position=?,revision=revision+1,updated_at_ms=?,deleted_at_ms=NULL,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NOT NULL`, temporaryBase+index*project.StoryboardOrderStep, at.UnixMilli(), source.ID.String(), source.Revision+1)
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
			return err
		}
	}
	finalOrder := make([]project.ClipID, 0, len(activeOrder)-1+len(merge.Sources))
	finalOrder = append(finalOrder, activeOrder[:index]...)
	for _, source := range merge.Sources {
		finalOrder = append(finalOrder, source.ID)
	}
	finalOrder = append(finalOrder, activeOrder[index+1:]...)
	return reorderClipPositions(ctx, tx, merge.Replacement.SceneID, finalOrder)
}

func revertClipSplit(ctx context.Context, tx *sql.Tx, split changeset.ClipSplit, at time.Time) error {
	activeOrder, err := orderedActiveClipIDs(ctx, tx, split.Source.SceneID, "")
	if err != nil {
		return err
	}
	start := -1
	for index := 0; index+len(split.Replacements) <= len(activeOrder); index++ {
		matches := true
		for replacementIndex, replacement := range split.Replacements {
			if activeOrder[index+replacementIndex] != replacement.Clip.ID {
				matches = false
				break
			}
		}
		if matches {
			start = index
			break
		}
	}
	if start < 0 {
		return ErrChangeSetPreconditionFailed
	}
	for _, replacement := range split.Replacements {
		write, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1,updated_at_ms=?,deleted_at_ms=?,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=1 AND deleted_at_ms IS NULL`, at.UnixMilli(), at.UnixMilli(), replacement.Clip.ID.String())
		if err != nil {
			return err
		}
		if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
			return err
		}
	}
	maximum, err := maximumActiveClipOrderKey(ctx, tx, split.Source.SceneID)
	if err != nil || maximum > math.MaxInt-project.StoryboardOrderStep {
		return ErrClipPositionConflict
	}
	write, err := tx.ExecContext(ctx, `UPDATE clips SET position=?,revision=revision+1,updated_at_ms=?,deleted_at_ms=NULL,deleted_with_scene_id=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NOT NULL`, maximum+project.StoryboardOrderStep, at.UnixMilli(), split.Source.ID.String(), split.Source.Revision+1)
	if err != nil {
		return err
	}
	if err := requireOne(write, ErrChangeSetPreconditionFailed); err != nil {
		return err
	}
	finalOrder := make([]project.ClipID, 0, len(activeOrder)-len(split.Replacements)+1)
	finalOrder = append(finalOrder, activeOrder[:start]...)
	finalOrder = append(finalOrder, split.Source.ID)
	finalOrder = append(finalOrder, activeOrder[start+len(split.Replacements):]...)
	return reorderClipPositions(ctx, tx, split.Source.SceneID, finalOrder)
}

func clipVideoPromptColumn(mode project.ClipGenerationMode) (string, error) {
	switch mode {
	case project.ClipGenerationFirstImage:
		return "first_image_video_prompt", nil
	case project.ClipGenerationFirstLastImage:
		return "first_last_image_video_prompt", nil
	case project.ClipGenerationReference:
		return "reference_video_prompt", nil
	default:
		return "", ErrInvalidStoredProjectData
	}
}

func finishStoryboardChangeSetApply(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operations []changeset.Operation, at time.Time) error {
	var expected int64
	for _, operation := range operations {
		if operation.SceneCreation != nil || operation.ClipCreation != nil || operation.ClipAssetRef != nil || operation.ClipMerge != nil || operation.ClipSplit != nil {
			expected = operation.Precondition.ExpectedRevision
			break
		}
	}
	write, err := tx.ExecContext(ctx, `UPDATE episodes SET storyboard_revision=storyboard_revision+1,revision=revision+1,updated_at_ms=? WHERE episode_id=? AND storyboard_revision=?`, at.UnixMilli(), owner.EpisodeID.String(), expected)
	if err != nil {
		return err
	}
	return requireOne(write, ErrChangeSetPreconditionFailed)
}

func inheritAssetCurrentMedia(ctx context.Context, tx *sql.Tx, sourceAssetID, targetAssetID project.AssetID, versionID project.MediaVersionID, at time.Time) error {
	sourceSlotID, err := project.FixedMediaSlotID(sourceAssetID.String(), project.MediaSlotAssetPrimaryImage)
	if err != nil {
		return err
	}
	targetSlotID, err := project.FixedMediaSlotID(targetAssetID.String(), project.MediaSlotAssetPrimaryImage)
	if err != nil {
		return err
	}
	var currentVersion sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_media_version_id FROM media_slots WHERE media_slot_id=?`, sourceSlotID.String()).Scan(&currentVersion); err != nil {
		return err
	}
	if !currentVersion.Valid {
		return nil
	}
	var artifactID string
	if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM media_versions WHERE media_version_id=? AND media_slot_id=? AND deleted_at_ms IS NULL`, currentVersion.String, sourceSlotID.String()).Scan(&artifactID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_versions (media_version_id,media_slot_id,artifact_id,sequence_number,trim_in_ms,trim_out_ms,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,1,0,0,1,?,?,NULL)`, versionID.String(), targetSlotID.String(), artifactID, at.UnixMilli(), at.UnixMilli()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE media_slots SET current_media_version_id=?,revision=2,updated_at_ms=? WHERE media_slot_id=?`, versionID.String(), at.UnixMilli(), targetSlotID.String())
	return err
}

func recordChangeSetConflict(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operationID changeset.OperationID, conflictID changeset.ConflictID, code, summary string, expectedRevision int64, at time.Time) error {
	var storedOperationID any
	if operationID != "" {
		storedOperationID = operationID.String()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO change_set_conflicts (conflict_id,change_set_id,operation_id,code,summary,detected_at_ms) VALUES (?,?,?,?,?,?)`, conflictID.String(), owner.ID.String(), storedOperationID, code, summary, at.UnixMilli())
	if err != nil {
		return err
	}
	write, err := tx.ExecContext(ctx, `UPDATE change_sets SET status='conflicted',revision=revision+1,failure_code=?,failure_summary=?,updated_at_ms=?,resolved_at_ms=? WHERE change_set_id=? AND status='pending' AND revision=?`, code, summary, at.UnixMilli(), at.UnixMilli(), owner.ID.String(), expectedRevision)
	if err != nil {
		return err
	}
	return requireOne(write, ErrChangeSetRevisionConflict)
}

func replayReceipt(ctx context.Context, tx *sql.Tx, expected changeset.CommandReceipt) (bool, error) {
	var hash, command, changeSetID string
	err := tx.QueryRowContext(ctx, `SELECT request_hash,command_type,change_set_id FROM change_set_command_receipts WHERE project_id=? AND operation_key=?`, expected.ProjectID.String(), expected.OperationKey).Scan(&hash, &command, &changeSetID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != expected.RequestHash || command != string(expected.Command) || changeSetID != expected.ChangeSetID.String() {
		return false, ErrChangeSetOperationConflict
	}
	return true, nil
}

func replayBeginReceipt(ctx context.Context, tx *sql.Tx, expected changeset.CommandReceipt) (bool, changeset.ChangeSetID, error) {
	var hash, command, changeSetID string
	err := tx.QueryRowContext(ctx, `SELECT request_hash,command_type,change_set_id FROM change_set_command_receipts WHERE project_id=? AND operation_key=?`, expected.ProjectID.String(), expected.OperationKey).Scan(&hash, &command, &changeSetID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if hash != expected.RequestHash || command != string(changeset.CommandBegin) {
		return false, "", ErrChangeSetOperationConflict
	}
	id, err := changeset.ParseChangeSetID(changeSetID)
	if err != nil {
		return false, "", ErrInvalidStoredProjectData
	}
	return true, id, nil
}

func insertReceipt(ctx context.Context, tx *sql.Tx, value changeset.CommandReceipt) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO change_set_command_receipts (project_id,operation_key,request_hash,command_type,change_set_id,created_at_ms) VALUES (?,?,?,?,?,?)`, value.ProjectID.String(), value.OperationKey, value.RequestHash, string(value.Command), value.ChangeSetID.String(), value.CreatedAt.UnixMilli())
	return err
}

func validChangeSetSummary(value string, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\x00") && utf8.RuneCountInString(value) <= 2_000 && (!required || value != "")
}
