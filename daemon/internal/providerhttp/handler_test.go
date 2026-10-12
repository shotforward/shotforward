package providerhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestConfigureAcceptsSecretButNeverReturnsIt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 4, 3, 4, 5, 0, time.UTC)
	service := &fakeService{profile: providerconfig.Profile{GeneratorID: domain.GeneratorVideoH3Official, EndpointPreset: providerconfig.EndpointMiniMaxGlobal,
		ModelID: "MiniMax-H3", Enabled: true, ConcurrencyLimit: 1, CredentialConfigured: true, Health: providerconfig.HealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at}}
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/generation-providers/video.h3.official", strings.NewReader(`{
        "endpoint_preset":"minimax_global","model_id":"MiniMax-H3","resource_id":"","workspace_id":"",
        "api_key":"minimax-super-secret","enabled":true,"concurrency_limit":1,"expected_revision":0}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.configured.APIKey != "minimax-super-secret" {
		t.Fatal("API key was not forwarded to Application Service")
	}
	if strings.Contains(response.Body.String(), "minimax-super-secret") || strings.Contains(response.Body.String(), "credential_ref") {
		t.Fatalf("response leaked credential material: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"credential_configured":true`) {
		t.Fatalf("response missing safe credential projection: %s", response.Body.String())
	}
}

func TestProviderRoutesAndStableErrors(t *testing.T) {
	t.Parallel()
	service := &fakeService{profile: providerconfig.Profile{GeneratorID: domain.GeneratorVideoWanOfficial, CredentialConfigured: true}, operationErr: providerconfig.ErrRevisionConflict}
	handler, _ := NewHandler(service)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/generation-providers/video.wan-3.0.official:check", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"revision_conflict"`) {
		t.Fatalf("check response = %d, %s", response.Code, response.Body.String())
	}

	service.operationErr = nil
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/generation-providers/video.wan-3.0.official", strings.NewReader(`{"expected_revision":2}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.deleted.ExpectedRevision != 2 {
		t.Fatalf("delete response = %d, %#v", response.Code, service.deleted)
	}
}

func TestModelProjectionIncludesDynamicCapabilities(t *testing.T) {
	profile := providerconfig.Profile{GeneratorID: domain.GeneratorVideoH3ShotForward, Enabled: true, Health: providerconfig.HealthReady, Models: []providerconfig.ModelProfile{{
		ModelID: "MiniMax-H3-Turbo-Ref2VA", DisplayName: "MiniMax-H3-Turbo · 参考生视频", Availability: "available", Family: "MiniMax-H3", Variant: "turbo", SortOrder: 10,
		CapabilityVersion: "catalog-v1", Health: providerconfig.HealthReady, Capabilities: &providerconfig.ModelCapabilities{
			Workflow: "ref2va", GenerationModes: []string{"reference"}, Duration: providerconfig.ModelDuration{MinimumSeconds: 4, MaximumSeconds: 15, StepSeconds: 1, DefaultSeconds: 5},
			Output: providerconfig.ModelOutput{DefaultSize: "720x1280", Sizes: []string{"720x1280", "1280x720", "1024x1024"}},
		},
	}}}
	response := mapProfile(profile)
	models := response["models"].([]map[string]any)
	if len(models) != 1 || models[0]["display_name"] != "MiniMax-H3-Turbo · 参考生视频" || models[0]["sort_order"] != 10 || models[0]["capabilities"] == nil {
		t.Fatalf("model projection=%#v", models)
	}
}

func TestProviderHTTPProjectionNeverReturnsNullCapabilityCollections(t *testing.T) {
	t.Parallel()
	service := &fakeService{profile: providerconfig.Profile{
		GeneratorID: domain.GeneratorImageShotForward,
		Models: []providerconfig.ModelProfile{{
			ModelID: "gpt-image-2", Capabilities: &providerconfig.ModelCapabilities{
				Inputs: []providerconfig.ModelInputCapability{{Role: "reference_image", MaximumCount: 5}},
			},
		}},
	}}
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/generation-providers", "/api/v1/generation-providers/image.shotforward"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var body struct {
				Provider  json.RawMessage   `json:"provider"`
				Providers []json.RawMessage `json:"providers"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			provider := body.Provider
			if provider == nil {
				provider = body.Providers[0]
			}
			for _, forbidden := range []string{`"generation_modes":null`, `"inputs":null`, `"sizes":null`, `"accepted_mime_types":null`} {
				if strings.Contains(string(provider), forbidden) {
					t.Fatalf("nil capability collection escaped HTTP projection: %s", provider)
				}
			}
			if !strings.Contains(string(provider), `"generation_modes":[]`) || !strings.Contains(string(provider), `"sizes":[]`) || !strings.Contains(string(provider), `"accepted_mime_types":[]`) {
				t.Fatalf("missing empty capability arrays: %s", provider)
			}
		})
	}
}

func TestValidateAndConfigureRoute(t *testing.T) {
	t.Parallel()
	service := &fakeService{profile: providerconfig.Profile{GeneratorID: domain.GeneratorVideoH3Official,
		Health: providerconfig.HealthReady, Revision: 2, CredentialConfigured: true}}
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/generation-providers/video.h3.official:validate-and-configure", strings.NewReader(`{
		"endpoint_preset":"minimax_global","model_id":"MiniMax-H3","models":[{"model_id":"MiniMax-H3","is_default":true}],
		"resource_id":"","workspace_id":"","api_key":"valid-secret","enabled":true,"concurrency_limit":1,"expected_revision":0}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.configured.APIKey != "valid-secret" {
		t.Fatalf("response = %d, configured = %#v, body = %s", response.Code, service.configured, response.Body.String())
	}

	service.operationErr = providerconfig.ErrProviderValidation
	request = httptest.NewRequest(http.MethodPost, "/api/v1/generation-providers/video.h3.official:validate-and-configure", strings.NewReader(`{
		"endpoint_preset":"minimax_global","model_id":"MiniMax-H3","models":[{"model_id":"MiniMax-H3","is_default":true}],
		"resource_id":"","workspace_id":"","api_key":"invalid-secret","enabled":true,"concurrency_limit":1,"expected_revision":0}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"provider_validation_failed"`) {
		t.Fatalf("validation response = %d, %s", response.Code, response.Body.String())
	}
}

func TestConfigureRejectsUnknownFieldsWithoutEchoingSecret(t *testing.T) {
	t.Parallel()
	handler, _ := NewHandler(&fakeService{})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/generation-providers/video.h3.official", strings.NewReader(`{"api_key":"secret-do-not-echo","unexpected":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "secret-do-not-echo") {
		t.Fatalf("response = %d, %s", response.Code, response.Body.String())
	}
}

type fakeService struct {
	profile      providerconfig.Profile
	configured   providerconfig.ConfigureInput
	deleted      providerconfig.DeleteInput
	operationErr error
}

func (s *fakeService) Profiles(context.Context) ([]providerconfig.Profile, error) {
	if s.operationErr != nil {
		return nil, s.operationErr
	}
	return []providerconfig.Profile{s.profile}, nil
}
func (s *fakeService) Profile(context.Context, domain.GeneratorID) (providerconfig.Profile, error) {
	return s.profile, s.operationErr
}
func (s *fakeService) Configure(_ context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	s.configured = input
	return s.profile, s.operationErr
}
func (s *fakeService) ValidateAndConfigure(_ context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	s.configured = input
	return s.profile, s.operationErr
}
func (s *fakeService) Check(context.Context, domain.GeneratorID) (providerconfig.Profile, error) {
	return s.profile, s.operationErr
}
func (s *fakeService) Delete(_ context.Context, input providerconfig.DeleteInput) error {
	s.deleted = input
	return s.operationErr
}
