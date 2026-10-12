package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestGenerationJobCompletionPublishesOnlyWhenSlotIsUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "project.db")
	store, project, episode, clip, firstArtifact := initializedGenerationStore(t, databasePath)
	at := firstArtifact.CreatedAt.Add(time.Second)
	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, firstArtifact.ID, "A", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimGenerationJob(ctx, job.ID, 1, at.Add(time.Second))
	if err != nil || claimed.State != domain.GenerationJobRunning || claimed.Revision != 2 {
		t.Fatalf("ClaimGenerationJob() = %#v, %v", claimed, err)
	}
	result := testVideoArtifact(t, project.ID, "V", strings.Repeat("1", 64), at.Add(2*time.Second))
	result.RelativePath = "media/videos/generated-v.mp4"
	result.RemoteReferenceURL = "https://cdn.example/generated-v.mp4?signature=private"
	remoteExpiresAt := result.CreatedAt.Add(24 * time.Hour)
	result.RemoteReferenceExpiresAt = &remoteExpiresAt
	resultVersionID, _ := domain.ParseMediaVersionID("mvr_" + strings.Repeat("V", 26))
	if err := store.CompleteGenerationJob(ctx, job.ID, 2, result, resultVersionID, result.CreatedAt); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GenerationJob(ctx, job.ID)
	if err != nil || completed.State != domain.GenerationJobSucceeded || completed.ResultArtifactID != result.ID || completed.ResultVersionID != resultVersionID {
		t.Fatalf("completed GenerationJob = %#v, %v", completed, err)
	}
	slot, err := store.MediaSlot(ctx, videoSlotID, true)
	if err != nil || slot.Current == nil || slot.Current.ID != resultVersionID || slot.Slot.Revision != 2 {
		t.Fatalf("published video Slot = %#v, %v", slot, err)
	}
	storedResult, err := store.Artifact(ctx, result.ID)
	if err != nil || storedResult.RemoteReferenceURL != result.RemoteReferenceURL || storedResult.RemoteReferenceExpiresAt == nil || !storedResult.RemoteReferenceExpiresAt.Equal(remoteExpiresAt) {
		t.Fatalf("stored remote reference = %#v, %v", storedResult, err)
	}

	secondAt := result.CreatedAt.Add(time.Second)
	secondBatch, secondJob := generationBatchAndJob(t, project, episode, videoSlotID, firstArtifact.ID, "B", 2, secondAt)
	if err := store.CreateGenerationBatch(ctx, secondBatch, []domain.GenerationJob{secondJob}, secondAt); err != nil {
		t.Fatal(err)
	}
	secondJob, err = store.ClaimGenerationJob(ctx, secondJob.ID, 1, secondAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	manual := testVideoArtifact(t, project.ID, "W", strings.Repeat("2", 64), secondAt.Add(2*time.Second))
	manual.RelativePath = "media/videos/manual-w.mp4"
	manualVersion := testMediaVersion(t, videoSlotID, manual.ID, "W", 2, manual.DurationMilliseconds, manual.CreatedAt)
	if err := store.AddMediaVersion(ctx, manual, manualVersion, 2, true, manual.CreatedAt); err != nil {
		t.Fatal(err)
	}
	late := testVideoArtifact(t, project.ID, "X", strings.Repeat("3", 64), secondAt.Add(3*time.Second))
	late.RelativePath = "media/videos/late-x.mp4"
	lateVersionID, _ := domain.ParseMediaVersionID("mvr_" + strings.Repeat("X", 26))
	if err := store.CompleteGenerationJob(ctx, secondJob.ID, secondJob.Revision, late, lateVersionID, late.CreatedAt); err != nil {
		t.Fatal(err)
	}
	slot, err = store.MediaSlot(ctx, videoSlotID, true)
	if err != nil || slot.Current == nil || slot.Current.ID != manualVersion.ID || len(slot.Versions) != 3 || slot.Slot.Revision != 4 {
		t.Fatalf("Slot after late result = %#v, %v", slot, err)
	}
	if slot.Versions[0].ID != lateVersionID {
		t.Fatalf("late result was not retained in history: %#v", slot.Versions)
	}
	recent, err := store.RecentGenerationJobs(ctx, 1)
	if err != nil || len(recent) != 1 || recent[0].ID != secondJob.ID {
		t.Fatalf("RecentGenerationJobs() = %#v, %v", recent, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.GenerationBatch(ctx, secondBatch.ID)
	if err != nil || len(restored.Jobs) != 1 || restored.Jobs[0].State != domain.GenerationJobSucceeded {
		t.Fatalf("reopened GenerationBatch = %#v, %v", restored, err)
	}
}

func TestGenerationBatchAcceptsProjectLibraryImageReferenceAcrossEpisodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, firstArtifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()

	second := testSecondEpisode(t, project, episode)
	second.CreatedAt = firstArtifact.CreatedAt.Add(time.Second)
	second.UpdatedAt = second.CreatedAt
	second.Script.UpdatedAt = second.CreatedAt
	if err := store.CreateEpisode(ctx, second, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	assetAt := second.UpdatedAt.Add(time.Second)
	identity, asset := assetModels(t, project.ID, second.ID, "R", "R", domain.AssetKindCharacter, "跨集参考图", "", assetAt)
	if err := store.CreateAsset(ctx, &identity, asset, assetAt); err != nil {
		t.Fatal(err)
	}
	referenceID, _ := domain.ParseArtifactID("art_" + strings.Repeat("R", 26))
	reference := domain.Artifact{
		ID: referenceID, ProjectID: project.ID, Kind: domain.MediaKindImage,
		RelativePath: "media/images/cross-episode-reference.png", SHA256: strings.Repeat("b", 64), ByteSize: 10,
		MIMEType: "image/png", OriginalFilename: "reference.png", Width: 1254, Height: 1254,
		CreatedAt: assetAt.Add(time.Second),
	}
	referenceSlotID, _ := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	referenceVersion := testMediaVersion(t, referenceSlotID, reference.ID, "R", 1, 0, reference.CreatedAt)
	if err := store.AddMediaVersion(ctx, reference, referenceVersion, 1, true, reference.CreatedAt); err != nil {
		t.Fatal(err)
	}

	targetSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipFirstImage)
	target, err := store.MediaSlot(ctx, targetSlotID, false)
	if err != nil {
		t.Fatal(err)
	}
	at := reference.CreatedAt.Add(time.Second)
	batchID, _ := domain.ParseGenerationBatchID("gbt_" + strings.Repeat("R", 26))
	jobID, _ := domain.ParseGenerationJobID("gjb_" + strings.Repeat("R", 26))
	batch := domain.GenerationBatch{
		ID: batchID, ProjectID: project.ID, EpisodeID: episode.ID,
		OperationKey: "cross-episode-image-reference", RequestHash: strings.Repeat("c", 64), CreatedAt: at,
	}
	job := domain.GenerationJob{
		ID: jobID, BatchID: batch.ID, ProjectID: project.ID, EpisodeID: episode.ID, TargetSlotID: targetSlotID,
		State: domain.GenerationJobQueued,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindImage,
			Prompt: "保留角色特征", GeneratorID: domain.GeneratorImageCodex, CapabilityVersion: "1",
			OutputWidth: project.Output.Width(), OutputHeight: project.Output.Height(),
			References:     []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceImage, ArtifactID: reference.ID}},
			ParametersJSON: "{}", TargetSlotRevision: target.Slot.Revision,
		},
		ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatalf("CreateGenerationBatch() with a project-library image reference = %v", err)
	}
}

func TestGenerationBatchAcceptsUploadedAudioBoundToClip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, firstArtifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()

	audioID, _ := domain.ParseArtifactID("art_" + strings.Repeat("U", 26))
	audio := domain.Artifact{
		ID: audioID, ProjectID: project.ID, Kind: domain.MediaKindAudio,
		RelativePath: "media/audio/uploaded-reference.mp3", SHA256: strings.Repeat("9", 64), ByteSize: 4096,
		MIMEType: "audio/mpeg", OriginalFilename: "uploaded-reference.mp3", DurationMilliseconds: 14_838,
		AudioSampleRate: 44_100, AudioChannels: 2, CreatedAt: firstArtifact.CreatedAt.Add(time.Second),
	}
	libraryItem := domain.EpisodeReferenceMedia{
		EpisodeID: episode.ID, Artifact: audio, Name: "角色配音", Description: "当前镜头的对白参考",
		Revision: 1, CreatedAt: audio.CreatedAt, UpdatedAt: audio.CreatedAt,
	}
	if err := store.PutEpisodeReferenceMedia(ctx, libraryItem, audio.CreatedAt); err != nil {
		t.Fatal(err)
	}
	boundAt := audio.CreatedAt.Add(time.Second)
	if err := store.AddClipReferenceArtifact(ctx, clip.ID, audio, boundAt); err != nil {
		t.Fatal(err)
	}

	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	target, err := store.MediaSlot(ctx, videoSlotID, false)
	if err != nil {
		t.Fatal(err)
	}
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, firstArtifact.ID, "U", target.Slot.Revision, boundAt.Add(time.Second))
	job.Snapshot.GenerationMode = domain.ClipGenerationReference
	job.Snapshot.References = []domain.GenerationArtifactReference{
		{Role: domain.GenerationReferenceImage, ArtifactID: firstArtifact.ID},
		{Role: domain.GenerationReferenceAudio, ArtifactID: audio.ID, TrimEndMilliseconds: audio.DurationMilliseconds},
	}
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, batch.CreatedAt); err != nil {
		t.Fatalf("CreateGenerationBatch() with Clip-bound uploaded audio = %v", err)
	}
	created, err := store.GenerationBatch(ctx, batch.ID)
	if err != nil || len(created.Jobs) != 1 || len(created.Jobs[0].Snapshot.References) != 2 {
		t.Fatalf("GenerationBatch() = %#v, %v", created, err)
	}
}

func TestGenerationBatchRequiresTargetSpecificOutputSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		width   int
		height  int
		wantErr error
	}{
		{name: "asset uses project aspect at 1.5K", width: 864, height: 1536},
		{name: "asset rejects project video resolution", width: 720, height: 1280, wantErr: domain.ErrInvalidGenerationSnapshot},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, project, episode, _, firstArtifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
			defer store.Close()

			at := firstArtifact.CreatedAt.Add(time.Second)
			identity, asset := assetModels(t, project.ID, episode.ID, "Y", "Y", domain.AssetKindCharacter, "角色", "角色提示词", at)
			if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
				t.Fatal(err)
			}
			slotID, _ := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
			target, err := store.MediaSlot(ctx, slotID, false)
			if err != nil {
				t.Fatal(err)
			}
			batchID, _ := domain.ParseGenerationBatchID("gbt_" + strings.Repeat("Y", 26))
			jobID, _ := domain.ParseGenerationJobID("gjb_" + strings.Repeat("Y", 26))
			batch := domain.GenerationBatch{
				ID: batchID, ProjectID: project.ID, EpisodeID: episode.ID,
				OperationKey: "asset-image-generation", RequestHash: strings.Repeat("d", 64), CreatedAt: at,
			}
			job := domain.GenerationJob{
				ID: jobID, BatchID: batch.ID, ProjectID: project.ID, EpisodeID: episode.ID, TargetSlotID: slotID,
				State: domain.GenerationJobQueued,
				Snapshot: domain.GenerationInputSnapshot{
					SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindImage,
					Prompt: "角色提示词", GeneratorID: domain.GeneratorImageCodex, CapabilityVersion: "1",
					OutputWidth: test.width, OutputHeight: test.height,
					ParametersJSON: "{}", TargetSlotRevision: target.Slot.Revision,
				},
				ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at,
			}
			err = store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("CreateGenerationBatch() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestGenerationOperationKeyAndActiveSlotAreAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, firstArtifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := firstArtifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, slotID, firstArtifact.ID, "C", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	duplicateKey := batch
	duplicateKey.ID, _ = domain.ParseGenerationBatchID("gbt_" + strings.Repeat("D", 26))
	duplicateJob := job
	duplicateJob.ID, _ = domain.ParseGenerationJobID("gjb_" + strings.Repeat("D", 26))
	duplicateJob.BatchID = duplicateKey.ID
	if err := store.CreateGenerationBatch(ctx, duplicateKey, []domain.GenerationJob{duplicateJob}, at); !errors.Is(err, ErrGenerationOperationKeyConflict) {
		t.Fatalf("duplicate operation error = %v", err)
	}
	busy, busyJob := generationBatchAndJob(t, project, episode, slotID, firstArtifact.ID, "E", 1, at.Add(time.Second))
	if err := store.CreateGenerationBatch(ctx, busy, []domain.GenerationJob{busyJob}, busy.CreatedAt); !errors.Is(err, ErrGenerationSlotBusy) {
		t.Fatalf("active Slot error = %v", err)
	}
	if _, err := store.GenerationBatch(ctx, busy.ID); !errors.Is(err, ErrGenerationBatchNotFound) {
		t.Fatalf("rolled-back Batch error = %v", err)
	}
	cancelled, err := store.CancelGenerationJob(ctx, job.ID, 1, at.Add(2*time.Second))
	if err != nil || cancelled.State != domain.GenerationJobCancelled {
		t.Fatalf("CancelGenerationJob() = %#v, %v", cancelled, err)
	}
	busy, busyJob = generationBatchAndJob(t, project, episode, slotID, firstArtifact.ID, "F", 1, at.Add(3*time.Second))
	if err := store.CreateGenerationBatch(ctx, busy, []domain.GenerationJob{busyJob}, busy.CreatedAt); err != nil {
		t.Fatalf("Slot was not released after cancellation: %v", err)
	}
}

func TestGenerationStopAndFailureStateMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, firstArtifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := firstArtifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, slotID, firstArtifact.ID, "G", 1, at)
	job.StopSupported = false
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimGenerationJob(ctx, job.ID, 1, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestGenerationStop(ctx, job.ID, job.Revision, at.Add(2*time.Second)); !errors.Is(err, ErrGenerationStopUnsupported) {
		t.Fatalf("unsupported stop error = %v", err)
	}
	failed, err := store.FinishGenerationJob(ctx, job.ID, job.Revision, domain.GenerationJobFailed, "generator_failed", "明确失败", at.Add(3*time.Second))
	if err != nil || failed.State != domain.GenerationJobFailed || failed.FailureCode != "generator_failed" {
		t.Fatalf("failed Job = %#v, %v", failed, err)
	}
	if _, err := store.ClaimGenerationJob(ctx, job.ID, failed.Revision, at.Add(4*time.Second)); !errors.Is(err, ErrGenerationJobStateConflict) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func initializedGenerationStore(t *testing.T, databasePath string) (*Store, domain.Project, domain.Episode, domain.Clip, domain.Artifact) {
	t.Helper()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStoreAtPath(t, databasePath)
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "Z", 1, "生成场景", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "Z", 1, 5, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	firstSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipFirstImage)
	artifactID, _ := domain.ParseArtifactID("art_" + strings.Repeat("Z", 26))
	first := domain.Artifact{ID: artifactID, ProjectID: project.ID, Kind: domain.MediaKindImage, RelativePath: "media/images/first-z.png", SHA256: strings.Repeat("f", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "first.png", Width: project.Output.Width(), Height: project.Output.Height(), CreatedAt: at}
	version := testMediaVersion(t, firstSlotID, first.ID, "Z", 1, 0, at)
	if err := store.AddMediaVersion(ctx, first, version, 1, true, at); err != nil {
		t.Fatal(err)
	}
	return store, project, episode, clip, first
}

func initializedStoryboardStoreAtPath(t *testing.T, path string) (*Store, domain.Project, domain.Episode) {
	t.Helper()
	store := newTestStore(t, path)
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	return store, project, episode
}

func generationBatchAndJob(t *testing.T, project domain.Project, episode domain.Episode, slotID domain.MediaSlotID, firstArtifactID domain.ArtifactID, suffix string, slotRevision int64, at time.Time) (domain.GenerationBatch, domain.GenerationJob) {
	t.Helper()
	batchID, err := domain.ParseGenerationBatchID("gbt_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := domain.ParseGenerationJobID("gjb_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	batch := domain.GenerationBatch{ID: batchID, ProjectID: project.ID, EpisodeID: episode.ID, OperationKey: "operation-" + suffix, RequestHash: strings.Repeat("a", 64), CreatedAt: at}
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo, Prompt: "镜头运动", GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: domain.GeneratorVideoWanOfficial, CapabilityVersion: "1", OutputWidth: project.Output.Width(), OutputHeight: project.Output.Height(), RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: firstArtifactID}}, ParametersJSON: "{}", TargetSlotRevision: slotRevision}
	job := domain.GenerationJob{ID: jobID, BatchID: batchID, ProjectID: project.ID, EpisodeID: episode.ID, TargetSlotID: slotID, State: domain.GenerationJobQueued, Snapshot: snapshot, StopSupported: true, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	return batch, job
}
