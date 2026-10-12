package projectapp_test

import (
	"context"
	"errors"
	"path/filepath"
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

func TestSubmitGenerationBatchSnapshotsInputsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "generation-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	processor := &recordingMediaProcessor{}
	catalog := fixedCapabilityCatalog{capability: projectapp.GenerationCapability{GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: "wan3.0-video", ProviderConnectionRevision: 7, CapabilityVersion: "catalog-1", MediaKind: domain.MediaKindVideo, SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationFirstImage}, SupportedDurations: []int64{5, 10}, ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: []string{"image/png"}, SupportsAutomaticPreparation: true}}, SupportsStop: true, SupportsRecovery: true}}
	queue := &recordingGenerationQueue{}
	events := &recordingGenerationEvents{}
	service, err := projectapp.NewService(projectapp.Dependencies{IDs: projectapp.NewSecureIDGenerator(), Clock: fixedClock{at: testNow()}, Workspace: platformpaths.ApplicationProjectWorkspace{}, Stores: projectstore.ApplicationStoreFactory{}, Library: library, Importer: scriptimport.FileImporter{}, Registry: registry, MediaFiles: processor, GenerationCatalog: catalog, GenerationQueue: queue, GenerationEvents: events})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: root, Name: "生成", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	firstEpisode, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	created.Project = firstEpisode.Project
	created.Episodes = firstEpisode.Episodes
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: "镜头", PlannedDurationSeconds: 7, GenerationMode: domain.ClipGenerationFirstImage}, ExpectedStoryboardRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	firstSlotID, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipFirstImage)
	first, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: created.Project.ID, MediaSlotID: firstSlotID, SourcePath: "/external/first.png", ExpectedSlotRevision: 1, MakeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	videoSlotID, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipVideo)
	input := projectapp.SubmitGenerationBatchInput{ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID, OperationKey: "generate-video-001", Targets: []projectapp.SubmitGenerationTarget{{MediaSlotID: videoSlotID, ExpectedSlotRevision: 1, Prompt: "缓慢推进", GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: "wan3.0-video", RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: first.Current.ArtifactID}}, ParametersJSON: "{}"}}}
	ready, err := service.CheckGenerationReadiness(ctx, projectapp.CheckGenerationReadinessInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Target: input.Targets[0]})
	if err != nil || ready.Slot.ID != videoSlotID || ready.Snapshot.CapabilityVersion != "catalog-1" || ready.Snapshot.ProviderModelID != "wan3.0-video" || ready.Snapshot.ProviderConnectionRevision != 7 {
		t.Fatalf("CheckGenerationReadiness() = %#v, %v", ready, err)
	}
	notReady := input.Targets[0]
	notReady.References = nil
	if _, err := service.CheckGenerationReadiness(ctx, projectapp.CheckGenerationReadinessInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Target: notReady}); !errors.Is(err, projectapp.ErrGenerationNotReady) {
		t.Fatalf("missing input readiness error = %v", err)
	}
	batch, err := service.SubmitGenerationBatch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Jobs) != 1 || batch.Jobs[0].State != domain.GenerationJobQueued || batch.Jobs[0].Snapshot.RequestedDurationSeconds != 5 || batch.Jobs[0].Snapshot.ProviderModelID != "wan3.0-video" || batch.Jobs[0].Snapshot.ProviderConnectionRevision != 7 || batch.Jobs[0].Snapshot.CapabilityVersion != "catalog-1" || batch.Jobs[0].Snapshot.TargetSlotRevision != 1 || !batch.Jobs[0].StopSupported {
		t.Fatalf("GenerationBatch = %#v", batch)
	}
	if len(queue.projects) != 1 || queue.projects[0] != created.Project.ID {
		t.Fatalf("queue notifications = %#v", queue.projects)
	}
	if len(events.jobs) != 1 || events.jobs[0].ID != batch.Jobs[0].ID {
		t.Fatalf("Generation events = %#v", events.jobs)
	}
	if err := service.CloseProject(ctx, created.Project.ID); !errors.Is(err, projectapp.ErrProjectHasActiveGeneration) {
		t.Fatalf("CloseProject() with active GenerationJob error = %v", err)
	}
	repeated, err := service.SubmitGenerationBatch(ctx, input)
	if err != nil || repeated.Batch.ID != batch.Batch.ID || len(queue.projects) != 1 {
		t.Fatalf("idempotent submit = %#v, %v, notifications=%d", repeated, err, len(queue.projects))
	}
	changed := input
	changed.Targets = append([]projectapp.SubmitGenerationTarget(nil), input.Targets...)
	changed.Targets[0].Prompt = "另一个提示"
	if _, err := service.SubmitGenerationBatch(ctx, changed); !errors.Is(err, projectapp.ErrGenerationOperationConflict) {
		t.Fatalf("changed idempotency input error = %v", err)
	}
	unsupported := input
	unsupported.OperationKey = "generate-video-unsupported"
	unsupported.Targets = append([]projectapp.SubmitGenerationTarget(nil), input.Targets...)
	unsupported.Targets[0].RequestedDurationSeconds = 7
	if _, err := service.SubmitGenerationBatch(ctx, unsupported); !errors.Is(err, projectapp.ErrGenerationCapabilityMismatch) {
		t.Fatalf("unsupported exact duration error = %v", err)
	}
	second, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	otherAsset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{ProjectID: created.Project.ID, EpisodeID: second.Episode.ID, Kind: domain.AssetKindProp, Name: "其他集道具"})
	if err != nil {
		t.Fatal(err)
	}
	otherSlotID, _ := domain.FixedMediaSlotID(otherAsset.Asset.Asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	processor.sha256 = strings.Repeat("b", 64)
	otherMedia, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: created.Project.ID, MediaSlotID: otherSlotID, SourcePath: "/external/other.png", ExpectedSlotRevision: 1, MakeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	crossEpisode := input
	crossEpisode.OperationKey = "generate-cross-episode"
	crossEpisode.Targets = append([]projectapp.SubmitGenerationTarget(nil), input.Targets...)
	crossEpisode.Targets[0].References = []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: otherMedia.Current.ArtifactID}}
	if _, err := service.SubmitGenerationBatch(ctx, crossEpisode); !errors.Is(err, projectapp.ErrGenerationCapabilityMismatch) {
		t.Fatalf("cross-Episode reference error = %v", err)
	}
	cancelled, err := service.CancelGenerationJob(ctx, projectapp.ChangeGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: batch.Jobs[0].ID, ExpectedRevision: batch.Jobs[0].Revision})
	if err != nil || cancelled.State != domain.GenerationJobCancelled {
		t.Fatalf("CancelGenerationJob() = %#v, %v", cancelled, err)
	}
	retried, err := service.RetryGenerationJob(ctx, projectapp.RetryGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: cancelled.ID, ExpectedRevision: cancelled.Revision, OperationKey: "retry-video-001"})
	if err != nil || len(retried.Jobs) != 1 || retried.Jobs[0].RetryOfJobID != cancelled.ID || retried.Jobs[0].Snapshot.Prompt != batch.Jobs[0].Snapshot.Prompt {
		t.Fatalf("RetryGenerationJob() = %#v, %v", retried, err)
	}
	repeatedRetry, err := service.RetryGenerationJob(ctx, projectapp.RetryGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: cancelled.ID, ExpectedRevision: cancelled.Revision, OperationKey: "retry-video-001"})
	if err != nil || repeatedRetry.Batch.ID != retried.Batch.ID {
		t.Fatalf("idempotent RetryGenerationJob() = %#v, %v", repeatedRetry, err)
	}
	if len(events.jobs) != 3 || len(queue.projects) != 3 {
		t.Fatalf("notifications after cancel/retry: events=%d queue=%d", len(events.jobs), len(queue.projects))
	}
}

type fixedCapabilityCatalog struct {
	capability projectapp.GenerationCapability
	err        error
}

func (c fixedCapabilityCatalog) Generator(context.Context, domain.GeneratorID) (projectapp.GenerationCapability, error) {
	return c.capability, c.err
}

func (c fixedCapabilityCatalog) GeneratorModel(_ context.Context, _ domain.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	if modelID != c.capability.ProviderModelID {
		return projectapp.GenerationCapability{}, errors.New("model not found")
	}
	return c.capability, c.err
}

type recordingGenerationQueue struct {
	projects []domain.ProjectID
}

type recordingGenerationEvents struct {
	jobs []domain.GenerationJob
}

func (e *recordingGenerationEvents) PublishGenerationJob(job domain.GenerationJob) {
	e.jobs = append(e.jobs, job)
}

func (q *recordingGenerationQueue) Notify(projectID domain.ProjectID) {
	q.projects = append(q.projects, projectID)
}
