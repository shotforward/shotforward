package agentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestMCPAutoAppliesRealChangeSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := createMCPProjectStore(t)
	defer store.Close()
	clock := &integrationClock{at: asset.UpdatedAt}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, integrationStoreResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	queries := integrationProjectQueries{store: store, projectID: projectValue.ID}
	events := &integrationEvents{}
	server, err := NewServer(Options{Queries: queries, Drafts: changeSets, DraftCleanup: changeSets, Events: events, Random: bytes.NewReader(bytes.Repeat([]byte{0x44}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43124")}
	sessionID := agent.SessionID("ags_MMMMMMMMMMMMMMMMMMMMMMMMMM")
	turnID := agent.TurnID("agt_NNNNNNNNNNNNNNNNNNNNNNNNNN")
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: "route_MMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMM", SessionID: sessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_integration", CodexTurnID: "turn_integration"}); err != nil {
		t.Fatal(err)
	}
	credential := server.Credential()
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	read := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": asset.ID.String()})
	if read.IsError || !strings.Contains(read.Text, asset.Prompt) {
		t.Fatalf("get_asset = %+v", read)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "优化角色 Prompt", "summary": "真实仓库集成测试", "operation_key": "mcp-int-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	appendResult := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "mcp-int-append",
		"operations": []map[string]any{{"type": "update_asset_prompt", "asset_id": asset.ID.String(), "new_prompt": "new pending prompt"}},
	})
	_, revision = changeSetIdentity(t, appendResult)
	finalized := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "mcp-int-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize_change_set = %+v", finalized)
	}
	replayedFinalize := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "mcp-int-finalize"})
	if replayedFinalize.IsError || !strings.Contains(replayedFinalize.Text, `"replayed":true`) {
		t.Fatalf("replayed finalize_change_set = %+v", replayedFinalize)
	}
	if got := events.snapshot(); len(got) != 1 || got[0].Type != agentapp.EventChangeSetApplied || got[0].SessionID != sessionID || got[0].TurnID != turnID || got[0].ChangeSetID != changeSetID {
		t.Fatalf("applied Studio events = %#v", got)
	}

	applied, err := store.ChangeSet(ctx, changeSetID)
	if err != nil || applied.Status != changeset.StatusApplied || applied.Source.AgentSessionID != sessionID || applied.Source.AgentTurnID != turnID || applied.Source.CodexThreadID != "thr_integration" || applied.Source.CodexTurnID != "turn_integration" {
		t.Fatalf("stored ChangeSet = %+v, %v", applied, err)
	}
	operations, err := store.ChangeSetOperations(ctx, changeSetID, 0, 10)
	if err != nil || len(operations) != 1 || operations[0].AssetPrompt == nil || operations[0].AssetPrompt.Before != asset.Prompt || operations[0].AssetPrompt.After != "new pending prompt" || operations[0].Precondition.ExpectedRevision != asset.Revision {
		t.Fatalf("stored operations = %+v, %v", operations, err)
	}
	changed, err := store.Asset(ctx, asset.ID)
	if err != nil || changed.Asset.Prompt != "new pending prompt" || changed.Asset.Revision != asset.Revision+1 {
		t.Fatalf("auto-applied ChangeSet did not edit Asset = %+v, %v", changed, err)
	}
	if err := server.EndTurn(ctx, sessionID, turnID); err != nil {
		t.Fatalf("EndTurn after finalize = %v", err)
	}
	stillApplied, err := store.ChangeSet(ctx, changeSetID)
	if err != nil || stillApplied.Status != changeset.StatusApplied {
		t.Fatalf("applied ChangeSet was discarded = %+v, %v", stillApplied, err)
	}
	secondTurnID := agent.TurnID("agt_PPPPPPPPPPPPPPPPPPPPPPPPPP")
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: secondTurnID, CodexThreadID: "thr_integration", CodexTurnID: "turn_cleanup"}); err != nil {
		t.Fatal(err)
	}
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	unfinalized := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Interrupted draft", "operation_key": "mcp-int-cleanup"})
	unfinalizedID, _ := changeSetIdentity(t, unfinalized)
	if err := server.EndTurn(ctx, sessionID, secondTurnID); err != nil {
		t.Fatalf("EndTurn cleanup = %v", err)
	}
	if _, err := store.ChangeSet(ctx, unfinalizedID); !errors.Is(err, projectstore.ErrChangeSetNotFound) {
		t.Fatalf("unfinalized ChangeSet survived Turn: %v", err)
	}
}

func TestMCPRestoresDeletedAssetAndUndoReturnsItToTrash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := createMCPProjectStore(t)
	defer store.Close()
	deletedAt := asset.UpdatedAt.Add(time.Second)
	deleted := asset
	deleted.DeletedAt = &deletedAt
	deleted.Revision++
	deleted.UpdatedAt = deletedAt
	if err := store.DeleteAsset(ctx, deleted, asset.Revision, false, deletedAt); err != nil {
		t.Fatal(err)
	}
	clock := &integrationClock{at: deletedAt}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, integrationStoreResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{Queries: integrationProjectQueries{store: store, projectID: projectValue.ID}, Drafts: changeSets, DraftCleanup: changeSets, Random: bytes.NewReader(bytes.Repeat([]byte{0x49}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43129")}
	sessionID := agent.SessionID("ags_IIIIIIIIIIIIIIIIIIIIIIIIII")
	turnID := agent.TurnID("agt_JJJJJJJJJJJJJJJJJJJJJJJJJJ")
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: "route_IIIIIIIIIIIIIIIIIIIIIIIIIIIIIIII", SessionID: sessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_restore", CodexTurnID: "turn_restore"}); err != nil {
		t.Fatal(err)
	}
	credential := server.Credential()
	declareProjectChange(t, server, endpoint, credential.Value, "assets")
	active := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{})
	if active.IsError || strings.Contains(active.Text, asset.ID.String()) {
		t.Fatalf("list_assets exposed deleted Asset = %+v", active)
	}
	hidden := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": asset.ID.String()})
	if !hidden.IsError || !strings.Contains(hidden.Text, "object_outside_scope") {
		t.Fatalf("get_asset exposed deleted Asset = %+v", hidden)
	}

	listed := callTool(t, server, endpoint, credential.Value, "list_deleted_assets", map[string]any{})
	if listed.IsError || !strings.Contains(listed.Text, asset.ID.String()) || !strings.Contains(listed.Text, `"name":"主角"`) {
		t.Fatalf("list_deleted_assets = %+v", listed)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "恢复主角资产", "summary": "保留原身份与素材", "operation_key": "restore-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	beforeRead := callTool(t, server, endpoint, credential.Value, "restore_asset", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "restore-append", "asset_id": asset.ID.String(),
	})
	if !beforeRead.IsError || !strings.Contains(beforeRead.Text, "object_not_observed") {
		t.Fatalf("restore before get_deleted_asset = %+v", beforeRead)
	}
	read := callTool(t, server, endpoint, credential.Value, "get_deleted_asset", map[string]any{"asset_id": asset.ID.String()})
	if read.IsError || !strings.Contains(read.Text, asset.Prompt) || !strings.Contains(read.Text, `"revision":2`) {
		t.Fatalf("get_deleted_asset = %+v", read)
	}
	appended := callTool(t, server, endpoint, credential.Value, "restore_asset", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "restore-append", "asset_id": asset.ID.String(),
	})
	_, revision = changeSetIdentity(t, appended)
	finalized := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "restore-finalize",
	})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize restore ChangeSet = %+v", finalized)
	}
	restored, err := store.Asset(ctx, asset.ID)
	if err != nil || restored.Asset.IsDeleted() || restored.Asset.Revision != 3 || restored.Asset.Prompt != asset.Prompt || restored.Asset.IdentityID != asset.IdentityID {
		t.Fatalf("restored Asset = %+v, %v", restored, err)
	}
	operations, err := store.ChangeSetOperations(ctx, changeSetID, 0, 10)
	if err != nil || len(operations) != 1 || operations[0].Type != changeset.RestoreEpisodeAsset || operations[0].AssetRestoration == nil || operations[0].AssetRestoration.AssetID != asset.ID {
		t.Fatalf("stored restoration = %+v, %v", operations, err)
	}
	undo, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: changeSetID, OperationKey: "restore-undo"})
	if err != nil || undo.ChangeSet.Status != changeset.StatusApplied || undo.ChangeSet.RevertsChangeSetID != changeSetID {
		t.Fatalf("Undo restoration = %+v, %v", undo, err)
	}
	againDeleted, err := store.Asset(ctx, asset.ID)
	if err != nil || !againDeleted.Asset.IsDeleted() || againDeleted.Asset.Revision != 4 || againDeleted.Asset.Prompt != asset.Prompt || againDeleted.Asset.IdentityID != asset.IdentityID {
		t.Fatalf("Asset after Undo = %+v, %v", againDeleted, err)
	}
}

func TestMCPDraftsCompleteEmptyStoryboardWithObservedInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := createMCPProjectStore(t)
	defer store.Close()
	clock := &integrationClock{at: asset.UpdatedAt}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, integrationStoreResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{Queries: integrationProjectQueries{store: store, projectID: projectValue.ID}, Drafts: changeSets, DraftCleanup: changeSets, Random: bytes.NewReader(bytes.Repeat([]byte{0x45}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43125")}
	sessionID := agent.SessionID("ags_QQQQQQQQQQQQQQQQQQQQQQQQQQ")
	turnID := agent.TurnID("agt_RRRRRRRRRRRRRRRRRRRRRRRRRR")
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: "route_QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ", SessionID: sessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_storyboard", CodexTurnID: "turn_storyboard", VideoPromptTarget: testSelectedVideoPromptContext()}); err != nil {
		t.Fatal(err)
	}
	credential := server.Credential()
	declareProjectChange(t, server, endpoint, credential.Value, "storyboard")
	modelContext := callTool(t, server, endpoint, credential.Value, "get_episode_context", map[string]any{})
	if modelContext.IsError || !strings.Contains(modelContext.Text, "subject_definitions") || !strings.Contains(modelContext.Text, "integrated_multimodal_description") {
		t.Fatalf("ordinary Storyboard request omitted current-model rules: %+v", modelContext)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "创建 Storyboard", "operation_key": "storyboard-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	operations := []map[string]any{
		{"type": "create_scene", "ref": "scene_1", "title": "开场"},
		{"type": "create_clip", "ref": "clip_1", "scene_ref": "scene_1", "title": "主角入场", "planned_duration_seconds": 6, "video_prompt": "subject_definitions:\n<Subject 1>: the protagonist\nsummary:\nThe protagonist enters the room.\nretention_analysis:\n<Picture 1>: identity\ndetailed_description:\n<Subject 1> walks slowly into the room.\noverall_soundscape:\nRoom tone\nnon_diegetic_music:\nNone", "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va"},
		{"type": "add_clip_asset_reference", "clip_ref": "clip_1", "asset_id": asset.ID.String()},
	}
	beforeRead := callTool(t, server, endpoint, credential.Value, "append_storyboard_operations", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "storyboard-append", "operations": operations})
	if !beforeRead.IsError || !strings.Contains(beforeRead.Text, "script_not_observed") {
		t.Fatalf("Storyboard append before reads = %+v", beforeRead)
	}
	for name, args := range map[string]map[string]any{
		"read_script": {}, "list_scenes": {}, "get_asset": {"asset_id": asset.ID.String()},
	} {
		result := callTool(t, server, endpoint, credential.Value, name, args)
		if result.IsError {
			t.Fatalf("%s = %+v", name, result)
		}
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_storyboard_operations", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "storyboard-append", "operations": operations})
	_, revision = changeSetIdentity(t, appended)
	finalized := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "storyboard-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize storyboard ChangeSet = %+v", finalized)
	}
	stored, err := store.ChangeSetOperations(ctx, changeSetID, 0, 10)
	if err != nil || len(stored) != 3 || stored[0].SceneCreation == nil || stored[1].ClipCreation == nil || stored[2].ClipAssetRef == nil {
		t.Fatalf("stored Storyboard operations = %+v, %v", stored, err)
	}
	promptSet := parseVideoPromptSet(stored[1].ClipCreation.Clip.VideoPrompts.Reference)
	if promptSet.Families["h3"].Profile != "h3_ref2va@1" || !strings.Contains(promptSet.Families["h3"].Prompt, "<Subject 1>") {
		t.Fatalf("Storyboard video Prompt was not stored as a model-ready H3 variant: %+v", promptSet)
	}
	clipID := stored[1].ClipCreation.Clip.ID
	manualClip, err := store.Clip(ctx, clipID)
	if err != nil {
		t.Fatal(err)
	}
	wanTarget := videoPromptTarget{Family: "wan3", Profile: "wan3@1", GeneratorID: string(project.GeneratorVideoWanOfficial), ProviderModelID: "wan3.0-video-prime"}
	manualClip.Clip.VideoPrompts.Reference, err = parseVideoPromptSet(manualClip.Clip.VideoPrompts.Reference).withVariant(wanTarget, wanTarget.GeneratorID, wanTarget.ProviderModelID, "Independent Wan family Prompt", []videoPromptReference{{Key: "asset:" + asset.ID.String(), Purposes: []string{"identity"}}})
	if err != nil {
		t.Fatal(err)
	}
	manualClip.Clip.VideoPrompts.FirstImage = "independent first-image mode"
	manualClip.Clip.VideoPrompts.FirstLastImage = "independent first-last-image mode"
	manualClip.Clip.Revision++
	clock.at = clock.at.Add(time.Second)
	manualClip.Clip.UpdatedAt = clock.at
	if err := store.UpdateClip(ctx, manualClip.Clip, manualClip.Clip.Revision-1, clock.at); err != nil {
		t.Fatal(err)
	}
	if read := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": clipID.String(), "prompt_fields": []string{"reference_video_prompt"}}); read.IsError {
		t.Fatalf("observe created Clip = %+v", read)
	}
	promptChange := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "调整镜头资产", "operation_key": "clip-assets-begin"})
	promptChangeID, promptChangeRevision := changeSetIdentity(t, promptChange)
	promptChange = callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": promptChangeID.String(), "expected_change_set_revision": promptChangeRevision, "operation_key": "clip-assets-append",
		"operations": []map[string]any{{"type": "update_clip_prompts", "clip_id": clipID.String(), "video_prompt_mode": "reference", "video_prompt": "人物独自在房间中缓慢抬头。", "asset_ids": []string{}}},
	})
	_, promptChangeRevision = changeSetIdentity(t, promptChange)
	promptChange = callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": promptChangeID.String(), "expected_change_set_revision": promptChangeRevision, "operation_key": "clip-assets-finalize"})
	if promptChange.IsError {
		t.Fatalf("apply atomic Clip Prompt/Asset change = %+v", promptChange)
	}
	changedClip, err := store.Clip(ctx, clipID)
	if err != nil || parseVideoPromptSet(changedClip.Clip.VideoPrompts.Reference).Families["h3"].Prompt != "人物独自在房间中缓慢抬头。" || len(changedClip.AssetReferences) != 0 {
		t.Fatalf("atomic Clip change = %+v, %v", changedClip, err)
	}
	if parseVideoPromptSet(changedClip.Clip.VideoPrompts.Reference).Families["wan3"].Prompt != "Independent Wan family Prompt" || changedClip.Clip.VideoPrompts.FirstImage != manualClip.Clip.VideoPrompts.FirstImage || changedClip.Clip.VideoPrompts.FirstLastImage != manualClip.Clip.VideoPrompts.FirstLastImage {
		t.Fatalf("ordinary rewrite overwrote another model family or generation mode: %+v", changedClip.Clip.VideoPrompts)
	}
	clock.at = clock.at.Add(time.Second)
	if _, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: promptChangeID, OperationKey: "clip-assets-undo"}); err != nil {
		t.Fatalf("undo atomic Clip Prompt/Asset change: %v", err)
	}
	restoredClip, err := store.Clip(ctx, clipID)
	restoredSet := parseVideoPromptSet(restoredClip.Clip.VideoPrompts.Reference)
	if err != nil || restoredSet.Families["h3"].Profile != "h3_ref2va@1" || len(restoredClip.AssetReferences) != 1 || restoredClip.AssetReferences[0].AssetID != asset.ID {
		t.Fatalf("undo atomic Clip change = %+v, %v", restoredClip, err)
	}
	storyboard, err := store.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 2 || len(storyboard.Scenes) != 1 || len(storyboard.Scenes[0].Clips) != 1 {
		t.Fatalf("auto-applied MCP Storyboard = %+v, %v", storyboard, err)
	}
	if listed := callTool(t, server, endpoint, credential.Value, "list_scenes", map[string]any{}); listed.IsError {
		t.Fatalf("re-read Scenes = %+v", listed)
	}
	existingSceneID := stored[0].SceneCreation.Scene.ID
	appendClip := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "在开场前插入视频单元", "operation_key": "storyboard-existing-begin"})
	appendClipID, appendClipRevision := changeSetIdentity(t, appendClip)
	existingSceneOperations := []map[string]any{
		{"type": "create_clip", "ref": "clip_1b", "scene_id": existingSceneID.String(), "before_clip_id": clipID.String(), "title": "主角环顾房间", "planned_duration_seconds": 6, "video_prompt": "The protagonist stops and looks around as the camera tracks sideways.", "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va"},
	}
	appendClip = callTool(t, server, endpoint, credential.Value, "append_storyboard_operations", map[string]any{"change_set_id": appendClipID.String(), "expected_change_set_revision": appendClipRevision, "operation_key": "storyboard-existing-append", "operations": existingSceneOperations})
	if !appendClip.IsError || !strings.Contains(appendClip.Text, "object_not_observed") {
		t.Fatalf("append existing Scene Clip before reading Clips = %+v", appendClip)
	}
	if clips := callTool(t, server, endpoint, credential.Value, "get_clips", map[string]any{"scene_id": existingSceneID.String()}); clips.IsError {
		t.Fatalf("read existing Scene Clips = %+v", clips)
	}
	appendClip = callTool(t, server, endpoint, credential.Value, "append_storyboard_operations", map[string]any{"change_set_id": appendClipID.String(), "expected_change_set_revision": appendClipRevision, "operation_key": "storyboard-existing-append", "operations": existingSceneOperations})
	_, appendClipRevision = changeSetIdentity(t, appendClip)
	appendClip = callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": appendClipID.String(), "expected_change_set_revision": appendClipRevision, "operation_key": "storyboard-existing-finalize"})
	if appendClip.IsError || !strings.Contains(appendClip.Text, `"status":"applied"`) {
		t.Fatalf("append existing Scene Clip = %+v", appendClip)
	}
	storyboard, err = store.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 3 || len(storyboard.Scenes) != 1 || len(storyboard.Scenes[0].Clips) != 2 || storyboard.Scenes[0].Clips[0].Clip.Title != "主角环顾房间" || storyboard.Scenes[0].Clips[1].Clip.ID != clipID {
		t.Fatalf("Storyboard after inserting before existing Clip = %+v, %v", storyboard, err)
	}
	if listed := callTool(t, server, endpoint, credential.Value, "list_scenes", map[string]any{}); listed.IsError {
		t.Fatalf("re-read Scenes after Clip append = %+v", listed)
	}
	second := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "创建第二场", "operation_key": "storyboard-second-begin"})
	secondID, secondRevision := changeSetIdentity(t, second)
	secondOperations := []map[string]any{
		{"type": "create_scene", "ref": "scene_2", "title": "会面"},
		{"type": "create_clip", "ref": "clip_2", "scene_ref": "scene_2", "title": "人物会面", "planned_duration_seconds": 6, "video_prompt": "Two people meet in the hall as the camera slowly pushes in.", "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va"},
	}
	second = callTool(t, server, endpoint, credential.Value, "append_storyboard_operations", map[string]any{"change_set_id": secondID.String(), "expected_change_set_revision": secondRevision, "operation_key": "storyboard-second-append", "operations": secondOperations})
	_, secondRevision = changeSetIdentity(t, second)
	second = callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": secondID.String(), "expected_change_set_revision": secondRevision, "operation_key": "storyboard-second-finalize"})
	if second.IsError || !strings.Contains(second.Text, `"status":"applied"`) {
		t.Fatalf("apply second Scene ChangeSet = %+v", second)
	}
	storyboard, err = store.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 4 || len(storyboard.Scenes) != 2 || len(storyboard.Scenes[0].Clips) != 2 {
		t.Fatalf("incremental Storyboard = %+v, %v", storyboard, err)
	}
	clock.at = clock.at.Add(time.Second)
	if _, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: appendClipID, OperationKey: "storyboard-existing-undo"}); err != nil {
		t.Fatalf("undo existing Scene Clip append: %v", err)
	}
	storyboard, err = store.Storyboard(ctx, episode.ID)
	if err != nil || storyboard.Revision != 5 || len(storyboard.Scenes) != 2 || len(storyboard.Scenes[0].Clips) != 1 || storyboard.Scenes[0].Scene.ID != existingSceneID {
		t.Fatalf("Storyboard after undoing existing Scene Clip append = %+v, %v", storyboard, err)
	}
}

func TestMCPMergesAdjacentClipsAndInheritsOnlyBoundaryFrames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := createMCPProjectStore(t)
	defer store.Close()
	at := asset.UpdatedAt.Add(time.Second)
	scene := project.Scene{ID: project.SceneID("scn_" + strings.Repeat("G", 26)), ProjectID: projectValue.ID, EpisodeID: episode.ID,
		Position: project.StoryboardOrderStep, Title: "连续动作", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	makeClip := func(id project.ClipID, position int, title string, seconds int64, when time.Time) project.Clip {
		return project.Clip{ID: id, ProjectID: projectValue.ID, EpisodeID: episode.ID, SceneID: scene.ID, Position: position, Title: title,
			PlannedDurationSeconds: seconds, GenerationMode: project.ClipGenerationReference, Revision: 1, CreatedAt: when, UpdatedAt: when}
	}
	at = at.Add(time.Second)
	first := makeClip(project.ClipID("clp_"+strings.Repeat("H", 26)), project.StoryboardOrderStep, "提醒", 6, at)
	if err := store.CreateClip(ctx, first, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	last := makeClip(project.ClipID("clp_"+strings.Repeat("I", 26)), 2*project.StoryboardOrderStep, "回应", 8, at)
	if err := store.CreateClip(ctx, last, 3, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	trailing := makeClip(project.ClipID("clp_"+strings.Repeat("J", 26)), 3*project.StoryboardOrderStep, "尾声", 5, at)
	if err := store.CreateClip(ctx, trailing, 4, at); err != nil {
		t.Fatal(err)
	}
	addImage := func(clipID project.ClipID, kind project.MediaSlotKind, suffix byte, when time.Time) project.ArtifactID {
		slotID, _ := project.FixedMediaSlotID(clipID.String(), kind)
		artifactID := project.ArtifactID("art_" + strings.Repeat(string(suffix), 26))
		versionID := project.MediaVersionID("mvr_" + strings.Repeat(string(suffix), 26))
		filename := strings.ToLower(string(suffix)) + ".png"
		artifact := project.Artifact{ID: artifactID, ProjectID: projectValue.ID, Kind: project.MediaKindImage, RelativePath: "media/" + filename,
			SHA256: strings.Repeat(strings.ToLower(string(suffix)), 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: filename, Width: 720, Height: 1280, CreatedAt: when}
		version := project.MediaVersion{ID: versionID, SlotID: slotID, ArtifactID: artifactID, SequenceNumber: 1, Revision: 1, CreatedAt: when, UpdatedAt: when}
		if err := store.AddMediaVersion(ctx, artifact, version, 1, true, when); err != nil {
			t.Fatal(err)
		}
		return artifactID
	}
	at = at.Add(time.Second)
	firstArtifact := addImage(first.ID, project.MediaSlotClipFirstImage, 'A', at)
	at = at.Add(time.Second)
	lastArtifact := addImage(last.ID, project.MediaSlotClipLastImage, 'B', at)
	at = at.Add(time.Second)
	videoSlotID, _ := project.FixedMediaSlotID(first.ID.String(), project.MediaSlotClipVideo)
	videoArtifact := project.Artifact{ID: project.ArtifactID("art_" + strings.Repeat("C", 26)), ProjectID: projectValue.ID, Kind: project.MediaKindVideo,
		RelativePath: "media/c.mp4", SHA256: strings.Repeat("c", 64), ByteSize: 20, MIMEType: "video/mp4", OriginalFilename: "c.mp4",
		Width: 720, Height: 1280, DurationMilliseconds: 6_000, FPSNumerator: 25, FPSDenominator: 1, CreatedAt: at}
	videoVersion := project.MediaVersion{ID: project.MediaVersionID("mvr_" + strings.Repeat("C", 26)), SlotID: videoSlotID, ArtifactID: videoArtifact.ID,
		SequenceNumber: 1, TrimOutMilliseconds: 6_000, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.AddMediaVersion(ctx, videoArtifact, videoVersion, 1, true, at); err != nil {
		t.Fatal(err)
	}

	clock := &integrationClock{at: at}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, integrationStoreResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{Queries: integrationProjectQueries{store: store, projectID: projectValue.ID}, Drafts: changeSets, DraftCleanup: changeSets, Random: bytes.NewReader(bytes.Repeat([]byte{0x49}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43127")}
	sessionID := agent.SessionID("ags_" + strings.Repeat("G", 26))
	turnID := agent.TurnID("agt_" + strings.Repeat("H", 26))
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: "route_" + strings.Repeat("G", 32), SessionID: sessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_merge", CodexTurnID: "turn_merge", VideoPromptTarget: testSelectedVideoPromptContext()}); err != nil {
		t.Fatal(err)
	}
	credential := server.Credential()
	declareProjectChange(t, server, endpoint, credential.Value, "storyboard")
	if result := callTool(t, server, endpoint, credential.Value, "get_clips", map[string]any{"scene_id": scene.ID.String(), "limit": 50}); result.IsError {
		t.Fatalf("get_clips = %+v", result)
	}
	for _, clip := range []project.Clip{first, last} {
		if result := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": clip.ID.String()}); result.IsError {
			t.Fatalf("get_clip = %+v", result)
		}
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "合并连续动作", "operation_key": "merge-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	appended := callTool(t, server, endpoint, credential.Value, "append_clip_merge_operation", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "merge-append",
		"source_clip_ids": []string{first.ID.String(), last.ID.String()}, "title": "提醒并回应", "description": "连续动作", "dialogue": "完整对白", "creative_intent": "保持连贯",
		"first_image_prompt": "开场构图", "last_image_prompt": "结束构图", "video_prompt": "两人连续完成提醒和回应。", "generation_mode": "reference",
	})
	_, revision = changeSetIdentity(t, appended)
	finalized := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "merge-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize merge = %+v", finalized)
	}
	storyboard, err := store.Storyboard(ctx, episode.ID)
	if err != nil || len(storyboard.Scenes) != 1 || len(storyboard.Scenes[0].Clips) != 2 {
		t.Fatalf("merged Storyboard = %+v, %v", storyboard, err)
	}
	merged := storyboard.Scenes[0].Clips[0]
	mergedPrompt := parseVideoPromptSet(merged.Clip.VideoPrompts.Reference).Families["h3"]
	if mergedPrompt.Profile != "h3_ref2va@1" || mergedPrompt.GeneratorID != "video.h3.shotforward" || mergedPrompt.ProviderModelID != "h3-ref2va" || merged.Clip.VideoPrompts.FirstImage != "" || merged.Clip.VideoPrompts.FirstLastImage != "" {
		t.Fatalf("merged Prompt model variant = %+v", merged.Clip.VideoPrompts)
	}
	if merged.Clip.PlannedDurationSeconds != 14 || len(merged.DerivedFromClipIDs) != 2 || merged.DerivedFromClipIDs[0] != first.ID || merged.DerivedFromClipIDs[1] != last.ID {
		t.Fatalf("merged Clip = %+v", merged)
	}
	for kind, wantArtifact := range map[project.MediaSlotKind]project.ArtifactID{project.MediaSlotClipFirstImage: firstArtifact, project.MediaSlotClipLastImage: lastArtifact} {
		slotID, _ := project.FixedMediaSlotID(merged.Clip.ID.String(), kind)
		slot, err := store.MediaSlot(ctx, slotID, false)
		if err != nil || slot.Current == nil || slot.Current.ArtifactID != wantArtifact {
			t.Fatalf("inherited %s = %+v, %v", kind, slot, err)
		}
	}
	for _, kind := range []project.MediaSlotKind{project.MediaSlotClipVideo, project.MediaSlotAudioPrimary} {
		slotID, _ := project.FixedMediaSlotID(merged.Clip.ID.String(), kind)
		slot, err := store.MediaSlot(ctx, slotID, false)
		if err != nil || slot.Current != nil || len(slot.Versions) != 0 {
			t.Fatalf("non-boundary media %s was inherited: %+v, %v", kind, slot, err)
		}
	}
	preservedVideo, err := store.MediaSlot(ctx, videoSlotID, true)
	if err != nil || preservedVideo.Current == nil || preservedVideo.Current.ArtifactID != videoArtifact.ID {
		t.Fatalf("source video was not preserved: %+v, %v", preservedVideo, err)
	}
	if _, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: changeSetID, OperationKey: "merge-undo"}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Storyboard(ctx, episode.ID)
	if err != nil || len(restored.Scenes[0].Clips) != 3 || restored.Scenes[0].Clips[0].Clip.ID != first.ID || restored.Scenes[0].Clips[1].Clip.ID != last.ID || restored.Scenes[0].Clips[2].Clip.ID != trailing.ID {
		t.Fatalf("Storyboard after merge Undo = %+v, %v", restored, err)
	}

	clock.at = clock.at.Add(time.Second)
	if result := callTool(t, server, endpoint, credential.Value, "get_clips", map[string]any{"scene_id": scene.ID.String(), "limit": 50}); result.IsError {
		t.Fatalf("get_clips before split = %+v", result)
	}
	if result := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": last.ID.String()}); result.IsError {
		t.Fatalf("get_clip before split = %+v", result)
	}
	begin = callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "拆分回应", "operation_key": "split-begin"})
	splitID, splitRevision := changeSetIdentity(t, begin)
	appended = callTool(t, server, endpoint, credential.Value, "append_clip_split_operation", map[string]any{
		"change_set_id": splitID.String(), "expected_change_set_revision": splitRevision, "operation_key": "split-append", "source_clip_id": last.ID.String(),
		"replacements": []map[string]any{
			{"title": "听见提醒", "description": "先听见提醒", "dialogue": "提醒", "creative_intent": "建立信息", "planned_duration_seconds": 6, "first_image_prompt": "开场", "last_image_prompt": "", "video_prompt": "人物听见提醒并转身。", "generation_mode": "reference", "asset_ids": []string{}},
			{"title": "作出回应", "description": "随后回应", "dialogue": "回应", "creative_intent": "完成动作", "planned_duration_seconds": 6, "first_image_prompt": "", "last_image_prompt": "结束", "video_prompt": "人物完成回应并停住。", "generation_mode": "reference", "asset_ids": []string{}},
		},
	})
	_, splitRevision = changeSetIdentity(t, appended)
	finalized = callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": splitID.String(), "expected_change_set_revision": splitRevision, "operation_key": "split-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize split = %+v", finalized)
	}
	splitStoryboard, err := store.Storyboard(ctx, episode.ID)
	if err != nil || len(splitStoryboard.Scenes[0].Clips) != 4 || splitStoryboard.Scenes[0].Clips[0].Clip.ID != first.ID || splitStoryboard.Scenes[0].Clips[3].Clip.ID != trailing.ID || splitStoryboard.Scenes[0].Clips[1].Clip.Title != "听见提醒" || splitStoryboard.Scenes[0].Clips[2].Clip.Title != "作出回应" {
		t.Fatalf("Storyboard after split = %+v, %v", splitStoryboard, err)
	}
	if splitStoryboard.PlannedDurationSeconds != first.PlannedDurationSeconds+trailing.PlannedDurationSeconds+12 {
		t.Fatalf("8-second source split into 12 seconds did not update the Storyboard total: %d", splitStoryboard.PlannedDurationSeconds)
	}
	for _, replacement := range splitStoryboard.Scenes[0].Clips[1:3] {
		variant := parseVideoPromptSet(replacement.Clip.VideoPrompts.Reference).Families["h3"]
		if replacement.Clip.PlannedDurationSeconds != 6 || variant.Profile != "h3_ref2va@1" || variant.ProviderModelID != "h3-ref2va" || replacement.Clip.VideoPrompts.FirstImage != "" || replacement.Clip.VideoPrompts.FirstLastImage != "" {
			t.Fatalf("split replacement lost selected model metadata: %+v", replacement.Clip)
		}
	}
	clock.at = clock.at.Add(time.Second)
	if _, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: splitID, OperationKey: "split-undo"}); err != nil {
		t.Fatal(err)
	}
	restored, err = store.Storyboard(ctx, episode.ID)
	if err != nil || len(restored.Scenes[0].Clips) != 3 || restored.Scenes[0].Clips[0].Clip.ID != first.ID || restored.Scenes[0].Clips[1].Clip.ID != last.ID || restored.Scenes[0].Clips[2].Clip.ID != trailing.ID {
		t.Fatalf("Storyboard after split Undo = %+v, %v", restored, err)
	}
}

func TestMCPReadsAndAutoAppliesCompleteScriptChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, projectValue, episode, asset := createMCPProjectStore(t)
	defer store.Close()
	clock := &integrationClock{at: asset.UpdatedAt}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, integrationStoreResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{Queries: integrationProjectQueries{store: store, projectID: projectValue.ID}, Drafts: changeSets, DraftCleanup: changeSets, Random: bytes.NewReader(bytes.Repeat([]byte{0x46}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43126")}
	sessionID := agent.SessionID("ags_TTTTTTTTTTTTTTTTTTTTTTTTTT")
	turnID := agent.TurnID("agt_VVVVVVVVVVVVVVVVVVVVVVVVVV")
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: "route_TTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTT", SessionID: sessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(sessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_script", CodexTurnID: "turn_script"}); err != nil {
		t.Fatal(err)
	}
	credential := server.Credential()
	intent := callTool(t, server, endpoint, credential.Value, "declare_turn_intent", map[string]any{"intent": "project_change", "target": "script"})
	if intent.IsError || !strings.Contains(intent.Text, `"target":"script"`) {
		t.Fatalf("declare_turn_intent = %+v", intent)
	}
	if read := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{}); read.IsError {
		t.Fatalf("read_script = %+v", read)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "撰写黛玉进府", "operation_key": "script-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	markdown := "# 黛玉进府\n\n黛玉乘轿来到荣国府门前。"
	appended := callTool(t, server, endpoint, credential.Value, "append_script_operation", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "script-append", "markdown": markdown})
	_, revision = changeSetIdentity(t, appended)
	finalized := callTool(t, server, endpoint, credential.Value, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "script-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize script ChangeSet = %+v", finalized)
	}
	current, err := store.Episode(ctx, episode.ID)
	if err != nil || current.Script.Markdown != markdown || current.Script.Revision != episode.Script.Revision+1 {
		t.Fatalf("auto-applied script = %+v, %v", current.Script, err)
	}
	undone, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: changeSetID, OperationKey: "script-undo"})
	if err != nil || undone.ChangeSet.Status != changeset.StatusApplied || undone.ChangeSet.RevertsChangeSetID != changeSetID {
		t.Fatalf("Undo script ChangeSet = %+v, %v", undone, err)
	}
	current, err = store.Episode(ctx, episode.ID)
	if err != nil || current.Script.Markdown != episode.Script.Markdown || current.Script.Revision != episode.Script.Revision+2 {
		t.Fatalf("script after Undo = %+v, %v", current.Script, err)
	}
}

type integrationEvents struct {
	mutex  sync.Mutex
	values []agentapp.StudioEvent
}

func (e *integrationEvents) Publish(value agentapp.StudioEvent) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	e.values = append(e.values, value)
}

func (e *integrationEvents) snapshot() []agentapp.StudioEvent {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return append([]agentapp.StudioEvent(nil), e.values...)
}

func declareProjectChange(t *testing.T, server *Server, endpoint, token, target string) {
	t.Helper()
	result := callTool(t, server, endpoint, token, "declare_turn_intent", map[string]any{"intent": "project_change", "target": target})
	if result.IsError {
		t.Fatalf("declare project change intent = %+v", result)
	}
}

func changeSetIdentity(t *testing.T, result toolResultView) (changeset.ChangeSetID, int64) {
	t.Helper()
	if result.IsError {
		t.Fatalf("ChangeSet tool failed: %+v", result)
	}
	var payload struct {
		ID       string `json:"change_set_id"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal([]byte(result.Text), &payload); err != nil {
		t.Fatal(err)
	}
	id, err := changeset.ParseChangeSetID(payload.ID)
	if err != nil || payload.Revision < 1 {
		t.Fatalf("ChangeSet identity = %q/%d, %v", payload.ID, payload.Revision, err)
	}
	return id, payload.Revision
}

type integrationStoreResolver struct{ store *projectstore.Store }

func (r integrationStoreResolver) ResolveChangeSetStore(context.Context, project.ProjectID) (changesetapp.Store, error) {
	return r.store, nil
}

type integrationClock struct{ at time.Time }

func (c *integrationClock) Now() time.Time {
	c.at = c.at.Add(time.Second)
	return c.at
}

type integrationProjectQueries struct {
	store     *projectstore.Store
	projectID project.ProjectID
}

func (q integrationProjectQueries) GetEpisode(ctx context.Context, input projectapp.GetEpisodeInput) (project.Episode, error) {
	if input.ProjectID != q.projectID {
		return project.Episode{}, projectapp.ErrEpisodeNotFound
	}
	value, err := q.store.Episode(ctx, input.EpisodeID)
	if err != nil || value.ProjectID != input.ProjectID {
		return project.Episode{}, projectapp.ErrEpisodeNotFound
	}
	return value, nil
}

func (q integrationProjectQueries) ListEpisodeAssets(ctx context.Context, input projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrEpisodeNotFound
	}
	return q.store.EpisodeAssets(ctx, input.EpisodeID, input.IncludeDeleted)
}

func (q integrationProjectQueries) FindPriorAssetCandidates(context.Context, projectapp.FindPriorAssetCandidatesInput) ([]projectapp.PriorAssetCandidate, error) {
	return nil, nil
}

func (q integrationProjectQueries) GetAsset(ctx context.Context, input projectapp.GetAssetInput) (project.EpisodeAsset, error) {
	if input.ProjectID != q.projectID {
		return project.EpisodeAsset{}, projectapp.ErrAssetNotFound
	}
	value, err := q.store.Asset(ctx, input.AssetID)
	if err != nil || value.Asset.ProjectID != input.ProjectID {
		return project.EpisodeAsset{}, projectapp.ErrAssetNotFound
	}
	return value, nil
}

func (q integrationProjectQueries) GetClip(ctx context.Context, input projectapp.GetClipInput) (project.ClipDetails, error) {
	if input.ProjectID != q.projectID {
		return project.ClipDetails{}, projectapp.ErrClipNotFound
	}
	value, err := q.store.Clip(ctx, input.ClipID)
	if err != nil || value.Clip.ProjectID != input.ProjectID {
		return project.ClipDetails{}, projectapp.ErrClipNotFound
	}
	return value, nil
}

func (q integrationProjectQueries) GetStoryboard(ctx context.Context, input projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error) {
	if input.ProjectID != q.projectID {
		return projectapp.StoryboardResult{}, projectapp.ErrEpisodeNotFound
	}
	value, err := q.store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return projectapp.StoryboardResult{}, err
	}
	episode, err := q.store.Episode(ctx, input.EpisodeID)
	if err != nil {
		return projectapp.StoryboardResult{}, err
	}
	return projectapp.StoryboardResult{Storyboard: value, TargetDuration: episode.TargetDuration}, nil
}

func (q integrationProjectQueries) GetMediaSlot(ctx context.Context, input projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error) {
	if input.ProjectID != q.projectID {
		return project.MediaSlotDetails{}, projectapp.ErrMediaSlotNotFound
	}
	return q.store.MediaSlot(ctx, input.MediaSlotID, input.IncludeDeleted)
}

func (q integrationProjectQueries) ListClipReferenceMedia(ctx context.Context, input projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrClipNotFound
	}
	return q.store.ClipReferenceArtifacts(ctx, input.ClipID)
}

func (q integrationProjectQueries) ListEpisodeReferenceMedia(ctx context.Context, input projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrEpisodeNotFound
	}
	return q.store.EpisodeReferenceMediaList(ctx, input.EpisodeID, input.IncludeDeleted)
}

func createMCPProjectStore(t *testing.T) (*projectstore.Store, project.Project, project.Episode, project.Asset) {
	t.Helper()
	ctx := context.Background()
	store, err := projectstore.Open(ctx, filepath.Join(t.TempDir(), "project.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 14, 0, 0, 0, time.UTC)
	projectID := project.ProjectID("prj_MMMMMMMMMMMMMMMMMMMMMMMMMM")
	episodeID := project.EpisodeID("ep_MMMMMMMMMMMMMMMMMMMMMMMMMM")
	output, _ := project.NewOutputSpec(720, 1280)
	projectValue := project.Project{ID: projectID, Name: "MCP Project", Output: output, Revision: 1, CreatedAt: now, UpdatedAt: now}
	episode := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: episodeID, ProjectID: projectID, SequenceNumber: 1, Title: "第 1 集", TargetDuration: time.Minute, Revision: 1, CreatedAt: now, UpdatedAt: now}, Script: project.Script{Revision: 1, UpdatedAt: now}}
	if err := store.Initialize(ctx, projectValue, episode); err != nil {
		store.Close()
		t.Fatal(err)
	}
	at := now.Add(time.Second)
	identity := project.AssetIdentity{ID: project.AssetIdentityID("asi_MMMMMMMMMMMMMMMMMMMMMMMMMM"), ProjectID: projectID, Kind: project.AssetKindCharacter, Name: "主角", Revision: 1, CreatedAt: at, UpdatedAt: at}
	asset := project.Asset{ID: project.AssetID("ast_MMMMMMMMMMMMMMMMMMMMMMMMMM"), ProjectID: projectID, EpisodeID: episodeID, IdentityID: identity.ID, Prompt: "original integration prompt", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, projectValue, episode, asset
}
