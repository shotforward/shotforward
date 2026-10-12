package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GenerationSnapshotVersion       = 2
	MaxGenerationPromptCharacters   = 20_000
	MaxGenerationParametersBytes    = 64 * 1024
	MaxGenerationProgressCharacters = 500
)

var (
	ErrInvalidGenerationBatch    = errors.New("invalid generation batch")
	ErrInvalidGenerationJob      = errors.New("invalid generation job")
	ErrInvalidGenerationSnapshot = errors.New("invalid generation snapshot")
)

type GeneratorID string

const (
	GeneratorImageCodex          GeneratorID = "image.codex"
	GeneratorImageShotForward    GeneratorID = "image.shotforward"
	GeneratorVideoH3China        GeneratorID = "video.h3.minimax-cn"
	GeneratorVideoH3Official     GeneratorID = "video.h3.official"
	GeneratorVideoH3ShotForward  GeneratorID = "video.h3.shotforward"
	GeneratorVideoSeedanceChina  GeneratorID = "video.seedance-2.5.volcengine-cn"
	GeneratorVideoSeedanceGlobal GeneratorID = "video.seedance-2.5.byteplus-global"
	GeneratorVideoWanOfficial    GeneratorID = "video.wan-3.0.official"
)

func (g GeneratorID) Validate() error {
	value := string(g)
	if len(value) < 3 || len(value) > 200 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: invalid Generator ID", ErrInvalidGenerationSnapshot)
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" {
			return fmt.Errorf("%w: invalid Generator ID", ErrInvalidGenerationSnapshot)
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return fmt.Errorf("%w: invalid Generator ID", ErrInvalidGenerationSnapshot)
			}
		}
	}
	return nil
}

func (g GeneratorID) MediaKind() MediaKind {
	switch {
	case strings.HasPrefix(string(g), "image."):
		return MediaKindImage
	case strings.HasPrefix(string(g), "video."):
		return MediaKindVideo
	default:
		return ""
	}
}

type GenerationJobState string

const (
	GenerationJobQueued        GenerationJobState = "queued"
	GenerationJobRunning       GenerationJobState = "running"
	GenerationJobStopRequested GenerationJobState = "stop_requested"
	GenerationJobSucceeded     GenerationJobState = "succeeded"
	GenerationJobFailed        GenerationJobState = "failed"
	GenerationJobCancelled     GenerationJobState = "cancelled"
	GenerationJobStopped       GenerationJobState = "stopped"
)

func (s GenerationJobState) IsValid() bool {
	switch s {
	case GenerationJobQueued, GenerationJobRunning, GenerationJobStopRequested, GenerationJobSucceeded, GenerationJobFailed, GenerationJobCancelled, GenerationJobStopped:
		return true
	}
	return false
}
func (s GenerationJobState) IsTerminal() bool {
	return s == GenerationJobSucceeded || s == GenerationJobFailed || s == GenerationJobCancelled || s == GenerationJobStopped
}
func (s GenerationJobState) IsActive() bool {
	return s == GenerationJobQueued || s == GenerationJobRunning || s == GenerationJobStopRequested
}

func CanTransitionGenerationJob(from, to GenerationJobState) bool {
	switch from {
	case GenerationJobQueued:
		return to == GenerationJobRunning || to == GenerationJobCancelled
	case GenerationJobRunning:
		return to == GenerationJobSucceeded || to == GenerationJobFailed || to == GenerationJobStopRequested
	case GenerationJobStopRequested:
		return to == GenerationJobSucceeded || to == GenerationJobFailed || to == GenerationJobStopped
	}
	return false
}

type GenerationArtifactRole string

const (
	GenerationReferenceFirstImage GenerationArtifactRole = "first_image"
	GenerationReferenceLastImage  GenerationArtifactRole = "last_image"
	GenerationReferenceImage      GenerationArtifactRole = "image_reference"
	GenerationReferenceVideo      GenerationArtifactRole = "video_reference"
	GenerationReferenceAudio      GenerationArtifactRole = "audio_reference"
)

func (r GenerationArtifactRole) IsValid() bool {
	return r == GenerationReferenceFirstImage || r == GenerationReferenceLastImage || r == GenerationReferenceImage || r == GenerationReferenceVideo || r == GenerationReferenceAudio
}

func (r GenerationArtifactRole) MediaKind() MediaKind {
	switch r {
	case GenerationReferenceFirstImage, GenerationReferenceLastImage, GenerationReferenceImage:
		return MediaKindImage
	case GenerationReferenceVideo:
		return MediaKindVideo
	case GenerationReferenceAudio:
		return MediaKindAudio
	default:
		return ""
	}
}

type GenerationArtifactReference struct {
	Role                  GenerationArtifactRole `json:"role"`
	ArtifactID            ArtifactID             `json:"artifact_id"`
	TrimStartMilliseconds int64                  `json:"trim_start_ms,omitempty"`
	TrimEndMilliseconds   int64                  `json:"trim_end_ms,omitempty"`
	PreparationRequired   bool                   `json:"preparation_required,omitempty"`
}

type GenerationInputSnapshot struct {
	SchemaVersion              int                           `json:"schema_version"`
	TargetMediaKind            MediaKind                     `json:"target_media_kind"`
	Prompt                     string                        `json:"prompt"`
	GenerationMode             ClipGenerationMode            `json:"generation_mode,omitempty"`
	GeneratorID                GeneratorID                   `json:"generator_id"`
	ProviderModelID            string                        `json:"provider_model_id,omitempty"`
	ProviderConnectionRevision int64                         `json:"provider_connection_revision,omitempty"`
	CapabilityVersion          string                        `json:"capability_version,omitempty"`
	OutputWidth                int                           `json:"output_width"`
	OutputHeight               int                           `json:"output_height"`
	RequestedDurationSeconds   int64                         `json:"requested_duration_seconds,omitempty"`
	References                 []GenerationArtifactReference `json:"references,omitempty"`
	ParametersJSON             string                        `json:"parameters_json"`
	TargetSlotRevision         int64                         `json:"target_slot_revision"`
}

func (s GenerationInputSnapshot) Validate() error {
	if s.SchemaVersion != GenerationSnapshotVersion {
		return fmt.Errorf("%w: unsupported schema version", ErrInvalidGenerationSnapshot)
	}
	if s.TargetMediaKind != MediaKindImage && s.TargetMediaKind != MediaKindVideo {
		return fmt.Errorf("%w: unsupported target media kind", ErrInvalidGenerationSnapshot)
	}
	if !utf8.ValidString(s.Prompt) || utf8.RuneCountInString(s.Prompt) > MaxGenerationPromptCharacters || strings.ContainsAny(s.Prompt, "\r\x00") {
		return fmt.Errorf("%w: invalid prompt", ErrInvalidGenerationSnapshot)
	}
	if err := s.GeneratorID.Validate(); err != nil || s.GeneratorID.MediaKind() != s.TargetMediaKind {
		return fmt.Errorf("%w: Generator does not match target media kind", ErrInvalidGenerationSnapshot)
	}
	if s.ProviderModelID == "" && s.ProviderConnectionRevision != 0 || s.ProviderModelID != "" && s.ProviderConnectionRevision < 1 {
		return fmt.Errorf("%w: invalid Provider model snapshot", ErrInvalidGenerationSnapshot)
	}
	if s.ProviderModelID != "" {
		if err := validateNamedTitle("Provider model ID", s.ProviderModelID, 200); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidGenerationSnapshot, err)
		}
	}
	if s.CapabilityVersion != "" {
		if err := validateNamedTitle("capability version", s.CapabilityVersion, 200); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidGenerationSnapshot, err)
		}
	}
	if !validGenerationOutputSize(s.TargetMediaKind, s.OutputWidth, s.OutputHeight) {
		return fmt.Errorf("%w: unsupported output size", ErrInvalidGenerationSnapshot)
	}
	if s.TargetSlotRevision < 1 {
		return fmt.Errorf("%w: invalid revision", ErrInvalidGenerationSnapshot)
	}
	if len(s.ParametersJSON) == 0 || len(s.ParametersJSON) > MaxGenerationParametersBytes || !json.Valid([]byte(s.ParametersJSON)) {
		return fmt.Errorf("%w: invalid Generator parameters", ErrInvalidGenerationSnapshot)
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.ParametersJSON), &parameters); err != nil || parameters == nil {
		return fmt.Errorf("%w: Generator parameters must be an object", ErrInvalidGenerationSnapshot)
	}
	roles := map[GenerationArtifactRole]int{}
	seenReferences := map[string]struct{}{}
	for _, reference := range s.References {
		if !reference.Role.IsValid() || reference.ArtifactID.Validate() != nil {
			return fmt.Errorf("%w: invalid Artifact reference", ErrInvalidGenerationSnapshot)
		}
		key := reference.ArtifactID.String()
		if _, exists := seenReferences[key]; exists {
			return fmt.Errorf("%w: duplicate Artifact reference", ErrInvalidGenerationSnapshot)
		}
		seenReferences[key] = struct{}{}
		roles[reference.Role]++
		if reference.Role.MediaKind() == MediaKindImage {
			if reference.TrimStartMilliseconds != 0 || reference.TrimEndMilliseconds != 0 {
				return fmt.Errorf("%w: image reference cannot be trimmed", ErrInvalidGenerationSnapshot)
			}
		} else if reference.TrimStartMilliseconds < 0 || reference.TrimEndMilliseconds <= reference.TrimStartMilliseconds {
			return fmt.Errorf("%w: timed reference requires an explicit valid range", ErrInvalidGenerationSnapshot)
		}
	}
	if roles[GenerationReferenceFirstImage] > 1 || roles[GenerationReferenceLastImage] > 1 {
		return fmt.Errorf("%w: duplicate singleton reference role", ErrInvalidGenerationSnapshot)
	}
	if s.TargetMediaKind == MediaKindImage {
		if s.GenerationMode != "" || s.RequestedDurationSeconds != 0 || len(s.References) != roles[GenerationReferenceImage] {
			return fmt.Errorf("%w: invalid image generation inputs", ErrInvalidGenerationSnapshot)
		}
		if s.Prompt == "" && len(s.References) == 0 {
			return fmt.Errorf("%w: image generation needs a prompt or reference", ErrInvalidGenerationSnapshot)
		}
		return nil
	}
	if !s.GenerationMode.IsValid() || s.RequestedDurationSeconds < 1 {
		return fmt.Errorf("%w: invalid video mode or duration", ErrInvalidGenerationSnapshot)
	}
	switch s.GenerationMode {
	case ClipGenerationFirstImage:
		if roles[GenerationReferenceFirstImage] != 1 || len(s.References) != 1 {
			return fmt.Errorf("%w: first-image mode references are invalid", ErrInvalidGenerationSnapshot)
		}
	case ClipGenerationFirstLastImage:
		if roles[GenerationReferenceFirstImage] != 1 || roles[GenerationReferenceLastImage] != 1 || len(s.References) != 2 {
			return fmt.Errorf("%w: first-last-image mode references are invalid", ErrInvalidGenerationSnapshot)
		}
	case ClipGenerationReference:
		if len(s.References) == 0 || roles[GenerationReferenceFirstImage] != 0 || roles[GenerationReferenceLastImage] != 0 {
			return fmt.Errorf("%w: reference mode inputs are invalid", ErrInvalidGenerationSnapshot)
		}
	}
	return nil
}

func validGenerationOutputSize(kind MediaKind, width, height int) bool {
	if _, err := NewOutputSpec(width, height); err == nil {
		return true
	}
	if kind != MediaKindImage {
		return false
	}
	switch [2]int{width, height} {
	case [2]int{864, 1536}, [2]int{1536, 864}, [2]int{1536, 1536}:
		return true
	default:
		return false
	}
}

type GenerationBatch struct {
	ID           GenerationBatchID
	ProjectID    ProjectID
	EpisodeID    EpisodeID
	OperationKey string
	RequestHash  string
	CreatedAt    time.Time
}

func (b GenerationBatch) Validate() error {
	if b.ID.Validate() != nil || b.ProjectID.Validate() != nil || b.EpisodeID.Validate() != nil {
		return ErrInvalidGenerationBatch
	}
	if err := validateOpaqueGenerationValue("operation key", b.OperationKey, 200, true); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationBatch, err)
	}
	if len(b.RequestHash) != 64 {
		return fmt.Errorf("%w: invalid request hash", ErrInvalidGenerationBatch)
	}
	for _, value := range b.RequestHash {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return fmt.Errorf("%w: invalid request hash", ErrInvalidGenerationBatch)
		}
	}
	if err := validateTimestamp("created at", b.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationBatch, err)
	}
	return nil
}

type GenerationJob struct {
	ID                   GenerationJobID
	BatchID              GenerationBatchID
	ProjectID            ProjectID
	EpisodeID            EpisodeID
	TargetSlotID         MediaSlotID
	RetryOfJobID         GenerationJobID
	State                GenerationJobState
	Snapshot             GenerationInputSnapshot
	StopSupported        bool
	GeneratorOperationID string
	ProgressStage        string
	ProgressMessage      string
	ResultArtifactID     ArtifactID
	ResultVersionID      MediaVersionID
	FailureCode          string
	FailureMessage       string
	Revision             int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
}

func (j GenerationJob) Validate() error {
	if j.ID.Validate() != nil || j.BatchID.Validate() != nil || j.ProjectID.Validate() != nil || j.EpisodeID.Validate() != nil || j.TargetSlotID.Validate() != nil || !j.State.IsValid() {
		return ErrInvalidGenerationJob
	}
	if j.RetryOfJobID != "" && (j.RetryOfJobID.Validate() != nil || j.RetryOfJobID == j.ID) {
		return fmt.Errorf("%w: invalid retry source", ErrInvalidGenerationJob)
	}
	if err := j.Snapshot.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationJob, err)
	}
	if j.Snapshot.TargetSlotRevision < 1 || j.Revision < 1 {
		return fmt.Errorf("%w: invalid revision", ErrInvalidGenerationJob)
	}
	if err := validateMediaRevisionTimes(ErrInvalidGenerationJob, j.Revision, j.CreatedAt, j.UpdatedAt, nil); err != nil {
		return err
	}
	if err := validateOpaqueGenerationValue("Generator operation ID", j.GeneratorOperationID, 500, false); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationJob, err)
	}
	if err := validateOpaqueGenerationValue("progress stage", j.ProgressStage, 100, false); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationJob, err)
	}
	if !utf8.ValidString(j.ProgressMessage) || utf8.RuneCountInString(j.ProgressMessage) > MaxGenerationProgressCharacters || strings.ContainsRune(j.ProgressMessage, 0) {
		return fmt.Errorf("%w: invalid progress message", ErrInvalidGenerationJob)
	}
	if err := validateOpaqueGenerationValue("failure code", j.FailureCode, 100, false); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerationJob, err)
	}
	if !utf8.ValidString(j.FailureMessage) || utf8.RuneCountInString(j.FailureMessage) > 2_000 || strings.ContainsRune(j.FailureMessage, 0) {
		return fmt.Errorf("%w: invalid failure message", ErrInvalidGenerationJob)
	}
	if j.StartedAt != nil && validateTimestamp("started at", *j.StartedAt) != nil {
		return fmt.Errorf("%w: invalid started at", ErrInvalidGenerationJob)
	}
	if j.FinishedAt != nil && validateTimestamp("finished at", *j.FinishedAt) != nil {
		return fmt.Errorf("%w: invalid finished at", ErrInvalidGenerationJob)
	}
	if j.StartedAt != nil && j.StartedAt.Before(j.CreatedAt) {
		return fmt.Errorf("%w: started before creation", ErrInvalidGenerationJob)
	}
	if j.FinishedAt != nil && (j.FinishedAt.Before(j.CreatedAt) || j.StartedAt != nil && j.FinishedAt.Before(*j.StartedAt)) {
		return fmt.Errorf("%w: finished before start", ErrInvalidGenerationJob)
	}
	switch j.State {
	case GenerationJobQueued:
		if j.StartedAt != nil || j.FinishedAt != nil || j.GeneratorOperationID != "" || j.ResultArtifactID != "" || j.ResultVersionID != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return fmt.Errorf("%w: queued Job contains execution state", ErrInvalidGenerationJob)
		}
	case GenerationJobRunning, GenerationJobStopRequested:
		if j.StartedAt == nil || j.FinishedAt != nil || j.ResultArtifactID != "" || j.ResultVersionID != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return fmt.Errorf("%w: active Job state is inconsistent", ErrInvalidGenerationJob)
		}
	case GenerationJobSucceeded:
		if j.StartedAt == nil || j.FinishedAt == nil || j.ResultArtifactID.Validate() != nil || j.ResultVersionID.Validate() != nil || j.FailureCode != "" || j.FailureMessage != "" {
			return fmt.Errorf("%w: succeeded Job state is inconsistent", ErrInvalidGenerationJob)
		}
	case GenerationJobFailed:
		if j.StartedAt == nil || j.FinishedAt == nil || j.FailureCode == "" || j.FailureMessage == "" || j.ResultArtifactID != "" || j.ResultVersionID != "" {
			return fmt.Errorf("%w: failed Job state is inconsistent", ErrInvalidGenerationJob)
		}
	case GenerationJobCancelled:
		if j.StartedAt != nil || j.FinishedAt == nil || j.GeneratorOperationID != "" || j.ResultArtifactID != "" || j.ResultVersionID != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return fmt.Errorf("%w: cancelled Job state is inconsistent", ErrInvalidGenerationJob)
		}
	case GenerationJobStopped:
		if j.StartedAt == nil || j.FinishedAt == nil || j.ResultArtifactID != "" || j.ResultVersionID != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return fmt.Errorf("%w: stopped Job state is inconsistent", ErrInvalidGenerationJob)
		}
	}
	if j.State == GenerationJobStopRequested && !j.StopSupported {
		return fmt.Errorf("%w: stop requested for unsupported Generator", ErrInvalidGenerationJob)
	}
	return nil
}

type GenerationBatchDetails struct {
	Batch GenerationBatch
	Jobs  []GenerationJob
}

type GenerationBatchState string

const (
	GenerationBatchQueued    GenerationBatchState = "queued"
	GenerationBatchRunning   GenerationBatchState = "running"
	GenerationBatchSucceeded GenerationBatchState = "succeeded"
	GenerationBatchPartial   GenerationBatchState = "partial"
	GenerationBatchFailed    GenerationBatchState = "failed"
	GenerationBatchCancelled GenerationBatchState = "cancelled"
	GenerationBatchStopped   GenerationBatchState = "stopped"
)

// DerivedState keeps Batch status free of persisted state that could diverge
// from its Jobs. Mixed terminal outcomes are reported as partial.
func (b GenerationBatchDetails) DerivedState() GenerationBatchState {
	if len(b.Jobs) == 0 {
		return ""
	}
	counts := map[GenerationJobState]int{}
	for _, job := range b.Jobs {
		counts[job.State]++
		if job.State.IsActive() {
			if counts[GenerationJobRunning]+counts[GenerationJobStopRequested] > 0 {
				return GenerationBatchRunning
			}
		}
	}
	if counts[GenerationJobQueued] == len(b.Jobs) {
		return GenerationBatchQueued
	}
	if counts[GenerationJobQueued]+counts[GenerationJobRunning]+counts[GenerationJobStopRequested] > 0 {
		return GenerationBatchRunning
	}
	if counts[GenerationJobSucceeded] == len(b.Jobs) {
		return GenerationBatchSucceeded
	}
	if counts[GenerationJobFailed] == len(b.Jobs) {
		return GenerationBatchFailed
	}
	if counts[GenerationJobCancelled] == len(b.Jobs) {
		return GenerationBatchCancelled
	}
	if counts[GenerationJobStopped] == len(b.Jobs) {
		return GenerationBatchStopped
	}
	return GenerationBatchPartial
}

func (b GenerationBatchDetails) Validate() error {
	if err := b.Batch.Validate(); err != nil {
		return err
	}
	seen := map[GenerationJobID]struct{}{}
	if len(b.Jobs) == 0 {
		return fmt.Errorf("%w: Batch has no Jobs", ErrInvalidGenerationBatch)
	}
	for _, job := range b.Jobs {
		if err := job.Validate(); err != nil {
			return err
		}
		if job.BatchID != b.Batch.ID || job.ProjectID != b.Batch.ProjectID || job.EpisodeID != b.Batch.EpisodeID {
			return fmt.Errorf("%w: Job belongs to another Batch", ErrInvalidGenerationBatch)
		}
		if _, exists := seen[job.ID]; exists {
			return fmt.Errorf("%w: duplicate Job", ErrInvalidGenerationBatch)
		}
		seen[job.ID] = struct{}{}
	}
	return nil
}

func validateOpaqueGenerationValue(label, value string, maximum int, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") || utf8.RuneCountInString(value) > maximum || value != strings.TrimSpace(value) || (required && value == "") {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}
