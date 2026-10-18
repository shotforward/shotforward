package agenthttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

type compactionHTTPSessions struct {
	SessionCommands
	value agentapp.SessionSnapshot
	err   error
	reads atomic.Int32
}

func (s *compactionHTTPSessions) Snapshot(context.Context, agent.SessionID) (agentapp.SessionSnapshot, error) {
	s.reads.Add(1)
	return s.value, s.err
}

type compactionHTTPCommands struct {
	TurnCommands
	started chan agent.SessionID
	finish  chan error
	active  atomic.Bool
	calls   atomic.Int32
}

func (c *compactionHTTPCommands) CompactSession(ctx context.Context, sessionID agent.SessionID) error {
	if !c.active.CompareAndSwap(false, true) {
		return agentapp.ErrContextCompactionBusy
	}
	c.calls.Add(1)
	defer c.active.Store(false)
	c.started <- sessionID
	select {
	case err := <-c.finish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *compactionHTTPCommands) IsContextCompacting(agent.SessionID) bool {
	return c.active.Load()
}

func newCompactionHTTPFixture(t *testing.T) (*Handler, *compactionHTTPSessions, *compactionHTTPCommands) {
	t.Helper()
	api := &fakeAPI{}
	sessions := &compactionHTTPSessions{SessionCommands: fakeSessions{api}, value: agentapp.SessionSnapshot{Session: testSessionValue()}}
	commands := &compactionHTTPCommands{TurnCommands: api, started: make(chan agent.SessionID, 1), finish: make(chan error, 1)}
	handler, err := NewHandler(Options{
		Runtime: api, Sessions: sessions, Turns: commands, ChangeSets: api, Events: fakeEvents{},
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, sessions, commands
}

func compactionHTTPPath() string {
	return "/api/v1/agent-sessions/" + testSession.String() + "/compact"
}

func readCompactionHTTPResponse(t *testing.T, result <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-result:
		return response
	case <-time.After(10 * time.Second):
		t.Fatal("compaction HTTP request did not finish")
		return nil
	}
}

func TestContextCompactionHTTPWaitsForCompletionAndExposesSnapshotState(t *testing.T) {
	t.Parallel()
	handler, sessions, commands := newCompactionHTTPFixture(t)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), strings.NewReader(`{}`)))
		result <- response
	}()
	select {
	case sessionID := <-commands.started:
		if sessionID != testSession || sessions.reads.Load() == 0 {
			t.Fatal("compaction skipped restored/scoped Session snapshot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP compaction did not start")
	}
	select {
	case response := <-result:
		t.Fatalf("premature compaction result: %s", response.Body.String())
	default:
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/snapshot", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"context_compacting":true`) {
		t.Fatalf("active compaction snapshot: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"context_compaction_busy"`) {
		t.Fatalf("duplicate compaction: %d %s", response.Code, response.Body.String())
	}
	commands.finish <- nil
	response = readCompactionHTTPResponse(t, result)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"context_compacted":true`) || strings.Contains(response.Body.String(), "private-thread") {
		t.Fatalf("completed compaction: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/snapshot", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"context_compacting":false`) {
		t.Fatalf("completed compaction snapshot: %d %s", response.Code, response.Body.String())
	}
}

func TestContextCompactionHTTPRequiresIdleActiveScopedSession(t *testing.T) {
	tests := []struct {
		name   string
		status int
		change func(*compactionHTTPSessions)
	}{
		{"not found", http.StatusNotFound, func(s *compactionHTTPSessions) { s.err = agentsession.ErrSessionNotFound }},
		{"restore failed", http.StatusServiceUnavailable, func(s *compactionHTTPSessions) { s.err = agentsession.ErrRuntimeNotReady }},
		{"archived", http.StatusConflict, func(s *compactionHTTPSessions) { s.value.Session.Status = agent.SessionArchived }},
		{"scope mismatch", http.StatusConflict, func(s *compactionHTTPSessions) {
			s.value.Session.ID = agent.SessionID("ags_" + strings.Repeat("B", 26))
		}},
		{"queued", http.StatusConflict, func(s *compactionHTTPSessions) { s.value.Turns = []agent.AgentTurn{testTurnValue()} }},
		{"running", http.StatusConflict, func(s *compactionHTTPSessions) {
			turn := testTurnValue()
			turn.Status = agent.TurnRunning
			s.value.Turns = []agent.AgentTurn{turn}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, sessions, commands := newCompactionHTTPFixture(t)
			test.change(sessions)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), nil))
			if response.Code != test.status || commands.calls.Load() != 0 {
				t.Fatalf("blocked compaction: %d %s calls=%d", response.Code, response.Body.String(), commands.calls.Load())
			}
			assertApplicationError(t, response.Body.Bytes())
		})
	}
}

func TestContextCompactionHTTPRejectsInvalidAndUnauthorizedRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		change func(*http.Request)
	}{
		{"bearer", http.StatusUnauthorized, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"csrf", http.StatusForbidden, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"origin", http.StatusForbidden, func(r *http.Request) { r.Header.Set("Origin", "https://example.com") }},
		{"method", http.StatusMethodNotAllowed, func(r *http.Request) { r.Method = http.MethodGet }},
		{"session ID", http.StatusBadRequest, func(r *http.Request) { r.URL.Path = "/api/v1/agent-sessions/invalid/compact" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, sessions, commands := newCompactionHTTPFixture(t)
			request := authorizedRequest(http.MethodPost, compactionHTTPPath(), nil)
			test.change(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || commands.calls.Load() != 0 || sessions.reads.Load() != 0 {
				t.Fatalf("invalid compaction: %d %s", response.Code, response.Body.String())
			}
		})
	}
	t.Run("unknown body field", func(t *testing.T) {
		handler, _, commands := newCompactionHTTPFixture(t)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), strings.NewReader(`{"delete_history":true}`)))
		if response.Code != http.StatusBadRequest || commands.calls.Load() != 0 {
			t.Fatalf("unknown compaction body: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestContextCompactionHTTPMapsSafeFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unsupported", agentapp.ErrContextCompactionUnavailable, http.StatusNotImplemented, "context_compaction_unavailable"},
		{"busy", agentapp.ErrContextCompactionBusy, http.StatusConflict, "context_compaction_busy"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "context_compaction_timeout"},
		{"cancelled", context.Canceled, http.StatusGatewayTimeout, "context_compaction_timeout"},
		{"upstream failure", errors.New("private native payload"), http.StatusBadGateway, "context_compaction_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, _, commands := newCompactionHTTPFixture(t)
			commands.finish <- test.err
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), nil))
			var body struct{ Error applicationError }
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != test.status || body.Error.Code != test.code || strings.Contains(response.Body.String(), "private native payload") {
				t.Fatalf("mapped compaction failure: %d %s err=%v", response.Code, response.Body.String(), err)
			}
			if commands.active.Load() {
				t.Fatal("failed compaction retained HTTP fake reservation")
			}
			assertApplicationError(t, response.Body.Bytes())
		})
	}
	t.Run("missing optional scheduler capability", func(t *testing.T) {
		handler, _, commands := newCompactionHTTPFixture(t)
		handler.turns = commands.TurnCommands
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authorizedRequest(http.MethodPost, compactionHTTPPath(), nil))
		if response.Code != http.StatusNotImplemented || !strings.Contains(response.Body.String(), `"code":"context_compaction_unavailable"`) {
			t.Fatalf("missing capability: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestContextCompactionHTTPPropagatesRequestCancellation(t *testing.T) {
	t.Parallel()
	handler, _, commands := newCompactionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := authorizedRequest(http.MethodPost, compactionHTTPPath(), nil).WithContext(ctx)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response
	}()
	select {
	case <-commands.started:
	case <-time.After(10 * time.Second):
		t.Fatal("compaction did not start")
	}
	cancel()
	response := readCompactionHTTPResponse(t, result)
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), `"code":"context_compaction_timeout"`) || commands.active.Load() {
		t.Fatalf("cancelled compaction: %d %s", response.Code, response.Body.String())
	}
}
