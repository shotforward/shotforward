package localhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testHost   = "127.0.0.1:43111"
	testOrigin = "http://127.0.0.1:43111"
)

func TestPairingCreatesHttpOnlySessionAndProtectsApplication(t *testing.T) {
	t.Parallel()
	credentials, pairing, err := NewCredentials(bytes.NewReader(bytes.Repeat([]byte{0x4a}, 96)))
	if err != nil {
		t.Fatal(err)
	}
	application := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "" || request.Header.Get("Origin") != testOrigin ||
			(request.Method != http.MethodGet && request.Method != http.MethodHead && request.Header.Get("X-CSRF-Token") == "") {
			t.Fatal("internal authentication headers were not injected")
		}
		response.WriteHeader(http.StatusNoContent)
	})
	gateway, err := NewGateway(GatewayOptions{Application: application, Static: http.NotFoundHandler(), Credentials: credentials, AllowedHost: testHost, AllowedOrigin: testOrigin, InstanceID: "instance-test"})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := localRequest(http.MethodPost, "/api/v1/projects", `{}`)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.Code)
	}

	pair := localRequest(http.MethodPost, "/api/v1/bootstrap", `{"pairing_code":"`+strings.ToLower(pairing)+`"}`)
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, pair)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("pair status=%d cookies=%v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if cookie.Name != SessionCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %#v", cookie)
	}
	var paired struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &paired); err != nil || paired.CSRFToken == "" {
		t.Fatalf("pair response = %s, %v", response.Body.String(), err)
	}

	restored := localRequest(http.MethodGet, "/api/v1/session", "")
	// Same-origin browser GET requests commonly omit Origin. Host and loopback
	// checks still prevent DNS rebinding, while mutating methods require Origin.
	restored.Header.Del("Origin")
	restored.AddCookie(cookie)
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, restored)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), paired.CSRFToken) || !strings.Contains(response.Body.String(), "instance-test") {
		t.Fatalf("session restore status=%d body=%s", response.Code, response.Body.String())
	}
	forwardedGet := localRequest(http.MethodGet, "/api/v1/projects", "")
	forwardedGet.Header.Del("Origin")
	forwardedGet.AddCookie(cookie)
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, forwardedGet)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin browser GET status=%d body=%s", response.Code, response.Body.String())
	}

	replay := httptest.NewRecorder()
	gateway.ServeHTTP(replay, localRequest(http.MethodPost, "/api/v1/bootstrap", `{"pairing_code":"`+pairing+`"}`))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("pairing replay status = %d", replay.Code)
	}

	missingCSRF := localRequest(http.MethodPost, "/api/v1/projects", `{}`)
	missingCSRF.AddCookie(cookie)
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, missingCSRF)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.Code)
	}
	var failure struct {
		Error struct {
			RequestID       string `json:"request_id"`
			SuggestedAction string `json:"suggested_action"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Error.RequestID == "" || failure.Error.SuggestedAction == "" {
		t.Fatalf("security error is missing stable guidance: %s (%v)", response.Body.String(), err)
	}

	authorized := localRequest(http.MethodPost, "/api/v1/projects", `{}`)
	authorized.AddCookie(cookie)
	authorized.Header.Set("X-CSRF-Token", paired.CSRFToken)
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, authorized)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestGatewayRejectsNetworkConfusionAndHealthLeaksNoSecrets(t *testing.T) {
	t.Parallel()
	credentials, _, err := NewCredentials(bytes.NewReader(bytes.Repeat([]byte{0x2a}, 96)))
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGateway(GatewayOptions{Application: http.NotFoundHandler(), Static: http.NotFoundHandler(), Credentials: credentials, AllowedHost: testHost, AllowedOrigin: testOrigin, InstanceID: "instance-public"})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*http.Request){
		"remote": func(request *http.Request) { request.RemoteAddr = "203.0.113.1:80" },
		"host":   func(request *http.Request) { request.Host = "attacker.example" },
		"origin": func(request *http.Request) { request.Header.Set("Origin", "https://attacker.example") },
	} {
		t.Run(name, func(t *testing.T) {
			request := localRequest(http.MethodPost, "/api/v1/bootstrap", `{}`)
			mutate(request)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
	health := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/healthz", nil)
	health.RemoteAddr, health.Host = "127.0.0.1:50000", testHost
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, health)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "instance-public") || strings.Contains(response.Body.String(), "token") {
		t.Fatalf("health response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayServesStaticUIWithoutExposingTheAPI(t *testing.T) {
	t.Parallel()
	credentials, _, err := NewCredentials(bytes.NewReader(bytes.Repeat([]byte{0x3a}, 96)))
	if err != nil {
		t.Fatal(err)
	}
	static := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = response.Write([]byte("studio-ui"))
	})
	gateway, err := NewGateway(GatewayOptions{Application: http.NotFoundHandler(), Static: static, Credentials: credentials, AllowedHost: testHost, AllowedOrigin: testOrigin, InstanceID: "instance-static"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/projects/example", nil)
	request.RemoteAddr, request.Host = "127.0.0.1:50000", testHost
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "studio-ui" {
		t.Fatalf("static response = %d %q", response.Code, response.Body.String())
	}

	api := localRequest(http.MethodGet, "/api/v1/projects", "")
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, api)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d", response.Code)
	}
}

func TestPairingAttemptsAreBounded(t *testing.T) {
	t.Parallel()
	credentials, pairing, err := NewCredentials(bytes.NewReader(bytes.Repeat([]byte{0x1a}, 96)))
	if err != nil {
		t.Fatal(err)
	}
	for range maximumPairingAttempts {
		if _, ok := credentials.Exchange("WRONG-CODE"); ok {
			t.Fatal("wrong pairing code succeeded")
		}
	}
	if _, ok := credentials.Exchange(pairing); ok {
		t.Fatal("pairing succeeded after attempt limit")
	}
}

func localRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, "http://"+testHost+path, strings.NewReader(body))
	request.RemoteAddr, request.Host = "127.0.0.1:50000", testHost
	request.Header.Set("Origin", testOrigin)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}
