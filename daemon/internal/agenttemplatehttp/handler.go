// Package agenttemplatehttp exposes Studio-wide Agent starter-template preferences.
// It must be mounted behind localhttp.Gateway.
package agenttemplatehttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

const maxRequestBytes = 256 * 1024

type Store interface {
	AgentTemplates(context.Context, string, time.Time) (studio.AgentTemplateSettings, error)
	ReplaceAgentTemplateOverrides(context.Context, map[string]string, int64, time.Time) (studio.AgentTemplateSettings, error)
}

type Handler struct {
	store Store
	now   func() time.Time
}

func NewHandler(store Store) (*Handler, error) {
	if store == nil {
		return nil, errors.New("create Agent template HTTP Handler: Store is required")
	}
	return &Handler{store: store, now: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != "/api/v1/agent-templates" {
		writeError(response, http.StatusNotFound, "not_found", "route not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		locale := request.URL.Query().Get("initial_locale")
		if locale != "zh-CN" && locale != "en" {
			writeError(response, http.StatusBadRequest, "invalid_argument", "initial_locale must be zh-CN or en")
			return
		}
		settings, err := h.store.AgentTemplates(request.Context(), locale, h.now())
		if err != nil {
			writeError(response, http.StatusInternalServerError, "internal_error", "Agent templates could not be loaded")
			return
		}
		writeSettings(response, settings)
	case http.MethodPut:
		var input struct {
			Overrides        map[string]string `json:"overrides"`
			ExpectedRevision int64             `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil || input.Overrides == nil || input.ExpectedRevision < 1 {
			writeError(response, http.StatusBadRequest, "invalid_argument", "Agent template settings are invalid")
			return
		}
		settings, err := h.store.ReplaceAgentTemplateOverrides(request.Context(), input.Overrides, input.ExpectedRevision, h.now())
		if errors.Is(err, studio.ErrAgentTemplateRevisionConflict) {
			writeError(response, http.StatusConflict, "revision_conflict", "Agent templates changed since they were read")
			return
		}
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", "Agent template settings are invalid")
			return
		}
		writeSettings(response, settings)
	default:
		response.Header().Set("Allow", "GET, PUT")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
	}
}

func writeSettings(response http.ResponseWriter, value studio.AgentTemplateSettings) {
	writeJSON(response, http.StatusOK, map[string]any{
		"locale": value.Locale, "overrides": value.Overrides, "revision": value.Revision,
	})
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		return errors.New("application/json is required")
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": map[string]any{}, "retryable": false,
		"request_id": response.Header().Get("X-Request-ID"), "suggested_action": "Refresh Agent template settings and retry.",
	}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
