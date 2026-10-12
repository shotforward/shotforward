package projectapp_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

func TestEpisodeLibraryTrimFreezesQueuedJobsAndRetriesButUpdatesNewSubmissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	capability := projectapp.GenerationCapability{
		GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: "wan3.0-video", ProviderConnectionRevision: 1, CapabilityVersion: "trim-test-1",
		MediaKind: domain.MediaKindVideo, SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationReference}, SupportedDurations: []int64{5},
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceAudio, MaximumCount: 1, MaximumDurationMilliseconds: 15_000},
			{Role: domain.GenerationReferenceVideo, MaximumCount: 1, MaximumDurationMilliseconds: 15_000},
		}, MaximumTotalTimedReferenceDurationMilliseconds: 20_000,
	}
	service, err := projectapp.NewService(projectapp.Dependencies{
		IDs: projectapp.NewSecureIDGenerator(), Clock: fixedClock{at: testNow()}, Workspace: platformpaths.ApplicationProjectWorkspace{},
		Stores: projectstore.ApplicationStoreFactory{}, Library: library, Importer: scriptimport.FileImporter{}, Registry: registry,
		GenerationCatalog: fixedCapabilityCatalog{capability: capability}, GenerationQueue: &recordingGenerationQueue{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: filepath.Join(t.TempDir(), "trim-project"), Name: "裁剪快照", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	episode, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	clips := []domain.ClipDetails{}
	for _, title := range []string{"第一个单元", "第二个单元"} {
		clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: title, PlannedDurationSeconds: 5, GenerationMode: domain.ClipGenerationReference}, ExpectedStoryboardRevision: int64(len(clips) + 2)})
		if err != nil {
			t.Fatal(err)
		}
		clips = append(clips, clip)
	}
	handle, ok := registry.ByProjectID(created.Project.ID)
	if !ok {
		t.Fatal("project was not registered")
	}
	artifacts := []domain.Artifact{}
	for index, kind := range []domain.MediaKind{domain.MediaKindAudio, domain.MediaKindVideo} {
		letter := []string{"A", "B"}[index]
		artifact := domain.Artifact{ID: domain.ArtifactID("art_" + strings.Repeat(letter, 26)), ProjectID: created.Project.ID, Kind: kind,
			RelativePath: "media/reference/" + letter + ".bin", SHA256: strings.Repeat(strings.ToLower(letter), 64), ByteSize: 128,
			MIMEType: "application/octet-stream", OriginalFilename: letter + ".bin", DurationMilliseconds: 30_000, CreatedAt: testNow(),
		}
		if kind == domain.MediaKindAudio {
			artifact.AudioSampleRate, artifact.AudioChannels = 44_100, 1
		} else {
			artifact.Width, artifact.Height, artifact.FPSNumerator, artifact.FPSDenominator = 720, 1280, 24, 1
		}
		media := domain.EpisodeReferenceMedia{EpisodeID: episode.Episode.ID, Artifact: artifact, Name: letter, Revision: 1, TrimStartMilliseconds: 3_000, TrimEndMilliseconds: 11_000, CreatedAt: testNow(), UpdatedAt: testNow()}
		if err := handle.Store.PutEpisodeReferenceMedia(ctx, media, testNow()); err != nil {
			t.Fatal(err)
		}
		for _, clip := range clips {
			if err := handle.Store.AddClipReferenceArtifact(ctx, clip.Clip.ID, artifact, testNow()); err != nil {
				t.Fatal(err)
			}
		}
		artifacts = append(artifacts, artifact)
	}
	targetFor := func(clip domain.ClipDetails) projectapp.SubmitGenerationTarget {
		slotID, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipVideo)
		return projectapp.SubmitGenerationTarget{MediaSlotID: slotID, ExpectedSlotRevision: 1, Prompt: "A character speaks and moves.", GenerationMode: domain.ClipGenerationReference,
			GeneratorID: capability.GeneratorID, ProviderModelID: capability.ProviderModelID, RequestedDurationSeconds: 5,
			References: []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceAudio, ArtifactID: artifacts[0].ID, TrimEndMilliseconds: 30_000}, {Role: domain.GenerationReferenceVideo, ArtifactID: artifacts[1].ID, TrimEndMilliseconds: 30_000}},
		}
	}
	input := projectapp.SubmitGenerationBatchInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, OperationKey: "trim-first-batch", Targets: []projectapp.SubmitGenerationTarget{targetFor(clips[0]), targetFor(clips[1])}}
	first, err := service.SubmitGenerationBatch(ctx, input)
	if err != nil || len(first.Jobs) != 2 {
		t.Fatalf("first submission = %+v, %v", first, err)
	}
	assertRanges := func(jobs []domain.GenerationJob, start, end int64) {
		t.Helper()
		for _, job := range jobs {
			if len(job.Snapshot.References) != 2 {
				t.Fatalf("job references = %+v", job.Snapshot.References)
			}
			for _, reference := range job.Snapshot.References {
				if reference.TrimStartMilliseconds != start || reference.TrimEndMilliseconds != end || !reference.PreparationRequired {
					t.Fatalf("frozen job selection = %+v; want %d..%d", reference, start, end)
				}
			}
		}
	}
	assertRanges(first.Jobs, 3_000, 11_000)
	start, end := int64(5_000), int64(9_000)
	for _, artifact := range artifacts {
		if _, err := service.UpdateEpisodeReferenceMedia(ctx, projectapp.UpdateEpisodeReferenceMediaInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, ArtifactID: artifact.ID, Name: strings.TrimSuffix(artifact.OriginalFilename, ".bin"), TrimStartMilliseconds: &start, TrimEndMilliseconds: &end, ExpectedRevision: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, original := range first.Jobs {
		persisted, err := service.GetGenerationJob(ctx, projectapp.GetGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: original.ID})
		if err != nil || !reflect.DeepEqual(persisted.Snapshot, original.Snapshot) {
			t.Fatalf("library update changed queued snapshot = %+v, %v", persisted, err)
		}
	}
	// Each Slot admits one active task. Cancel the originals only after
	// checking their persisted snapshots still retain the first selection.
	var cancelled domain.GenerationJob
	for index, original := range first.Jobs {
		value, err := service.CancelGenerationJob(ctx, projectapp.ChangeGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: original.ID, ExpectedRevision: original.Revision})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			cancelled = value
		}
	}
	input.OperationKey = "trim-next-batch"
	next, err := service.SubmitGenerationBatch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertRanges(next.Jobs, 5_000, 9_000)
	for _, job := range next.Jobs {
		if _, err := service.CancelGenerationJob(ctx, projectapp.ChangeGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: job.ID, ExpectedRevision: job.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	retried, err := service.RetryGenerationJob(ctx, projectapp.RetryGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: cancelled.ID, ExpectedRevision: cancelled.Revision, OperationKey: "trim-retry"})
	if err != nil || len(retried.Jobs) != 1 {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	assertRanges(retried.Jobs, 3_000, 11_000)
	if retried.Jobs[0].RetryOfJobID != cancelled.ID {
		t.Fatal("retry lost its original Job identity")
	}
	forged := input
	forged.OperationKey = "trim-forged-retry"
	forged.Targets = []projectapp.SubmitGenerationTarget{targetFor(clips[0])}
	forged.Targets[0].RetryOfJobID = cancelled.ID
	if _, err := service.SubmitGenerationBatch(ctx, forged); !errors.Is(err, projectapp.ErrInvalidRequest) {
		t.Fatalf("client-supplied retry ID enabled frozen selection: %v", err)
	}
	for _, artifact := range artifacts {
		current, err := handle.Store.Artifact(ctx, artifact.ID)
		if err != nil || !reflect.DeepEqual(current, artifact) {
			t.Fatalf("crop changed immutable original Artifact: %+v, %v", current, err)
		}
	}
	// A deduplicated audio Artifact can also be selected by an independent
	// active AudioReferenceAsset Slot. Deleting the upload-library entry must
	// still block new submissions and retries, rather than falling back to the
	// full original merely because ArtifactSelectedInEpisode remains true.
	audioReference, err := service.CreateAudioReferenceAsset(ctx, projectapp.CreateAudioReferenceAssetInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, Name: "独立音频引用"})
	if err != nil {
		t.Fatal(err)
	}
	audioSlotID, _ := domain.FixedMediaSlotID(audioReference.ID.String(), domain.MediaSlotAudioPrimary)
	version := domain.MediaVersion{ID: domain.MediaVersionID("mvr_" + strings.Repeat("C", 26)), SlotID: audioSlotID, ArtifactID: artifacts[0].ID, SequenceNumber: 1, Revision: 1, CreatedAt: testNow(), UpdatedAt: testNow()}
	if err := handle.Store.AddMediaVersion(ctx, artifacts[0], version, 1, true, testNow()); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteEpisodeReferenceMedia(ctx, projectapp.DeleteEpisodeReferenceMediaInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, ArtifactID: artifacts[0].ID, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	selected, err := handle.Store.ArtifactSelectedInEpisode(ctx, artifacts[0].ID, episode.Episode.ID)
	if err != nil || !selected {
		t.Fatalf("fixture must retain active AudioReferenceAsset Slot selection: %v, %v", selected, err)
	}
	if _, err := handle.Store.EpisodeReferenceMedia(ctx, episode.Episode.ID, artifacts[0].ID, false); !errors.Is(err, projectapp.ErrArtifactNotFound) {
		t.Fatalf("active library lookup after soft-delete = %v", err)
	}
	deletedMedia, err := handle.Store.EpisodeReferenceMedia(ctx, episode.Episode.ID, artifacts[0].ID, true)
	if err != nil || deletedMedia.DeletedAt == nil {
		t.Fatalf("tombstone lookup after soft-delete = %+v, %v", deletedMedia, err)
	}
	if _, err := service.CheckGenerationReadiness(ctx, projectapp.CheckGenerationReadinessInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, Target: targetFor(clips[0])}); !errors.Is(err, projectapp.ErrGenerationCapabilityMismatch) || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("active Slot revived soft-deleted upload in readiness: %v", err)
	}
	input.OperationKey = "trim-deleted-library-submit"
	if _, err := service.SubmitGenerationBatch(ctx, input); !errors.Is(err, projectapp.ErrGenerationCapabilityMismatch) || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("active Slot revived soft-deleted upload in submission: %v", err)
	}
	if _, err := service.RetryGenerationJob(ctx, projectapp.RetryGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: cancelled.ID, ExpectedRevision: cancelled.Revision, OperationKey: "trim-deleted-library-retry"}); !errors.Is(err, projectapp.ErrGenerationCapabilityMismatch) || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("retry revived soft-deleted upload in the same Episode: %v", err)
	}
}
