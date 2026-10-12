// Package agentmcp exposes the deliberately small, Session-scoped MCP surface
// used by ShotForward's local Codex Runtime.
package agentmcp

import (
	"context"
	"errors"
	"io"

	"github.com/shotforward/shotforward/daemon/internal/agentimage"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	DefaultTokenEnvironmentVariable = "SHOTFORWARD_MCP_TOKEN"
	MaxRequestBytes                 = 512 * 1024
	MaxToolResultBytes              = 64 * 1024
	MaxAssetPageSize                = 50
	MaxReferenceMediaPageSize       = 50
	MaxPriorAssetPageSize           = 50
	MaxScenePageSize                = 20
	MaxClipPageSize                 = 50
	MaxScriptPageCharacters         = 12_000
	MaxAssetOperationsPerChangeSet  = 1
	MaxCursorsPerBinding            = 128
)

var (
	ErrInvalidConfiguration   = errors.New("invalid ShotForward MCP configuration")
	ErrInvalidBinding         = errors.New("invalid ShotForward MCP binding")
	ErrBindingConflict        = errors.New("ShotForward MCP binding conflict")
	ErrBindingNotFound        = errors.New("ShotForward MCP binding not found")
	ErrTurnNotActive          = errors.New("Agent Turn is not active for MCP")
	ErrTurnConflict           = errors.New("another Agent Turn is active for MCP")
	ErrObjectNotObserved      = errors.New("object was not observed in this Turn")
	ErrFieldNotObserved       = errors.New("field was not observed in this Turn")
	ErrObservationStale       = errors.New("Turn observation is stale")
	ErrScriptNotObserved      = errors.New("complete script was not observed in this Turn")
	ErrAssetListNotObserved   = errors.New("complete Asset list was not observed in this Turn")
	ErrSourceAssetNotObserved = errors.New("earlier-Episode source Asset was not observed in this Turn")
	ErrAssetAlreadyPresent    = errors.New("Asset is already present in this Episode")
	ErrCursorInvalid          = errors.New("invalid MCP cursor")
	ErrCursorStale            = errors.New("stale MCP cursor")
	ErrResultTooLarge         = errors.New("MCP tool result exceeds size limit")
	ErrInvalidArguments       = errors.New("invalid MCP tool arguments")
)

// ProjectQueries deliberately contains only the first MCP vertical slice.
// projectapp.Service satisfies this interface without exposing mutations.
type ProjectQueries interface {
	GetEpisode(context.Context, projectapp.GetEpisodeInput) (project.Episode, error)
	ListEpisodeAssets(context.Context, projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error)
	FindPriorAssetCandidates(context.Context, projectapp.FindPriorAssetCandidatesInput) ([]projectapp.PriorAssetCandidate, error)
	GetAsset(context.Context, projectapp.GetAssetInput) (project.EpisodeAsset, error)
	GetClip(context.Context, projectapp.GetClipInput) (project.ClipDetails, error)
	GetStoryboard(context.Context, projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error)
	GetMediaSlot(context.Context, projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error)
	ListClipReferenceMedia(context.Context, projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error)
	ListEpisodeReferenceMedia(context.Context, projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error)
}

type Options struct {
	Queries                  ProjectQueries
	Drafts                   changesetapp.DraftCommands
	DraftCleanup             changesetapp.DraftCleanup
	Images                   ImageCommands
	Events                   agentapp.EventPublisher
	Random                   io.Reader
	TokenEnvironmentVariable string
}

type ImageCommands interface {
	Generate(context.Context, agentimage.Input) (agentimage.Result, error)
}

// BindingScope is immutable for the lifetime of a registered route.
type BindingScope struct {
	RouteKey  string
	SessionID agent.SessionID
	ProjectID project.ProjectID
	EpisodeID project.EpisodeID
}

// ActiveTurn is supplied by the scheduler only after Codex has assigned its
// Turn ID. Tool calls never accept these identity fields from the model.
type ActiveTurn struct {
	AgentTurnID       agent.TurnID
	CodexThreadID     string
	CodexTurnID       string
	RuntimeKind       agentapp.RuntimeKind
	ArtifactIDs       []project.ArtifactID
	Focus             agent.FocusSnapshot
	VideoPromptTarget *VideoPromptContext
}

type Credential struct {
	EnvironmentVariable string
	Value               string
}

type toolFailure struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	SourceAssetID string `json:"source_asset_id,omitempty"`
	NextTool      string `json:"next_tool,omitempty"`
}
