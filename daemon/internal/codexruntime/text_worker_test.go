package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type textWorkerTestClient struct {
	mu      sync.Mutex
	handler func(context.Context, string, map[string]any) (any, error)
	notify  func(rpcNotification)
	exited  func(error)
	closed  bool
}

func (c *textWorkerTestClient) Request(ctx context.Context, method string, params, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	values, _ := params.(map[string]any)
	result, err := c.handler(ctx, method, values)
	if err != nil || output == nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func (c *textWorkerTestClient) Notify(string, any) error { return nil }

func (c *textWorkerTestClient) Close(context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *textWorkerTestClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *textWorkerTestClient) emit(method string, params any) {
	data, _ := json.Marshal(params)
	c.notify(rpcNotification{Method: method, Params: data})
}

type textWorkerTestStart struct {
	args        []string
	environment []string
	client      *textWorkerTestClient
}

type textWorkerTestTurn struct {
	client *textWorkerTestClient
	params map[string]any
	thread string
	turn   string
}

type textWorkerFixture struct {
	mu              sync.Mutex
	starts          []textWorkerTestStart
	threads         []map[string]any
	turns           chan textWorkerTestTurn
	stops           chan map[string]any
	onTurn          func(textWorkerTestTurn)
	account         map[string]any
	onAccount       func(context.Context, *textWorkerTestClient) (any, error)
	onStart         func(context.Context, string, map[string]any) (any, error)
	onTurnResponse  func(context.Context, textWorkerTestTurn) (any, error)
	skipTurnStarted bool
	prepared        int
}

func newTextWorkerFixture(t *testing.T, options TextWorkerOptions) (*TextWorker, *textWorkerFixture) {
	t.Helper()
	if options.WorkingDirectory == "" {
		options.WorkingDirectory = filepath.Clean(t.TempDir())
	}
	if options.CompletionTimeout == 0 {
		options.CompletionTimeout = time.Second
	}
	f := &textWorkerFixture{turns: make(chan textWorkerTestTurn, 10), stops: make(chan map[string]any, 10), account: map[string]any{"account": nil, "requiresOpenaiAuth": false}}
	deps := dependencies{
		discover: func(string) (string, error) { return "/fake/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.155.1", nil },
		prepareModelCatalog: func(context.Context, string, string, string, []string) error {
			f.mu.Lock()
			f.prepared++
			f.mu.Unlock()
			return nil
		},
		newClient: func(_ string, args []string, _ string, env []string, _ time.Duration, notify func(rpcNotification), exited func(error)) (protocolClient, error) {
			c := &textWorkerTestClient{notify: notify, exited: exited}
			c.handler = func(ctx context.Context, method string, params map[string]any) (any, error) {
				switch method {
				case "initialize":
					return map[string]any{}, nil
				case "config/read":
					return map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"personal": map[string]any{}, "shotforward": map[string]any{}}}}, nil
				case "mcpServerStatus/list":
					return map[string]any{"data": []map[string]any{{"name": "remote", "pluginId": "plugin-a"}}}, nil
				case "account/read":
					if f.onAccount != nil {
						return f.onAccount(ctx, c)
					}
					return f.account, nil
				case "thread/start":
					if f.onStart != nil {
						return f.onStart(ctx, method, params)
					}
					f.mu.Lock()
					f.threads = append(f.threads, params)
					id := fmt.Sprintf("thread-%d", len(f.threads))
					f.mu.Unlock()
					return map[string]any{"thread": map[string]any{"id": id}}, nil
				case "turn/start":
					thread, _ := params["threadId"].(string)
					turn := textWorkerTestTurn{client: c, params: params, thread: thread, turn: "turn-" + thread}
					if !f.skipTurnStarted {
						c.emit("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn.turn}})
					}
					if f.onTurn != nil {
						f.onTurn(turn)
					}
					f.turns <- turn
					if f.onTurnResponse != nil {
						return f.onTurnResponse(ctx, turn)
					}
					return map[string]any{"turn": map[string]any{"id": turn.turn}}, nil
				case "turn/interrupt":
					f.stops <- params
					return map[string]any{}, nil
				default:
					return nil, errors.New("unexpected text worker method: " + method)
				}
			}
			f.mu.Lock()
			f.starts = append(f.starts, textWorkerTestStart{args: append([]string(nil), args...), environment: append([]string(nil), env...), client: c})
			f.mu.Unlock()
			return c, nil
		},
	}
	w, err := newTextWorker(options, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	return w, f
}

func (f *textWorkerFixture) snapshot() ([]textWorkerTestStart, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]textWorkerTestStart(nil), f.starts...), append([]map[string]any(nil), f.threads...)
}

func completeTextWorker(w *TextWorker, ctx context.Context) <-chan textResult {
	result := make(chan textResult, 1)
	go func() {
		text, err := w.Complete(ctx, "gpt-test", "Translate the entire input. Return only its translation.", "完整提示词。\n第二段 @角色。")
		result <- textResult{text: text, err: err}
	}()
	return result
}

func waitTextWorkerTurn(t *testing.T, f *textWorkerFixture) textWorkerTestTurn {
	t.Helper()
	select {
	case turn := <-f.turns:
		return turn
	case <-time.After(2 * time.Second):
		t.Fatal("text worker did not start a turn")
		return textWorkerTestTurn{}
	}
}

func waitTextWorkerResult(t *testing.T, results <-chan textResult) textResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("text worker did not return")
		return textResult{}
	}
}

func (turn textWorkerTestTurn) answer(id, phase, text string) {
	turn.client.emit("item/completed", map[string]any{"threadId": turn.thread, "turnId": turn.turn, "item": map[string]any{"id": id, "type": "agentMessage", "text": text, "phase": phase}})
}

func (turn textWorkerTestTurn) finish(status string) {
	turn.client.emit("turn/completed", map[string]any{"threadId": turn.thread, "turn": map[string]any{"id": turn.turn, "status": status}})
}

func TestTextWorkerCompletesWholeTextOnlyAfterSuccessfulTurn(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{Environment: []string{"CODEX_HOME=/private/home", "SHOTFORWARD_MCP_TOKEN=project-secret"}, AdditionalConfig: []string{`model_provider="shotforward"`}})
	if starts, _ := f.snapshot(); len(starts) != 0 {
		t.Fatal("text worker is not lazy")
	}
	result := completeTextWorker(w, context.Background())
	turn := waitTextWorkerTurn(t, f)
	input := turn.params["input"].([]map[string]any)
	if len(input) != 1 || input[0]["text"] != "完整提示词。\n第二段 @角色。" || turn.params["model"] != "gpt-test" {
		t.Fatalf("whole input = %#v", turn.params)
	}
	turn.client.emit("item/agentMessage/delta", map[string]any{"threadId": turn.thread, "turnId": turn.turn, "delta": "ignored partial"})
	turn.answer("commentary", "commentary", "Working on it")
	turn.answer("answer", "final_answer", "Whole prompt.\nSecond paragraph @角色.")
	turn.answer("answer", "final_answer", "Whole prompt.\nSecond paragraph @角色.")
	select {
	case <-result:
		t.Fatal("returned before turn completion")
	default:
	}
	turn.finish("completed")
	got := waitTextWorkerResult(t, result)
	if got.err != nil || got.text != "Whole prompt.\nSecond paragraph @角色." {
		t.Fatalf("result = %#v", got)
	}
	starts, threads := f.snapshot()
	if len(starts) != 2 || !starts[0].client.isClosed() {
		t.Fatalf("startup count = %d", len(starts))
	}
	for _, config := range []string{"features.shell_tool=false", "features.image_generation=false", "agents.enabled=false", `web_search="disabled"`, `mcp_servers."personal"={enabled=false,url="http://127.0.0.1:1"}`, `mcp_servers."shotforward"={enabled=false,url="http://127.0.0.1:1"}`, `plugins."plugin-a".mcp_servers."remote".enabled=false`, `model_provider="shotforward"`} {
		if !containsArgument(starts[1].args, config) {
			t.Errorf("missing disabled tool/provider config %s", config)
		}
	}
	if containsArgument(starts[1].args, "features.shell_tool=true") || strings.Contains(strings.Join(starts[1].environment, "\n"), "project-secret") {
		t.Fatal("text worker enabled shell or inherited project MCP credentials")
	}
	if len(threads) != 1 || threads[0]["ephemeral"] != true || threads[0]["sandbox"] != "read-only" || threads[0]["approvalPolicy"] != "never" || threads[0]["serviceName"] != "shotforward_text_worker" {
		t.Fatalf("thread = %#v", threads)
	}
	config := threads[0]["config"].(map[string]any)
	if config["web_search"] != "disabled" || config["mcp_servers"] != nil {
		t.Fatalf("thread config = %#v", config)
	}
}

func TestTextWorkerRejectsToolsAndInvalidCompletion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		run  func(textWorkerTestTurn)
		want error
	}{
		{"shell", func(turn textWorkerTestTurn) {
			turn.client.emit("item/started", map[string]any{"threadId": turn.thread, "turnId": turn.turn, "item": map[string]any{"type": "commandExecution"}})
		}, ErrTextWorkerToolsForbidden},
		{"unknown tool", func(turn textWorkerTestTurn) {
			turn.client.emit("item/completed", map[string]any{"threadId": turn.thread, "turnId": turn.turn, "item": map[string]any{"type": "futureTool"}})
		}, ErrTextWorkerToolsForbidden},
		{"commentary only", func(turn textWorkerTestTurn) { turn.answer("a", "commentary", "Working"); turn.finish("completed") }, ErrTextWorkerInvalidResult},
		{"no answer", func(turn textWorkerTestTurn) { turn.finish("completed") }, ErrTextWorkerInvalidResult},
		{"empty final", func(turn textWorkerTestTurn) { turn.answer("a", "final_answer", " ") }, ErrTextWorkerInvalidResult},
		{"multiple finals", func(turn textWorkerTestTurn) {
			turn.answer("a", "final_answer", "A")
			turn.answer("b", "final_answer", "B")
			turn.finish("completed")
		}, ErrTextWorkerInvalidResult},
		{"changed duplicate", func(turn textWorkerTestTurn) {
			turn.answer("a", "final_answer", "A")
			turn.answer("a", "final_answer", "B")
		}, ErrTextWorkerInvalidResult},
		{"failed turn", func(turn textWorkerTestTurn) { turn.answer("a", "final_answer", "A"); turn.finish("failed") }, nil},
		{"interrupted turn", func(turn textWorkerTestTurn) { turn.answer("a", "final_answer", "A"); turn.finish("interrupted") }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, f := newTextWorkerFixture(t, TextWorkerOptions{})
			result := completeTextWorker(w, context.Background())
			turn := waitTextWorkerTurn(t, f)
			test.run(turn)
			got := waitTextWorkerResult(t, result)
			if got.err == nil || got.text != "" || (test.want != nil && !errors.Is(got.err, test.want)) {
				t.Fatalf("result = %#v, want %v", got, test.want)
			}
			if !turn.client.isClosed() {
				t.Fatal("failed turn process was not closed")
			}
			select {
			case stop := <-f.stops:
				if stop["threadId"] != turn.thread || stop["turnId"] != turn.turn {
					t.Fatalf("interrupt = %#v", stop)
				}
			default:
				t.Fatal("failed turn was not interrupted")
			}
		})
	}
}

func TestTextWorkerCancellationTimeoutAndCloseInterrupt(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cancel", "timeout", "close"} {
		t.Run(name, func(t *testing.T) {
			options := TextWorkerOptions{}
			if name == "timeout" {
				options.CompletionTimeout = 40 * time.Millisecond
			}
			w, f := newTextWorkerFixture(t, options)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := completeTextWorker(w, ctx)
			turn := waitTextWorkerTurn(t, f)
			if name == "cancel" {
				cancel()
			}
			if name == "close" {
				if err := w.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			got := waitTextWorkerResult(t, result)
			want := context.Canceled
			if name == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(got.err, want) || got.text != "" || !turn.client.isClosed() {
				t.Fatalf("result = %#v, closed=%v", got, turn.client.isClosed())
			}
			select {
			case <-f.stops:
			default:
				t.Fatal("canceled turn was not interrupted")
			}
		})
	}
}

func TestTextWorkerSerializesFreshThreadsAndReusesProcess(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{})
	first := completeTextWorker(w, context.Background())
	a := waitTextWorkerTurn(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	queued := completeTextWorker(w, ctx)
	cancel()
	if got := waitTextWorkerResult(t, queued); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("queued result = %#v", got)
	}
	second := completeTextWorker(w, context.Background())
	select {
	case <-f.turns:
		t.Fatal("second request overlapped the first turn")
	default:
	}
	a.answer("a", "final_answer", "first")
	a.finish("completed")
	if got := waitTextWorkerResult(t, first); got.err != nil {
		t.Fatal(got.err)
	}
	b := waitTextWorkerTurn(t, f)
	if a.thread == b.thread || a.client != b.client {
		t.Fatal("requests did not use separate threads in the same process")
	}
	a.answer("late", "final_answer", "unrelated old answer")
	a.finish("failed")
	b.answer("b", "", "second")
	b.finish("completed")
	if got := waitTextWorkerResult(t, second); got.err != nil || got.text != "second" {
		t.Fatalf("second result = %#v", got)
	}
	if starts, _ := f.snapshot(); len(starts) != 2 {
		t.Fatalf("startup count = %d", len(starts))
	}
}

func TestTextWorkerCloudBusinessFailureAndOptionalAuth(t *testing.T) {
	t.Parallel()
	var begun, finished string
	w, f := newTextWorkerFixture(t, TextWorkerOptions{BeginTurn: func(threadID string) func() string {
		begun = threadID
		return func() string { finished = threadID; return "shotforward_insufficient_points" }
	}})
	result := completeTextWorker(w, context.Background())
	turn := waitTextWorkerTurn(t, f)
	turn.finish("failed")
	got := waitTextWorkerResult(t, result)
	var failure *TextWorkerFailure
	if !errors.As(got.err, &failure) || failure.Code != "shotforward_insufficient_points" || begun != turn.thread || finished != turn.thread {
		t.Fatalf("business error = %#v, begun=%q finished=%q", got, begun, finished)
	}
	if strings.Contains(got.err.Error(), failure.Code) {
		t.Fatal("business failure did not use a safe public message")
	}

	local, fixture := newTextWorkerFixture(t, TextWorkerOptions{})
	fixture.account = map[string]any{"account": nil, "requiresOpenaiAuth": true}
	if _, err := local.Complete(context.Background(), "gpt-test", "Translate", "input"); err == nil {
		t.Fatal("missing required local authentication was accepted")
	}
	if starts, threads := fixture.snapshot(); len(starts) != 2 || len(threads) != 0 || !starts[1].client.isClosed() {
		t.Fatal("unauthenticated startup leaked a process or started a thread")
	}
}

func TestTextWorkerStartupCancellationAndDisconnect(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{})
	entered := make(chan struct{})
	f.onStart = func(ctx context.Context, _ string, _ map[string]any) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := completeTextWorker(w, ctx)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("thread/start was not reached")
	}
	cancel()
	if got := waitTextWorkerResult(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("startup cancellation = %#v", got)
	}
	f.onStart = nil
	result = completeTextWorker(w, context.Background())
	turn := waitTextWorkerTurn(t, f)
	turn.client.exited(errors.New("private upstream error"))
	if got := waitTextWorkerResult(t, result); got.err == nil || strings.Contains(got.err.Error(), "private") {
		t.Fatalf("disconnect result = %#v", got)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Complete(context.Background(), "gpt-test", "Translate", "input"); !errors.Is(err, ErrTextWorkerClosed) {
		t.Fatalf("closed worker result = %v", err)
	}
}

func TestTextWorkerReloadsNetworkSettingsAndPreparesCloudCatalog(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	path := filepath.Join(root, "network.json")
	w, f := newTextWorkerFixture(t, TextWorkerOptions{WorkingDirectory: root, NetworkSettingsPath: path, DirectToolModelCatalogPath: filepath.Join(root, "models.json"), Environment: []string{"CODEX_HOME=/private/cloud", "SHOTFORWARD_EMBEDDED_API_KEY=proxy-only"}})
	first := completeTextWorker(w, context.Background())
	a := waitTextWorkerTurn(t, f)
	a.answer("a", "final_answer", "first")
	a.finish("completed")
	if got := waitTextWorkerResult(t, first); got.err != nil {
		t.Fatal(got.err)
	}
	if err := writeNetworkSettings(path, agentapp.RuntimeNetworkSettings{ProxyURL: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	second := completeTextWorker(w, context.Background())
	b := waitTextWorkerTurn(t, f)
	b.answer("b", "final_answer", "second")
	b.finish("completed")
	if got := waitTextWorkerResult(t, second); got.err != nil {
		t.Fatal(got.err)
	}
	starts, _ := f.snapshot()
	if len(starts) != 4 || !a.client.isClosed() || a.client == b.client {
		t.Fatal("network change did not replace the dedicated worker process")
	}
	if !containsArgument(starts[3].environment, "HTTPS_PROXY=http://127.0.0.1:7890") || !containsArgument(starts[3].environment, "SHOTFORWARD_EMBEDDED_API_KEY=proxy-only") {
		t.Fatalf("environment = %#v", starts[3].environment)
	}
	f.mu.Lock()
	prepared := f.prepared
	f.mu.Unlock()
	if prepared != 2 {
		t.Fatalf("model catalogs prepared = %d", prepared)
	}
}

func TestTextWorkerCloseDuringStartupClosesUnpublishedProcess(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{})
	entered := make(chan struct{})
	f.onAccount = func(ctx context.Context, _ *textWorkerTestClient) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	result := completeTextWorker(w, context.Background())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("account startup phase was not reached")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := waitTextWorkerResult(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("startup close = %#v", got)
	}
	starts, threads := f.snapshot()
	if len(starts) != 2 || len(threads) != 0 || !starts[1].client.isClosed() {
		t.Fatal("unpublished startup process was not closed")
	}
}

func TestTextWorkerCancellationOfLostTurnStartClosesProcess(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{})
	f.skipTurnStarted = true
	f.onTurnResponse = func(ctx context.Context, _ textWorkerTestTurn) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := completeTextWorker(w, ctx)
	turn := waitTextWorkerTurn(t, f)
	cancel()
	if got := waitTextWorkerResult(t, result); !errors.Is(got.err, context.Canceled) || !turn.client.isClosed() {
		t.Fatalf("lost turn/start result = %#v, closed=%v", got, turn.client.isClosed())
	}
}

func TestTextWorkerRejectsInvalidAndCanceledInputWithoutStarting(t *testing.T) {
	t.Parallel()
	w, f := newTextWorkerFixture(t, TextWorkerOptions{})
	for _, input := range []string{"", " ", "input\x00", strings.Repeat("x", maximumConversationMessageBytes+1)} {
		if _, err := w.Complete(context.Background(), "gpt-test", "Translate", input); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Complete(ctx, "gpt-test", "Translate", "input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled input = %v", err)
	}
	if starts, _ := f.snapshot(); len(starts) != 0 {
		t.Fatal("invalid/canceled request started an app-server")
	}
}
