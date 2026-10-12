package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrSceneNotFound = errors.New("scene not found")
	ErrClipNotFound  = errors.New("clip not found")
)

const sceneColumns = `
SELECT scene_id, project_id, episode_id, position, title, description,
       source_script_revision, source_start_byte, source_end_byte, source_excerpt,
       revision, created_at_ms, updated_at_ms, deleted_at_ms
FROM scenes`

const clipColumns = `
SELECT clips.clip_id, clips.project_id, clips.episode_id, clips.scene_id, clips.position, clips.title, clips.description,
       clips.dialogue, clips.creative_intent, clips.planned_duration_seconds,
       clips.first_image_prompt, clips.last_image_prompt,
       clips.first_image_video_prompt, clips.first_last_image_video_prompt, clips.reference_video_prompt, clips.generation_mode, clips.continue_from_previous,
       clips.revision, clips.created_at_ms, clips.updated_at_ms, clips.deleted_at_ms, clips.deleted_with_scene_id
FROM clips`

func (s *Store) Scene(ctx context.Context, sceneID domain.SceneID) (domain.Scene, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.Scene{}, fmt.Errorf("read Scene: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.Scene{}, fmt.Errorf("read Scene: %w", err)
	}
	if err := sceneID.Validate(); err != nil {
		return domain.Scene{}, fmt.Errorf("read Scene: %w", err)
	}
	scene, err := scanScene(database.QueryRowContext(ctx, sceneColumns+" WHERE scene_id = ?", sceneID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Scene{}, fmt.Errorf("read Scene %q: %w", sceneID, ErrSceneNotFound)
	}
	if err != nil {
		return domain.Scene{}, fmt.Errorf("read Scene %q: %w", sceneID, err)
	}
	return scene, nil
}

func (s *Store) SceneDetails(ctx context.Context, sceneID domain.SceneID) (domain.StoryboardScene, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.StoryboardScene{}, fmt.Errorf("read Scene details: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.StoryboardScene{}, fmt.Errorf("read Scene details: %w", err)
	}
	if err := sceneID.Validate(); err != nil {
		return domain.StoryboardScene{}, fmt.Errorf("read Scene details: %w", err)
	}
	return readSnapshot(ctx, database, "read Scene details", func(tx *sql.Tx) (domain.StoryboardScene, error) {
		scene, err := scanScene(tx.QueryRowContext(ctx, sceneColumns+" WHERE scene_id = ?", sceneID.String()))
		if errors.Is(err, sql.ErrNoRows) {
			return domain.StoryboardScene{}, ErrSceneNotFound
		}
		if err != nil {
			return domain.StoryboardScene{}, err
		}
		clips, err := querySceneClips(ctx, tx, scene.ID, scene.IsDeleted())
		if err != nil {
			return domain.StoryboardScene{}, err
		}
		return domain.StoryboardScene{Scene: scene, Clips: clips}, nil
	})
}

func (s *Store) Clip(ctx context.Context, clipID domain.ClipID) (domain.ClipDetails, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.ClipDetails{}, fmt.Errorf("read Clip: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("read Clip: %w", err)
	}
	if err := clipID.Validate(); err != nil {
		return domain.ClipDetails{}, fmt.Errorf("read Clip: %w", err)
	}
	return readSnapshot(ctx, database, "read Clip", func(tx *sql.Tx) (domain.ClipDetails, error) {
		clip, err := scanClip(tx.QueryRowContext(ctx, clipColumns+" WHERE clip_id = ?", clipID.String()))
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ClipDetails{}, ErrClipNotFound
		}
		if err != nil {
			return domain.ClipDetails{}, err
		}
		references, err := queryClipAssetReferences(ctx, tx, clipID)
		if err != nil {
			return domain.ClipDetails{}, err
		}
		derived, err := queryClipDerivations(ctx, tx, clipID)
		if err != nil {
			return domain.ClipDetails{}, err
		}
		result := domain.ClipDetails{Clip: clip, AssetReferences: references, DerivedFromClipIDs: derived}
		if err := result.Validate(); err != nil {
			return domain.ClipDetails{}, invalidStoredData("Clip details", err)
		}
		return result, nil
	})
}

func (s *Store) Storyboard(ctx context.Context, episodeID domain.EpisodeID) (domain.Storyboard, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return domain.Storyboard{}, fmt.Errorf("read Storyboard: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return domain.Storyboard{}, fmt.Errorf("read Storyboard: %w", err)
	}
	if err := episodeID.Validate(); err != nil {
		return domain.Storyboard{}, fmt.Errorf("read Storyboard: %w", err)
	}
	return readSnapshot(ctx, database, "read Storyboard", func(tx *sql.Tx) (domain.Storyboard, error) {
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT storyboard_revision FROM episodes WHERE episode_id = ?", episodeID.String()).Scan(&revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Storyboard{}, ErrEpisodeNotFound
			}
			return domain.Storyboard{}, err
		}
		rows, err := tx.QueryContext(ctx, sceneColumns+" WHERE episode_id = ? AND deleted_at_ms IS NULL ORDER BY position, scene_id", episodeID.String())
		if err != nil {
			return domain.Storyboard{}, err
		}
		scenes := make([]domain.Scene, 0)
		for rows.Next() {
			scene, err := scanScene(rows)
			if err != nil {
				rows.Close()
				return domain.Storyboard{}, err
			}
			scenes = append(scenes, scene)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.Storyboard{}, err
		}
		if err := rows.Close(); err != nil {
			return domain.Storyboard{}, err
		}
		result := domain.Storyboard{EpisodeID: episodeID, Revision: revision, Scenes: make([]domain.StoryboardScene, 0, len(scenes))}
		for _, scene := range scenes {
			clips, err := querySceneClips(ctx, tx, scene.ID, false)
			if err != nil {
				return domain.Storyboard{}, err
			}
			result.Scenes = append(result.Scenes, domain.StoryboardScene{Scene: scene, Clips: clips})
			for _, clip := range clips {
				result.PlannedDurationSeconds += clip.Clip.PlannedDurationSeconds
			}
		}
		if err := result.Validate(); err != nil {
			return domain.Storyboard{}, invalidStoredData("Storyboard", err)
		}
		return result, nil
	})
}

// DeletedStoryboardItems returns recoverable Scene groups and independently
// deleted Clips. Clips deleted as part of a Scene remain nested under that
// Scene so clients cannot accidentally restore an incomplete group.
type deletedStoryboardItems struct {
	scenes []domain.StoryboardScene
	clips  []domain.ClipDetails
}

func (s *Store) DeletedStoryboardItems(ctx context.Context) ([]domain.StoryboardScene, []domain.ClipDetails, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, nil, fmt.Errorf("list deleted Storyboard items: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("list deleted Storyboard items: %w", err)
	}
	result, err := readSnapshot(ctx, database, "list deleted Storyboard items", func(tx *sql.Tx) (deletedStoryboardItems, error) {
		sceneRows, err := tx.QueryContext(ctx, sceneColumns+" WHERE deleted_at_ms IS NOT NULL ORDER BY deleted_at_ms DESC, scene_id")
		if err != nil {
			return deletedStoryboardItems{}, err
		}
		scenes := make([]domain.Scene, 0)
		for sceneRows.Next() {
			scene, scanErr := scanScene(sceneRows)
			if scanErr != nil {
				sceneRows.Close()
				return deletedStoryboardItems{}, scanErr
			}
			scenes = append(scenes, scene)
		}
		if err := sceneRows.Err(); err != nil {
			sceneRows.Close()
			return deletedStoryboardItems{}, err
		}
		if err := sceneRows.Close(); err != nil {
			return deletedStoryboardItems{}, err
		}
		groups := make([]domain.StoryboardScene, 0, len(scenes))
		for _, scene := range scenes {
			clips, queryErr := querySceneCascadeDeletedClips(ctx, tx, scene.ID)
			if queryErr != nil {
				return deletedStoryboardItems{}, queryErr
			}
			groups = append(groups, domain.StoryboardScene{Scene: scene, Clips: clips})
		}

		clipRows, err := tx.QueryContext(ctx, clipColumns+" WHERE clips.deleted_at_ms IS NOT NULL AND (clips.deleted_with_scene_id IS NULL OR clips.deleted_with_scene_id = '') ORDER BY clips.deleted_at_ms DESC, clips.clip_id")
		if err != nil {
			return deletedStoryboardItems{}, err
		}
		plain := make([]domain.Clip, 0)
		for clipRows.Next() {
			clip, scanErr := scanClip(clipRows)
			if scanErr != nil {
				clipRows.Close()
				return deletedStoryboardItems{}, scanErr
			}
			plain = append(plain, clip)
		}
		if err := clipRows.Err(); err != nil {
			clipRows.Close()
			return deletedStoryboardItems{}, err
		}
		if err := clipRows.Close(); err != nil {
			return deletedStoryboardItems{}, err
		}
		clips := make([]domain.ClipDetails, 0, len(plain))
		for _, clip := range plain {
			references, queryErr := queryClipAssetReferences(ctx, tx, clip.ID)
			if queryErr != nil {
				return deletedStoryboardItems{}, queryErr
			}
			derived, queryErr := queryClipDerivations(ctx, tx, clip.ID)
			if queryErr != nil {
				return deletedStoryboardItems{}, queryErr
			}
			clips = append(clips, domain.ClipDetails{Clip: clip, AssetReferences: references, DerivedFromClipIDs: derived})
		}
		return deletedStoryboardItems{scenes: groups, clips: clips}, nil
	})
	return result.scenes, result.clips, err
}

func querySceneCascadeDeletedClips(ctx context.Context, database queryer, sceneID domain.SceneID) ([]domain.ClipDetails, error) {
	rows, err := database.QueryContext(ctx, clipColumns+" WHERE clips.scene_id = ? AND clips.deleted_with_scene_id = ? ORDER BY clips.position, clips.clip_id", sceneID.String(), sceneID.String())
	if err != nil {
		return nil, fmt.Errorf("list Scene cascade-deleted Clips: %w", err)
	}
	plain, err := scanClips(rows)
	if err != nil {
		return nil, err
	}
	result := make([]domain.ClipDetails, 0, len(plain))
	for _, clip := range plain {
		references, queryErr := queryClipAssetReferences(ctx, database, clip.ID)
		if queryErr != nil {
			return nil, queryErr
		}
		derived, queryErr := queryClipDerivations(ctx, database, clip.ID)
		if queryErr != nil {
			return nil, queryErr
		}
		result = append(result, domain.ClipDetails{Clip: clip, AssetReferences: references, DerivedFromClipIDs: derived})
	}
	return result, nil
}

func scanClips(rows *sql.Rows) ([]domain.Clip, error) {
	plain := make([]domain.Clip, 0)
	for rows.Next() {
		clip, err := scanClip(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		plain = append(plain, clip)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return plain, nil
}

func (s *Store) AssetUsage(ctx context.Context, assetID domain.AssetID) ([]domain.Clip, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("read Asset usage: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("read Asset usage: %w", err)
	}
	if err := assetID.Validate(); err != nil {
		return nil, fmt.Errorf("read Asset usage: %w", err)
	}
	var exists int
	if err := database.QueryRowContext(ctx, "SELECT 1 FROM assets WHERE asset_id = ?", assetID.String()).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAssetNotFound
		}
		return nil, fmt.Errorf("inspect Asset: %w", err)
	}
	rows, err := database.QueryContext(ctx, clipColumns+`
 JOIN clip_asset_references r ON r.clip_id = clips.clip_id
 JOIN scenes s ON s.scene_id = clips.scene_id
 WHERE r.asset_id = ? AND clips.deleted_at_ms IS NULL AND s.deleted_at_ms IS NULL
 ORDER BY clips.episode_id, s.position, clips.position, clips.clip_id`, assetID.String())
	if err != nil {
		return nil, fmt.Errorf("query Asset usage: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Clip, 0)
	for rows.Next() {
		clip, err := scanClip(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, clip)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func querySceneClips(ctx context.Context, database queryer, sceneID domain.SceneID, includeDeleted bool) ([]domain.ClipDetails, error) {
	predicate := " AND deleted_at_ms IS NULL"
	if includeDeleted {
		predicate = ""
	}
	rows, err := database.QueryContext(ctx, clipColumns+" WHERE scene_id = ?"+predicate+" ORDER BY position, clip_id", sceneID.String())
	if err != nil {
		return nil, fmt.Errorf("list Scene Clips: %w", err)
	}
	plainClips := make([]domain.Clip, 0)
	for rows.Next() {
		clip, err := scanClip(rows)
		if err != nil {
			return nil, err
		}
		plainClips = append(plainClips, clip)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	clips := make([]domain.ClipDetails, 0, len(plainClips))
	for _, clip := range plainClips {
		references, err := queryClipAssetReferences(ctx, database, clip.ID)
		if err != nil {
			return nil, err
		}
		derived, err := queryClipDerivations(ctx, database, clip.ID)
		if err != nil {
			return nil, err
		}
		clips = append(clips, domain.ClipDetails{Clip: clip, AssetReferences: references, DerivedFromClipIDs: derived})
	}
	return clips, nil
}

func queryClipDerivations(ctx context.Context, database queryer, clipID domain.ClipID) ([]domain.ClipID, error) {
	rows, err := database.QueryContext(ctx, "SELECT source_clip_id FROM clip_derivations WHERE clip_id=? ORDER BY position", clipID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ClipID, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := domain.ParseClipID(raw)
		if err != nil {
			return nil, invalidStoredData("source Clip ID", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSnapshot[T any](ctx context.Context, database *sql.DB, operation string, action func(*sql.Tx) (T, error)) (_ T, returnErr error) {
	var zero T
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, fmt.Errorf("%s: begin: %w", operation, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := transaction.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, fmt.Errorf("%s: roll back: %w", operation, err))
		}
	}()
	result, err := action(transaction)
	if err != nil {
		return zero, fmt.Errorf("%s: %w", operation, err)
	}
	if err := transaction.Commit(); err != nil {
		return zero, fmt.Errorf("%s: commit: %w", operation, err)
	}
	committed = true
	return result, nil
}

func queryClipAssetReferences(ctx context.Context, database queryer, clipID domain.ClipID) ([]domain.ClipAssetReference, error) {
	rows, err := database.QueryContext(ctx, `
SELECT r.asset_id, r.position, a.asset_identity_id, i.kind, i.name, a.deleted_at_ms
FROM clip_asset_references r
JOIN assets a ON a.asset_id = r.asset_id
JOIN asset_identities i ON i.asset_identity_id = a.asset_identity_id
WHERE r.clip_id = ?
ORDER BY r.position, r.asset_id`, clipID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ClipAssetReference, 0)
	for rows.Next() {
		var value string
		var position int
		var identityValue, kind, name string
		var deletedAt sql.NullInt64
		if err := rows.Scan(&value, &position, &identityValue, &kind, &name, &deletedAt); err != nil {
			return nil, err
		}
		assetID, err := domain.ParseAssetID(value)
		if err != nil {
			return nil, invalidStoredData("Clip Asset reference", err)
		}
		identityID, err := domain.ParseAssetIdentityID(identityValue)
		if err != nil {
			return nil, invalidStoredData("Clip Asset reference Identity", err)
		}
		result = append(result, domain.ClipAssetReference{
			ClipID: clipID, AssetID: assetID, AssetIdentityID: identityID,
			Kind: domain.AssetKind(kind), Name: name, AssetDeleted: deletedAt.Valid, Position: position,
		})
	}
	return result, rows.Err()
}

func scanScene(row rowScanner) (domain.Scene, error) {
	var sceneID, projectID, episodeID, title, description string
	var position int
	var sourceRevision, sourceStart, sourceEnd sql.NullInt64
	var sourceExcerpt sql.NullString
	var revision, createdAtMS, updatedAtMS int64
	var deletedAtMS sql.NullInt64
	if err := row.Scan(&sceneID, &projectID, &episodeID, &position, &title, &description, &sourceRevision, &sourceStart, &sourceEnd, &sourceExcerpt, &revision, &createdAtMS, &updatedAtMS, &deletedAtMS); err != nil {
		return domain.Scene{}, err
	}
	id, err := domain.ParseSceneID(sceneID)
	if err != nil {
		return domain.Scene{}, invalidStoredData("Scene ID", err)
	}
	owner, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.Scene{}, invalidStoredData("Scene Project ID", err)
	}
	episode, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.Scene{}, invalidStoredData("Scene Episode ID", err)
	}
	var source *domain.SceneScriptSource
	if sourceRevision.Valid || sourceStart.Valid || sourceEnd.Valid || sourceExcerpt.Valid {
		if !sourceRevision.Valid || !sourceStart.Valid || !sourceEnd.Valid || !sourceExcerpt.Valid {
			return domain.Scene{}, invalidStoredData("Scene source", errors.New("partial source fields"))
		}
		source = &domain.SceneScriptSource{ScriptRevision: sourceRevision.Int64, StartByte: int(sourceStart.Int64), EndByte: int(sourceEnd.Int64), Excerpt: sourceExcerpt.String}
	}
	var deletedAt *time.Time
	if deletedAtMS.Valid {
		value := time.UnixMilli(deletedAtMS.Int64).UTC()
		deletedAt = &value
	}
	result := domain.Scene{ID: id, ProjectID: owner, EpisodeID: episode, Position: position, Title: title, Description: description, Source: source, Revision: revision, CreatedAt: time.UnixMilli(createdAtMS).UTC(), UpdatedAt: time.UnixMilli(updatedAtMS).UTC(), DeletedAt: deletedAt}
	if err := result.Validate(); err != nil {
		return domain.Scene{}, invalidStoredData("Scene", err)
	}
	return result, nil
}

func scanClip(row rowScanner) (domain.Clip, error) {
	var clipID, projectID, episodeID, sceneID, title, description, dialogue, intent string
	var firstPrompt, lastPrompt, firstVideoPrompt, firstLastVideoPrompt, referenceVideoPrompt, mode string
	var position int
	var duration, revision, createdAtMS, updatedAtMS int64
	var continueFromPrevious bool
	var deletedAtMS sql.NullInt64
	var deletedWith sql.NullString
	if err := row.Scan(&clipID, &projectID, &episodeID, &sceneID, &position, &title, &description, &dialogue, &intent, &duration, &firstPrompt, &lastPrompt, &firstVideoPrompt, &firstLastVideoPrompt, &referenceVideoPrompt, &mode, &continueFromPrevious, &revision, &createdAtMS, &updatedAtMS, &deletedAtMS, &deletedWith); err != nil {
		return domain.Clip{}, err
	}
	id, err := domain.ParseClipID(clipID)
	if err != nil {
		return domain.Clip{}, invalidStoredData("Clip ID", err)
	}
	owner, err := domain.ParseProjectID(projectID)
	if err != nil {
		return domain.Clip{}, invalidStoredData("Clip Project ID", err)
	}
	episode, err := domain.ParseEpisodeID(episodeID)
	if err != nil {
		return domain.Clip{}, invalidStoredData("Clip Episode ID", err)
	}
	parent, err := domain.ParseSceneID(sceneID)
	if err != nil {
		return domain.Clip{}, invalidStoredData("Clip Scene ID", err)
	}
	var deletedAt *time.Time
	if deletedAtMS.Valid {
		value := time.UnixMilli(deletedAtMS.Int64).UTC()
		deletedAt = &value
	}
	var deletedWithID domain.SceneID
	if deletedWith.Valid {
		deletedWithID, err = domain.ParseSceneID(deletedWith.String)
		if err != nil {
			return domain.Clip{}, invalidStoredData("Clip deletion parent", err)
		}
	}
	result := domain.Clip{ID: id, ProjectID: owner, EpisodeID: episode, SceneID: parent, Position: position, Title: title, Description: description, Dialogue: dialogue, CreativeIntent: intent, PlannedDurationSeconds: duration, FirstImagePrompt: firstPrompt, LastImagePrompt: lastPrompt,
		VideoPrompts: domain.ClipVideoPrompts{FirstImage: firstVideoPrompt, FirstLastImage: firstLastVideoPrompt, Reference: referenceVideoPrompt}, GenerationMode: parsedClipGenerationMode(mode), ContinueFromPrevious: continueFromPrevious, Revision: revision, CreatedAt: time.UnixMilli(createdAtMS).UTC(), UpdatedAt: time.UnixMilli(updatedAtMS).UTC(), DeletedAt: deletedAt, DeletedWithSceneID: deletedWithID}
	if err := result.Validate(); err != nil {
		return domain.Clip{}, invalidStoredData("Clip", err)
	}
	return result, nil
}

func storedClipGenerationMode(mode domain.ClipGenerationMode) string {
	if mode == domain.ClipGenerationReference {
		return "asset_reference"
	}
	return string(mode)
}

func parsedClipGenerationMode(value string) domain.ClipGenerationMode {
	if value == "asset_reference" {
		return domain.ClipGenerationReference
	}
	return domain.ClipGenerationMode(value)
}
