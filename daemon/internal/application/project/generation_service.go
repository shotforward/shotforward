package projectapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const maximumGenerationBatchJobs = 1_000

const generationPromptPreparationTimeout = 90 * time.Second
const maximumH3PromptCharacters = 7_000

func (s *Service) SubmitGenerationBatch(ctx context.Context, input SubmitGenerationBatchInput) (domain.GenerationBatchDetails, error) {
	return s.submitGenerationBatch(ctx, input, false)
}

// Only RetryGenerationJob may preserve a prior snapshot's reference ranges,
// after it has read and validated the original Job. A client-supplied retry ID
// never enables this trusted path.
func (s *Service) submitGenerationBatch(ctx context.Context, input SubmitGenerationBatchInput, frozenRetry bool) (domain.GenerationBatchDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "submit GenerationBatch", input.ProjectID)
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	if err := validateGenerationSubmissionIdentity(input); err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrInvalidRequest, err)
	}
	requestHash, err := hashGenerationRequest(input)
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrInvalidRequest, err)
	}
	release, err := s.acquireGenerationSubmission(ctx, generationSubmissionIdentity{input.ProjectID, input.OperationKey})
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	defer release()
	if existing, err := handle.Store.GenerationBatchByOperationKey(ctx, input.ProjectID, input.OperationKey); err == nil {
		if existing.Batch.RequestHash != requestHash {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", ErrGenerationOperationConflict)
		}
		return existing, nil
	} else if !errors.Is(err, ErrGenerationBatchNotFound) {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
	}
	if s.generationCatalog == nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", ErrGenerationGeneratorNotFound)
	}
	if len(input.Targets) == 0 || len(input.Targets) > maximumGenerationBatchJobs {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: invalid target count", ErrInvalidRequest)
	}
	episode, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID)
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
	}
	project, err := handle.Store.Project(ctx)
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
	}
	batchID, err := s.ids.NewGenerationBatchID()
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
	}
	batch := domain.GenerationBatch{ID: batchID, ProjectID: input.ProjectID, EpisodeID: episode.ID, OperationKey: input.OperationKey, RequestHash: requestHash, CreatedAt: now}
	if err := batch.Validate(); err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrInvalidRequest, err)
	}
	jobs := make([]domain.GenerationJob, 0, len(input.Targets))
	seenSlots := map[domain.MediaSlotID]struct{}{}
	for _, target := range input.Targets {
		if target.RetryOfJobID != "" && !frozenRetry {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: retry must use RetryGenerationJob", ErrInvalidRequest)
		}
		if _, exists := seenSlots[target.MediaSlotID]; exists {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: duplicate target Slot", ErrInvalidRequest)
		}
		seenSlots[target.MediaSlotID] = struct{}{}
		_, snapshot, capability, err := s.prepareGenerationTarget(ctx, handle.Store, project, input.EpisodeID, target, !frozenRetry)
		if err != nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
		}
		jobID, err := s.ids.NewGenerationJobID()
		if err != nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrProjectWriteFailed, err)
		}
		job := domain.GenerationJob{ID: jobID, BatchID: batch.ID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, TargetSlotID: target.MediaSlotID, RetryOfJobID: target.RetryOfJobID, State: domain.GenerationJobQueued, Snapshot: snapshot, StopSupported: capability.SupportsStop, ProgressStage: "queued", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := job.Validate(); err != nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w: %w", ErrInvalidRequest, err)
		}
		jobs = append(jobs, job)
	}
	// Validate the entire batch before spending any Agent work. Readiness uses
	// prepareGenerationTarget too, so translation deliberately lives only here.
	preparationCtx, cancel := context.WithTimeout(ctx, generationPromptPreparationTimeout)
	defer cancel()
	for index := range jobs {
		job := &jobs[index]
		if job.RetryOfJobID != "" || !requiresH3PromptTranslation(job.Snapshot) {
			continue
		}
		if s.promptTranslator == nil || input.PromptTranslation == nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", ErrPromptTranslationUnavailable)
		}
		prompt, err := s.promptTranslator.Translate(preparationCtx, *input.PromptTranslation, job.Snapshot.Prompt)
		if errors.Is(err, context.DeadlineExceeded) {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", ErrPromptTranslationTimeout)
		}
		if err != nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
		}
		job.Snapshot.Prompt = prompt
		if strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) > maximumH3PromptCharacters || job.Validate() != nil {
			return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", ErrPromptTranslationInvalid)
		}
	}
	if err := preparationCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = ErrPromptTranslationTimeout
		}
		return domain.GenerationBatchDetails{}, err
	}
	if err := handle.Store.CreateGenerationBatch(ctx, batch, jobs, now); err != nil {
		if errors.Is(err, ErrGenerationOperationConflict) {
			existing, readErr := handle.Store.GenerationBatchByOperationKey(ctx, input.ProjectID, input.OperationKey)
			if readErr == nil && existing.Batch.RequestHash == requestHash {
				return existing, nil
			}
		}
		return domain.GenerationBatchDetails{}, fmt.Errorf("submit GenerationBatch: %w", err)
	}
	for _, job := range jobs {
		s.publishGenerationJob(job)
	}
	if s.generationQueue != nil {
		s.generationQueue.Notify(input.ProjectID)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	return handle.Store.GenerationBatch(ctx, batch.ID)
}

func requiresH3PromptTranslation(snapshot domain.GenerationInputSnapshot) bool {
	if snapshot.TargetMediaKind != domain.MediaKindVideo {
		return false
	}
	switch snapshot.GeneratorID {
	case domain.GeneratorVideoH3China, domain.GeneratorVideoH3Official, domain.GeneratorVideoH3ShotForward:
		return true
	default:
		return false
	}
}

// CheckGenerationReadiness applies the exact same target, reference, and
// capability rules used by SubmitGenerationBatch without creating a Job.
func (s *Service) CheckGenerationReadiness(ctx context.Context, input CheckGenerationReadinessInput) (GenerationReadinessResult, error) {
	handle, err := s.assetProjectHandle(ctx, "check Generation readiness", input.ProjectID)
	if err != nil {
		return GenerationReadinessResult{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return GenerationReadinessResult{}, fmt.Errorf("check Generation readiness: %w", err)
	}
	project, err := handle.Store.Project(ctx)
	if err != nil {
		return GenerationReadinessResult{}, fmt.Errorf("check Generation readiness: %w", err)
	}
	if s.generationCatalog == nil {
		return GenerationReadinessResult{}, fmt.Errorf("check Generation readiness: %w", ErrGenerationGeneratorNotFound)
	}
	details, snapshot, _, err := s.prepareGenerationTarget(ctx, handle.Store, project, input.EpisodeID, input.Target, true)
	if err != nil {
		return GenerationReadinessResult{}, fmt.Errorf("check Generation readiness: %w", err)
	}
	return GenerationReadinessResult{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Slot: details.Slot, Snapshot: snapshot}, nil
}

func (s *Service) GetGenerationBatch(ctx context.Context, input GetGenerationBatchInput) (domain.GenerationBatchDetails, error) {
	handle, err := s.assetProjectHandle(ctx, "get GenerationBatch", input.ProjectID)
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	value, err := handle.Store.GenerationBatch(ctx, input.GenerationBatchID)
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("get GenerationBatch: %w", err)
	}
	if err := value.Validate(); err != nil || value.Batch.ProjectID != input.ProjectID {
		return domain.GenerationBatchDetails{}, fmt.Errorf("get GenerationBatch: %w", ErrProjectDataInvalid)
	}
	return value, nil
}

func (s *Service) GetGenerationJob(ctx context.Context, input GetGenerationJobInput) (domain.GenerationJob, error) {
	handle, err := s.assetProjectHandle(ctx, "get GenerationJob", input.ProjectID)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	value, err := handle.Store.GenerationJob(ctx, input.GenerationJobID)
	if err != nil {
		return domain.GenerationJob{}, fmt.Errorf("get GenerationJob: %w", err)
	}
	if err := value.Validate(); err != nil || value.ProjectID != input.ProjectID {
		return domain.GenerationJob{}, fmt.Errorf("get GenerationJob: %w", ErrProjectDataInvalid)
	}
	return value, nil
}

func (s *Service) CancelGenerationJob(ctx context.Context, input ChangeGenerationJobInput) (domain.GenerationJob, error) {
	handle, current, err := s.mutableGenerationJob(ctx, "cancel GenerationJob", input)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.GenerationJob{}, fmt.Errorf("cancel GenerationJob: %w", err)
	}
	value, err := handle.Store.CancelGenerationJob(ctx, current.ID, input.ExpectedRevision, now)
	if err != nil {
		return domain.GenerationJob{}, fmt.Errorf("cancel GenerationJob: %w", err)
	}
	s.publishGenerationJob(value)
	if s.generationQueue != nil {
		s.generationQueue.Notify(input.ProjectID)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.GenerationJob{}, err
	}
	return value, nil
}

func (s *Service) RequestGenerationStop(ctx context.Context, input ChangeGenerationJobInput) (domain.GenerationJob, error) {
	handle, current, err := s.mutableGenerationJob(ctx, "request Generation stop", input)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.GenerationJob{}, fmt.Errorf("request Generation stop: %w", err)
	}
	value, err := handle.Store.RequestGenerationStop(ctx, current.ID, input.ExpectedRevision, now)
	if err != nil {
		return domain.GenerationJob{}, fmt.Errorf("request Generation stop: %w", err)
	}
	s.publishGenerationJob(value)
	if s.generationQueue != nil {
		s.generationQueue.Notify(input.ProjectID)
	}
	if err := s.syncLibraryAfterMutation(ctx, handle, now); err != nil {
		return domain.GenerationJob{}, err
	}
	return value, nil
}

// RetryGenerationJob creates a new one-Job Batch from a terminal Job's frozen
// semantic inputs. The current target Slot revision is captured so late output
// can never silently replace a newer user selection.
func (s *Service) RetryGenerationJob(ctx context.Context, input RetryGenerationJobInput) (domain.GenerationBatchDetails, error) {
	if err := validateGenerationOperationKey(input.OperationKey); err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("retry GenerationJob: %w: %w", ErrInvalidRequest, err)
	}
	handle, err := s.assetProjectHandle(ctx, "retry GenerationJob", input.ProjectID)
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	if existing, err := handle.Store.GenerationBatchByOperationKey(ctx, input.ProjectID, input.OperationKey); err == nil {
		if len(existing.Jobs) == 1 && existing.Jobs[0].RetryOfJobID == input.GenerationJobID {
			return existing, nil
		}
		return domain.GenerationBatchDetails{}, fmt.Errorf("retry GenerationJob: %w", ErrGenerationOperationConflict)
	} else if !errors.Is(err, ErrGenerationBatchNotFound) {
		return domain.GenerationBatchDetails{}, fmt.Errorf("retry GenerationJob: %w", err)
	}
	_, current, err := s.mutableGenerationJob(ctx, "retry GenerationJob", ChangeGenerationJobInput{ProjectID: input.ProjectID, GenerationJobID: input.GenerationJobID, ExpectedRevision: input.ExpectedRevision})
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	if !current.State.IsTerminal() {
		return domain.GenerationBatchDetails{}, fmt.Errorf("retry GenerationJob: %w", ErrGenerationJobStateConflict)
	}
	slot, err := mediaSlotForProject(ctx, handle.Store, input.ProjectID, current.TargetSlotID, true)
	if err != nil {
		return domain.GenerationBatchDetails{}, fmt.Errorf("retry GenerationJob: %w", err)
	}
	target := SubmitGenerationTarget{
		MediaSlotID: current.TargetSlotID, ExpectedSlotRevision: slot.Slot.Revision,
		Prompt: current.Snapshot.Prompt, GenerationMode: current.Snapshot.GenerationMode,
		GeneratorID: current.Snapshot.GeneratorID, ProviderModelID: current.Snapshot.ProviderModelID,
		RequestedDurationSeconds: current.Snapshot.RequestedDurationSeconds,
		References:               slices.Clone(current.Snapshot.References), ParametersJSON: current.Snapshot.ParametersJSON,
		RetryOfJobID: current.ID,
	}
	return s.submitGenerationBatch(ctx, SubmitGenerationBatchInput{ProjectID: input.ProjectID, EpisodeID: current.EpisodeID, OperationKey: input.OperationKey, Targets: []SubmitGenerationTarget{target}}, true)
}

func (s *Service) CancelGenerationBatch(ctx context.Context, input CancelGenerationBatchInput) (domain.GenerationBatchDetails, error) {
	details, err := s.GetGenerationBatch(ctx, GetGenerationBatchInput{ProjectID: input.ProjectID, GenerationBatchID: input.GenerationBatchID})
	if err != nil {
		return domain.GenerationBatchDetails{}, err
	}
	for _, job := range details.Jobs {
		switch job.State {
		case domain.GenerationJobQueued:
			_, err = s.CancelGenerationJob(ctx, ChangeGenerationJobInput{ProjectID: input.ProjectID, GenerationJobID: job.ID, ExpectedRevision: job.Revision})
		case domain.GenerationJobRunning:
			if input.RequestStopForRunningJobs && job.StopSupported {
				_, err = s.RequestGenerationStop(ctx, ChangeGenerationJobInput{ProjectID: input.ProjectID, GenerationJobID: job.ID, ExpectedRevision: job.Revision})
			}
		}
		if err != nil && !errors.Is(err, ErrGenerationJobStateConflict) {
			return domain.GenerationBatchDetails{}, err
		}
	}
	return s.GetGenerationBatch(ctx, GetGenerationBatchInput{ProjectID: input.ProjectID, GenerationBatchID: input.GenerationBatchID})
}

func (s *Service) mutableGenerationJob(ctx context.Context, operation string, input ChangeGenerationJobInput) (ProjectHandle, domain.GenerationJob, error) {
	handle, err := s.assetProjectHandle(ctx, operation, input.ProjectID)
	if err != nil {
		return ProjectHandle{}, domain.GenerationJob{}, err
	}
	job, err := handle.Store.GenerationJob(ctx, input.GenerationJobID)
	if err != nil {
		return ProjectHandle{}, domain.GenerationJob{}, fmt.Errorf("%s: %w", operation, err)
	}
	if job.ProjectID != input.ProjectID {
		return ProjectHandle{}, domain.GenerationJob{}, fmt.Errorf("%s: %w", operation, ErrProjectDataInvalid)
	}
	if input.ExpectedRevision < 1 || job.Revision != input.ExpectedRevision {
		return ProjectHandle{}, domain.GenerationJob{}, revisionConflict(operation, input.ExpectedRevision, job.Revision)
	}
	return handle, job, nil
}

func validateGenerationCapability(capability GenerationCapability, snapshot domain.GenerationInputSnapshot) error {
	if capability.GeneratorID.Validate() != nil || capability.GeneratorID != snapshot.GeneratorID || capability.MediaKind != snapshot.TargetMediaKind || strings.TrimSpace(capability.CapabilityVersion) == "" || capability.CapabilityVersion != snapshot.CapabilityVersion || capability.ProviderModelID != snapshot.ProviderModelID || capability.ProviderConnectionRevision != snapshot.ProviderConnectionRevision || capability.ProviderModelID == "" && capability.ProviderConnectionRevision != 0 || capability.ProviderModelID != "" && capability.ProviderConnectionRevision < 1 || capability.MaximumTotalTimedReferenceDurationMilliseconds < 0 || capability.MaximumInputVideoPlusOutputDurationMilliseconds < 0 {
		return errors.New("Generator identity, capability version, or output kind does not match")
	}
	if snapshot.TargetMediaKind == domain.MediaKindVideo {
		if len(capability.SupportedDurations) > 0 && (capability.MinimumDurationSeconds != 0 || capability.MaximumDurationSeconds != 0 || capability.DurationStepSeconds != 0) || len(capability.SupportedDurations) == 0 && (capability.MinimumDurationSeconds < 1 || capability.MaximumDurationSeconds < capability.MinimumDurationSeconds || capability.DurationStepSeconds < 0) {
			return errors.New("Generator has an invalid output-duration capability")
		}
		durationSupported := slices.Contains(capability.SupportedDurations, snapshot.RequestedDurationSeconds)
		if len(capability.SupportedDurations) == 0 {
			durationSupported = capability.MinimumDurationSeconds > 0 && capability.MaximumDurationSeconds >= capability.MinimumDurationSeconds && snapshot.RequestedDurationSeconds >= capability.MinimumDurationSeconds && snapshot.RequestedDurationSeconds <= capability.MaximumDurationSeconds
		}
		if capability.DurationStepSeconds > 0 && (snapshot.RequestedDurationSeconds-capability.MinimumDurationSeconds)%capability.DurationStepSeconds != 0 {
			durationSupported = false
		}
		if !slices.Contains(capability.SupportedVideoModes, snapshot.GenerationMode) || !durationSupported {
			return errors.New("video mode or exact duration is unsupported")
		}
		if len(capability.SupportedOutputSizes) > 0 && !slices.Contains(capability.SupportedOutputSizes, fmt.Sprintf("%dx%d", snapshot.OutputWidth, snapshot.OutputHeight)) {
			return errors.New("video output size is unsupported")
		}
		if capability.MaximumInputVideoPlusOutputDurationMilliseconds > 0 {
			inputVideoDuration := int64(0)
			for _, reference := range snapshot.References {
				if reference.Role == domain.GenerationReferenceVideo {
					inputVideoDuration += reference.TrimEndMilliseconds - reference.TrimStartMilliseconds
				}
			}
			maximum := capability.MaximumInputVideoPlusOutputDurationMilliseconds
			if inputVideoDuration > maximum || snapshot.RequestedDurationSeconds > (maximum-inputVideoDuration)/1_000 {
				return errors.New("input video and output duration exceed the Generator limit")
			}
		}
	} else if len(capability.SupportedDurations) != 0 || capability.MinimumDurationSeconds != 0 || capability.MaximumDurationSeconds != 0 {
		return errors.New("image model declares video durations")
	}
	return nil
}

func (s *Service) prepareGenerationTarget(ctx context.Context, store ProjectStore, project domain.Project, episodeID domain.EpisodeID, target SubmitGenerationTarget, latestLibrarySelection bool) (domain.MediaSlotDetails, domain.GenerationInputSnapshot, GenerationCapability, error) {
	details, err := mediaSlotForProject(ctx, store, project.ID, target.MediaSlotID, true)
	if err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, err
	}
	if details.Slot.EpisodeID != episodeID {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: target belongs to another Episode", ErrInvalidRequest)
	}
	if err := ensureMediaOwnerActive(ctx, store, details.Slot); err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, err
	}
	if err := validateSlotRevision("prepare Generation target", details.Slot, target.ExpectedSlotRevision); err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, err
	}
	var capability GenerationCapability
	if target.ProviderModelID != "" {
		catalog, ok := s.generationCatalog.(ModelGenerationCapabilityCatalog)
		if !ok {
			return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: model selection is unsupported", ErrGenerationGeneratorNotFound)
		}
		capability, err = catalog.GeneratorModel(ctx, target.GeneratorID, target.ProviderModelID)
	} else {
		capability, err = s.generationCatalog.Generator(ctx, target.GeneratorID)
	}
	if err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: %v", ErrGenerationGeneratorNotFound, err)
	}
	parameters := target.ParametersJSON
	if parameters == "" {
		parameters = "{}"
	}
	references, err := normalizeGenerationReferencesWithSelection(ctx, store, project.ID, episodeID, project.Output.Width(), project.Output.Height(), target.GenerationMode, capability, target.References, latestLibrarySelection)
	if err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: %w", ErrGenerationCapabilityMismatch, err)
	}
	outputWidth, outputHeight := domain.GenerationOutputSize(details.Slot.Kind, project.Output)
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: details.Slot.Kind.MediaKind(), Prompt: target.Prompt, GenerationMode: target.GenerationMode, GeneratorID: target.GeneratorID, ProviderModelID: capability.ProviderModelID, ProviderConnectionRevision: capability.ProviderConnectionRevision, CapabilityVersion: capability.CapabilityVersion, OutputWidth: outputWidth, OutputHeight: outputHeight, RequestedDurationSeconds: target.RequestedDurationSeconds, References: references, ParametersJSON: parameters, TargetSlotRevision: target.ExpectedSlotRevision}
	if err := snapshot.Validate(); err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: %w", ErrGenerationNotReady, err)
	}
	if err := validateGenerationCapability(capability, snapshot); err != nil {
		return domain.MediaSlotDetails{}, domain.GenerationInputSnapshot{}, GenerationCapability{}, fmt.Errorf("%w: %w", ErrGenerationCapabilityMismatch, err)
	}
	return details, snapshot, capability, nil
}

func normalizeGenerationReferences(ctx context.Context, store ProjectStore, projectID domain.ProjectID, episodeID domain.EpisodeID, outputWidth, outputHeight int, generationMode domain.ClipGenerationMode, capability GenerationCapability, references []domain.GenerationArtifactReference) ([]domain.GenerationArtifactReference, error) {
	return normalizeGenerationReferencesWithSelection(ctx, store, projectID, episodeID, outputWidth, outputHeight, generationMode, capability, references, true)
}

func normalizeGenerationReferencesWithSelection(ctx context.Context, store ProjectStore, projectID domain.ProjectID, episodeID domain.EpisodeID, outputWidth, outputHeight int, generationMode domain.ClipGenerationMode, capability GenerationCapability, references []domain.GenerationArtifactReference, latestLibrarySelection bool) ([]domain.GenerationArtifactReference, error) {
	// A submission freezes its own reference ranges. Do not mutate a caller's
	// reusable input slice while resolving the latest Episode library selection.
	references = slices.Clone(references)
	limits := make(map[domain.GenerationArtifactRole]GenerationReferenceCapability, len(capability.ReferenceCapabilities))
	for _, limit := range capability.ReferenceCapabilities {
		if !limit.Role.IsValid() || limit.MinimumCount < 0 || limit.MaximumCount < 1 || limit.MinimumCount > limit.MaximumCount || limit.MaximumBytes < 0 || limit.MinimumDurationMilliseconds < 0 || limit.MaximumDurationMilliseconds < 0 || limit.MaximumTotalDurationMilliseconds < 0 || limit.MaximumDurationMilliseconds > 0 && limit.MinimumDurationMilliseconds > limit.MaximumDurationMilliseconds {
			return nil, errors.New("Generator has an invalid reference capability")
		}
		for _, mode := range limit.GenerationModes {
			if !mode.IsValid() {
				return nil, errors.New("Generator has an invalid reference generation mode")
			}
		}
		for _, role := range limit.RequiresAnyOf {
			if !role.IsValid() {
				return nil, errors.New("Generator has an invalid dependent reference role")
			}
		}
		if _, exists := limits[limit.Role]; exists {
			return nil, errors.New("Generator has duplicate reference capabilities")
		}
		if limit.Role.MediaKind() == domain.MediaKindImage && (limit.MinimumDurationMilliseconds != 0 || limit.MaximumDurationMilliseconds != 0 || limit.MaximumTotalDurationMilliseconds != 0) {
			return nil, errors.New("image reference capability declares duration limits")
		}
		limits[limit.Role] = limit
	}
	counts := map[domain.GenerationArtifactRole]int{}
	totals := map[domain.GenerationArtifactRole]int64{}
	var allTimedTotal int64
	for index := range references {
		reference := &references[index]
		reference.PreparationRequired = false
		limit, supported := limits[reference.Role]
		if !supported || len(limit.GenerationModes) > 0 && !slices.Contains(limit.GenerationModes, generationMode) {
			return nil, fmt.Errorf("reference role %q is unsupported", reference.Role)
		}
		artifact, err := store.Artifact(ctx, reference.ArtifactID)
		if err != nil {
			return nil, err
		}
		if artifact.ProjectID != projectID || artifact.Kind != reference.Role.MediaKind() {
			return nil, errors.New("reference Artifact kind or Project does not match")
		}
		selected, err := store.ArtifactSelectedInEpisode(ctx, artifact.ID, episodeID)
		if err != nil {
			return nil, err
		}
		// General image references are project-library inputs. Unlike timeline
		// endpoints (first/last frames) they do not need to be selected in the
		// target Episode; the immutable Artifact and Project checks above keep the
		// generation snapshot scoped and reproducible.
		if !selected && reference.Role != domain.GenerationReferenceImage {
			return nil, errors.New("reference Artifact is not selected in the target Episode")
		}
		counts[reference.Role]++
		if counts[reference.Role] > limit.MaximumCount {
			return nil, &GenerationCapabilityIssue{Constraint: "reference_count", Role: reference.Role, Actual: int64(counts[reference.Role]), Minimum: int64(limit.MinimumCount), Maximum: int64(limit.MaximumCount)}
		}
		if artifact.Kind == domain.MediaKindImage {
			if reference.TrimStartMilliseconds != 0 || reference.TrimEndMilliseconds != 0 {
				return nil, errors.New("image references cannot have a trim range")
			}
			if reference.Role == domain.GenerationReferenceFirstImage || reference.Role == domain.GenerationReferenceLastImage {
				if int64(artifact.Width)*int64(outputHeight) != int64(artifact.Height)*int64(outputWidth) {
					return nil, ErrMediaAspectRatioMismatch
				}
				if artifact.Width != outputWidth || artifact.Height != outputHeight {
					reference.PreparationRequired = true
				}
			}
		} else {
			// Include tombstones: another active Slot can select the same
			// deduplicated Artifact after its upload-library entry is deleted.
			// That must not revive the source as an untrimmed continuity input.
			libraryMedia, libraryErr := store.EpisodeReferenceMedia(ctx, episodeID, artifact.ID, true)
			if libraryErr == nil {
				if libraryMedia.EpisodeID != episodeID || libraryMedia.Artifact.ID != artifact.ID || libraryMedia.Artifact.ProjectID != projectID || libraryMedia.DeletedAt != nil {
					return nil, errors.New("reference media library entry is not active in the target Episode")
				}
				if latestLibrarySelection {
					// Uploaded media has one selection for the entire Episode. The
					// library is authoritative even when an older UI sends a stale
					// full-file or per-Clip range. Generated continuity references
					// outside the upload library retain their explicit tail range.
					reference.TrimStartMilliseconds, reference.TrimEndMilliseconds = libraryMedia.EffectiveTrimRange()
				}
			} else if !errors.Is(libraryErr, ErrArtifactNotFound) {
				return nil, libraryErr
			}
			if reference.TrimStartMilliseconds == 0 && reference.TrimEndMilliseconds == 0 {
				reference.TrimEndMilliseconds = artifact.DurationMilliseconds
			}
			if reference.TrimStartMilliseconds < 0 || reference.TrimEndMilliseconds <= reference.TrimStartMilliseconds || reference.TrimEndMilliseconds > artifact.DurationMilliseconds {
				return nil, errors.New("reference trim range is outside the Artifact duration")
			}
			duration := reference.TrimEndMilliseconds - reference.TrimStartMilliseconds
			if limit.MinimumDurationMilliseconds > 0 && duration < limit.MinimumDurationMilliseconds || limit.MaximumDurationMilliseconds > 0 && duration > limit.MaximumDurationMilliseconds {
				return nil, &GenerationCapabilityIssue{Constraint: "reference_duration", Role: reference.Role, Actual: duration, Minimum: limit.MinimumDurationMilliseconds, Maximum: limit.MaximumDurationMilliseconds}
			}
			totals[reference.Role] += duration
			allTimedTotal += duration
			if reference.TrimStartMilliseconds != 0 || reference.TrimEndMilliseconds != artifact.DurationMilliseconds {
				reference.PreparationRequired = true
			}
		}
		if limit.MaximumBytes > 0 && artifact.ByteSize > limit.MaximumBytes {
			if !limit.SupportsAutomaticPreparation {
				return nil, fmt.Errorf("reference role %q exceeds its file-size limit", reference.Role)
			}
			reference.PreparationRequired = true
		}
		if len(limit.AcceptedMIMETypes) > 0 && !slices.Contains(limit.AcceptedMIMETypes, artifact.MIMEType) {
			if !limit.SupportsAutomaticPreparation {
				return nil, fmt.Errorf("reference role %q has an unsupported media format", reference.Role)
			}
			reference.PreparationRequired = true
		}
	}
	if capability.MinimumTotalReferences < 0 || capability.MaximumTotalReferences < 0 || capability.MaximumTotalReferences > 0 && capability.MinimumTotalReferences > capability.MaximumTotalReferences || len(references) < capability.MinimumTotalReferences || capability.MaximumTotalReferences > 0 && len(references) > capability.MaximumTotalReferences {
		return nil, &GenerationCapabilityIssue{Constraint: "total_reference_count", Actual: int64(len(references)), Minimum: int64(capability.MinimumTotalReferences), Maximum: int64(capability.MaximumTotalReferences)}
	}
	for role, limit := range limits {
		if len(limit.GenerationModes) > 0 && !slices.Contains(limit.GenerationModes, generationMode) {
			continue
		}
		if counts[role] < limit.MinimumCount {
			return nil, &GenerationCapabilityIssue{Constraint: "reference_count", Role: role, Actual: int64(counts[role]), Minimum: int64(limit.MinimumCount), Maximum: int64(limit.MaximumCount)}
		}
		if counts[role] > 0 && len(limit.RequiresAnyOf) > 0 {
			matched := false
			for _, requiredRole := range limit.RequiresAnyOf {
				matched = matched || counts[requiredRole] > 0
			}
			if !matched {
				return nil, fmt.Errorf("reference role %q is missing a required companion input", role)
			}
		}
	}
	for role, total := range totals {
		limit := limits[role]
		if limit.MaximumTotalDurationMilliseconds > 0 && total > limit.MaximumTotalDurationMilliseconds {
			return nil, &GenerationCapabilityIssue{Constraint: "reference_total_duration", Role: role, Actual: total, Maximum: limit.MaximumTotalDurationMilliseconds}
		}
	}
	if capability.MaximumTotalTimedReferenceDurationMilliseconds > 0 && allTimedTotal > capability.MaximumTotalTimedReferenceDurationMilliseconds {
		return nil, &GenerationCapabilityIssue{Constraint: "total_timed_reference_duration", Actual: allTimedTotal, Maximum: capability.MaximumTotalTimedReferenceDurationMilliseconds}
	}
	return references, nil
}

func hashGenerationRequest(input SubmitGenerationBatchInput) (string, error) {
	value, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:]), nil
}

func validateGenerationSubmissionIdentity(input SubmitGenerationBatchInput) error {
	return validateGenerationOperationKey(input.OperationKey)
}

func validateGenerationOperationKey(value string) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 200 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("invalid operation identity")
	}
	return nil
}

func (s *Service) publishGenerationJob(job domain.GenerationJob) {
	if s.generationEvents != nil {
		s.generationEvents.PublishGenerationJob(job)
	}
}
