package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	testAgentSessionID = "ags_AAAAAAAAAAAAAAAAAAAAAAAAAA"
	testProjectID      = "prj_BBBBBBBBBBBBBBBBBBBBBBBBBB"
	testEpisodeID      = "ep_CCCCCCCCCCCCCCCCCCCCCCCCCC"
)

type fakeProtocolClient struct {
	mu             sync.Mutex
	handler        func(string, map[string]any) (any, error)
	onNotification func(rpcNotification)
	onExit         func(error)
	closed         bool
}

func (f *fakeProtocolClient) Request(_ context.Context, method string, params, output any) error {
	values, _ := params.(map[string]any)
	result, err := f.handler(method, values)
	if err != nil {
		return err
	}
	if output != nil {
		data, _ := json.Marshal(result)
		return json.Unmarshal(data, output)
	}
	return nil
}

func (f *fakeProtocolClient) Notify(string, any) error { return nil }
func (f *fakeProtocolClient) Close(context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeProtocolClient) emit(method string, params any) {
	data, _ := json.Marshal(params)
	f.onNotification(rpcNotification{Method: method, Params: data})
}
func (f *fakeProtocolClient) exit(err error) { f.onExit(err) }

func TestThreadUnavailableClassification(t *testing.T) {
	t.Parallel()
	missing := &rpcError{Method: "thread/resume", Code: -32600, Message: "thread not found: thr_missing"}
	if !isThreadUnavailable(missing) {
		t.Fatal("thread-not-found RPC error was not classified as unavailable")
	}
	if isThreadUnavailable(errors.New("runtime disconnected")) {
		t.Fatal("transient Runtime error was classified as an unavailable Thread")
	}
}

func TestSafeExecutionConfigUsesElevatedWindowsSandbox(t *testing.T) {
	t.Parallel()

	windowsArgs := safeExecutionConfigArgumentsForOS(`C:\\Program Files\\ShotForward\\codex.exe`, "windows")
	if !containsArgument(windowsArgs, `windows.sandbox="elevated"`) {
		t.Fatalf("Windows safe-execution args do not enable the elevated sandbox: %v", windowsArgs)
	}
	for _, goos := range []string{"darwin", "linux"} {
		args := safeExecutionConfigArgumentsForOS("/opt/shotforward/codex", goos)
		for _, argument := range args {
			if strings.HasPrefix(argument, "windows.sandbox=") {
				t.Fatalf("%s safe-execution args unexpectedly include %q", goos, argument)
			}
		}
	}
}

func TestSupervisorLifecycleAndRuntimeMapping(t *testing.T) {
	t.Parallel()
	probe := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "command/exec":
			return map[string]any{"exitCode": 0}, nil
		case "config/read":
			return map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"personal": map[string]any{}}}}, nil
		case "mcpServerStatus/list":
			return map[string]any{"data": []map[string]any{{"name": "remote", "pluginId": "plugin-a"}}, "nextCursor": nil}, nil
		default:
			return nil, errors.New("unexpected probe call: " + method)
		}
	}}
	baseHandler := runtimeHandler(false)
	var threadConfigs []map[string]any
	var turnModel string
	var turnPermissions string
	var turnWorkspaceRoots []string
	var turnCWD string
	formal := &fakeProtocolClient{handler: func(method string, params map[string]any) (any, error) {
		if method == "thread/start" || method == "thread/resume" {
			if config, ok := params["config"].(map[string]any); ok {
				threadConfigs = append(threadConfigs, config)
			}
		}
		if method == "turn/start" {
			turnModel, _ = params["model"].(string)
			turnPermissions, _ = params["permissions"].(string)
			turnWorkspaceRoots, _ = params["runtimeWorkspaceRoots"].([]string)
			turnCWD, _ = params["cwd"].(string)
		}
		return baseHandler(method, params)
	}}
	sandboxProbe := standardProbe()
	clients := []*fakeProtocolClient{probe, sandboxProbe, formal}
	var starts [][]string
	deps := dependencies{
		discover: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.153.0", nil },
		newClient: func(_ string, args []string, _ string, _ []string, _ time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
			starts = append(starts, append([]string(nil), args...))
			client := clients[0]
			clients = clients[1:]
			client.onNotification = notification
			client.onExit = exited
			return client, nil
		},
	}
	runtimeParent := filepath.Clean(t.TempDir())
	runtimeRoot := filepath.Join(runtimeParent, "user")
	sessionWorkspaceRoot := filepath.Join(runtimeParent, "agent-sessions")
	for _, directory := range []string{runtimeRoot, sessionWorkspaceRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := newSupervisor(Options{WorkingDirectory: runtimeRoot, SessionWorkspaceRoot: sessionWorkspaceRoot, ClientVersion: "test", Environment: []string{"SHOTFORWARD_MCP_TOKEN=test-token"}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 0 {
		t.Fatal("Supervisor was not lazy")
	}
	snapshot, err := supervisor.Snapshot(context.Background())
	if err != nil || snapshot.Health != agentapp.RuntimeReady || snapshot.Version != "0.153.0" {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if !snapshot.SafeExecutionAvailable || snapshot.SafeExecutionBackend == "" || snapshot.SafeExecutionFailureCode != "" {
		t.Fatalf("safe execution snapshot = %+v", snapshot)
	}
	if len(starts) != 3 || !containsArgument(starts[2], "features.image_generation=false") || !containsArgument(starts[2], `mcp_servers."personal"={enabled=false,url="http://127.0.0.1:1"}`) || !containsArgument(starts[2], `plugins."plugin-a".mcp_servers."remote".enabled=false`) {
		t.Fatalf("formal args do not isolate extensions: %v", starts[2])
	}
	for _, expected := range []string{"project_doc_max_bytes=0", "features.shell_tool=true", "allow_login_shell=false", `shell_environment_policy.inherit="core"`, "shell_environment_policy.ignore_default_excludes=false", `shell_environment_policy.filters."SHOTFORWARD_*"="exclude"`, `default_permissions="shotforward_session"`, `permissions.shotforward_session.network.enabled=false`} {
		if !containsArgument(starts[2], expected) {
			t.Fatalf("formal args missing %q: %v", expected, starts[2])
		}
	}
	models, err := supervisor.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gpt-test" {
		t.Fatalf("models = %+v, %v", models, err)
	}

	workspace := filepath.Join(sessionWorkspaceRoot, "session-1")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	request := agentapp.ThreadRequest{
		AgentSessionID: agent.SessionID(testAgentSessionID),
		ProjectID:      project.ProjectID(testProjectID),
		EpisodeID:      project.EpisodeID(testEpisodeID),
		ModelID:        "gpt-test", WorkspacePath: workspace,
		MCPBindingURL: "http://127.0.0.1:4321/mcp/session", WebSearchDisabled: true,
	}
	thread, err := supervisor.StartThread(context.Background(), request)
	if err != nil || thread.CodexThreadID != "thr_1" || thread.CodexSessionID != "sess_1" {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	if _, err := supervisor.ResumeThread(context.Background(), "thr_1", request); err != nil {
		t.Fatal(err)
	}
	if len(threadConfigs) != 2 {
		t.Fatalf("Thread MCP configs = %d", len(threadConfigs))
	}
	for _, config := range threadConfigs {
		servers, ok := config["mcp_servers"].(map[string]any)
		shotForward, serverOK := servers[shotForwardMCPName].(map[string]any)
		tools, toolsOK := shotForward["enabled_tools"].([]string)
		if !ok || !serverOK || !toolsOK || strings.Join(tools, ",") != strings.Join(agentapp.ShotForwardMCPToolNames(), ",") || shotForward["required"] != true {
			t.Fatalf("ShotForward MCP allowlist config = %#v", config)
		}
	}
	read, err := supervisor.ReadThread(context.Background(), "thr_1")
	if err != nil || len(read.Turns) != 3 || read.Turns[0].Status != agent.TurnRunning || read.Turns[1].Status != agent.TurnCompleted || read.Turns[2].Status != agent.TurnInterrupted {
		t.Fatalf("ReadThread() = %#v, %v", read, err)
	}
	conversation, err := supervisor.ListConversation(context.Background(), "thr_1", "older", 2)
	if err != nil || conversation.NextCursor != "oldest" || len(conversation.Turns) != 2 {
		t.Fatalf("ListConversation() = %#v, %v", conversation, err)
	}
	if conversation.Turns[0].CodexTurnID != "conversation-1" || len(conversation.Turns[0].Messages) != 2 ||
		conversation.Turns[0].Messages[0].Role != agentapp.ConversationUser || conversation.Turns[0].Messages[0].Text != "First question" ||
		conversation.Turns[0].Messages[1].Role != agentapp.ConversationAssistant || conversation.Turns[0].Messages[1].Phase != agentapp.ConversationPhaseFinal || conversation.Turns[0].Messages[1].Text != "First answer" ||
		conversation.Turns[1].CodexTurnID != "conversation-2" || len(conversation.Turns[1].Messages) != 1 {
		t.Fatalf("safe conversation mapping = %#v", conversation)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := supervisor.Subscribe(subCtx, "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thr_1", ModelID: "gpt-test", Text: "analyze"})
	if err != nil || turn.CodexTurnID != "turn_1" {
		t.Fatalf("turn = %+v, %v", turn, err)
	}
	if turnModel != "gpt-test" {
		t.Fatalf("turn/start model = %q", turnModel)
	}
	if turnCWD != workspace || turnPermissions != safeExecutionPermissionProfile {
		t.Fatalf("turn/start permissions = %q cwd=%q", turnPermissions, turnCWD)
	}
	if len(turnWorkspaceRoots) != 1 || turnWorkspaceRoots[0] != workspace {
		t.Fatalf("turn/start workspace roots = %#v", turnWorkspaceRoots)
	}
	formal.emit("item/agentMessage/delta", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "item_1", "delta": "hello"})
	if event := <-events; event.Type != agentapp.RuntimeAssistantDelta || event.TextDelta != "hello" {
		t.Fatalf("delta event = %+v", event)
	}
	formal.emit("item/started", map[string]any{"threadId": "thr_1", "turnId": "turn_1", "item": map[string]any{"id": "shell_1", "type": "commandExecution"}})
	if event := <-events; event.Type != agentapp.RuntimeToolStarted || event.ToolName != "shell" || !event.SandboxedExecution {
		t.Fatalf("sandboxed shell event = %+v", event)
	}
	if err := supervisor.SteerTurn(context.Background(), "thr_1", "turn_1", "more"); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.InterruptTurn(context.Background(), "thr_1", "turn_1"); err != nil {
		t.Fatal(err)
	}
	formal.emit("turn/completed", map[string]any{"threadId": "thr_1", "turn": map[string]any{"id": "turn_1", "status": "interrupted"}})
	if event := <-events; event.TurnStatus != "interrupted" {
		t.Fatalf("completed event = %+v", event)
	}
	challenge, err := supervisor.StartLogin(context.Background(), agentapp.LoginChatGPTDeviceCode)
	if err != nil || challenge.UserCode != "ABCD-1234" {
		t.Fatalf("challenge = %+v, %v", challenge, err)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !probe.closed || !sandboxProbe.closed || !formal.closed {
		t.Fatal("clients were not closed")
	}
}

func TestSupervisorLoginRequiredAndDisconnectEvent(t *testing.T) {
	t.Parallel()
	probe := standardProbe()
	sandboxProbe := standardProbe()
	formal := &fakeProtocolClient{handler: runtimeHandler(true)}
	clients := []*fakeProtocolClient{probe, sandboxProbe, formal}
	supervisor, err := newSupervisor(Options{WorkingDirectory: filepath.Clean(t.TempDir())}, fakeDependencies(&clients))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := supervisor.Snapshot(context.Background())
	if err != nil || snapshot.Health != agentapp.RuntimeLoginRequired {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := supervisor.Subscribe(ctx, "thr_login")
	formal.exit(errors.New("boom"))
	select {
	case event := <-events:
		if event.Type != agentapp.RuntimeConnectionFailure {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing disconnect event")
	}
}

func TestSupervisorClassifiesMissingCoreMethodAsIncompatible(t *testing.T) {
	t.Parallel()
	probe := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		if method == "initialize" {
			return map[string]any{}, nil
		}
		return nil, &rpcError{Method: method, Code: -32601, Message: "method not found"}
	}}
	clients := []*fakeProtocolClient{probe}
	supervisor, _ := newSupervisor(Options{WorkingDirectory: filepath.Clean(t.TempDir())}, fakeDependencies(&clients))
	snapshot, err := supervisor.Snapshot(context.Background())
	if err == nil || snapshot.Health != agentapp.RuntimeIncompatible || snapshot.FailureCode != "codex_incompatible" {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
}

func TestSupervisorRequiresMinimumUserCodexVersion(t *testing.T) {
	t.Parallel()
	clientStarts := 0
	deps := dependencies{
		discover: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.155.0", nil },
		newClient: func(string, []string, string, []string, time.Duration, func(rpcNotification), func(error)) (protocolClient, error) {
			clientStarts++
			return nil, errors.New("must not start an incompatible Codex runtime")
		},
	}
	supervisor, err := newSupervisor(Options{
		WorkingDirectory: filepath.Clean(t.TempDir()),
		MinimumVersion:   MinimumSupportedUserCodexVersion,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, snapshotErr := supervisor.Snapshot(context.Background())
	if snapshotErr == nil || snapshot.Health != agentapp.RuntimeIncompatible || snapshot.FailureCode != "codex_upgrade_required" ||
		snapshot.Version != "0.155.0" || snapshot.MinimumVersion != MinimumSupportedUserCodexVersion {
		t.Fatalf("snapshot = %+v, error = %v", snapshot, snapshotErr)
	}
	if clientStarts != 0 {
		t.Fatalf("client starts = %d, want 0", clientStarts)
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "0.155.0", want: false},
		{version: "0.155.1-beta.1", want: false},
		{version: "0.155.1", want: true},
		{version: "0.155.1+build.2", want: true},
		{version: "0.156.0", want: true},
		{version: "1.0.0", want: true},
	} {
		got, err := versionAtLeast(test.version, MinimumSupportedUserCodexVersion)
		if err != nil || got != test.want {
			t.Errorf("versionAtLeast(%q) = %v, %v; want %v", test.version, got, err, test.want)
		}
	}
	if _, err := versionAtLeast("development", MinimumSupportedUserCodexVersion); err == nil {
		t.Fatal("invalid version was accepted")
	}
}

func TestSupervisorInterruptsActiveTurnsBeforeShutdown(t *testing.T) {
	t.Parallel()
	probe := standardProbe()
	sandboxProbe := standardProbe()
	interrupts := 0
	handler := runtimeHandler(false)
	formal := &fakeProtocolClient{handler: func(method string, params map[string]any) (any, error) {
		if method == "turn/interrupt" {
			interrupts++
		}
		return handler(method, params)
	}}
	clients := []*fakeProtocolClient{probe, sandboxProbe, formal}
	root := filepath.Clean(t.TempDir())
	workspace := filepath.Join(root, "session")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := newSupervisor(Options{WorkingDirectory: root}, fakeDependencies(&clients))
	if err != nil {
		t.Fatal(err)
	}
	supervisor.threadWorkspaces["thr_1"] = workspace
	if _, err := supervisor.StartTurn(context.Background(), agentapp.TurnRequest{CodexThreadID: "thr_1", ModelID: "gpt-test", Text: "analyze"}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if interrupts != 1 {
		t.Fatalf("interrupt count = %d", interrupts)
	}
}

func TestThreadSecurityInputsStayInsideRuntime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inside := filepath.Join(root, "session")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if !validSessionWorkspace(root, inside) || validSessionWorkspace(root, root) || validSessionWorkspace(root, outside) {
		t.Fatal("workspace containment validation failed")
	}
	if !validMCPBindingURL("http://127.0.0.1:4321/mcp/session") {
		t.Fatal("valid loopback MCP URL was rejected")
	}
	for _, value := range []string{
		"https://127.0.0.1:4321/mcp/session",
		"http://localhost:4321/mcp/session",
		"http://example.com:4321/mcp/session",
		"http://127.0.0.1:4321/mcp/session?token=secret",
	} {
		if validMCPBindingURL(value) {
			t.Fatalf("unsafe MCP URL accepted: %s", value)
		}
	}
	if !hasNonEmptyEnvironment([]string{"SHOTFORWARD_MCP_TOKEN=secret"}, "SHOTFORWARD_MCP_TOKEN") || hasNonEmptyEnvironment([]string{"SHOTFORWARD_MCP_TOKEN="}, "SHOTFORWARD_MCP_TOKEN") {
		t.Fatal("MCP token environment validation failed")
	}
}

func TestCodexRuntimeReadRootStaysInsideDistribution(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	packageRoot := filepath.Join(root, "node_modules", "@openai", "codex")
	executable := filepath.Join(packageRoot, "vendor", "target", "codex")
	if got := codexRuntimeReadRoot(executable); got != packageRoot {
		t.Fatalf("npm Runtime read root = %q, want %q", got, packageRoot)
	}
	embedded := filepath.Join(root, "sidecars", "codex")
	if got := codexRuntimeReadRoot(embedded); got != embedded {
		t.Fatalf("embedded Runtime read root = %q, want %q", got, embedded)
	}
}

func TestVerifyThreadMCPRejectsScopeMismatch(t *testing.T) {
	t.Parallel()
	client := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		switch method {
		case "mcpServerStatus/list":
			return map[string]any{"data": []map[string]any{{"name": shotForwardMCPName, "runtimeStatus": "connected"}}, "nextCursor": nil}, nil
		case "mcpServer/tool/call":
			return map[string]any{
				"structuredContent": map[string]any{"session_id": testAgentSessionID, "project_id": testProjectID, "episode_id": "ep_DDDDDDDDDDDDDDDDDDDDDDDDDD"},
				"isError":           false,
			}, nil
		default:
			return nil, errors.New("unexpected verification call: " + method)
		}
	}}
	expected := agentapp.ThreadRequest{
		AgentSessionID: agent.SessionID(testAgentSessionID),
		ProjectID:      project.ProjectID(testProjectID),
		EpisodeID:      project.EpisodeID(testEpisodeID),
	}
	if err := verifyThreadMCP(context.Background(), client, "thr_1", expected); err == nil || !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("scope mismatch error = %v", err)
	}
}

func TestVerifyThreadMCPWaitsForConnection(t *testing.T) {
	t.Parallel()
	statusChecks := 0
	client := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		switch method {
		case "mcpServerStatus/list":
			statusChecks++
			status := "starting"
			if statusChecks == 3 {
				status = "connected"
			}
			return map[string]any{"data": []map[string]any{{"name": shotForwardMCPName, "runtimeStatus": status}}, "nextCursor": nil}, nil
		case "mcpServer/tool/call":
			return map[string]any{
				"structuredContent": map[string]any{"session_id": testAgentSessionID, "project_id": testProjectID, "episode_id": testEpisodeID},
				"isError":           false,
			}, nil
		default:
			return nil, errors.New("unexpected verification call: " + method)
		}
	}}
	expected := agentapp.ThreadRequest{
		AgentSessionID: agent.SessionID(testAgentSessionID),
		ProjectID:      project.ProjectID(testProjectID),
		EpisodeID:      project.EpisodeID(testEpisodeID),
	}
	if err := verifyThreadMCP(context.Background(), client, "thr_1", expected); err != nil {
		t.Fatal(err)
	}
	if statusChecks != 3 {
		t.Fatalf("status checks = %d, want 3", statusChecks)
	}
}

func TestVerifyThreadMCPStopsOnTerminalConnectionFailure(t *testing.T) {
	t.Parallel()
	statusChecks := 0
	client := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		if method != "mcpServerStatus/list" {
			return nil, errors.New("unexpected verification call: " + method)
		}
		statusChecks++
		return map[string]any{"data": []map[string]any{{"name": shotForwardMCPName, "runtimeStatus": "failed"}}, "nextCursor": nil}, nil
	}}
	err := verifyThreadMCP(context.Background(), client, "thr_1", agentapp.ThreadRequest{})
	if err == nil || !strings.Contains(err.Error(), `terminal status "failed"`) {
		t.Fatalf("verification error = %v", err)
	}
	if statusChecks != 1 {
		t.Fatalf("status checks = %d, want 1", statusChecks)
	}
}

func runtimeHandler(loginRequired bool) func(string, map[string]any) (any, error) {
	return func(method string, params map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "account/read":
			if loginRequired {
				return map[string]any{"account": nil, "requiresOpenaiAuth": true}, nil
			}
			return map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}, nil
		case "model/list":
			return map[string]any{"data": []map[string]any{{"id": "gpt-test", "displayName": "GPT Test", "hidden": false}}, "nextCursor": nil}, nil
		case "thread/start", "thread/resume":
			return map[string]any{"thread": map[string]any{"id": "thr_1", "sessionId": "sess_1", "name": "Test", "model": "gpt-test"}}, nil
		case "thread/read":
			return map[string]any{"thread": map[string]any{
				"id": "thr_1", "sessionId": "sess_1", "name": "Test", "model": "gpt-test",
				"turns": []map[string]any{{"id": "running", "status": "inProgress"}, {"id": "completed", "status": "completed"}, {"id": "interrupted", "status": "interrupted"}},
			}}, nil
		case "mcpServerStatus/list":
			return map[string]any{"data": []map[string]any{{"name": shotForwardMCPName, "runtimeStatus": "connected"}}, "nextCursor": nil}, nil
		case "mcpServer/tool/call":
			return map[string]any{
				"structuredContent": map[string]any{"session_id": testAgentSessionID, "project_id": testProjectID, "episode_id": testEpisodeID},
				"isError":           false,
			}, nil
		case "thread/turns/list":
			if params["itemsView"] == "notLoaded" {
				return map[string]any{"data": []map[string]any{{"id": "running", "status": "inProgress"}, {"id": "completed", "status": "completed"}, {"id": "interrupted", "status": "interrupted"}}, "nextCursor": nil}, nil
			}
			if params["limit"] != 1 || params["sortDirection"] != "desc" || params["itemsView"] != "full" {
				return nil, errors.New("unexpected conversation pagination")
			}
			if params["cursor"] == "older" {
				return map[string]any{"data": []map[string]any{{"id": "conversation-2", "status": "inProgress", "items": []map[string]any{
					{"id": "reasoning-2", "type": "reasoning", "summary": []string{"must stay private"}},
					{"id": "agent-2", "type": "agentMessage", "text": "Working", "phase": "commentary"},
				}}}, "nextCursor": "middle"}, nil
			}
			if params["cursor"] != "middle" {
				return nil, errors.New("unexpected conversation cursor")
			}
			return map[string]any{
				"data": []map[string]any{
					{"id": "conversation-1", "status": "completed", "items": []map[string]any{
						{"id": "user-1", "type": "userMessage", "content": []map[string]any{{"type": "input_text", "text": "First question"}}},
						{"id": "tool-1", "type": "mcpToolCall", "arguments": map[string]any{"secret": true}},
						{"id": "agent-1", "type": "agentMessage", "text": "First answer", "phase": "final_answer"},
					}},
				},
				"nextCursor": "oldest",
			}, nil
		case "turn/start":
			return map[string]any{"turn": map[string]any{"id": "turn_1"}}, nil
		case "turn/steer":
			return map[string]any{"turnId": params["expectedTurnId"]}, nil
		case "turn/interrupt":
			return map[string]any{}, nil
		case "account/login/start":
			return map[string]any{"loginId": "login_1", "verificationUrl": "https://example.invalid/device", "userCode": "ABCD-1234"}, nil
		default:
			return nil, errors.New("unexpected runtime call: " + method)
		}
	}
}

func standardProbe() *fakeProtocolClient {
	return &fakeProtocolClient{handler: func(method string, params map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "command/exec":
			command, _ := params["command"].([]string)
			if len(command) > 0 {
				if err := os.WriteFile(command[len(command)-1], nil, 0o600); err != nil {
					return nil, err
				}
			}
			return map[string]any{"exitCode": 0}, nil
		case "config/read":
			return map[string]any{"config": map[string]any{}}, nil
		case "mcpServerStatus/list":
			return map[string]any{"data": []any{}, "nextCursor": nil}, nil
		default:
			return nil, errors.New("unexpected: " + method)
		}
	}}
}

func TestSupervisorFailsClosedWhenSandboxPreflightFails(t *testing.T) {
	t.Parallel()
	probe := standardProbe()
	sandboxProbe := standardProbe()
	sandboxProbe.handler = func(method string, params map[string]any) (any, error) {
		if method == "command/exec" {
			return nil, &rpcError{Method: method, Code: -32000, Message: "sandbox unavailable"}
		}
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "config/read":
			return map[string]any{"config": map[string]any{}}, nil
		case "mcpServerStatus/list":
			return map[string]any{"data": []any{}, "nextCursor": nil}, nil
		default:
			return nil, errors.New("unexpected: " + method)
		}
	}
	formal := &fakeProtocolClient{handler: runtimeHandler(false)}
	clients := []*fakeProtocolClient{probe, sandboxProbe, formal}
	var starts [][]string
	deps := fakeDependencies(&clients)
	baseNewClient := deps.newClient
	deps.newClient = func(executable string, args []string, cwd string, environment []string, timeout time.Duration, notify func(rpcNotification), exit func(error)) (protocolClient, error) {
		starts = append(starts, append([]string(nil), args...))
		return baseNewClient(executable, args, cwd, environment, timeout, notify, exit)
	}
	root := filepath.Clean(t.TempDir())
	supervisor, err := newSupervisor(Options{WorkingDirectory: root, SessionWorkspaceRoot: root}, deps)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := supervisor.Snapshot(context.Background())
	if err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if snapshot.SafeExecutionAvailable || snapshot.SafeExecutionBackend != "" || snapshot.SafeExecutionFailureCode != "codex_sandbox_unavailable" || snapshot.SafeExecutionFailureSummary == "" {
		t.Fatalf("sandbox failure snapshot = %+v", snapshot)
	}
	if len(starts) != 3 || containsArgument(starts[2], "features.shell_tool=true") {
		t.Fatalf("unsafe shell was enabled after failed preflight: %v", starts)
	}
}

func fakeDependencies(clients *[]*fakeProtocolClient) dependencies {
	return dependencies{
		discover: func(string) (string, error) { return "/bin/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.153.0", nil },
		newClient: func(_ string, _ []string, _ string, _ []string, _ time.Duration, notify func(rpcNotification), exit func(error)) (protocolClient, error) {
			client := (*clients)[0]
			*clients = (*clients)[1:]
			client.onNotification = notify
			client.onExit = exit
			return client, nil
		},
	}
}

func containsArgument(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func TestTurnInputIncludesLocalImages(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "reference.png")
	if err := os.WriteFile(imagePath, []byte("not-empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := turnInput("use this", []string{imagePath})
	if err != nil || len(input) != 2 || input[0]["type"] != "text" || input[1]["type"] != "localImage" || input[1]["path"] != imagePath {
		t.Fatalf("turnInput() = %#v, %v", input, err)
	}
	if _, err := turnInput("use this", []string{"relative.png"}); err == nil {
		t.Fatal("turnInput accepted a relative image path")
	}
}

func TestNormalizeImageGeneratedEvent(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "generated.png")
	if err := os.WriteFile(imagePath, []byte("not-empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(map[string]any{
		"threadId": "thr_1", "turnId": "turn_1",
		"item": map[string]any{"id": "image_1", "type": "imageGeneration", "savedPath": imagePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := normalizeEvent(rpcNotification{Method: "item/completed", Params: params})
	if !ok || event.Type != agentapp.RuntimeImageGenerated || event.GeneratedImagePath != imagePath {
		t.Fatalf("normalizeEvent() = %#v, %v", event, ok)
	}
}
