package localhttp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

const maxBootstrapRequestBytes = 4 * 1024

type GatewayOptions struct {
	Application   http.Handler
	Static        http.Handler
	Credentials   *Credentials
	AllowedHost   string
	AllowedOrigin string
	InstanceID    string
	ServiceBearer string
}

type Gateway struct {
	application   http.Handler
	static        http.Handler
	credentials   *Credentials
	host          string
	origin        string
	instanceID    string
	requests      atomic.Uint64
	serviceBearer string
}

func NewGateway(options GatewayOptions) (*Gateway, error) {
	if options.Application == nil || options.Static == nil || options.Credentials == nil || !validLoopbackHost(options.AllowedHost) ||
		options.AllowedOrigin != "http://"+options.AllowedHost || strings.TrimSpace(options.InstanceID) == "" {
		return nil, ErrInvalidSecurityConfiguration
	}
	return &Gateway{application: options.Application, static: options.Static, credentials: options.Credentials, host: options.AllowedHost, origin: options.AllowedOrigin, instanceID: options.InstanceID, serviceBearer: options.ServiceBearer}, nil
}

func validLoopbackHost(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (g *Gateway) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	requestID := "req_" + g.instanceID + "_" + strconv.FormatUint(g.requests.Add(1), 10)
	response.Header().Set("X-Request-ID", requestID)
	request.Header.Set("X-Request-ID", requestID)
	if !strings.HasPrefix(request.URL.Path, "/api/") && request.URL.Path != "/healthz" {
		if err := g.validateNetwork(request, false); err != nil {
			writeLocalError(response, err.status, err.code, err.message)
			return
		}
		g.static.ServeHTTP(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	serviceRequest := strings.HasPrefix(request.URL.Path, "/api/v1/cloud/openai/")
	requireOrigin := !serviceRequest && request.URL.Path != "/healthz" && request.Method != http.MethodGet && request.Method != http.MethodHead
	if err := g.validateNetwork(request, requireOrigin); err != nil {
		writeLocalError(response, err.status, err.code, err.message)
		return
	}
	if request.Header.Get("Origin") == g.origin {
		response.Header().Set("Access-Control-Allow-Origin", g.origin)
		response.Header().Set("Vary", "Origin")
	}
	switch request.URL.Path {
	case "/healthz":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeLocalError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
			return
		}
		writeLocalJSON(response, http.StatusOK, map[string]any{"status": "ok", "instance_id": g.instanceID})
		return
	case "/api/v1/bootstrap":
		g.bootstrap(response, request)
		return
	}
	if request.Method == http.MethodOptions {
		g.writePreflight(response)
		return
	}
	if serviceRequest {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		wantedHash, providedHash := sha256.Sum256([]byte(g.serviceBearer)), sha256.Sum256([]byte(provided))
		if g.serviceBearer == "" || provided == "" || subtle.ConstantTimeCompare(wantedHash[:], providedHash[:]) != 1 {
			writeLocalError(response, http.StatusUnauthorized, "authentication_required", "service authentication failed")
			return
		}
		g.application.ServeHTTP(response, request)
		return
	}
	cookie, err := request.Cookie(SessionCookieName)
	requireCSRF := request.Method != http.MethodGet && request.Method != http.MethodHead
	if err != nil || !g.credentials.Authenticate(cookie.Value, request.Header.Get("X-CSRF-Token"), requireCSRF) {
		code, status := "authentication_required", http.StatusUnauthorized
		if err == nil && requireCSRF && g.credentials.Authenticate(cookie.Value, "", false) {
			code, status = "csrf_rejected", http.StatusForbidden
		}
		writeLocalError(response, status, code, "local session authentication failed")
		return
	}
	if request.URL.Path == "/api/v1/session" {
		if request.Method != http.MethodGet {
			writeLocalError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
			return
		}
		_, csrf := g.credentials.InternalTokens()
		writeLocalJSON(response, http.StatusOK, map[string]any{"csrf_token": csrf, "instance_id": g.instanceID})
		return
	}
	bearer, csrf := g.credentials.InternalTokens()
	forwarded := request.Clone(request.Context())
	forwarded.Header = request.Header.Clone()
	forwarded.Header.Set("Authorization", "Bearer "+bearer)
	// The public gateway has already validated Host, loopback source, and any
	// browser-supplied Origin. Downstream adapters receive a canonical trusted
	// Origin even when a same-origin browser GET legitimately omitted it.
	forwarded.Header.Set("Origin", g.origin)
	if requireCSRF {
		forwarded.Header.Set("X-CSRF-Token", csrf)
	}
	g.application.ServeHTTP(response, forwarded)
}

func (g *Gateway) bootstrap(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodOptions {
		g.writePreflight(response)
		return
	}
	if request.Method != http.MethodPost {
		writeLocalError(response, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeLocalError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "application/json is required")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxBootstrapRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		PairingCode string `json:"pairing_code"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeLocalError(response, http.StatusBadRequest, "invalid_argument", "bootstrap request is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeLocalError(response, http.StatusBadRequest, "invalid_argument", "bootstrap request must contain one JSON object")
		return
	}
	csrf, ok := g.credentials.Exchange(input.PairingCode)
	if !ok {
		writeLocalError(response, http.StatusUnauthorized, "pairing_rejected", "pairing code is invalid or unavailable")
		return
	}
	session, _ := g.credentials.InternalTokens()
	http.SetCookie(response, &http.Cookie{Name: SessionCookieName, Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeLocalJSON(response, http.StatusOK, map[string]any{"csrf_token": csrf})
}

func (g *Gateway) writePreflight(response http.ResponseWriter) {
	response.Header().Set("Access-Control-Allow-Origin", g.origin)
	response.Header().Set("Vary", "Origin")
	response.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	response.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID, X-CSRF-Token")
	response.WriteHeader(http.StatusNoContent)
}

type localFailure struct {
	status        int
	code, message string
}

func (g *Gateway) validateNetwork(request *http.Request, requireOrigin bool) *localFailure {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return &localFailure{http.StatusForbidden, "forbidden", "request source is not loopback"}
	}
	if request.Host != g.host {
		return &localFailure{http.StatusForbidden, "forbidden", "request Host is not allowed"}
	}
	origin := request.Header.Get("Origin")
	if origin != "" && origin != g.origin {
		return &localFailure{http.StatusForbidden, "forbidden", "request Origin is not allowed"}
	}
	if requireOrigin && origin == "" {
		return &localFailure{http.StatusForbidden, "forbidden", "request Origin is not allowed"}
	}
	return nil
}

func writeLocalError(response http.ResponseWriter, status int, code, message string) {
	writeLocalJSON(response, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": map[string]any{}, "retryable": false,
		"request_id": response.Header().Get("X-Request-ID"), "suggested_action": "Retry through the active ShotForward desktop session.",
	}})
}

func writeLocalJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
