package changeset

import (
	"errors"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const suffix = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestOperationUnionValidation(t *testing.T) {
	t.Parallel()
	operationID := OperationID("cop_" + suffix)
	changeSetID := ChangeSetID("cst_" + suffix)
	operation := Operation{ID: operationID, ChangeSetID: changeSetID, Position: 1, Type: UpdateAssetPrompt, Precondition: Precondition{ID: PreconditionID("cpc_" + suffix), OperationID: operationID, TargetKind: TargetAsset, TargetID: "ast_" + suffix, ExpectedRevision: 2}, AssetPrompt: &AssetPromptChange{AssetID: project.AssetID("ast_" + suffix), Before: "before", After: "after"}}
	if err := operation.Validate(); err != nil {
		t.Fatalf("operation error = %v", err)
	}
	copy := operation
	copy.ClipPrompts = &ClipPromptsChange{}
	if err := copy.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("union error = %v", err)
	}
}

func TestAssetCreationOperationValidation(t *testing.T) {
	t.Parallel()
	operationID := OperationID("cop_" + suffix)
	operation := Operation{
		ID: operationID, ChangeSetID: ChangeSetID("cst_" + suffix), Position: 1, Type: CreateEpisodeAsset,
		Precondition:  Precondition{ID: PreconditionID("cpc_" + suffix), OperationID: operationID, TargetKind: TargetEpisodeScript, TargetID: "ep_" + suffix, ExpectedRevision: 3},
		AssetCreation: &AssetCreation{AssetID: project.AssetID("ast_" + suffix), IdentityID: project.AssetIdentityID("asi_" + suffix), Kind: project.AssetKindLocation, Name: "天台", Prompt: "雨夜天台"},
	}
	if err := operation.Validate(); err != nil {
		t.Fatalf("new Asset operation error = %v", err)
	}
	derived := operation
	derived.AssetCreation = &AssetCreation{AssetID: project.AssetID("ast_" + suffix), IdentityID: project.AssetIdentityID("asi_" + suffix), SourceAssetID: project.AssetID("ast_ZYXWVUTSRQPONMLKJIHGFEDCBA"), SourceAssetRevision: 2, InheritedMediaVersionID: project.MediaVersionID("mvr_" + suffix), Kind: project.AssetKindCharacter, Name: "顾言", Prompt: "新造型"}
	if err := derived.Validate(); err != nil {
		t.Fatalf("derived Asset operation error = %v", err)
	}
	derived.AssetCreation.InheritedMediaVersionID = ""
	if err := derived.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("missing inherited media ID error = %v", err)
	}
}

func TestClipPromptOperationDoesNotHideBindingOnlyMutation(t *testing.T) {
	t.Parallel()
	operationID := OperationID("cop_" + suffix)
	clipID := project.ClipID("clp_" + suffix)
	assetID := project.AssetID("ast_" + suffix)
	operation := Operation{
		ID: operationID, ChangeSetID: ChangeSetID("cst_" + suffix), Position: 1, Type: UpdateClipPrompts,
		Precondition: Precondition{ID: PreconditionID("cpc_" + suffix), OperationID: operationID, TargetKind: TargetClip, TargetID: clipID.String(), ExpectedRevision: 2},
		ClipPrompts: &ClipPromptsChange{
			ClipID: clipID, VideoMode: project.ClipGenerationReference, VideoBefore: "人物缓慢抬头。", VideoAfter: "人物缓慢抬头。",
			AssetIDsAfter: []project.AssetID{assetID}, AssetBindingsRecorded: true,
		},
	}
	if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("binding-only Prompt operation error = %v", err)
	}
	operation.ClipPrompts.VideoAfter = "@林黛玉缓慢抬头。"
	if err := operation.Validate(); err != nil {
		t.Fatalf("Prompt and binding operation error = %v", err)
	}
}

func TestChangeSetLifecycleShape(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_000).UTC()
	value := ChangeSet{ID: "cst_" + suffix, ProjectID: "prj_" + suffix, EpisodeID: "ep_" + suffix, Source: Source{AgentSessionID: agent.SessionID("ags_" + suffix), AgentTurnID: agent.TurnID("agt_" + suffix), CodexThreadID: "thr_1", CodexTurnID: "turn_1"}, Title: "Prompts", Status: StatusDraft, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := value.ValidateNew(); err != nil {
		t.Fatalf("draft error = %v", err)
	}
	value.Status = StatusPending
	if err := value.Validate(); !errors.Is(err, ErrInvalidChangeSet) {
		t.Fatalf("invalid pending error = %v", err)
	}
}
