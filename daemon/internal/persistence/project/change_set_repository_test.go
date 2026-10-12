package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestChangeSetHappyPathAndIdempotency(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	value := fixture.begin(t, "begin-1", "a")
	operations := fixture.operations(value.ID)[1:]
	operations[0].Position = 1
	value = fixture.append(t, value, operations, "append-1", "b")
	if value.Status != changeset.StatusDraft || value.OperationCount != 1 || value.Revision != 2 {
		t.Fatalf("after append = %+v", value)
	}
	value = fixture.finalize(t, value, "finalize-1", "c")
	if value.Status != changeset.StatusPending || value.Revision != 3 || value.FinalizedAt == nil {
		t.Fatalf("after finalize = %+v", value)
	}

	if _, err := fixture.store.database.ExecContext(context.Background(), `UPDATE change_set_operations SET clip_video_prompt_after='bypass' WHERE operation_id=?`, operations[0].ID.String()); err == nil {
		t.Fatal("frozen operation update unexpectedly succeeded")
	}

	at := fixture.nextTime()
	receipt := fixture.receipt(value.ID, changeset.CommandApply, "apply-1", "d", at)
	applied, replay, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("C", 26)), at, receipt)
	if err != nil || replay || applied.Status != changeset.StatusApplied || applied.Revision != 4 {
		t.Fatalf("ApplyChangeSet() = %+v, replay=%v, err=%v", applied, replay, err)
	}
	clip, err := fixture.store.Clip(context.Background(), fixture.clip.ID)
	if err != nil || clip.Clip.FirstImagePrompt != "new first" || clip.Clip.LastImagePrompt != "new last" || clip.Clip.VideoPrompts.Reference != "new video" || clip.Clip.VideoPrompts.FirstImage != "first-mode untouched" || clip.Clip.VideoPrompts.FirstLastImage != "first-last-mode untouched" || clip.Clip.Revision != 2 {
		t.Fatalf("Clip after apply = %+v, %v", clip, err)
	}
	replayed, replay, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("D", 26)), at, receipt)
	if err != nil || !replay || replayed.Status != changeset.StatusApplied {
		t.Fatalf("replayed ApplyChangeSet() = %+v, replay=%v, err=%v", replayed, replay, err)
	}

	badReceipt := receipt
	badReceipt.RequestHash = strings.Repeat("e", 64)
	_, _, err = fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("E", 26)), at, badReceipt)
	if !errors.Is(err, ErrChangeSetOperationConflict) {
		t.Fatalf("operation key conflict error = %v", err)
	}
}

func TestClipPromptChangeSetAppliesUploadedAudioBindingAtomically(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	audioID := project.ArtifactID("art_" + strings.Repeat("U", 26))
	at := fixture.nextTime()
	audio := project.Artifact{
		ID: audioID, ProjectID: fixture.project.ID, Kind: project.MediaKindAudio,
		RelativePath: "media/audio/bubu.mp3", SHA256: strings.Repeat("a", 64), ByteSize: 128,
		MIMEType: "audio/mpeg", OriginalFilename: "bubu.mp3", DurationMilliseconds: 15_000,
		AudioSampleRate: 44_100, AudioChannels: 1, CreatedAt: at,
	}
	if err := fixture.store.PutEpisodeReferenceMedia(context.Background(), project.EpisodeReferenceMedia{
		EpisodeID: fixture.episode.ID, Artifact: audio, Name: "布布配音", Description: "对白音色参考。",
		Revision: 1, CreatedAt: at, UpdatedAt: at,
	}, at); err != nil {
		t.Fatal(err)
	}
	value := fixture.begin(t, "begin-audio-binding", "1")
	operationID := changeset.OperationID("cop_" + strings.Repeat("U", 26))
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.UpdateClipPrompts,
		Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("U", 26)), OperationID: operationID, TargetKind: changeset.TargetClip, TargetID: fixture.clip.ID.String(), ExpectedRevision: 1},
		ClipPrompts: &changeset.ClipPromptsChange{
			ClipID: fixture.clip.ID, VideoMode: project.ClipGenerationReference,
			FirstBefore: "old first", FirstAfter: "old first", LastBefore: "old last", LastAfter: "old last",
			VideoBefore: "old video", VideoAfter: "@布布配音 作为人物对白音色参考。",
			ReferenceArtifactIDsAfter: []project.ArtifactID{audioID}, ReferenceBindingsRecorded: true,
		},
	}
	value = fixture.append(t, value, []changeset.Operation{operation}, "append-audio-binding", "2")
	value = fixture.finalize(t, value, "finalize-audio-binding", "3")
	applyAt := fixture.nextTime()
	applied, replay, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("U", 26)), applyAt, fixture.receipt(value.ID, changeset.CommandApply, "apply-audio-binding", "4", applyAt))
	if err != nil || replay || applied.Status != changeset.StatusApplied {
		t.Fatalf("ApplyChangeSet() = %+v, replay=%v, err=%v", applied, replay, err)
	}
	clip, err := fixture.store.Clip(context.Background(), fixture.clip.ID)
	if err != nil || clip.Clip.VideoPrompts.Reference != "@布布配音 作为人物对白音色参考。" {
		t.Fatalf("Clip after apply = %+v, %v", clip, err)
	}
	references, err := fixture.store.ClipReferenceArtifacts(context.Background(), fixture.clip.ID)
	if err != nil || len(references) != 1 || references[0].ID != audioID {
		t.Fatalf("Clip references after apply = %+v, %v", references, err)
	}

	undoAt := fixture.nextTime()
	undo := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + strings.Repeat("V", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: applied.Source, Title: "Undo audio binding", Status: changeset.StatusDraft, Revision: 1, CreatedAt: undoAt, UpdatedAt: undoAt,
	}
	undo, replay, err = fixture.store.CreateChangeSet(context.Background(), undo, fixture.receipt(undo.ID, changeset.CommandBegin, "begin-audio-undo", "5", undoAt))
	if err != nil || replay {
		t.Fatalf("Create undo ChangeSet = %+v, replay=%v, err=%v", undo, replay, err)
	}
	revertID := changeset.OperationID("cop_" + strings.Repeat("V", 26))
	undo = fixture.append(t, undo, []changeset.Operation{{
		ID: revertID, ChangeSetID: undo.ID, Position: 1, Type: changeset.RevertChangeSet,
		Precondition:    changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("V", 26)), OperationID: revertID, TargetKind: changeset.TargetChangeSet, TargetID: applied.ID.String(), ExpectedRevision: applied.Revision},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: applied.ID},
	}}, "append-audio-undo", "6")
	undo = fixture.finalize(t, undo, "finalize-audio-undo", "7")
	undoAt = fixture.nextTime()
	undo, replay, err = fixture.store.ApplyChangeSet(context.Background(), undo.ID, undo.Revision, changeset.ConflictID("ccf_"+strings.Repeat("V", 26)), undoAt, fixture.receipt(undo.ID, changeset.CommandApply, "apply-audio-undo", "8", undoAt))
	if err != nil || replay || undo.Status != changeset.StatusApplied {
		t.Fatalf("Apply undo ChangeSet = %+v, replay=%v, err=%v", undo, replay, err)
	}
	clip, err = fixture.store.Clip(context.Background(), fixture.clip.ID)
	references, referenceErr := fixture.store.ClipReferenceArtifacts(context.Background(), fixture.clip.ID)
	if err != nil || referenceErr != nil || clip.Clip.VideoPrompts.Reference != "old video" || len(references) != 0 {
		t.Fatalf("Clip after undo = %+v, references=%+v, clipErr=%v, referenceErr=%v", clip, references, err, referenceErr)
	}
}

func TestChangeSetConflictDoesNotPartiallyApply(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	value := fixture.begin(t, "begin-conflict", "1")
	operations := fixture.operations(value.ID)[1:]
	operations[0].Position = 1
	value = fixture.append(t, value, operations, "append-conflict", "2")
	value = fixture.finalize(t, value, "finalize-conflict", "3")

	manualAt := fixture.nextTime()
	manual := fixture.clip
	manual.VideoPrompts.Reference = "manual edit"
	manual.Revision++
	manual.UpdatedAt = manualAt
	if err := fixture.store.UpdateClip(context.Background(), manual, 1, manualAt); err != nil {
		t.Fatal(err)
	}

	applyAt := fixture.nextTime()
	conflictID := changeset.ConflictID("ccf_" + strings.Repeat("F", 26))
	conflicted, _, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, conflictID, applyAt, fixture.receipt(value.ID, changeset.CommandApply, "apply-conflict", "4", applyAt))
	if !errors.Is(err, ErrChangeSetConflicted) || conflicted.Status != changeset.StatusConflicted || conflicted.FailureCode != "target_revision_changed" {
		t.Fatalf("conflicted ApplyChangeSet() = %+v, %v", conflicted, err)
	}
	asset, err := fixture.store.Asset(context.Background(), fixture.asset.ID)
	if err != nil || asset.Asset.Prompt != "old asset prompt" || asset.Asset.Revision != 1 {
		t.Fatalf("first operation was partially applied: %+v, %v", asset, err)
	}
	clip, _ := fixture.store.Clip(context.Background(), fixture.clip.ID)
	if clip.Clip.VideoPrompts.Reference != "manual edit" || clip.Clip.Revision != 2 {
		t.Fatalf("manual Clip update was overwritten: %+v", clip.Clip)
	}
	conflicts, err := fixture.store.ChangeSetConflicts(context.Background(), value.ID)
	if err != nil || len(conflicts) != 1 || conflicts[0].ID != conflictID || conflicts[0].OperationID != operations[0].ID {
		t.Fatalf("conflicts = %+v, %v", conflicts, err)
	}
}

func TestScriptChangeSetApplyAndDeterministicUndo(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	ctx := context.Background()

	original := fixture.begin(t, "begin-script", "a")
	operationID := changeset.OperationID("cop_" + strings.Repeat("W", 26))
	before := fixture.episode.Script.Markdown
	after := "# 黛玉进府\n\n黛玉乘轿进入荣国府。"
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: original.ID, Position: 1, Type: changeset.ReplaceEpisodeScript,
		Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("W", 26)), OperationID: operationID,
			TargetKind: changeset.TargetEpisodeScript, TargetID: fixture.episode.ID.String(), ExpectedRevision: fixture.episode.Script.Revision},
		EpisodeScript: &changeset.EpisodeScriptChange{EpisodeID: fixture.episode.ID, Before: before, After: after},
	}
	original = fixture.append(t, original, []changeset.Operation{operation}, "append-script", "b")
	original = fixture.finalize(t, original, "finalize-script", "c")
	at := fixture.nextTime()
	original, _, err := fixture.store.ApplyChangeSet(ctx, original.ID, original.Revision, changeset.ConflictID("ccf_"+strings.Repeat("W", 26)), at, fixture.receipt(original.ID, changeset.CommandApply, "apply-script", "d", at))
	if err != nil || original.Status != changeset.StatusApplied {
		t.Fatalf("apply script ChangeSet = %+v, %v", original, err)
	}
	episode, err := fixture.store.Episode(ctx, fixture.episode.ID)
	if err != nil || episode.Script.Markdown != after || episode.Script.Revision != 2 {
		t.Fatalf("script after apply = %+v, %v", episode.Script, err)
	}

	at = fixture.nextTime()
	inverse := changeset.ChangeSet{ID: changeset.ChangeSetID("cst_" + strings.Repeat("U", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: original.Source, Title: "Undo script", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at}
	inverse, _, err = fixture.store.CreateChangeSet(ctx, inverse, fixture.receipt(inverse.ID, changeset.CommandBegin, "begin-undo", "e", at))
	if err != nil {
		t.Fatal(err)
	}
	revertID := changeset.OperationID("cop_" + strings.Repeat("U", 26))
	revert := changeset.Operation{ID: revertID, ChangeSetID: inverse.ID, Position: 1, Type: changeset.RevertChangeSet,
		Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("U", 26)), OperationID: revertID,
			TargetKind: changeset.TargetChangeSet, TargetID: original.ID.String(), ExpectedRevision: original.Revision},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: original.ID}}
	inverse = fixture.append(t, inverse, []changeset.Operation{revert}, "append-undo", "f")
	inverse = fixture.finalize(t, inverse, "finalize-undo", "1")
	at = fixture.nextTime()
	inverse, _, err = fixture.store.ApplyChangeSet(ctx, inverse.ID, inverse.Revision, changeset.ConflictID("ccf_"+strings.Repeat("U", 26)), at, fixture.receipt(inverse.ID, changeset.CommandApply, "apply-undo", "2", at))
	if err != nil || inverse.Status != changeset.StatusApplied || inverse.RevertsChangeSetID != original.ID {
		t.Fatalf("apply inverse ChangeSet = %+v, %v", inverse, err)
	}
	episode, err = fixture.store.Episode(ctx, fixture.episode.ID)
	if err != nil || episode.Script.Markdown != before || episode.Script.Revision != 3 {
		t.Fatalf("script after Undo = %+v, %v", episode.Script, err)
	}
	original, err = fixture.store.ChangeSet(ctx, original.ID)
	if err != nil || original.RevertedByChangeSetID != inverse.ID {
		t.Fatalf("original undo link = %+v, %v", original, err)
	}
}

func TestAssetCreationChangeSetAppliesAndScriptConflictRollsBack(t *testing.T) {
	t.Run("apply", func(t *testing.T) {
		fixture := newChangeSetFixture(t)
		defer fixture.store.Close()
		value := fixture.begin(t, "begin-create", "a")
		operationID := changeset.OperationID("cop_" + strings.Repeat("N", 26))
		operation := changeset.Operation{
			ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateEpisodeAsset,
			Precondition:  changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("N", 26)), OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: fixture.episode.ID.String(), ExpectedRevision: fixture.episode.Script.Revision},
			AssetCreation: &changeset.AssetCreation{AssetID: project.AssetID("ast_" + strings.Repeat("N", 26)), IdentityID: project.AssetIdentityID("asi_" + strings.Repeat("N", 26)), Kind: project.AssetKindLocation, Name: "雨夜天台", Prompt: "潮湿反光的城市天台"},
		}
		value = fixture.append(t, value, []changeset.Operation{operation}, "append-create", "b")
		stored, err := fixture.store.ChangeSetOperations(context.Background(), value.ID, 0, 10)
		if err != nil || len(stored) != 1 || stored[0].AssetCreation == nil || stored[0].AssetCreation.Name != "雨夜天台" {
			t.Fatalf("stored Asset creation = %+v, %v", stored, err)
		}
		value = fixture.finalize(t, value, "finalize-create", "c")
		at := fixture.nextTime()
		applied, _, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("N", 26)), at, fixture.receipt(value.ID, changeset.CommandApply, "apply-create", "d", at))
		if err != nil || applied.Status != changeset.StatusApplied {
			t.Fatalf("apply Asset creation = %+v, %v", applied, err)
		}
		created, err := fixture.store.Asset(context.Background(), operation.AssetCreation.AssetID)
		if err != nil || created.Identity.Name != "雨夜天台" || created.Asset.Prompt != "潮湿反光的城市天台" || created.Asset.EpisodeID != fixture.episode.ID {
			t.Fatalf("created Asset = %+v, %v", created, err)
		}
		undoAt := fixture.nextTime()
		inverse := changeset.ChangeSet{ID: changeset.ChangeSetID("cst_" + strings.Repeat("M", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
			Source: applied.Source, Title: "Undo asset", Status: changeset.StatusDraft, Revision: 1, CreatedAt: undoAt, UpdatedAt: undoAt}
		inverse, _, err = fixture.store.CreateChangeSet(context.Background(), inverse, fixture.receipt(inverse.ID, changeset.CommandBegin, "begin-create-undo", "e", undoAt))
		if err != nil {
			t.Fatal(err)
		}
		revertID := changeset.OperationID("cop_" + strings.Repeat("M", 26))
		revert := changeset.Operation{ID: revertID, ChangeSetID: inverse.ID, Position: 1, Type: changeset.RevertChangeSet,
			Precondition:    changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("M", 26)), OperationID: revertID, TargetKind: changeset.TargetChangeSet, TargetID: applied.ID.String(), ExpectedRevision: applied.Revision},
			ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: applied.ID}}
		inverse = fixture.append(t, inverse, []changeset.Operation{revert}, "append-create-undo", "f")
		inverse = fixture.finalize(t, inverse, "finalize-create-undo", "1")
		undoAt = fixture.nextTime()
		if _, _, err := fixture.store.ApplyChangeSet(context.Background(), inverse.ID, inverse.Revision, changeset.ConflictID("ccf_"+strings.Repeat("M", 26)), undoAt, fixture.receipt(inverse.ID, changeset.CommandApply, "apply-create-undo", "2", undoAt)); err != nil {
			t.Fatalf("undo Asset creation = %v", err)
		}
		if _, err := fixture.store.Asset(context.Background(), operation.AssetCreation.AssetID); !errors.Is(err, ErrAssetNotFound) {
			t.Fatalf("Asset survived Undo: %v", err)
		}
		var identityCount int
		if err := fixture.store.database.QueryRowContext(context.Background(), `SELECT count(*) FROM asset_identities WHERE asset_identity_id=?`, operation.AssetCreation.IdentityID.String()).Scan(&identityCount); err != nil || identityCount != 0 {
			t.Fatalf("created identity survived Undo: count=%d err=%v", identityCount, err)
		}
	})

	t.Run("script conflict", func(t *testing.T) {
		fixture := newChangeSetFixture(t)
		defer fixture.store.Close()
		value := fixture.begin(t, "begin-script-conflict", "e")
		operationID := changeset.OperationID("cop_" + strings.Repeat("O", 26))
		assetID := project.AssetID("ast_" + strings.Repeat("O", 26))
		operation := changeset.Operation{ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateEpisodeAsset,
			Precondition:  changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("O", 26)), OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: fixture.episode.ID.String(), ExpectedRevision: fixture.episode.Script.Revision},
			AssetCreation: &changeset.AssetCreation{AssetID: assetID, IdentityID: project.AssetIdentityID("asi_" + strings.Repeat("O", 26)), Kind: project.AssetKindProp, Name: "旧钥匙", Prompt: "生锈的黄铜钥匙"}}
		value = fixture.append(t, value, []changeset.Operation{operation}, "append-script-conflict", "f")
		value = fixture.finalize(t, value, "finalize-script-conflict", "1")
		manualAt := fixture.nextTime()
		script := fixture.episode.Script
		script.Markdown = "剧本已由用户修改"
		script.Revision++
		script.UpdatedAt = manualAt
		if err := fixture.store.SaveScript(context.Background(), fixture.episode.ID, script, 1, manualAt); err != nil {
			t.Fatal(err)
		}
		applyAt := fixture.nextTime()
		conflicted, _, err := fixture.store.ApplyChangeSet(context.Background(), value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("O", 26)), applyAt, fixture.receipt(value.ID, changeset.CommandApply, "apply-script-conflict", "2", applyAt))
		if !errors.Is(err, ErrChangeSetConflicted) || conflicted.Status != changeset.StatusConflicted || conflicted.FailureCode != "script_revision_changed" {
			t.Fatalf("script-conflicted Apply = %+v, %v", conflicted, err)
		}
		conflicts, err := fixture.store.ChangeSetConflicts(context.Background(), value.ID)
		if err != nil || len(conflicts) != 1 || conflicts[0].OperationID != operationID {
			t.Fatalf("Asset creation conflicts = %+v, %v", conflicts, err)
		}
		if _, err := fixture.store.Asset(context.Background(), assetID); !errors.Is(err, ErrAssetNotFound) {
			t.Fatalf("Asset created despite script conflict: %v", err)
		}
	})
}

func TestAssetCreationChangeSetIgnoresDeletedNameAndPreservesIdentityOnUndo(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	ctx := context.Background()

	original, err := fixture.store.Asset(ctx, fixture.asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := fixture.nextTime()
	deleted := fixture.asset
	deleted.Revision++
	deleted.UpdatedAt = deletedAt
	deleted.DeletedAt = &deletedAt
	if err := fixture.store.DeleteAsset(ctx, deleted, fixture.asset.Revision, false, deletedAt); err != nil {
		t.Fatal(err)
	}

	value := fixture.begin(t, "begin-reuse-deleted", "3")
	operationID := changeset.OperationID("cop_" + strings.Repeat("V", 26))
	assetID := project.AssetID("ast_" + strings.Repeat("V", 26))
	proposedIdentityID := project.AssetIdentityID("asi_" + strings.Repeat("V", 26))
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateEpisodeAsset,
		Precondition:  changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("V", 26)), OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: fixture.episode.ID.String(), ExpectedRevision: fixture.episode.Script.Revision},
		AssetCreation: &changeset.AssetCreation{AssetID: assetID, IdentityID: proposedIdentityID, Kind: original.Identity.Kind, Name: original.Identity.Name, Prompt: "新提示词"},
	}
	value = fixture.append(t, value, []changeset.Operation{operation}, "append-reuse-deleted", "4")
	stored, err := fixture.store.ChangeSetOperations(ctx, value.ID, 0, 10)
	if err != nil || len(stored) != 1 || stored[0].AssetCreation.IdentityID != original.Identity.ID {
		t.Fatalf("stored dormant Identity resolution = %+v, %v", stored, err)
	}
	value = fixture.finalize(t, value, "finalize-reuse-deleted", "5")
	applyAt := fixture.nextTime()
	applied, _, err := fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("V", 26)), applyAt, fixture.receipt(value.ID, changeset.CommandApply, "apply-reuse-deleted", "6", applyAt))
	if err != nil || applied.Status != changeset.StatusApplied {
		t.Fatalf("apply replacement Asset = %+v, %v", applied, err)
	}
	created, err := fixture.store.Asset(ctx, assetID)
	if err != nil || created.Identity.ID != original.Identity.ID || created.Asset.Prompt != "新提示词" {
		t.Fatalf("created replacement Asset = %+v, %v", created, err)
	}

	undoAt := fixture.nextTime()
	inverse := changeset.ChangeSet{ID: changeset.ChangeSetID("cst_" + strings.Repeat("V", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: applied.Source, Title: "Undo replacement Asset", Status: changeset.StatusDraft, Revision: 1, CreatedAt: undoAt, UpdatedAt: undoAt}
	inverse, _, err = fixture.store.CreateChangeSet(ctx, inverse, fixture.receipt(inverse.ID, changeset.CommandBegin, "begin-reuse-undo", "7", undoAt))
	if err != nil {
		t.Fatal(err)
	}
	revertID := changeset.OperationID("cop_" + strings.Repeat("U", 26))
	revert := changeset.Operation{ID: revertID, ChangeSetID: inverse.ID, Position: 1, Type: changeset.RevertChangeSet,
		Precondition:    changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("U", 26)), OperationID: revertID, TargetKind: changeset.TargetChangeSet, TargetID: applied.ID.String(), ExpectedRevision: applied.Revision},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: applied.ID}}
	inverse = fixture.append(t, inverse, []changeset.Operation{revert}, "append-reuse-undo", "8")
	inverse = fixture.finalize(t, inverse, "finalize-reuse-undo", "9")
	undoAt = fixture.nextTime()
	if _, _, err := fixture.store.ApplyChangeSet(ctx, inverse.ID, inverse.Revision, changeset.ConflictID("ccf_"+strings.Repeat("U", 26)), undoAt, fixture.receipt(inverse.ID, changeset.CommandApply, "apply-reuse-undo", "a", undoAt)); err != nil {
		t.Fatalf("undo replacement Asset = %v", err)
	}
	if _, err := fixture.store.Asset(ctx, assetID); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("replacement Asset survived Undo: %v", err)
	}
	remaining, err := fixture.store.Asset(ctx, fixture.asset.ID)
	if err != nil || !remaining.Asset.IsDeleted() || remaining.Identity.ID != original.Identity.ID {
		t.Fatalf("original deleted Asset after Undo = %+v, %v", remaining, err)
	}
}

func TestDerivedAssetCreationPreservesPriorIdentity(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	ctx := context.Background()
	at := fixture.nextTime()
	sourceSlotID, err := project.FixedMediaSlotID(fixture.asset.ID.String(), project.MediaSlotAssetPrimaryImage)
	if err != nil {
		t.Fatal(err)
	}
	artifact := project.Artifact{ID: project.ArtifactID("art_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID, Kind: project.MediaKindImage, RelativePath: "media/images/derived-source.png", SHA256: strings.Repeat("d", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "derived-source.png", Width: 720, Height: 1280, CreatedAt: at}
	sourceVersion := project.MediaVersion{ID: project.MediaVersionID("mvr_" + strings.Repeat("E", 26)), SlotID: sourceSlotID, ArtifactID: artifact.ID, SequenceNumber: 1, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := fixture.store.AddMediaVersion(ctx, artifact, sourceVersion, 1, true, at); err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	target := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: project.EpisodeID("ep_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID, SequenceNumber: 2, Title: "第 2 集", TargetDuration: time.Minute, Revision: 1, CreatedAt: at, UpdatedAt: at}, Script: project.Script{Markdown: "角色再次出现。", Revision: 1, UpdatedAt: at}}
	if err := fixture.store.CreateEpisode(ctx, target, at); err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value := changeset.ChangeSet{ID: changeset.ChangeSetID("cst_" + strings.Repeat("D", 26)), ProjectID: fixture.project.ID, EpisodeID: target.ID,
		Source: changeset.Source{AgentSessionID: agent.SessionID("ags_" + strings.Repeat("D", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("D", 26)), CodexThreadID: "thr_derived", CodexTurnID: "turn_derived"},
		Title:  "延续角色资产", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at}
	created, replay, err := fixture.store.CreateChangeSet(ctx, value, fixture.receipt(value.ID, changeset.CommandBegin, "begin-derived", "a", at))
	if err != nil || replay {
		t.Fatalf("create derived ChangeSet = %+v, %v, %v", created, replay, err)
	}
	operationID := changeset.OperationID("cop_" + strings.Repeat("D", 26))
	assetID := project.AssetID("ast_" + strings.Repeat("D", 26))
	operation := changeset.Operation{ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateEpisodeAsset,
		Precondition:  changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("D", 26)), OperationID: operationID, TargetKind: changeset.TargetEpisodeScript, TargetID: target.ID.String(), ExpectedRevision: 1},
		AssetCreation: &changeset.AssetCreation{AssetID: assetID, IdentityID: project.AssetIdentityID("asi_" + strings.Repeat("Q", 26)), SourceAssetID: fixture.asset.ID, SourceAssetRevision: fixture.asset.Revision, InheritedMediaVersionID: project.MediaVersionID("mvr_" + strings.Repeat("D", 26)), Kind: project.AssetKindCharacter, Name: "角色", Prompt: "第二集造型"}}
	value = fixture.append(t, created, []changeset.Operation{operation}, "append-derived", "b")
	value = fixture.finalize(t, value, "finalize-derived", "c")
	at = fixture.nextTime()
	applied, _, err := fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("D", 26)), at, fixture.receipt(value.ID, changeset.CommandApply, "apply-derived", "d", at))
	if err != nil || applied.Status != changeset.StatusApplied {
		t.Fatalf("apply derived Asset = %+v, %v", applied, err)
	}
	result, err := fixture.store.Asset(ctx, assetID)
	if err != nil || result.Asset.IdentityID != fixture.asset.IdentityID || result.Asset.DerivedFromAssetID != fixture.asset.ID || result.Asset.Prompt != "第二集造型" {
		t.Fatalf("derived Asset = %+v, %v", result, err)
	}
	targetSlotID, err := project.FixedMediaSlotID(assetID.String(), project.MediaSlotAssetPrimaryImage)
	if err != nil {
		t.Fatal(err)
	}
	media, err := fixture.store.MediaSlot(ctx, targetSlotID, true)
	if err != nil || media.Current == nil || media.Current.ID != operation.AssetCreation.InheritedMediaVersionID || media.Current.ArtifactID != artifact.ID || len(media.Versions) != 1 {
		t.Fatalf("derived Asset media = %+v, %v", media, err)
	}
}

func TestPendingChangeSetCanBeRejectedButNotMutated(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	value := fixture.begin(t, "begin-reject", "5")
	operations := fixture.operations(value.ID)[:1]
	value = fixture.append(t, value, operations, "append-reject", "6")
	value = fixture.finalize(t, value, "finalize-reject", "7")

	_, _, err := fixture.store.AppendChangeSetOperations(context.Background(), value.ID, value.Revision, operations, fixture.nextTime(), fixture.receipt(value.ID, changeset.CommandAppend, "late-append", "8", fixture.now))
	if !errors.Is(err, ErrChangeSetStateConflict) {
		t.Fatalf("append pending error = %v", err)
	}
	at := fixture.nextTime()
	rejected, replay, err := fixture.store.RejectChangeSet(context.Background(), value.ID, value.Revision, "用户拒绝此提案", at, fixture.receipt(value.ID, changeset.CommandReject, "reject-1", "9", at))
	if err != nil || replay || rejected.Status != changeset.StatusRejected || rejected.FailureCode != "user_rejected" {
		t.Fatalf("RejectChangeSet() = %+v, replay=%v, err=%v", rejected, replay, err)
	}
	if _, err := fixture.store.database.ExecContext(context.Background(), `DELETE FROM change_sets WHERE change_set_id=?`, value.ID.String()); err == nil {
		t.Fatal("terminal ChangeSet deletion unexpectedly succeeded")
	}
}

func TestOnlyDraftChangeSetCanBeDiscarded(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	draft := fixture.begin(t, "begin-discard", "a")
	if err := fixture.store.DiscardChangeSetDraft(context.Background(), draft.ID); err != nil {
		t.Fatalf("DiscardChangeSetDraft(draft) = %v", err)
	}
	if _, err := fixture.store.ChangeSet(context.Background(), draft.ID); !errors.Is(err, ErrChangeSetNotFound) {
		t.Fatalf("discarded ChangeSet error = %v", err)
	}

	pending := fixture.begin(t, "begin-keep", "b")
	pending = fixture.append(t, pending, fixture.operations(pending.ID)[:1], "append-keep", "c")
	pending = fixture.finalize(t, pending, "finalize-keep", "d")
	if err := fixture.store.DiscardChangeSetDraft(context.Background(), pending.ID); !errors.Is(err, ErrChangeSetStateConflict) {
		t.Fatalf("DiscardChangeSetDraft(pending) = %v", err)
	}
	stored, err := fixture.store.ChangeSet(context.Background(), pending.ID)
	if err != nil || stored.Status != changeset.StatusPending {
		t.Fatalf("pending ChangeSet after discard attempt = %+v, %v", stored, err)
	}

	at := fixture.nextTime()
	draftForTurn := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + strings.Repeat("U", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: pending.Source, Title: "Crash draft", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	if _, _, err := fixture.store.CreateChangeSet(context.Background(), draftForTurn, fixture.receipt(draftForTurn.ID, changeset.CommandBegin, "begin-turn-cleanup", "e", at)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DiscardChangeSetDraftsForTurn(context.Background(), pending.Source.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ChangeSet(context.Background(), draftForTurn.ID); !errors.Is(err, ErrChangeSetNotFound) {
		t.Fatalf("Turn draft survived cleanup: %v", err)
	}
	if kept, err := fixture.store.ChangeSet(context.Background(), pending.ID); err != nil || kept.Status != changeset.StatusPending {
		t.Fatalf("pending ChangeSet was removed by Turn cleanup: %#v, %v", kept, err)
	}
}

type changeSetFixture struct {
	store   *Store
	project project.Project
	episode project.Episode
	asset   project.Asset
	clip    project.Clip
	now     time.Time
}

func newChangeSetFixture(t *testing.T) *changeSetFixture {
	t.Helper()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	projectValue, episode := testModels(t)
	if err := store.Initialize(context.Background(), projectValue, episode); err != nil {
		t.Fatal(err)
	}
	now := episode.UpdatedAt.Add(time.Second)
	identity, asset := assetModels(t, projectValue.ID, episode.ID, "Q", "Q", project.AssetKindCharacter, "角色", "old asset prompt", now)
	if err := store.CreateAsset(context.Background(), &identity, asset, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	scene := storyboardScene(t, projectValue.ID, episode.ID, "R", 1, "场景", now)
	if err := store.CreateScene(context.Background(), scene, 1, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	clip := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, "T", 1, 5, now)
	clip.FirstImagePrompt = "old first"
	clip.LastImagePrompt = "old last"
	clip.VideoPrompts.FirstImage = "first-mode untouched"
	clip.VideoPrompts.FirstLastImage = "first-last-mode untouched"
	clip.VideoPrompts.Reference = "old video"
	if err := store.CreateClip(context.Background(), clip, 2, now); err != nil {
		t.Fatal(err)
	}
	return &changeSetFixture{store: store, project: projectValue, episode: episode, asset: asset, clip: clip, now: now}
}

func (f *changeSetFixture) nextTime() time.Time { f.now = f.now.Add(time.Second); return f.now }

func (f *changeSetFixture) begin(t *testing.T, key, hashCharacter string) changeset.ChangeSet {
	t.Helper()
	at := f.nextTime()
	value := changeset.ChangeSet{ID: changeset.ChangeSetID("cst_" + strings.Repeat("S", 26)), ProjectID: f.project.ID, EpisodeID: f.episode.ID, Source: changeset.Source{AgentSessionID: agent.SessionID("ags_" + strings.Repeat("A", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("B", 26)), CodexThreadID: "thr_test", CodexTurnID: "turn_test"}, Title: "Prompt proposal", Summary: "Update prompts", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at}
	created, replay, err := f.store.CreateChangeSet(context.Background(), value, f.receipt(value.ID, changeset.CommandBegin, key, hashCharacter, at))
	if err != nil || replay {
		t.Fatalf("CreateChangeSet() = %+v, replay=%v, err=%v", created, replay, err)
	}
	return created
}

func (f *changeSetFixture) operations(id changeset.ChangeSetID) []changeset.Operation {
	assetOperationID := changeset.OperationID("cop_" + strings.Repeat("A", 26))
	clipOperationID := changeset.OperationID("cop_" + strings.Repeat("B", 26))
	return []changeset.Operation{
		{ID: assetOperationID, ChangeSetID: id, Position: 1, Type: changeset.UpdateAssetPrompt, Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("C", 26)), OperationID: assetOperationID, TargetKind: changeset.TargetAsset, TargetID: f.asset.ID.String(), ExpectedRevision: 1}, AssetPrompt: &changeset.AssetPromptChange{AssetID: f.asset.ID, Before: "old asset prompt", After: "new asset prompt"}},
		{ID: clipOperationID, ChangeSetID: id, Position: 2, Type: changeset.UpdateClipPrompts, Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("D", 26)), OperationID: clipOperationID, TargetKind: changeset.TargetClip, TargetID: f.clip.ID.String(), ExpectedRevision: 1}, ClipPrompts: &changeset.ClipPromptsChange{ClipID: f.clip.ID, VideoMode: project.ClipGenerationReference, FirstBefore: "old first", FirstAfter: "new first", LastBefore: "old last", LastAfter: "new last", VideoBefore: "old video", VideoAfter: "new video"}},
	}
}

func (f *changeSetFixture) append(t *testing.T, value changeset.ChangeSet, operations []changeset.Operation, key, hashCharacter string) changeset.ChangeSet {
	t.Helper()
	at := f.nextTime()
	result, replay, err := f.store.AppendChangeSetOperations(context.Background(), value.ID, value.Revision, operations, at, f.receipt(value.ID, changeset.CommandAppend, key, hashCharacter, at))
	if err != nil || replay {
		t.Fatalf("AppendChangeSetOperations() = %+v, replay=%v, err=%v", result, replay, err)
	}
	return result
}

func (f *changeSetFixture) finalize(t *testing.T, value changeset.ChangeSet, key, hashCharacter string) changeset.ChangeSet {
	t.Helper()
	at := f.nextTime()
	result, replay, err := f.store.FinalizeChangeSet(context.Background(), value.ID, value.Revision, at, f.receipt(value.ID, changeset.CommandFinalize, key, hashCharacter, at))
	if err != nil || replay {
		t.Fatalf("FinalizeChangeSet() = %+v, replay=%v, err=%v", result, replay, err)
	}
	return result
}

func (f *changeSetFixture) receipt(id changeset.ChangeSetID, command changeset.CommandType, key, hashCharacter string, at time.Time) changeset.CommandReceipt {
	return changeset.CommandReceipt{ProjectID: f.project.ID, OperationKey: key, RequestHash: strings.Repeat(hashCharacter, 64), Command: command, ChangeSetID: id, CreatedAt: at}
}
