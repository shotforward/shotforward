package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndexAndSPAFallback(t *testing.T) {
	t.Parallel()
	handler := NewHandler()
	for _, requestPath := range []string{
		"/",
		"/projects",
		"/projects/prj_test",
		"/projects/prj_test/assets",
		"/projects/prj_test/settings",
		"/projects/prj_test/trash",
		"/projects/prj_test/episodes/ep_test/script",
		"/projects/prj_test/episodes/ep_test/assets",
		"/projects/prj_test/episodes/ep_test/storyboard",
		"/projects/prj_test/episodes/ep_test/storyboard/clips/clp_test",
		"/projects/prj_test/episodes/ep_test/preview",
		"/change-sets/chg_test",
		"/tasks",
		"/settings",
		"/settings/agent",
		"/settings/runtime",
		"/settings/providers",
		"/settings/privacy",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ShotForward") {
			t.Fatalf("GET %s = %d %q", requestPath, response.Code, response.Body.String())
		}
		if cache := response.Header().Get("Cache-Control"); cache != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q", requestPath, cache)
		}
	}
}

func TestHandlerDoesNotFallbackMissingAssets(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
