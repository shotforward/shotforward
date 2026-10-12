package projecthttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

func TestGenerationTranslationSelectionPublicContract(t *testing.T) {
	service, projectID, episodeID, slotID := generationHTTPFixture(t)
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	body := `{"episode_id":"` + episodeID.String() + `","operation_key":"translate-1","prompt_translation":{"runtime_kind":"user_codex","model_id":"m"},"targets":[{"media_slot_id":"` + slotID.String() + `","expected_slot_revision":3,"prompt":"雨夜","generator_id":"video.h3.official","generation_mode":"first_image","requested_duration_seconds":5,"parameters":{}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.submitted.PromptTranslation == nil || service.submitted.PromptTranslation.RuntimeKind != "user_codex" || service.submitted.PromptTranslation.ModelID != "m" {
		t.Fatalf("selection not passed: %d %s %#v", response.Code, response.Body.String(), service.submitted)
	}
	bad := strings.Replace(body, `"model_id":"m"`, `"model_id":"m","session_id":"forbidden"`, 1)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/generation/jobs", strings.NewReader(bad))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("chat-session parameter accepted: %d", response.Code)
	}
}

func TestGenerationTranslationPublicErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{projectapp.ErrPromptTranslationUnavailable, 503, "prompt_translation_unavailable"},
		{projectapp.ErrPromptTranslationSelectionChanged, 409, "prompt_translation_selection_changed"},
		{projectapp.ErrPromptTranslationTimeout, 504, "prompt_translation_timeout"},
		{projectapp.ErrPromptTranslationInvalid, 422, "prompt_translation_invalid"},
		{projectapp.ErrPromptTranslationFailed, 502, "prompt_translation_failed"},
		{&projectapp.GenerationPromptTranslationFailure{Code: "shotforward_insufficient_points"}, 402, "shotforward_insufficient_points"},
		{&projectapp.GenerationPromptTranslationFailure{Code: "shotforward_negative_balance"}, 402, "shotforward_negative_balance"},
		{&projectapp.GenerationPromptTranslationFailure{Code: "shotforward_wallet_not_found"}, 402, "shotforward_wallet_not_found"},
		{&projectapp.GenerationPromptTranslationFailure{Code: "untrusted-upstream-code"}, 502, "prompt_translation_failed"},
	} {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeMappedError(response, test.err)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("mapped error=%d %s", response.Code, response.Body.String())
			}
		})
	}
}
