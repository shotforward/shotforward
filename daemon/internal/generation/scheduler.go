package generation

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	defaultPollInterval  = 2 * time.Second
	maximumRetryInterval = time.Minute
)

type SchedulerOptions struct {
	Registry          *projectapp.ProjectStoreRegistry
	Generators        GeneratorRegistry
	MediaFiles        projectapp.MediaFileProcessor
	IDs               projectapp.IDGenerator
	Clock             projectapp.Clock
	GlobalConcurrency int
	PollInterval      time.Duration
	Events            projectapp.GenerationEventPublisher
}

type Scheduler struct {
	registry     *projectapp.ProjectStoreRegistry
	generators   GeneratorRegistry
	mediaFiles   projectapp.MediaFileProcessor
	ids          projectapp.IDGenerator
	clock        projectapp.Clock
	globalLimit  int
	pollInterval time.Duration
	events       projectapp.GenerationEventPublisher
	wake         chan struct{}

	mutex           sync.Mutex
	active          map[string]struct{}
	generatorActive map[domain.GeneratorID]int
	waitGroup       sync.WaitGroup
}

func NewScheduler(options SchedulerOptions) (*Scheduler, error) {
	if options.Registry == nil || options.Generators == nil || options.MediaFiles == nil || options.IDs == nil || options.Clock == nil || options.GlobalConcurrency < 1 || options.GlobalConcurrency > 1_000 {
		return nil, errors.New("create Generation scheduler: invalid dependencies or concurrency")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	return &Scheduler{registry: options.Registry, generators: options.Generators, mediaFiles: options.MediaFiles, ids: options.IDs, clock: options.Clock, globalLimit: options.GlobalConcurrency, pollInterval: options.PollInterval, events: options.Events, wake: make(chan struct{}, 1), active: map[string]struct{}{}, generatorActive: map[domain.GeneratorID]int{}}, nil
}

func (s *Scheduler) Notify(domain.ProjectID) {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run Generation scheduler: context is required")
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			s.waitGroup.Wait()
			return nil
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// ProcessNext executes the globally oldest recoverable or queued Job and is
// useful for deterministic host startup and integration tests.
func (s *Scheduler) ProcessNext(ctx context.Context) (bool, error) {
	candidates, err := s.candidates(ctx)
	if err != nil || len(candidates) == 0 {
		return false, err
	}
	return true, s.process(ctx, candidates[0].handle, candidates[0].job)
}

type scheduledCandidate struct {
	handle projectapp.ProjectHandle
	job    domain.GenerationJob
}

func (s *Scheduler) dispatch(ctx context.Context) {
	candidates, err := s.candidates(ctx)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		generator, ok := s.generators.Generator(candidate.job.Snapshot.GeneratorID)
		generatorLimit := 1
		if ok && generator.ConcurrencyLimit() > 0 {
			generatorLimit = generator.ConcurrencyLimit()
		}
		key := candidate.job.ProjectID.String() + "/" + candidate.job.ID.String()
		s.mutex.Lock()
		if len(s.active) >= s.globalLimit {
			s.mutex.Unlock()
			return
		}
		if _, exists := s.active[key]; exists || s.generatorActive[candidate.job.Snapshot.GeneratorID] >= generatorLimit {
			s.mutex.Unlock()
			continue
		}
		s.active[key] = struct{}{}
		s.generatorActive[candidate.job.Snapshot.GeneratorID]++
		s.waitGroup.Add(1)
		s.mutex.Unlock()
		go func(candidate scheduledCandidate, key string) {
			defer s.release(candidate.job.Snapshot.GeneratorID, key)
			_ = s.process(ctx, candidate.handle, candidate.job)
		}(candidate, key)
	}
}

func (s *Scheduler) release(generatorID domain.GeneratorID, key string) {
	s.mutex.Lock()
	delete(s.active, key)
	s.generatorActive[generatorID]--
	s.mutex.Unlock()
	s.waitGroup.Done()
	s.Notify("")
}

func (s *Scheduler) candidates(ctx context.Context) ([]scheduledCandidate, error) {
	result := []scheduledCandidate{}
	for _, handle := range s.registry.Handles() {
		jobs, err := handle.Store.GenerationJobsByStates(ctx, []domain.GenerationJobState{domain.GenerationJobRunning, domain.GenerationJobStopRequested, domain.GenerationJobQueued}, 10_000)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			result = append(result, scheduledCandidate{handle: handle, job: job})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].job.CreatedAt.Equal(result[j].job.CreatedAt) {
			return result[i].job.ID.String() < result[j].job.ID.String()
		}
		return result[i].job.CreatedAt.Before(result[j].job.CreatedAt)
	})
	return result, nil
}

func (s *Scheduler) process(ctx context.Context, handle projectapp.ProjectHandle, job domain.GenerationJob) error {
	claimedByThisProcess := false
	if job.State == domain.GenerationJobQueued {
		at, err := s.now()
		if err != nil {
			return err
		}
		job, err = handle.Store.ClaimGenerationJob(ctx, job.ID, job.Revision, at)
		if err != nil {
			if errors.Is(err, projectapp.ErrGenerationJobStateConflict) {
				return nil
			}
			return err
		}
		s.publish(job)
		claimedByThisProcess = true
	}
	generator, ok := s.generators.Generator(job.Snapshot.GeneratorID)
	if !ok || generator.ID() != job.Snapshot.GeneratorID {
		return s.fail(ctx, handle.Store, job, "generator_unavailable", ErrGeneratorNotFound)
	}
	if job.GeneratorOperationID != "" && !generator.SupportsRecovery(job.Snapshot) {
		return s.fail(ctx, handle.Store, job, "execution_interrupted", errors.New("Generator operation cannot be recovered after restart"))
	}
	if job.GeneratorOperationID == "" && !claimedByThisProcess {
		return s.fail(ctx, handle.Store, job, "execution_interrupted", errors.New("Generator submission was interrupted before its operation identity was recorded"))
	}
	operation := GeneratorOperation{ID: job.GeneratorOperationID}
	if operation.ID == "" {
		request, cleanup, requestErr := s.generatorRequest(ctx, handle, job)
		if requestErr != nil {
			return s.fail(ctx, handle.Store, job, "reference_unavailable", requestErr)
		}
		startedOperation, startErr := generator.Start(ctx, request)
		cleanup()
		if startErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return s.fail(ctx, handle.Store, job, "generator_start_failed", startErr)
		}
		operation = startedOperation
		if err := operation.Validate(); err != nil {
			return s.fail(ctx, handle.Store, job, "generator_protocol_error", err)
		}
		at, err := s.now()
		if err != nil {
			return err
		}
		recorded, err := handle.Store.RecordGenerationOperation(ctx, job.ID, job.Revision, operation.ID, "generating", "", at)
		if err != nil {
			if !errors.Is(err, projectapp.ErrGenerationJobStateConflict) {
				return err
			}
			current, readErr := handle.Store.GenerationJob(ctx, job.ID)
			if readErr != nil {
				return readErr
			}
			if current.State != domain.GenerationJobStopRequested || current.GeneratorOperationID != "" {
				return err
			}
			recorded, err = handle.Store.RecordGenerationOperation(ctx, current.ID, current.Revision, operation.ID, "stop_requested", "", at)
			if err != nil {
				return err
			}
		}
		job = recorded
		s.publish(job)
	}
	stopSent := false
	retryInterval := time.Duration(0)
	pollInterval := generatorPollInterval(generator, s.pollInterval)
	for {
		current, err := handle.Store.GenerationJob(ctx, job.ID)
		if err != nil {
			return err
		}
		job = current
		if job.State.IsTerminal() {
			return nil
		}
		if job.State == domain.GenerationJobStopRequested && !stopSent {
			if err := generator.Stop(ctx, operation); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return s.fail(ctx, handle.Store, job, "generator_stop_failed", err)
			}
			stopSent = true
		}
		status, err := generator.Poll(ctx, operation)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if IsRetryablePollError(err) {
				retryInterval = nextRetryInterval(retryInterval, s.pollInterval)
				if !waitContext(ctx, retryInterval) {
					return nil
				}
				continue
			}
			return s.fail(ctx, handle.Store, job, "generator_poll_failed", err)
		}
		retryInterval = 0
		if err := status.Validate(); err != nil {
			return s.fail(ctx, handle.Store, job, "generator_protocol_error", err)
		}
		switch status.State {
		case GeneratorRunning:
			if status.Stage != job.ProgressStage || status.Message != job.ProgressMessage {
				at, err := s.now()
				if err != nil {
					return err
				}
				updated, updateErr := handle.Store.UpdateGenerationProgress(ctx, job.ID, job.Revision, status.Stage, status.Message, at)
				if updateErr != nil && !errors.Is(updateErr, projectapp.ErrGenerationJobStateConflict) {
					return updateErr
				}
				if updateErr == nil {
					job = updated
					s.publish(job)
				}
			}
			if !waitContext(ctx, pollInterval) {
				return nil
			}
		case GeneratorFailed:
			return s.finish(ctx, handle.Store, job, domain.GenerationJobFailed, status.FailureCode, status.FailureMessage)
		case GeneratorStopped:
			return s.finish(ctx, handle.Store, job, domain.GenerationJobStopped, "", "")
		case GeneratorSucceeded:
			return s.complete(ctx, handle, job, status)
		}
	}
}

func generatorPollInterval(generator Generator, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = defaultPollInterval
	}
	configured, ok := generator.(PollIntervalGenerator)
	if !ok {
		return fallback
	}
	interval := configured.PollInterval()
	if interval < fallback {
		return fallback
	}
	return interval
}

func nextRetryInterval(current, base time.Duration) time.Duration {
	if base <= 0 {
		base = defaultPollInterval
	}
	if current <= 0 {
		if base > maximumRetryInterval {
			return maximumRetryInterval
		}
		return base
	}
	if current >= maximumRetryInterval/2 {
		return maximumRetryInterval
	}
	return current * 2
}

func (s *Scheduler) generatorRequest(ctx context.Context, handle projectapp.ProjectHandle, job domain.GenerationJob) (GeneratorRequest, func(), error) {
	references := make([]GeneratorArtifactReference, 0, len(job.Snapshot.References))
	prepared := make([]projectapp.PreparedMedia, 0)
	cleanup := func() {
		for _, item := range prepared {
			_ = s.mediaFiles.Discard(handle.RootPath, item)
		}
	}
	snapshot := job.Snapshot
	snapshot.References = append([]domain.GenerationArtifactReference(nil), job.Snapshot.References...)
	for _, reference := range job.Snapshot.References {
		artifact, err := handle.Store.Artifact(ctx, reference.ArtifactID)
		if err != nil {
			cleanup()
			return GeneratorRequest{}, func() {}, err
		}
		absolutePath := filepath.Join(handle.RootPath, filepath.FromSlash(artifact.RelativePath))
		if reference.PreparationRequired {
			prepareRequest := projectapp.PrepareMediaRequest{ProjectRoot: handle.RootPath, SourcePath: absolutePath, OriginalFilename: artifact.OriginalFilename, Kind: artifact.Kind}
			if artifact.Kind == domain.MediaKindImage {
				prepareRequest.RequiredWidth = snapshot.OutputWidth
				prepareRequest.RequiredHeight = snapshot.OutputHeight
			} else {
				prepareRequest.TrimStartMilliseconds = reference.TrimStartMilliseconds
				prepareRequest.TrimEndMilliseconds = reference.TrimEndMilliseconds
			}
			item, prepareErr := s.mediaFiles.Prepare(ctx, prepareRequest)
			if prepareErr != nil {
				cleanup()
				return GeneratorRequest{}, func() {}, prepareErr
			}
			prepared = append(prepared, item)
			artifact.SHA256, artifact.ByteSize, artifact.MIMEType = item.SHA256, item.ByteSize, item.MIMEType
			artifact.OriginalFilename = item.OriginalFilename
			artifact.Width, artifact.Height, artifact.DurationMilliseconds = item.Width, item.Height, item.DurationMilliseconds
			artifact.FPSNumerator, artifact.FPSDenominator = item.FPSNumerator, item.FPSDenominator
			artifact.AudioSampleRate, artifact.AudioChannels = item.AudioSampleRate, item.AudioChannels
			artifact.RemoteReferenceURL, artifact.RemoteReferenceExpiresAt = "", nil
			absolutePath = item.AbsolutePath
			reference.PreparationRequired = false
			if artifact.Kind != domain.MediaKindImage {
				reference.TrimStartMilliseconds = 0
				reference.TrimEndMilliseconds = artifact.DurationMilliseconds
			}
			snapshot.References[len(references)] = reference
		}
		references = append(references, GeneratorArtifactReference{Input: reference, Artifact: artifact, AbsolutePath: absolutePath})
	}
	request := GeneratorRequest{JobID: job.ID, Snapshot: snapshot, ProjectRoot: handle.RootPath, References: references}
	if err := request.Validate(); err != nil {
		cleanup()
		return GeneratorRequest{}, func() {}, err
	}
	return request, cleanup, nil
}

func (s *Scheduler) complete(ctx context.Context, handle projectapp.ProjectHandle, job domain.GenerationJob, status GeneratorStatus) error {
	slot, err := handle.Store.MediaSlot(ctx, job.TargetSlotID, true)
	if err != nil {
		return s.fail(ctx, handle.Store, job, "target_slot_unavailable", err)
	}
	request := projectapp.PrepareMediaRequest{ProjectRoot: handle.RootPath, SourcePath: status.Outputs[0].SourcePath, Kind: job.Snapshot.TargetMediaKind}
	if slot.Slot.Kind.MediaKind() == domain.MediaKindImage {
		request.RequiredWidth = job.Snapshot.OutputWidth
		request.RequiredHeight = job.Snapshot.OutputHeight
		request.CropToFill = true
	}
	prepared, err := s.mediaFiles.Prepare(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return s.fail(ctx, handle.Store, job, "invalid_generator_output", err)
	}
	discard := true
	defer func() {
		if discard {
			_ = s.mediaFiles.Discard(handle.RootPath, prepared)
		}
	}()
	artifact, err := handle.Store.ArtifactBySHA256(ctx, job.ProjectID, prepared.SHA256)
	installed := projectapp.InstalledMedia{}
	if errors.Is(err, projectapp.ErrArtifactNotFound) {
		artifactID, idErr := s.ids.NewArtifactID()
		if idErr != nil {
			return s.fail(ctx, handle.Store, job, "id_generation_failed", idErr)
		}
		installed, err = s.mediaFiles.Install(ctx, handle.RootPath, prepared, artifactID)
		if err != nil {
			return s.fail(ctx, handle.Store, job, "media_install_failed", err)
		}
		discard = false
		at, timeErr := s.now()
		if timeErr != nil {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
			return timeErr
		}
		artifact = generatedArtifact(artifactID, job.ProjectID, installed.RelativePath, prepared, at)
	} else if err != nil {
		return s.fail(ctx, handle.Store, job, "artifact_lookup_failed", err)
	} else {
		if err := s.mediaFiles.Discard(handle.RootPath, prepared); err != nil {
			return s.fail(ctx, handle.Store, job, "staging_cleanup_failed", err)
		}
		discard = false
	}
	artifact.RemoteReferenceURL = status.Outputs[0].RemoteReferenceURL
	artifact.RemoteReferenceExpiresAt = status.Outputs[0].RemoteReferenceExpiresAt
	versionID, err := s.ids.NewMediaVersionID()
	if err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return s.fail(ctx, handle.Store, job, "id_generation_failed", err)
	}
	at, err := s.now()
	if err != nil {
		if installed.RelativePath != "" {
			_ = s.mediaFiles.Remove(handle.RootPath, installed)
		}
		return err
	}
	err = handle.Store.CompleteGenerationJob(ctx, job.ID, job.Revision, artifact, versionID, at)
	if err != nil {
		committed, readErr := handle.Store.GenerationJob(ctx, job.ID)
		if readErr == nil && committed.State == domain.GenerationJobSucceeded {
			s.removeDeduplicatedInstall(handle.RootPath, installed, artifact.ID, committed.ResultArtifactID)
			s.publish(committed)
			return nil
		}
		if errors.Is(err, projectapp.ErrGenerationJobStateConflict) && readErr == nil && committed.State == domain.GenerationJobStopRequested {
			err = handle.Store.CompleteGenerationJob(ctx, committed.ID, committed.Revision, artifact, versionID, at)
			if err == nil {
				committed, readErr = handle.Store.GenerationJob(ctx, committed.ID)
				if readErr == nil {
					s.removeDeduplicatedInstall(handle.RootPath, installed, artifact.ID, committed.ResultArtifactID)
					s.publish(committed)
				}
				return readErr
			}
		}
		return s.fail(ctx, handle.Store, job, "result_commit_failed", err)
	}
	if committed, readErr := handle.Store.GenerationJob(ctx, job.ID); readErr == nil {
		s.removeDeduplicatedInstall(handle.RootPath, installed, artifact.ID, committed.ResultArtifactID)
		s.publish(committed)
	}
	return nil
}

func (s *Scheduler) removeDeduplicatedInstall(projectRoot string, installed projectapp.InstalledMedia, proposedID, committedID domain.ArtifactID) {
	if installed.RelativePath != "" && committedID != "" && committedID != proposedID {
		_ = s.mediaFiles.Remove(projectRoot, installed)
	}
}

func generatedArtifact(id domain.ArtifactID, projectID domain.ProjectID, relativePath string, prepared projectapp.PreparedMedia, at time.Time) domain.Artifact {
	return domain.Artifact{ID: id, ProjectID: projectID, Kind: prepared.Kind, RelativePath: relativePath, SHA256: prepared.SHA256, ByteSize: prepared.ByteSize, MIMEType: prepared.MIMEType, OriginalFilename: prepared.OriginalFilename, Width: prepared.Width, Height: prepared.Height, DurationMilliseconds: prepared.DurationMilliseconds, FPSNumerator: prepared.FPSNumerator, FPSDenominator: prepared.FPSDenominator, AudioSampleRate: prepared.AudioSampleRate, AudioChannels: prepared.AudioChannels, CreatedAt: at}
}

func (s *Scheduler) fail(ctx context.Context, store projectapp.ProjectStore, job domain.GenerationJob, code string, cause error) error {
	if ctx.Err() != nil {
		return nil
	}
	failureCode, message := generatorError(code, cause)
	return s.finish(ctx, store, job, domain.GenerationJobFailed, failureCode, message)
}

func (s *Scheduler) finish(ctx context.Context, store projectapp.ProjectStore, job domain.GenerationJob, state domain.GenerationJobState, code, message string) error {
	current, err := store.GenerationJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if current.State.IsTerminal() {
		return nil
	}
	at, err := s.now()
	if err != nil {
		return err
	}
	finished, err := store.FinishGenerationJob(ctx, current.ID, current.Revision, state, code, message, at)
	if err == nil {
		s.publish(finished)
	}
	return err
}

func (s *Scheduler) publish(job domain.GenerationJob) {
	if s.events != nil {
		s.events.PublishGenerationJob(job)
	}
}

func (s *Scheduler) now() (time.Time, error) {
	value := s.clock.Now()
	if value.IsZero() || value.Location() != time.UTC || value.Nanosecond()%int(time.Millisecond) != 0 || value.UnixMilli() < 0 {
		return time.Time{}, errors.New("Generation scheduler Clock returned an invalid timestamp")
	}
	return value, nil
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
