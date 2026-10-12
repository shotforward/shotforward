package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
)

func TestSchedulerCompletesQueuedJobAndPublishesArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, status: GeneratorStatus{State: GeneratorSucceeded, Stage: "complete", Outputs: []GeneratorOutput{{SourcePath: filepath.Join(fixture.root, "generator", "output.png")}}}}
	scheduler := fixture.scheduler(t, generator)
	processed, err := scheduler.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	job, err := fixture.store.GenerationJob(ctx, fixture.jobs[0].ID)
	if err != nil || job.State != domain.GenerationJobSucceeded || job.ResultArtifactID == "" || job.ResultVersionID == "" {
		t.Fatalf("completed Job = %#v, %v", job, err)
	}
	slot, err := fixture.store.MediaSlot(ctx, fixture.jobs[0].TargetSlotID, true)
	if err != nil || slot.Current == nil || slot.Current.ID != job.ResultVersionID || slot.Current.ArtifactID != job.ResultArtifactID {
		t.Fatalf("completed Slot = %#v, %v", slot, err)
	}
	artifact, err := fixture.store.Artifact(ctx, job.ResultArtifactID)
	if err != nil || artifact.Width != job.Snapshot.OutputWidth || artifact.Height != job.Snapshot.OutputHeight {
		t.Fatalf("normalized Artifact = %#v, %v", artifact, err)
	}
	if generator.startCalls != 1 || generator.pollCalls != 1 {
		t.Fatalf("Generator calls: start=%d poll=%d", generator.startCalls, generator.pollCalls)
	}
}

func TestSchedulerRetriesExplicitlyRetryablePollErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	generator := &fakeGenerator{
		id:          domain.GeneratorImageShotForward,
		limit:       1,
		recoverable: true,
		pollErrors: []error{
			NewRetryablePollError(errors.New("rate limited")),
			NewRetryablePollError(errors.New("upstream unavailable")),
		},
	}
	processed, err := fixture.scheduler(t, generator).ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	job, err := fixture.store.GenerationJob(ctx, fixture.jobs[0].ID)
	if err != nil || job.State != domain.GenerationJobSucceeded || generator.pollCalls != 3 {
		t.Fatalf("retried Job = %#v, err=%v, polls=%d", job, err, generator.pollCalls)
	}
}

func TestSchedulerDoesNotRetryUnmarkedPollError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, pollErrors: []error{errors.New("invalid response")}}
	processed, err := fixture.scheduler(t, generator).ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	job, err := fixture.store.GenerationJob(ctx, fixture.jobs[0].ID)
	if err != nil || job.State != domain.GenerationJobFailed || job.FailureCode != "generator_poll_failed" || generator.pollCalls != 1 {
		t.Fatalf("failed Job = %#v, err=%v, polls=%d", job, err, generator.pollCalls)
	}
}

func TestPollRetryIntervalDoublesAndCaps(t *testing.T) {
	t.Parallel()
	delay := time.Duration(0)
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for index, expected := range want {
		delay = nextRetryInterval(delay, 2*time.Second)
		if delay != expected {
			t.Fatalf("retry %d delay = %s, want %s", index+1, delay, expected)
		}
	}
}

func TestGeneratorPollIntervalUsesAdapterMinimum(t *testing.T) {
	t.Parallel()
	base := &fakeGenerator{}
	if got := generatorPollInterval(base, 2*time.Second); got != 2*time.Second {
		t.Fatalf("default interval = %s", got)
	}
	configured := slowPollGenerator{fakeGenerator: base, interval: 10 * time.Second}
	if got := generatorPollInterval(configured, 2*time.Second); got != 10*time.Second {
		t.Fatalf("adapter interval = %s", got)
	}
	configured.interval = time.Second
	if got := generatorPollInterval(configured, 2*time.Second); got != 2*time.Second {
		t.Fatalf("minimum interval = %s", got)
	}
}

func TestSchedulerMarksUnrecoverableRunningJobFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	at := fixture.jobs[0].CreatedAt.Add(time.Second)
	job, err := fixture.store.ClaimGenerationJob(ctx, fixture.jobs[0].ID, 1, at)
	if err != nil {
		t.Fatal(err)
	}
	job, err = fixture.store.RecordGenerationOperation(ctx, job.ID, job.Revision, "external-1", "generating", "", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: false}
	processed, err := fixture.scheduler(t, generator).ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	failed, err := fixture.store.GenerationJob(ctx, job.ID)
	if err != nil || failed.State != domain.GenerationJobFailed || failed.FailureCode != "execution_interrupted" || generator.pollCalls != 0 {
		t.Fatalf("unrecoverable Job = %#v, err=%v, polls=%d", failed, err, generator.pollCalls)
	}
}

func TestSchedulerDoesNotResubmitRunningJobWithoutOperationIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	job, err := fixture.store.ClaimGenerationJob(ctx, fixture.jobs[0].ID, 1, fixture.jobs[0].CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true}
	processed, err := fixture.scheduler(t, generator).ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	failed, err := fixture.store.GenerationJob(ctx, job.ID)
	if err != nil || failed.State != domain.GenerationJobFailed || failed.FailureCode != "execution_interrupted" || generator.startCalls != 0 {
		t.Fatalf("interrupted Job = %#v, err=%v, starts=%d", failed, err, generator.startCalls)
	}
}

func TestSchedulerStopsRequestedGeneratorOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	at := fixture.jobs[0].CreatedAt.Add(time.Second)
	job, err := fixture.store.ClaimGenerationJob(ctx, fixture.jobs[0].ID, 1, at)
	if err != nil {
		t.Fatal(err)
	}
	job, err = fixture.store.RecordGenerationOperation(ctx, job.ID, job.Revision, "external-stop", "generating", "", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	job, err = fixture.store.RequestGenerationStop(ctx, job.ID, job.Revision, at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, status: GeneratorStatus{State: GeneratorStopped, Stage: "stopped"}}
	processed, err := fixture.scheduler(t, generator).ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	stopped, err := fixture.store.GenerationJob(ctx, job.ID)
	if err != nil || stopped.State != domain.GenerationJobStopped || generator.stopCalls != 1 {
		t.Fatalf("stopped Job = %#v, err=%v, stop calls=%d", stopped, err, generator.stopCalls)
	}
}

func TestSchedulerPersistsOperationBeforeStoppingConcurrentRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, status: GeneratorStatus{State: GeneratorStopped, Stage: "stopped"}, started: make(chan string, 1), startRelease: make(chan struct{})}
	scheduler := fixture.scheduler(t, generator)
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.ProcessNext(ctx)
		done <- err
	}()
	select {
	case <-generator.started:
	case <-time.After(3 * time.Second):
		t.Fatal("Generator Start was not reached")
	}
	running, err := fixture.store.GenerationJob(ctx, fixture.jobs[0].ID)
	if err != nil || running.State != domain.GenerationJobRunning || running.GeneratorOperationID != "" {
		t.Fatalf("running Job = %#v, %v", running, err)
	}
	requested, err := fixture.store.RequestGenerationStop(ctx, running.ID, running.Revision, running.UpdatedAt)
	if err != nil || requested.State != domain.GenerationJobStopRequested {
		t.Fatalf("RequestGenerationStop() = %#v, %v", requested, err)
	}
	close(generator.startRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Scheduler did not finish")
	}
	stopped, err := fixture.store.GenerationJob(ctx, running.ID)
	if err != nil || stopped.State != domain.GenerationJobStopped || stopped.GeneratorOperationID == "" || generator.stopCalls != 1 {
		t.Fatalf("stopped Job = %#v, err=%v, stops=%d", stopped, err, generator.stopCalls)
	}
}

func TestSchedulerCommitsSuccessThatRacesWithStopRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSchedulerFixture(t, 1)
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, status: GeneratorStatus{State: GeneratorSucceeded, Stage: "complete", Outputs: []GeneratorOutput{{SourcePath: filepath.Join(fixture.root, "generator", "raced.png")}}}, release: make(chan struct{}, 1)}
	scheduler := fixture.scheduler(t, generator)
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.ProcessNext(ctx)
		done <- err
	}()
	var running domain.GenerationJob
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		running, _ = fixture.store.GenerationJob(ctx, fixture.jobs[0].ID)
		generator.mutex.Lock()
		polling := generator.pollCalls > 0
		generator.mutex.Unlock()
		if running.State == domain.GenerationJobRunning && running.GeneratorOperationID != "" && polling {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if running.GeneratorOperationID == "" {
		t.Fatal("Generator operation was not recorded")
	}
	requested, err := fixture.store.RequestGenerationStop(ctx, running.ID, running.Revision, running.UpdatedAt)
	if err != nil || requested.State != domain.GenerationJobStopRequested {
		t.Fatalf("RequestGenerationStop() = %#v, %v", requested, err)
	}
	generator.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Scheduler did not finish")
	}
	completed, err := fixture.store.GenerationJob(ctx, running.ID)
	if err != nil || completed.State != domain.GenerationJobSucceeded || completed.ResultArtifactID == "" {
		t.Fatalf("raced successful Job = %#v, %v", completed, err)
	}
}

func TestSchedulerEnforcesGeneratorConcurrency(t *testing.T) {
	fixture := newSchedulerFixture(t, 2)
	generator := &fakeGenerator{id: domain.GeneratorImageShotForward, limit: 1, recoverable: true, started: make(chan string, 2), release: make(chan struct{}, 2)}
	scheduler := fixture.scheduler(t, generator)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	scheduler.Notify(fixture.project.ID)

	select {
	case <-generator.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first Job did not start")
	}
	select {
	case second := <-generator.started:
		t.Fatalf("second Job %q started before Generator capacity was released", second)
	case <-time.After(100 * time.Millisecond):
	}
	generator.release <- struct{}{}
	select {
	case <-generator.started:
	case <-time.After(3 * time.Second):
		t.Fatal("second Job did not start after Generator capacity was released")
	}
	generator.release <- struct{}{}
	waitForTerminalJobs(t, fixture.store, fixture.jobs)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Scheduler did not stop")
	}
	generator.mutex.Lock()
	maximum := generator.maximumActive
	generator.mutex.Unlock()
	if maximum != 1 {
		t.Fatalf("maximum Generator concurrency = %d", maximum)
	}
}

type schedulerFixture struct {
	root     string
	store    projectapp.ProjectStore
	registry *projectapp.ProjectStoreRegistry
	project  domain.Project
	episode  domain.Episode
	jobs     []domain.GenerationJob
	clock    schedulerTestClock
}

func newSchedulerFixture(t *testing.T, targetCount int) *schedulerFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := (projectstore.ApplicationStoreFactory{}).Create(ctx, filepath.Join(root, "project.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	projectID := mustProjectID(t, "A")
	episodeID := mustEpisodeID(t, "A")
	output, _ := domain.NewOutputSpec(720, 1280)
	project := domain.Project{ID: projectID, Name: "Scheduler", Output: output, Revision: 1, CreatedAt: now, UpdatedAt: now}
	episode := domain.Episode{EpisodeMetadata: domain.EpisodeMetadata{ID: episodeID, ProjectID: projectID, SequenceNumber: 1, Title: "第 1 集", TargetDuration: 60 * time.Second, Revision: 1, CreatedAt: now, UpdatedAt: now}, Script: domain.Script{Revision: 1, UpdatedAt: now}}
	if err := store.Initialize(ctx, project, episode); err != nil {
		store.Close()
		t.Fatal(err)
	}
	batchID := mustBatchID(t, "A")
	batch := domain.GenerationBatch{ID: batchID, ProjectID: projectID, EpisodeID: episodeID, OperationKey: "scheduler-batch", RequestHash: strings.Repeat("a", 64), CreatedAt: now.Add(time.Second)}
	assetWidth, assetHeight := domain.GenerationOutputSize(domain.MediaSlotAssetPrimaryImage, output)
	jobs := make([]domain.GenerationJob, 0, targetCount)
	for index := 0; index < targetCount; index++ {
		suffix := string(rune('B' + index))
		identityID, _ := domain.ParseAssetIdentityID("asi_" + strings.Repeat(suffix, 26))
		assetID, _ := domain.ParseAssetID("ast_" + strings.Repeat(suffix, 26))
		assetAt := now.Add(time.Duration(index+1) * time.Millisecond)
		identity := domain.AssetIdentity{ID: identityID, ProjectID: projectID, Kind: domain.AssetKindProp, Name: "Asset " + suffix, Revision: 1, CreatedAt: assetAt, UpdatedAt: assetAt}
		asset := domain.Asset{ID: assetID, ProjectID: projectID, EpisodeID: episodeID, IdentityID: identityID, Prompt: "", Revision: 1, CreatedAt: assetAt, UpdatedAt: assetAt}
		if err := store.CreateAsset(ctx, &identity, asset, assetAt); err != nil {
			store.Close()
			t.Fatal(err)
		}
		slotID, _ := domain.FixedMediaSlotID(assetID.String(), domain.MediaSlotAssetPrimaryImage)
		jobID, _ := domain.ParseGenerationJobID("gjb_" + strings.Repeat(suffix, 26))
		snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindImage, Prompt: "生成道具", GeneratorID: domain.GeneratorImageShotForward, CapabilityVersion: "1", OutputWidth: assetWidth, OutputHeight: assetHeight, ParametersJSON: "{}", TargetSlotRevision: 1}
		jobs = append(jobs, domain.GenerationJob{ID: jobID, BatchID: batchID, ProjectID: projectID, EpisodeID: episodeID, TargetSlotID: slotID, State: domain.GenerationJobQueued, Snapshot: snapshot, StopSupported: true, ProgressStage: "queued", Revision: 1, CreatedAt: batch.CreatedAt, UpdatedAt: batch.CreatedAt})
	}
	if err := store.CreateGenerationBatch(ctx, batch, jobs, batch.CreatedAt); err != nil {
		store.Close()
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	if _, _, err := registry.Register(projectapp.ProjectHandle{ProjectID: projectID, RootPath: root, Store: store}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	fixture := &schedulerFixture{root: root, store: store, registry: registry, project: project, episode: episode, jobs: jobs, clock: schedulerTestClock{at: batch.CreatedAt.Add(time.Hour)}}
	t.Cleanup(func() { _ = registry.CloseAll() })
	return fixture
}

func (f *schedulerFixture) scheduler(t *testing.T, generator *fakeGenerator) *Scheduler {
	t.Helper()
	if generator.outputRoot == "" {
		generator.outputRoot = filepath.Join(f.root, "generator")
	}
	value, err := NewScheduler(SchedulerOptions{Registry: f.registry, Generators: fakeGeneratorRegistry{generator: generator}, MediaFiles: &fakeMediaFiles{}, IDs: projectapp.NewSecureIDGenerator(), Clock: f.clock, GlobalConcurrency: 2, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type fakeGeneratorRegistry struct{ generator Generator }

type schedulerTestClock struct{ at time.Time }

func (c schedulerTestClock) Now() time.Time { return c.at }

func (r fakeGeneratorRegistry) Generator(id domain.GeneratorID) (Generator, bool) {
	return r.generator, r.generator != nil && r.generator.ID() == id
}

type fakeGenerator struct {
	mutex         sync.Mutex
	id            domain.GeneratorID
	limit         int
	recoverable   bool
	status        GeneratorStatus
	outputRoot    string
	pollErrors    []error
	started       chan string
	release       chan struct{}
	startRelease  chan struct{}
	active        int
	maximumActive int
	startCalls    int
	pollCalls     int
	stopCalls     int
}

type slowPollGenerator struct {
	*fakeGenerator
	interval time.Duration
}

func (p slowPollGenerator) PollInterval() time.Duration { return p.interval }

func (p *fakeGenerator) ID() domain.GeneratorID { return p.id }
func (p *fakeGenerator) ConcurrencyLimit() int  { return p.limit }
func (p *fakeGenerator) SupportsRecovery(domain.GenerationInputSnapshot) bool {
	return p.recoverable
}
func (p *fakeGenerator) Start(_ context.Context, request GeneratorRequest) (GeneratorOperation, error) {
	p.mutex.Lock()
	p.startCalls++
	p.active++
	if p.active > p.maximumActive {
		p.maximumActive = p.active
	}
	p.mutex.Unlock()
	if p.started != nil {
		p.started <- request.JobID.String()
	}
	if p.startRelease != nil {
		<-p.startRelease
	}
	return GeneratorOperation{ID: request.JobID.String()}, nil
}
func (p *fakeGenerator) Poll(ctx context.Context, operation GeneratorOperation) (GeneratorStatus, error) {
	p.mutex.Lock()
	p.pollCalls++
	if len(p.pollErrors) > 0 {
		err := p.pollErrors[0]
		p.pollErrors = p.pollErrors[1:]
		p.mutex.Unlock()
		return GeneratorStatus{}, err
	}
	p.mutex.Unlock()
	if p.release != nil {
		select {
		case <-ctx.Done():
			return GeneratorStatus{}, ctx.Err()
		case <-p.release:
		}
	}
	p.mutex.Lock()
	p.active--
	p.mutex.Unlock()
	status := p.status
	if status.State == "" {
		status = GeneratorStatus{State: GeneratorSucceeded, Stage: "complete", Outputs: []GeneratorOutput{{SourcePath: filepath.Join(p.outputRoot, operation.ID+".png")}}}
	}
	return status, nil
}
func (p *fakeGenerator) Stop(context.Context, GeneratorOperation) error {
	p.mutex.Lock()
	p.stopCalls++
	p.mutex.Unlock()
	return nil
}

type fakeMediaFiles struct{}

func (*fakeMediaFiles) Prepare(_ context.Context, request projectapp.PrepareMediaRequest) (projectapp.PreparedMedia, error) {
	hash := sha256.Sum256([]byte(request.SourcePath))
	width, height := 320, 180
	if request.RequiredWidth > 0 && request.RequiredHeight > 0 {
		width, height = request.RequiredWidth, request.RequiredHeight
	}
	return projectapp.PreparedMedia{Token: "prepared.png", Kind: request.Kind, Extension: ".png", SHA256: hex.EncodeToString(hash[:]), ByteSize: 10, MIMEType: "image/png", OriginalFilename: filepath.Base(request.SourcePath), Width: width, Height: height}, nil
}
func (f *fakeMediaFiles) PrepareVideoFrame(ctx context.Context, request projectapp.PrepareVideoFrameRequest) (projectapp.PreparedMedia, error) {
	return f.Prepare(ctx, projectapp.PrepareMediaRequest{SourcePath: request.SourceRelativePath, Kind: domain.MediaKindImage})
}
func (*fakeMediaFiles) Install(_ context.Context, _ string, prepared projectapp.PreparedMedia, id domain.ArtifactID) (projectapp.InstalledMedia, error) {
	return projectapp.InstalledMedia{RelativePath: "media/images/" + id.String() + prepared.Extension}, nil
}
func (*fakeMediaFiles) Discard(string, projectapp.PreparedMedia) error { return nil }
func (*fakeMediaFiles) Remove(string, projectapp.InstalledMedia) error { return nil }

func waitForTerminalJobs(t *testing.T, store projectapp.ProjectStore, jobs []domain.GenerationJob) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allTerminal := true
		for _, expected := range jobs {
			job, err := store.GenerationJob(context.Background(), expected.ID)
			if err != nil {
				t.Fatal(err)
			}
			allTerminal = allTerminal && job.State.IsTerminal()
		}
		if allTerminal {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Jobs did not reach terminal states")
}

func mustProjectID(t *testing.T, suffix string) domain.ProjectID {
	t.Helper()
	id, err := domain.ParseProjectID("prj_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func mustEpisodeID(t *testing.T, suffix string) domain.EpisodeID {
	t.Helper()
	id, err := domain.ParseEpisodeID("ep_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func mustBatchID(t *testing.T, suffix string) domain.GenerationBatchID {
	t.Helper()
	id, err := domain.ParseGenerationBatchID("gbt_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
