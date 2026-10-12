package agenthttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInputEndpointForwardsVideoPromptTargetWithoutRewritingUserMessage(t *testing.T) {
	handler, api := testHandler(t, fakeEvents{})
	body := `{"client_operation_id":"video-context-1","model_id":"visible","text":"Split into three clips","focus":{"clip_id":"clp_FFFFFFFFFFFFFFFFFFFFFFFFFF"},"video_prompt_target":{"generator_id":"video.h3.minimax-cn","provider_model_id":"h3-ref2va","keyframe_provider_model_id":"h3-fl2va","focus":{"clip_id":"clp_FFFFFFFFFFFFFFFFFFFFFFFFFF","generation_mode":"reference","generator_id":"video.h3.minimax-cn","provider_model_id":"h3-ref2va"}}}`
	request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/inputs", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	target := api.submitted.VideoPromptTarget
	if api.submitted.Text != "Split into three clips" || target == nil || target.ProviderModelID != "h3-ref2va" || target.Focus == nil || target.Focus.ClipID != api.submitted.Focus.ClipID {
		t.Fatalf("submitted input = %#v", api.submitted)
	}
}

func TestInputEndpointRejectsInvalidOrMismatchedVideoPromptTarget(t *testing.T) {
	for _, target := range []string{
		`{"generator_id":"image.codex","provider_model_id":"image"}`,
		`{"generator_id":"video.h3.minimax-cn","provider_model_id":"h3-ref2va","focus":{"clip_id":"clp_FFFFFFFFFFFFFFFFFFFFFFFFFF","generation_mode":"reference","generator_id":"video.h3.minimax-cn","provider_model_id":"h3-ref2va"}}`,
		`{"generator_id":"video.h3.minimax-cn","provider_model_id":""}`,
	} {
		t.Run(target, func(t *testing.T) {
			handler, api := testHandler(t, fakeEvents{})
			body := `{"client_operation_id":"bad-video-context","model_id":"visible","text":"Rewrite video prompt","focus":{},"video_prompt_target":` + target + `}`
			request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/inputs", strings.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if api.submitted.Text != "" {
				t.Fatal("invalid target reached scheduler")
			}
		})
	}
}
