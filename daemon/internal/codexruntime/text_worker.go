package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrTextWorkerClosed         = errors.New("Codex text worker is closed")
	ErrTextWorkerToolsForbidden = errors.New("Codex text worker attempted a tool call")
	ErrTextWorkerInvalidResult  = errors.New("Codex text worker returned an invalid final answer")
)

// TextWorkerFailure retains a recognized provider business code without
// exposing upstream response bodies or credentials to application callers.
type TextWorkerFailure struct{ Code string }

func (e *TextWorkerFailure) Error() string { return "Codex text request could not be completed" }

// TextWorkerOptions configures the same isolated app-server implementation for
// a user's Codex installation or the bundled runtime with a local cloud proxy.
type TextWorkerOptions struct {
	ConfiguredExecutable       string
	WorkingDirectory           string
	Environment                []string
	ClientVersion              string
	MinimumVersion             string
	DirectToolModelCatalogPath string
	AdditionalConfig           []string
	NetworkSettingsPath        string
	RequestTimeout             time.Duration
	StartupTimeout             time.Duration
	CompletionTimeout          time.Duration
	ShutdownTimeout            time.Duration
	Logger                     *slog.Logger
	BeginTurn                  func(string) func() string
}

type textResult struct {
	text string
	err  error
}

type textOperation struct {
	threadID   string
	turnID     string
	turnIssued bool
	terminal   bool
	items      map[string]wireConversationItem
	done       chan textResult
}

// TextWorker serializes independent ephemeral requests. Its app-server never
// receives a project MCP binding, Agent instructions, or conversation history.
type TextWorker struct {
	options TextWorkerOptions
	deps    dependencies
	gate    chan struct{}

	mu           sync.Mutex
	closed       bool
	client       protocolClient
	clientEpoch  uint64
	proxyURL     string
	active       *textOperation
	activeCancel context.CancelFunc
	activeDone   chan struct{}
}

func NewTextWorker(options TextWorkerOptions) (*TextWorker, error) {
	return newTextWorker(options, dependencies{
		discover:            DiscoverExecutable,
		version:             readVersion,
		prepareModelCatalog: prepareDirectToolModelCatalog,
		newClient: func(executable string, args []string, cwd string, env []string, timeout time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
			return newRPCClient(executable, args, cwd, env, timeout, startOSProcess, notification, exited)
		},
	})
}

func newTextWorker(options TextWorkerOptions, deps dependencies) (*TextWorker, error) {
	if !cleanAbsolutePath(options.WorkingDirectory) {
		return nil, errors.New("create Codex text worker: clean absolute working directory is required")
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, errors.New("create Codex text worker: working directory must exist")
	}
	for _, path := range []string{options.DirectToolModelCatalogPath, options.NetworkSettingsPath} {
		if path != "" && !cleanAbsolutePath(path) {
			return nil, errors.New("create Codex text worker: clean absolute configuration paths are required")
		}
	}
	if options.MinimumVersion != "" && !stableSemanticVersionPattern.MatchString(options.MinimumVersion) {
		return nil, errors.New("create Codex text worker: minimum version must be a stable semantic version")
	}
	if options.ClientVersion == "" {
		options.ClientVersion = "dev"
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.CompletionTimeout <= 0 {
		options.CompletionTimeout = time.Minute
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	// The text worker has no use for the daemon's project-scoped MCP token.
	environment := make([]string, 0, len(options.Environment))
	for _, entry := range options.Environment {
		name, _, _ := strings.Cut(entry, "=")
		if name != defaultMCPTokenEnvironmentVariable {
			environment = append(environment, entry)
		}
	}
	options.Environment = environment
	options.AdditionalConfig = append([]string(nil), options.AdditionalConfig...)
	if deps.discover == nil || deps.version == nil || deps.newClient == nil || (options.DirectToolModelCatalogPath != "" && deps.prepareModelCatalog == nil) {
		return nil, errors.New("create Codex text worker: dependencies are required")
	}
	return &TextWorker{options: options, deps: deps, gate: make(chan struct{}, 1)}, nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validTextWorkerText(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximumConversationMessageBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (w *TextWorker) Complete(ctx context.Context, modelID, instructions, text string) (result string, returnErr error) {
	if ctx == nil || strings.TrimSpace(modelID) == "" || len(modelID) > 256 || strings.ContainsRune(modelID, 0) || !validTextWorkerText(instructions) || !validTextWorkerText(text) {
		return "", errors.New("Codex text request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case w.gate <- struct{}{}:
		defer func() { <-w.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	completion, cancel := context.WithTimeout(ctx, w.options.CompletionTimeout)
	defer cancel()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return "", ErrTextWorkerClosed
	}
	done := make(chan struct{})
	w.activeCancel, w.activeDone = cancel, done
	w.mu.Unlock()
	var operation *textOperation
	var client protocolClient
	finish := func() string { return "" }
	defer func() {
		if returnErr != nil && client != nil {
			if operation != nil {
				w.abort(client, operation)
			} else {
				w.discard(client)
			}
		}
		if code := finish(); code != "" && returnErr != nil {
			returnErr = &TextWorkerFailure{Code: code}
		}
		w.mu.Lock()
		w.active, w.activeCancel, w.activeDone = nil, nil, nil
		close(done)
		w.mu.Unlock()
	}()
	var err error
	client, err = w.ensureStarted(completion)
	if err != nil {
		return "", err
	}
	if err := completion.Err(); err != nil {
		return "", err
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	params := map[string]any{
		"ephemeral": true, "cwd": w.options.WorkingDirectory, "model": modelID,
		"approvalPolicy": "never", "sandbox": "read-only",
		"baseInstructions": instructions, "serviceName": "shotforward_text_worker",
		"config": map[string]any{
			"apps":       map[string]any{"_default": map[string]any{"enabled": false}},
			"web_search": "disabled",
		},
	}
	if err := client.Request(completion, "thread/start", params, &thread); err != nil {
		return "", err
	}
	if strings.TrimSpace(thread.Thread.ID) == "" {
		return "", errors.New("Codex text worker thread/start returned no Thread ID")
	}
	operation = &textOperation{threadID: thread.Thread.ID, items: make(map[string]wireConversationItem), done: make(chan textResult, 1)}
	w.mu.Lock()
	if w.client != client {
		w.mu.Unlock()
		return "", errors.New("Codex text app-server disconnected before the turn started")
	}
	w.active = operation
	w.mu.Unlock()
	if w.options.BeginTurn != nil {
		if cleanup := w.options.BeginTurn(operation.threadID); cleanup != nil {
			finish = cleanup
		}
	}
	if err := completion.Err(); err != nil {
		return "", err
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	w.mu.Lock()
	operation.turnIssued = true
	w.mu.Unlock()
	if err := client.Request(completion, "turn/start", map[string]any{
		"threadId": operation.threadID, "model": modelID,
		"input": []map[string]any{{"type": "text", "text": text}},
		"cwd":   w.options.WorkingDirectory, "approvalPolicy": "never",
		"sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false},
		"turnTrigger":   "shotforward_text_worker",
	}, &turn); err != nil {
		return "", err
	}
	w.mu.Lock()
	if turn.Turn.ID == "" || (operation.turnID != "" && operation.turnID != turn.Turn.ID) {
		w.mu.Unlock()
		return "", errors.New("Codex text worker turn/start returned an invalid Turn ID")
	}
	operation.turnID = turn.Turn.ID
	w.mu.Unlock()
	select {
	case received := <-operation.done:
		if err := completion.Err(); err != nil {
			return "", err
		}
		return received.text, received.err
	case <-completion.Done():
		return "", completion.Err()
	}
}

func (w *TextWorker) ensureStarted(ctx context.Context) (protocolClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proxyURL := ""
	if w.options.NetworkSettingsPath != "" {
		settings, err := readNetworkSettings(w.options.NetworkSettingsPath)
		if err != nil {
			return nil, errors.New("Codex text worker network settings are invalid")
		}
		proxyURL = settings.ProxyURL
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, ErrTextWorkerClosed
	}
	client := w.client
	if client != nil && w.proxyURL == proxyURL {
		w.mu.Unlock()
		return client, nil
	}
	w.client = nil
	w.clientEpoch++
	epoch := w.clientEpoch
	w.mu.Unlock()
	if client != nil {
		w.closeClient(client)
	}
	startup, cancel := context.WithTimeout(ctx, w.options.StartupTimeout)
	defer cancel()
	executable, err := w.deps.discover(w.options.ConfiguredExecutable)
	if err != nil {
		return nil, err
	}
	version, err := w.deps.version(startup, executable)
	if err != nil {
		return nil, err
	}
	if w.options.MinimumVersion != "" {
		compatible, err := versionAtLeast(version, w.options.MinimumVersion)
		if err != nil || !compatible {
			return nil, fmt.Errorf("Codex text worker requires Codex %s or newer", w.options.MinimumVersion)
		}
	}
	environment := environmentWithProxy(w.options.Environment, proxyURL)
	additional := append([]string(nil), w.options.AdditionalConfig...)
	additional = append(additional, "features.shell_tool=false", "features.image_generation=false", "agents.enabled=false", `web_search="disabled"`)
	if w.options.DirectToolModelCatalogPath != "" {
		if err := w.deps.prepareModelCatalog(startup, executable, w.options.DirectToolModelCatalogPath, w.options.WorkingDirectory, environment); err != nil {
			return nil, err
		}
		additional = append(additional, `model_catalog_json=`+strconv.Quote(w.options.DirectToolModelCatalogPath))
	}
	baseArgs := baseConfigArguments(additional)
	probe, err := w.deps.newClient(executable, append([]string{"app-server", "--stdio"}, baseArgs...), w.options.WorkingDirectory, environment, w.options.RequestTimeout, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := initialize(startup, probe, w.options.ClientVersion); err != nil {
		w.closeClient(probe)
		return nil, err
	}
	disableArgs, err := inspectExtensions(startup, probe)
	w.closeClient(probe)
	if err != nil {
		return nil, err
	}
	disableArgs = append(disableArgs, "-c", `mcp_servers."shotforward"={enabled=false,url="http://127.0.0.1:1"}`)
	client, err = w.deps.newClient(executable, append(append([]string{"app-server", "--stdio"}, baseArgs...), disableArgs...), w.options.WorkingDirectory, environment, w.options.RequestTimeout,
		func(notification rpcNotification) { w.onNotification(epoch, notification) },
		func(err error) { w.onExit(epoch, err) })
	if err != nil {
		return nil, err
	}
	if err := initialize(startup, client, w.options.ClientVersion); err != nil {
		w.closeClient(client)
		return nil, err
	}
	var account struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := client.Request(startup, "account/read", map[string]any{"refreshToken": false}, &account); err != nil {
		w.closeClient(client)
		return nil, err
	}
	if account.RequiresOpenAIAuth && (len(account.Account) == 0 || string(account.Account) == "null") {
		w.closeClient(client)
		return nil, errors.New("Codex login is required")
	}
	w.mu.Lock()
	if w.closed || w.clientEpoch != epoch || startup.Err() != nil {
		closed := w.closed
		w.mu.Unlock()
		w.closeClient(client)
		if closed {
			return nil, ErrTextWorkerClosed
		}
		if err := startup.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("Codex text app-server disconnected during startup")
	}
	w.client, w.proxyURL = client, proxyURL
	w.mu.Unlock()
	return client, nil
}

func (w *TextWorker) onNotification(epoch uint64, notification rpcNotification) {
	var params struct {
		ThreadID string               `json:"threadId"`
		TurnID   string               `json:"turnId"`
		Item     wireConversationItem `json:"item"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	operation := w.active
	if w.clientEpoch != epoch || operation == nil || operation.terminal || operation.threadID != params.ThreadID {
		return
	}
	turnID := params.TurnID
	if notification.Method == "turn/started" || notification.Method == "turn/completed" {
		turnID = params.Turn.ID
	}
	if turnID != "" {
		if operation.turnID != "" && operation.turnID != turnID {
			return
		}
		operation.turnID = turnID
	}
	switch notification.Method {
	case "item/started", "item/completed":
		switch params.Item.Type {
		case "agentMessage", "userMessage", "reasoning", "plan", "contextCompaction":
		default:
			w.finishLocked(operation, textResult{err: ErrTextWorkerToolsForbidden})
			return
		}
		if notification.Method == "item/completed" && params.Item.Type == "agentMessage" {
			if params.Item.ID == "" || !validTextWorkerText(params.Item.Text) {
				w.finishLocked(operation, textResult{err: ErrTextWorkerInvalidResult})
				return
			}
			if prior, exists := operation.items[params.Item.ID]; exists && (prior.Text != params.Item.Text || prior.Phase != params.Item.Phase) {
				w.finishLocked(operation, textResult{err: ErrTextWorkerInvalidResult})
				return
			}
			operation.items[params.Item.ID] = params.Item
		}
	case "turn/completed":
		if params.Turn.ID == "" || params.Turn.Status != "completed" {
			w.finishLocked(operation, textResult{err: errors.New("Codex text turn did not complete successfully")})
			return
		}
		var final, unphased []string
		for _, item := range operation.items {
			switch item.Phase {
			case "final_answer":
				final = append(final, item.Text)
			case "":
				unphased = append(unphased, item.Text)
			}
		}
		if len(final) == 1 {
			w.finishLocked(operation, textResult{text: final[0]})
		} else if len(final) == 0 && len(unphased) == 1 {
			// Runtimes/models that omit phase still return one completed answer.
			w.finishLocked(operation, textResult{text: unphased[0]})
		} else {
			w.finishLocked(operation, textResult{err: ErrTextWorkerInvalidResult})
		}
	}
}

func (w *TextWorker) finishLocked(operation *textOperation, result textResult) {
	if !operation.terminal {
		operation.terminal = true
		operation.done <- result
	}
}

func (w *TextWorker) onExit(epoch uint64, _ error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.clientEpoch != epoch {
		return
	}
	w.client, w.clientEpoch = nil, w.clientEpoch+1
	if w.active != nil {
		w.finishLocked(w.active, textResult{err: errors.New("Codex text app-server disconnected")})
	}
}

func (w *TextWorker) abort(client protocolClient, operation *textOperation) {
	if client == nil {
		return
	}
	w.mu.Lock()
	threadID, turnID, issued := operation.threadID, operation.turnID, operation.turnIssued
	w.mu.Unlock()
	if issued && threadID != "" && turnID != "" {
		shutdown, cancel := context.WithTimeout(context.Background(), w.options.ShutdownTimeout)
		_ = client.Request(shutdown, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
		cancel()
	}
	// Closing this dedicated process also stops a turn whose start response was
	// lost, and prevents a failed/forbidden request from overlapping the next one.
	w.discard(client)
}

func (w *TextWorker) discard(client protocolClient) {
	w.mu.Lock()
	if w.client == client {
		w.client, w.clientEpoch = nil, w.clientEpoch+1
	}
	w.mu.Unlock()
	w.closeClient(client)
}

func (w *TextWorker) closeClient(client protocolClient) {
	ctx, cancel := context.WithTimeout(context.Background(), w.options.ShutdownTimeout)
	defer cancel()
	_ = client.Close(ctx)
}

func (w *TextWorker) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close Codex text worker: context is required")
	}
	w.mu.Lock()
	w.closed = true
	activeCancel, done := w.activeCancel, w.activeDone
	w.mu.Unlock()
	if activeCancel != nil {
		activeCancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			w.mu.Lock()
			client := w.client
			w.client, w.clientEpoch = nil, w.clientEpoch+1
			w.mu.Unlock()
			if client != nil {
				_ = client.Close(ctx)
			}
			return ctx.Err()
		}
	}
	w.mu.Lock()
	client := w.client
	w.client, w.clientEpoch = nil, w.clientEpoch+1
	w.mu.Unlock()
	if client == nil {
		return nil
	}
	shutdown, cancel := context.WithTimeout(ctx, w.options.ShutdownTimeout)
	defer cancel()
	return client.Close(shutdown)
}
