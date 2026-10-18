package codexruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type nativeCompactionInterrupt struct{ threadID, turnID string }

type nativeCompactionFixture struct {
	supervisor *Supervisor
	client     *fakeProtocolClient
	requests   chan string
	interrupts chan nativeCompactionInterrupt
}

func newNativeCompactionFixture(t *testing.T) nativeCompactionFixture {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	clients := []*fakeProtocolClient{}
	supervisor, err := newSupervisor(Options{WorkingDirectory: root, SessionWorkspaceRoot: root}, fakeDependencies(&clients))
	if err != nil {
		t.Fatal(err)
	}
	fixture := nativeCompactionFixture{
		supervisor: supervisor, requests: make(chan string, 16), interrupts: make(chan nativeCompactionInterrupt, 16),
	}
	client := &fakeProtocolClient{handler: func(method string, params map[string]any) (any, error) {
		switch method {
		case "thread/compact/start":
			fixture.requests <- params["threadId"].(string)
			return map[string]any{}, nil
		case "turn/interrupt":
			fixture.interrupts <- nativeCompactionInterrupt{params["threadId"].(string), params["turnId"].(string)}
			return map[string]any{}, nil
		case "turn/start":
			return map[string]any{"turn": map[string]any{"id": "user-" + params["threadId"].(string)}}, nil
		default:
			return nil, errors.New("unexpected mock method: " + method)
		}
	}}
	fixture.client = client
	client.onNotification, client.onExit = supervisor.onNotification, supervisor.onExit
	supervisor.client = client
	supervisor.snapshot.Health = agentapp.RuntimeReady
	for _, threadID := range []string{"thread-a", "thread-b"} {
		workspace := filepath.Join(root, threadID)
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		supervisor.threadWorkspaces[threadID] = workspace
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	return fixture
}

func startNativeCompaction(ctx context.Context, supervisor *Supervisor, threadID string) <-chan error {
	result := make(chan error, 1)
	go func() { result <- supervisor.CompactThread(ctx, threadID) }()
	return result
}

func awaitNativeCompactionRequest(t *testing.T, fixture nativeCompactionFixture, threadID string) {
	t.Helper()
	select {
	case got := <-fixture.requests:
		if got != threadID {
			t.Fatalf("native compaction requested %q, want %q", got, threadID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native compaction request did not arrive")
	}
}

func awaitNativeCompactionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("native compaction did not finish")
		return nil
	}
}

func assertNativeCompactionWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("compaction finished before native terminal notification: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func emitNativeCompactionTurn(client *fakeProtocolClient, method, threadID, turnID, status string) {
	client.emit(method, map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status}})
}

func TestNativeCompactionWaitsForTerminalAndSuppressesMaintenanceChatEvents(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := fixture.supervisor.Subscribe(ctx, "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	result := startNativeCompaction(ctx, fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	assertNativeCompactionWaiting(t, result)
	if !fixture.supervisor.ContextCompactionInProgress("thread-a") {
		t.Fatal("RPC ACK released the native fence")
	}
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "maintenance-a", "inProgress")
	fixture.client.emit("item/started", map[string]any{"threadId": "thread-a", "turnId": "maintenance-a", "item": map[string]any{"id": "private-item", "type": "reasoning"}})
	fixture.client.emit("item/agentMessage/delta", map[string]any{"threadId": "thread-a", "turnId": "maintenance-a", "itemId": "private-message", "delta": "private maintenance output"})
	fixture.client.emit("thread/compacted", map[string]any{"threadId": "thread-a"})
	assertNativeCompactionWaiting(t, result)
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "maintenance-a", "completed")
	if err := awaitNativeCompactionResult(t, result); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.ContextCompactionInProgress("thread-a") {
		t.Fatal("terminal success retained the native fence")
	}
	select {
	case event := <-events:
		t.Fatalf("maintenance leaked a chat event: %+v", event)
	default:
	}
	fixture.supervisor.mu.Lock()
	active := fixture.supervisor.activeTurns["thread-a"]
	fixture.supervisor.mu.Unlock()
	if active != "" {
		t.Fatalf("maintenance claimed a user Turn: %q", active)
	}
}

func TestNativeCompactionTerminalFailureIsNotSuccess(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			fixture := newNativeCompactionFixture(t)
			result := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
			awaitNativeCompactionRequest(t, fixture, "thread-a")
			emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "maintenance", "inProgress")
			emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "maintenance", status)
			if err := awaitNativeCompactionResult(t, result); err == nil {
				t.Fatal("failed native maintenance reported success")
			}
			if fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("failed native maintenance retained its fence")
			}
		})
	}
}

func TestNativeCompactionKeepsThreadsIndependent(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	otherEvents, err := fixture.supervisor.Subscribe(ctx, "thread-b")
	if err != nil {
		t.Fatal(err)
	}
	resultA := startNativeCompaction(ctx, fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "maintenance-a", "inProgress")
	fixture.client.emit("item/agentMessage/delta", map[string]any{"threadId": "thread-b", "turnId": "other-chat", "itemId": "message", "delta": "other thread answer"})
	select {
	case event := <-otherEvents:
		if event.CodexThreadID != "thread-b" || event.TextDelta != "other thread answer" {
			t.Fatalf("other Thread event changed: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("compaction swallowed another Thread's chat")
	}
	resultB := startNativeCompaction(ctx, fixture.supervisor, "thread-b")
	awaitNativeCompactionRequest(t, fixture, "thread-b")
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-b", "maintenance-b", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-b", "maintenance-b", "completed")
	if err := awaitNativeCompactionResult(t, resultB); err != nil {
		t.Fatal(err)
	}
	assertNativeCompactionWaiting(t, resultA)
	if !fixture.supervisor.ContextCompactionInProgress("thread-a") {
		t.Fatal("another Thread released the active fence")
	}
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "maintenance-a", "completed")
	if err := awaitNativeCompactionResult(t, resultA); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-otherEvents:
		t.Fatalf("Thread B maintenance leaked a chat event: %+v", event)
	default:
	}
}

func TestNativeCompactionRejectsBusyAndUnknownThreads(t *testing.T) {
	for _, active := range []string{"running-turn", "pending-turn"} {
		t.Run(active, func(t *testing.T) {
			fixture := newNativeCompactionFixture(t)
			if active == "running-turn" {
				fixture.supervisor.activeTurns["thread-a"] = "busy"
			} else {
				fixture.supervisor.pendingTurns["thread-a"] = &pendingTurnStart{}
			}
			if err := fixture.supervisor.CompactThread(context.Background(), "thread-a"); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
				t.Fatalf("busy compaction = %v", err)
			}
			select {
			case <-fixture.requests:
				t.Fatal("busy Thread reached native compaction")
			default:
			}
		})
	}
	fixture := newNativeCompactionFixture(t)
	for _, threadID := range []string{"", "unbound-thread"} {
		if err := fixture.supervisor.CompactThread(context.Background(), threadID); err == nil {
			t.Fatalf("invalid Thread %q compacted", threadID)
		}
	}
	if err := fixture.supervisor.CompactThread(nil, "thread-a"); err == nil {
		t.Fatal("nil context compacted")
	}
}

func TestNativeCompactionRPCRejectionReleasesFence(t *testing.T) {
	for _, code := range []int{-32601, -32602} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			fixture := newNativeCompactionFixture(t)
			base := fixture.client.handler
			fixture.client.handler = func(method string, params map[string]any) (any, error) {
				if method == "thread/compact/start" {
					return nil, &rpcError{Method: method, Code: code, Message: "mock rejected"}
				}
				return base(method, params)
			}
			err := fixture.supervisor.CompactThread(context.Background(), "thread-a")
			if err == nil || (code == -32601 && !errors.Is(err, agentapp.ErrContextCompactionUnavailable)) {
				t.Fatalf("native rejection = %v", err)
			}
			if fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("definite rejection retained the fence")
			}
			if _, err := fixture.supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thread-a", ModelID: "test-model", Text: "continue"}); err != nil {
				t.Fatalf("chat after rejected compaction = %v", err)
			}
		})
	}
}

func TestNativeCompactionCancelledWaitFencesLateNativeStart(t *testing.T) {
	for _, when := range []string{"before-ack", "before-native-start", "after-native-start"} {
		t.Run(when, func(t *testing.T) {
			fixture := newNativeCompactionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			eventContext, stopEvents := context.WithCancel(context.Background())
			defer stopEvents()
			events, err := fixture.supervisor.Subscribe(eventContext, "thread-a")
			if err != nil {
				t.Fatal(err)
			}
			if when == "before-ack" {
				base := fixture.client.handler
				fixture.client.handler = func(method string, params map[string]any) (any, error) {
					if method == "thread/compact/start" {
						fixture.requests <- params["threadId"].(string)
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return base(method, params)
				}
			}
			result := startNativeCompaction(ctx, fixture.supervisor, "thread-a")
			awaitNativeCompactionRequest(t, fixture, "thread-a")
			if when == "after-native-start" {
				emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "late-maintenance", "inProgress")
			}
			cancel()
			if err := awaitNativeCompactionResult(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled wait = %v", err)
			}
			if !fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("cancelled wait released a possibly-running native task")
			}
			if err := fixture.supervisor.CompactThread(context.Background(), "thread-a"); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
				t.Fatalf("overlapping compact = %v", err)
			}
			if _, err := fixture.supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thread-a", ModelID: "test-model", Text: "must wait"}); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
				t.Fatalf("chat overlapped compaction: %v", err)
			}
			if err := fixture.supervisor.SteerTurnWithImages(context.Background(), "thread-a", "late-maintenance", "must wait", nil); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
				t.Fatalf("steer overlapped compaction: %v", err)
			}
			if when != "after-native-start" {
				emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "late-maintenance", "inProgress")
			}
			select {
			case interrupt := <-fixture.interrupts:
				if interrupt.threadID != "thread-a" || interrupt.turnID != "late-maintenance" {
					t.Fatalf("wrong maintenance interrupted: %+v", interrupt)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("late maintenance was not interrupted after cancellation")
			}
			if !fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("interrupt ACK released the fence before native terminal")
			}
			emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "late-maintenance", "interrupted")
			if fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("native terminal did not release cancellation fence")
			}
			select {
			case event := <-events:
				t.Fatalf("cancelled maintenance leaked chat output: %+v", event)
			default:
			}
		})
	}
}

func TestNativeCompactionLostACKKeepsFenceUntilNativeTerminal(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	base := fixture.client.handler
	fixture.client.handler = func(method string, params map[string]any) (any, error) {
		if method == "thread/compact/start" {
			return nil, errors.New("mock transport lost ACK")
		}
		return base(method, params)
	}
	if err := fixture.supervisor.CompactThread(context.Background(), "thread-a"); err == nil {
		t.Fatal("lost ACK reported success")
	}
	if !fixture.supervisor.ContextCompactionInProgress("thread-a") {
		t.Fatal("ambiguous transport failure released native fence")
	}
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "late-maintenance", "inProgress")
	select {
	case <-fixture.interrupts:
	case <-time.After(2 * time.Second):
		t.Fatal("ambiguous native task was not interrupted")
	}
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "late-maintenance", "interrupted")
	if fixture.supervisor.ContextCompactionInProgress("thread-a") {
		t.Fatal("native terminal retained lost-ACK fence")
	}
}

func TestNativeCompactionExitAndStopFinishWaiters(t *testing.T) {
	for _, mode := range []string{"exit", "close", "restart-stop"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newNativeCompactionFixture(t)
			result := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
			awaitNativeCompactionRequest(t, fixture, "thread-a")
			switch mode {
			case "exit":
				fixture.supervisor.onExit(errors.New("mock disconnected"))
			case "close":
				if err := fixture.supervisor.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "restart-stop":
				fixture.supervisor.lifecycleMu.Lock()
				err := fixture.supervisor.stop(context.Background(), false)
				fixture.supervisor.lifecycleMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := awaitNativeCompactionResult(t, result); err == nil || !(strings.Contains(err.Error(), "disconnected") || strings.Contains(err.Error(), "stopped")) {
				t.Fatalf("cleanup result = %v", err)
			}
			if fixture.supervisor.ContextCompactionInProgress("thread-a") {
				t.Fatal("Runtime cleanup retained maintenance state")
			}
		})
	}
}

func TestNativeCompactionCannotRacePendingChatStart(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	base := fixture.client.handler
	entered, release := make(chan struct{}), make(chan struct{})
	releaseChat := sync.OnceFunc(func() { close(release) })
	defer releaseChat()
	fixture.client.handler = func(method string, params map[string]any) (any, error) {
		if method == "turn/start" {
			close(entered)
			<-release
		}
		return base(method, params)
	}
	result := make(chan error, 1)
	go func() {
		_, err := fixture.supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thread-a", ModelID: "test-model", Text: "hello"})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("chat start did not reach mock Runtime")
	}
	if err := fixture.supervisor.CompactThread(context.Background(), "thread-a"); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("compaction raced a pending chat ACK: %v", err)
	}
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "user-thread-a", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "user-thread-a", "completed")
	releaseChat()
	if err := awaitNativeCompactionResult(t, result); err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.mu.Lock()
	active, pending := fixture.supervisor.activeTurns["thread-a"], fixture.supervisor.pendingTurns["thread-a"]
	fixture.supervisor.mu.Unlock()
	if active != "" || pending != nil {
		t.Fatalf("terminal-before-ACK chat retained busy state: active=%q pending=%v", active, pending)
	}
	compact := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "maintenance", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "maintenance", "completed")
	if err := awaitNativeCompactionResult(t, compact); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCompactionIgnoresOldTerminalAndItemsUntilNativeStart(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	result := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	fixture.client.emit("item/agentMessage/delta", map[string]any{"threadId": "thread-a", "turnId": "old-chat", "itemId": "old-item", "delta": "delayed old reply"})
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "old-chat", "completed")
	assertNativeCompactionWaiting(t, result)
	fixture.supervisor.mu.Lock()
	flight := fixture.supervisor.compactions["thread-a"]
	unclaimed := flight != nil && flight.turnID == ""
	fixture.supervisor.mu.Unlock()
	if !unclaimed {
		t.Fatal("a delayed user terminal/item claimed the new maintenance operation")
	}
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "new-maintenance", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "old-chat", "completed")
	assertNativeCompactionWaiting(t, result)
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "new-maintenance", "completed")
	if err := awaitNativeCompactionResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCompactionLateMaintenanceCannotReleaseNewChatFence(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	result := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "old-maintenance", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "old-maintenance", "completed")
	if err := awaitNativeCompactionResult(t, result); err != nil {
		t.Fatal(err)
	}
	// Delayed notifications from the completed maintenance operation must not
	// become the identity (or completion) of a subsequent maintenance request.
	next := startNativeCompaction(context.Background(), fixture.supervisor, "thread-a")
	awaitNativeCompactionRequest(t, fixture, "thread-a")
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "old-maintenance", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "old-maintenance", "completed")
	assertNativeCompactionWaiting(t, next)
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "new-maintenance", "inProgress")
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "new-maintenance", "completed")
	if err := awaitNativeCompactionResult(t, next); err != nil {
		t.Fatal(err)
	}
	chat, err := fixture.supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thread-a", ModelID: "test-model", Text: "new user request"})
	if err != nil {
		t.Fatal(err)
	}
	eventContext, stopEvents := context.WithCancel(context.Background())
	defer stopEvents()
	events, err := fixture.supervisor.Subscribe(eventContext, "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	emitNativeCompactionTurn(fixture.client, "turn/started", "thread-a", "new-maintenance", "inProgress")
	fixture.client.emit("item/agentMessage/delta", map[string]any{"threadId": "thread-a", "turnId": "new-maintenance", "itemId": "late-private", "delta": "late maintenance text"})
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "new-maintenance", "completed")
	select {
	case event := <-events:
		t.Fatalf("completed maintenance leaked into new chat: %+v", event)
	default:
	}
	fixture.supervisor.mu.Lock()
	active := fixture.supervisor.activeTurns["thread-a"]
	fixture.supervisor.mu.Unlock()
	if active != chat.CodexTurnID {
		t.Fatalf("late maintenance replaced/cleared current chat fence: %q", active)
	}
	// An older genuine user Turn terminal must likewise leave the new Turn busy.
	emitNativeCompactionTurn(fixture.client, "turn/completed", "thread-a", "older-user-turn", "completed")
	if err := fixture.supervisor.CompactThread(context.Background(), "thread-a"); !errors.Is(err, agentapp.ErrContextCompactionBusy) {
		t.Fatalf("old terminal released the current chat fence: %v", err)
	}
}
