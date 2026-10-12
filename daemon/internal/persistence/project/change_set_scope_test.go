package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestStoryboardProposalRejectsFragmentedScene(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	at := fixture.nextTime()
	scene := storyboardScene(t, fixture.project.ID, fixture.episode.ID, "F", project.StoryboardOrderStep, "Fragmented Scene", at)
	clip := storyboardClip(t, fixture.project.ID, fixture.episode.ID, scene.ID, "G", project.StoryboardOrderStep, 4, at)
	owner := changeset.ChangeSet{ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID}
	operations := []changeset.Operation{
		{
			Type:         changeset.CreateScene,
			Precondition: changeset.Precondition{TargetID: fixture.episode.ID.String()},
			SceneCreation: &changeset.SceneCreation{
				Ref:   "scene",
				Scene: scene,
			},
		},
		{
			Type:         changeset.CreateClip,
			Precondition: changeset.Precondition{TargetID: fixture.episode.ID.String()},
			ClipCreation: &changeset.ClipCreation{
				Ref:      "clip",
				SceneRef: "scene",
				Clip:     clip,
			},
		},
	}
	var conflict *operationConflict
	err := fixture.store.withTransaction(context.Background(), "validate fragmented Storyboard", func(tx *sql.Tx) error {
		var err error
		conflict, err = validateStoryboardProposal(context.Background(), tx, owner, operations)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict == nil || conflict.code != "clip_plan_too_fragmented" {
		t.Fatalf("fragmented Scene conflict = %+v", conflict)
	}
}

func TestChangeSetRejectsMixedSemanticScopes(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()
	value := fixture.begin(t, "mixed-begin", "a")
	value = fixture.append(t, value, fixture.operations(value.ID), "mixed-append", "b")
	at := fixture.nextTime()
	_, _, err := fixture.store.FinalizeChangeSet(context.Background(), value.ID, value.Revision, at, fixture.receipt(value.ID, changeset.CommandFinalize, "mixed-finalize", "c", at))
	if !errors.Is(err, ErrChangeSetPreconditionFailed) {
		t.Fatalf("mixed Asset/Clip finalize error = %v, want ErrChangeSetPreconditionFailed", err)
	}
}

func TestSceneGuardIgnoresObsoleteNumericOrderKeys(t *testing.T) {
	t.Parallel()
	legacy := `{"exists":true,"head":"scene-head","clips":[{"id":"clp_A","position":2,"head":"clip-head"}]}`
	current := `{"exists":true,"head":"scene-head","clips":[{"id":"clp_A","head":"clip-head"}]}`
	if scopeGuardToken(legacy) != scopeGuardToken(current) {
		t.Fatal("numeric order key changed the semantic Scene guard")
	}
}

func TestClipScopedUndoUsesPerResourceHistory(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	first := applyClipPromptChange(t, fixture, 1, "old video", "first agent edit")
	second := applyClipPromptChange(t, fixture, 2, "first agent edit", "second agent edit")

	if _, err := undoAppliedChange(t, fixture, first, 3); !errors.Is(err, ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "undo_target_changed") {
		t.Fatalf("undo older Clip change = %v", err)
	}
	if _, err := undoAppliedChange(t, fixture, second, 4); err != nil {
		t.Fatalf("undo latest Clip change: %v", err)
	}
	clip, err := fixture.store.Clip(context.Background(), fixture.clip.ID)
	if err != nil || clip.Clip.VideoPrompts.Reference != "first agent edit" {
		t.Fatalf("Clip after first undo = %+v, %v", clip.Clip, err)
	}
	if _, err := undoAppliedChange(t, fixture, first, 5); err != nil {
		t.Fatalf("undo restored prior Clip head: %v", err)
	}
	clip, err = fixture.store.Clip(context.Background(), fixture.clip.ID)
	if err != nil || clip.Clip.VideoPrompts.Reference != "old video" || clip.Clip.Revision != 5 {
		t.Fatalf("Clip after stacked undo = %+v, %v", clip.Clip, err)
	}
}

func TestManualClipEditBlocksOlderUndo(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	original := applyClipPromptChange(t, fixture, 6, "old video", "agent edit")
	manual, err := fixture.store.Clip(context.Background(), fixture.clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := fixture.nextTime()
	manual.Clip.VideoPrompts.Reference = "manual edit"
	manual.Clip.Revision++
	manual.Clip.UpdatedAt = at
	if err := fixture.store.UpdateClip(context.Background(), manual.Clip, manual.Clip.Revision-1, at); err != nil {
		t.Fatal(err)
	}
	if _, err := undoAppliedChange(t, fixture, original, 7); !errors.Is(err, ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "undo_target_changed") {
		t.Fatalf("undo across manual Clip edit = %v", err)
	}
}

func TestSceneUndoIsBlockedByChildClipUntilChildChangeIsUndone(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	sceneChange, createdClip := applySceneCreation(t, fixture, 10)
	fixture.clip = createdClip
	clipChange := applyClipPromptChange(t, fixture, 11, "", "later Clip edit")

	if _, err := undoAppliedChange(t, fixture, sceneChange, 12); !errors.Is(err, ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "undo_target_changed") {
		t.Fatalf("undo Scene across child Clip edit = %v", err)
	}
	if _, err := undoAppliedChange(t, fixture, clipChange, 13); err != nil {
		t.Fatalf("undo child Clip change: %v", err)
	}
	if _, err := undoAppliedChange(t, fixture, sceneChange, 14); err != nil {
		t.Fatalf("undo Scene after restoring child heads: %v", err)
	}
	undoneScene, err := fixture.store.Scene(context.Background(), createdClip.SceneID)
	if err != nil || !undoneScene.IsDeleted() {
		t.Fatalf("created Scene survived undo: %+v, %v", undoneScene, err)
	}
}

func TestUndoMiddleSceneKeepsStoryboardReadableWithoutRenumberingSiblings(t *testing.T) {
	t.Parallel()
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	_, _ = applySceneCreation(t, fixture, 30)
	middle, middleClip := applySceneCreation(t, fixture, 31)
	_, lastClip := applySceneCreation(t, fixture, 32)

	if _, err := undoAppliedChange(t, fixture, middle, 33); err != nil {
		t.Fatalf("undo middle Scene: %v", err)
	}
	storyboard, err := fixture.store.Storyboard(context.Background(), fixture.episode.ID)
	if err != nil {
		t.Fatalf("read Storyboard after middle undo: %v", err)
	}
	if len(storyboard.Scenes) != 3 {
		t.Fatalf("active Scene count = %d, want 3", len(storyboard.Scenes))
	}
	if storyboard.Scenes[len(storyboard.Scenes)-1].Scene.ID != lastClip.SceneID {
		t.Fatalf("later Scene changed after middle undo: %+v", storyboard.Scenes)
	}
	if storyboard.Scenes[len(storyboard.Scenes)-1].Scene.Position <= middleClip.Position {
		t.Fatal("later Scene order key was compacted during undo")
	}
}

func TestOnlyMostRecentTwoHundredChangeSetsRemainReversible(t *testing.T) {
	fixture := newChangeSetFixture(t)
	defer fixture.store.Close()

	before := "old video"
	var oldest changeset.ChangeSet
	for index := 0; index <= retainedUndoChangeSets; index++ {
		after := fmt.Sprintf("agent edit %d", index)
		applied := applyClipPromptChange(t, fixture, 20+index, before, after)
		if index == 0 {
			oldest = applied
		}
		before = after
	}
	var active, expired int
	if err := fixture.store.database.QueryRow(`SELECT count(*) FROM change_set_scopes WHERE undo_expired=0`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.database.QueryRow(`SELECT count(*) FROM change_set_scopes WHERE undo_expired=1`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if active != retainedUndoChangeSets || expired != 1 {
		t.Fatalf("retained scopes active=%d expired=%d, want %d and 1", active, expired, retainedUndoChangeSets)
	}
	visible, err := fixture.store.ChangeSetsForAgentSession(context.Background(), fixture.project.ID, oldest.Source.AgentSessionID)
	if err != nil || len(visible) != retainedUndoChangeSets {
		t.Fatalf("visible ChangeSet history = %d, %v; want %d", len(visible), err, retainedUndoChangeSets)
	}
	var expiredPayloads int
	if err := fixture.store.database.QueryRow(`SELECT count(*) FROM change_set_operations WHERE change_set_id=?`, oldest.ID.String()).Scan(&expiredPayloads); err != nil || expiredPayloads != 0 {
		t.Fatalf("expired reversible payload count = %d, %v; want 0", expiredPayloads, err)
	}
	if _, err := undoAppliedChange(t, fixture, oldest, 300); !errors.Is(err, ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "undo_expired") {
		t.Fatalf("undo expired ChangeSet = %v", err)
	}
}

func applyClipPromptChange(t *testing.T, fixture *changeSetFixture, sequence int, before, after string) changeset.ChangeSet {
	t.Helper()
	ctx := context.Background()
	suffix := scopedTestSuffix(sequence)
	at := fixture.nextTime()
	value := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + suffix), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: changeset.Source{AgentSessionID: agent.SessionID("ags_" + strings.Repeat("A", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("B", 26)), CodexThreadID: "thr_scope", CodexTurnID: "turn_scope"},
		Title:  "Clip prompt edit", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	value, _, err := fixture.store.CreateChangeSet(ctx, value, scopedReceipt(fixture, value.ID, changeset.CommandBegin, sequence, "begin", at))
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.Clip(ctx, fixture.clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	operationID := changeset.OperationID("cop_" + suffix)
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.UpdateClipPrompts,
		Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + suffix), OperationID: operationID, TargetKind: changeset.TargetClip, TargetID: fixture.clip.ID.String(), ExpectedRevision: current.Clip.Revision},
		ClipPrompts:  &changeset.ClipPromptsChange{ClipID: fixture.clip.ID, VideoMode: project.ClipGenerationReference, FirstBefore: current.Clip.FirstImagePrompt, FirstAfter: current.Clip.FirstImagePrompt, LastBefore: current.Clip.LastImagePrompt, LastAfter: current.Clip.LastImagePrompt, VideoBefore: before, VideoAfter: after},
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.AppendChangeSetOperations(ctx, value.ID, value.Revision, []changeset.Operation{operation}, at, scopedReceipt(fixture, value.ID, changeset.CommandAppend, sequence, "append", at))
	if err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.FinalizeChangeSet(ctx, value.ID, value.Revision, at, scopedReceipt(fixture, value.ID, changeset.CommandFinalize, sequence, "finalize", at))
	if err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision, changeset.ConflictID("ccf_"+suffix), at, scopedReceipt(fixture, value.ID, changeset.CommandApply, sequence, "apply", at))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func applySceneCreation(t *testing.T, fixture *changeSetFixture, sequence int) (changeset.ChangeSet, project.Clip) {
	t.Helper()
	ctx := context.Background()
	suffix := scopedTestSuffix(sequence)
	at := fixture.nextTime()
	value := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + suffix), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: changeset.Source{AgentSessionID: agent.SessionID("ags_" + strings.Repeat("A", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("B", 26)), CodexThreadID: "thr_scope", CodexTurnID: "turn_scope"},
		Title:  "Scene creation", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	var err error
	value, _, err = fixture.store.CreateChangeSet(ctx, value, scopedReceipt(fixture, value.ID, changeset.CommandBegin, sequence, "scene-begin", at))
	if err != nil {
		t.Fatal(err)
	}
	storyboard, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastOrderKey := 0
	if len(storyboard.Scenes) > 0 {
		lastOrderKey = storyboard.Scenes[len(storyboard.Scenes)-1].Scene.Position
	}
	scene := storyboardScene(t, fixture.project.ID, fixture.episode.ID, "X", lastOrderKey+project.StoryboardOrderStep, "Scoped Scene", at)
	clip := storyboardClip(t, fixture.project.ID, fixture.episode.ID, scene.ID, "Y", project.StoryboardOrderStep, 6, at)
	scene.ID = project.SceneID("scn_" + scopedTestSuffix(sequence+600))
	clip.ID = project.ClipID("clp_" + scopedTestSuffix(sequence+700))
	clip.SceneID = scene.ID
	clip.GenerationMode = project.ClipGenerationReference
	sceneOperationID := changeset.OperationID("cop_" + scopedTestSuffix(sequence+400))
	clipOperationID := changeset.OperationID("cop_" + scopedTestSuffix(sequence+500))
	operations := []changeset.Operation{
		{ID: sceneOperationID, ChangeSetID: value.ID, Position: 1, Type: changeset.CreateScene,
			Precondition:  changeset.Precondition{ID: changeset.PreconditionID("cpc_" + scopedTestSuffix(sequence+400)), OperationID: sceneOperationID, TargetKind: changeset.TargetStoryboard, TargetID: fixture.episode.ID.String(), ExpectedRevision: storyboard.Revision},
			SceneCreation: &changeset.SceneCreation{Ref: "scoped_scene", Scene: scene, ExpectedScriptRevision: fixture.episode.Script.Revision}},
		{ID: clipOperationID, ChangeSetID: value.ID, Position: 2, Type: changeset.CreateClip,
			Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + scopedTestSuffix(sequence+500)), OperationID: clipOperationID, TargetKind: changeset.TargetStoryboard, TargetID: fixture.episode.ID.String(), ExpectedRevision: storyboard.Revision},
			ClipCreation: &changeset.ClipCreation{Ref: "scoped_clip", SceneRef: "scoped_scene", Clip: clip, ExpectedScriptRevision: fixture.episode.Script.Revision}},
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.AppendChangeSetOperations(ctx, value.ID, value.Revision, operations, at, scopedReceipt(fixture, value.ID, changeset.CommandAppend, sequence, "scene-append", at))
	if err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.FinalizeChangeSet(ctx, value.ID, value.Revision, at, scopedReceipt(fixture, value.ID, changeset.CommandFinalize, sequence, "scene-finalize", at))
	if err != nil {
		t.Fatal(err)
	}
	at = fixture.nextTime()
	value, _, err = fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision, changeset.ConflictID("ccf_"+suffix), at, scopedReceipt(fixture, value.ID, changeset.CommandApply, sequence, "scene-apply", at))
	if err != nil {
		t.Fatal(err)
	}
	return value, clip
}

func undoAppliedChange(t *testing.T, fixture *changeSetFixture, original changeset.ChangeSet, sequence int) (changeset.ChangeSet, error) {
	t.Helper()
	ctx := context.Background()
	suffix := scopedTestSuffix(1000 + sequence)
	at := fixture.nextTime()
	inverse := changeset.ChangeSet{
		ID: changeset.ChangeSetID("cst_" + suffix), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
		Source: original.Source, Title: "Undo", Status: changeset.StatusDraft, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	var err error
	inverse, _, err = fixture.store.CreateChangeSet(ctx, inverse, scopedReceipt(fixture, inverse.ID, changeset.CommandBegin, sequence, "undo-begin", at))
	if err != nil {
		return inverse, err
	}
	operationID := changeset.OperationID("cop_" + suffix)
	operation := changeset.Operation{
		ID: operationID, ChangeSetID: inverse.ID, Position: 1, Type: changeset.RevertChangeSet,
		Precondition:    changeset.Precondition{ID: changeset.PreconditionID("cpc_" + suffix), OperationID: operationID, TargetKind: changeset.TargetChangeSet, TargetID: original.ID.String(), ExpectedRevision: original.Revision},
		ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: original.ID},
	}
	at = fixture.nextTime()
	inverse, _, err = fixture.store.AppendChangeSetOperations(ctx, inverse.ID, inverse.Revision, []changeset.Operation{operation}, at, scopedReceipt(fixture, inverse.ID, changeset.CommandAppend, sequence, "undo-append", at))
	if err != nil {
		return inverse, err
	}
	at = fixture.nextTime()
	inverse, _, err = fixture.store.FinalizeChangeSet(ctx, inverse.ID, inverse.Revision, at, scopedReceipt(fixture, inverse.ID, changeset.CommandFinalize, sequence, "undo-finalize", at))
	if err != nil {
		return inverse, err
	}
	at = fixture.nextTime()
	result, _, err := fixture.store.ApplyChangeSet(ctx, inverse.ID, inverse.Revision, changeset.ConflictID("ccf_"+suffix), at, scopedReceipt(fixture, inverse.ID, changeset.CommandApply, sequence, "undo-apply", at))
	return result, err
}

func scopedReceipt(fixture *changeSetFixture, id changeset.ChangeSetID, command changeset.CommandType, sequence int, phase string, at time.Time) changeset.CommandReceipt {
	return changeset.CommandReceipt{ProjectID: fixture.project.ID, OperationKey: fmt.Sprintf("scope-%d-%s", sequence, phase), RequestHash: strings.Repeat("a", 64), Command: command, ChangeSetID: id, CreatedAt: at}
}

func scopedTestSuffix(value int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	result := []byte(strings.Repeat("A", 26))
	for index := len(result) - 1; value > 0; index-- {
		result[index] = alphabet[value%len(alphabet)]
		value /= len(alphabet)
	}
	return string(result)
}
