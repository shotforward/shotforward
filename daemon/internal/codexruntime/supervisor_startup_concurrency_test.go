package codexruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// These fakes never start a process or contact an account. Unlike the older
// sequential lifecycle fixtures they honor cancellation and synchronize counters.
type startupFixture struct {
	mu           sync.Mutex
	versions     int
	clients      []*startupClient
	environments [][]string
	requests     map[string]int
	versionHook  func(context.Context, int) error
	requestHook  func(context.Context, int, string, map[string]any) error
	closeHook    func(context.Context, int) error
	formalHook   func(string, map[string]any) (any, error)
}

type startupClient struct {
	fixture *startupFixture
	ordinal int
	formal  bool
	exit    func(error)
	mu      sync.Mutex
	closed  bool
}

type observedWaiterContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *observedWaiterContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

func (c *startupClient) Request(ctx context.Context, method string, params, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	values, _ := params.(map[string]any)
	c.fixture.mu.Lock()
	c.fixture.requests[method]++
	c.fixture.mu.Unlock()
	if hook := c.fixture.requestHook; hook != nil {
		if err := hook(ctx, c.ordinal, method, values); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handler := standardProbe().handler
	if c.formal {
		handler = runtimeHandler(false)
		if c.fixture.formalHook != nil {
			handler = c.fixture.formalHook
		}
	}
	value, err := handler(method, values)
	if err != nil || output == nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func (c *startupClient) Notify(string, any) error { return nil }
func (c *startupClient) Close(ctx context.Context) error {
	if hook := c.fixture.closeHook; hook != nil {
		if err := hook(ctx, c.ordinal); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *startupClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func newStartupFixture(t *testing.T, fixture *startupFixture, timeout time.Duration, logger *slog.Logger) *Supervisor {
	t.Helper()
	fixture.requests = make(map[string]int)
	root := filepath.Clean(t.TempDir())
	deps := dependencies{
		discover: func(string) (string, error) { return "/test/codex", nil },
		version: func(ctx context.Context, _ string) (string, error) {
			fixture.mu.Lock()
			fixture.versions++
			count := fixture.versions
			fixture.mu.Unlock()
			if fixture.versionHook != nil {
				if err := fixture.versionHook(ctx, count); err != nil {
					return "", err
				}
			}
			return "0.160.0", nil
		},
		newClient: func(_ string, _ []string, _ string, environment []string, _ time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			client := &startupClient{fixture: fixture, ordinal: len(fixture.clients) + 1, formal: notification != nil, exit: exited}
			fixture.clients = append(fixture.clients, client)
			fixture.environments = append(fixture.environments, append([]string(nil), environment...))
			return client, nil
		},
	}
	supervisor, err := newSupervisor(Options{
		WorkingDirectory: root, SessionWorkspaceRoot: root,
		NetworkSettingsPath: filepath.Join(root, "network.json"),
		Environment:         []string{"SHOTFORWARD_MCP_TOKEN=test-sensitive-mcp-value"},
		StartupTimeout:      timeout, Logger: logger,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := supervisor.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return supervisor
}

func startupAwait(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("startup operation did not complete")
		return nil
	}
}

func startupBarrier(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup barrier was not reached")
	}
}

func TestSupervisorConcurrentInitializationRunsOnlyOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fixture := &startupFixture{versionHook: func(ctx context.Context, count int) error {
		if count != 1 {
			return errors.New("unexpected repeated initialization")
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	results := make(chan error, 20)
	for i := range 20 {
		go func() {
			if i%2 == 0 {
				_, err := supervisor.Snapshot(context.Background())
				results <- err
			} else {
				_, err := supervisor.Models(context.Background())
				results <- err
			}
		}()
	}
	startupBarrier(t, entered)
	close(release)
	for range 20 {
		if err := startupAwait(t, results); err != nil {
			t.Fatal(err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.versions != 1 || len(fixture.clients) != 3 {
		t.Fatalf("versions=%d clients=%d", fixture.versions, len(fixture.clients))
	}
	if fixture.requests["thread/start"] != 0 || fixture.requests["turn/start"] != 0 {
		t.Fatal("initialization created a Thread or Turn")
	}
}

func TestSupervisorCallerCancellationDoesNotCancelSharedInitialization(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fixture := &startupFixture{versionHook: func(ctx context.Context, _ int) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := supervisor.Snapshot(ctx); first <- err }()
	startupBarrier(t, entered)
	cancel()
	if err := startupAwait(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller error=%v", err)
	}
	otherCtx, cancelOther := context.WithCancel(context.Background())
	observed := &observedWaiterContext{Context: otherCtx, observed: make(chan struct{})}
	otherResult := make(chan error, 1)
	go func() { _, err := supervisor.Models(observed); otherResult <- err }()
	startupBarrier(t, observed.observed)
	cancelOther()
	if err := startupAwait(t, otherResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v", err)
	}
	close(release)
	if snapshot, err := supervisor.Snapshot(context.Background()); err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("shared initialization=%+v, %v", snapshot, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.versions != 1 || len(fixture.clients) != 3 {
		t.Fatalf("versions=%d clients=%d", fixture.versions, len(fixture.clients))
	}
}

func TestSupervisorSharesInitializationFailureAndBacksOffBeforeRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	failure := errors.New("controlled version failure")
	fixture := &startupFixture{versionHook: func(ctx context.Context, count int) error {
		if count != 1 {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	results := make(chan error, 10)
	for range 10 {
		go func() { _, err := supervisor.Snapshot(context.Background()); results <- err }()
	}
	startupBarrier(t, entered)
	close(release)
	for range 10 {
		if err := startupAwait(t, results); !errors.Is(err, failure) {
			t.Fatalf("shared failure=%v", err)
		}
	}
	if _, err := supervisor.Models(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("backoff failure=%v", err)
	}
	fixture.mu.Lock()
	versions := fixture.versions
	fixture.mu.Unlock()
	if versions != 1 {
		t.Fatalf("backoff retried %d times", versions)
	}
	// Expire the retry window deterministically; no wall-clock sleep is needed.
	supervisor.mu.Lock()
	supervisor.startRetryAt = time.Time{}
	supervisor.mu.Unlock()
	if snapshot, err := supervisor.Snapshot(context.Background()); err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("retry=%+v, %v", snapshot, err)
	}
}

func TestSupervisorSharedInitializationHasItsOwnDeadline(t *testing.T) {
	fixture := &startupFixture{versionHook: func(ctx context.Context, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	supervisor := newStartupFixture(t, fixture, 20*time.Millisecond, nil)
	snapshot, err := supervisor.Snapshot(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || snapshot.Health == agentapp.RuntimeStarting || snapshot.Health == agentapp.RuntimeReady {
		t.Fatalf("bounded initialization=%+v, %v", snapshot, err)
	}
}

func TestSupervisorCloseCancelsStartupAndWaitsForCleanup(t *testing.T) {
	entered, cleaning, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fixture := &startupFixture{
		requestHook: func(ctx context.Context, ordinal int, method string, _ map[string]any) error {
			if ordinal == 1 && method == "initialize" {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		closeHook: func(ctx context.Context, ordinal int) error {
			if ordinal == 1 {
				close(cleaning)
				select {
				case <-releaseCleanup:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	result, closed := make(chan error, 1), make(chan error, 1)
	go func() { _, err := supervisor.Snapshot(context.Background()); result <- err }()
	startupBarrier(t, entered)
	go func() { closed <- supervisor.Close(context.Background()) }()
	startupBarrier(t, cleaning)
	select {
	case err := <-closed:
		t.Fatalf("Close returned before startup cleanup: %v", err)
	default:
	}
	close(releaseCleanup)
	if err := startupAwait(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := startupAwait(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed initialization error=%v", err)
	}
	if _, err := supervisor.Snapshot(context.Background()); !errors.Is(err, errSupervisorClosed) {
		t.Fatalf("Snapshot after terminal Close=%v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.clients) != 1 || !fixture.clients[0].isClosed() {
		t.Fatal("cancelled startup left a client alive")
	}
}

func TestSupervisorConfigureNetworkFencesStartupAndUsesNewEnvironment(t *testing.T) {
	entered := make(chan struct{})
	fixture := &startupFixture{requestHook: func(ctx context.Context, ordinal int, method string, _ map[string]any) error {
		if ordinal == 1 && method == "initialize" {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	result := make(chan error, 1)
	go func() { _, err := supervisor.Snapshot(context.Background()); result <- err }()
	startupBarrier(t, entered)
	settings := agentapp.RuntimeNetworkSettings{ProxyURL: "http://127.0.0.1:7890"}
	if err := supervisor.ConfigureNetwork(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := startupAwait(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("old generation error=%v", err)
	}
	if snapshot, err := supervisor.Snapshot(context.Background()); err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("new generation=%+v, %v", snapshot, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.versions != 2 || len(fixture.clients) != 4 || !fixture.clients[0].isClosed() {
		t.Fatalf("versions=%d clients=%d", fixture.versions, len(fixture.clients))
	}
	for _, environment := range fixture.environments[1:] {
		if !slices.Contains(environment, "HTTPS_PROXY=http://127.0.0.1:7890") {
			t.Fatal("new generation did not receive the updated proxy")
		}
	}
}

func TestSupervisorOldClientExitCannotDisconnectReplacement(t *testing.T) {
	fixture := &startupFixture{}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	if _, err := supervisor.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	old := fixture.clients[2]
	fixture.mu.Unlock()
	if err := supervisor.ConfigureNetwork(context.Background(), agentapp.RuntimeNetworkSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	old.exit(errors.New("late old-process exit"))
	if snapshot, err := supervisor.Snapshot(context.Background()); err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("replacement after stale exit=%+v, %v", snapshot, err)
	}
}

func startupThreadRequest(t *testing.T, supervisor *Supervisor) agentapp.ThreadRequest {
	t.Helper()
	workspace := filepath.Join(supervisor.options.SessionWorkspaceRoot, "session-1")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return agentapp.ThreadRequest{AgentSessionID: agent.SessionID(testAgentSessionID), ProjectID: project.ProjectID(testProjectID),
		EpisodeID: project.EpisodeID(testEpisodeID), ModelID: "gpt-test", WorkspacePath: workspace,
		MCPBindingURL: "http://127.0.0.1:4321/mcp/test-private-route"}
}

func TestSupervisorStartThreadCannotReturnUnverifiedScope(t *testing.T) {
	base := runtimeHandler(false)
	fixture := &startupFixture{formalHook: func(method string, params map[string]any) (any, error) {
		if method == "mcpServer/tool/call" {
			return map[string]any{"isError": false, "structuredContent": map[string]any{
				"session_id": testAgentSessionID, "project_id": "wrong-project", "episode_id": testEpisodeID}}, nil
		}
		return base(method, params)
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	thread, err := supervisor.StartThread(context.Background(), startupThreadRequest(t, supervisor))
	if err == nil || !strings.Contains(err.Error(), "scope mismatch") || thread.CodexThreadID != "" {
		t.Fatalf("unverified Thread=%+v, %v", thread, err)
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if len(supervisor.threadWorkspaces) != 0 {
		t.Fatal("unverified Thread was cached")
	}
}

func TestSupervisorStartThreadWaitsForScopeVerification(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fixture := &startupFixture{requestHook: func(ctx context.Context, _ int, method string, _ map[string]any) error {
		if method == "mcpServer/tool/call" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	request := startupThreadRequest(t, supervisor)
	result := make(chan error, 1)
	go func() { _, err := supervisor.StartThread(context.Background(), request); result <- err }()
	startupBarrier(t, entered)
	select {
	case err := <-result:
		t.Fatalf("StartThread returned before scope verification: %v", err)
	default:
	}
	close(release)
	if err := startupAwait(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorThreadFromOldGenerationCannotBePublishedAfterNetworkReset(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fixture := &startupFixture{requestHook: func(ctx context.Context, ordinal int, method string, _ map[string]any) error {
		if ordinal == 3 && method == "mcpServer/tool/call" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	request := startupThreadRequest(t, supervisor)
	result := make(chan error, 1)
	go func() { _, err := supervisor.StartThread(context.Background(), request); result <- err }()
	startupBarrier(t, entered)
	if err := supervisor.ConfigureNetwork(context.Background(), agentapp.RuntimeNetworkSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := startupAwait(t, result); !errors.Is(err, errSupervisorStopping) {
		t.Fatalf("old-generation Thread returned successfully: %v", err)
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if len(supervisor.threadWorkspaces) != 0 {
		t.Fatal("old-generation Thread was added to replacement Runtime")
	}
}

func TestSupervisorPhaseDiagnosticsDoNotContainSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	fixture := &startupFixture{}
	supervisor := newStartupFixture(t, fixture, 0, logger)
	if _, err := supervisor.StartThread(context.Background(), startupThreadRequest(t, supervisor)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, phase := range []string{"version", "config_read", "mcp_extension_list", "sandbox_preflight", "formal_initialize", "account_read", "model_list", "thread/start", "mcp_scope_verify"} {
		if !strings.Contains(text, `"phase":"`+phase+`"`) {
			t.Errorf("missing phase %s", phase)
		}
	}
	for _, sensitive := range []string{"test-sensitive-mcp-value", "test-private-route", "requiresOpenaiAuth", "structuredContent", "bearer_token_env_var"} {
		if strings.Contains(text, sensitive) {
			t.Errorf("diagnostics contain %s", sensitive)
		}
	}
}

func TestSupervisorAlreadyCancelledCallerDoesNotStartInitialization(t *testing.T) {
	var entered atomic.Bool
	fixture := &startupFixture{versionHook: func(context.Context, int) error { entered.Store(true); return nil }}
	supervisor := newStartupFixture(t, fixture, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := supervisor.Snapshot(ctx); !errors.Is(err, context.Canceled) || entered.Load() {
		t.Fatalf("already cancelled caller started work: %v", err)
	}
}

func TestSupervisorLoginOverlappingCloseOrNetworkResetNeverUsesNilClient(t *testing.T) {
	for _, operation := range []string{"close", "reset"} {
		t.Run(operation, func(t *testing.T) {
			fixture := &startupFixture{}
			supervisor := newStartupFixture(t, fixture, 0, nil)
			if _, err := supervisor.Snapshot(context.Background()); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 32)
			for range 32 {
				go func() {
					<-start
					challenge, err := supervisor.StartLogin(context.Background(), agentapp.LoginChatGPTDeviceCode)
					if err == nil && challenge.LoginID == "" {
						err = errors.New("login returned an empty challenge")
					}
					results <- err
				}()
			}
			close(start)
			var err error
			if operation == "close" {
				err = supervisor.Close(context.Background())
			} else {
				err = supervisor.ConfigureNetwork(context.Background(), agentapp.RuntimeNetworkSettings{ProxyURL: "http://127.0.0.1:7890"})
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 32 {
				err := startupAwait(t, results)
				if err != nil && !errors.Is(err, errSupervisorClosed) && !errors.Is(err, errSupervisorStopping) && !errors.Is(err, context.Canceled) {
					t.Fatalf("unexpected overlapping login error: %v", err)
				}
			}
		})
	}
}
