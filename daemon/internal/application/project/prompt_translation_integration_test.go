package projectapp_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	"github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

type recordingPromptTranslator struct {
	calls        int
	options      projectapp.GenerationPromptTranslationOptions
	source       string
	result       string
	err          error
	beforeReturn func()
}

func (t *recordingPromptTranslator) Translate(_ context.Context, options projectapp.GenerationPromptTranslationOptions, prompt string) (string, error) {
	t.calls++
	t.options, t.source = options, prompt
	if t.beforeReturn != nil {
		t.beforeReturn()
	}
	return t.result, t.err
}

func h3SubmissionFixture(t *testing.T, id domain.GeneratorID, translator projectapp.GenerationPromptTranslator) (*projectapp.Service, projectapp.SubmitGenerationBatchInput, domain.ClipID, projectapp.ProjectStore, *recordingGenerationQueue) {
	t.Helper()
	ctx := context.Background()
	studioStore := newStudioStore(t)
	t.Cleanup(func() { _ = studioStore.Close() })
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	t.Cleanup(func() { _ = registry.CloseAll() })
	catalog := fixedCapabilityCatalog{capability: projectapp.GenerationCapability{GeneratorID: id, ProviderModelID: "h3-base", ProviderConnectionRevision: 1, CapabilityVersion: "h3-test", MediaKind: domain.MediaKindVideo, SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationFirstImage}, SupportedDurations: []int64{5}, ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1}}}}
	queue := &recordingGenerationQueue{}
	service, err := projectapp.NewService(projectapp.Dependencies{IDs: projectapp.NewSecureIDGenerator(), Clock: fixedClock{at: testNow()}, Workspace: platformpaths.ApplicationProjectWorkspace{}, Stores: projectstore.ApplicationStoreFactory{}, Library: library, Importer: scriptimport.FileImporter{}, Registry: registry, MediaFiles: &recordingMediaProcessor{}, GenerationCatalog: catalog, GenerationQueue: queue, PromptTranslator: translator})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: filepath.Join(t.TempDir(), "translation-project"), Name: "翻译", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	episodeID := episodes.Episodes[0].ID
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: episodeID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	original := "integrated_multimodal_description: 缓慢推进。<d>[Chinese]你好！</d>\noverall_soundscape: 安静。\nnon_diegetic_music: 无。"
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: episodeID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: "镜头", PlannedDurationSeconds: 5, GenerationMode: domain.ClipGenerationFirstImage, VideoPrompts: domain.ClipVideoPrompts{FirstImage: original}}, ExpectedStoryboardRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	firstSlot, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipFirstImage)
	first, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: created.Project.ID, MediaSlotID: firstSlot, SourcePath: "/external/first.png", ExpectedSlotRevision: 1, MakeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	videoSlot, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipVideo)
	input := projectapp.SubmitGenerationBatchInput{ProjectID: created.Project.ID, EpisodeID: episodeID, OperationKey: "translate-h3-001", PromptTranslation: &projectapp.GenerationPromptTranslationOptions{RuntimeKind: "user_codex", ModelID: "test-model"}, Targets: []projectapp.SubmitGenerationTarget{{MediaSlotID: videoSlot, ExpectedSlotRevision: 1, Prompt: original, GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: id, ProviderModelID: "h3-base", RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: first.Current.ArtifactID}}, ParametersJSON: "{}"}}}
	handle, _ := registry.ByProjectID(created.Project.ID)
	return service, input, clip.Clip.ID, handle.Store, queue
}

func TestH3TranslationSubmissionPreservesEditorReferencesAndRetry(t *testing.T) {
	for _, generator := range []domain.GeneratorID{domain.GeneratorVideoH3China, domain.GeneratorVideoH3Official, domain.GeneratorVideoH3ShotForward} {
		t.Run(string(generator), func(t *testing.T) {
			translator := &recordingPromptTranslator{result: "integrated_multimodal_description: Move in slowly. <d>[Chinese]你好！</d>\noverall_soundscape: Quiet.\nnon_diegetic_music: None."}
			service, input, clipID, _, queue := h3SubmissionFixture(t, generator, translator)
			ctx := context.Background()
			if _, err := service.CheckGenerationReadiness(ctx, projectapp.CheckGenerationReadinessInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Target: input.Targets[0]}); err != nil {
				t.Fatal(err)
			}
			if translator.calls != 0 {
				t.Fatal("readiness called Agent")
			}
			batch, err := service.SubmitGenerationBatch(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			job := batch.Jobs[0]
			if translator.calls != 1 || translator.source != input.Targets[0].Prompt || translator.options != *input.PromptTranslation || job.Snapshot.Prompt != translator.result || !reflect.DeepEqual(job.Snapshot.References, input.Targets[0].References) {
				t.Fatalf("translation changed frozen inputs: %#v", job)
			}
			clip, err := service.GetClip(ctx, projectapp.GetClipInput{ProjectID: input.ProjectID, ClipID: clipID})
			if err != nil || clip.Clip.VideoPrompts.FirstImage != input.Targets[0].Prompt {
				t.Fatalf("editor original changed: %#v, %v", clip, err)
			}
			if _, err := service.SubmitGenerationBatch(ctx, input); err != nil || translator.calls != 1 || len(queue.projects) != 1 {
				t.Fatalf("dedup retranlated: %v", err)
			}
			cancelled, err := service.CancelGenerationJob(ctx, projectapp.ChangeGenerationJobInput{ProjectID: input.ProjectID, GenerationJobID: job.ID, ExpectedRevision: job.Revision})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := service.RetryGenerationJob(ctx, projectapp.RetryGenerationJobInput{ProjectID: input.ProjectID, GenerationJobID: job.ID, ExpectedRevision: cancelled.Revision, OperationKey: "retry-translated-h3"})
			if err != nil || translator.calls != 1 || retry.Jobs[0].Snapshot.Prompt != translator.result {
				t.Fatalf("retry changed prompt: %#v, %v", retry, err)
			}
		})
	}
}

func TestH3TranslationFailureDoesNotPersistBatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		result string
		err    error
		want   error
	}{
		{"worker", "", projectapp.ErrPromptTranslationFailed, projectapp.ErrPromptTranslationFailed},
		{"timeout", "", context.DeadlineExceeded, projectapp.ErrPromptTranslationTimeout},
		{"empty", "", nil, projectapp.ErrPromptTranslationInvalid},
		{"expanded-too-long", strings.Repeat("a", 7001), nil, projectapp.ErrPromptTranslationInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			translator := &recordingPromptTranslator{result: test.result, err: test.err}
			service, input, _, store, queue := h3SubmissionFixture(t, domain.GeneratorVideoH3Official, translator)
			if _, err := service.SubmitGenerationBatch(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, err := store.GenerationBatchByOperationKey(context.Background(), input.ProjectID, input.OperationKey); !errors.Is(err, projectapp.ErrGenerationBatchNotFound) || len(queue.projects) != 0 {
				t.Fatalf("failed preparation persisted batch: %v", err)
			}
		})
	}
}

func TestH3PreparationValidatesAllTargetsAndCancellation(t *testing.T) {
	translator := &recordingPromptTranslator{result: "Slowly push in."}
	service, input, _, store, queue := h3SubmissionFixture(t, domain.GeneratorVideoH3Official, translator)
	bad := input
	bad.Targets = append(append([]projectapp.SubmitGenerationTarget(nil), input.Targets...), input.Targets[0])
	if _, err := service.SubmitGenerationBatch(context.Background(), bad); err == nil || translator.calls != 0 {
		t.Fatalf("invalid batch consumed Agent: %v", err)
	}
	missing := input
	missing.PromptTranslation = nil
	if _, err := service.SubmitGenerationBatch(context.Background(), missing); !errors.Is(err, projectapp.ErrPromptTranslationUnavailable) || translator.calls != 0 {
		t.Fatalf("missing selection = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	translator.beforeReturn = cancel
	if _, err := service.SubmitGenerationBatch(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if _, err := store.GenerationBatchByOperationKey(context.Background(), input.ProjectID, input.OperationKey); !errors.Is(err, projectapp.ErrGenerationBatchNotFound) || len(queue.projects) != 0 {
		t.Fatal("cancelled preparation persisted batch")
	}
}

type blockingPromptTranslator struct {
	calls   atomic.Int32
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (t *blockingPromptTranslator) Translate(ctx context.Context, _ projectapp.GenerationPromptTranslationOptions, _ string) (string, error) {
	t.calls.Add(1)
	t.once.Do(func() { close(t.started) })
	select {
	case <-t.proceed:
		return "Slowly push in.", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestConcurrentH3SubmissionReplayTranslatesOnce(t *testing.T) {
	translator := &blockingPromptTranslator{started: make(chan struct{}), proceed: make(chan struct{})}
	service, input, _, _, queue := h3SubmissionFixture(t, domain.GeneratorVideoH3Official, translator)
	type result struct {
		batch domain.GenerationBatchDetails
		err   error
	}
	results := make(chan result, 2)
	go func() {
		batch, err := service.SubmitGenerationBatch(context.Background(), input)
		results <- result{batch, err}
	}()
	<-translator.started
	go func() {
		batch, err := service.SubmitGenerationBatch(context.Background(), input)
		results <- result{batch, err}
	}()
	close(translator.proceed)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.batch.Batch.ID != second.batch.Batch.ID || translator.calls.Load() != 1 || len(queue.projects) != 1 {
		t.Fatalf("concurrent replay: %v/%v calls=%d", first.err, second.err, translator.calls.Load())
	}
}

func TestWaitingH3SubmissionReplayCanBeCancelled(t *testing.T) {
	translator := &blockingPromptTranslator{started: make(chan struct{}), proceed: make(chan struct{})}
	service, input, _, _, _ := h3SubmissionFixture(t, domain.GeneratorVideoH3Official, translator)
	first := make(chan error, 1)
	go func() { _, err := service.SubmitGenerationBatch(context.Background(), input); first <- err }()
	<-translator.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.SubmitGenerationBatch(ctx, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting cancellation=%v", err)
	}
	close(translator.proceed)
	if err := <-first; err != nil || translator.calls.Load() != 1 {
		t.Fatalf("first request=%v calls=%d", err, translator.calls.Load())
	}
}
