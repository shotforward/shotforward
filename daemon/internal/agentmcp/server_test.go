package agentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentimage"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	testRoute = "route_ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
)

var (
	testSessionID = agent.SessionID("ags_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testTurnID    = agent.TurnID("agt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testProjectID = project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testEpisodeID = project.EpisodeID("ep_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testAssetID   = project.AssetID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testClipID    = project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
)

func TestServerRequiresLoopbackBearerAndExposesOnlyAllowedTools(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, _ := newMCPFixture(t)
	if credential.EnvironmentVariable != DefaultTokenEnvironmentVariable || len(credential.Value) < 43 || strings.Contains(endpoint, credential.Value) {
		t.Fatalf("credential or endpoint is unsafe: env=%q token_length=%d endpoint=%q", credential.EnvironmentVariable, len(credential.Value), endpoint)
	}
	wrongScope := BindingScope{
		RouteKey:  "route_ZYXWVUTSRQPONMLKJIHGFEDCBA987654",
		SessionID: agent.SessionID("ags_ZYXWVUTSRQPONMLKJIHGFEDCBA"),
		ProjectID: testProjectID,
		EpisodeID: project.EpisodeID("ep_ZYXWVUTSRQPONMLKJIHGFEDCBA"),
	}
	if _, err := server.RegisterBinding(context.Background(), wrongScope); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("wrong-scope binding error = %v", err)
	}

	status, _ := postRPC(t, server, endpoint, "", "initialize", map[string]any{"protocolVersion": mcpProtocolVersion}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", status)
	}
	wrongEndpoint := strings.TrimSuffix(endpoint, testRoute) + "missing_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	status, _ = postRPC(t, server, wrongEndpoint, credential.Value, "initialize", map[string]any{"protocolVersion": mcpProtocolVersion}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown route status = %d", status)
	}

	requestBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": mcpProtocolVersion}})
	request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential.Value)
	request.Header.Set("Origin", "http://127.0.0.1")
	request.RemoteAddr = "127.0.0.1:54321"
	request.Host = "127.0.0.1:43123"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("browser Origin status = %d", recorder.Code)
	}
	largeBody := `{"jsonrpc":"2.0","id":1,"method":"ping","padding":"` + strings.Repeat("x", MaxRequestBytes) + `"}`
	largeRequest := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(largeBody))
	largeRequest.RemoteAddr = "127.0.0.1:54321"
	largeRequest.Host = "127.0.0.1:43123"
	largeRequest.Header.Set("Content-Type", "application/json")
	largeRequest.Header.Set("Authorization", "Bearer "+credential.Value)
	largeRecorder := httptest.NewRecorder()
	server.ServeHTTP(largeRecorder, largeRequest)
	if largeRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d", largeRecorder.Code)
	}

	status, initialized := postRPC(t, server, endpoint, credential.Value, "initialize", map[string]any{"protocolVersion": mcpProtocolVersion}, "")
	if status != http.StatusOK || !strings.Contains(string(initialized), serverInstructions[:80]) {
		t.Fatalf("initialize = %d %s", status, initialized)
	}
	_, listed := postRPC(t, server, endpoint, credential.Value, "tools/list", map[string]any{}, "")
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed, &envelope); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range envelope.Result.Tools {
		names = append(names, tool.Name)
	}
	expected := EnabledToolNames()
	sort.Strings(names)
	sort.Strings(expected)
	if strings.Join(names, ",") != strings.Join(expected, ",") || strings.Contains(strings.Join(names, ","), "apply") || strings.Contains(strings.Join(names, ","), "generation") {
		t.Fatalf("tool names = %v", names)
	}

	session := callTool(t, server, endpoint, credential.Value, "get_session_context", map[string]any{})
	if session.IsError || !strings.Contains(session.Text, testSessionID.String()) || !strings.Contains(session.Text, testProjectID.String()) || !strings.Contains(session.Text, testEpisodeID.String()) {
		t.Fatalf("session context = %+v", session)
	}
	beforeTurn := callTool(t, server, endpoint, credential.Value, "get_episode_context", map[string]any{})
	if !beforeTurn.IsError || toolFailureCode(t, beforeTurn.Text) != "turn_not_active" {
		t.Fatalf("episode before Turn = %+v", beforeTurn)
	}
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_test", CodexTurnID: "turn_test", Focus: agent.FocusSnapshot{AssetID: testAssetID}}); err != nil {
		t.Fatal(err)
	}
	focused := callTool(t, server, endpoint, credential.Value, "get_session_context", map[string]any{})
	if focused.IsError || !strings.Contains(focused.Text, `"asset_id":"`+testAssetID.String()+`"`) {
		t.Fatalf("focused session context = %+v", focused)
	}
	afterTurn := callTool(t, server, endpoint, credential.Value, "get_episode_context", map[string]any{})
	if afterTurn.IsError || !strings.Contains(afterTurn.Text, `"script_revision":1`) {
		t.Fatalf("episode during Turn = %+v", afterTurn)
	}
	if err := server.EndTurn(context.Background(), testSessionID, testTurnID); err != nil {
		t.Fatal(err)
	}
	ended := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": testAssetID.String()})
	if !ended.IsError || toolFailureCode(t, ended.Text) != "turn_not_active" {
		t.Fatalf("read after EndTurn = %+v", ended)
	}
}

func TestProjectToolWaitsForPreparedTurnActivation(t *testing.T) {
	t.Parallel()
	server, _, _, _, _ := newMCPFixture(t)
	if err := server.PrepareTurn(testSessionID, testTurnID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		payload []byte
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		payload, err := server.callTool(context.Background(), testRoute, "get_asset", json.RawMessage(`{"asset_id":"`+testAssetID.String()+`"}`))
		completed <- result{payload: payload, err: err}
	}()

	select {
	case value := <-completed:
		t.Fatalf("tool call completed before activation: payload=%s error=%v", value.payload, value.err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_shotforward", CodexTurnID: "turn_fast", Focus: agent.FocusSnapshot{AssetID: testAssetID}}); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-completed:
		if value.err != nil || !strings.Contains(string(value.payload), `"asset_id":"`+testAssetID.String()+`"`) {
			t.Fatalf("tool call after activation: payload=%s error=%v", value.payload, value.err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool call did not resume after Turn activation")
	}
}

func TestVideoPromptAdaptationIncludesUploadedMediaDescriptionsAndPersistsPurposes(t *testing.T) {
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	now := queries.episode.CreatedAt
	imageID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	videoID := project.ArtifactID("art_ZYXWVUTSRQPONMLKJIHGFEDCBA")
	image := project.Artifact{ID: imageID, ProjectID: testProjectID, Kind: project.MediaKindImage, OriginalFilename: "composition.png"}
	video := project.Artifact{ID: videoID, ProjectID: testProjectID, Kind: project.MediaKindVideo, OriginalFilename: "walk.mp4"}
	queries.clipReferenceMedia = map[project.ClipID][]project.Artifact{testClipID: {image, video}}
	queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{
		{EpisodeID: testEpisodeID, Artifact: image, Name: "构图参考", Revision: 1, CreatedAt: now, UpdatedAt: now},
		{EpisodeID: testEpisodeID, Artifact: video, Name: "走路动作", Description: "只参考步伐和衣摆运动，不参考人物外观。", Revision: 2, CreatedAt: now, UpdatedAt: now},
	}
	clip := queries.clips[testClipID]
	clip.Clip.GenerationMode = project.ClipGenerationReference
	clip.Clip.VideoPrompts.Reference = "人物走入画面。"
	queries.clips[testClipID] = clip
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_test", CodexTurnID: "turn-upload-adaptation", Focus: agent.FocusSnapshot{ClipID: testClipID}}); err != nil {
		t.Fatal(err)
	}
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	contextResult := callTool(t, server, endpoint, credential.Value, "get_video_prompt_adaptation_context", map[string]any{
		"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
	})
	if contextResult.IsError || !strings.Contains(contextResult.Text, `"reference_key":"upload:`+imageID.String()+`"`) || !strings.Contains(contextResult.Text, `"model_label":"\u003cVideo 1\u003e"`) || !strings.Contains(contextResult.Text, "missing_description") || !strings.Contains(contextResult.Text, "只参考步伐") {
		t.Fatalf("adaptation context = %+v", contextResult)
	}
	var contextPayload struct {
		Source struct {
			GenerationMode string `json:"generation_mode"`
			Family         string `json:"prompt_family"`
			Hash           string `json:"prompt_hash"`
		} `json:"source"`
		ReferenceSetHash string `json:"reference_set_hash"`
	}
	if err := json.Unmarshal([]byte(contextResult.Text), &contextPayload); err != nil {
		t.Fatal(err)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "适配上传素材", "operation_key": "upload-adaptation-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	prompt := "subject_definitions:\n<Subject 1>: a person\nsummary:\nA walking shot using <Picture 1> composition and <Video 1> motion.\nretention_analysis:\n<Picture 1>: composition_only\n<Video 1>: motion_only\ndetailed_description:\nThe person follows <Video 1> without copying appearance.\noverall_soundscape:\nFootsteps\nnon_diegetic_music:\nNone"
	appendResult := callTool(t, server, endpoint, credential.Value, "append_video_prompt_adaptation", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "upload-adaptation-append",
		"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
		"prompt_family": "h3", "prompt_profile": "h3_ref2va@1", "source_generation_mode": contextPayload.Source.GenerationMode, "source_family": contextPayload.Source.Family,
		"source_prompt_hash": contextPayload.Source.Hash, "reference_set_hash": contextPayload.ReferenceSetHash, "prompt": prompt,
		"references": []map[string]any{
			{"key": "upload:" + imageID.String(), "purposes": []string{"composition"}, "note": "Use framing only"},
			{"key": "upload:" + videoID.String(), "purposes": []string{"motion"}, "note": "Do not copy appearance"},
		},
	})
	if appendResult.IsError {
		t.Fatalf("append = %+v", appendResult)
	}
	operation := drafts.lastAppend().Operations[0]
	if operation.ClipPrompts == nil || len(operation.ClipPrompts.ReferenceArtifactIDsBefore) != 2 || len(operation.ClipPrompts.ReferenceArtifactIDsAfter) != 2 {
		t.Fatalf("omitted reference_artifact_ids did not preserve bindings: %#v", operation)
	}
	stored := parseVideoPromptSet(operation.ClipPrompts.VideoAfter)
	references := stored.Families["h3"].References
	if len(references) != 2 || references[1].Key != "upload:"+videoID.String() || references[1].Purposes[0] != "motion" || references[1].Note != "Do not copy appearance" {
		t.Fatalf("stored references = %+v", references)
	}
}

func TestUploadedAudioCanBeListedAndBoundWithClipPrompt(t *testing.T) {
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	now := queries.episode.CreatedAt
	audioID := project.ArtifactID("art_" + strings.Repeat("U", 26))
	audio := project.Artifact{
		ID: audioID, ProjectID: testProjectID, Kind: project.MediaKindAudio,
		RelativePath: "media/audio/bubu.mp3", SHA256: strings.Repeat("a", 64), ByteSize: 128,
		MIMEType: "audio/mpeg", OriginalFilename: "bubu.mp3", DurationMilliseconds: 15_000,
		AudioSampleRate: 44_100, AudioChannels: 1, CreatedAt: now,
	}
	queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{{
		EpisodeID: testEpisodeID, Artifact: audio, Name: "布布配音", Description: "布布的对白音色参考。",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}
	clip := queries.clips[testClipID]
	clip.Clip.GenerationMode = project.ClipGenerationReference
	clip.Clip.VideoPrompts.Reference = "人物开口说话。"
	queries.clips[testClipID] = clip
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_test", CodexTurnID: "turn-audio-binding", Focus: agent.FocusSnapshot{ClipID: testClipID}}); err != nil {
		t.Fatal(err)
	}
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	listed := callTool(t, server, endpoint, credential.Value, "list_reference_media", map[string]any{"kind": "audio"})
	if listed.IsError || !strings.Contains(listed.Text, `"artifact_id":"`+audioID.String()+`"`) || !strings.Contains(listed.Text, `"name":"布布配音"`) {
		t.Fatalf("list_reference_media = %+v", listed)
	}
	read := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": testClipID.String(), "prompt_fields": []string{"reference_video_prompt"}})
	if read.IsError {
		t.Fatalf("get_clip = %+v", read)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "绑定布布配音", "operation_key": "audio-bind-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "audio-bind-append",
		"operations": []map[string]any{{
			"type": "update_clip_prompts", "clip_id": testClipID.String(), "video_prompt_mode": "reference",
			"video_prompt": "@布布配音 作为人物对白音色参考。", "reference_artifact_ids": []string{audioID.String()},
			"generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
		}},
	})
	if appended.IsError {
		t.Fatalf("append = %+v", appended)
	}
	operation := drafts.lastAppend().Operations[0]
	if operation.ClipPrompts == nil || !operation.ClipPrompts.ReferenceBindingsRecorded || len(operation.ClipPrompts.ReferenceArtifactIDsAfter) != 1 || operation.ClipPrompts.ReferenceArtifactIDsAfter[0] != audioID || !strings.Contains(operation.ClipPrompts.VideoAfter, "@布布配音") {
		t.Fatalf("audio Clip operation = %#v", operation)
	}
}

func TestVideoPromptAdaptationCanChangeUploadedAudioBindings(t *testing.T) {
	tests := []struct {
		name           string
		currentlyBound bool
		desiredIDs     []string
		references     []map[string]any
		wantBefore     int
		wantAfter      int
	}{
		{
			name: "bind explicitly mentioned audio", desiredIDs: []string{"art_UUUUUUUUUUUUUUUUUUUUUUUUUU"},
			references: []map[string]any{{"key": "upload:art_UUUUUUUUUUUUUUUUUUUUUUUUUU", "purposes": []string{"voice"}, "note": "Use as the character dialogue voice"}},
			wantAfter:  1,
		},
		{name: "unbind explicitly removed audio", currentlyBound: true, desiredIDs: []string{}, references: []map[string]any{}, wantBefore: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, endpoint, credential, queries, drafts := newMCPFixture(t)
			now := queries.episode.CreatedAt
			audioID := project.ArtifactID("art_" + strings.Repeat("U", 26))
			audio := project.Artifact{
				ID: audioID, ProjectID: testProjectID, Kind: project.MediaKindAudio,
				RelativePath: "media/audio/bubu.mp3", SHA256: strings.Repeat("a", 64), ByteSize: 128,
				MIMEType: "audio/mpeg", OriginalFilename: "bubu.mp3", DurationMilliseconds: 15_000,
				AudioSampleRate: 44_100, AudioChannels: 1, CreatedAt: now,
			}
			queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{{
				EpisodeID: testEpisodeID, Artifact: audio, Name: "布布配音", Description: "布布的对白音色参考。",
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			}}
			if test.currentlyBound {
				queries.clipReferenceMedia = map[project.ClipID][]project.Artifact{testClipID: {audio}}
			}
			clip := queries.clips[testClipID]
			clip.Clip.GenerationMode = project.ClipGenerationReference
			clip.Clip.VideoPrompts.Reference = "人物开口说话。"
			queries.clips[testClipID] = clip
			if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_test", CodexTurnID: "turn-adaptation-audio-bindings", Focus: agent.FocusSnapshot{ClipID: testClipID}}); err != nil {
				t.Fatal(err)
			}
			declareProjectChange(t, server, endpoint, credential.Value, "prompts")
			listed := callTool(t, server, endpoint, credential.Value, "list_reference_media", map[string]any{"kind": "audio"})
			if listed.IsError || !strings.Contains(listed.Text, audioID.String()) {
				t.Fatalf("list_reference_media = %+v", listed)
			}
			contextResult := callTool(t, server, endpoint, credential.Value, "get_video_prompt_adaptation_context", map[string]any{
				"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
			})
			if contextResult.IsError {
				t.Fatalf("adaptation context = %+v", contextResult)
			}
			var contextPayload struct {
				Source struct {
					GenerationMode string `json:"generation_mode"`
					Family         string `json:"prompt_family"`
					Hash           string `json:"prompt_hash"`
				} `json:"source"`
				ReferenceSetHash string `json:"reference_set_hash"`
			}
			if err := json.Unmarshal([]byte(contextResult.Text), &contextPayload); err != nil {
				t.Fatal(err)
			}
			begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "调整配音素材", "operation_key": "adaptation-audio-bindings-begin"})
			if begin.IsError {
				t.Fatalf("begin = %+v", begin)
			}
			prompt := "subject_definitions:\n<Subject 1>: a person\nsummary:\nThe person speaks.\nretention_analysis:\nPreserve identity.\ndetailed_description:\nThe person delivers the line clearly.\noverall_soundscape:\nQuiet ambience.\nnon_diegetic_music:\nNone"
			if test.wantAfter == 1 {
				prompt = strings.Replace(prompt, "delivers the line clearly", "delivers the line using @布布配音", 1)
			}
			appended := callTool(t, server, endpoint, credential.Value, "append_video_prompt_adaptation", map[string]any{
				"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "adaptation-audio-bindings-append",
				"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
				"prompt_family": "h3", "prompt_profile": "h3_ref2va@1", "source_generation_mode": contextPayload.Source.GenerationMode, "source_family": contextPayload.Source.Family,
				"source_prompt_hash": contextPayload.Source.Hash, "reference_set_hash": contextPayload.ReferenceSetHash, "prompt": prompt,
				"reference_artifact_ids": test.desiredIDs, "references": test.references,
			})
			if appended.IsError {
				t.Fatalf("append = %+v", appended)
			}
			operation := drafts.lastAppend().Operations[0]
			if operation.ClipPrompts == nil || len(operation.ClipPrompts.ReferenceArtifactIDsBefore) != test.wantBefore || len(operation.ClipPrompts.ReferenceArtifactIDsAfter) != test.wantAfter {
				t.Fatalf("adaptation audio bindings = %#v", operation)
			}
			stored := parseVideoPromptSet(operation.ClipPrompts.VideoAfter)
			if len(stored.Families["h3"].References) != test.wantAfter {
				t.Fatalf("stored adaptation references = %+v", stored.Families["h3"].References)
			}
		})
	}
}

func TestGenerateConversationImageUsesActiveTurnRuntimeAndScope(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, _ := newMCPFixture(t)
	images := &recordingImageCommands{result: agentimage.Result{ArtifactID: project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), GeneratorID: project.GeneratorImageShotForward, ReferenceCount: 1}}
	server.images = images
	referenceID := project.ArtifactID("art_BCDEFGHIJKLMNOPQRSTUVWXYZA")
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_image", CodexTurnID: "turn_image", RuntimeKind: agentapp.RuntimeShotForward, ArtifactIDs: []project.ArtifactID{referenceID}}); err != nil {
		t.Fatal(err)
	}
	result := callTool(t, server, endpoint, credential.Value, "generate_conversation_image", map[string]any{"prompt": "Keep the attached character and place it on a white background."})
	if result.IsError || !strings.Contains(result.Text, `"status":"completed"`) || !strings.Contains(result.Text, `"reference_count":1`) {
		t.Fatalf("generation result = %+v", result)
	}
	if images.input.ProjectID != testProjectID || images.input.EpisodeID != testEpisodeID || images.input.SessionID != testSessionID || images.input.TurnID != testTurnID || images.input.RuntimeKind != agentapp.RuntimeShotForward || len(images.input.ArtifactIDs) != 1 || images.input.ArtifactIDs[0] != referenceID {
		t.Fatalf("generation input = %#v", images.input)
	}
}

type recordingImageCommands struct {
	input  agentimage.Input
	result agentimage.Result
	err    error
}

func (r *recordingImageCommands) Generate(_ context.Context, input agentimage.Input) (agentimage.Result, error) {
	r.input = input
	return r.result, r.err
}

func TestStoryboardCursorIsBoundToRevision(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, _ := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn-storyboard-cursor")
	now := queries.episode.CreatedAt
	queries.mutex.Lock()
	queries.storyboard = project.Storyboard{EpisodeID: testEpisodeID, Revision: 1, Scenes: []project.StoryboardScene{
		{Scene: project.Scene{ID: project.SceneID("scn_AAAAAAAAAAAAAAAAAAAAAAAAAA"), ProjectID: testProjectID, EpisodeID: testEpisodeID, Position: 1, Title: "A", Revision: 1, CreatedAt: now, UpdatedAt: now}},
		{Scene: project.Scene{ID: project.SceneID("scn_BBBBBBBBBBBBBBBBBBBBBBBBBB"), ProjectID: testProjectID, EpisodeID: testEpisodeID, Position: 2, Title: "B", Revision: 1, CreatedAt: now, UpdatedAt: now}},
	}}
	queries.mutex.Unlock()
	first := callTool(t, server, endpoint, credential.Value, "list_scenes", map[string]any{"limit": 1})
	if first.IsError {
		t.Fatalf("first list_scenes = %+v", first)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(first.Text), &page); err != nil || page.NextCursor == "" {
		t.Fatalf("first page = %q, %v", first.Text, err)
	}
	queries.mutex.Lock()
	queries.storyboard.Revision = 2
	queries.mutex.Unlock()
	stale := callTool(t, server, endpoint, credential.Value, "list_scenes", map[string]any{"limit": 1, "cursor": page.NextCursor})
	if !stale.IsError || toolFailureCode(t, stale.Text) != "cursor_stale" {
		t.Fatalf("stale Storyboard cursor = %+v", stale)
	}
}

func TestEnsureBindingIsIdempotentOnlyForExactScope(t *testing.T) {
	t.Parallel()
	server, first, credential, _, _ := newMCPFixture(t)
	scope := BindingScope{RouteKey: testRoute, SessionID: testSessionID, ProjectID: testProjectID, EpisodeID: testEpisodeID}
	if !server.BindingRegistered(scope) {
		t.Fatal("registered scope was not reported live")
	}
	second, err := server.EnsureBinding(context.Background(), scope)
	if err != nil || second != first {
		t.Fatalf("second EnsureBinding() = %q, %v, want %q", second, err, first)
	}
	changed := scope
	changed.RouteKey = "route_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	if server.BindingRegistered(changed) {
		t.Fatal("changed scope was reported live")
	}
	if _, err := server.EnsureBinding(context.Background(), changed); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("changed EnsureBinding() error = %v", err)
	}
	status, bridged := postRPC(t, server, first, credential.Value, "tools/call", map[string]any{
		"tool": "get_episode_context", "arguments": map[string]any{}, "_meta": map[string]any{"requestId": "bridge-test"},
	}, "")
	if status != http.StatusOK || !strings.Contains(string(bridged), "turn_not_active") {
		t.Fatalf("bridged tools/call = %d %s", status, bridged)
	}
	status, conflicting := postRPC(t, server, first, credential.Value, "tools/call", map[string]any{
		"name": "get_asset", "tool": "get_clip", "arguments": map[string]any{},
	}, "")
	if status != http.StatusOK || !strings.Contains(string(conflicting), `"code":-32602`) {
		t.Fatalf("conflicting tools/call = %d %s", status, conflicting)
	}
}

func TestTurnReadSetBuildsTrustedPromptOperationsAndIsDestroyed(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_one")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")

	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "优化 Prompt", "summary": "待审阅", "operation_key": "begin-1"})
	if begin.IsError || !strings.Contains(begin.Text, `"status":"draft"`) {
		t.Fatalf("begin = %+v", begin)
	}
	foreign := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ZYXWVUTSRQPONMLKJIHGFEDCBA", "expected_change_set_revision": 1, "operation_key": "append-foreign",
		"operations": []map[string]any{{"type": "update_asset_prompt", "asset_id": testAssetID.String(), "new_prompt": "foreign"}},
	})
	if !foreign.IsError || toolFailureCode(t, foreign.Text) != "change_set_outside_turn" {
		t.Fatalf("foreign ChangeSet append = %+v", foreign)
	}
	withoutRead := appendAssetPrompt(t, server, endpoint, credential.Value, "new prompt", "append-unobserved")
	if !withoutRead.IsError || toolFailureCode(t, withoutRead.Text) != "object_not_observed" || drafts.appendCount() != 0 {
		t.Fatalf("append without read = %+v, calls=%d", withoutRead, drafts.appendCount())
	}

	read := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": testAssetID.String()})
	if read.IsError || !strings.Contains(read.Text, `"prompt":"old prompt"`) {
		t.Fatalf("get Asset = %+v", read)
	}
	queries.updateAsset("manual prompt", 2)
	stale := appendAssetPrompt(t, server, endpoint, credential.Value, "new prompt", "append-stale")
	if !stale.IsError || toolFailureCode(t, stale.Text) != "observation_stale" || drafts.appendCount() != 0 {
		t.Fatalf("stale append = %+v, calls=%d", stale, drafts.appendCount())
	}

	read = callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": testAssetID.String()})
	if read.IsError {
		t.Fatalf("refresh Asset = %+v", read)
	}
	appended := appendAssetPrompt(t, server, endpoint, credential.Value, "new prompt", "append-1")
	if appended.IsError || !strings.Contains(appended.Text, `"operation_count":1`) {
		t.Fatalf("append = %+v", appended)
	}
	input := drafts.lastAppend()
	operation := input.Operations[0]
	if operation.ExpectedRevision != 2 || operation.AssetPrompt == nil || operation.AssetPrompt.Before != "manual prompt" || operation.AssetPrompt.After != "new prompt" {
		t.Fatalf("trusted operation = %#v", operation)
	}

	clipRead := callTool(t, server, endpoint, credential.Value, "get_clip", map[string]any{"clip_id": testClipID.String(), "prompt_fields": []string{"first_last_image_video_prompt"}})
	if clipRead.IsError || strings.Contains(clipRead.Text, "first_image_prompt") {
		t.Fatalf("partial Clip read = %+v", clipRead)
	}
	unseenField := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 2, "operation_key": "append-clip-bad",
		"operations": []map[string]any{{"type": "update_clip_prompts", "clip_id": testClipID.String(), "first_image_prompt": "new first"}},
	})
	if !unseenField.IsError || toolFailureCode(t, unseenField.Text) != "field_not_observed" {
		t.Fatalf("unseen Clip field = %+v", unseenField)
	}
	clipAppend := callTool(t, server, endpoint, credential.Value, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 2, "operation_key": "append-clip",
		"operations": []map[string]any{{"type": "update_clip_prompts", "clip_id": testClipID.String(), "video_prompt_mode": "first_last_image", "video_prompt": "new video", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-fl2va"}},
	})
	if clipAppend.IsError {
		t.Fatalf("Clip append = %+v", clipAppend)
	}
	clipOperation := drafts.lastAppend().Operations[0]
	if clipOperation.ClipPrompts == nil || clipOperation.ClipPrompts.VideoMode != project.ClipGenerationFirstLastImage || clipOperation.ClipPrompts.VideoBefore != "old video" || parseVideoPromptSet(clipOperation.ClipPrompts.VideoAfter).Families["h3"].Prompt != "new video" || clipOperation.ClipPrompts.FirstBefore != "old first" || clipOperation.ClipPrompts.FirstAfter != "old first" {
		t.Fatalf("trusted Clip operation = %#v", clipOperation)
	}

	if err := server.EndTurn(context.Background(), testSessionID, testTurnID); err != nil {
		t.Fatal(err)
	}
	if drafts.discardCount() != 1 {
		t.Fatalf("discarded drafts = %d, want 1", drafts.discardCount())
	}
	secondTurn := agent.TurnID("agt_ZYXWVUTSRQPONMLKJIHGFEDCBA")
	activateTestTurn(t, server, secondTurn, "turn_two")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	secondBegin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Second draft", "operation_key": "begin-2"})
	if secondBegin.IsError {
		t.Fatalf("second begin = %+v", secondBegin)
	}
	destroyed := appendAssetPrompt(t, server, endpoint, credential.Value, "another prompt", "append-after-turn")
	if !destroyed.IsError || toolFailureCode(t, destroyed.Text) != "object_not_observed" {
		t.Fatalf("ReadSet survived Turn = %+v", destroyed)
	}
}

func TestChangeSetRequiresDeclaredProjectChangeIntent(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, _ := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_intent")
	before := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Should fail", "operation_key": "intent-before"})
	if !before.IsError || toolFailureCode(t, before.Text) != "intent_required" {
		t.Fatalf("begin before intent = %+v", before)
	}
	declareProjectChange(t, server, endpoint, credential.Value, "script")
	after := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Allowed", "operation_key": "intent-after"})
	if after.IsError {
		t.Fatalf("begin after intent = %+v", after)
	}
}

func TestAssetExtractionRequiresCompleteScriptAndAssetList(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, drafts := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_extract")
	declareProjectChange(t, server, endpoint, credential.Value, "assets")
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "提取资产", "operation_key": "extract-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	arguments := map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "extract-append",
		"operations": []map[string]any{{"kind": "location", "name": "雨夜天台", "prompt": "潮湿反光的城市天台"}},
	}
	beforeRead := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if !beforeRead.IsError || toolFailureCode(t, beforeRead.Text) != "script_not_observed" {
		t.Fatalf("append before reads = %+v", beforeRead)
	}
	if result := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{}); result.IsError {
		t.Fatalf("read_script = %+v", result)
	}
	if result := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{}); result.IsError {
		t.Fatalf("list_assets = %+v", result)
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("append after reads = %+v, count=%d", appended, drafts.appendCount())
	}
	draft := drafts.lastAppend().Operations[0]
	if draft.Type != changeset.CreateEpisodeAsset || draft.ExpectedRevision != 1 || draft.AssetCreation == nil || draft.AssetCreation.Kind != project.AssetKindLocation || draft.AssetCreation.Name != "雨夜天台" {
		t.Fatalf("Asset creation draft = %+v", draft)
	}
}

func TestAssetExtractionRequiresEveryCurrentScriptPage(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	queries.mutex.Lock()
	queries.episode.Script.Markdown = strings.Repeat("a", 7_000) + "\n" + strings.Repeat("b", 7_000)
	queries.mutex.Unlock()
	activateTestTurn(t, server, testTurnID, "turn_script_pages")
	declareProjectChange(t, server, endpoint, credential.Value, "assets")
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "分页提取", "operation_key": "paged-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	first := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{})
	var page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if first.IsError || json.Unmarshal([]byte(first.Text), &page) != nil || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first script page = %+v", first)
	}
	if result := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{}); result.IsError {
		t.Fatalf("list_assets = %+v", result)
	}
	arguments := map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "paged-append",
		"operations": []map[string]any{{"kind": "prop", "name": "长剧本道具", "prompt": "一件关键道具"}},
	}
	partial := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if !partial.IsError || toolFailureCode(t, partial.Text) != "script_not_observed" {
		t.Fatalf("append after partial script = %+v", partial)
	}

	queries.mutex.Lock()
	queries.episode.Script.Markdown += "\nchanged"
	queries.episode.Script.Revision++
	queries.mutex.Unlock()
	stale := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{"cursor": page.NextCursor})
	if !stale.IsError || toolFailureCode(t, stale.Text) != "cursor_stale" {
		t.Fatalf("stale script page = %+v", stale)
	}
	first = callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{})
	if first.IsError || json.Unmarshal([]byte(first.Text), &page) != nil || page.NextCursor == "" {
		t.Fatalf("restarted script page = %+v", first)
	}
	second := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{"cursor": page.NextCursor})
	if second.IsError || json.Unmarshal([]byte(second.Text), &page) != nil || page.HasMore {
		t.Fatalf("final script page = %+v", second)
	}
	arguments["expected_change_set_revision"] = 1
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if appended.IsError || drafts.appendCount() != 1 || drafts.lastAppend().Operations[0].ExpectedRevision != 2 {
		t.Fatalf("append after complete current script = %+v, count=%d", appended, drafts.appendCount())
	}
}

func TestAssetExtractionCanDeriveObservedPriorCandidate(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	queries.mutex.Lock()
	queries.episode.SequenceNumber = 2
	queries.mutex.Unlock()
	priorID := project.AssetID("ast_PPPPPPPPPPPPPPPPPPPPPPPPPP")
	priorEpisodeID := project.EpisodeID("ep_PPPPPPPPPPPPPPPPPPPPPPPPPP")
	queries.addPriorAsset(priorID, priorEpisodeID, "林晚", "第一集黑色风衣", queries.episode.CreatedAt)
	activateTestTurn(t, server, testTurnID, "turn_derive")
	declareProjectChange(t, server, endpoint, credential.Value, "assets")
	if result := callTool(t, server, endpoint, credential.Value, "read_script", map[string]any{}); result.IsError {
		t.Fatalf("read_script = %+v", result)
	}
	if result := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{}); result.IsError {
		t.Fatalf("list_assets = %+v", result)
	}
	listed := callTool(t, server, endpoint, credential.Value, "list_prior_asset_candidates", map[string]any{"kind": "character"})
	if listed.IsError || !strings.Contains(listed.Text, priorID.String()) {
		t.Fatalf("prior candidates = %+v", listed)
	}
	observed := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()})
	if observed.IsError || !strings.Contains(observed.Text, "第一集黑色风衣") {
		t.Fatalf("prior candidate = %+v", observed)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "延续资产", "operation_key": "derive-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "derive-append",
		"operations": []map[string]any{{"source_asset_id": priorID.String(), "prompt": "第二集灰色大衣"}},
	})
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("append derived Asset = %+v", appended)
	}
	creation := drafts.lastAppend().Operations[0].AssetCreation
	if creation == nil || creation.SourceAssetID != priorID || creation.IdentityID != queries.prior[priorID].Asset.Identity.ID || creation.Name != "林晚" || creation.Prompt != "第二集灰色大衣" {
		t.Fatalf("derived creation draft = %+v", creation)
	}
}

func TestAssetCursorBecomesStaleAndOversizedResultIsNotObserved(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_cursor")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Cursor draft", "operation_key": "begin-cursor"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	queries.addAsset(project.AssetID("ast_ZYXWVUTSRQPONMLKJIHGFEDCBA"), "Second", "second prompt")

	first := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{"limit": 1})
	if first.IsError {
		t.Fatalf("first page = %+v", first)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(first.Text), &page); err != nil || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page payload = %s, %v", first.Text, err)
	}
	queries.updateAsset("changed", 2)
	stale := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{"limit": 1, "cursor": page.NextCursor})
	if !stale.IsError || toolFailureCode(t, stale.Text) != "cursor_stale" {
		t.Fatalf("stale page = %+v", stale)
	}

	queries.updateAsset(strings.Repeat("界", 25_000), 3)
	tooLarge := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": testAssetID.String()})
	if !tooLarge.IsError || toolFailureCode(t, tooLarge.Text) != "result_too_large" {
		t.Fatalf("oversized result = %+v", tooLarge)
	}
	appendResult := appendAssetPrompt(t, server, endpoint, credential.Value, "small", "append-large")
	if !appendResult.IsError || toolFailureCode(t, appendResult.Text) != "object_not_observed" || drafts.appendCount() != 0 {
		t.Fatalf("oversized observation leaked = %+v, calls=%d", appendResult, drafts.appendCount())
	}
}

func TestUnregisterBindingWaitsForTurnAndDiscardsDraft(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, drafts := newMCPFixture(t)
	activateTestTurn(t, server, testTurnID, "turn_unregister")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Unregistered draft", "operation_key": "begin-unregister"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	if err := server.UnregisterBinding(context.Background(), testSessionID); err != nil {
		t.Fatalf("UnregisterBinding() = %v", err)
	}
	if drafts.discardCount() != 1 {
		t.Fatalf("discarded drafts = %d, want 1", drafts.discardCount())
	}
	status, _ := postRPC(t, server, endpoint, credential.Value, "ping", map[string]any{}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unregistered route status = %d", status)
	}
}

type toolResultView struct {
	IsError bool
	Text    string
}

func callTool(t *testing.T, server *Server, endpoint, token, name string, arguments any) toolResultView {
	t.Helper()
	status, raw := postRPC(t, server, endpoint, token, "tools/call", map[string]any{"name": name, "arguments": arguments}, "")
	if status != http.StatusOK {
		t.Fatalf("tools/call %s status = %d", name, status)
	}
	var envelope struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Result.Content) != 1 {
		t.Fatalf("decode tools/call %s: %s, %v", name, raw, err)
	}
	return toolResultView{IsError: envelope.Result.IsError, Text: envelope.Result.Content[0].Text}
}

func postRPC(t *testing.T, server *Server, endpoint, token, method string, params any, origin string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.RemoteAddr = "127.0.0.1:54321"
	request.Host = "127.0.0.1:43123"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}

func toolFailureCode(t *testing.T, text string) string {
	t.Helper()
	var failure toolFailure
	if err := json.Unmarshal([]byte(text), &failure); err != nil {
		t.Fatalf("decode tool failure %q: %v", text, err)
	}
	return failure.Code
}

func appendAssetPrompt(t *testing.T, server *Server, endpoint, token, prompt, operationKey string) toolResultView {
	t.Helper()
	return callTool(t, server, endpoint, token, "append_prompt_operations", map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": operationKey,
		"operations": []map[string]any{{"type": "update_asset_prompt", "asset_id": testAssetID.String(), "new_prompt": prompt}},
	})
}

func activateTestTurn(t *testing.T, server *Server, turnID agent.TurnID, codexTurnID string) {
	t.Helper()
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: turnID, CodexThreadID: "thr_test", CodexTurnID: codexTurnID}); err != nil {
		t.Fatal(err)
	}
}

func newMCPFixture(t *testing.T) (*Server, string, Credential, *fakeProjectQueries, *fakeDraftCommands) {
	t.Helper()
	now := time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC)
	queries := &fakeProjectQueries{
		episode:    project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: testEpisodeID, ProjectID: testProjectID, SequenceNumber: 1, Title: "Episode", TargetDuration: time.Minute, Revision: 1, CreatedAt: now, UpdatedAt: now}, Script: project.Script{Revision: 1, UpdatedAt: now}},
		assets:     make(map[project.AssetID]project.EpisodeAsset),
		prior:      make(map[project.AssetID]projectapp.PriorAssetCandidate),
		episodes:   make(map[project.EpisodeID]project.Episode),
		clips:      make(map[project.ClipID]project.ClipDetails),
		storyboard: project.Storyboard{EpisodeID: testEpisodeID, Revision: 1},
	}
	queries.addAsset(testAssetID, "Hero", "old prompt")
	queries.clips[testClipID] = project.ClipDetails{Clip: project.Clip{ID: testClipID, ProjectID: testProjectID, EpisodeID: testEpisodeID, SceneID: project.SceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), Position: 1, Title: "Clip", PlannedDurationSeconds: 5, FirstImagePrompt: "old first", LastImagePrompt: "old last", VideoPrompts: project.ClipVideoPrompts{FirstLastImage: "old video"}, GenerationMode: project.ClipGenerationFirstLastImage, Revision: 1, CreatedAt: now, UpdatedAt: now}}
	drafts := &fakeDraftCommands{}
	randomBytes := bytes.Repeat([]byte{0x5a}, 512)
	server, err := NewServer(Options{Queries: queries, Drafts: drafts, DraftCleanup: drafts, Random: bytes.NewReader(randomBytes)})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43123")}
	endpoint, err := server.RegisterBinding(context.Background(), BindingScope{RouteKey: testRoute, SessionID: testSessionID, ProjectID: testProjectID, EpisodeID: testEpisodeID})
	if err != nil {
		t.Fatal(err)
	}
	return server, endpoint, server.Credential(), queries, drafts
}

type fakeProjectQueries struct {
	mutex                 sync.RWMutex
	episode               project.Episode
	assets                map[project.AssetID]project.EpisodeAsset
	prior                 map[project.AssetID]projectapp.PriorAssetCandidate
	episodes              map[project.EpisodeID]project.Episode
	clips                 map[project.ClipID]project.ClipDetails
	clipReferenceMedia    map[project.ClipID][]project.Artifact
	episodeReferenceMedia []project.EpisodeReferenceMedia
	storyboard            project.Storyboard
}

func (q *fakeProjectQueries) GetEpisode(_ context.Context, input projectapp.GetEpisodeInput) (project.Episode, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	if input.EpisodeID != q.episode.ID {
		value, ok := q.episodes[input.EpisodeID]
		if !ok {
			return project.Episode{}, projectapp.ErrEpisodeNotFound
		}
		return value, nil
	}
	return q.episode, nil
}

func (q *fakeProjectQueries) ListEpisodeAssets(context.Context, projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	result := make([]project.EpisodeAsset, 0, len(q.assets))
	for _, value := range q.assets {
		result = append(result, value)
	}
	return result, nil
}

func (q *fakeProjectQueries) FindPriorAssetCandidates(context.Context, projectapp.FindPriorAssetCandidatesInput) ([]projectapp.PriorAssetCandidate, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	result := make([]projectapp.PriorAssetCandidate, 0, len(q.prior))
	for _, value := range q.prior {
		result = append(result, value)
	}
	return result, nil
}

func (q *fakeProjectQueries) GetAsset(_ context.Context, input projectapp.GetAssetInput) (project.EpisodeAsset, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	value, ok := q.assets[input.AssetID]
	if !ok {
		candidate, prior := q.prior[input.AssetID]
		value, ok = candidate.Asset, prior
	}
	if !ok {
		return project.EpisodeAsset{}, projectapp.ErrAssetNotFound
	}
	return value, nil
}

func (q *fakeProjectQueries) addPriorAsset(id project.AssetID, episodeID project.EpisodeID, name, prompt string, at time.Time) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	identityID := project.AssetIdentityID("asi_" + strings.TrimPrefix(id.String(), "ast_"))
	episode := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: episodeID, ProjectID: testProjectID, SequenceNumber: 1, Title: "Prior", TargetDuration: time.Minute, Revision: 1, CreatedAt: at, UpdatedAt: at}, Script: project.Script{Revision: 1, UpdatedAt: at}}
	value := project.EpisodeAsset{Identity: project.AssetIdentity{ID: identityID, ProjectID: testProjectID, Kind: project.AssetKindCharacter, Name: name, Revision: 1, CreatedAt: at, UpdatedAt: at}, Asset: project.Asset{ID: id, ProjectID: testProjectID, EpisodeID: episodeID, IdentityID: identityID, Prompt: prompt, Revision: 1, CreatedAt: at, UpdatedAt: at}}
	q.episodes[episodeID] = episode
	q.prior[id] = projectapp.PriorAssetCandidate{Asset: value, EpisodeSequence: 1}
}

func (q *fakeProjectQueries) GetClip(_ context.Context, input projectapp.GetClipInput) (project.ClipDetails, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	value, ok := q.clips[input.ClipID]
	if !ok {
		return project.ClipDetails{}, projectapp.ErrClipNotFound
	}
	return value, nil
}

func (q *fakeProjectQueries) GetStoryboard(_ context.Context, input projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	if input.ProjectID != q.episode.ProjectID || input.EpisodeID != q.episode.ID {
		return projectapp.StoryboardResult{}, projectapp.ErrEpisodeNotFound
	}
	return projectapp.StoryboardResult{Storyboard: q.storyboard, TargetDuration: q.episode.TargetDuration}, nil
}

func (q *fakeProjectQueries) GetMediaSlot(_ context.Context, input projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	for _, details := range q.clips {
		for _, kind := range []project.MediaSlotKind{project.MediaSlotClipFirstImage, project.MediaSlotClipLastImage, project.MediaSlotClipVideo, project.MediaSlotAudioPrimary} {
			id, _ := project.FixedMediaSlotID(details.Clip.ID.String(), kind)
			if id == input.MediaSlotID {
				now := details.Clip.CreatedAt
				return project.MediaSlotDetails{Slot: project.MediaSlot{ID: id, ProjectID: details.Clip.ProjectID, EpisodeID: details.Clip.EpisodeID, OwnerKind: project.MediaOwnerClip, OwnerID: details.Clip.ID.String(), Kind: kind, Revision: 1, CreatedAt: now, UpdatedAt: now}, Artifacts: map[project.ArtifactID]project.Artifact{}}, nil
			}
		}
	}
	return project.MediaSlotDetails{}, projectapp.ErrMediaSlotNotFound
}

func (q *fakeProjectQueries) ListClipReferenceMedia(_ context.Context, input projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return append([]project.Artifact(nil), q.clipReferenceMedia[input.ClipID]...), nil
}

func (q *fakeProjectQueries) ListEpisodeReferenceMedia(context.Context, projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return append([]project.EpisodeReferenceMedia(nil), q.episodeReferenceMedia...), nil
}

func (q *fakeProjectQueries) addAsset(id project.AssetID, name, prompt string) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	now := q.episode.CreatedAt
	identityID := project.AssetIdentityID("asi_" + strings.TrimPrefix(id.String(), "ast_"))
	q.assets[id] = project.EpisodeAsset{
		Identity: project.AssetIdentity{ID: identityID, ProjectID: testProjectID, Kind: project.AssetKindCharacter, Name: name, Revision: 1, CreatedAt: now, UpdatedAt: now},
		Asset:    project.Asset{ID: id, ProjectID: testProjectID, EpisodeID: testEpisodeID, IdentityID: identityID, Prompt: prompt, Revision: 1, CreatedAt: now, UpdatedAt: now},
	}
}

func (q *fakeProjectQueries) updateAsset(prompt string, revision int64) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	value := q.assets[testAssetID]
	value.Asset.Prompt = prompt
	value.Asset.Revision = revision
	value.Asset.UpdatedAt = value.Asset.UpdatedAt.Add(time.Duration(revision) * time.Second)
	q.assets[testAssetID] = value
}

type fakeDraftCommands struct {
	mutex    sync.Mutex
	appends  []changesetapp.AppendOperationsInput
	discards []changeset.ChangeSetID
}

func (f *fakeDraftCommands) BeginDraft(_ context.Context, input changesetapp.BeginDraftInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{ChangeSet: changeset.ChangeSet{ID: changeset.ChangeSetID("cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, Status: changeset.StatusDraft, Revision: 1}}, nil
}

func (f *fakeDraftCommands) AppendOperations(_ context.Context, input changesetapp.AppendOperationsInput) (changesetapp.CommandResult, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.appends = append(f.appends, input)
	return changesetapp.CommandResult{ChangeSet: changeset.ChangeSet{ID: input.ChangeSetID, ProjectID: input.ProjectID, Status: changeset.StatusDraft, OperationCount: len(input.Operations), Revision: input.ExpectedRevision + 1}}, nil
}

func (f *fakeDraftCommands) Finalize(_ context.Context, input changesetapp.FinalizeInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{ChangeSet: changeset.ChangeSet{ID: input.ChangeSetID, ProjectID: input.ProjectID, Status: changeset.StatusPending, OperationCount: 1, Revision: input.ExpectedRevision + 1}}, nil
}

func (f *fakeDraftCommands) Apply(_ context.Context, input changesetapp.ApplyInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{ChangeSet: changeset.ChangeSet{ID: input.ChangeSetID, ProjectID: input.ProjectID, Status: changeset.StatusApplied, OperationCount: 1, Revision: input.ExpectedRevision + 1}}, nil
}

func (f *fakeDraftCommands) DiscardDraft(_ context.Context, _ project.ProjectID, id changeset.ChangeSetID) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.discards = append(f.discards, id)
	return nil
}

func (f *fakeDraftCommands) DiscardTurnDrafts(context.Context, project.ProjectID, agent.TurnID) error {
	return nil
}

func (f *fakeDraftCommands) appendCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.appends)
}

func (f *fakeDraftCommands) lastAppend() changesetapp.AppendOperationsInput {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.appends[len(f.appends)-1]
}

func (f *fakeDraftCommands) discardCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.discards)
}

type staticAddress string

func (a staticAddress) Network() string { return "tcp" }
func (a staticAddress) String() string  { return string(a) }

type staticListener struct{ address net.Addr }

func (l staticListener) Accept() (net.Conn, error) { return nil, errors.New("not implemented") }
func (l staticListener) Close() error              { return nil }
func (l staticListener) Addr() net.Addr            { return l.address }
