package projectstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
)

const retainedUndoChangeSets = 200

type historyScope struct {
	Kind string
	ID   string
}

type historyResource struct {
	Kind    string
	ID      string
	Created bool
}

type guardClip struct {
	ID   string `json:"id"`
	Head string `json:"head"`
}

type scopeGuard struct {
	Exists bool        `json:"exists"`
	Head   string      `json:"head,omitempty"`
	Clips  []guardClip `json:"clips,omitempty"`
}

func deriveHistoryScope(ctx context.Context, tx *sql.Tx, owner changeset.ChangeSet, operations []changeset.Operation) (historyScope, []historyResource, error) {
	if len(operations) == 0 {
		return historyScope{}, nil, ErrChangeSetBatchInvalid
	}
	if len(operations) == 1 && operations[0].ChangeSetRevert != nil {
		var scope historyScope
		err := tx.QueryRowContext(ctx, `SELECT scope_kind,scope_id FROM change_set_scopes WHERE change_set_id=?`, operations[0].ChangeSetRevert.OriginalChangeSetID.String()).Scan(&scope.Kind, &scope.ID)
		if err == nil {
			return scope, nil, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return historyScope{}, nil, err
		}
		// ChangeSets written before scoped history was introduced retain the
		// legacy global-LIFO checks, but still need a semantic scope so their
		// inverse can be finalized after migration.
		original, err := transactionChangeSet(ctx, tx, operations[0].ChangeSetRevert.OriginalChangeSetID)
		if err != nil {
			return historyScope{}, nil, err
		}
		originalOperations, err := transactionChangeSetOperations(ctx, tx, original.ID)
		if err != nil {
			return historyScope{}, nil, err
		}
		return deriveHistoryScope(ctx, tx, original, originalOperations)
	}
	resources := make(map[string]historyResource)
	var scope historyScope
	setScope := func(kind, id string) error {
		if scope.Kind == "" {
			scope = historyScope{Kind: kind, ID: id}
			return nil
		}
		if scope.Kind != kind || scope.ID != id {
			return ErrChangeSetBatchInvalid
		}
		return nil
	}
	add := func(kind, id string, created bool) {
		key := kind + ":" + id
		value := resources[key]
		value.Kind, value.ID, value.Created = kind, id, value.Created || created
		resources[key] = value
	}

	var storyboardSceneID string
	for _, operation := range operations {
		switch operation.Type {
		case changeset.CreateScene:
			id := operation.SceneCreation.Scene.ID.String()
			if storyboardSceneID != "" && storyboardSceneID != id {
				return historyScope{}, nil, ErrChangeSetBatchInvalid
			}
			storyboardSceneID = id
			add("scene", id, true)
		case changeset.CreateClip:
			id := operation.ClipCreation.Clip.ID.String()
			if storyboardSceneID != "" && storyboardSceneID != operation.ClipCreation.Clip.SceneID.String() {
				return historyScope{}, nil, ErrChangeSetBatchInvalid
			}
			storyboardSceneID = operation.ClipCreation.Clip.SceneID.String()
			add("clip", id, true)
		case changeset.MergeClips:
			merge := operation.ClipMerge
			if storyboardSceneID != "" && storyboardSceneID != merge.Replacement.SceneID.String() {
				return historyScope{}, nil, ErrChangeSetBatchInvalid
			}
			storyboardSceneID = merge.Replacement.SceneID.String()
			for _, source := range merge.Sources {
				add("clip", source.ID.String(), false)
			}
			add("clip", merge.Replacement.ID.String(), true)
		case changeset.SplitClip:
			split := operation.ClipSplit
			if storyboardSceneID != "" && storyboardSceneID != split.Source.SceneID.String() {
				return historyScope{}, nil, ErrChangeSetBatchInvalid
			}
			storyboardSceneID = split.Source.SceneID.String()
			add("clip", split.Source.ID.String(), false)
			for _, replacement := range split.Replacements {
				add("clip", replacement.Clip.ID.String(), true)
			}
		case changeset.AddClipAssetRef:
			add("clip", operation.ClipAssetRef.ClipID.String(), false)
		case changeset.UpdateClipPrompts:
			if err := setScope("clip", operation.ClipPrompts.ClipID.String()); err != nil {
				return historyScope{}, nil, err
			}
			add("clip", operation.ClipPrompts.ClipID.String(), false)
		case changeset.UpdateAssetPrompt:
			if err := setScope("asset", operation.AssetPrompt.AssetID.String()); err != nil {
				return historyScope{}, nil, err
			}
			add("asset", operation.AssetPrompt.AssetID.String(), false)
		case changeset.CreateEpisodeAsset:
			if err := setScope("asset", operation.AssetCreation.AssetID.String()); err != nil {
				return historyScope{}, nil, err
			}
			add("asset", operation.AssetCreation.AssetID.String(), true)
		case changeset.RestoreEpisodeAsset:
			if err := setScope("asset", operation.AssetRestoration.AssetID.String()); err != nil {
				return historyScope{}, nil, err
			}
			add("asset", operation.AssetRestoration.AssetID.String(), false)
		case changeset.ReplaceEpisodeScript:
			if err := setScope("episode_script", owner.EpisodeID.String()); err != nil {
				return historyScope{}, nil, err
			}
			add("episode_script", owner.EpisodeID.String(), false)
		default:
			return historyScope{}, nil, ErrChangeSetBatchInvalid
		}
	}
	if storyboardSceneID != "" {
		if scope.Kind != "" {
			return historyScope{}, nil, ErrChangeSetBatchInvalid
		}
		scope = historyScope{Kind: "scene", ID: storyboardSceneID}
	}
	if scope.Kind == "" {
		return historyScope{}, nil, ErrChangeSetBatchInvalid
	}
	result := make([]historyResource, 0, len(resources))
	for _, value := range resources {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind == result[j].Kind {
			return result[i].ID < result[j].ID
		}
		return result[i].Kind < result[j].Kind
	})
	return scope, result, nil
}

func prepareHistoryResources(ctx context.Context, tx *sql.Tx, changeSetID changeset.ChangeSetID, resources []historyResource, at time.Time) error {
	for _, resource := range resources {
		before := ""
		if !resource.Created {
			var err error
			before, err = ensureResourceHead(ctx, tx, resource.Kind, resource.ID, at)
			if err != nil {
				return err
			}
		}
		after := changeSetID.String() + ":" + resource.Kind + ":" + resource.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO change_set_resource_heads (change_set_id,resource_kind,resource_id,before_head,after_head) VALUES (?,?,?,?,?)`, changeSetID.String(), resource.Kind, resource.ID, before, after); err != nil {
			return err
		}
	}
	return nil
}

func activateHistoryResources(ctx context.Context, tx *sql.Tx, changeSetID changeset.ChangeSetID, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT resource_kind,resource_id,after_head FROM change_set_resource_heads WHERE change_set_id=?`, changeSetID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct{ kind, id, head string }
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.kind, &value.id, &value.head); err != nil {
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, value := range items {
		if err := setResourceHead(ctx, tx, value.kind, value.id, value.head, "changeset", at); err != nil {
			return err
		}
	}
	return nil
}

func restoreHistoryResources(ctx context.Context, tx *sql.Tx, originalID changeset.ChangeSetID, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT resource_kind,resource_id,before_head FROM change_set_resource_heads WHERE change_set_id=?`, originalID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct{ kind, id, head string }
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.kind, &value.id, &value.head); err != nil {
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, value := range items {
		if value.head == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM resource_history_heads WHERE resource_kind=? AND resource_id=?`, value.kind, value.id); err != nil {
				return err
			}
			continue
		}
		source := "changeset"
		if len(value.head) >= 7 && value.head[:7] == "manual:" {
			source = "manual"
		} else if len(value.head) >= 9 && value.head[:9] == "baseline:" {
			source = "baseline"
		}
		if err := setResourceHead(ctx, tx, value.kind, value.id, value.head, source, at); err != nil {
			return err
		}
	}
	return nil
}

func ensureResourceHead(ctx context.Context, tx *sql.Tx, kind, id string, at time.Time) (string, error) {
	var head string
	err := tx.QueryRowContext(ctx, `SELECT head_token FROM resource_history_heads WHERE resource_kind=? AND resource_id=?`, kind, id).Scan(&head)
	if err == nil {
		return head, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	revision, err := resourceRevision(ctx, tx, kind, id)
	if err != nil {
		return "", err
	}
	head = fmt.Sprintf("baseline:%s:%s:%d", kind, id, revision)
	if err := setResourceHead(ctx, tx, kind, id, head, "baseline", at); err != nil {
		return "", err
	}
	return head, nil
}

func resourceRevision(ctx context.Context, tx *sql.Tx, kind, id string) (int64, error) {
	var revision int64
	var query string
	switch kind {
	case "asset":
		query = `SELECT revision FROM assets WHERE asset_id=?`
	case "clip":
		query = `SELECT revision FROM clips WHERE clip_id=?`
	case "scene":
		query = `SELECT revision FROM scenes WHERE scene_id=?`
	case "episode_script":
		query = `SELECT script_revision FROM episodes WHERE episode_id=?`
	default:
		return 0, ErrInvalidStoredProjectData
	}
	if err := tx.QueryRowContext(ctx, query, id).Scan(&revision); err != nil {
		return 0, err
	}
	return revision, nil
}

func setResourceHead(ctx context.Context, tx *sql.Tx, kind, id, token, source string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO resource_history_heads (resource_kind,resource_id,head_token,source_kind,updated_at_ms) VALUES (?,?,?,?,?)
ON CONFLICT(resource_kind,resource_id) DO UPDATE SET head_token=excluded.head_token,source_kind=excluded.source_kind,updated_at_ms=excluded.updated_at_ms`, kind, id, token, source, at.UnixMilli())
	return err
}

func markManualResourceHead(ctx context.Context, tx *sql.Tx, kind, id string, revision int64, at time.Time) error {
	token := fmt.Sprintf("manual:%s:%s:%d:%d", kind, id, revision, at.UnixMilli())
	return setResourceHead(ctx, tx, kind, id, token, "manual", at)
}

func scopeGuardJSON(ctx context.Context, tx *sql.Tx, scope historyScope, at time.Time) (string, error) {
	guard := scopeGuard{}
	switch scope.Kind {
	case "asset":
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT revision FROM assets WHERE asset_id=? AND deleted_at_ms IS NULL`, scope.ID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		guard.Exists = true
		guard.Head, err = ensureResourceHead(ctx, tx, scope.Kind, scope.ID, at)
		if err != nil {
			return "", err
		}
	case "clip":
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT revision FROM clips WHERE clip_id=? AND deleted_at_ms IS NULL`, scope.ID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		guard.Exists = true
		guard.Head, err = ensureResourceHead(ctx, tx, scope.Kind, scope.ID, at)
		if err != nil {
			return "", err
		}
	case "scene":
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT revision FROM scenes WHERE scene_id=? AND deleted_at_ms IS NULL`, scope.ID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		guard.Exists = true
		guard.Head, err = ensureResourceHead(ctx, tx, scope.Kind, scope.ID, at)
		if err != nil {
			return "", err
		}
		rows, err := tx.QueryContext(ctx, `SELECT clip_id,revision FROM clips WHERE scene_id=? AND deleted_at_ms IS NULL ORDER BY position,clip_id`, scope.ID)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var clip guardClip
			var clipRevision int64
			if err := rows.Scan(&clip.ID, &clipRevision); err != nil {
				rows.Close()
				return "", err
			}
			clip.Head, err = ensureResourceHead(ctx, tx, "clip", clip.ID, at)
			if err != nil {
				rows.Close()
				return "", err
			}
			guard.Clips = append(guard.Clips, clip)
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	case "episode_script":
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT script_revision FROM episodes WHERE episode_id=?`, scope.ID).Scan(&revision); err != nil {
			return "", err
		}
		guard.Exists = true
		var err error
		guard.Head, err = ensureResourceHead(ctx, tx, scope.Kind, scope.ID, at)
		if err != nil {
			return "", err
		}
	default:
		return "", ErrInvalidStoredProjectData
	}
	encoded, err := json.Marshal(guard)
	return string(encoded), err
}

func scopeGuardToken(value string) string {
	// Canonicalize guards before hashing. Scene guards written by schema 13
	// included numeric Clip positions; order is now represented by array order,
	// so decoding into scopeGuard intentionally discards those obsolete values
	// without invalidating otherwise safe retained ChangeSets.
	var guard scopeGuard
	if err := json.Unmarshal([]byte(value), &guard); err == nil {
		if encoded, err := json.Marshal(guard); err == nil {
			value = string(encoded)
		}
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func recordChangeSetScope(ctx context.Context, tx *sql.Tx, id changeset.ChangeSetID, scope historyScope, before, after string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO change_set_scopes (change_set_id,scope_kind,scope_id,before_guard_json,after_guard_json,created_at_ms) VALUES (?,?,?,?,?,?)`, id.String(), scope.Kind, scope.ID, before, after, at.UnixMilli())
	if err != nil {
		return err
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM change_sets WHERE change_set_id=?`, id.String()).Scan(&projectID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE change_set_scopes SET undo_expired=1 WHERE change_set_id IN (
SELECT s.change_set_id FROM change_set_scopes s
JOIN change_sets cs ON cs.change_set_id=s.change_set_id
WHERE cs.project_id=? AND s.undo_expired=0
ORDER BY s.created_at_ms DESC,s.change_set_id DESC LIMIT -1 OFFSET ?
)`, projectID, retainedUndoChangeSets)
	if err != nil {
		return err
	}
	return compactExpiredChangeSets(ctx, tx, projectID)
}

func compactExpiredChangeSets(ctx context.Context, tx *sql.Tx, projectID string) error {
	for _, table := range []string{
		"change_set_operations",
		"change_set_asset_creations",
		"change_set_asset_restorations",
		"change_set_storyboard_operations",
		"change_set_clip_merge_operations",
		"change_set_script_operations",
		"change_set_resource_heads",
	} {
		query := fmt.Sprintf(`DELETE FROM %s WHERE change_set_id IN (
SELECT scope.change_set_id FROM change_set_scopes scope
JOIN change_sets cs ON cs.change_set_id=scope.change_set_id
WHERE cs.project_id=? AND scope.undo_expired=1
)`, table)
		if _, err := tx.ExecContext(ctx, query, projectID); err != nil {
			return err
		}
	}
	return nil
}

func scopedRevertGuard(ctx context.Context, tx *sql.Tx, id changeset.ChangeSetID, at time.Time) (*operationConflict, bool, error) {
	var scope historyScope
	var expected string
	var expired int
	err := tx.QueryRowContext(ctx, `SELECT scope_kind,scope_id,after_guard_json,undo_expired FROM change_set_scopes WHERE change_set_id=?`, id.String()).Scan(&scope.Kind, &scope.ID, &expected, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if expired != 0 {
		return &operationConflict{"undo_expired", "The ChangeSet is outside the most recent 200 reversible changes"}, true, nil
	}
	current, err := scopeGuardJSON(ctx, tx, scope, at)
	if err != nil {
		return nil, true, err
	}
	if scopeGuardToken(current) != scopeGuardToken(expected) {
		return &operationConflict{"undo_target_changed", "The ChangeSet scope or one of its child resources changed later"}, true, nil
	}
	return nil, true, nil
}
