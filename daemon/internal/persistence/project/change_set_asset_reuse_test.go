package projectstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestUndoReusedAssetRemovesOnlyTargetEpisodeAppearance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	sourceSlotID, _ := project.FixedMediaSlotID(fixture.asset.ID.String(), project.MediaSlotAssetPrimaryImage)
	at := fixture.nextTime()
	artifact := project.Artifact{
		ID: project.ArtifactID("art_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID,
		Kind: project.MediaKindImage, RelativePath: "media/images/reuse-source.png",
		SHA256: strings.Repeat("d", 64), ByteSize: 10, MIMEType: "image/png",
		OriginalFilename: "reuse-source.png", Width: 720, Height: 1280, CreatedAt: at,
	}
	sourceVersion := testMediaVersion(t, sourceSlotID, artifact.ID, "D", 1, 0, at)
	if err := fixture.store.AddMediaVersion(ctx, artifact, sourceVersion, 1, true, at); err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	target := project.Episode{
		EpisodeMetadata: project.EpisodeMetadata{
			ID: project.EpisodeID("ep_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID,
			SequenceNumber: 2, Title: "婚礼", TargetDuration: time.Minute,
			Revision: 1, CreatedAt: at, UpdatedAt: at,
		},
		Script: project.Script{Markdown: "角色换上婚礼西装。", Revision: 1, UpdatedAt: at},
	}
	if err := fixture.store.CreateEpisode(ctx, target, at); err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID,
		EpisodeID: target.ID, Source: changeset.Source{
			AgentSessionID: agent.SessionID("ags_" + strings.Repeat("D", 26)),
			AgentTurnID:    agent.TurnID("agt_" + strings.Repeat("D", 26)),
			CodexThreadID:  "thr_reuse", CodexTurnID: "turn_reuse",
		},
		Title: "复用角色", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	value, _, err := fixture.store.CreateChangeSet(ctx, value, fixture.receipt(value.ID, changeset.CommandBegin, "reuse-begin", "1", at))
	if err != nil {
		t.Fatal(err)
	}
	operationID := changeset.OperationID("cop_" + strings.Repeat("D", 26))
	assetID := project.AssetID("ast_" + strings.Repeat("D", 26))
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateEpisodeAsset,
		Precondition: changeset.Precondition{
			ID: changeset.PreconditionID("cpc_" + strings.Repeat("D", 26)), OperationID: operationID,
			TargetKind: changeset.TargetEpisodeScript, TargetID: target.ID.String(), ExpectedRevision: 1,
		},
		AssetCreation: &changeset.AssetCreation{
			AssetID: assetID, IdentityID: fixture.asset.IdentityID, SourceAssetID: fixture.asset.ID,
			SourceAssetRevision:     fixture.asset.Revision,
			InheritedMediaVersionID: project.MediaVersionID("mvr_" + strings.Repeat("E", 26)),
			Kind:                    project.AssetKindCharacter, Name: "角色", Prompt: "中年，白色婚礼西装",
		},
	}
	value = fixture.append(t, value, []changeset.Operation{operation}, "reuse-append", "2")
	value = fixture.finalize(t, value, "reuse-finalize", "3")
	at = fixture.nextTime()
	applied, _, err := fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision,
		changeset.ConflictID("ccf_"+strings.Repeat("D", 26)), at,
		fixture.receipt(value.ID, changeset.CommandApply, "reuse-apply", "4", at))
	if err != nil || applied.Status != changeset.StatusApplied {
		t.Fatalf("apply reuse = %+v, %v", applied, err)
	}
	targetSlotID, _ := project.FixedMediaSlotID(assetID.String(), project.MediaSlotAssetPrimaryImage)
	targetMedia, err := fixture.store.MediaSlot(ctx, targetSlotID, true)
	if err != nil || targetMedia.Current == nil || targetMedia.Current.ArtifactID != artifact.ID ||
		targetMedia.Current.ID == sourceVersion.ID || targetMedia.Current.SlotID == sourceVersion.SlotID {
		t.Fatalf("target should own the inherited image version: %+v, %v", targetMedia, err)
	}
	// A later edit to the source Episode must neither synchronize into the
	// reused appearance nor prevent undoing that appearance's creation.
	at = fixture.nextTime()
	source := fixture.asset
	source.Prompt = "老年，黑色大衣"
	source.Revision++
	source.UpdatedAt = at
	if err := fixture.store.UpdateAsset(ctx, source, fixture.asset.Revision, at); err != nil {
		t.Fatal(err)
	}
	targetAsset, err := fixture.store.Asset(ctx, assetID)
	if err != nil || targetAsset.Asset.Prompt != operation.AssetCreation.Prompt {
		t.Fatalf("source edit changed target appearance: %+v, %v", targetAsset, err)
	}
	at = fixture.nextTime()
	undo := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + strings.Repeat("U", 26)), ProjectID: fixture.project.ID,
		EpisodeID: target.ID, Source: applied.Source, Title: "撤销角色复用",
		Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	undo, _, err = fixture.store.CreateChangeSet(ctx, undo, fixture.receipt(undo.ID, changeset.CommandBegin, "reuse-undo-begin", "5", at))
	if err != nil {
		t.Fatal(err)
	}
	revertID := changeset.OperationID("cop_" + strings.Repeat("U", 26))
	undo = fixture.append(t, undo, []changeset.Operation{{
		ID: revertID, ChangeSetID: undo.ID, Position: 1, Type: changeset.RevertChangeSet,
		Precondition: changeset.Precondition{
			ID: changeset.PreconditionID("cpc_" + strings.Repeat("U", 26)), OperationID: revertID,
			TargetKind: changeset.TargetChangeSet, TargetID: applied.ID.String(), ExpectedRevision: applied.Revision,
		},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: applied.ID},
	}}, "reuse-undo-append", "6")
	undo = fixture.finalize(t, undo, "reuse-undo-finalize", "7")
	at = fixture.nextTime()
	undo, _, err = fixture.store.ApplyChangeSet(ctx, undo.ID, undo.Revision,
		changeset.ConflictID("ccf_"+strings.Repeat("U", 26)), at,
		fixture.receipt(undo.ID, changeset.CommandApply, "reuse-undo-apply", "8", at))
	if err != nil || undo.Status != changeset.StatusApplied {
		t.Fatalf("undo reuse = %+v, %v", undo, err)
	}
	if _, err := fixture.store.Asset(ctx, assetID); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("target appearance survived undo: %v", err)
	}
	assets, err := fixture.store.EpisodeAssets(ctx, target.ID, false)
	if err != nil || len(assets) != 0 {
		t.Fatalf("target Episode after undo = %+v, %v", assets, err)
	}
	remaining, err := fixture.store.Asset(ctx, fixture.asset.ID)
	if err != nil || remaining.Asset.Prompt != source.Prompt || remaining.Asset.Revision != source.Revision ||
		remaining.Asset.IsDeleted() || remaining.Identity.ID != fixture.asset.IdentityID {
		t.Fatalf("source appearance was modified by target undo: %+v, %v", remaining, err)
	}
	sourceMedia, err := fixture.store.MediaSlot(ctx, sourceSlotID, true)
	if err != nil || sourceMedia.Current == nil || sourceMedia.Current.ID != sourceVersion.ID ||
		sourceMedia.Current.ArtifactID != artifact.ID || len(sourceMedia.Versions) != 1 {
		t.Fatalf("source image was modified by target undo: %+v, %v", sourceMedia, err)
	}
}
