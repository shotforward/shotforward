package shotforwardcloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryCredentials struct {
	mu    sync.Mutex
	value string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func jsonResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

func (s *memoryCredentials) Put(_ context.Context, _, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}
func (s *memoryCredentials) Get(context.Context, string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.value != "", nil
}
func (s *memoryCredentials) Delete(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := s.value != ""
	s.value = ""
	return found, nil
}

func TestDesktopExchangePersistsOnlyRefreshAndAuthenticatedCallsReuseSession(t *testing.T) {
	credentials := &memoryCredentials{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.example" {
			t.Fatalf("API request host=%q", request.URL.Host)
		}
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", TokenType: "Bearer", ExpiresIn: 900}), nil
		case "/v1/me":
			if request.Header.Get("Authorization") != "Bearer access-1" {
				return jsonResponse(http.StatusUnauthorized, map[string]string{}), nil
			}
			return jsonResponse(http.StatusOK, User{ID: "user-1", Status: "active"}), nil
		case "/v1/wallet":
			return jsonResponse(http.StatusOK, map[string]float64{
				"available_points": 492.989683587,
				"reserved_points":  1.25,
			}), nil
		}
		return jsonResponse(http.StatusNotFound, map[string]string{}), nil
	})}
	manager, err := NewManagerWithWebURL("https://api.example", "https://shotforward.example", credentials, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	if credentials.value != "refresh-1" {
		t.Fatalf("stored credential=%q", credentials.value)
	}
	account, err := manager.Account(context.Background())
	if err != nil || !account.SignedIn || account.Wallet.AvailablePoints != 492.989683587 || account.Wallet.ReservedPoints != 1.25 {
		t.Fatalf("account=%#v err=%v", account, err)
	}
	if account.AccountPortalURL != "https://shotforward.example/app/account" {
		t.Fatalf("account portal URL=%q", account.AccountPortalURL)
	}
}

func TestModelsDecodesPublishedVideoCapabilities(t *testing.T) {
	model := cloudVideoModel("MiniMax-H3-Turbo-Ref2VA", "ref2va", []string{"reference"}, 10)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/models":
			return jsonResponse(http.StatusOK, map[string]any{"schema_version": 1, "catalog_version": "catalog-1", "data": []Model{model}}), nil
		default:
			t.Fatalf("unexpected request: %s", request.URL.Path)
			return nil, nil
		}
	})}
	manager, err := NewManager("https://api.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	models, err := manager.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].Capabilities == nil {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	got := models[0]
	if got.ID != model.ID || got.CapabilityVersion != "catalog-v1" || got.SortOrder != 10 || got.Capabilities.Workflow != "ref2va" || len(got.Capabilities.Inputs) != 3 || got.Capabilities.Output.Sizes[2] != "1024x1024" {
		t.Fatalf("decoded model=%#v", got)
	}
}

func TestModelsRejectsUnknownCapabilitySchema(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/models":
			return jsonResponse(http.StatusOK, map[string]any{"schema_version": 2, "data": []Model{}}), nil
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{}), nil
		}
	})}
	manager, _ := NewManager("https://api.example", &memoryCredentials{}, client)
	if err := manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Models(context.Background()); !errors.Is(err, ErrCloudResponse) {
		t.Fatalf("Models() error=%v", err)
	}
}

func TestDesktopExchangeReturnsSpecificErrorForRejectedCode(t *testing.T) {
	manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/auth/desktop-code/exchanges" {
			t.Fatalf("unexpected path=%q", request.URL.Path)
		}
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": map[string]string{
			"code": "invalid_desktop_code", "message": "desktop login code is invalid or expired",
		}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "INVALID-CODE"); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("exchange error=%v", err)
	}

	recorder := httptest.NewRecorder()
	writeError(recorder, err)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"invalid_desktop_code"`) {
		t.Fatalf("response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountWithoutRefreshTokenIsSignedOut(t *testing.T) {
	manager, err := NewManagerWithWebURL("https://api.example", "https://shotforward.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, map[string]string{}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	account, err := manager.Account(context.Background())
	if err != nil || account.SignedIn {
		t.Fatalf("account=%#v err=%v", account, err)
	}
	if account.AccountPortalURL != "https://shotforward.example/app/account" {
		t.Fatalf("account portal URL=%q", account.AccountPortalURL)
	}
}

func TestAccountClearsRejectedRefreshTokenAndReturnsSignedOut(t *testing.T) {
	credentials := &memoryCredentials{value: "expired-refresh"}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/auth/refresh" {
			t.Fatalf("unexpected cloud path=%q", request.URL.Path)
		}
		return jsonResponse(http.StatusUnauthorized, map[string]string{}), nil
	})}
	manager, err := NewManagerWithWebURL("https://api.example", "https://shotforward.example", credentials, client)
	if err != nil {
		t.Fatal(err)
	}
	account, err := manager.Account(context.Background())
	if err != nil || account.SignedIn {
		t.Fatalf("account=%#v err=%v", account, err)
	}
	if credentials.value != "" {
		t.Fatal("rejected refresh credential was not deleted")
	}
}

func TestForgetSessionDoesNotRequireCloudAvailability(t *testing.T) {
	credentials := &memoryCredentials{value: "saved-refresh"}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return jsonResponse(http.StatusServiceUnavailable, map[string]string{}), nil
	})}
	manager, err := NewManagerWithWebURL("https://api.example", "https://shotforward.example", credentials, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ForgetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if credentials.value != "" || requests != 0 {
		t.Fatalf("credential=%q cloud requests=%d", credentials.value, requests)
	}
}

func TestAgentModelsOnlyPublishesUniqueResponsesModels(t *testing.T) {
	manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path=%q", request.URL.Path)
		}
		return jsonResponse(http.StatusOK, map[string]any{"data": []Model{
			{ID: "gpt-6-astra", DisplayName: "GPT-6-Astra", ProductKind: "responses"},
			{ID: "image-model", DisplayName: "Image", ProductKind: "image"},
			{ID: "gpt-6-astra", DisplayName: "duplicate", ProductKind: "responses"},
			{ID: " gpt-6-sol ", ProductKind: "responses"},
		}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken = "test-token"
	manager.expiresAt = time.Now().Add(time.Hour)

	models, err := manager.AgentModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-6-astra" || models[0].DisplayName != "GPT-6-Astra" || models[1].ID != "gpt-6-sol" || models[1].DisplayName != "gpt-6-sol" {
		t.Fatalf("Agent models=%#v", models)
	}
}

func TestAgentModelsCachesOrdinaryReadsAndExplicitRefreshBypassesCache(t *testing.T) {
	requests := 0
	manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path=%q", request.URL.Path)
		}
		requests++
		return jsonResponse(http.StatusOK, map[string]any{"data": []Model{{ID: "gpt-6-luna", DisplayName: "Luna", ProductKind: "responses"}}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken = "test-token"
	manager.expiresAt = time.Now().Add(time.Hour)

	first, err := manager.AgentModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.AgentModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := manager.RefreshAgentModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(first) != 1 || len(second) != 1 || len(refreshed) != 1 {
		t.Fatalf("requests=%d first=%#v second=%#v refreshed=%#v", requests, first, second, refreshed)
	}
}

func TestAuthenticatedRequestPreservesSanitizedCloudError(t *testing.T) {
	credentials := &memoryCredentials{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-private", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/video-tasks":
			response := jsonResponse(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_reference", "message": "access-private image was rejected"}})
			response.Header.Set("X-Request-ID", "cloud-request-1")
			return response, nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	})}
	manager, err := NewManager("https://api.example", credentials, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	err = manager.DoAuthenticated(context.Background(), http.MethodPost, "/v1/video-tasks", map[string]string{"model": "h3"}, &struct{}{})
	if err == nil || strings.Contains(err.Error(), "access-private") || !strings.Contains(err.Error(), "invalid_reference") || !strings.Contains(err.Error(), "cloud-request-1") {
		t.Fatalf("cloud error = %v", err)
	}
	var public interface{ PublicFailureMessage() string }
	if !errors.As(err, &public) || public.PublicFailureMessage() == "" {
		t.Fatalf("cloud error is not publicly displayable: %T", err)
	}
}
