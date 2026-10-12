// Package projectapp coordinates Project creation and opening without exposing
// filesystem, SQLite, or transport details to callers.
package projectapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type ProjectFile struct {
	Content    ReadSeekCloser
	ByteSize   int64
	ModifiedAt time.Time
}

type ProjectFileReader interface {
	OpenProjectFile(string, string) (ProjectFile, error)
}

type ArtifactContentSource struct {
	Content          ReadSeekCloser
	MIMEType         string
	OriginalFilename string
	ByteSize         int64
	ModifiedAt       time.Time
}

type GetArtifactContentInput struct {
	ProjectID  domain.ProjectID
	ArtifactID domain.ArtifactID
}

type ListClipReferenceMediaInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	ClipID    domain.ClipID
}

type UploadClipReferenceMediaInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	ClipID           domain.ClipID
	SourcePath       string
	OriginalFilename string
	Kind             domain.MediaKind
}

type DeleteClipReferenceMediaInput struct {
	ProjectID  domain.ProjectID
	EpisodeID  domain.EpisodeID
	ClipID     domain.ClipID
	ArtifactID domain.ArtifactID
}

type ListEpisodeReferenceMediaInput struct {
	ProjectID      domain.ProjectID
	EpisodeID      domain.EpisodeID
	IncludeDeleted bool
}

type UploadEpisodeReferenceMediaInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	SourcePath       string
	OriginalFilename string
	Kind             domain.MediaKind
}

type UpdateEpisodeReferenceMediaInput struct {
	ProjectID             domain.ProjectID
	EpisodeID             domain.EpisodeID
	ArtifactID            domain.ArtifactID
	Name                  string
	Description           string
	TrimStartMilliseconds *int64
	TrimEndMilliseconds   *int64
	ExpectedRevision      int64
}

type DeleteEpisodeReferenceMediaInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	ArtifactID       domain.ArtifactID
	ExpectedRevision int64
}

type AttachClipReferenceMediaInput struct {
	ProjectID  domain.ProjectID
	EpisodeID  domain.EpisodeID
	ClipID     domain.ClipID
	ArtifactID domain.ArtifactID
}

type UploadAgentConversationMediaInput struct {
	ProjectID         domain.ProjectID
	EpisodeID         domain.EpisodeID
	SessionID         string
	ClientOperationID string
	SourcePath        string
	OriginalFilename  string
}

type AgentConversationMediaReference struct {
	ArtifactID domain.ArtifactID
	Name       string
	Origin     domain.AgentConversationMediaOrigin
}

type PrepareAgentInputMediaInput struct {
	ProjectID         domain.ProjectID
	EpisodeID         domain.EpisodeID
	SessionID         string
	ClientOperationID string
	References        []AgentConversationMediaReference
}

type BindAgentInputMediaInput struct {
	ProjectID         domain.ProjectID
	EpisodeID         domain.EpisodeID
	SessionID         string
	ClientOperationID string
	TurnID            string
	ArtifactIDs       []domain.ArtifactID
}

type ImportAgentGeneratedMediaInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	SessionID        string
	TurnID           string
	SourcePath       string
	OriginalFilename string
}

type ListAgentConversationMediaInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	SessionID string
}

type PrepareAgentImageGenerationInput struct {
	ProjectID   domain.ProjectID
	EpisodeID   domain.EpisodeID
	SessionID   string
	TurnID      string
	ArtifactIDs []domain.ArtifactID
}

// AgentImageGenerationContext contains only the project-owned facts needed by
// the ShotForward MCP image tool. Absolute paths never cross the MCP boundary.
type AgentImageGenerationContext struct {
	ProjectRoot string
	Project     domain.Project
	References  []AgentInputMedia
}

type AgentInputMedia struct {
	Media        domain.AgentConversationMedia
	AbsolutePath string
}

var (
	ErrInvalidRequest               = errors.New("invalid project request")
	ErrProjectPathExists            = errors.New("project path already exists")
	ErrProjectPathNotFound          = errors.New("project path not found")
	ErrProjectPathInvalid           = errors.New("invalid project path")
	ErrProjectPathNotWritable       = errors.New("project path is not writable")
	ErrProjectPathConflict          = errors.New("project identity is registered at another path")
	ErrProjectDatabaseNotFound      = errors.New("project database not found")
	ErrProjectDatabaseInvalid       = errors.New("invalid project database")
	ErrProjectSchemaTooNew          = errors.New("project schema is newer than this application")
	ErrProjectMigrationFailed       = errors.New("project database migration failed")
	ErrProjectDataInvalid           = errors.New("invalid project data")
	ErrProjectCreateFailed          = errors.New("project creation failed")
	ErrProjectCreatedNotRegistered  = errors.New("project was created but not registered")
	ErrProjectOpenedNotRegistered   = errors.New("project was opened but not registered")
	ErrProjectUpdatedNotRegistered  = errors.New("project was updated but not registered")
	ErrProjectStoreNotOpen          = errors.New("project store is not open")
	ErrProjectHasActiveGeneration   = errors.New("project has active generation jobs")
	ErrProjectHasActiveExport       = errors.New("project has active export jobs")
	ErrProjectHasActiveAgent        = errors.New("project has active Agent tasks")
	ErrProjectNameConfirmation      = errors.New("project name confirmation does not match")
	ErrEpisodeNotFound              = errors.New("episode not found")
	ErrEpisodeNotDeleted            = errors.New("episode is not deleted")
	ErrEpisodeHasActiveGeneration   = errors.New("episode has active generation jobs")
	ErrEpisodeHasActiveExport       = errors.New("episode has active export jobs")
	ErrEpisodeHasActiveAgent        = errors.New("episode has active Agent tasks")
	ErrAssetNotFound                = errors.New("asset not found")
	ErrAssetIdentityNotFound        = errors.New("asset identity not found")
	ErrAssetAlreadyExists           = errors.New("asset already exists in episode")
	ErrAssetIdentityNameConflict    = errors.New("asset identity name conflict")
	ErrAssetKindMismatch            = errors.New("asset identity kind mismatch")
	ErrInvalidAssetProvenance       = errors.New("invalid asset provenance")
	ErrAssetDeleted                 = errors.New("asset is deleted")
	ErrAssetNotDeleted              = errors.New("asset is not deleted")
	ErrAssetInUse                   = errors.New("asset is referenced by active clips")
	ErrSceneNotFound                = errors.New("scene not found")
	ErrClipNotFound                 = errors.New("clip not found")
	ErrSceneDeleted                 = errors.New("scene is deleted")
	ErrSceneNotDeleted              = errors.New("scene is not deleted")
	ErrSceneNotEmpty                = errors.New("scene contains active clips")
	ErrSceneOrderMismatch           = errors.New("scene order does not match active storyboard")
	ErrClipDeleted                  = errors.New("clip is deleted")
	ErrClipNotDeleted               = errors.New("clip is not deleted")
	ErrClipAssetAlreadyBound        = errors.New("asset is already bound to clip")
	ErrClipAssetNotBound            = errors.New("asset is not bound to clip")
	ErrClipHasActiveGeneration      = errors.New("clip has an active generation job")
	ErrMediaSlotNotFound            = errors.New("media slot not found")
	ErrArtifactNotFound             = errors.New("artifact not found")
	ErrMediaVersionNotFound         = errors.New("media version not found")
	ErrMediaKindMismatch            = errors.New("media kind does not match slot")
	ErrMediaVersionAlreadyExists    = errors.New("artifact already has an active version in slot")
	ErrMediaImportUnavailable       = errors.New("media import is unavailable")
	ErrMediaImportUnsupported       = errors.New("unsupported media import format")
	ErrMediaImportUnreadable        = errors.New("media import file is unreadable")
	ErrMediaImportInvalid           = errors.New("media import file is invalid")
	ErrMediaAspectRatioMismatch     = errors.New("image aspect ratio does not match project output")
	ErrMediaOutputSpecMismatch      = errors.New("image dimensions do not match project output")
	ErrAudioReferenceNotFound       = errors.New("audio reference asset not found")
	ErrAudioReferenceInUse          = errors.New("audio reference asset is bound to a clip")
	ErrAudioReferenceDeleted        = errors.New("audio reference asset is deleted")
	ErrAudioReferenceNotDeleted     = errors.New("audio reference asset is not deleted")
	ErrGenerationBatchNotFound      = errors.New("generation batch not found")
	ErrGenerationJobNotFound        = errors.New("generation job not found")
	ErrGenerationOperationConflict  = errors.New("generation operation key was reused with different input")
	ErrGenerationSlotBusy           = errors.New("media slot already has an active generation job")
	ErrGenerationJobStateConflict   = errors.New("generation job state conflict")
	ErrGenerationStopUnsupported    = errors.New("generation Generator does not support stopping")
	ErrGenerationGeneratorNotFound  = errors.New("generation Generator not found")
	ErrGenerationCapabilityMismatch = errors.New("generation request is not supported by Generator")
	ErrGenerationNotReady           = errors.New("generation target is not ready")
	ErrExportJobNotFound            = errors.New("export job not found")
	ErrExportOperationConflict      = errors.New("export operation key was reused with different input")
	ErrExportEpisodeBusy            = errors.New("episode already has an active export job")
	ErrExportJobStateConflict       = errors.New("export job state conflict")
	ErrExportNotReady               = errors.New("episode has no current videos to export")
	ErrStoryboardRevisionConflict   = errors.New("storyboard revision conflict")
	ErrStoryboardResetConfirmation  = errors.New("storyboard reset requires confirmation")
	ErrRevisionConflict             = errors.New("revision conflict")
	ErrProjectWriteFailed           = errors.New("project write failed")
	ErrScriptTooLong                = errors.New("script exceeds character limit")
	ErrScriptImportUnsupported      = errors.New("unsupported script import format")
	ErrScriptImportUnreadable       = errors.New("script import file is unreadable")
	ErrScriptImportEncoding         = errors.New("script import encoding is unsupported")
	ErrScriptImportInvalid          = errors.New("script import file is invalid")
)

type ScriptImportFormat string

var (
	ErrPromptTranslationUnavailable      = errors.New("prompt translation Agent is unavailable")
	ErrPromptTranslationFailed           = errors.New("prompt translation failed")
	ErrPromptTranslationInvalid          = errors.New("translated prompt failed preservation or length checks")
	ErrPromptTranslationTimeout          = errors.New("prompt translation timed out")
	ErrPromptTranslationSelectionChanged = errors.New("prompt translation Agent selection changed")
)

// GenerationPromptTranslationFailure carries only recognized cloud business
// codes; upstream bodies and credentials never cross the application boundary.
type GenerationPromptTranslationFailure struct{ Code string }

func (e *GenerationPromptTranslationFailure) Error() string {
	return ErrPromptTranslationFailed.Error()
}
func (e *GenerationPromptTranslationFailure) Unwrap() error { return ErrPromptTranslationFailed }

const (
	ScriptImportText     ScriptImportFormat = "text"
	ScriptImportMarkdown ScriptImportFormat = "markdown"
	ScriptImportDOCX     ScriptImportFormat = "docx"
)

// CreateProjectInput contains only user-controlled creation fields.
type CreateProjectInput struct {
	RootPath     string
	Name         string
	Description  string
	OutputWidth  int
	OutputHeight int
}

// OpenProjectInput identifies an existing portable project directory.
type OpenProjectInput struct {
	RootPath string
}

type RestoreProjectInput struct {
	ProjectID domain.ProjectID
	RootPath  string
}

type DeleteProjectInput struct {
	ProjectID        domain.ProjectID
	ConfirmationName string
}

type UpdateProjectInput struct {
	ProjectID        domain.ProjectID
	Name             string
	Description      string
	ExpectedRevision int64
}

type GetEpisodeInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
}

type CreateEpisodeInput struct {
	ProjectID      domain.ProjectID
	Title          string
	TargetDuration time.Duration
}

type UpdateEpisodeInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	Title            string
	TargetDuration   time.Duration
	ExpectedRevision int64
}

type ChangeEpisodeDeletionInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	ExpectedRevision int64
}

type SaveScriptInput struct {
	ProjectID              domain.ProjectID
	EpisodeID              domain.EpisodeID
	Markdown               string
	ExpectedScriptRevision int64
}

type ImportScriptInput struct {
	ProjectID              domain.ProjectID
	EpisodeID              domain.EpisodeID
	SourcePath             string
	ExpectedScriptRevision int64
}

type ValidateScriptInput struct {
	Markdown string
}

type GetAssetInput struct {
	ProjectID domain.ProjectID
	AssetID   domain.AssetID
}

type ListEpisodeAssetsInput struct {
	ProjectID      domain.ProjectID
	EpisodeID      domain.EpisodeID
	IncludeDeleted bool
}
type FindPriorAssetCandidatesInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	Kind      domain.AssetKind
	NameQuery string
}

type PriorAssetCandidate struct {
	Asset           domain.EpisodeAsset
	EpisodeSequence int
}

type CreateAssetInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	Kind      domain.AssetKind
	Name      string
	Prompt    string
}

// DeriveAssetInput creates an independent Episode Asset linked to a prior
// Episode Asset's identity. A nil Prompt inherits the source prompt; a
// non-nil Prompt, including an empty string, is an explicit override.
type DeriveAssetInput struct {
	ProjectID     domain.ProjectID
	EpisodeID     domain.EpisodeID
	SourceAssetID domain.AssetID
	Prompt        *string
}

type UpdateAssetPromptInput struct {
	ProjectID        domain.ProjectID
	AssetID          domain.AssetID
	Prompt           string
	ExpectedRevision int64
}

type RenameAssetIdentityInput struct {
	ProjectID        domain.ProjectID
	AssetIdentityID  domain.AssetIdentityID
	Name             string
	ExpectedRevision int64
}

type RelinkAssetIdentityInput struct {
	ProjectID             domain.ProjectID
	AssetID               domain.AssetID
	TargetAssetIdentityID domain.AssetIdentityID
	ExpectedAssetRevision int64
}

type DetachAssetIdentityInput struct {
	ProjectID             domain.ProjectID
	AssetID               domain.AssetID
	NewName               string
	ExpectedAssetRevision int64
}

type DeleteAssetInput struct {
	ProjectID         domain.ProjectID
	AssetID           domain.AssetID
	ExpectedRevision  int64
	ConfirmReferenced bool
}

type RestoreAssetInput struct {
	ProjectID        domain.ProjectID
	AssetID          domain.AssetID
	ExpectedRevision int64
}

type SceneScriptSourceInput struct {
	ScriptRevision int64
	StartByte      int
	EndByte        int
}

type GetStoryboardInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
}

type ResetStoryboardInput struct {
	ProjectID                  domain.ProjectID
	EpisodeID                  domain.EpisodeID
	ExpectedStoryboardRevision int64
	Confirm                    bool
	RequestStopForRunningJobs  bool
}

type ResetStoryboardResult struct {
	Storyboard             domain.Storyboard
	ChangedJobs            []domain.GenerationJob
	Reset                  bool
	WaitingForTerminalJobs bool
}
type GetSceneInput struct {
	ProjectID domain.ProjectID
	SceneID   domain.SceneID
}
type GetClipInput struct {
	ProjectID domain.ProjectID
	ClipID    domain.ClipID
}

type ReorderScenesInput struct {
	ProjectID                  domain.ProjectID
	EpisodeID                  domain.EpisodeID
	OrderedSceneIDs            []domain.SceneID
	ExpectedStoryboardRevision int64
}

type CreateSceneInput struct {
	ProjectID                  domain.ProjectID
	EpisodeID                  domain.EpisodeID
	Title                      string
	Description                string
	Source                     *SceneScriptSourceInput
	ExpectedStoryboardRevision int64
}

type UpdateSceneInput struct {
	ProjectID        domain.ProjectID
	SceneID          domain.SceneID
	Title            string
	Description      string
	ExpectedRevision int64
}

type DeleteSceneInput struct {
	ProjectID                                    domain.ProjectID
	SceneID                                      domain.SceneID
	ExpectedRevision, ExpectedStoryboardRevision int64
	ConfirmNonEmpty                              bool
}
type RestoreSceneInput struct {
	ProjectID                                    domain.ProjectID
	SceneID                                      domain.SceneID
	ExpectedRevision, ExpectedStoryboardRevision int64
}

type ClipFields struct {
	Title, Description, Dialogue, CreativeIntent string
	PlannedDurationSeconds                       int64
	FirstImagePrompt, LastImagePrompt            string
	VideoPrompts                                 domain.ClipVideoPrompts
	GenerationMode                               domain.ClipGenerationMode
	ContinueFromPrevious                         bool
}

type CreateClipInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	SceneID   domain.SceneID
	ClipFields
	ExpectedStoryboardRevision int64
}
type UpdateClipInput struct {
	ProjectID domain.ProjectID
	ClipID    domain.ClipID
	ClipFields
	ExpectedRevision int64
}
type MoveClipInput struct {
	ProjectID                                    domain.ProjectID
	ClipID                                       domain.ClipID
	TargetSceneID                                domain.SceneID
	BeforeClipID, AfterClipID                    domain.ClipID
	ExpectedRevision, ExpectedStoryboardRevision int64
}
type DeleteClipInput struct {
	ProjectID                                    domain.ProjectID
	ClipID                                       domain.ClipID
	ExpectedRevision, ExpectedStoryboardRevision int64
}
type RestoreClipInput struct {
	ProjectID                                    domain.ProjectID
	ClipID                                       domain.ClipID
	ExpectedRevision, ExpectedStoryboardRevision int64
}
type BindClipAssetInput struct {
	ProjectID            domain.ProjectID
	ClipID               domain.ClipID
	AssetID              domain.AssetID
	ExpectedClipRevision int64
}
type UnbindClipAssetInput = BindClipAssetInput
type ReplaceClipAssetInput struct {
	ProjectID              domain.ProjectID
	ClipID                 domain.ClipID
	OldAssetID, NewAssetID domain.AssetID
	ExpectedClipRevision   int64
}

type ReplacementClipInput struct {
	ClipFields
	AssetIDs []domain.AssetID
}

type SplitClipInput struct {
	ProjectID                  domain.ProjectID
	ClipID                     domain.ClipID
	ExpectedRevision           int64
	ExpectedStoryboardRevision int64
	Replacements               []ReplacementClipInput
}

type MergeClipSource struct {
	ClipID           domain.ClipID
	ExpectedRevision int64
}

type MergeClipsInput struct {
	ProjectID                  domain.ProjectID
	Sources                    []MergeClipSource
	Replacement                ReplacementClipInput
	ExpectedStoryboardRevision int64
}

type ReplaceClipsResult struct {
	Storyboard StoryboardResult
	Created    []domain.ClipDetails
	Replaced   []domain.ClipDetails
}

type StoryboardResult struct {
	Storyboard      domain.Storyboard
	TargetDuration  time.Duration
	DurationWarning bool
}

type StoryboardValidationSeverity string

const (
	StoryboardValidationError   StoryboardValidationSeverity = "error"
	StoryboardValidationWarning StoryboardValidationSeverity = "warning"
)

type StoryboardValidationCode string

const (
	StoryboardIssueEmpty                    StoryboardValidationCode = "storyboard_empty"
	StoryboardIssueEmptyScene               StoryboardValidationCode = "scene_empty"
	StoryboardIssueDurationOutsideTolerance StoryboardValidationCode = "duration_outside_tolerance"
	StoryboardIssueDeletedAssetReference    StoryboardValidationCode = "asset_reference_deleted"
	StoryboardIssueAssetImageMissing        StoryboardValidationCode = "asset_reference_image_missing"
	StoryboardIssueFirstFrameMissing        StoryboardValidationCode = "first_frame_missing"
	StoryboardIssueLastFrameMissing         StoryboardValidationCode = "last_frame_missing"
	StoryboardIssueReferenceInputMissing    StoryboardValidationCode = "reference_input_missing"
)

// StoryboardValidationIssue is a stable, transport-neutral explanation of one
// current Storyboard problem. Empty IDs mean the issue applies to the whole
// Storyboard rather than a narrower object.
type StoryboardValidationIssue struct {
	Severity        StoryboardValidationSeverity
	Code            StoryboardValidationCode
	SceneID         domain.SceneID
	ClipID          domain.ClipID
	AssetID         domain.AssetID
	Message         string
	SuggestedAction string
}

// StoryboardValidationResult is a dynamic projection. It is never persisted
// as Clip status and does not include Provider-specific capability checks.
type StoryboardValidationResult struct {
	ProjectID          domain.ProjectID
	EpisodeID          domain.EpisodeID
	StoryboardRevision int64
	PlannedDurationMS  int64
	TargetDurationMS   int64
	Valid              bool
	Issues             []StoryboardValidationIssue
}

type AssetUsage struct {
	AssetID domain.AssetID
	Clips   []domain.Clip
}

type GetMediaSlotInput struct {
	ProjectID      domain.ProjectID
	MediaSlotID    domain.MediaSlotID
	IncludeDeleted bool
}

type ImportMediaInput struct {
	ProjectID            domain.ProjectID
	MediaSlotID          domain.MediaSlotID
	SourcePath           string
	ExpectedSlotRevision int64
	MakeCurrent          bool
}

type ExtractVideoFrameInput struct {
	ProjectID              domain.ProjectID
	TargetMediaSlotID      domain.MediaSlotID
	SourceVideoMediaSlotID domain.MediaSlotID
	ExpectedTargetRevision int64
}

type AssignArtifactInput struct {
	ProjectID            domain.ProjectID
	MediaSlotID          domain.MediaSlotID
	ArtifactID           domain.ArtifactID
	ExpectedSlotRevision int64
	MakeCurrent          bool
}

type SelectMediaVersionInput struct {
	ProjectID            domain.ProjectID
	MediaSlotID          domain.MediaSlotID
	MediaVersionID       domain.MediaVersionID
	ExpectedSlotRevision int64
}

type ChangeMediaVersionDeletionInput struct {
	ProjectID                    domain.ProjectID
	MediaSlotID                  domain.MediaSlotID
	MediaVersionID               domain.MediaVersionID
	ExpectedMediaVersionRevision int64
	ExpectedMediaSlotRevision    int64
}

type UpdateVideoTrimInput struct {
	ProjectID               domain.ProjectID
	MediaSlotID             domain.MediaSlotID
	MediaVersionID          domain.MediaVersionID
	TrimInMilliseconds      int64
	TrimOutMilliseconds     int64
	ExpectedVersionRevision int64
}

type ListAudioReferenceAssetsInput struct {
	ProjectID      domain.ProjectID
	EpisodeID      domain.EpisodeID
	IncludeDeleted bool
}

type GetAudioReferenceAssetInput struct {
	ProjectID             domain.ProjectID
	AudioReferenceAssetID domain.AudioReferenceAssetID
}

type CreateAudioReferenceAssetInput struct {
	ProjectID        domain.ProjectID
	EpisodeID        domain.EpisodeID
	Name             string
	CharacterAssetID domain.AssetID
}

type UpdateAudioReferenceAssetInput struct {
	ProjectID             domain.ProjectID
	AudioReferenceAssetID domain.AudioReferenceAssetID
	Name                  string
	CharacterAssetID      domain.AssetID
	ExpectedRevision      int64
}

type ChangeAudioReferenceDeletionInput struct {
	ProjectID             domain.ProjectID
	AudioReferenceAssetID domain.AudioReferenceAssetID
	ExpectedRevision      int64
}

type SetClipAudioReferenceInput struct {
	ProjectID             domain.ProjectID
	ClipID                domain.ClipID
	AudioReferenceAssetID domain.AudioReferenceAssetID
	ExpectedClipRevision  int64
}

type ClearClipAudioReferenceInput struct {
	ProjectID            domain.ProjectID
	ClipID               domain.ClipID
	ExpectedClipRevision int64
}

type SubmitGenerationTarget struct {
	MediaSlotID              domain.MediaSlotID
	ExpectedSlotRevision     int64
	Prompt                   string
	GenerationMode           domain.ClipGenerationMode
	GeneratorID              domain.GeneratorID
	ProviderModelID          string
	RequestedDurationSeconds int64
	References               []domain.GenerationArtifactReference
	ParametersJSON           string
	RetryOfJobID             domain.GenerationJobID
}

type SubmitGenerationBatchInput struct {
	ProjectID         domain.ProjectID
	EpisodeID         domain.EpisodeID
	OperationKey      string
	Targets           []SubmitGenerationTarget
	PromptTranslation *GenerationPromptTranslationOptions `json:",omitempty"`
}

// GenerationPromptTranslationOptions captures service/model selection without
// coupling generation preparation to any user-visible Agent Session.
type GenerationPromptTranslationOptions struct {
	RuntimeKind string `json:"runtime_kind"`
	ModelID     string `json:"model_id"`
}

type GenerationPromptTranslator interface {
	Translate(context.Context, GenerationPromptTranslationOptions, string) (string, error)
}

type CheckGenerationReadinessInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	Target    SubmitGenerationTarget
}

type GenerationReadinessResult struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
	Slot      domain.MediaSlot
	Snapshot  domain.GenerationInputSnapshot
}

type GetGenerationBatchInput struct {
	ProjectID         domain.ProjectID
	GenerationBatchID domain.GenerationBatchID
}

type GetGenerationJobInput struct {
	ProjectID       domain.ProjectID
	GenerationJobID domain.GenerationJobID
}

type ChangeGenerationJobInput struct {
	ProjectID        domain.ProjectID
	GenerationJobID  domain.GenerationJobID
	ExpectedRevision int64
}

type RetryGenerationJobInput struct {
	ProjectID        domain.ProjectID
	GenerationJobID  domain.GenerationJobID
	ExpectedRevision int64
	OperationKey     string
}

type CancelGenerationBatchInput struct {
	ProjectID                 domain.ProjectID
	GenerationBatchID         domain.GenerationBatchID
	RequestStopForRunningJobs bool
}

type GetExportPreviewInput struct {
	ProjectID domain.ProjectID
	EpisodeID domain.EpisodeID
}

type SubmitExportJobInput struct {
	ProjectID                  domain.ProjectID
	EpisodeID                  domain.EpisodeID
	OperationKey               string
	ExpectedStoryboardRevision int64
}

type GetExportJobInput struct {
	ProjectID   domain.ProjectID
	ExportJobID domain.ExportJobID
}

type CancelExportJobInput struct {
	ProjectID        domain.ProjectID
	ExportJobID      domain.ExportJobID
	ExpectedRevision int64
}

type PreparedMedia struct {
	Token                string
	AbsolutePath         string
	Kind                 domain.MediaKind
	Extension            string
	SHA256               string
	ByteSize             int64
	MIMEType             string
	OriginalFilename     string
	Width                int
	Height               int
	DurationMilliseconds int64
	FPSNumerator         int64
	FPSDenominator       int64
	AudioSampleRate      int
	AudioChannels        int
}

type PrepareMediaRequest struct {
	ProjectRoot           string
	SourcePath            string
	OriginalFilename      string
	Kind                  domain.MediaKind
	RequiredWidth         int
	RequiredHeight        int
	CropToFill            bool
	TrimStartMilliseconds int64
	TrimEndMilliseconds   int64
}

type PrepareVideoFrameRequest struct {
	ProjectRoot        string
	SourceRelativePath string
	AtMilliseconds     int64
	OriginalFilename   string
	RequiredWidth      int
	RequiredHeight     int
}

type InstalledMedia struct {
	RelativePath string
}

// ProjectResult is the initial Project view returned by create and open.
type ProjectResult struct {
	Project  domain.Project
	Episodes []domain.EpisodeSummary
}

// EpisodeResult returns the changed Episode together with the refreshed
// lightweight Project view needed by the caller after a mutation.
type EpisodeResult struct {
	Project  domain.Project
	Episode  domain.Episode
	Episodes []domain.EpisodeSummary
}

type ImportedScript struct {
	Markdown       string
	Format         ScriptImportFormat
	CharacterCount int
}

type ImportScriptResult struct {
	EpisodeResult
	Format         ScriptImportFormat
	CharacterCount int
	Diagnostics    []domain.ScriptDiagnostic
}

type ValidateScriptResult struct {
	CharacterCount int
	Diagnostics    []domain.ScriptDiagnostic
}

type AssetResult struct {
	Project domain.Project
	Asset   domain.EpisodeAsset
}

type AssetIdentityResult struct {
	Project  domain.Project
	Identity domain.AssetIdentity
}

type ProjectTrashResult struct {
	Project             domain.Project
	Episodes            []domain.EpisodeSummary
	Assets              []domain.EpisodeAsset
	Scenes              []domain.StoryboardScene
	Clips               []domain.ClipDetails
	StoryboardRevisions map[domain.EpisodeID]int64
}

// ProjectPaths are the only paths Application needs from the filesystem
// adapter during project creation and opening.
type ProjectPaths struct {
	Root     string
	Database string
}

// ProjectLibraryRecord is a rebuildable Studio project-card projection.
type ProjectLibraryRecord struct {
	ProjectID        domain.ProjectID
	RootPath         string
	DisplayName      string
	CoverCacheKey    string
	Width            int
	Height           int
	EpisodeCount     int
	ProjectUpdatedAt time.Time
	OpenedAt         time.Time
}

// ProjectCatalogEntry is the transport-safe projection of one Studio project
// library record. Project content remains authoritative in project.db.
type ProjectCatalogEntry struct {
	ProjectID        domain.ProjectID
	RootPath         string
	DisplayName      string
	CoverCacheKey    string
	Width            int
	Height           int
	EpisodeCount     int
	ProjectUpdatedAt time.Time
	LastOpenedAt     *time.Time
}

// IDGenerator supplies stable typed IDs without coupling Application to a
// global random source.
type IDGenerator interface {
	NewProjectID() (domain.ProjectID, error)
	NewEpisodeID() (domain.EpisodeID, error)
	NewAssetID() (domain.AssetID, error)
	NewAssetIdentityID() (domain.AssetIdentityID, error)
	NewSceneID() (domain.SceneID, error)
	NewClipID() (domain.ClipID, error)
	NewArtifactID() (domain.ArtifactID, error)
	NewMediaVersionID() (domain.MediaVersionID, error)
	NewAudioReferenceAssetID() (domain.AudioReferenceAssetID, error)
	NewGenerationBatchID() (domain.GenerationBatchID, error)
	NewGenerationJobID() (domain.GenerationJobID, error)
	NewExportJobID() (domain.ExportJobID, error)
}

// Clock supplies the single timestamp used by one creation operation.
type Clock interface {
	Now() time.Time
}

// ProjectWorkspace owns safe project-directory creation, inspection, and
// cleanup of a directory created by the current failed operation.
type ProjectWorkspace interface {
	Create(rootPath string) (ProjectPaths, error)
	Open(rootPath string) (ProjectPaths, error)
	CleanupCreated(paths ProjectPaths) error
	Delete(paths ProjectPaths) error
}

// ProjectStore exposes only the project persistence needed by this use case.
type ProjectStore interface {
	InitializeProject(context.Context, domain.Project) error
	Initialize(context.Context, domain.Project, domain.Episode) error
	Project(context.Context) (domain.Project, error)
	UpdateProject(context.Context, domain.Project, int64) error
	Episodes(context.Context) ([]domain.EpisodeSummary, error)
	DeletedEpisodes(context.Context) ([]domain.EpisodeSummary, error)
	Episode(context.Context, domain.EpisodeID) (domain.Episode, error)
	EpisodeIncludingDeleted(context.Context, domain.EpisodeID) (domain.Episode, error)
	CreateEpisode(context.Context, domain.Episode, time.Time) error
	UpdateEpisode(context.Context, domain.EpisodeMetadata, int64, time.Time) error
	DeleteEpisode(context.Context, domain.EpisodeMetadata, int64, time.Time) error
	RestoreEpisode(context.Context, domain.EpisodeMetadata, int64, time.Time) error
	SaveScript(context.Context, domain.EpisodeID, domain.Script, int64, time.Time) error
	Asset(context.Context, domain.AssetID) (domain.EpisodeAsset, error)
	EpisodeAssets(context.Context, domain.EpisodeID, bool) ([]domain.EpisodeAsset, error)
	AssetIdentity(context.Context, domain.AssetIdentityID) (domain.AssetIdentity, error)
	CreateAsset(context.Context, *domain.AssetIdentity, domain.Asset, time.Time) error
	CreateDerivedAsset(context.Context, domain.Asset, domain.MediaVersionID, time.Time) error
	UpdateAsset(context.Context, domain.Asset, int64, time.Time) error
	DeleteAsset(context.Context, domain.Asset, int64, bool, time.Time) error
	UpdateAssetIdentity(context.Context, domain.AssetIdentity, int64, time.Time) error
	DetachAssetIdentity(context.Context, domain.AssetIdentity, domain.Asset, int64, time.Time) error
	Storyboard(context.Context, domain.EpisodeID) (domain.Storyboard, error)
	Scene(context.Context, domain.SceneID) (domain.Scene, error)
	SceneDetails(context.Context, domain.SceneID) (domain.StoryboardScene, error)
	Clip(context.Context, domain.ClipID) (domain.ClipDetails, error)
	CreateScene(context.Context, domain.Scene, int64, time.Time) error
	UpdateScene(context.Context, domain.Scene, int64, time.Time) error
	DeleteScene(context.Context, domain.Scene, int64, int64, bool, time.Time) error
	RestoreScene(context.Context, domain.Scene, int64, int64, time.Time) error
	ReorderScenes(context.Context, domain.EpisodeID, []domain.SceneID, int64, time.Time) error
	CreateClip(context.Context, domain.Clip, int64, time.Time) error
	UpdateClip(context.Context, domain.Clip, int64, time.Time) error
	MoveClip(context.Context, domain.Clip, domain.ClipID, domain.ClipID, int64, int64, time.Time) error
	DeleteClip(context.Context, domain.Clip, int64, int64, time.Time) error
	RestoreClip(context.Context, domain.Clip, int64, int64, time.Time) error
	BindClipAsset(context.Context, domain.ClipID, domain.AssetID, int64, time.Time) error
	UnbindClipAsset(context.Context, domain.ClipID, domain.AssetID, int64, time.Time) error
	ReplaceClipAsset(context.Context, domain.ClipID, domain.AssetID, domain.AssetID, int64, time.Time) error
	ReplaceClips(context.Context, []domain.Clip, []domain.ClipReplacement, int64, time.Time) error
	ResetStoryboard(context.Context, domain.EpisodeID, int64, bool, time.Time) ([]domain.GenerationJob, bool, error)
	AssetUsage(context.Context, domain.AssetID) ([]domain.Clip, error)
	MediaSlot(context.Context, domain.MediaSlotID, bool) (domain.MediaSlotDetails, error)
	Artifact(context.Context, domain.ArtifactID) (domain.Artifact, error)
	ArtifactSelectedInEpisode(context.Context, domain.ArtifactID, domain.EpisodeID) (bool, error)
	ArtifactBySHA256(context.Context, domain.ProjectID, string) (domain.Artifact, error)
	ClipReferenceArtifacts(context.Context, domain.ClipID) ([]domain.Artifact, error)
	EpisodeReferenceMedia(context.Context, domain.EpisodeID, domain.ArtifactID, bool) (domain.EpisodeReferenceMedia, error)
	EpisodeReferenceMediaList(context.Context, domain.EpisodeID, bool) ([]domain.EpisodeReferenceMedia, error)
	AgentConversationMediaList(context.Context, domain.EpisodeID, string) ([]domain.AgentConversationMedia, error)
	AgentConversationMediaForOperation(context.Context, domain.EpisodeID, string, string) ([]domain.AgentConversationMedia, error)
	PutAgentConversationMedia(context.Context, domain.AgentConversationMedia, time.Time) error
	BindAgentConversationMedia(context.Context, domain.EpisodeID, string, string, string, []domain.ArtifactID, time.Time) error
	PutEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, time.Time) error
	UpdateEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, int64, time.Time) error
	DeleteEpisodeReferenceMedia(context.Context, domain.EpisodeReferenceMedia, int64, time.Time) error
	AddClipReferenceArtifact(context.Context, domain.ClipID, domain.Artifact, time.Time) error
	DeleteClipReferenceArtifact(context.Context, domain.ClipID, domain.ArtifactID, time.Time) error
	AddMediaVersion(context.Context, domain.Artifact, domain.MediaVersion, int64, bool, time.Time) error
	SelectMediaVersion(context.Context, domain.MediaSlotID, domain.MediaVersionID, int64, time.Time) error
	UpdateVideoTrim(context.Context, domain.MediaVersion, int64, time.Time) error
	DeleteMediaVersion(context.Context, domain.MediaVersion, int64, int64, time.Time) error
	RestoreMediaVersion(context.Context, domain.MediaVersion, int64, int64, time.Time) error
	AudioReferenceAsset(context.Context, domain.AudioReferenceAssetID) (domain.AudioReferenceAsset, error)
	EpisodeAudioReferenceAssets(context.Context, domain.EpisodeID, bool) ([]domain.AudioReferenceAsset, error)
	CreateAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, time.Time) error
	UpdateAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error
	DeleteAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error
	RestoreAudioReferenceAsset(context.Context, domain.AudioReferenceAsset, int64, time.Time) error
	ClipAudioReference(context.Context, domain.ClipID) (domain.AudioReferenceAssetID, error)
	SetClipAudioReference(context.Context, domain.ClipID, domain.AudioReferenceAssetID, int64, time.Time) error
	ClearClipAudioReference(context.Context, domain.ClipID, int64, time.Time) error
	GenerationBatch(context.Context, domain.GenerationBatchID) (domain.GenerationBatchDetails, error)
	GenerationBatchByOperationKey(context.Context, domain.ProjectID, string) (domain.GenerationBatchDetails, error)
	GenerationJob(context.Context, domain.GenerationJobID) (domain.GenerationJob, error)
	GenerationJobsByStates(context.Context, []domain.GenerationJobState, int) ([]domain.GenerationJob, error)
	RecentGenerationJobs(context.Context, int) ([]domain.GenerationJob, error)
	CreateGenerationBatch(context.Context, domain.GenerationBatch, []domain.GenerationJob, time.Time) error
	ClaimGenerationJob(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error)
	RecordGenerationOperation(context.Context, domain.GenerationJobID, int64, string, string, string, time.Time) (domain.GenerationJob, error)
	UpdateGenerationProgress(context.Context, domain.GenerationJobID, int64, string, string, time.Time) (domain.GenerationJob, error)
	CancelGenerationJob(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error)
	RequestGenerationStop(context.Context, domain.GenerationJobID, int64, time.Time) (domain.GenerationJob, error)
	FinishGenerationJob(context.Context, domain.GenerationJobID, int64, domain.GenerationJobState, string, string, time.Time) (domain.GenerationJob, error)
	CompleteGenerationJob(context.Context, domain.GenerationJobID, int64, domain.Artifact, domain.MediaVersionID, time.Time) error
	ExportJob(context.Context, domain.ExportJobID) (domain.ExportJob, error)
	ExportJobByOperationKey(context.Context, domain.ProjectID, string) (domain.ExportJob, error)
	ExportJobsByStates(context.Context, []domain.ExportJobState, int) ([]domain.ExportJob, error)
	RecentExportJobs(context.Context, int) ([]domain.ExportJob, error)
	CreateExportJob(context.Context, domain.ExportJob) error
	ClaimExportJob(context.Context, domain.ExportJobID, int64, time.Time) (domain.ExportJob, error)
	UpdateExportProgress(context.Context, domain.ExportJobID, int64, string, string, time.Time) (domain.ExportJob, error)
	RequestExportCancel(context.Context, domain.ExportJobID, int64, time.Time) (domain.ExportJob, error)
	FinishExportJob(context.Context, domain.ExportJobID, int64, domain.ExportJobState, string, string, string, time.Time) (domain.ExportJob, error)
	Close() error
}

// ProjectStoreFactory explicitly distinguishes new databases from existing
// databases.
type ProjectStoreFactory interface {
	Create(context.Context, string) (ProjectStore, error)
	Open(context.Context, string) (ProjectStore, error)
}

// ProjectLibrary synchronizes a rebuildable project-card record with studio.db.
type ProjectLibrary interface {
	SyncProject(context.Context, ProjectLibraryRecord) error
	Project(context.Context, domain.ProjectID) (ProjectCatalogEntry, error)
	CanDeleteProject(context.Context, domain.ProjectID) error
	CanDeleteEpisode(context.Context, domain.ProjectID, domain.EpisodeID) error
	DeleteProject(context.Context, domain.ProjectID) error
}

type ProjectCatalog interface {
	Projects(context.Context) ([]ProjectCatalogEntry, error)
	Project(context.Context, domain.ProjectID) (ProjectCatalogEntry, error)
}

// ScriptImporter reads one external file into normalized Markdown. It never
// retains or writes the source file.
type ScriptImporter interface {
	Import(context.Context, string) (ImportedScript, error)
}

// MediaFileProcessor validates imported media, owns staging files, and moves
// validated bytes atomically into the portable project directory.
type MediaFileProcessor interface {
	Prepare(context.Context, PrepareMediaRequest) (PreparedMedia, error)
	PrepareVideoFrame(context.Context, PrepareVideoFrameRequest) (PreparedMedia, error)
	Install(context.Context, string, PreparedMedia, domain.ArtifactID) (InstalledMedia, error)
	Discard(string, PreparedMedia) error
	Remove(string, InstalledMedia) error
}

type GenerationReferenceCapability struct {
	Role            domain.GenerationArtifactRole
	GenerationModes []domain.ClipGenerationMode
	MinimumCount    int
	MaximumCount    int
	// MaximumBytes applies to the prepared upload derivative. A larger source
	// Artifact is accepted only when SupportsAutomaticPreparation is true.
	MaximumBytes                     int64
	MinimumDurationMilliseconds      int64
	MaximumDurationMilliseconds      int64
	MaximumTotalDurationMilliseconds int64
	AcceptedMIMETypes                []string
	RequiresAnyOf                    []domain.GenerationArtifactRole
	// Automatic preparation may transcode, compress, resize, or apply an
	// explicitly selected range. It never chooses or shortens a range itself.
	SupportsAutomaticPreparation bool
}

type GenerationCapability struct {
	GeneratorID                                     domain.GeneratorID
	ProviderModelID                                 string
	ProviderConnectionRevision                      int64
	CapabilityVersion                               string
	MediaKind                                       domain.MediaKind
	SupportedVideoModes                             []domain.ClipGenerationMode
	SupportedDurations                              []int64
	MinimumDurationSeconds                          int64
	MaximumDurationSeconds                          int64
	DurationStepSeconds                             int64
	SupportedOutputSizes                            []string
	ReferenceCapabilities                           []GenerationReferenceCapability
	MinimumTotalReferences                          int
	MaximumTotalReferences                          int
	MaximumTotalTimedReferenceDurationMilliseconds  int64
	MaximumInputVideoPlusOutputDurationMilliseconds int64
	SupportsStop                                    bool
	SupportsRecovery                                bool
}

type GenerationCapabilityIssue struct {
	Constraint string
	Role       domain.GenerationArtifactRole
	Actual     int64
	Minimum    int64
	Maximum    int64
}

func (e *GenerationCapabilityIssue) Error() string {
	if e == nil {
		return "generation capability limit was exceeded"
	}
	return fmt.Sprintf("generation capability %s for %s: actual=%d minimum=%d maximum=%d", e.Constraint, e.Role, e.Actual, e.Minimum, e.Maximum)
}

type GenerationCapabilityCatalog interface {
	Generator(context.Context, domain.GeneratorID) (GenerationCapability, error)
}

type ModelGenerationCapabilityCatalog interface {
	GeneratorModel(context.Context, domain.GeneratorID, string) (GenerationCapability, error)
}

// GenerationQueueNotifier wakes the scheduler after durable queue changes.
// Queued Jobs remain recoverable when notification fails or is coalesced.
type GenerationQueueNotifier interface {
	Notify(domain.ProjectID)
}

// GenerationEventPublisher projects durable Job changes to the process-local
// Global Task SSE stream. SQLite remains authoritative when events are lost.
type GenerationEventPublisher interface {
	PublishGenerationJob(domain.GenerationJob)
}

type ExportQueueNotifier interface {
	NotifyExport(domain.ProjectID, domain.ExportJobID)
}

type ExportEventPublisher interface {
	PublishExportJob(domain.ExportJob)
}

// RegistrationError preserves the valid project path when only the Studio
// project-library synchronization failed.
type RegistrationError struct {
	Category error
	RootPath string
	Cause    error
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("%v at %q: %v", e.Category, e.RootPath, e.Cause)
}

func (e *RegistrationError) Unwrap() []error {
	return []error{e.Category, e.Cause}
}
