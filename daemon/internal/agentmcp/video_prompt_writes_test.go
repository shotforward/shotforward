package agentmcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func testSelectedVideoPromptContext() *VideoPromptContext {
	return &VideoPromptContext{GeneratorID: "video.h3.shotforward", ProviderModelID: "h3-ref2va", KeyframeProviderModelID: "h3-fl2va"}
}

func TestVideoPromptContextValidationAndModeGuidance(t *testing.T) {
	t.Parallel()
	if (*VideoPromptContext)(nil).Validate() != nil || (*VideoPromptContext)(nil).InstructionText() != "" {
		t.Fatal("absent model context must remain compatible with non-video requests")
	}
	selected := testSelectedVideoPromptContext()
	if err := selected.Validate(); err != nil {
		t.Fatal(err)
	}
	instructions := selected.InstructionText()
	for _, required := range []string{"creation, rewrite, split, and merge", "h3_ref2va@1", "subject_definitions", "retention_analysis", "h3_base@1", "integrated_multimodal_description", "user's requested language", "<lyrics>", "<on_screen_text>", "English copy without overwriting"} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("selected model rules omitted %q: %s", required, instructions)
		}
	}
	if strings.Contains(instructions, "bodies in English") || strings.Contains(videoPromptAdaptationInstructions, "use English structured fields") {
		t.Fatal("H3 editable prompts must not be forced into English")
	}
	withoutKeyframes := *selected
	withoutKeyframes.KeyframeProviderModelID = ""
	if _, err := withoutKeyframes.selected("", project.ClipGenerationFirstImage); err == nil {
		t.Fatal("H3 reference model was silently reused as a keyframe model")
	}
	for _, invalid := range []*VideoPromptContext{
		{GeneratorID: strings.Repeat("x", 1_000), ProviderModelID: "model"},
		{GeneratorID: "video.h3.unknown", ProviderModelID: "model"},
		{GeneratorID: selected.GeneratorID, ProviderModelID: strings.Repeat("m", 201)},
		{GeneratorID: selected.GeneratorID, ProviderModelID: "model\nextra instruction"},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("invalid model context was accepted: %+v", invalid)
		}
	}
}

func TestSelectedFocusModelIsScopedAndCopied(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, _ := newMCPFixture(t)
	selected := testSelectedVideoPromptContext()
	selected.Focus = &VideoPromptFocusTarget{ClipID: testClipID, GenerationMode: project.ClipGenerationReference, GeneratorID: string(project.GeneratorVideoWanOfficial), ProviderModelID: "wan3.0-video-prime"}
	turn := ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_focus_model", CodexTurnID: "turn_focus_model", VideoPromptTarget: selected}
	if err := server.ActivateTurn(testSessionID, turn); err != ErrInvalidBinding {
		t.Fatalf("focus target not matching trusted Focus was accepted: %v", err)
	}
	turn.Focus.ClipID = testClipID
	if err := server.ActivateTurn(testSessionID, turn); err != nil {
		t.Fatal(err)
	}
	selected.Focus.ProviderModelID = "mutated-model"
	read := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": testClipID.String()})
	if read.IsError || !strings.Contains(read.Text, "wan3.0-video-prime") || strings.Contains(read.Text, "mutated-model") || !strings.Contains(read.Text, "h3_base@1") {
		t.Fatalf("focused model selection was not independently captured: %+v", read)
	}
	if err := server.EndTurn(context.Background(), testSessionID, testTurnID); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryPromptRewriteStoresSelectedVariantAndPreservesFamiliesAndBindings(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	audioID := project.ArtifactID("art_" + strings.Repeat("A", 26))
	oldReferences := []videoPromptReference{{Key: "asset:" + testAssetID.String(), Purposes: []string{"identity"}, Note: "preserve appearance"}, {Key: "upload:" + audioID.String(), Purposes: []string{"voice"}, Note: "dialogue voice"}}
	h3 := videoPromptTarget{Family: "h3", Profile: "h3_ref2va@1", GeneratorID: "video.h3.shotforward", ProviderModelID: "h3-ref2va"}
	wan := videoPromptTarget{Family: "wan3", Profile: "wan3@1", GeneratorID: string(project.GeneratorVideoWanOfficial), ProviderModelID: "wan3.0-video-prime"}
	stored, err := parseVideoPromptSet("").withVariant(h3, h3.GeneratorID, h3.ProviderModelID, "old H3", oldReferences)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = parseVideoPromptSet(stored).withVariant(wan, wan.GeneratorID, wan.ProviderModelID, "independent Wan Prompt", oldReferences)
	if err != nil {
		t.Fatal(err)
	}
	queries.mutex.Lock()
	clip := queries.clips[testClipID]
	clip.Clip.VideoPrompts.Reference = stored
	clip.AssetReferences = []project.ClipAssetReference{{AssetID: testAssetID, Name: "Hero", Position: 1}}
	queries.clips[testClipID] = clip
	queries.clipReferenceMedia = map[project.ClipID][]project.Artifact{testClipID: {{ID: audioID, Kind: project.MediaKindAudio}}}
	queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{{Artifact: project.Artifact{ID: audioID, Kind: project.MediaKindAudio}, Name: "Hero voice"}}
	queries.mutex.Unlock()
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_ordinary_model", CodexTurnID: "turn_ordinary_model", VideoPromptTarget: testSelectedVideoPromptContext()}); err != nil {
		t.Fatal(err)
	}
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	read := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": testClipID.String()})
	if read.IsError || !strings.Contains(read.Text, "subject_definitions") {
		t.Fatalf("ordinary Prompt read omitted structured H3 rules: %+v", read)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "普通改写", "operation_key": "ordinary-model-begin"})
	if begin.IsError {
		t.Fatal(begin)
	}
	prompt := "subject_definitions:\n<Subject 1>: @Hero\nsummary:\nHe listens.\nretention_analysis:\nPreserve @Hero identity and @Hero voice.\ndetailed_description:\n<Subject 1> turns and replies.\noverall_soundscape:\n<d>[Chinese] 好的。</d>\nnon_diegetic_music:\nNone"
	appended := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "ordinary-model-append",
		"operations": []map[string]any{{"type": "update_clip_prompts", "clip_id": testClipID.String(), "video_prompt_mode": "reference", "video_prompt": prompt}},
	})
	if appended.IsError {
		t.Fatal(appended)
	}
	operation := drafts.lastAppend().Operations[0].ClipPrompts
	set := parseVideoPromptSet(operation.VideoAfter)
	if set.Families["h3"].Profile != "h3_ref2va@1" || set.Families["h3"].Prompt != prompt || !reflect.DeepEqual(set.Families["h3"].References, oldReferences) || !reflect.DeepEqual(set.Families["wan3"], parseVideoPromptSet(stored).Families["wan3"]) {
		t.Fatalf("ordinary rewrite lost variant metadata: %+v", set)
	}
	if !reflect.DeepEqual(operation.AssetIDsBefore, operation.AssetIDsAfter) || !reflect.DeepEqual(operation.ReferenceArtifactIDsBefore, operation.ReferenceArtifactIDsAfter) || len(operation.ReferenceArtifactIDsAfter) != 1 || operation.ReferenceArtifactIDsAfter[0] != audioID || operation.FirstBefore != operation.FirstAfter || operation.LastBefore != operation.LastAfter {
		t.Fatalf("ordinary rewrite changed unrelated binding or image Prompt: %+v", operation)
	}
	if queries.clips[testClipID].Clip.VideoPrompts.FirstLastImage != "old video" {
		t.Fatal("ordinary rewrite changed another mode")
	}
}

func TestVideoWriteRequiresModelButStillImageEditDoesNot(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, drafts := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_missing_model")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": testClipID.String()})
	callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "改写", "operation_key": "missing-model-begin"})
	arguments := map[string]any{"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "missing-model-append", "operations": []map[string]any{{"type": "update_clip_prompts", "clip_id": testClipID.String(), "video_prompt_mode": "reference", "video_prompt": "editable wording"}}}
	missing := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", arguments)
	if !missing.IsError || toolFailureCode(t, missing.Text) != "video_prompt_target_missing" || drafts.appendCount() != 0 {
		t.Fatalf("unprofiled video Prompt accepted: %+v", missing)
	}
	arguments["operations"] = []map[string]any{{"type": "update_clip_prompts", "clip_id": testClipID.String(), "first_image_prompt": "new still-image composition"}}
	imageEdit := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", arguments)
	if imageEdit.IsError || drafts.appendCount() != 1 {
		t.Fatalf("still-image edit was incorrectly blocked by video model context: %+v", imageEdit)
	}
}

func TestExplicitVideoTargetCannotOverrideCapturedSelection(t *testing.T) {
	t.Parallel()
	state := bindingSnapshot{turn: ActiveTurn{VideoPromptTarget: testSelectedVideoPromptContext()}}
	_, err := resolveVideoPromptWriteTarget(state, testClipID, project.ClipGenerationReference, string(project.GeneratorVideoWanOfficial), "wan3.0-video-prime", "")
	if err == nil {
		t.Fatal("tool arguments overrode the user's captured model selection")
	}
}
