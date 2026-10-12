// Package providerhttp exposes local, authenticated Generation Provider
// configuration use cases. It must be mounted behind localhttp.Gateway.
package providerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const MaxJSONRequestBytes = 32 * 1024

type Service interface {
	Profiles(context.Context) ([]providerconfig.Profile, error)
	Profile(context.Context, domain.GeneratorID) (providerconfig.Profile, error)
	Configure(context.Context, providerconfig.ConfigureInput) (providerconfig.Profile, error)
	ValidateAndConfigure(context.Context, providerconfig.ConfigureInput) (providerconfig.Profile, error)
	Check(context.Context, domain.GeneratorID) (providerconfig.Profile, error)
	Delete(context.Context, providerconfig.DeleteInput) error
}

type Handler struct{ service Service }

func NewHandler(service Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("create Provider HTTP Handler: Service is required")
	}
	return &Handler{service: service}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	segments := splitPath(request.URL.Path)
	switch {
	case len(segments) == 3 && equal(segments, "api", "v1", "generation-providers"):
		h.list(response, request)
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "generation-providers" && strings.HasSuffix(segments[3], ":check"):
		h.check(response, request, strings.TrimSuffix(segments[3], ":check"))
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "generation-providers" && strings.HasSuffix(segments[3], ":validate-and-configure"):
		h.validateAndConfigure(response, request, strings.TrimSuffix(segments[3], ":validate-and-configure"))
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "generation-providers":
		h.item(response, request, segments[3])
	default:
		writeError(response, http.StatusNotFound, "not_found", "route not found")
	}
}

func (h *Handler) list(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	profiles, err := h.service.Profiles(request.Context())
	if err != nil {
		writeMappedError(response, err)
		return
	}
	items := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, mapProfile(profile))
	}
	writeJSON(response, http.StatusOK, map[string]any{"providers": items})
}

func (h *Handler) item(response http.ResponseWriter, request *http.Request, rawID string) {
	id := domain.GeneratorID(rawID)
	switch request.Method {
	case http.MethodGet:
		profile, err := h.service.Profile(request.Context(), id)
		if err != nil {
			writeMappedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"provider": mapProfile(profile)})
	case http.MethodPut:
		input, err := decodeConfigureInput(response, request, id)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		profile, err := h.service.Configure(request.Context(), input)
		if err != nil {
			writeMappedError(response, err)
			return
		}
		status := http.StatusOK
		if input.ExpectedRevision == 0 {
			status = http.StatusCreated
		}
		writeJSON(response, status, map[string]any{"provider": mapProfile(profile)})
	case http.MethodDelete:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decodeJSON(response, request, &input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if err := h.service.Delete(request.Context(), providerconfig.DeleteInput{GeneratorID: id, ExpectedRevision: input.ExpectedRevision}); err != nil {
			writeMappedError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response)
	}
}

func (h *Handler) check(response http.ResponseWriter, request *http.Request, rawID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var input struct{}
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	profile, err := h.service.Check(request.Context(), domain.GeneratorID(rawID))
	if err != nil {
		writeMappedError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"provider": mapProfile(profile)})
}

func (h *Handler) validateAndConfigure(response http.ResponseWriter, request *http.Request, rawID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	input, err := decodeConfigureInput(response, request, domain.GeneratorID(rawID))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	profile, err := h.service.ValidateAndConfigure(request.Context(), input)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	status := http.StatusOK
	if input.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	writeJSON(response, status, map[string]any{"provider": mapProfile(profile)})
}

func decodeConfigureInput(response http.ResponseWriter, request *http.Request, generatorID domain.GeneratorID) (providerconfig.ConfigureInput, error) {
	var input struct {
		EndpointPreset   string `json:"endpoint_preset"`
		ModelID          string `json:"model_id"`
		ResourceID       string `json:"resource_id"`
		WorkspaceID      string `json:"workspace_id"`
		APIKey           string `json:"api_key"`
		Enabled          bool   `json:"enabled"`
		ConcurrencyLimit int    `json:"concurrency_limit"`
		ExpectedRevision int64  `json:"expected_revision"`
		Models           []struct {
			ModelID   string `json:"model_id"`
			IsDefault bool   `json:"is_default"`
		} `json:"models"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		return providerconfig.ConfigureInput{}, err
	}
	models := make([]providerconfig.ModelRecord, 0, len(input.Models))
	for _, model := range input.Models {
		models = append(models, providerconfig.ModelRecord{ModelID: model.ModelID, IsDefault: model.IsDefault})
	}
	return providerconfig.ConfigureInput{GeneratorID: generatorID,
		EndpointPreset: input.EndpointPreset, ModelID: input.ModelID, ResourceID: input.ResourceID, WorkspaceID: input.WorkspaceID,
		Models: models, APIKey: input.APIKey, Enabled: input.Enabled, ConcurrencyLimit: input.ConcurrencyLimit, ExpectedRevision: input.ExpectedRevision}, nil
}

func mapProfile(value providerconfig.Profile) map[string]any {
	models := make([]map[string]any, 0, len(value.Models))
	for _, model := range value.Models {
		item := map[string]any{"model_id": model.ModelID, "is_default": model.IsDefault, "capability_version": model.CapabilityVersion, "health": model.Health, "failure_code": model.FailureCode}
		if model.DisplayName != "" {
			item["display_name"] = model.DisplayName
		}
		if model.Availability != "" {
			item["availability"] = model.Availability
		}
		if model.Family != "" {
			item["family"] = model.Family
		}
		if model.Variant != "" {
			item["variant"] = model.Variant
		}
		if model.SortOrder != 0 {
			item["sort_order"] = model.SortOrder
		}
		if model.Deprecated {
			item["deprecated"] = true
		}
		if model.ReplacementModelID != "" {
			item["replacement_model_id"] = model.ReplacementModelID
		}
		if model.Capabilities != nil {
			item["capabilities"] = model.Capabilities
		}
		if model.LastCheckedAt != nil {
			item["last_checked_at"] = *model.LastCheckedAt
		}
		models = append(models, item)
	}
	result := map[string]any{"generator_id": string(value.GeneratorID), "endpoint_preset": value.EndpointPreset, "model_id": value.ModelID,
		"resource_id": value.ResourceID, "workspace_id": value.WorkspaceID, "enabled": value.Enabled,
		"concurrency_limit": value.ConcurrencyLimit, "credential_configured": value.CredentialConfigured,
		"capability_version": value.CapabilityVersion, "health": value.Health, "failure_code": value.FailureCode,
		"revision": value.Revision, "created_at": value.CreatedAt, "updated_at": value.UpdatedAt, "models": models}
	if value.LastCheckedAt != nil {
		result["last_checked_at"] = *value.LastCheckedAt
	}
	return result
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		return errors.New("application/json is required")
	}
	request.Body = http.MaxBytesReader(response, request.Body, MaxJSONRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeMappedError(response http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "Provider configuration operation failed"
	switch {
	case errors.Is(err, providerconfig.ErrInvalidRequest):
		status, code, message = http.StatusBadRequest, "invalid_argument", "Provider configuration request is invalid"
	case errors.Is(err, providerconfig.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "Provider configuration was not found"
	case errors.Is(err, providerconfig.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "revision_conflict", "Provider configuration changed since it was read"
	case errors.Is(err, providerconfig.ErrCredentialUnavailable):
		status, code, message = http.StatusServiceUnavailable, "credential_store_unavailable", "operating system credential storage is unavailable"
	case errors.Is(err, providerconfig.ErrProviderValidation):
		status, code, message = http.StatusUnprocessableEntity, "provider_validation_failed", "Provider credentials or capabilities could not be validated"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusServiceUnavailable, "request_interrupted", "Provider configuration operation was interrupted"
	}
	writeError(response, status, code, message)
}

func methodNotAllowed(response http.ResponseWriter) {
	writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
}
func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}, "retryable": false,
		"request_id": response.Header().Get("X-Request-ID"), "suggested_action": "Refresh Provider settings and retry."}})
}
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
func splitPath(value string) []string {
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
func equal(values []string, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	for index := range values {
		if values[index] != expected[index] {
			return false
		}
	}
	return true
}
