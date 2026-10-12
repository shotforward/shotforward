package shotforwardcloud

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type agentModelResult struct {
	models []agentapp.Model
	err    error
}

func modelCacheManager(t *testing.T, transport roundTripFunc) *Manager {
	t.Helper()
	manager, err := NewManager("https://api.example", &memoryCredentials{}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken, manager.expiresAt = "test-access", time.Now().Add(time.Hour)
	return manager
}

func modelResponse(id string) *http.Response {
	return jsonResponse(http.StatusOK, map[string]any{"data": []Model{{ID: id, ProductKind: "responses"}}})
}

func waitModelFlight(t *testing.T, manager *Manager, waiters int) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		manager.modelMu.Lock()
		ready := manager.modelFlight != nil && manager.modelFlight.waiters == waiters
		manager.modelMu.Unlock()
		if ready {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("model flight did not reach %d waiters", waiters)
		case <-ticker.C:
		}
	}
}

func receiveModelResult(t *testing.T, results <-chan agentModelResult) agentModelResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("model lookup did not finish")
		return agentModelResult{}
	}
}

func TestAgentModelsColdReadsShareOneCancelableFlight(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var requests atomic.Int32
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		close(started)
		select {
		case <-release:
			return modelResponse("shared-model"), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	leaderCtx, cancelLeader := context.WithCancel(ctx)
	leader, follower := make(chan agentModelResult, 1), make(chan agentModelResult, 1)
	go func() { values, err := manager.AgentModels(leaderCtx); leader <- agentModelResult{values, err} }()
	<-started
	go func() { values, err := manager.AgentModels(ctx); follower <- agentModelResult{values, err} }()
	waitModelFlight(t, manager, 2)
	cancelLeader()
	if result := receiveModelResult(t, leader); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled leader = %+v", result)
	}
	manager.modelMu.Lock()
	running := manager.modelFlight != nil && !manager.modelFlight.finished
	manager.modelMu.Unlock()
	if !running || requests.Load() != 1 {
		t.Fatal("one canceled caller terminated the follower's shared lookup")
	}
	// End the mock request without cancelling the remaining caller.
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result := receiveModelResult(t, follower); result.err != nil || len(result.models) != 1 || result.models[0].ID != "shared-model" || requests.Load() != 1 {
		t.Fatalf("shared follower = %+v; requests=%d", result, requests.Load())
	}
}

func TestAgentModelsLoginInvalidatesOldAccountFlight(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var requests atomic.Int32
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/auth/desktop-code/exchanges" {
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "new-account-access", RefreshToken: "new-account-refresh", ExpiresIn: 900}), nil
		}
		if requests.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-request.Context().Done():
				// Simulate a late successful response despite request cancellation.
			}
			return modelResponse("old-account-model"), nil
		}
		if request.Header.Get("Authorization") != "Bearer new-account-access" {
			t.Error("new catalog lookup did not use the new account")
		}
		return modelResponse("new-account-model"), nil
	})
	results := make(chan agentModelResult, 1)
	go func() {
		values, err := manager.AgentModels(context.Background())
		results <- agentModelResult{values, err}
	}()
	<-started
	if err := manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	if result := receiveModelResult(t, results); !errors.Is(result.err, ErrLoginRequired) || len(result.models) != 0 {
		t.Fatalf("old account lookup survived new login: %+v", result)
	}
	values, err := manager.AgentModels(context.Background())
	if err != nil || len(values) != 1 || values[0].ID != "new-account-model" || requests.Load() != 2 {
		t.Fatalf("new account models = %+v, %v", values, err)
	}
}

func TestAgentModelsLastCanceledWaiterCancelsRequestAndNextReadRetries(t *testing.T) {
	t.Parallel()
	started, canceled := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(started)
			<-request.Context().Done()
			close(canceled)
			return nil, request.Context().Err()
		}
		return modelResponse("retry-model"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan agentModelResult, 1)
	go func() { values, err := manager.AgentModels(ctx); results <- agentModelResult{values, err} }()
	<-started
	cancel()
	if result := receiveModelResult(t, results); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled final waiter = %+v", result)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("unused HTTP request was not canceled")
	}
	values, err := manager.AgentModels(context.Background())
	if err != nil || len(values) != 1 || values[0].ID != "retry-model" || requests.Load() != 2 {
		t.Fatalf("retry = %+v, %v; requests=%d", values, err, requests.Load())
	}
}

func TestAgentModelsWarmReadsStayFastDuringFailedRefreshAndNeverServeExpiredCache(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			return modelResponse("cached-model"), nil
		case 2:
			close(started)
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{}), nil
	})
	first, err := manager.AgentModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first[0].ID = "caller-mutated-model"
	results := make(chan agentModelResult, 1)
	go func() {
		values, err := manager.RefreshAgentModels(context.Background())
		results <- agentModelResult{values, err}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	warm, err := manager.AgentModels(ctx)
	if err != nil || len(warm) != 1 || warm[0].ID != "cached-model" {
		t.Fatalf("cached read waited behind refresh or was mutated: %+v, %v", warm, err)
	}
	close(release)
	if result := receiveModelResult(t, results); !errors.Is(result.err, ErrCloudResponse) {
		t.Fatalf("failed refresh = %+v", result)
	}
	warm, err = manager.AgentModels(context.Background())
	if err != nil || len(warm) != 1 || warm[0].ID != "cached-model" || requests.Load() != 2 {
		t.Fatalf("failed refresh discarded an unexpired cache: %+v, %v", warm, err)
	}
	manager.modelMu.Lock()
	manager.modelsUntil = time.Now().Add(-time.Second)
	manager.modelMu.Unlock()
	if values, err := manager.AgentModels(context.Background()); err == nil || len(values) != 0 || requests.Load() != 3 {
		t.Fatalf("expired cache was served indefinitely: %+v, %v", values, err)
	}
}

func TestAgentModelsLogoutInvalidatesInFlightResponseWithoutWaitingForNetwork(t *testing.T) {
	t.Parallel()
	started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		close(started)
		// Deliberately ignore cancellation to prove an old response cannot publish
		// even if a transport or remote peer finishes after ForgetSession.
		<-release
		close(returned)
		return modelResponse("old-account-model"), nil
	})
	results := make(chan agentModelResult, 1)
	go func() {
		values, err := manager.AgentModels(context.Background())
		results <- agentModelResult{values, err}
	}()
	<-started
	forgot := make(chan error, 1)
	go func() { forgot <- manager.ForgetSession(context.Background()) }()
	select {
	case err := <-forgot:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("logout waited behind the catalog network request")
	}
	if result := receiveModelResult(t, results); !errors.Is(result.err, ErrLoginRequired) || len(result.models) != 0 {
		t.Fatalf("old session response was returned after logout: %+v", result)
	}
	close(release)
	<-returned
	manager.modelMu.Lock()
	if len(manager.agentModels) != 0 || !manager.modelsUntil.IsZero() {
		t.Errorf("old account catalog was published after logout: %+v", manager.agentModels)
	}
	manager.modelMu.Unlock()
	if values, err := manager.AgentModels(context.Background()); !errors.Is(err, ErrLoginRequired) || len(values) != 0 {
		t.Fatalf("signed-out model read = %+v, %v", values, err)
	}
}

func TestBackgroundAgentModelsSkipAbsentExpiredOrBusyAuthentication(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	manager := modelCacheManager(t, func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return modelResponse("available-model"), nil
	})
	manager.accessToken = ""
	if _, ran, err := manager.RefreshAgentModelsIfSignedIn(context.Background()); ran || err != nil {
		t.Fatalf("signed-out background refresh = %v, %v", ran, err)
	}
	manager.accessToken, manager.expiresAt = "expired-token", time.Now().Add(-time.Second)
	if _, ran, err := manager.RefreshAgentModelsIfSignedIn(context.Background()); ran || err != nil {
		t.Fatalf("expired background refresh = %v, %v", ran, err)
	}
	manager.accessToken, manager.expiresAt = "valid-token", time.Now().Add(time.Hour)
	manager.mu.Lock()
	if _, ran, err := manager.RefreshAgentModelsIfSignedIn(context.Background()); ran || err != nil {
		manager.mu.Unlock()
		t.Fatalf("busy authentication background refresh = %v, %v", ran, err)
	}
	manager.mu.Unlock()
	if requests.Load() != 0 {
		t.Fatal("background inspection tried to restore credentials")
	}
	values, ran, err := manager.RefreshAgentModelsIfSignedIn(context.Background())
	if !ran || err != nil || len(values) != 1 || requests.Load() != 1 {
		t.Fatalf("signed-in background refresh = %+v, %v, %v", values, ran, err)
	}
}

type modelCacheCredentials struct {
	memoryCredentials
	reads  atomic.Int32
	writes atomic.Int32
}

func (s *modelCacheCredentials) Get(ctx context.Context, reference string) (string, bool, error) {
	s.reads.Add(1)
	return s.memoryCredentials.Get(ctx, reference)
}
func (s *modelCacheCredentials) Put(ctx context.Context, reference, value string) error {
	s.writes.Add(1)
	return s.memoryCredentials.Put(ctx, reference, value)
}

func TestBackgroundAgentModelsUnauthorizedDoesNotRefreshStoredCredentials(t *testing.T) {
	t.Parallel()
	credentials := &modelCacheCredentials{memoryCredentials: memoryCredentials{value: "stored-refresh"}}
	var requests atomic.Int32
	manager, err := NewManager("https://api.example", credentials, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer existing-access" {
			t.Errorf("background lookup attempted authentication instead of using its existing session: %s", request.URL.Path)
		}
		return jsonResponse(http.StatusUnauthorized, map[string]any{}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	manager.accessToken, manager.expiresAt = "existing-access", time.Now().Add(time.Hour)
	values, ran, err := manager.RefreshAgentModelsIfSignedIn(context.Background())
	if !ran || !errors.Is(err, ErrLoginRequired) || len(values) != 0 || requests.Load() != 1 || credentials.reads.Load() != 0 || credentials.writes.Load() != 0 {
		t.Fatalf("background 401 = %+v, %v, %v; requests=%d credential reads/writes=%d/%d", values, ran, err, requests.Load(), credentials.reads.Load(), credentials.writes.Load())
	}
}

func TestBackgroundAgentModelsAfterSessionSnapshotNeverWaitForAuthenticationMutex(t *testing.T) {
	t.Parallel()
	started, canceled := make(chan struct{}), make(chan struct{})
	manager := modelCacheManager(t, func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		close(canceled)
		return nil, request.Context().Err()
	})
	// This is the boundary immediately after the background entry check. A
	// foreground auth operation may take the mutex before the HTTP fetch starts.
	session := manager.backgroundAgentModelSession()
	if session == nil {
		t.Fatal("ready background session was not captured")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan agentModelResult, 1)
	go func() {
		values, err := manager.agentModelCatalogForSession(ctx, true, session)
		results <- agentModelResult{values, err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background fetch waited on authentication after its entry check")
	}
	cancel()
	if result := receiveModelResult(t, results); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("background cancellation while auth was busy = %+v", result)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("background network cleanup waited on authentication")
	}
}
