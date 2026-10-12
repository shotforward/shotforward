// Package changesetapp coordinates Agent drafting and user review of ChangeSets.
package changesetapp

import (
	"context"
	"errors"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrChangeSetNotFound           = errors.New("ChangeSet not found")
	ErrChangeSetRevisionConflict   = errors.New("ChangeSet revision conflict")
	ErrChangeSetStateConflict      = errors.New("ChangeSet state conflict")
	ErrChangeSetOperationConflict  = errors.New("ChangeSet operation key conflict")
	ErrChangeSetConflicted         = errors.New("ChangeSet conflicted")
	ErrChangeSetPreconditionFailed = errors.New("ChangeSet precondition no longer holds")
	ErrChangeSetIdentityConflict   = errors.New("ChangeSet identity exists in multiple projects")
)

type IDGenerator interface {
	NewChangeSetID() (changeset.ChangeSetID, error)
	NewOperationID() (changeset.OperationID, error)
	NewPreconditionID() (changeset.PreconditionID, error)
	NewConflictID() (changeset.ConflictID, error)
	NewAssetID() (project.AssetID, error)
	NewAssetIdentityID() (project.AssetIdentityID, error)
	NewMediaVersionID() (project.MediaVersionID, error)
}

type StoryboardIDGenerator interface {
	NewSceneID() (project.SceneID, error)
	NewClipID() (project.ClipID, error)
}

type Clock interface{ Now() time.Time }

type Store interface {
	CreateChangeSet(context.Context, changeset.ChangeSet, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)
	ChangeSet(context.Context, changeset.ChangeSetID) (changeset.ChangeSet, error)
	AppendChangeSetOperations(context.Context, changeset.ChangeSetID, int64, []changeset.Operation, time.Time, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)
	FinalizeChangeSet(context.Context, changeset.ChangeSetID, int64, time.Time, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)
	ApplyChangeSet(context.Context, changeset.ChangeSetID, int64, changeset.ConflictID, time.Time, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)
	RejectChangeSet(context.Context, changeset.ChangeSetID, int64, string, time.Time, changeset.CommandReceipt) (changeset.ChangeSet, bool, error)
	DiscardChangeSetDraft(context.Context, changeset.ChangeSetID) error
	DiscardChangeSetDraftsForTurn(context.Context, agent.TurnID) error
	ChangeSetOperations(context.Context, changeset.ChangeSetID, int, int) ([]changeset.Operation, error)
	ChangeSetConflicts(context.Context, changeset.ChangeSetID) ([]changeset.Conflict, error)
}

type StoreResolver interface {
	ResolveChangeSetStore(context.Context, project.ProjectID) (Store, error)
}

type Locator interface {
	LocateChangeSet(context.Context, changeset.ChangeSetID) (project.ProjectID, error)
}

// DraftCommands is the only mutation surface exposed to ShotForward MCP.
type DraftCommands interface {
	BeginDraft(context.Context, BeginDraftInput) (CommandResult, error)
	AppendOperations(context.Context, AppendOperationsInput) (CommandResult, error)
	Finalize(context.Context, FinalizeInput) (CommandResult, error)
	Apply(context.Context, ApplyInput) (CommandResult, error)
}

// DraftCleanup is an internal lifecycle port used when a Turn ends before
// finalize. It is never published as an MCP tool.
type DraftCleanup interface {
	DiscardDraft(context.Context, project.ProjectID, changeset.ChangeSetID) error
	DiscardTurnDrafts(context.Context, project.ProjectID, agent.TurnID) error
}

// ReviewCommands is wired only to authenticated local UI endpoints.
type ReviewCommands interface {
	Apply(context.Context, ApplyInput) (CommandResult, error)
	Reject(context.Context, RejectInput) (CommandResult, error)
	Undo(context.Context, UndoInput) (CommandResult, error)
}

type BeginDraftInput struct {
	ProjectID      project.ProjectID
	EpisodeID      project.EpisodeID
	AgentSessionID agent.SessionID
	AgentTurnID    agent.TurnID
	CodexThreadID  string
	CodexTurnID    string
	Title          string
	Summary        string
	OperationKey   string
}

type OperationDraft struct {
	Type                   changeset.OperationType
	ExpectedRevision       int64
	ExpectedScriptRevision int64
	AssetPrompt            *changeset.AssetPromptChange
	ClipPrompts            *changeset.ClipPromptsChange
	AssetCreation          *changeset.AssetCreation
	AssetRestoration       *changeset.AssetRestoration
	SceneCreation          *SceneCreationDraft
	ClipCreation           *ClipCreationDraft
	ClipAssetRef           *ClipAssetReferenceDraft
	ClipMerge              *ClipMergeDraft
	ClipSplit              *ClipSplitDraft
	EpisodeScript          *changeset.EpisodeScriptChange
	ChangeSetRevert        *changeset.ChangeSetRevert
}

type SceneCreationDraft struct {
	Ref         string
	Position    int
	Title       string
	Description string
	Source      *project.SceneScriptSource
}

type ClipCreationDraft struct {
	Ref                    string
	SceneRef               string
	ExistingSceneID        project.SceneID
	Position               int
	Title                  string
	Description            string
	Dialogue               string
	CreativeIntent         string
	PlannedDurationSeconds int64
	FirstImagePrompt       string
	LastImagePrompt        string
	VideoPrompt            string
	GenerationMode         project.ClipGenerationMode
}

type ClipAssetReferenceDraft struct {
	ClipRef  string
	AssetID  project.AssetID
	Position int
}

type ClipMergeFrameDraft struct {
	Slot    project.MediaSlot
	Current *project.MediaVersion
}

type ClipMergeDraft struct {
	Sources          []project.Clip
	Title            string
	Description      string
	Dialogue         string
	CreativeIntent   string
	FirstImagePrompt string
	LastImagePrompt  string
	VideoPrompt      string
	GenerationMode   project.ClipGenerationMode
	AssetIDs         []project.AssetID
	FirstFrame       ClipMergeFrameDraft
	LastFrame        ClipMergeFrameDraft
}

type ClipSplitReplacementDraft struct {
	Title                  string
	Description            string
	Dialogue               string
	CreativeIntent         string
	PlannedDurationSeconds int64
	FirstImagePrompt       string
	LastImagePrompt        string
	VideoPrompt            string
	GenerationMode         project.ClipGenerationMode
	AssetIDs               []project.AssetID
}

type ClipSplitDraft struct {
	Source       project.Clip
	Replacements []ClipSplitReplacementDraft
	FirstFrame   ClipMergeFrameDraft
	LastFrame    ClipMergeFrameDraft
}

type AppendOperationsInput struct {
	ProjectID        project.ProjectID
	ChangeSetID      changeset.ChangeSetID
	ExpectedRevision int64
	Operations       []OperationDraft
	OperationKey     string
}

type FinalizeInput struct {
	ProjectID        project.ProjectID
	ChangeSetID      changeset.ChangeSetID
	ExpectedRevision int64
	OperationKey     string
}
type ApplyInput struct {
	ProjectID        project.ProjectID
	ChangeSetID      changeset.ChangeSetID
	ExpectedRevision int64
	OperationKey     string
}
type RejectInput struct {
	ProjectID        project.ProjectID
	ChangeSetID      changeset.ChangeSetID
	ExpectedRevision int64
	Reason           string
	OperationKey     string
}

type UndoInput struct {
	ProjectID    project.ProjectID
	ChangeSetID  changeset.ChangeSetID
	OperationKey string
}

type CommandResult struct {
	ChangeSet changeset.ChangeSet
	Replayed  bool
}
