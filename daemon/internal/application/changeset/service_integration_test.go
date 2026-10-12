package changesetapp_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestServiceDraftAndReviewPortsApplyAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := applicationFixture(t)
	defer store.Close()
	clock := &stepClock{now: asset.UpdatedAt}
	service, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, staticResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	var draftPort changesetapp.DraftCommands = service
	var reviewPort changesetapp.ReviewCommands = service

	begin := changesetapp.BeginDraftInput{ProjectID: projectValue.ID, EpisodeID: episode.ID, AgentSessionID: agent.SessionID("ags_" + strings.Repeat("A", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("B", 26)), CodexThreadID: "thr_test", CodexTurnID: "turn_test", Title: "优化角色提示词", Summary: "增强视觉描述", OperationKey: "begin-service"}
	created, err := draftPort.BeginDraft(ctx, begin)
	if err != nil || created.Replayed || created.ChangeSet.Status != changeset.StatusDraft {
		t.Fatalf("BeginDraft() = %+v, %v", created, err)
	}
	replayed, err := draftPort.BeginDraft(ctx, begin)
	if err != nil || !replayed.Replayed || replayed.ChangeSet.ID != created.ChangeSet.ID {
		t.Fatalf("replayed BeginDraft() = %+v, %v", replayed, err)
	}

	appended, err := draftPort.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: projectValue.ID, ChangeSetID: created.ChangeSet.ID, ExpectedRevision: 1, OperationKey: "append-service", Operations: []changesetapp.OperationDraft{{Type: changeset.UpdateAssetPrompt, ExpectedRevision: 1, AssetPrompt: &changeset.AssetPromptChange{AssetID: asset.ID, Before: asset.Prompt, After: "new prompt"}}}})
	if err != nil || appended.ChangeSet.OperationCount != 1 || appended.ChangeSet.Revision != 2 {
		t.Fatalf("AppendOperations() = %+v, %v", appended, err)
	}
	finalized, err := draftPort.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: projectValue.ID, ChangeSetID: created.ChangeSet.ID, ExpectedRevision: 2, OperationKey: "finalize-service"})
	if err != nil || finalized.ChangeSet.Status != changeset.StatusPending {
		t.Fatalf("Finalize() = %+v, %v", finalized, err)
	}
	applied, err := reviewPort.Apply(ctx, changesetapp.ApplyInput{ProjectID: projectValue.ID, ChangeSetID: created.ChangeSet.ID, ExpectedRevision: 3, OperationKey: "apply-service"})
	if err != nil || applied.ChangeSet.Status != changeset.StatusApplied {
		t.Fatalf("Apply() = %+v, %v", applied, err)
	}
	updated, err := store.Asset(ctx, asset.ID)
	if err != nil || updated.Asset.Prompt != "new prompt" || updated.Asset.Revision != 2 {
		t.Fatalf("Asset after Service apply = %+v, %v", updated, err)
	}
}

func TestServiceCreatesStoryboardThroughPendingChangeSetAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := applicationFixture(t)
	defer store.Close()
	clock := &stepClock{now: asset.UpdatedAt}
	service, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, staticResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.BeginDraft(ctx, changesetapp.BeginDraftInput{ProjectID: projectValue.ID, EpisodeID: episode.ID,
		AgentSessionID: agent.SessionID("ags_" + strings.Repeat("S", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("T", 26)),
		CodexThreadID: "thread-storyboard", CodexTurnID: "turn-storyboard", Title: "创建分镜", OperationKey: "begin-storyboard"})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := service.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID,
		ExpectedRevision: 1, OperationKey: "append-storyboard", Operations: []changesetapp.OperationDraft{
			{Type: changeset.CreateScene, ExpectedRevision: 1, ExpectedScriptRevision: 1, SceneCreation: &changesetapp.SceneCreationDraft{Ref: "scene_intro", Position: project.StoryboardOrderStep, Title: "开场"}},
			{Type: changeset.CreateClip, ExpectedRevision: 1, ExpectedScriptRevision: 1, ClipCreation: &changesetapp.ClipCreationDraft{Ref: "clip_arrival", SceneRef: "scene_intro", Position: project.StoryboardOrderStep, Title: "角色入场", PlannedDurationSeconds: 6, GenerationMode: project.ClipGenerationReference}},
			{Type: changeset.AddClipAssetRef, ExpectedRevision: 1, ExpectedScriptRevision: 1, ClipAssetRef: &changesetapp.ClipAssetReferenceDraft{ClipRef: "clip_arrival", AssetID: asset.ID, Position: 1}},
		}})
	if err != nil || appended.ChangeSet.OperationCount != 3 {
		t.Fatalf("AppendOperations() = %+v, %v", appended, err)
	}
	operations, err := service.Operations(ctx, projectValue.ID, begin.ChangeSet.ID, 0, 100)
	if err != nil || len(operations) != 3 || operations[0].SceneCreation == nil || operations[1].ClipCreation == nil || operations[2].ClipAssetRef == nil {
		t.Fatalf("Operations() = %+v, %v", operations, err)
	}
	if operations[0].SceneCreation.Scene.ID == "" || operations[1].ClipCreation.Clip.ID == "" || operations[1].ClipCreation.Clip.SceneID != operations[0].SceneCreation.Scene.ID || operations[2].ClipAssetRef.ClipID != operations[1].ClipCreation.Clip.ID {
		t.Fatalf("daemon IDs and temporary references were not frozen: %+v", operations)
	}
	finalized, err := service.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID, ExpectedRevision: 2, OperationKey: "finalize-storyboard"})
	if err != nil || finalized.ChangeSet.Status != changeset.StatusPending {
		t.Fatalf("Finalize() = %+v, %v", finalized, err)
	}
	beforeApply, err := store.Storyboard(ctx, episode.ID)
	if err != nil || len(beforeApply.Scenes) != 0 || beforeApply.Revision != 1 {
		t.Fatalf("pending ChangeSet modified Storyboard: %+v, %v", beforeApply, err)
	}
	applied, err := service.Apply(ctx, changesetapp.ApplyInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID, ExpectedRevision: 3, OperationKey: "apply-storyboard"})
	if err != nil || applied.ChangeSet.Status != changeset.StatusApplied {
		t.Fatalf("Apply() = %+v, %v", applied, err)
	}
	storyboard, err := store.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 2 || len(storyboard.Scenes) != 1 || len(storyboard.Scenes[0].Clips) != 1 || len(storyboard.Scenes[0].Clips[0].AssetReferences) != 1 || storyboard.Scenes[0].Clips[0].AssetReferences[0].AssetID != asset.ID {
		t.Fatalf("Storyboard after Apply = %+v, %v", storyboard, err)
	}
}

func TestStoryboardChangeSetConflictsWithoutPartialApplyAfterScriptEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := applicationFixture(t)
	defer store.Close()
	clock := &stepClock{now: asset.UpdatedAt}
	service, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, staticResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.BeginDraft(ctx, changesetapp.BeginDraftInput{ProjectID: projectValue.ID, EpisodeID: episode.ID,
		AgentSessionID: agent.SessionID("ags_" + strings.Repeat("U", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("V", 26)),
		CodexThreadID: "thread-conflict", CodexTurnID: "turn-conflict", Title: "创建分镜", OperationKey: "begin-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := service.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID,
		ExpectedRevision: 1, OperationKey: "append-conflict", Operations: []changesetapp.OperationDraft{
			{Type: changeset.CreateScene, ExpectedRevision: 1, ExpectedScriptRevision: 1, SceneCreation: &changesetapp.SceneCreationDraft{Ref: "scene", Position: project.StoryboardOrderStep, Title: "场景"}},
			{Type: changeset.CreateClip, ExpectedRevision: 1, ExpectedScriptRevision: 1, ClipCreation: &changesetapp.ClipCreationDraft{Ref: "clip", SceneRef: "scene", Position: project.StoryboardOrderStep, Title: "镜头", PlannedDurationSeconds: 6, GenerationMode: project.ClipGenerationFirstImage}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := service.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID, ExpectedRevision: appended.ChangeSet.Revision, OperationKey: "finalize-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	manualAt := clock.Now()
	if err := store.SaveScript(ctx, episode.ID, project.Script{Markdown: "# 用户修改", Revision: 2, UpdatedAt: manualAt}, 1, manualAt); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, changesetapp.ApplyInput{ProjectID: projectValue.ID, ChangeSetID: begin.ChangeSet.ID, ExpectedRevision: finalized.ChangeSet.Revision, OperationKey: "apply-conflict"})
	if !errors.Is(err, projectstore.ErrChangeSetConflicted) || result.ChangeSet.Status != changeset.StatusConflicted {
		t.Fatalf("Apply() = %+v, %v", result, err)
	}
	storyboard, readErr := store.Storyboard(ctx, episode.ID)
	if readErr != nil || storyboard.Revision != 1 || len(storyboard.Scenes) != 0 {
		t.Fatalf("conflicted Apply partially modified Storyboard: %+v, %v", storyboard, readErr)
	}
}

type staticResolver struct{ store *projectstore.Store }

func (r staticResolver) ResolveChangeSetStore(context.Context, project.ProjectID) (changesetapp.Store, error) {
	return r.store, nil
}

type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { c.now = c.now.Add(time.Second); return c.now }

func applicationFixture(t *testing.T) (*projectstore.Store, project.Project, project.Episode, project.Asset) {
	t.Helper()
	ctx := context.Background()
	store, err := projectstore.Open(ctx, filepath.Join(t.TempDir(), "project.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	projectID := project.ProjectID("prj_" + strings.Repeat("P", 26))
	episodeID := project.EpisodeID("ep_" + strings.Repeat("E", 26))
	output, _ := project.NewOutputSpec(720, 1280)
	projectValue := project.Project{ID: projectID, Name: "Project", Output: output, Revision: 1, CreatedAt: now, UpdatedAt: now}
	episode := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: episodeID, ProjectID: projectID, SequenceNumber: 1, Title: "第 1 集", TargetDuration: 60 * time.Second, Revision: 1, CreatedAt: now, UpdatedAt: now}, Script: project.Script{Revision: 1, UpdatedAt: now}}
	if err := store.Initialize(ctx, projectValue, episode); err != nil {
		t.Fatal(err)
	}
	at := now.Add(time.Second)
	identity := project.AssetIdentity{ID: project.AssetIdentityID("asi_" + strings.Repeat("I", 26)), ProjectID: projectID, Kind: project.AssetKindCharacter, Name: "角色", Revision: 1, CreatedAt: at, UpdatedAt: at}
	asset := project.Asset{ID: project.AssetID("ast_" + strings.Repeat("A", 26)), ProjectID: projectID, EpisodeID: episodeID, IdentityID: identity.ID, Prompt: "old prompt", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
		t.Fatal(err)
	}
	return store, projectValue, episode, asset
}
