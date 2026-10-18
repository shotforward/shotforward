package shotforwardcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const refreshCredentialReference = "shotforward_cloud_refresh_token"
const agentModelCacheTTL = time.Minute
const agentModelFetchTimeout = 15 * time.Second
const maxOpenAIProxyRequestBytes = 64 << 20

var (
	ErrLoginRequired    = errors.New("ShotForward cloud login required")
	ErrInvalidLoginCode = errors.New("ShotForward desktop login code is invalid or expired")
	ErrCloudResponse    = errors.New("ShotForward cloud request failed")
)

type HTTPStatusError struct {
	Status        int
	Code          string
	publicMessage string
}

func (e *HTTPStatusError) Error() string {
	if e.publicMessage != "" {
		return e.publicMessage
	}
	return fmt.Sprintf("%v: HTTP %d", ErrCloudResponse, e.Status)
}
func (e *HTTPStatusError) Unwrap() error                 { return ErrCloudResponse }
func (e *HTTPStatusError) PublicFailureMessage() string  { return e.publicMessage }
func (e *HTTPStatusError) GenerationFailureCode() string { return e.Code }

func IsHTTPStatus(err error, status int) bool {
	var target *HTTPStatusError
	return errors.As(err, &target) && target.Status == status
}

type CredentialStore interface {
	Put(context.Context, string, string) error
	Get(context.Context, string) (string, bool, error)
	Delete(context.Context, string) (bool, error)
}

type Tokens struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

type User struct {
	ID            string  `json:"id"`
	Email         *string `json:"email"`
	Status        string  `json:"status"`
	EmailVerified bool    `json:"email_verified"`
}

type Wallet struct {
	AvailablePoints float64 `json:"available_points"`
	ReservedPoints  float64 `json:"reserved_points"`
}

type Account struct {
	SignedIn         bool    `json:"signed_in"`
	AccountPortalURL string  `json:"account_portal_url"`
	User             *User   `json:"user,omitempty"`
	Wallet           *Wallet `json:"wallet,omitempty"`
}

type Model struct {
	ID                 string                            `json:"id"`
	DisplayName        string                            `json:"display_name"`
	ProductKind        string                            `json:"product_kind"`
	Availability       string                            `json:"availability"`
	Family             string                            `json:"family"`
	Variant            string                            `json:"variant"`
	SortOrder          int                               `json:"sort_order"`
	Deprecated         bool                              `json:"deprecated"`
	ReplacementModelID string                            `json:"replacement_model_id"`
	IsDefault          bool                              `json:"is_default"`
	CapabilityVersion  string                            `json:"capability_version"`
	Capabilities       *providerconfig.ModelCapabilities `json:"capabilities"`
}

type Manager struct {
	failureMu     sync.Mutex
	agentFailures map[string]*agentFailure
	apiBase       *url.URL
	webBase       *url.URL
	http          *http.Client
	agentHTTP     *http.Client
	proxyTimeouts agentProxyTimeouts
	proxyLogger   *slog.Logger // guarded by mu
	credentials   CredentialStore
	mu            sync.Mutex
	accessToken   string
	expiresAt     time.Time
	modelMu       sync.Mutex
	agentModels   []agentapp.Model
	modelsUntil   time.Time
	modelEpoch    uint64
	modelFlight   *agentModelFlight
}

type agentModelFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	epoch    uint64
	waiters  int
	finished bool
	models   []agentapp.Model
	err      error
}

type agentModelSession struct {
	token string
	epoch uint64
}

type cloudModelCatalog struct {
	SchemaVersion  int     `json:"schema_version"`
	CatalogVersion string  `json:"catalog_version"`
	Data           []Model `json:"data"`
}

func NewManager(baseURL string, credentials CredentialStore, client *http.Client) (*Manager, error) {
	return NewManagerWithWebURL(baseURL, baseURL, credentials, client)
}

func NewManagerWithWebURL(apiBaseURL, webBaseURL string, credentials CredentialStore, client *http.Client) (*Manager, error) {
	apiBase, err := parseBaseURL(apiBaseURL)
	if err != nil {
		return nil, errors.New("ShotForward cloud API base URL must be an absolute HTTPS URL")
	}
	webBase, err := parseBaseURL(webBaseURL)
	if err != nil {
		return nil, errors.New("ShotForward cloud web base URL must be an absolute HTTPS URL")
	}
	if credentials == nil {
		return nil, errors.New("ShotForward cloud credential store is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	// A Client timeout includes reading the entire response body, so it must not
	// bound a live Agent stream. Copy the Client, not its Transport: proxy, TLS,
	// redirect, and cookie policies remain identical without mutating the caller.
	agentClient := *client
	agentClient.Timeout = 0
	return &Manager{
		apiBase: apiBase, webBase: webBase, credentials: credentials, http: client,
		agentHTTP: &agentClient, proxyTimeouts: defaultAgentProxyTimeouts(), proxyLogger: slog.Default(),
	}, nil
}

func parseBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid HTTPS base URL")
	}
	return parsed, nil
}

func (m *Manager) ExchangeDesktopCode(ctx context.Context, code string) error {
	var tokens Tokens
	err := m.requestJSON(ctx, http.MethodPost, "/v1/auth/desktop-code/exchanges", map[string]any{
		"code": code, "device_label": "ShotForward Desktop", "device_platform": runtime.GOOS,
	}, &tokens, false, false)
	if err != nil {
		if IsHTTPStatus(err, http.StatusUnauthorized) {
			return ErrInvalidLoginCode
		}
		return err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return ErrCloudResponse
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.credentials.Put(ctx, refreshCredentialReference, tokens.RefreshToken); err != nil {
		return fmt.Errorf("store ShotForward cloud session: %w", err)
	}
	m.accessToken = tokens.AccessToken
	m.expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	m.clearAgentModelCache()
	return nil
}

func (m *Manager) Logout(ctx context.Context) error {
	_ = m.requestJSON(ctx, http.MethodPost, "/v1/auth/logout", nil, nil, true, false)
	return m.ForgetSession(ctx)
}

// ForgetSession removes the local cloud session without requiring the cloud
// service to be reachable. It is the recovery path for unreadable, expired, or
// otherwise unusable stored credentials.
func (m *Manager) ForgetSession(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessToken, m.expiresAt = "", time.Time{}
	m.clearAgentModelCache()
	_, err := m.credentials.Delete(ctx, refreshCredentialReference)
	return err
}

func (m *Manager) Account(ctx context.Context) (Account, error) {
	portalURL := m.webBase.ResolveReference(&url.URL{Path: "/app/account"}).String()
	var user User
	if err := m.requestJSON(ctx, http.MethodGet, "/v1/me", nil, &user, true, true); errors.Is(err, ErrLoginRequired) {
		return Account{SignedIn: false, AccountPortalURL: portalURL}, nil
	} else if err != nil {
		return Account{}, err
	}
	var wallet Wallet
	if err := m.requestJSON(ctx, http.MethodGet, "/v1/wallet", nil, &wallet, true, true); err != nil {
		return Account{}, err
	}
	return Account{SignedIn: true, AccountPortalURL: portalURL, User: &user, Wallet: &wallet}, nil
}

func (m *Manager) Models(ctx context.Context) ([]Model, error) {
	var response cloudModelCatalog
	if err := m.requestJSON(ctx, http.MethodGet, "/v1/models", nil, &response, true, true); err != nil {
		return nil, err
	}
	return response.models()
}

func (c cloudModelCatalog) models() ([]Model, error) {
	// A missing version is the pre-capability legacy response. Unknown explicit
	// versions fail closed so new semantics are never guessed by an old client.
	if c.SchemaVersion != 0 && c.SchemaVersion != 1 {
		return nil, ErrCloudResponse
	}
	models := make([]Model, len(c.Data))
	for index, model := range c.Data {
		model.Capabilities = completeImageModelCapabilities(model)
		models[index] = model
	}
	return models, nil
}

// Background reads use only the already-authenticated session snapshot. They
// must not restore credentials, refresh login on 401, or acquire the auth mutex
// again while a network operation is in flight.
func (m *Manager) modelsWithSession(ctx context.Context, session *agentModelSession) ([]Model, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiBase.ResolveReference(&url.URL{Path: "/v1/models"}).String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+session.token)
	response, err := m.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return nil, ErrLoginRequired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, cloudHTTPStatusError(response, session.token)
	}
	var catalog cloudModelCatalog
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCloudResponse, err)
	}
	return catalog.models()
}

// AgentModels returns the server-published Responses models used by the
// ShotForward Agent. Other product kinds belong to image/video generation and
// must never leak into the Agent model picker.
func (m *Manager) AgentModels(ctx context.Context) ([]agentapp.Model, error) {
	return m.agentModelCatalog(ctx, false)
}

func (m *Manager) RefreshAgentModels(ctx context.Context) ([]agentapp.Model, error) {
	return m.agentModelCatalog(ctx, true)
}

// RefreshAgentModelsIfSignedIn is a background-only probe. It never restores a
// stored refresh credential or performs a login just to warm the model picker.
func (m *Manager) RefreshAgentModelsIfSignedIn(ctx context.Context) ([]agentapp.Model, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	session := m.backgroundAgentModelSession()
	if session == nil {
		return nil, false, nil
	}
	models, err := m.agentModelCatalogForSession(ctx, true, session)
	return models, true, err
}

func (m *Manager) backgroundAgentModelSession() *agentModelSession {
	// Authentication may itself be waiting on network I/O. Maintenance is
	// opportunistic and must never queue behind that mutex during shutdown.
	if !m.mu.TryLock() {
		return nil
	}
	defer m.mu.Unlock()
	if m.accessToken == "" || !time.Now().Add(30*time.Second).Before(m.expiresAt) {
		return nil
	}
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	return &agentModelSession{token: m.accessToken, epoch: m.modelEpoch}
}

func (m *Manager) agentModelCatalog(ctx context.Context, refresh bool) ([]agentapp.Model, error) {
	return m.agentModelCatalogForSession(ctx, refresh, nil)
}

func (m *Manager) agentModelCatalogForSession(ctx context.Context, refresh bool, session *agentModelSession) ([]agentapp.Model, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.modelMu.Lock()
	if session != nil && session.epoch != m.modelEpoch {
		m.modelMu.Unlock()
		return nil, ErrLoginRequired
	}
	if !refresh && time.Now().Before(m.modelsUntil) {
		models := append([]agentapp.Model(nil), m.agentModels...)
		m.modelMu.Unlock()
		return models, nil
	}
	flight := m.modelFlight
	if flight == nil {
		// The lookup belongs to its waiters, not to the first caller's deadline.
		// Once no callers remain, releaseAgentModelWaiter cancels it immediately.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentModelFetchTimeout)
		flight = &agentModelFlight{done: make(chan struct{}), cancel: cancel, epoch: m.modelEpoch}
		m.modelFlight = flight
		go m.loadAgentModels(fetchCtx, flight, session)
	}
	flight.waiters++
	m.modelMu.Unlock()
	select {
	case <-ctx.Done():
		m.releaseAgentModelWaiter(flight)
		return nil, ctx.Err()
	case <-flight.done:
		m.releaseAgentModelWaiter(flight)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return append([]agentapp.Model(nil), flight.models...), flight.err
	}
}

func (m *Manager) loadAgentModels(ctx context.Context, flight *agentModelFlight, session *agentModelSession) {
	defer flight.cancel()
	var models []Model
	var err error
	if session != nil {
		models, err = m.modelsWithSession(ctx, session)
	} else {
		models, err = m.Models(ctx)
	}
	result := make([]agentapp.Model, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if model.ProductKind != "responses" || id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(model.DisplayName)
		if displayName == "" {
			displayName = id
		}
		result = append(result, agentapp.Model{ID: id, DisplayName: displayName})
	}
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	if flight.finished {
		return
	}
	if err == nil {
		err = ctx.Err()
	}
	if m.modelFlight != flight || m.modelEpoch != flight.epoch {
		err = ErrLoginRequired
	}
	if err == nil {
		m.agentModels = append([]agentapp.Model(nil), result...)
		m.modelsUntil = time.Now().Add(agentModelCacheTTL)
		flight.models = append([]agentapp.Model(nil), result...)
	}
	flight.err, flight.finished = err, true
	if m.modelFlight == flight {
		m.modelFlight = nil
	}
	close(flight.done)
}

func (m *Manager) releaseAgentModelWaiter(flight *agentModelFlight) {
	m.modelMu.Lock()
	flight.waiters--
	if flight.waiters == 0 && !flight.finished && m.modelFlight == flight {
		m.modelFlight = nil
		flight.err, flight.finished = context.Canceled, true
		close(flight.done)
		m.modelMu.Unlock()
		flight.cancel()
		return
	}
	m.modelMu.Unlock()
}

func (m *Manager) clearAgentModelCache() {
	m.modelMu.Lock()
	m.modelEpoch++
	m.agentModels = nil
	m.modelsUntil = time.Time{}
	flight := m.modelFlight
	if flight != nil {
		m.modelFlight = nil
		flight.err, flight.finished = ErrLoginRequired, true
		close(flight.done)
	}
	m.modelMu.Unlock()
	if flight != nil {
		flight.cancel()
	}
}

// ProxyOpenAI is the credential boundary for the isolated bundled Codex. The
// child receives only a process-local capability; cloud access/refresh tokens
// remain owned by this manager.
func (m *Manager) ProxyOpenAI(w http.ResponseWriter, r *http.Request, suffix string) {
	if suffix != "/models" && suffix != "/responses" && suffix != "/responses/compact" {
		http.NotFound(w, r)
		return
	}
	if suffix == "/responses/compact" && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := m.ensureAccess(r.Context()); err != nil {
		if r.Context().Err() == nil {
			writeError(w, err)
		}
		return
	}
	// Read one byte beyond the limit so a large conversation cannot become a
	// successful HTTP request containing a silently truncated JSON body. Treat
	// encoded request bodies as opaque bytes, including compressed bodies.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOpenAIProxyRequestBytes+1))
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, err)
		}
		return
	}
	if len(body) > maxOpenAIProxyRequestBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]any{
			"message": "Responses request body exceeds the 64 MiB limit",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "request_too_large",
		}})
		return
	}
	proxyContext, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	request, err := http.NewRequestWithContext(proxyContext, r.Method, m.apiBase.ResolveReference(&url.URL{Path: "/v1" + suffix}).String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, err)
		return
	}
	// Forward session metadata unchanged for protocol compatibility.
	// Do not persist these headers locally.
	for _, name := range []string{
		"Accept", "Content-Type", "Content-Encoding", "OpenAI-Beta", "Idempotency-Key",
		"Session-ID", "Thread-ID", "Traceparent", "Tracestate",
		"X-Codex-Turn-Metadata", "X-Codex-Turn-State",
	} {
		for _, value := range r.Header.Values(name) {
			if value != "" {
				request.Header.Add(name, value)
			}
		}
	}
	m.mu.Lock()
	token := m.accessToken
	m.mu.Unlock()
	request.Header.Set("Authorization", "Bearer "+token)
	client := m.http
	var watchdog *agentProxyWatchdog
	started := time.Now()
	if suffix != "/models" {
		client = m.agentHTTP
		watchdog = newAgentProxyWatchdog(cancel, m.proxyTimeouts.header, errAgentProxyHeaderTimeout)
		defer watchdog.stop()
	}
	response, err := client.Do(request)
	if err != nil {
		m.failAgentProxy(w, r, suffix, "headers", proxyContext, err, 0, 0, started, false)
		return
	}
	upstreamBody := &agentProxyCloseOnceBody{ReadCloser: response.Body}
	response.Body = upstreamBody
	defer upstreamBody.Close()
	if proxyContext.Err() != nil {
		m.failAgentProxy(w, r, suffix, "headers", proxyContext, proxyContext.Err(), response.StatusCode, 0, started, false)
		return
	}
	if watchdog != nil {
		watchdog.reset(m.proxyTimeouts.readIdle, errAgentProxyReadIdleTimeout)
		response.Body = &agentProxyActivityBody{ReadCloser: upstreamBody, watchdog: watchdog, timeout: m.proxyTimeouts.readIdle}
	}
	// Cancellation also closes a custom Transport's body. This makes an idle
	// read releasable without spawning a new goroutine for each streamed chunk.
	stopClose := context.AfterFunc(proxyContext, func() { _ = upstreamBody.Close() })
	defer stopClose()
	for _, name := range []string{"Content-Type", "Cache-Control", "X-Request-ID", "X-Codex-Turn-State"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if response.StatusCode == http.StatusPaymentRequired {
		errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if readErr != nil {
			m.failAgentProxy(w, r, suffix, "read", proxyContext, readErr, response.StatusCode, 0, started, false)
			return
		}
		threadID := r.Header.Get("Thread-ID")
		if threadID == "" {
			threadID = r.Header.Get("Session-ID")
		}
		if threadID == "" {
			var metadata struct {
				ThreadID string `json:"prompt_cache_key"`
			}
			if json.Unmarshal(body, &metadata) == nil {
				threadID = metadata.ThreadID
			}
		}
		m.recordAgentFailure(threadID, cloudErrorCode(response.StatusCode, errorBody))
		response.Body = io.NopCloser(io.MultiReader(bytes.NewReader(errorBody), response.Body))
	}
	m.forwardAgentProxyBody(w, r, suffix, response, proxyContext, cancel, watchdog, started)
}

func (m *Manager) DoAuthenticated(ctx context.Context, method, path string, body any, output any) error {
	return m.requestJSON(ctx, method, path, body, output, true, true)
}

func (m *Manager) DoAuthenticatedWithHeaders(ctx context.Context, method, path string, body, output any, headers map[string]string) error {
	return m.requestJSONHeaders(ctx, method, path, body, output, headers, true)
}

func (m *Manager) DownloadAuthenticated(ctx context.Context, rawURL string, target io.Writer) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ErrCloudResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrCloudResponse
	}
	_, err = io.Copy(target, io.LimitReader(response.Body, 512<<20))
	return err
}

func (m *Manager) UploadAuthenticated(ctx context.Context, rawURL string, content []byte, headers map[string]string) error {
	return m.UploadAuthenticatedJSON(ctx, rawURL, content, headers, nil)
}

// UploadAuthenticatedJSON uploads a reserved media input and, when output is
// non-nil, validates the bounded JSON acknowledgement returned by the cloud.
func (m *Manager) UploadAuthenticatedJSON(ctx context.Context, rawURL string, content []byte, headers map[string]string, output any) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != m.apiBase.Scheme || parsed.Host != m.apiBase.Host || parsed.User != nil {
		return ErrCloudResponse
	}
	if err := m.ensureAccess(ctx); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, parsed.String(), bytes.NewReader(content))
	if err != nil {
		return err
	}
	m.mu.Lock()
	token := m.accessToken
	m.mu.Unlock()
	request.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cloudHTTPStatusError(response, token)
	}
	if output != nil {
		if response.StatusCode == http.StatusNoContent {
			return ErrCloudResponse
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output); err != nil {
			return fmt.Errorf("%w: %v", ErrCloudResponse, err)
		}
	}
	return nil
}

func (m *Manager) requestJSON(ctx context.Context, method, path string, body, output any, authenticated, retry bool) error {
	return m.requestJSONHeadersInternal(ctx, method, path, body, output, nil, authenticated, retry)
}

func (m *Manager) requestJSONHeaders(ctx context.Context, method, path string, body, output any, headers map[string]string, retry bool) error {
	return m.requestJSONHeadersInternal(ctx, method, path, body, output, headers, true, retry)
}

func (m *Manager) requestJSONHeadersInternal(ctx context.Context, method, path string, body, output any, headers map[string]string, authenticated, retry bool) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	if authenticated {
		if err := m.ensureAccess(ctx); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, m.apiBase.ResolveReference(&url.URL{Path: path}).String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	token := ""
	if authenticated {
		m.mu.Lock()
		token = m.accessToken
		m.mu.Unlock()
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && authenticated && retry {
		m.clearAccess()
		return m.requestJSONHeadersInternal(ctx, method, path, body, output, headers, authenticated, false)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized && authenticated {
			return ErrLoginRequired
		}
		return cloudHTTPStatusError(response, token)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("%w: %v", ErrCloudResponse, err)
	}
	return nil
}

func cloudHTTPStatusError(response *http.Response, secrets ...string) *HTTPStatusError {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	public := generation.NewProviderHTTPError("ShotForward", response.StatusCode, response.Header, bytes.NewReader(body), secrets...)
	return &HTTPStatusError{Status: response.StatusCode, Code: cloudErrorCode(response.StatusCode, body), publicMessage: public.Error()}
}

func (m *Manager) ensureAccess(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accessToken != "" && time.Now().Add(30*time.Second).Before(m.expiresAt) {
		return nil
	}
	refresh, found, err := m.credentials.Get(ctx, refreshCredentialReference)
	if err != nil {
		return err
	}
	if !found {
		return ErrLoginRequired
	}
	var tokens Tokens
	if err = m.requestRefreshLocked(ctx, refresh, &tokens); err != nil {
		if errors.Is(err, ErrLoginRequired) {
			_, _ = m.credentials.Delete(context.Background(), refreshCredentialReference)
		}
		return err
	}
	if err = m.credentials.Put(ctx, refreshCredentialReference, tokens.RefreshToken); err != nil {
		return err
	}
	m.accessToken, m.expiresAt = tokens.AccessToken, time.Now().Add(time.Duration(tokens.ExpiresIn)*time.Second)
	return nil
}

func (m *Manager) requestRefreshLocked(ctx context.Context, refresh string, tokens *Tokens) error {
	encoded, _ := json.Marshal(map[string]string{"refresh_token": refresh})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.apiBase.ResolveReference(&url.URL{Path: "/v1/auth/refresh"}).String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return ErrLoginRequired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrCloudResponse, response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(tokens)
}

func (m *Manager) clearAccess() {
	m.mu.Lock()
	m.accessToken, m.expiresAt = "", time.Time{}
	m.mu.Unlock()
}
