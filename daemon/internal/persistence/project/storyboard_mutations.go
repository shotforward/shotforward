package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrSceneRevisionConflict      = errors.New("scene revision conflict")
	ErrClipRevisionConflict       = errors.New("clip revision conflict")
	ErrStoryboardRevisionConflict = errors.New("storyboard revision conflict")
	ErrScenePositionConflict      = errors.New("scene position conflict")
	ErrClipPositionConflict       = errors.New("clip position conflict")
	ErrClipAssetAlreadyBound      = errors.New("clip asset already bound")
	ErrClipAssetNotBound          = errors.New("clip asset not bound")
	ErrClipHasActiveGeneration    = errors.New("clip has active generation")
	ErrClipStructureConflict      = errors.New("clip replacement structure conflicts")
	ErrSceneDeleted               = errors.New("scene is deleted")
	ErrSceneNotEmpty              = errors.New("scene contains active Clips")
	ErrSceneOrderMismatch         = errors.New("scene order does not match active Storyboard")
)

func (s *Store) CreateScene(ctx context.Context, scene domain.Scene, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := scene.Validate(); err != nil {
		return fmt.Errorf("create Scene: %w", err)
	}
	if scene.Revision != 1 || scene.IsDeleted() {
		return errors.New("create Scene: initial state is invalid")
	}
	if err := validateStoryboardMutation(expectedStoryboardRevision, updatedAt, scene.UpdatedAt); err != nil {
		return fmt.Errorf("create Scene: %w", err)
	}
	return s.withTransaction(ctx, "create Scene", func(tx *sql.Tx) error {
		if err := validateEpisodeOwner(ctx, tx, scene.EpisodeID, scene.ProjectID); err != nil {
			return err
		}
		maximum, err := maximumActiveSceneOrderKey(ctx, tx, scene.EpisodeID)
		if err != nil {
			return err
		}
		if scene.Position <= maximum {
			return ErrScenePositionConflict
		}
		if err := advanceStoryboard(ctx, tx, scene.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		if err := insertScene(ctx, tx, scene); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", scene.ID.String(), scene.Revision, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, scene.ProjectID, updatedAt)
	})
}

func (s *Store) UpdateScene(ctx context.Context, scene domain.Scene, expectedRevision int64, updatedAt time.Time) error {
	if err := scene.Validate(); err != nil {
		return fmt.Errorf("update Scene: %w", err)
	}
	if err := validateContentMutation(expectedRevision, scene.Revision, updatedAt, scene.UpdatedAt); err != nil {
		return fmt.Errorf("update Scene: %w", err)
	}
	return s.withTransaction(ctx, "update Scene", func(tx *sql.Tx) error {
		current, err := transactionScene(ctx, tx, scene.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrSceneRevisionConflict
		}
		if current.ProjectID != scene.ProjectID || current.EpisodeID != scene.EpisodeID || current.Position != scene.Position || current.CreatedAt != scene.CreatedAt || current.IsDeleted() != scene.IsDeleted() {
			return errors.New("immutable Scene fields changed")
		}
		result, err := tx.ExecContext(ctx, "UPDATE scenes SET title=?, description=?, revision=?, updated_at_ms=? WHERE scene_id=? AND revision=?", scene.Title, scene.Description, scene.Revision, scene.UpdatedAt.UnixMilli(), scene.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrSceneRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", scene.ID.String(), scene.Revision, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, scene.ProjectID, updatedAt)
	})
}

func (s *Store) DeleteScene(ctx context.Context, scene domain.Scene, expectedRevision, expectedStoryboardRevision int64, confirmNonEmpty bool, updatedAt time.Time) error {
	if err := scene.Validate(); err != nil {
		return fmt.Errorf("delete Scene: %w", err)
	}
	if !scene.IsDeleted() || scene.Revision != expectedRevision+1 || scene.UpdatedAt != updatedAt || scene.DeletedAt == nil || *scene.DeletedAt != updatedAt {
		return errors.New("delete Scene: transition is invalid")
	}
	return s.withTransaction(ctx, "delete Scene", func(tx *sql.Tx) error {
		current, err := transactionScene(ctx, tx, scene.ID)
		if err != nil {
			return err
		}
		if current.IsDeleted() || current.Revision != expectedRevision {
			return ErrSceneRevisionConflict
		}
		if current.ProjectID != scene.ProjectID || current.EpisodeID != scene.EpisodeID || current.Position != scene.Position {
			return errors.New("Scene ownership or position changed")
		}
		if !confirmNonEmpty {
			count, err := activeClipCount(ctx, tx, scene.ID)
			if err != nil {
				return err
			}
			if count > 0 {
				return ErrSceneNotEmpty
			}
		}
		if err := advanceStoryboard(ctx, tx, scene.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE scenes SET revision=?, updated_at_ms=?, deleted_at_ms=? WHERE scene_id=? AND revision=? AND deleted_at_ms IS NULL", scene.Revision, updatedAt.UnixMilli(), updatedAt.UnixMilli(), scene.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrSceneRevisionConflict); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1, updated_at_ms=?, deleted_at_ms=?, deleted_with_scene_id=? WHERE scene_id=? AND deleted_at_ms IS NULL`, updatedAt.UnixMilli(), updatedAt.UnixMilli(), scene.ID.String(), scene.ID.String()); err != nil {
			return err
		}
		if err := markSceneClipsManuallyChanged(ctx, tx, scene.ID, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", scene.ID.String(), scene.Revision, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, scene.ProjectID, updatedAt)
	})
}

func (s *Store) RestoreScene(ctx context.Context, scene domain.Scene, expectedRevision, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := scene.Validate(); err != nil {
		return fmt.Errorf("restore Scene: %w", err)
	}
	if scene.IsDeleted() || scene.Revision != expectedRevision+1 || scene.UpdatedAt != updatedAt {
		return errors.New("restore Scene: transition is invalid")
	}
	return s.withTransaction(ctx, "restore Scene", func(tx *sql.Tx) error {
		current, err := transactionScene(ctx, tx, scene.ID)
		if err != nil {
			return err
		}
		if !current.IsDeleted() || current.Revision != expectedRevision {
			return ErrSceneRevisionConflict
		}
		if current.ProjectID != scene.ProjectID || current.EpisodeID != scene.EpisodeID {
			return errors.New("Scene ownership changed")
		}
		position, err := availableSceneOrderKey(ctx, tx, scene.EpisodeID, scene.Position)
		if err != nil {
			return err
		}
		scene.Position = position
		if err := advanceStoryboard(ctx, tx, scene.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE scenes SET position=?, revision=?, updated_at_ms=?, deleted_at_ms=NULL WHERE scene_id=? AND revision=? AND deleted_at_ms IS NOT NULL", position, scene.Revision, updatedAt.UnixMilli(), scene.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrSceneRevisionConflict); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE clips SET revision=revision+1, updated_at_ms=?, deleted_at_ms=NULL, deleted_with_scene_id=NULL WHERE scene_id=? AND deleted_with_scene_id=?`, updatedAt.UnixMilli(), scene.ID.String(), scene.ID.String()); err != nil {
			return err
		}
		if err := markSceneClipsManuallyChanged(ctx, tx, scene.ID, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", scene.ID.String(), scene.Revision, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, scene.ProjectID, updatedAt)
	})
}

// A Scene delete/restore also mutates every child Clip. Recording those child
// heads prevents an older Clip ChangeSet from crossing the manual Scene edit.
func markSceneClipsManuallyChanged(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT clip_id,revision FROM clips WHERE scene_id=? AND updated_at_ms=?`, sceneID.String(), at.UnixMilli())
	if err != nil {
		return err
	}
	type changedClip struct {
		id       string
		revision int64
	}
	var clips []changedClip
	for rows.Next() {
		var clip changedClip
		if err := rows.Scan(&clip.id, &clip.revision); err != nil {
			return err
		}
		clips = append(clips, clip)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, clip := range clips {
		if err := markManualResourceHead(ctx, tx, "clip", clip.id, clip.revision, at); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReorderScenes(ctx context.Context, episodeID domain.EpisodeID, orderedIDs []domain.SceneID, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := episodeID.Validate(); err != nil {
		return fmt.Errorf("reorder Scenes: %w", err)
	}
	if expectedStoryboardRevision < 1 || expectedStoryboardRevision == math.MaxInt64 {
		return errors.New("reorder Scenes: invalid Storyboard revision")
	}
	if err := validateMutationTime(updatedAt); err != nil {
		return fmt.Errorf("reorder Scenes: %w", err)
	}
	seen := make(map[domain.SceneID]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("reorder Scenes: %w", err)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("reorder Scenes: %w", ErrSceneOrderMismatch)
		}
		seen[id] = struct{}{}
	}
	return s.withTransaction(ctx, "reorder Scenes", func(tx *sql.Tx) error {
		var projectValue string
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM episodes WHERE episode_id=?", episodeID.String()).Scan(&projectValue); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEpisodeNotFound
			}
			return err
		}
		projectID, err := domain.ParseProjectID(projectValue)
		if err != nil {
			return invalidStoredData("Episode Project ID", err)
		}
		rows, err := tx.QueryContext(ctx, "SELECT scene_id, revision FROM scenes WHERE episode_id=? AND deleted_at_ms IS NULL ORDER BY position, scene_id", episodeID.String())
		if err != nil {
			return err
		}
		current := make(map[domain.SceneID]int64, len(orderedIDs))
		for rows.Next() {
			var value string
			var revision int64
			if err := rows.Scan(&value, &revision); err != nil {
				rows.Close()
				return err
			}
			id, err := domain.ParseSceneID(value)
			if err != nil {
				rows.Close()
				return invalidStoredData("Scene ID", err)
			}
			current[id] = revision
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(current) != len(orderedIDs) {
			return ErrSceneOrderMismatch
		}
		for _, id := range orderedIDs {
			_, ok := current[id]
			if !ok {
				return ErrSceneOrderMismatch
			}
		}
		if err := advanceStoryboard(ctx, tx, episodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		maximum, err := maximumActiveSceneOrderKey(ctx, tx, episodeID)
		if err != nil || maximum > math.MaxInt-(len(orderedIDs)+1)*domain.StoryboardOrderStep {
			return ErrSceneOrderMismatch
		}
		offset := maximum + (len(orderedIDs)+1)*domain.StoryboardOrderStep
		if _, err := tx.ExecContext(ctx, "UPDATE scenes SET position=position+? WHERE episode_id=? AND deleted_at_ms IS NULL", offset, episodeID.String()); err != nil {
			return err
		}
		for index, id := range orderedIDs {
			if _, err := tx.ExecContext(ctx, "UPDATE scenes SET position=? WHERE scene_id=? AND deleted_at_ms IS NULL", (index+1)*domain.StoryboardOrderStep, id.String()); err != nil {
				return err
			}
			if err := markManualResourceHead(ctx, tx, "scene", id.String(), 0, updatedAt); err != nil {
				return err
			}
		}
		return updateProjectTimestamp(ctx, tx, projectID, updatedAt)
	})
}

func (s *Store) CreateClip(ctx context.Context, clip domain.Clip, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := clip.Validate(); err != nil {
		return fmt.Errorf("create Clip: %w", err)
	}
	if clip.Revision != 1 || clip.IsDeleted() {
		return errors.New("create Clip: initial state is invalid")
	}
	if err := validateStoryboardMutation(expectedStoryboardRevision, updatedAt, clip.UpdatedAt); err != nil {
		return fmt.Errorf("create Clip: %w", err)
	}
	return s.withTransaction(ctx, "create Clip", func(tx *sql.Tx) error {
		scene, err := transactionScene(ctx, tx, clip.SceneID)
		if err != nil {
			return err
		}
		if scene.IsDeleted() {
			return ErrSceneDeleted
		}
		if scene.ProjectID != clip.ProjectID || scene.EpisodeID != clip.EpisodeID {
			return errors.New("Clip Scene ownership does not match")
		}
		maximum, err := maximumActiveClipOrderKey(ctx, tx, clip.SceneID)
		if err != nil {
			return err
		}
		if clip.Position <= maximum {
			return ErrClipPositionConflict
		}
		if err := advanceStoryboard(ctx, tx, clip.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		if err := insertClip(ctx, tx, clip); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), clip.Revision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", clip.SceneID.String(), 0, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func (s *Store) UpdateClip(ctx context.Context, clip domain.Clip, expectedRevision int64, updatedAt time.Time) error {
	if err := clip.Validate(); err != nil {
		return fmt.Errorf("update Clip: %w", err)
	}
	if err := validateContentMutation(expectedRevision, clip.Revision, updatedAt, clip.UpdatedAt); err != nil {
		return fmt.Errorf("update Clip: %w", err)
	}
	return s.withTransaction(ctx, "update Clip", func(tx *sql.Tx) error {
		current, err := transactionClip(ctx, tx, clip.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrClipRevisionConflict
		}
		if current.ProjectID != clip.ProjectID || current.EpisodeID != clip.EpisodeID || current.SceneID != clip.SceneID || current.Position != clip.Position || current.CreatedAt != clip.CreatedAt || current.IsDeleted() != clip.IsDeleted() {
			return errors.New("immutable Clip fields changed")
		}
		result, err := tx.ExecContext(ctx, `UPDATE clips SET title=?, description=?, dialogue=?, creative_intent=?, planned_duration_seconds=?, first_image_prompt=?, last_image_prompt=?, first_image_video_prompt=?, first_last_image_video_prompt=?, reference_video_prompt=?, generation_mode=?, continue_from_previous=?, revision=?, updated_at_ms=? WHERE clip_id=? AND revision=?`, clip.Title, clip.Description, clip.Dialogue, clip.CreativeIntent, clip.PlannedDurationSeconds, clip.FirstImagePrompt, clip.LastImagePrompt, clip.VideoPrompts.FirstImage, clip.VideoPrompts.FirstLastImage, clip.VideoPrompts.Reference, storedClipGenerationMode(clip.GenerationMode), clip.ContinueFromPrevious, clip.Revision, updatedAt.UnixMilli(), clip.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), clip.Revision, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func (s *Store) MoveClip(ctx context.Context, clip domain.Clip, beforeID, afterID domain.ClipID, expectedRevision, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := clip.Validate(); err != nil {
		return fmt.Errorf("move Clip: %w", err)
	}
	if (beforeID != "" && beforeID.Validate() != nil) || (afterID != "" && afterID.Validate() != nil) || (beforeID != "" && afterID != "") {
		return errors.New("move Clip: invalid placement anchor")
	}
	if err := validateContentMutation(expectedRevision, clip.Revision, updatedAt, clip.UpdatedAt); err != nil {
		return fmt.Errorf("move Clip: %w", err)
	}
	return s.withTransaction(ctx, "move Clip", func(tx *sql.Tx) error {
		current, err := transactionClip(ctx, tx, clip.ID)
		if err != nil {
			return err
		}
		if current.IsDeleted() || current.Revision != expectedRevision {
			return ErrClipRevisionConflict
		}
		target, err := transactionScene(ctx, tx, clip.SceneID)
		if err != nil {
			return err
		}
		if target.IsDeleted() {
			return ErrSceneDeleted
		}
		if target.ProjectID != current.ProjectID || target.EpisodeID != current.EpisodeID {
			return errors.New("target Scene belongs to another Episode")
		}
		if clip.ProjectID != current.ProjectID || clip.EpisodeID != current.EpisodeID || clip.CreatedAt != current.CreatedAt {
			return errors.New("immutable Clip fields changed")
		}
		targetOrder, err := orderedActiveClipIDs(ctx, tx, clip.SceneID, clip.ID)
		if err != nil {
			return err
		}
		insertAt := len(targetOrder)
		anchorID := beforeID
		insertAfter := false
		if anchorID == "" {
			anchorID, insertAfter = afterID, true
		}
		if anchorID != "" {
			insertAt = -1
			for index, id := range targetOrder {
				if id == anchorID {
					insertAt = index
					if insertAfter {
						insertAt++
					}
					break
				}
			}
			if insertAt < 0 {
				return ErrClipPositionConflict
			}
		}
		targetOrder = append(targetOrder, "")
		copy(targetOrder[insertAt+1:], targetOrder[insertAt:])
		targetOrder[insertAt] = clip.ID
		var sourceOrder []domain.ClipID
		if current.SceneID != clip.SceneID {
			sourceOrder, err = orderedActiveClipIDs(ctx, tx, current.SceneID, clip.ID)
			if err != nil {
				return err
			}
		}
		if err := advanceStoryboard(ctx, tx, clip.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		if current.SceneID != clip.SceneID {
			maximum, err := maximumActiveClipOrderKey(ctx, tx, clip.SceneID)
			if err != nil || maximum > math.MaxInt-domain.StoryboardOrderStep {
				return ErrClipPositionConflict
			}
			clip.Position = maximum + domain.StoryboardOrderStep
		}
		result, err := tx.ExecContext(ctx, "UPDATE clips SET scene_id=?, position=?, revision=?, updated_at_ms=? WHERE clip_id=? AND revision=?", clip.SceneID.String(), clip.Position, clip.Revision, updatedAt.UnixMilli(), clip.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if len(sourceOrder) > 0 {
			if err := reorderClipPositions(ctx, tx, current.SceneID, sourceOrder); err != nil {
				return err
			}
		}
		if err := reorderClipPositions(ctx, tx, clip.SceneID, targetOrder); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), clip.Revision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", current.SceneID.String(), 0, updatedAt); err != nil {
			return err
		}
		if current.SceneID != clip.SceneID {
			if err := markManualResourceHead(ctx, tx, "scene", clip.SceneID.String(), 0, updatedAt); err != nil {
				return err
			}
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func (s *Store) DeleteClip(ctx context.Context, clip domain.Clip, expectedRevision, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := clip.Validate(); err != nil {
		return fmt.Errorf("delete Clip: %w", err)
	}
	if !clip.IsDeleted() || clip.Revision != expectedRevision+1 || clip.UpdatedAt != updatedAt || clip.DeletedAt == nil || *clip.DeletedAt != updatedAt {
		return errors.New("delete Clip: transition is invalid")
	}
	return s.withTransaction(ctx, "delete Clip", func(tx *sql.Tx) error {
		current, err := transactionClip(ctx, tx, clip.ID)
		if err != nil {
			return err
		}
		if current.IsDeleted() || current.Revision != expectedRevision {
			return ErrClipRevisionConflict
		}
		if err := advanceStoryboard(ctx, tx, current.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE clips SET revision=?, updated_at_ms=?, deleted_at_ms=?, deleted_with_scene_id=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL", clip.Revision, updatedAt.UnixMilli(), updatedAt.UnixMilli(), clip.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), clip.Revision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", current.SceneID.String(), 0, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, current.ProjectID, updatedAt)
	})
}

func (s *Store) RestoreClip(ctx context.Context, clip domain.Clip, expectedRevision, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := clip.Validate(); err != nil {
		return fmt.Errorf("restore Clip: %w", err)
	}
	if clip.IsDeleted() || clip.Revision != expectedRevision+1 || clip.UpdatedAt != updatedAt {
		return errors.New("restore Clip: transition is invalid")
	}
	return s.withTransaction(ctx, "restore Clip", func(tx *sql.Tx) error {
		current, err := transactionClip(ctx, tx, clip.ID)
		if err != nil {
			return err
		}
		if !current.IsDeleted() || current.Revision != expectedRevision {
			return ErrClipRevisionConflict
		}
		if current.DeletedWithSceneID != "" {
			return ErrSceneDeleted
		}
		scene, err := transactionScene(ctx, tx, current.SceneID)
		if err != nil {
			return err
		}
		if scene.IsDeleted() {
			return ErrSceneDeleted
		}
		position, err := availableClipOrderKey(ctx, tx, current.SceneID, clip.Position)
		if err != nil {
			return err
		}
		if err := advanceStoryboard(ctx, tx, current.EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE clips SET position=?, revision=?, updated_at_ms=?, deleted_at_ms=NULL WHERE clip_id=? AND revision=? AND deleted_at_ms IS NOT NULL", position, clip.Revision, updatedAt.UnixMilli(), clip.ID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clip.ID.String(), clip.Revision, updatedAt); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "scene", current.SceneID.String(), 0, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, current.ProjectID, updatedAt)
	})
}

// ReplaceClips atomically implements both Split (one source, multiple
// replacements) and Merge (multiple adjacent sources, one replacement).
// Existing media remains attached to deleted source Clips; replacements start
// with empty media slots and only explicitly supplied Asset bindings.
func (s *Store) ReplaceClips(ctx context.Context, sources []domain.Clip, replacements []domain.ClipReplacement, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if err := validateClipReplacements(sources, replacements, expectedStoryboardRevision, updatedAt); err != nil {
		return fmt.Errorf("replace Clips: %w", err)
	}
	return s.withTransaction(ctx, "replace Clips", func(tx *sql.Tx) error {
		originalOrder, err := orderedActiveClipIDs(ctx, tx, sources[0].SceneID, "")
		if err != nil {
			return err
		}
		sourceStart := -1
		for index := 0; index+len(sources) <= len(originalOrder); index++ {
			matches := true
			for sourceIndex, source := range sources {
				if originalOrder[index+sourceIndex] != source.ID {
					matches = false
					break
				}
			}
			if matches {
				sourceStart = index
				break
			}
		}
		if sourceStart < 0 {
			return ErrClipStructureConflict
		}
		finalOrder := make([]domain.ClipID, 0, len(originalOrder)-len(sources)+len(replacements))
		finalOrder = append(finalOrder, originalOrder[:sourceStart]...)
		for _, replacement := range replacements {
			finalOrder = append(finalOrder, replacement.Clip.ID)
		}
		finalOrder = append(finalOrder, originalOrder[sourceStart+len(sources):]...)
		for _, source := range sources {
			current, err := transactionClip(ctx, tx, source.ID)
			if err != nil {
				return err
			}
			if current.IsDeleted() || current.Revision != source.Revision || current.ProjectID != source.ProjectID || current.EpisodeID != source.EpisodeID || current.SceneID != source.SceneID {
				return ErrClipRevisionConflict
			}
			var active int
			if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM generation_jobs j
    JOIN media_slots s ON s.media_slot_id=j.target_media_slot_id
    WHERE s.owner_kind='clip' AND s.owner_id=? AND j.state IN ('queued','running','stop_requested')
)`, source.ID.String()).Scan(&active); err != nil {
				return err
			}
			if active != 0 {
				return ErrClipHasActiveGeneration
			}
		}
		for _, replacement := range replacements {
			for _, assetID := range replacement.AssetIDs {
				if err := validateBindableAsset(ctx, tx, assetID, replacement.Clip); err != nil {
					return err
				}
			}
		}
		if err := advanceStoryboard(ctx, tx, sources[0].EpisodeID, expectedStoryboardRevision); err != nil {
			return err
		}
		for _, source := range sources {
			result, err := tx.ExecContext(ctx, `UPDATE clips
SET revision=revision+1, updated_at_ms=?, deleted_at_ms=?, deleted_with_scene_id=NULL
WHERE clip_id=? AND revision=? AND deleted_at_ms IS NULL`, updatedAt.UnixMilli(), updatedAt.UnixMilli(), source.ID.String(), source.Revision)
			if err != nil {
				return err
			}
			if err := requireOne(result, ErrClipRevisionConflict); err != nil {
				return err
			}
		}
		maximum, err := maximumActiveClipOrderKey(ctx, tx, sources[0].SceneID)
		if err != nil || maximum > math.MaxInt-(len(replacements)+1)*domain.StoryboardOrderStep {
			return ErrClipStructureConflict
		}
		for index := range replacements {
			replacements[index].Clip.Position = maximum + (index+1)*domain.StoryboardOrderStep
		}
		for _, replacement := range replacements {
			if err := insertClip(ctx, tx, replacement.Clip); err != nil {
				return err
			}
			for index, assetID := range replacement.AssetIDs {
				if _, err := tx.ExecContext(ctx, "INSERT INTO clip_asset_references (clip_id,asset_id,position) VALUES (?,?,?)", replacement.Clip.ID.String(), assetID.String(), index+1); err != nil {
					return err
				}
			}
			for index, sourceID := range replacement.SourceClipIDs {
				if _, err := tx.ExecContext(ctx, "INSERT INTO clip_derivations (clip_id,source_clip_id,position) VALUES (?,?,?)", replacement.Clip.ID.String(), sourceID.String(), index+1); err != nil {
					return err
				}
			}
		}
		if err := reorderClipPositions(ctx, tx, sources[0].SceneID, finalOrder); err != nil {
			return err
		}
		for _, source := range sources {
			if err := markManualResourceHead(ctx, tx, "clip", source.ID.String(), source.Revision+1, updatedAt); err != nil {
				return err
			}
		}
		for _, replacement := range replacements {
			if err := markManualResourceHead(ctx, tx, "clip", replacement.Clip.ID.String(), replacement.Clip.Revision, updatedAt); err != nil {
				return err
			}
		}
		if err := markManualResourceHead(ctx, tx, "scene", sources[0].SceneID.String(), 0, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, sources[0].ProjectID, updatedAt)
	})
}

func validateClipReplacements(sources []domain.Clip, replacements []domain.ClipReplacement, expectedStoryboardRevision int64, updatedAt time.Time) error {
	if len(sources) == 0 || len(replacements) == 0 || expectedStoryboardRevision < 1 || expectedStoryboardRevision == math.MaxInt64 || validateMutationTime(updatedAt) != nil {
		return ErrClipStructureConflict
	}
	first := sources[0]
	if err := first.Validate(); err != nil || first.IsDeleted() {
		return ErrClipStructureConflict
	}
	sourceIDs := make(map[domain.ClipID]struct{}, len(sources))
	var sourceDuration int64
	for _, source := range sources {
		if err := source.Validate(); err != nil || source.IsDeleted() || source.ProjectID != first.ProjectID || source.EpisodeID != first.EpisodeID || source.SceneID != first.SceneID {
			return ErrClipStructureConflict
		}
		if _, exists := sourceIDs[source.ID]; exists || sourceDuration > math.MaxInt64-source.PlannedDurationSeconds {
			return ErrClipStructureConflict
		}
		sourceIDs[source.ID] = struct{}{}
		sourceDuration += source.PlannedDurationSeconds
	}
	replacementIDs := make(map[domain.ClipID]struct{}, len(replacements))
	var replacementDuration int64
	for _, replacement := range replacements {
		if err := replacement.Validate(); err != nil || replacement.Clip.ProjectID != first.ProjectID || replacement.Clip.EpisodeID != first.EpisodeID || replacement.Clip.SceneID != first.SceneID || replacement.Clip.CreatedAt != updatedAt || replacement.Clip.UpdatedAt != updatedAt {
			return ErrClipStructureConflict
		}
		if _, exists := sourceIDs[replacement.Clip.ID]; exists {
			return ErrClipStructureConflict
		}
		if _, exists := replacementIDs[replacement.Clip.ID]; exists || replacementDuration > math.MaxInt64-replacement.Clip.PlannedDurationSeconds {
			return ErrClipStructureConflict
		}
		replacementIDs[replacement.Clip.ID] = struct{}{}
		replacementDuration += replacement.Clip.PlannedDurationSeconds
		for _, sourceID := range replacement.SourceClipIDs {
			if _, exists := sourceIDs[sourceID]; !exists {
				return ErrClipStructureConflict
			}
		}
	}
	// A split deliberately replans each resulting Clip independently. Merges
	// and other replacements still preserve their sources' total duration.
	isSplit := len(sources) == 1 && len(replacements) >= 2
	if !isSplit && sourceDuration != replacementDuration {
		return ErrClipStructureConflict
	}
	return nil
}

func (s *Store) BindClipAsset(ctx context.Context, clipID domain.ClipID, assetID domain.AssetID, expectedRevision int64, updatedAt time.Time) error {
	return s.mutateClipAsset(ctx, "bind Clip Asset", clipID, assetID, "", expectedRevision, updatedAt)
}
func (s *Store) UnbindClipAsset(ctx context.Context, clipID domain.ClipID, assetID domain.AssetID, expectedRevision int64, updatedAt time.Time) error {
	return s.mutateClipAsset(ctx, "unbind Clip Asset", clipID, "", assetID, expectedRevision, updatedAt)
}
func (s *Store) ReplaceClipAsset(ctx context.Context, clipID domain.ClipID, oldAssetID, newAssetID domain.AssetID, expectedRevision int64, updatedAt time.Time) error {
	if oldAssetID == newAssetID {
		return errors.New("replace Clip Asset: old and new Assets are the same")
	}
	return s.mutateClipAsset(ctx, "replace Clip Asset", clipID, newAssetID, oldAssetID, expectedRevision, updatedAt)
}

func (s *Store) mutateClipAsset(ctx context.Context, operation string, clipID domain.ClipID, newAssetID, oldAssetID domain.AssetID, expectedRevision int64, updatedAt time.Time) error {
	if err := clipID.Validate(); err != nil {
		return err
	}
	if newAssetID != "" {
		if err := newAssetID.Validate(); err != nil {
			return err
		}
	}
	if oldAssetID != "" {
		if err := oldAssetID.Validate(); err != nil {
			return err
		}
	}
	if expectedRevision < 1 || expectedRevision == math.MaxInt64 {
		return errors.New("invalid Clip revision")
	}
	if err := validateMutationTime(updatedAt); err != nil {
		return err
	}
	return s.withTransaction(ctx, operation, func(tx *sql.Tx) error {
		clip, err := transactionClip(ctx, tx, clipID)
		if err != nil {
			return err
		}
		if clip.IsDeleted() || clip.Revision != expectedRevision {
			return ErrClipRevisionConflict
		}
		if oldAssetID == "" {
			if err := validateBindableAsset(ctx, tx, newAssetID, clip); err != nil {
				return err
			}
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM clip_asset_references WHERE clip_id=? AND asset_id=?", clipID.String(), newAssetID.String()).Scan(&exists)
			if err == nil {
				return ErrClipAssetAlreadyBound
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var position int
			if err := tx.QueryRowContext(ctx, "SELECT count(*)+1 FROM clip_asset_references WHERE clip_id=?", clipID.String()).Scan(&position); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO clip_asset_references (clip_id, asset_id, position) VALUES (?, ?, ?)", clipID.String(), newAssetID.String(), position); err != nil {
				return err
			}
		} else {
			var position int
			err := tx.QueryRowContext(ctx, "SELECT position FROM clip_asset_references WHERE clip_id=? AND asset_id=?", clipID.String(), oldAssetID.String()).Scan(&position)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrClipAssetNotBound
			}
			if err != nil {
				return err
			}
			if newAssetID == "" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM clip_asset_references WHERE clip_id=? AND asset_id=?", clipID.String(), oldAssetID.String()); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "UPDATE clip_asset_references SET position=position-1 WHERE clip_id=? AND position>?", clipID.String(), position); err != nil {
					return err
				}
			} else {
				if err := validateBindableAsset(ctx, tx, newAssetID, clip); err != nil {
					return err
				}
				var exists int
				err := tx.QueryRowContext(ctx, "SELECT 1 FROM clip_asset_references WHERE clip_id=? AND asset_id=?", clipID.String(), newAssetID.String()).Scan(&exists)
				if err == nil {
					return ErrClipAssetAlreadyBound
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if _, err := tx.ExecContext(ctx, "UPDATE clip_asset_references SET asset_id=? WHERE clip_id=? AND asset_id=?", newAssetID.String(), clipID.String(), oldAssetID.String()); err != nil {
					return err
				}
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE clips SET revision=revision+1, updated_at_ms=? WHERE clip_id=? AND revision=?", updatedAt.UnixMilli(), clipID.String(), expectedRevision)
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipRevisionConflict); err != nil {
			return err
		}
		if err := markManualResourceHead(ctx, tx, "clip", clipID.String(), expectedRevision+1, updatedAt); err != nil {
			return err
		}
		return updateProjectTimestamp(ctx, tx, clip.ProjectID, updatedAt)
	})
}

func validateBindableAsset(ctx context.Context, tx *sql.Tx, assetID domain.AssetID, clip domain.Clip) error {
	asset, err := transactionAsset(ctx, tx, assetID)
	if err != nil {
		return err
	}
	if asset.IsDeleted() {
		return errors.New("Asset is deleted")
	}
	if asset.ProjectID != clip.ProjectID || asset.EpisodeID != clip.EpisodeID {
		return errors.New("Asset belongs to another Episode")
	}
	return nil
}

func insertScene(ctx context.Context, tx *sql.Tx, scene domain.Scene) error {
	var sr, start, end, excerpt any
	if scene.Source != nil {
		sr = scene.Source.ScriptRevision
		start = scene.Source.StartByte
		end = scene.Source.EndByte
		excerpt = scene.Source.Excerpt
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO scenes (scene_id, project_id, episode_id, position, title, description, source_script_revision, source_start_byte, source_end_byte, source_excerpt, revision, created_at_ms, updated_at_ms, deleted_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`, scene.ID.String(), scene.ProjectID.String(), scene.EpisodeID.String(), scene.Position, scene.Title, scene.Description, sr, start, end, excerpt, scene.Revision, scene.CreatedAt.UnixMilli(), scene.UpdatedAt.UnixMilli())
	return err
}

func insertClip(ctx context.Context, tx *sql.Tx, clip domain.Clip) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO clips (clip_id, project_id, episode_id, scene_id, position, title, description, dialogue, creative_intent, planned_duration_seconds, first_image_prompt, last_image_prompt, first_image_video_prompt, first_last_image_video_prompt, reference_video_prompt, generation_mode, continue_from_previous, revision, created_at_ms, updated_at_ms, deleted_at_ms, deleted_with_scene_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`, clip.ID.String(), clip.ProjectID.String(), clip.EpisodeID.String(), clip.SceneID.String(), clip.Position, clip.Title, clip.Description, clip.Dialogue, clip.CreativeIntent, clip.PlannedDurationSeconds, clip.FirstImagePrompt, clip.LastImagePrompt, clip.VideoPrompts.FirstImage, clip.VideoPrompts.FirstLastImage, clip.VideoPrompts.Reference, storedClipGenerationMode(clip.GenerationMode), clip.ContinueFromPrevious, clip.Revision, clip.CreatedAt.UnixMilli(), clip.UpdatedAt.UnixMilli())
	if err != nil {
		return err
	}
	for _, kind := range []domain.MediaSlotKind{domain.MediaSlotClipFirstImage, domain.MediaSlotClipLastImage, domain.MediaSlotClipVideo, domain.MediaSlotAudioPrimary} {
		if err := insertFixedMediaSlot(ctx, tx, clip.ProjectID, clip.EpisodeID, domain.MediaOwnerClip, clip.ID.String(), kind, clip.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func transactionScene(ctx context.Context, tx *sql.Tx, id domain.SceneID) (domain.Scene, error) {
	scene, err := scanScene(tx.QueryRowContext(ctx, sceneColumns+" WHERE scene_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Scene{}, ErrSceneNotFound
	}
	return scene, err
}
func transactionClip(ctx context.Context, tx *sql.Tx, id domain.ClipID) (domain.Clip, error) {
	clip, err := scanClip(tx.QueryRowContext(ctx, clipColumns+" WHERE clip_id=?", id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Clip{}, ErrClipNotFound
	}
	return clip, err
}

func validateEpisodeOwner(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID, projectID domain.ProjectID) error {
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT project_id FROM episodes WHERE episode_id=?", episodeID.String()).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEpisodeNotFound
		}
		return err
	}
	if owner != projectID.String() {
		return errors.New("Episode belongs to another Project")
	}
	return nil
}

func advanceStoryboard(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID, expected int64) error {
	if expected < 1 || expected == math.MaxInt64 {
		return ErrStoryboardRevisionConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE episodes SET storyboard_revision=storyboard_revision+1 WHERE episode_id=? AND storyboard_revision=?", episodeID.String(), expected)
	if err != nil {
		return err
	}
	return requireChangedEpisode(ctx, tx, result, episodeID, ErrStoryboardRevisionConflict)
}

func activeSceneCount(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM scenes WHERE episode_id=? AND deleted_at_ms IS NULL", episodeID.String()).Scan(&count)
	return count, err
}
func activeClipCount(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM clips WHERE scene_id=? AND deleted_at_ms IS NULL", sceneID.String()).Scan(&count)
	return count, err
}

func maximumActiveSceneOrderKey(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID) (int, error) {
	var maximum int
	err := tx.QueryRowContext(ctx, "SELECT coalesce(max(position),0) FROM scenes WHERE episode_id=? AND deleted_at_ms IS NULL", episodeID.String()).Scan(&maximum)
	return maximum, err
}

func maximumActiveClipOrderKey(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID) (int, error) {
	var maximum int
	err := tx.QueryRowContext(ctx, "SELECT coalesce(max(position),0) FROM clips WHERE scene_id=? AND deleted_at_ms IS NULL", sceneID.String()).Scan(&maximum)
	return maximum, err
}

func availableSceneOrderKey(ctx context.Context, tx *sql.Tx, episodeID domain.EpisodeID, preferred int) (int, error) {
	var occupied int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM scenes WHERE episode_id=? AND position=? AND deleted_at_ms IS NULL", episodeID.String(), preferred).Scan(&occupied); err != nil {
		return 0, err
	}
	if preferred >= 1 && occupied == 0 {
		return preferred, nil
	}
	maximum, err := maximumActiveSceneOrderKey(ctx, tx, episodeID)
	if err != nil {
		return 0, err
	}
	if maximum > math.MaxInt-domain.StoryboardOrderStep {
		return 0, ErrScenePositionConflict
	}
	return maximum + domain.StoryboardOrderStep, nil
}

func availableClipOrderKey(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID, preferred int) (int, error) {
	var occupied int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM clips WHERE scene_id=? AND position=? AND deleted_at_ms IS NULL", sceneID.String(), preferred).Scan(&occupied); err != nil {
		return 0, err
	}
	if preferred >= 1 && occupied == 0 {
		return preferred, nil
	}
	maximum, err := maximumActiveClipOrderKey(ctx, tx, sceneID)
	if err != nil {
		return 0, err
	}
	if maximum > math.MaxInt-domain.StoryboardOrderStep {
		return 0, ErrClipPositionConflict
	}
	return maximum + domain.StoryboardOrderStep, nil
}

func orderedActiveClipIDs(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID, excluded domain.ClipID) ([]domain.ClipID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT clip_id FROM clips WHERE scene_id=? AND deleted_at_ms IS NULL AND clip_id<>? ORDER BY position,clip_id`, sceneID.String(), excluded.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ClipID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := domain.ParseClipID(raw)
		if err != nil {
			return nil, invalidStoredData("Clip ID", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// reorderClipPositions rewrites only storage order keys. It deliberately does
// not advance Clip revisions or history heads: the semantic mutation belongs
// to the containing Scene, while sibling Clip content is unchanged.
func reorderClipPositions(ctx context.Context, tx *sql.Tx, sceneID domain.SceneID, ordered []domain.ClipID) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM clips WHERE scene_id=? AND deleted_at_ms IS NULL`, sceneID.String()).Scan(&count); err != nil {
		return err
	}
	if count != len(ordered) {
		return ErrClipPositionConflict
	}
	maximum, err := maximumActiveClipOrderKey(ctx, tx, sceneID)
	if err != nil || maximum > math.MaxInt-(len(ordered)+1)*domain.StoryboardOrderStep {
		return ErrClipPositionConflict
	}
	temporaryBase := maximum + domain.StoryboardOrderStep
	for index, id := range ordered {
		result, err := tx.ExecContext(ctx, `UPDATE clips SET position=? WHERE clip_id=? AND scene_id=? AND deleted_at_ms IS NULL`, temporaryBase+index*domain.StoryboardOrderStep, id.String(), sceneID.String())
		if err != nil {
			return err
		}
		if err := requireOne(result, ErrClipPositionConflict); err != nil {
			return err
		}
	}
	for index, id := range ordered {
		if _, err := tx.ExecContext(ctx, `UPDATE clips SET position=? WHERE clip_id=? AND scene_id=? AND deleted_at_ms IS NULL`, (index+1)*domain.StoryboardOrderStep, id.String(), sceneID.String()); err != nil {
			return err
		}
	}
	return nil
}

func validateStoryboardMutation(expected int64, operationTime, objectTime time.Time) error {
	if expected < 1 || expected == math.MaxInt64 {
		return errors.New("invalid Storyboard revision")
	}
	if err := validateMutationTime(operationTime); err != nil {
		return err
	}
	if operationTime != objectTime {
		return errors.New("timestamps must match")
	}
	return nil
}
func validateContentMutation(expected, next int64, operationTime, objectTime time.Time) error {
	if expected < 1 || expected == math.MaxInt64 || next != expected+1 {
		return errors.New("invalid revisions")
	}
	if err := validateMutationTime(operationTime); err != nil {
		return err
	}
	if operationTime != objectTime {
		return errors.New("timestamps must match")
	}
	return nil
}
func requireOne(result sql.Result, conflict error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return conflict
	}
	return nil
}
