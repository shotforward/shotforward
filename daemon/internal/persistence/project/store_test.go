package projectstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestOpenCreatesAndMigratesProjectStore(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "project.db")
	store, err := Open(context.Background(), path, sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	defer store.Close()

	version, err := sharedsqlite.SchemaVersion(context.Background(), store.database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}

	for _, table := range []string{"projects", "episodes", "asset_identities", "assets", "scenes", "clips", "clip_asset_references", "clip_reference_media", "agent_conversation_media", "clip_derivations", "generation_batches", "generation_jobs", "generation_job_artifact_references", "change_sets", "change_set_operations", "change_set_preconditions", "change_set_asset_creations", "change_set_asset_restorations", "change_set_storyboard_operations", "change_set_clip_merge_operations", "change_set_script_operations", "change_set_revert_operations", "change_set_conflicts", "change_set_command_receipts", "change_set_scopes", "resource_history_heads", "change_set_resource_heads", "export_jobs"} {
		var count int
		if err := store.database.QueryRowContext(
			context.Background(),
			"SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?",
			table,
		).Scan(&count); err != nil {
			t.Fatalf("read %s schema: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %q count = %d, want 1", table, count)
		}
	}
}

func TestOpenExistingRejectsNewerProjectSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "project.db")
	store := newTestStore(t, path)
	if _, err := store.database.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatalf("set newer schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err := Open(context.Background(), path, sharedsqlite.OpenExisting)
	if !errors.Is(err, sharedsqlite.ErrSchemaTooNew) {
		t.Fatalf("Open(OpenExisting) error = %v, want ErrSchemaTooNew", err)
	}
}

func TestOpenExistingMigratesVersionOneWithoutChangingProjectData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{
		Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatalf("Open(version 1) error = %v", err)
	}
	defer upgraded.Close()
	version, err := sharedsqlite.SchemaVersion(ctx, upgraded.database)
	if err != nil || version != len(migrations) {
		t.Fatalf("upgraded schema version = %d, %v", version, err)
	}
	gotProject, err := upgraded.Project(ctx)
	if err != nil || gotProject != project {
		t.Fatalf("Project after migration = %#v, %v", gotProject, err)
	}
	gotEpisode, err := upgraded.Episode(ctx, episode.ID)
	if err != nil || gotEpisode != episode {
		t.Fatalf("Episode after migration = %#v, %v", gotEpisode, err)
	}
	assets, err := upgraded.EpisodeAssets(ctx, episode.ID, true)
	if err != nil || len(assets) != 0 {
		t.Fatalf("EpisodeAssets after migration = %#v, %v", assets, err)
	}
}

func TestOpenExistingMigratesVersionSixToChangeSetSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{
		Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:6]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatalf("Open(version 6) error = %v", err)
	}
	defer upgraded.Close()
	version, err := sharedsqlite.SchemaVersion(ctx, upgraded.database)
	if err != nil || version != len(migrations) {
		t.Fatalf("upgraded schema version = %d, %v", version, err)
	}
	gotProject, err := upgraded.Project(ctx)
	if err != nil || gotProject != project {
		t.Fatalf("Project after v7 migration = %#v, %v", gotProject, err)
	}
	gotEpisode, err := upgraded.Episode(ctx, episode.ID)
	if err != nil || gotEpisode != episode {
		t.Fatalf("Episode after v7 migration = %#v, %v", gotEpisode, err)
	}
	for _, table := range []string{"change_sets", "change_set_operations", "change_set_preconditions", "change_set_asset_creations", "change_set_conflicts", "change_set_command_receipts"} {
		var count int
		if err := upgraded.database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v7 table %q count = %d, %v", table, count, err)
		}
	}
}

func TestOpenExistingMigratesClipPromptIntoItsCurrentMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:14]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "V", 1, "旧提示词", at)
	if _, err := database.ExecContext(ctx, `INSERT INTO scenes (scene_id,project_id,episode_id,position,title,description,source_script_revision,source_start_byte,source_end_byte,source_excerpt,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,'',NULL,NULL,NULL,NULL,1,?,?,NULL)`, scene.ID.String(), scene.ProjectID.String(), scene.EpisodeID.String(), scene.Position, scene.Title, at.UnixMilli(), at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "V", 1, 8, at.Add(time.Second))
	if _, err := database.ExecContext(ctx, `INSERT INTO clips (clip_id,project_id,episode_id,scene_id,position,title,description,dialogue,creative_intent,planned_duration_seconds,first_image_prompt,last_image_prompt,video_prompt,generation_mode,revision,created_at_ms,updated_at_ms,deleted_at_ms,deleted_with_scene_id) VALUES (?,?,?,?,?,?,'','','',?,'','','旧参考提示词','asset_reference',1,?,?,NULL,NULL)`, clip.ID.String(), clip.ProjectID.String(), clip.EpisodeID.String(), clip.SceneID.String(), clip.Position, clip.Title, clip.PlannedDurationSeconds, clip.CreatedAt.UnixMilli(), clip.UpdatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	changeSetID := "cst_" + strings.Repeat("V", 26)
	operationID := "cop_" + strings.Repeat("V", 26)
	if _, err := database.ExecContext(ctx, `INSERT INTO change_sets (change_set_id,project_id,episode_id,agent_session_id,agent_turn_id,codex_thread_id,codex_turn_id,title,summary,status,operation_count,revision,failure_code,failure_summary,created_at_ms,updated_at_ms,finalized_at_ms,resolved_at_ms) VALUES (?,?,?,?,?,?,?,'旧草稿','string migration','draft',0,1,'','',?,?,NULL,NULL)`, changeSetID, project.ID.String(), episode.ID.String(), "session", "turn", "thread", "codex-turn", at.UnixMilli(), at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	legacyPayload := `{"Clip":{"VideoPrompt":"待应用旧提示词","GenerationMode":"reference"}}`
	if _, err := database.ExecContext(ctx, `INSERT INTO change_set_storyboard_operations (operation_id,change_set_id,position,precondition_id,episode_id,expected_storyboard_revision,expected_script_revision,operation_type,temporary_ref,parent_ref,scene_id,clip_id,asset_id,payload_json,created_at_ms) VALUES (?,?,1,?,?,1,1,'create_clip','clip_ref','scene_ref',?,?,NULL,?,?)`, operationID, changeSetID, "cpc_"+strings.Repeat("V", 26), episode.ID.String(), scene.ID.String(), clip.ID.String(), legacyPayload, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.Clip(ctx, clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Clip.VideoPrompts.Reference != "旧参考提示词" || got.Clip.VideoPrompts.FirstImage != "" || got.Clip.VideoPrompts.FirstLastImage != "" {
		t.Fatalf("migrated video Prompts = %#v", got.Clip.VideoPrompts)
	}
	var migratedPayload string
	if err := upgraded.database.QueryRowContext(ctx, `SELECT payload_json FROM change_set_storyboard_operations WHERE operation_id=?`, operationID).Scan(&migratedPayload); err != nil {
		t.Fatal(err)
	}
	var migrated struct {
		Clip struct {
			VideoPrompt  *string
			VideoPrompts domain.ClipVideoPrompts
		}
	}
	if err := json.Unmarshal([]byte(migratedPayload), &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Clip.VideoPrompt != nil || migrated.Clip.VideoPrompts.Reference != "待应用旧提示词" {
		t.Fatalf("migrated ChangeSet payload = %s", migratedPayload)
	}
}

func TestOpenExistingMigratesVersionTwoWithoutChangingAssets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	identity, asset := assetModels(t, project.ID, episode.ID, "S", "S", domain.AssetKindStyle, "雨夜风格", "电影感", episode.UpdatedAt.Add(time.Second))
	if _, err := legacy.database.ExecContext(ctx, `INSERT INTO asset_identities (asset_identity_id,project_id,kind,name,revision,created_at_ms,updated_at_ms) VALUES (?,?,?,?,?,?,?)`, identity.ID.String(), identity.ProjectID.String(), string(identity.Kind), identity.Name, identity.Revision, identity.CreatedAt.UnixMilli(), identity.UpdatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.database.ExecContext(ctx, `INSERT INTO assets (asset_id,project_id,episode_id,asset_identity_id,derived_from_asset_id,prompt,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,NULL,?,?,?,?,NULL)`, asset.ID.String(), asset.ProjectID.String(), asset.EpisodeID.String(), asset.IdentityID.String(), asset.Prompt, asset.Revision, asset.CreatedAt.UnixMilli(), asset.UpdatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.Asset(ctx, asset.ID)
	if err != nil || got.Asset != asset || got.Identity != identity {
		t.Fatalf("Asset after v2 migration = %#v, %v", got, err)
	}
	storyboard, err := upgraded.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 1 || len(storyboard.Scenes) != 0 {
		t.Fatalf("Storyboard after v2 migration = %#v, %v", storyboard, err)
	}
	slotID, err := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := upgraded.MediaSlot(ctx, slotID, true)
	if err != nil || slot.Slot.OwnerID != asset.ID.String() || len(slot.Versions) != 0 {
		t.Fatalf("MediaSlot after v2 migration = %#v, %v", slot, err)
	}
}

func TestOpenExistingMigratesProviderJobsToGeneratorRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:5]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "L", 1, "旧生成任务", at)
	if _, err := legacy.database.ExecContext(ctx, `INSERT INTO scenes (scene_id,project_id,episode_id,position,title,description,source_script_revision,source_start_byte,source_end_byte,source_excerpt,revision,created_at_ms,updated_at_ms,deleted_at_ms) VALUES (?,?,?,?,?,'',NULL,NULL,NULL,NULL,1,?,?,NULL)`, scene.ID.String(), scene.ProjectID.String(), scene.EpisodeID.String(), scene.Position, scene.Title, scene.CreatedAt.UnixMilli(), scene.UpdatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "L", 1, 5, at.Add(time.Second))
	if _, err := legacy.database.ExecContext(ctx, `INSERT INTO clips (clip_id,project_id,episode_id,scene_id,position,title,description,dialogue,creative_intent,planned_duration_seconds,first_image_prompt,last_image_prompt,video_prompt,generation_mode,revision,created_at_ms,updated_at_ms,deleted_at_ms,deleted_with_scene_id) VALUES (?,?,?,?,?,?,'','','',?,'','','','first_image',1,?,?,NULL,NULL)`, clip.ID.String(), clip.ProjectID.String(), clip.EpisodeID.String(), clip.SceneID.String(), clip.Position, clip.Title, clip.PlannedDurationSeconds, clip.CreatedAt.UnixMilli(), clip.UpdatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []domain.MediaSlotKind{domain.MediaSlotClipFirstImage, domain.MediaSlotClipLastImage, domain.MediaSlotClipVideo} {
		slotID, slotErr := domain.FixedMediaSlotID(clip.ID.String(), kind)
		if slotErr != nil {
			t.Fatal(slotErr)
		}
		if _, err := legacy.database.ExecContext(ctx, `INSERT INTO media_slots (media_slot_id,project_id,episode_id,owner_kind,owner_id,slot_kind,current_media_version_id,revision,created_at_ms,updated_at_ms) VALUES (?,?,?,?,?,?,NULL,1,?,?)`, slotID.String(), project.ID.String(), episode.ID.String(), string(domain.MediaOwnerClip), clip.ID.String(), string(kind), clip.CreatedAt.UnixMilli(), clip.UpdatedAt.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.database.ExecContext(ctx, `UPDATE episodes SET storyboard_revision=3 WHERE episode_id=?`, episode.ID.String()); err != nil {
		t.Fatal(err)
	}
	imageID := "art_" + strings.Repeat("I", 26)
	audioID := "art_" + strings.Repeat("U", 26)
	for _, values := range [][]any{
		{imageID, project.ID.String(), "image", "media/images/legacy.png", strings.Repeat("1", 64), 10, "image/png", "legacy.png", 720, 1280, 0, 0, 0, 0, 0, at.UnixMilli()},
		{audioID, project.ID.String(), "audio", "media/audio/legacy.mp3", strings.Repeat("2", 64), 20, "audio/mpeg", "legacy.mp3", 0, 0, 15_000, 0, 0, 48_000, 2, at.UnixMilli()},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO artifacts (artifact_id,project_id,media_kind,relative_path,sha256,byte_size,mime_type,original_filename,width,height,duration_ms,fps_numerator,fps_denominator,audio_sample_rate,audio_channels,created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...); err != nil {
			t.Fatal(err)
		}
	}
	batchID := "gbt_" + strings.Repeat("L", 26)
	jobID := "gjb_" + strings.Repeat("L", 26)
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	snapshot, err := json.Marshal(map[string]any{
		"schema_version": 1, "target_media_kind": "video", "prompt": "旧任务", "generation_mode": "asset_reference",
		"provider_id": "legacy-provider", "model_id": "legacy-model", "capability_version": "legacy-1",
		"output_width": project.Output.Width(), "output_height": project.Output.Height(), "requested_duration_seconds": 5,
		"references":      []map[string]any{{"role": "asset_reference", "artifact_id": imageID}, {"role": "audio_reference", "artifact_id": audioID}},
		"parameters_json": "{}", "quote_id": "old-quote", "estimated_credits": 12, "target_slot_revision": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO generation_batches (generation_batch_id,project_id,episode_id,operation_key,request_hash,origin,session_id,turn_id,created_at_ms) VALUES (?,?,?,?,?,'ui',NULL,NULL,?)`, batchID, project.ID.String(), episode.ID.String(), "legacy-operation", strings.Repeat("a", 64), at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO generation_jobs (generation_job_id,generation_batch_id,project_id,episode_id,target_media_slot_id,retry_of_job_id,state,snapshot_json,provider_id,model_id,target_slot_revision,stop_supported,provider_operation_id,progress_stage,progress_message,result_artifact_id,result_media_version_id,failure_code,failure_message,actual_credits,revision,created_at_ms,updated_at_ms,started_at_ms,finished_at_ms) VALUES (?,?,?,?,?,NULL,'queued',?,?,?,1,1,NULL,'queued','',NULL,NULL,'','',0,1,?,?,NULL,NULL)`, jobID, batchID, project.ID.String(), episode.ID.String(), slotID.String(), string(snapshot), "legacy-provider", "legacy-model", at.UnixMilli(), at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for _, reference := range [][2]string{{"asset_reference", imageID}, {"audio_reference", audioID}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO generation_job_artifact_references (generation_job_id,role,position,artifact_id) VALUES (?,?,1,?)`, jobID, reference[0], reference[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	referenceAudioSlotID, err := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotAudioPrimary)
	if err != nil {
		t.Fatal(err)
	}
	referenceAudioSlot, err := upgraded.MediaSlot(ctx, referenceAudioSlotID, true)
	if err != nil || referenceAudioSlot.Slot.OwnerKind != domain.MediaOwnerClip || referenceAudioSlot.Slot.Kind != domain.MediaSlotAudioPrimary {
		t.Fatalf("migrated Clip reference audio slot = %#v, %v", referenceAudioSlot, err)
	}
	parsedJobID, _ := domain.ParseGenerationJobID(jobID)
	job, err := upgraded.GenerationJob(ctx, parsedJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Snapshot.SchemaVersion != domain.GenerationSnapshotVersion || job.Snapshot.GeneratorID != "video.legacy" || job.Snapshot.GenerationMode != domain.ClipGenerationReference || len(job.Snapshot.References) != 2 || job.Snapshot.References[1].TrimEndMilliseconds != 15_000 {
		t.Fatalf("migrated GenerationJob = %#v", job)
	}
	var providerColumns int
	if err := upgraded.database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('generation_jobs') WHERE name IN ('provider_id','model_id','actual_credits')`).Scan(&providerColumns); err != nil || providerColumns != 0 {
		t.Fatalf("legacy provider columns = %d, %v", providerColumns, err)
	}
}
