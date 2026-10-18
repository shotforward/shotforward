package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

const (
	MinimumSupportedUserCodexVersion   = "0.155.1"
	defaultRequestTimeout              = 15 * time.Second
	defaultStartupTimeout              = 30 * time.Second
	defaultStartupRetryBackoff         = time.Second
	defaultShutdownTimeout             = 2 * time.Second
	defaultMCPVerificationTimeout      = 10 * time.Second
	defaultMCPVerificationPollInterval = 100 * time.Millisecond
	shotForwardMCPName                 = "shotforward"
	defaultMCPTokenEnvironmentVariable = "SHOTFORWARD_MCP_TOKEN"
	safeExecutionPermissionProfile     = "shotforward_session"
	maximumConversationCursorBytes     = 4 * 1024
	maximumConversationMessageBytes    = 512 * 1024
	maximumConversationPageBytes       = 4 * 1024 * 1024
)

var versionPattern = regexp.MustCompile(`(?m)codex-cli\s+([^\s]+)`)
var stableSemanticVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var semanticVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
var sensitiveSummaryPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s]+|(sk-[a-z0-9_-]+)`)
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Options struct {
	ConfiguredExecutable        string
	WorkingDirectory            string
	SessionWorkspaceRoot        string
	DirectToolModelCatalogPath  string
	Environment                 []string
	ClientVersion               string
	Logger                      *slog.Logger
	MinimumVersion              string
	RequestTimeout              time.Duration
	StartupTimeout              time.Duration
	ShutdownTimeout             time.Duration
	MCPTokenEnvironmentVariable string
	BaseInstructions            string
	AdditionalConfig            []string
	NetworkSettingsPath         string
}

type dependencies struct {
	discover            func(string) (string, error)
	version             func(context.Context, string) (string, error)
	prepareModelCatalog func(context.Context, string, string, string, []string) error
	newClient           func(string, []string, string, []string, time.Duration, func(rpcNotification), func(error)) (protocolClient, error)
}

type Supervisor struct {
	options           Options
	deps              dependencies
	mu                sync.Mutex
	snapshot          agentapp.RuntimeSnapshot
	models            []agentapp.Model
	client            protocolClient
	starting          *startupAttempt
	startupGeneration uint64
	startRetryAt      time.Time
	startErr          error
	lifecycleMu       sync.Mutex
	closed            bool
	stopping          bool
	subscribers       map[string]map[uint64]chan agentapp.RuntimeEvent
	nextSubscriber    uint64
	activeTurns       map[string]string
	pendingTurns      map[string]*pendingTurnStart
	compactions       map[string]*contextCompaction
	compactedTurns    map[string]string
	threadWorkspaces  map[string]string
	networkSettings   agentapp.RuntimeNetworkSettings
	baseEnvironment   []string
}

// One initialization belongs to the Supervisor, not to the HTTP caller that
// happened to request it first. Closing/resetting the Supervisor cancels it.
type startupAttempt struct {
	done       chan struct{}
	cancel     context.CancelFunc
	generation uint64
	err        error
	exited     bool // guarded by Supervisor.mu
}

var errSupervisorClosed = errors.New("Codex Runtime is closed")
var errSupervisorStopping = errors.New("Codex Runtime is restarting")

var _ agentapp.Runtime = (*Supervisor)(nil)

func NewSupervisor(options Options) (*Supervisor, error) {
	deps := dependencies{
		discover:            DiscoverExecutable,
		version:             readVersion,
		prepareModelCatalog: prepareDirectToolModelCatalog,
		newClient: func(executable string, args []string, cwd string, env []string, timeout time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
			return newRPCClient(executable, args, cwd, env, timeout, startOSProcess, notification, exited)
		},
	}
	return newSupervisor(options, deps)
}

func newSupervisor(options Options, deps dependencies) (*Supervisor, error) {
	if options.WorkingDirectory == "" || !filepath.IsAbs(options.WorkingDirectory) || filepath.Clean(options.WorkingDirectory) != options.WorkingDirectory {
		return nil, errors.New("create Codex Runtime: clean absolute working directory is required")
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, errors.New("create Codex Runtime: working directory must exist")
	}
	if options.SessionWorkspaceRoot == "" {
		options.SessionWorkspaceRoot = options.WorkingDirectory
	}
	if !filepath.IsAbs(options.SessionWorkspaceRoot) || filepath.Clean(options.SessionWorkspaceRoot) != options.SessionWorkspaceRoot {
		return nil, errors.New("create Codex Runtime: clean absolute Session workspace root is required")
	}
	if info, err := os.Stat(options.SessionWorkspaceRoot); err != nil || !info.IsDir() {
		return nil, errors.New("create Codex Runtime: Session workspace root must exist")
	}
	if options.DirectToolModelCatalogPath != "" && (!filepath.IsAbs(options.DirectToolModelCatalogPath) || filepath.Clean(options.DirectToolModelCatalogPath) != options.DirectToolModelCatalogPath) {
		return nil, errors.New("create Codex Runtime: clean absolute direct-tool model catalog path is required")
	}
	if options.ClientVersion == "" {
		options.ClientVersion = "dev"
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.MinimumVersion != "" && !stableSemanticVersionPattern.MatchString(options.MinimumVersion) {
		return nil, errors.New("create Codex Runtime: minimum version must be a stable semantic version")
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = append([]string(nil), options.Environment...)
	baseEnvironment := append([]string(nil), options.Environment...)
	networkSettings := agentapp.RuntimeNetworkSettings{}
	if options.NetworkSettingsPath != "" {
		if !filepath.IsAbs(options.NetworkSettingsPath) || filepath.Clean(options.NetworkSettingsPath) != options.NetworkSettingsPath {
			return nil, errors.New("create Codex Runtime: clean absolute network settings path is required")
		}
		settings, settingsErr := readNetworkSettings(options.NetworkSettingsPath)
		if settingsErr == nil {
			options.Environment = environmentWithProxy(options.Environment, settings.ProxyURL)
			networkSettings = settings
		}
	}
	if options.MCPTokenEnvironmentVariable == "" {
		options.MCPTokenEnvironmentVariable = defaultMCPTokenEnvironmentVariable
	}
	if !environmentNamePattern.MatchString(options.MCPTokenEnvironmentVariable) {
		return nil, errors.New("create Codex Runtime: invalid MCP token environment variable name")
	}
	if options.BaseInstructions == "" {
		options.BaseInstructions = "Use ShotForward tools for project facts. First declare each Turn as discussion, clarification, or project_change. Discussion and clarification should be answered directly without a ChangeSet. A user request to create or edit a script, Asset definition/Prompt, Storyboard structure/Prompt, or dialogue is a persistent project_change: read current state, create one or more small semantic ChangeSets, and finalize each. Keep every ChangeSet to one semantic scope: one Asset, one Clip, one Scene with its Clips, or one Episode script; split cross-scope work into multiple ChangeSets. Build large Storyboards as one Scene ChangeSet at a time; after each finalize, re-read Scenes before appending the next Scene. Each Clip has independent first-frame, first-and-last-frame, and reference-mode video Prompts; initialize or update only the requested mode. Storyboard video prompts are Clip-local generation instructions: never prefix duration or timestamp Shot 1, and use only Clip-local times for later cuts. Reference-mode Prompts must bind and mention every depended-on Asset by exact @AssetName even before it has an image; keyframe-mode Prompts may rely on their input frames. For long work, send a concise progress update before drafting and after each semantic group so the user is never left with only a final status. Finalize auto-applies safe local edits and the UI provides deterministic Undo, so report what was applied. Attached images are temporary conversation inputs. When the user explicitly requests image generation, call the ShotForward generate_conversation_image tool; do not call a Runtime-native image-generation tool. The ShotForward tool automatically uses images attached to the current Turn and adds the generated image to the conversation. Generated images stay in the conversation until the user manually applies them to a video unit or saves them as an Asset. Never decide between creating a new Asset and a new version when the request is ambiguous: briefly ask which result the user wants and explain that a version updates the same Asset while a new Asset is independently selectable. Never automatically write conversation media into Assets or video units. Never modify project files, generate video or audio, change Provider configuration, preview, or export through Agent tools."
	}
	options.BaseInstructions += agentapp.AssetReuseInstructions + agentapp.VideoPromptInstructions
	if deps.discover == nil || deps.version == nil || deps.newClient == nil || (options.DirectToolModelCatalogPath != "" && deps.prepareModelCatalog == nil) {
		return nil, errors.New("create Codex Runtime: dependencies are required")
	}
	return &Supervisor{
		options:          options,
		deps:             deps,
		snapshot:         agentapp.RuntimeSnapshot{Health: agentapp.RuntimeNotInstalled, FailureCode: "codex_not_installed", FailureSummary: "Codex Runtime has not been started"},
		subscribers:      make(map[string]map[uint64]chan agentapp.RuntimeEvent),
		activeTurns:      make(map[string]string),
		pendingTurns:     make(map[string]*pendingTurnStart),
		compactions:      make(map[string]*contextCompaction),
		threadWorkspaces: make(map[string]string),
		networkSettings:  networkSettings,
		baseEnvironment:  baseEnvironment,
	}, nil
}

func (s *Supervisor) Snapshot(ctx context.Context) (agentapp.RuntimeSnapshot, error) {
	if err := s.ensureStarted(ctx); err != nil {
		s.mu.Lock()
		snapshot := s.snapshot
		s.mu.Unlock()
		return snapshot, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *Supervisor) Models(ctx context.Context) ([]agentapp.Model, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentapp.Model(nil), s.models...), nil
}

func (s *Supervisor) ensureStarted(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start Codex Runtime: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSupervisorClosed
	}
	if s.stopping {
		s.mu.Unlock()
		return errSupervisorStopping
	}
	if s.client != nil && (s.snapshot.Health == agentapp.RuntimeReady || s.snapshot.Health == agentapp.RuntimeLoginRequired) {
		s.mu.Unlock()
		return nil
	}
	attempt := s.starting
	if attempt == nil {
		if s.startErr != nil && time.Now().Before(s.startRetryAt) {
			err := s.startErr
			s.mu.Unlock()
			return err
		}
		s.startupGeneration++
		ownerContext, cancel := context.WithTimeout(context.Background(), s.options.StartupTimeout)
		attempt = &startupAttempt{done: make(chan struct{}), cancel: cancel, generation: s.startupGeneration}
		s.starting = attempt
		s.snapshot = agentapp.RuntimeSnapshot{Health: agentapp.RuntimeStarting}
		options := s.options
		options.Environment = append([]string(nil), options.Environment...)
		go s.runStartup(ownerContext, attempt, options)
	}
	s.mu.Unlock()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) runStartup(ctx context.Context, attempt *startupAttempt, options Options) {
	defer attempt.cancel()
	startedAt := time.Now()
	// Build on isolated state so a cancelled generation cannot publish a late
	// failure/Ready snapshot, or read settings while ConfigureNetwork updates them.
	deps := s.deps
	newClient := deps.newClient
	deps.newClient = func(executable string, args []string, cwd string, environment []string, timeout time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
		if notification != nil {
			notification = func(value rpcNotification) {
				s.mu.Lock()
				current := s.startupGeneration == attempt.generation && !s.stopping && !s.closed
				s.mu.Unlock()
				if current {
					s.onNotification(value)
				}
			}
		}
		if exited != nil {
			exited = func(err error) {
				s.mu.Lock()
				attempt.exited = true
				s.mu.Unlock()
				s.onExitGeneration(attempt.generation, err)
			}
		}
		return newClient(executable, args, cwd, environment, timeout, notification, exited)
	}
	builder := &Supervisor{options: options, deps: deps}
	err := builder.start(ctx)
	s.mu.Lock()
	current := s.startupGeneration == attempt.generation && !s.stopping && !s.closed
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
		builder.snapshot.Health = agentapp.RuntimeConfigurationError
		builder.snapshot.FailureCode = "codex_startup_timeout"
		builder.snapshot.FailureSummary = "Codex Runtime initialization timed out"
	}
	if attempt.exited && err == nil {
		err = errors.New("Codex app-server disconnected during startup")
		builder.snapshot.Health = agentapp.RuntimeDisconnected
		builder.snapshot.FailureCode = "codex_disconnected"
		builder.snapshot.FailureSummary = "Codex app-server disconnected during startup"
	}
	if current {
		s.snapshot = builder.snapshot
		if err == nil {
			s.client, s.models = builder.client, builder.models
		} else {
			s.startErr = err
			s.startRetryAt = time.Now().Add(defaultStartupRetryBackoff)
		}
	} else if err == nil {
		err = ctx.Err()
		if err == nil {
			err = context.Canceled
		}
	}
	s.mu.Unlock()
	if (!current || err != nil) && builder.client != nil {
		builder.closeClient(builder.client)
	}
	s.mu.Lock()
	current = s.startupGeneration == attempt.generation && !s.stopping && !s.closed
	if !current && err == nil {
		err = context.Canceled
	}
	if current && err == nil {
		s.startErr, s.startRetryAt = nil, time.Time{}
	}
	attempt.err = err
	if s.starting == attempt {
		s.starting = nil
	}
	options.Logger.Info("Codex Runtime initialization completed", "generation", attempt.generation,
		"elapsed_ms", time.Since(startedAt).Milliseconds(), "success", err == nil)
	close(attempt.done)
	s.mu.Unlock()
}

func (s *Supervisor) start(ctx context.Context) error {
	phaseStartedAt := time.Now()
	executable, err := s.deps.discover(s.options.ConfiguredExecutable)
	s.logPhase("discover", phaseStartedAt, err)
	if err != nil {
		s.setFailure(agentapp.RuntimeNotInstalled, "codex_not_installed", "Codex executable was not found", "", "")
		return err
	}
	versionContext, cancelVersion := context.WithTimeout(ctx, s.options.RequestTimeout)
	phaseStartedAt = time.Now()
	version, err := s.deps.version(versionContext, executable)
	cancelVersion()
	s.logPhase("version", phaseStartedAt, err)
	if err != nil {
		s.setFailure(agentapp.RuntimeIncompatible, "codex_version_failed", safeSummary(err), executable, "")
		return err
	}
	if s.options.MinimumVersion != "" {
		compatible, compareErr := versionAtLeast(version, s.options.MinimumVersion)
		if compareErr != nil || !compatible {
			summary := fmt.Sprintf("Codex %s is too old. ShotForward requires Codex %s or newer. Update the local Codex installation and retry.", version, s.options.MinimumVersion)
			if compareErr != nil {
				summary = fmt.Sprintf("Codex version %q could not be verified. ShotForward requires Codex %s or newer. Update the local Codex installation and retry.", version, s.options.MinimumVersion)
			}
			s.setUpgradeRequired(summary, executable, version, s.options.MinimumVersion)
			return errors.New(summary)
		}
	}

	// Agent image generation is owned by ShotForward MCP so runtime selection,
	// reference handling, and conversation import all follow one path. The
	// isolated Codex subscription image worker is a separate app-server and does
	// not receive this setting.
	additionalConfig := append([]string{"features.image_generation=false"}, s.options.AdditionalConfig...)
	if s.options.DirectToolModelCatalogPath != "" {
		phaseStartedAt = time.Now()
		catalogContext, cancelCatalog := context.WithTimeout(ctx, s.options.RequestTimeout)
		err = s.deps.prepareModelCatalog(catalogContext, executable, s.options.DirectToolModelCatalogPath, s.options.WorkingDirectory, s.options.Environment)
		cancelCatalog()
		s.logPhase("bundled_model_catalog", phaseStartedAt, err)
		if err != nil {
			s.setFailure(agentapp.RuntimeConfigurationError, "codex_model_catalog_failed", safeSummary(err), executable, version)
			return err
		}
		additionalConfig = append(additionalConfig, `model_catalog_json=`+strconv.Quote(s.options.DirectToolModelCatalogPath))
	}
	baseArgs := baseConfigArguments(additionalConfig)
	probe, err := s.deps.newClient(executable, append([]string{"app-server", "--stdio"}, baseArgs...), s.options.WorkingDirectory, s.options.Environment, s.options.RequestTimeout, nil, nil)
	if err != nil {
		s.setFailure(agentapp.RuntimeConfigurationError, "codex_start_failed", safeSummary(err), executable, version)
		return err
	}
	phaseStartedAt = time.Now()
	err = initialize(ctx, probe, s.options.ClientVersion)
	s.logPhase("probe_initialize", phaseStartedAt, err)
	if err != nil {
		s.closeClient(probe)
		s.setStartupRPCFailure(err, executable, version)
		return err
	}
	disableArgs, err := inspectExtensionsWithLogger(ctx, probe, s.options.Logger)
	phaseStartedAt = time.Now()
	s.closeClient(probe)
	s.logPhase("probe_close", phaseStartedAt, nil)
	safeExecutionAvailable := false
	safeExecutionFailureCode := ""
	safeExecutionFailureSummary := ""
	if err == nil {
		phaseStartedAt = time.Now()
		preflightErr := s.preflightSandbox(ctx, executable, baseArgs, disableArgs)
		s.logPhase("sandbox_preflight", phaseStartedAt, preflightErr)
		if preflightErr != nil {
			safeExecutionFailureCode = "codex_sandbox_unavailable"
			safeExecutionFailureSummary = "Safe code execution is unavailable on this device"
			s.options.Logger.Warn("Codex sandbox preflight failed", "error", safeSummary(preflightErr), "platform", runtime.GOOS)
		} else {
			safeExecutionAvailable = true
		}
	}
	if err != nil {
		s.setStartupRPCFailure(err, executable, version)
		return err
	}

	formalArgs := append(append([]string{"app-server", "--stdio"}, baseArgs...), disableArgs...)
	if safeExecutionAvailable {
		formalArgs = append(formalArgs, safeExecutionConfigArguments(executable)...)
	}
	client, err := s.deps.newClient(executable, formalArgs, s.options.WorkingDirectory, s.options.Environment, s.options.RequestTimeout, s.onNotification, s.onExit)
	if err != nil {
		s.setFailure(agentapp.RuntimeConfigurationError, "codex_start_failed", safeSummary(err), executable, version)
		return err
	}
	phaseStartedAt = time.Now()
	err = initializeWithCapabilities(ctx, client, s.options.ClientVersion, safeExecutionAvailable)
	s.logPhase("formal_initialize", phaseStartedAt, err)
	if err != nil {
		s.closeClient(client)
		s.setStartupRPCFailure(err, executable, version)
		return err
	}
	var account struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	phaseStartedAt = time.Now()
	err = client.Request(ctx, "account/read", map[string]any{"refreshToken": false}, &account)
	s.logPhase("account_read", phaseStartedAt, err)
	if err != nil {
		s.closeClient(client)
		s.setStartupRPCFailure(err, executable, version)
		return err
	}
	phaseStartedAt = time.Now()
	models, err := listModels(ctx, client)
	s.logPhase("model_list", phaseStartedAt, err)
	if err != nil {
		s.closeClient(client)
		s.setStartupRPCFailure(err, executable, version)
		return err
	}
	health := agentapp.RuntimeReady
	if account.RequiresOpenAIAuth && (len(account.Account) == 0 || string(account.Account) == "null") {
		health = agentapp.RuntimeLoginRequired
	}
	if health == agentapp.RuntimeReady && len(models) == 0 {
		s.closeClient(client)
		err := errors.New("model/list returned no available models")
		s.setFailure(agentapp.RuntimeConfigurationError, "codex_models_unavailable", err.Error(), executable, version)
		return err
	}
	s.mu.Lock()
	s.client = client
	s.models = models
	s.snapshot = agentapp.RuntimeSnapshot{
		Health: health, Version: version, Executable: executable,
		SafeExecutionAvailable:      safeExecutionAvailable,
		SafeExecutionBackend:        safeExecutionBackend(safeExecutionAvailable),
		SafeExecutionFailureCode:    safeExecutionFailureCode,
		SafeExecutionFailureSummary: safeExecutionFailureSummary,
	}
	s.mu.Unlock()
	s.options.Logger.Info("Codex app-server started",
		"executable", executable,
		"version", version,
		"health", health,
		"working_directory", s.options.WorkingDirectory)
	return nil
}

func (s *Supervisor) logPhase(phase string, startedAt time.Time, err error) {
	logRuntimePhase(s.options.Logger, phase, startedAt, err)
}

// Timing diagnostics deliberately omit RPC params, extension names, routes,
// environment values, and errors that might contain account or token data.
func logRuntimePhase(logger *slog.Logger, phase string, startedAt time.Time, err error) {
	if logger != nil {
		logger.Info("Codex Runtime phase completed", "phase", phase,
			"elapsed_ms", time.Since(startedAt).Milliseconds(), "success", err == nil)
	}
}

func initialize(ctx context.Context, client protocolClient, version string) error {
	return initializeWithCapabilities(ctx, client, version, false)
}

func initializeWithCapabilities(ctx context.Context, client protocolClient, version string, experimentalAPI bool) error {
	params := map[string]any{"clientInfo": map[string]any{"name": "shotforward_video_studio", "title": "ShotForward Video Studio", "version": version}}
	if experimentalAPI {
		params["capabilities"] = map[string]any{"experimentalApi": true}
	}
	var result json.RawMessage
	if err := client.Request(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return client.Notify("initialized", map[string]any{})
}

func baseConfigArguments(additional []string) []string {
	values := []string{
		"analytics.enabled=false", `otel.exporter="none"`, "otel.log_user_prompt=false",
		"project_doc_max_bytes=0",
		"features.shell_tool=false", "agents.enabled=false", "memories.generate_memories=false",
		"feedback.enabled=false", "apps._default.enabled=false",
	}
	values = append(values, additional...)
	args := make([]string, 0, len(values)*2)
	for _, value := range values {
		args = append(args, "-c", value)
	}
	return args
}

func safeExecutionConfigArguments(executable string) []string {
	return safeExecutionConfigArgumentsForOS(executable, runtime.GOOS)
}

func safeExecutionConfigArgumentsForOS(executable, goos string) []string {
	runtimeReadRoot := codexRuntimeReadRoot(executable)
	filesystem := `{":minimal"="read",":workspace_roots"={"."="write"}`
	if runtimeReadRoot != "" {
		filesystem += `,` + strconv.Quote(runtimeReadRoot) + `="read"`
	}
	filesystem += `}`
	values := []string{
		"features.shell_tool=true",
		"allow_login_shell=false",
		`shell_environment_policy.inherit="core"`,
		"shell_environment_policy.ignore_default_excludes=false",
		"shell_environment_policy.experimental_use_profile=false",
		`shell_environment_policy.filters."SHOTFORWARD_*"="exclude"`,
		`shell_environment_policy.filters."OPENAI_*"="exclude"`,
		`shell_environment_policy.filters."AWS_*"="exclude"`,
		`shell_environment_policy.filters."AZURE_*"="exclude"`,
		`shell_environment_policy.filters."GOOGLE_*"="exclude"`,
		`shell_environment_policy.filters."GITHUB_*"="exclude"`,
		`shell_environment_policy.filters."GH_*"="exclude"`,
		`default_permissions="` + safeExecutionPermissionProfile + `"`,
		`permissions.` + safeExecutionPermissionProfile + `.description="ShotForward session workspace"`,
		`permissions.` + safeExecutionPermissionProfile + `.filesystem=` + filesystem,
		`permissions.` + safeExecutionPermissionProfile + `.network.enabled=false`,
	}
	if goos == "windows" {
		values = append(values, `windows.sandbox="elevated"`)
	}
	args := make([]string, 0, len(values)*2)
	for _, value := range values {
		args = append(args, "-c", value)
	}
	return args
}

func codexRuntimeReadRoot(executable string) string {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	resolved = filepath.Clean(resolved)
	separator := string(filepath.Separator)
	marker := separator + filepath.Join("node_modules", "@openai", "codex")
	searchPath := resolved
	searchMarker := marker
	if runtime.GOOS == "windows" {
		searchPath = strings.ToLower(searchPath)
		searchMarker = strings.ToLower(searchMarker)
	}
	if index := strings.Index(searchPath, searchMarker); index >= 0 {
		return resolved[:index+len(marker)]
	}
	return resolved
}

func safeExecutionBackend(available bool) string {
	if !available {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return "codex_macos_seatbelt"
	case "windows":
		return "codex_windows_sandbox"
	case "linux":
		return "codex_linux_sandbox"
	default:
		return "codex_native_sandbox"
	}
}

func (s *Supervisor) preflightSandbox(ctx context.Context, executable string, baseArgs, disableArgs []string) error {
	workspace, err := os.MkdirTemp(s.options.SessionWorkspaceRoot, ".sandbox-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	deniedFile, err := os.CreateTemp(s.options.SessionWorkspaceRoot, ".sandbox-denied-")
	if err != nil {
		return err
	}
	deniedPath := deniedFile.Name()
	if closeErr := deniedFile.Close(); closeErr != nil {
		return closeErr
	}
	defer os.Remove(deniedPath)
	outputPath := filepath.Join(workspace, "write-check")
	args := append(append(append([]string{"app-server", "--stdio"}, baseArgs...), disableArgs...), safeExecutionConfigArguments(executable)...)
	client, err := s.deps.newClient(executable, args, s.options.WorkingDirectory, s.options.Environment, s.options.RequestTimeout, nil, nil)
	if err != nil {
		return err
	}
	defer s.closeClient(client)
	if err := initializeWithCapabilities(ctx, client, s.options.ClientVersion, true); err != nil {
		return err
	}
	command := []string{"/bin/sh", "-c", `test ! -r "$1" && test -z "${SHOTFORWARD_MCP_TOKEN:-}" && : > "$2"`, "shotforward-sandbox-preflight", deniedPath, outputPath}
	if runtime.GOOS == "windows" {
		batchPath := filepath.Join(workspace, "preflight.cmd")
		batch := "@echo off\r\nif defined SHOTFORWARD_MCP_TOKEN exit /b 10\r\nif exist \"%~1\" exit /b 11\r\ntype nul > \"%~2\"\r\n"
		if err := os.WriteFile(batchPath, []byte(batch), 0o600); err != nil {
			return err
		}
		command = []string{"cmd.exe", "/d", "/c", batchPath, deniedPath, outputPath}
	}
	var result struct {
		ExitCode int    `json:"exitCode"`
		Stderr   string `json:"stderr"`
	}
	params := map[string]any{
		"command": command, "cwd": workspace,
		"permissionProfile": safeExecutionPermissionProfile,
		"timeoutMs":         5_000,
	}
	if err := client.Request(ctx, "command/exec", params, &result); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("sandbox preflight exited with status %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if info, err := os.Stat(outputPath); err != nil || !info.Mode().IsRegular() {
		return errors.New("sandbox preflight could not write its workspace")
	}
	return nil
}

func inspectExtensions(ctx context.Context, client protocolClient) ([]string, error) {
	return inspectExtensionsWithLogger(ctx, client, nil)
}

func inspectExtensionsWithLogger(ctx context.Context, client protocolClient, logger *slog.Logger) ([]string, error) {
	var configResult struct {
		Config struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	startedAt := time.Now()
	err := client.Request(ctx, "config/read", map[string]any{"includeLayers": true}, &configResult)
	logRuntimePhase(logger, "config_read", startedAt, err)
	if err != nil {
		return nil, err
	}
	top := make(map[string]struct{})
	for name := range configResult.Config.MCPServers {
		if name != shotForwardMCPName {
			top[name] = struct{}{}
		}
	}
	type status struct {
		Name     string `json:"name"`
		PluginID string `json:"pluginId"`
	}
	var statuses []status
	var cursor *string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page struct {
			Data       []status `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		params := map[string]any{"detail": "toolsAndAuthOnly", "limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		startedAt := time.Now()
		err := client.Request(ctx, "mcpServerStatus/list", params, &page)
		logRuntimePhase(logger, "mcp_extension_list", startedAt, err)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, page.Data...)
		if page.NextCursor == nil {
			break
		}
		if cursor != nil && *page.NextCursor == *cursor {
			return nil, errors.New("mcpServerStatus/list repeated its cursor")
		}
		cursor = page.NextCursor
		if pageNumber == 99 {
			return nil, errors.New("mcpServerStatus/list exceeded pagination limit")
		}
	}
	var values []string
	for name := range top {
		values = append(values, `mcp_servers.`+strconv.Quote(name)+`={enabled=false,url="http://127.0.0.1:1"}`)
	}
	for _, item := range statuses {
		if item.PluginID != "" && item.Name != "" {
			values = append(values, `plugins.`+strconv.Quote(item.PluginID)+`.mcp_servers.`+strconv.Quote(item.Name)+`.enabled=false`)
		}
	}
	sort.Strings(values)
	args := make([]string, 0, len(values)*2)
	for _, value := range values {
		args = append(args, "-c", value)
	}
	return args, nil
}

func listModels(ctx context.Context, client protocolClient) ([]agentapp.Model, error) {
	var result []agentapp.Model
	var cursor *string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
				Hidden      bool   `json:"hidden"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		if err := client.Request(ctx, "model/list", params, &page); err != nil {
			return nil, err
		}
		for _, model := range page.Data {
			if model.ID != "" && !model.Hidden {
				result = append(result, agentapp.Model{ID: model.ID, DisplayName: model.DisplayName, Hidden: false})
			}
		}
		if page.NextCursor == nil {
			break
		}
		if cursor != nil && *page.NextCursor == *cursor {
			return nil, errors.New("model/list repeated its cursor")
		}
		cursor = page.NextCursor
		if pageNumber == 99 {
			return nil, errors.New("model/list exceeded pagination limit")
		}
	}
	return result, nil
}

func (s *Supervisor) StartLogin(ctx context.Context, mode agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return agentapp.LoginChallenge{}, err
	}
	params := map[string]any{}
	switch mode {
	case agentapp.LoginChatGPTBrowser:
		params["type"] = "chatgpt"
		params["useHostedLoginSuccessPage"] = true
		params["appBrand"] = "chatgpt"
	case agentapp.LoginChatGPTDeviceCode:
		params["type"] = "chatgptDeviceCode"
	default:
		return agentapp.LoginChallenge{}, errors.New("start Codex login: unsupported login mode")
	}
	s.mu.Lock()
	client := s.client
	closed, stopping := s.closed, s.stopping
	s.mu.Unlock()
	if closed {
		return agentapp.LoginChallenge{}, errSupervisorClosed
	}
	if stopping || client == nil {
		return agentapp.LoginChallenge{}, errSupervisorStopping
	}
	var response struct {
		LoginID         string `json:"loginId"`
		AuthURL         string `json:"authUrl"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := client.Request(ctx, "account/login/start", params, &response); err != nil {
		return agentapp.LoginChallenge{}, err
	}
	if response.LoginID == "" || (mode == agentapp.LoginChatGPTBrowser && response.AuthURL == "") || (mode == agentapp.LoginChatGPTDeviceCode && (response.VerificationURL == "" || response.UserCode == "")) {
		return agentapp.LoginChallenge{}, errors.New("account/login/start returned an incomplete challenge")
	}
	return agentapp.LoginChallenge{LoginID: response.LoginID, AuthURL: response.AuthURL, VerificationURL: response.VerificationURL, UserCode: response.UserCode}, nil
}

func (s *Supervisor) StartThread(ctx context.Context, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	return s.threadRequest(ctx, "thread/start", "", request)
}

func (s *Supervisor) ResumeThread(ctx context.Context, threadID string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	return s.threadRequest(ctx, "thread/resume", threadID, request)
}

func (s *Supervisor) threadRequest(ctx context.Context, method, threadID string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	client, err := s.readyClient(ctx)
	if err != nil {
		return agentapp.Thread{}, err
	}
	s.mu.Lock()
	environment := append([]string(nil), s.options.Environment...)
	generation := s.startupGeneration
	current := s.client == client && !s.closed && !s.stopping
	s.mu.Unlock()
	if !current {
		return agentapp.Thread{}, errSupervisorStopping
	}
	if request.AgentSessionID.Validate() != nil || request.ProjectID.Validate() != nil || request.EpisodeID.Validate() != nil || request.ModelID == "" || !validSessionWorkspace(s.options.SessionWorkspaceRoot, request.WorkspacePath) || !validMCPBindingURL(request.MCPBindingURL) || !hasNonEmptyEnvironment(environment, s.options.MCPTokenEnvironmentVariable) {
		return agentapp.Thread{}, errors.New("Codex thread request is invalid")
	}
	s.mu.Lock()
	modelAvailable := false
	for _, model := range s.models {
		if model.ID == request.ModelID {
			modelAvailable = true
			break
		}
	}
	s.mu.Unlock()
	if !modelAvailable {
		return agentapp.Thread{}, errors.New("Codex model is not available")
	}
	if method == "thread/resume" && strings.TrimSpace(threadID) == "" {
		return agentapp.Thread{}, errors.New("Codex Thread ID is required")
	}
	s.mu.Lock()
	safeExecutionAvailable := s.snapshot.SafeExecutionAvailable
	s.mu.Unlock()
	params := map[string]any{
		"cwd": request.WorkspacePath, "model": request.ModelID, "approvalPolicy": "never",
		"baseInstructions": s.options.BaseInstructions,
	}
	if safeExecutionAvailable {
		params["permissions"] = safeExecutionPermissionProfile
		params["runtimeWorkspaceRoots"] = []string{request.WorkspacePath}
	} else {
		params["sandbox"] = "read-only"
	}
	if method == "thread/start" {
		params["ephemeral"] = false
		params["serviceName"] = "shotforward_agent"
	} else {
		params["threadId"] = threadID
		// Resume native history in the Runtime, not in this JSONL response.
		// Display and recovery load their bounded pages independently.
		params["excludeTurns"] = true
	}
	config := map[string]any{"apps": map[string]any{"_default": map[string]any{"enabled": false}}}
	if request.WebSearchDisabled {
		config["web_search"] = "disabled"
	}
	if request.MCPBindingURL != "" {
		config["mcp_servers"] = map[string]any{shotForwardMCPName: map[string]any{
			"url": request.MCPBindingURL, "bearer_token_env_var": s.options.MCPTokenEnvironmentVariable,
			"required": true, "enabled_tools": agentapp.ShotForwardMCPToolNames(),
			"default_tools_approval_mode": "approve", "startup_timeout_sec": 10, "tool_timeout_sec": 600,
		}}
	}
	params["config"] = config
	var response struct {
		Thread wireThread `json:"thread"`
	}
	startedAt := time.Now()
	err = client.Request(ctx, method, params, &response)
	s.logPhase(method, startedAt, err)
	if err != nil {
		if method == "thread/resume" && isThreadUnavailable(err) {
			return agentapp.Thread{}, errors.Join(agentapp.ErrThreadUnavailable, err)
		}
		return agentapp.Thread{}, err
	}
	mapped := mapThread(response.Thread, request.ModelID)
	if mapped.CodexThreadID == "" || mapped.CodexSessionID == "" {
		return agentapp.Thread{}, fmt.Errorf("%s returned an incomplete Thread", method)
	}
	if err := verifyThreadMCPWithLogger(ctx, client, mapped.CodexThreadID, request, s.options.Logger); err != nil {
		return agentapp.Thread{}, fmt.Errorf("%s verify ShotForward MCP: %w", method, err)
	}
	s.mu.Lock()
	if s.startupGeneration != generation || s.client != client || s.closed || s.stopping {
		s.mu.Unlock()
		return agentapp.Thread{}, errSupervisorStopping
	}
	s.threadWorkspaces[mapped.CodexThreadID] = request.WorkspacePath
	s.mu.Unlock()
	return mapped, nil
}

func isThreadUnavailable(err error) bool {
	var rpcErr *rpcError
	return errors.As(err, &rpcErr) && strings.Contains(strings.ToLower(rpcErr.Message), "thread not found")
}

func verifyThreadMCP(ctx context.Context, client protocolClient, threadID string, expected agentapp.ThreadRequest) error {
	return verifyThreadMCPWithLogger(ctx, client, threadID, expected, nil)
}

func verifyThreadMCPWithLogger(ctx context.Context, client protocolClient, threadID string, expected agentapp.ThreadRequest, logger *slog.Logger) (returnErr error) {
	verificationContext, cancel := context.WithTimeout(ctx, defaultMCPVerificationTimeout)
	defer cancel()
	startedAt := time.Now()
	connectionDuration := time.Duration(0)
	pollCount := 0
	connected := false
	defer func() {
		if connectionDuration == 0 {
			connectionDuration = time.Since(startedAt)
		}
		if logger != nil {
			logger.Info("Codex MCP connection verification completed", "elapsed_ms", connectionDuration.Milliseconds(),
				"poll_count", pollCount, "connected", connected)
		}
	}()
	lastStatus := "not reported"
	for {
		pollCount++
		status, err := threadMCPConnectionStatus(verificationContext, client, threadID)
		if err != nil {
			if verificationContext.Err() != nil {
				return fmt.Errorf("ShotForward MCP did not connect before timeout (last status %q): %w", lastStatus, verificationContext.Err())
			}
			return err
		}
		if status != "" {
			lastStatus = status
		}
		if status == "connected" {
			connected = true
			connectionDuration = time.Since(startedAt)
			break
		}
		if status == "authenticationRequired" || status == "failed" || status == "cancelled" || status == "disabled" {
			return fmt.Errorf("ShotForward MCP connection entered terminal status %q", status)
		}
		select {
		case <-verificationContext.Done():
			return fmt.Errorf("ShotForward MCP did not connect before timeout (last status %q): %w", lastStatus, verificationContext.Err())
		case <-time.After(defaultMCPVerificationPollInterval):
		}
	}

	startedAt = time.Now()
	err := verifyThreadMCPScope(verificationContext, client, threadID, expected)
	logRuntimePhase(logger, "mcp_scope_verify", startedAt, err)
	return err
}

func threadMCPConnectionStatus(ctx context.Context, client protocolClient, threadID string) (string, error) {
	type serverStatus struct {
		Name          string `json:"name"`
		RuntimeStatus string `json:"runtimeStatus"`
	}
	connectionStatus := ""
	var cursor *string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page struct {
			Data       []serverStatus `json:"data"`
			NextCursor *string        `json:"nextCursor"`
		}
		params := map[string]any{"threadId": threadID, "detail": "toolsAndAuthOnly", "limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		if err := client.Request(ctx, "mcpServerStatus/list", params, &page); err != nil {
			return "", err
		}
		for _, status := range page.Data {
			if status.Name == shotForwardMCPName {
				connectionStatus = status.RuntimeStatus
			}
		}
		if page.NextCursor == nil {
			break
		}
		if cursor != nil && *page.NextCursor == *cursor {
			return "", errors.New("mcpServerStatus/list repeated its cursor")
		}
		cursor = page.NextCursor
		if pageNumber == 99 {
			return "", errors.New("mcpServerStatus/list exceeded pagination limit")
		}
	}
	return connectionStatus, nil
}

func verifyThreadMCPScope(ctx context.Context, client protocolClient, threadID string, expected agentapp.ThreadRequest) error {
	var result struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := client.Request(ctx, "mcpServer/tool/call", map[string]any{
		"threadId": threadID, "server": shotForwardMCPName,
		"tool": "get_session_context", "arguments": map[string]any{},
	}, &result); err != nil {
		return err
	}
	if result.IsError {
		return errors.New("get_session_context returned an error")
	}
	raw := result.StructuredContent
	if len(raw) == 0 || string(raw) == "null" {
		for _, content := range result.Content {
			if content.Type == "text" && content.Text != "" {
				raw = json.RawMessage(content.Text)
				break
			}
		}
	}
	var actual struct {
		SessionID string `json:"session_id"`
		ProjectID string `json:"project_id"`
		EpisodeID string `json:"episode_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &actual) != nil {
		return errors.New("get_session_context returned invalid content")
	}
	if actual.SessionID != expected.AgentSessionID.String() || actual.ProjectID != expected.ProjectID.String() || actual.EpisodeID != expected.EpisodeID.String() {
		return errors.New("get_session_context scope mismatch")
	}
	return nil
}

func (s *Supervisor) ReadThread(ctx context.Context, threadID string) (agentapp.Thread, error) {
	client, err := s.readyClient(ctx)
	if err != nil {
		return agentapp.Thread{}, err
	}
	if strings.TrimSpace(threadID) == "" {
		return agentapp.Thread{}, errors.New("Codex Thread ID is required")
	}
	var response struct {
		Thread wireThread `json:"thread"`
	}
	if err := client.Request(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &response); err != nil {
		return agentapp.Thread{}, err
	}
	mapped := mapThread(response.Thread, "")
	if mapped.CodexThreadID == "" || mapped.CodexSessionID == "" {
		return agentapp.Thread{}, errors.New("thread/read returned an incomplete Thread")
	}
	mapped.Turns, err = readTurnStatuses(ctx, client, threadID)
	if err != nil {
		return agentapp.Thread{}, err
	}
	return mapped, nil
}

func (s *Supervisor) ListConversation(ctx context.Context, threadID, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	client, err := s.readyClient(ctx)
	if err != nil {
		return agentapp.RuntimeConversationPage{}, err
	}
	if strings.TrimSpace(threadID) == "" || len(cursor) > maximumConversationCursorBytes || strings.ContainsRune(cursor, 0) || limit < 1 || limit > 50 {
		return agentapp.RuntimeConversationPage{}, errors.New("Codex conversation request is invalid")
	}
	values, nextCursor, err := readConversationTurns(ctx, client, threadID, cursor, limit)
	if err != nil {
		return agentapp.RuntimeConversationPage{}, err
	}
	result := agentapp.RuntimeConversationPage{Turns: make([]agentapp.RuntimeConversationTurn, 0, len(values)), NextCursor: nextCursor}
	totalBytes := 0
	// Codex returns the requested page newest-first. The application contract is
	// chronological within each page so clients can prepend older pages.
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if strings.TrimSpace(value.ID) == "" {
			continue
		}
		turn := agentapp.RuntimeConversationTurn{CodexTurnID: value.ID, Status: mapPersistedTurnStatus(value.Status)}
		for _, item := range value.Items {
			message, ok := mapConversationItem(item)
			if !ok {
				continue
			}
			totalBytes += len(message.Text)
			if totalBytes > maximumConversationPageBytes {
				return agentapp.RuntimeConversationPage{}, errors.New("thread/turns/list returned too much message content")
			}
			turn.Messages = append(turn.Messages, message)
		}
		result.Turns = append(result.Turns, turn)
	}
	return result, nil
}

type wireConversationTurn struct {
	ID     string                 `json:"id"`
	Status string                 `json:"status"`
	Items  []wireConversationItem `json:"items"`
}

type wireConversationItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Phase   string `json:"phase"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func mapConversationItem(value wireConversationItem) (agentapp.RuntimeConversationMessage, bool) {
	if strings.TrimSpace(value.ID) == "" {
		return agentapp.RuntimeConversationMessage{}, false
	}
	message := agentapp.RuntimeConversationMessage{ItemID: value.ID}
	switch value.Type {
	case "userMessage":
		message.Role = agentapp.ConversationUser
		parts := make([]string, 0, len(value.Content))
		for _, content := range value.Content {
			// ThreadItem.UserInput uses "text" in current app-server schemas, while
			// persisted/raw Codex message content uses "input_text". Accept both
			// representations so a Runtime version difference cannot erase the user
			// bubble from otherwise valid conversation history.
			if (content.Type == "text" || content.Type == "input_text" || content.Type == "inputText") && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
		message.Text = strings.Join(parts, "\n")
	case "agentMessage":
		message.Role = agentapp.ConversationAssistant
		message.Text = value.Text
		switch value.Phase {
		case "commentary":
			message.Phase = agentapp.ConversationPhaseCommentary
		case "final_answer":
			message.Phase = agentapp.ConversationPhaseFinal
		default:
			message.Phase = agentapp.ConversationPhaseUnknown
		}
	default:
		return agentapp.RuntimeConversationMessage{}, false
	}
	if strings.TrimSpace(message.Text) == "" || len(message.Text) > maximumConversationMessageBytes || strings.ContainsRune(message.Text, 0) {
		return agentapp.RuntimeConversationMessage{}, false
	}
	return message, true
}

type wireThread struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Preview   string `json:"preview"`
	Model     string `json:"model"`
	Turns     []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turns"`
}

func mapThread(value wireThread, fallbackModel string) agentapp.Thread {
	title := value.Name
	if title == "" {
		title = value.Preview
	}
	model := value.Model
	if model == "" {
		model = fallbackModel
	}
	turns := make([]agentapp.RuntimeTurnSnapshot, 0, len(value.Turns))
	for _, value := range value.Turns {
		if value.ID == "" {
			continue
		}
		turns = append(turns, agentapp.RuntimeTurnSnapshot{CodexTurnID: value.ID, Status: mapPersistedTurnStatus(value.Status)})
	}
	return agentapp.Thread{CodexThreadID: value.ID, CodexSessionID: value.SessionID, Title: title, ModelID: model, Turns: turns}
}

func mapPersistedTurnStatus(value string) agent.TurnStatus {
	switch value {
	case "inProgress", "running":
		return agent.TurnRunning
	case "waitingForInput", "waiting_user":
		return agent.TurnWaitingUser
	case "completed":
		return agent.TurnCompleted
	case "interrupted", "cancelled":
		return agent.TurnInterrupted
	default:
		return agent.TurnFailed
	}
}

func (s *Supervisor) StartTurn(ctx context.Context, request agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	client, err := s.readyClient(ctx)
	if err != nil {
		return agentapp.RuntimeTurn{}, err
	}
	if request.CodexThreadID == "" || strings.TrimSpace(request.ModelID) == "" || strings.TrimSpace(request.Text) == "" {
		return agentapp.RuntimeTurn{}, errors.New("Codex Turn request is invalid")
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	s.mu.Lock()
	workspace := s.threadWorkspaces[request.CodexThreadID]
	safeExecutionAvailable := s.snapshot.SafeExecutionAvailable
	if s.compactions[request.CodexThreadID] != nil || s.pendingTurns[request.CodexThreadID] != nil {
		s.mu.Unlock()
		return agentapp.RuntimeTurn{}, agentapp.ErrContextCompactionBusy
	}
	pending := &pendingTurnStart{}
	if s.pendingTurns == nil {
		s.pendingTurns = make(map[string]*pendingTurnStart)
	}
	s.pendingTurns[request.CodexThreadID] = pending
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.pendingTurns[request.CodexThreadID] == pending {
			delete(s.pendingTurns, request.CodexThreadID)
		}
		s.mu.Unlock()
	}()
	if workspace == "" || !validSessionWorkspace(s.options.SessionWorkspaceRoot, workspace) {
		return agentapp.RuntimeTurn{}, errors.New("Codex Turn workspace is unavailable")
	}
	imagePaths, err := prepareAgentInputImages(ctx, s.options.WorkingDirectory, request.ImagePaths)
	if err != nil {
		return agentapp.RuntimeTurn{}, err
	}
	input, err := turnInput(request.Text, imagePaths)
	if err != nil {
		return agentapp.RuntimeTurn{}, err
	}
	params := map[string]any{
		"threadId": request.CodexThreadID, "model": request.ModelID, "input": input,
		"approvalPolicy": "never", "cwd": workspace,
	}
	if safeExecutionAvailable {
		params["permissions"] = safeExecutionPermissionProfile
		params["runtimeWorkspaceRoots"] = []string{workspace}
	} else {
		params["sandboxPolicy"] = map[string]any{
			"type": "readOnly", "networkAccess": false,
		}
	}
	if err := client.Request(ctx, "turn/start", params, &response); err != nil {
		return agentapp.RuntimeTurn{}, err
	}
	if response.Turn.ID == "" {
		return agentapp.RuntimeTurn{}, errors.New("turn/start returned no Turn ID")
	}
	s.mu.Lock()
	if s.client == client && pending.completedTurnID != response.Turn.ID {
		s.activeTurns[request.CodexThreadID] = response.Turn.ID
	}
	s.mu.Unlock()
	return agentapp.RuntimeTurn{CodexTurnID: response.Turn.ID}, nil
}

func (s *Supervisor) SteerTurn(ctx context.Context, threadID, turnID, text string) error {
	return s.SteerTurnWithImages(ctx, threadID, turnID, text, nil)
}

func (s *Supervisor) SteerTurnWithImages(ctx context.Context, threadID, turnID, text string, imagePaths []string) error {
	client, err := s.readyClient(ctx)
	if err != nil {
		return err
	}
	if threadID == "" || turnID == "" || strings.TrimSpace(text) == "" {
		return errors.New("Codex steer request is invalid")
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	s.mu.Lock()
	compacting := s.compactions[threadID] != nil
	s.mu.Unlock()
	if compacting {
		return agentapp.ErrContextCompactionBusy
	}
	imagePaths, err = prepareAgentInputImages(ctx, s.options.WorkingDirectory, imagePaths)
	if err != nil {
		return err
	}
	input, err := turnInput(text, imagePaths)
	if err != nil {
		return err
	}
	err = client.Request(ctx, "turn/steer", map[string]any{"threadId": threadID, "expectedTurnId": turnID, "input": input}, &response)
	if err == nil && response.TurnID != turnID {
		return errors.New("turn/steer returned an unexpected Turn ID")
	}
	return err
}

func turnInput(text string, imagePaths []string) ([]map[string]any, error) {
	input := []map[string]any{{"type": "text", "text": text}}
	for _, imagePath := range imagePaths {
		if imagePath == "" || !filepath.IsAbs(imagePath) || filepath.Clean(imagePath) != imagePath {
			return nil, errors.New("Codex image input path is invalid")
		}
		input = append(input, map[string]any{"type": "localImage", "path": imagePath})
	}
	return input, nil
}

func (s *Supervisor) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	client, err := s.readyClient(ctx)
	if err != nil {
		return err
	}
	if threadID == "" || turnID == "" {
		return errors.New("Codex interrupt request is invalid")
	}
	return client.Request(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}

func (s *Supervisor) readyClient(ctx context.Context) (protocolClient, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Health == agentapp.RuntimeLoginRequired {
		return nil, errors.New("Codex login is required")
	}
	if s.client == nil || s.snapshot.Health != agentapp.RuntimeReady {
		return nil, errors.New("Codex Runtime is not ready")
	}
	return s.client, nil
}

func (s *Supervisor) Subscribe(ctx context.Context, threadID string) (<-chan agentapp.RuntimeEvent, error) {
	if ctx == nil || threadID == "" {
		return nil, errors.New("subscribe to Codex Runtime: context and Thread ID are required")
	}
	channel := make(chan agentapp.RuntimeEvent, 64)
	s.mu.Lock()
	s.nextSubscriber++
	id := s.nextSubscriber
	if s.subscribers[threadID] == nil {
		s.subscribers[threadID] = make(map[uint64]chan agentapp.RuntimeEvent)
	}
	s.subscribers[threadID][id] = channel
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if values := s.subscribers[threadID]; values != nil {
			if current := values[id]; current != nil {
				delete(values, id)
				close(current)
			}
			if len(values) == 0 {
				delete(s.subscribers, threadID)
			}
		}
		s.mu.Unlock()
	}()
	return channel, nil
}

func (s *Supervisor) onNotification(notification rpcNotification) {
	if notification.Method == "account/updated" || notification.Method == "account/login/completed" {
		go s.refreshAccount()
		return
	}
	if s.onCompactionNotification(notification) {
		return
	}
	event, ok := normalizeEvent(notification)
	if ok {
		if event.ToolName == "shell" {
			s.mu.Lock()
			event.SandboxedExecution = s.snapshot.SafeExecutionAvailable
			s.mu.Unlock()
		}
		if event.Type == agentapp.RuntimeTurnStarted {
			s.mu.Lock()
			s.activeTurns[event.CodexThreadID] = event.CodexTurnID
			s.mu.Unlock()
		} else if event.Type == agentapp.RuntimeTurnCompleted {
			s.mu.Lock()
			if s.activeTurns[event.CodexThreadID] == event.CodexTurnID {
				delete(s.activeTurns, event.CodexThreadID)
			}
			if pending := s.pendingTurns[event.CodexThreadID]; pending != nil {
				pending.completedTurnID = event.CodexTurnID
			}
			s.mu.Unlock()
		}
		s.emit(event)
	}
}

func (s *Supervisor) refreshAccount() {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.options.RequestTimeout)
	defer cancel()
	var account struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if client.Request(ctx, "account/read", map[string]any{"refreshToken": false}, &account) != nil {
		return
	}
	models, err := listModels(ctx, client)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != client {
		return
	}
	s.models = models
	if account.RequiresOpenAIAuth && (len(account.Account) == 0 || string(account.Account) == "null") {
		s.snapshot.Health = agentapp.RuntimeLoginRequired
	} else {
		s.snapshot.Health = agentapp.RuntimeReady
	}
}

func normalizeEvent(notification rpcNotification) (agentapp.RuntimeEvent, bool) {
	var params struct {
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
		ItemID    string `json:"itemId"`
		Delta     string `json:"delta"`
		WillRetry bool   `json:"willRetry"`
		Error     *struct {
			Message *string `json:"message"`
		} `json:"error"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Server    string `json:"server"`
			Tool      string `json:"tool"`
			SavedPath string `json:"savedPath"`
		} `json:"item"`
	}
	if json.Unmarshal(notification.Params, &params) != nil {
		return agentapp.RuntimeEvent{}, false
	}
	event := agentapp.RuntimeEvent{CodexThreadID: params.ThreadID, CodexTurnID: params.TurnID, OccurredAt: time.Now().UTC()}
	switch notification.Method {
	case "error":
		// The bundled app-server's ErrorNotification requires error, threadId,
		// turnId and willRetry. A recoverable request error is not a terminal
		// Turn failure; Codex owns its retry and subsequent completion.
		if !params.WillRetry || params.ThreadID == "" || params.TurnID == "" || params.Error == nil || params.Error.Message == nil {
			return agentapp.RuntimeEvent{}, false
		}
		event.Type = agentapp.RuntimeTurnRetrying
		event.FailureCode = agentapp.RuntimeRetryingFailureCode
	case "turn/started":
		event.Type = agentapp.RuntimeTurnStarted
		event.CodexTurnID = params.Turn.ID
	case "item/agentMessage/delta":
		event.Type = agentapp.RuntimeAssistantDelta
		event.ItemID = params.ItemID
		event.TextDelta = params.Delta
	case "item/started", "item/completed":
		event.ItemID = params.Item.ID
		if notification.Method == "item/completed" && params.Item.Type == "imageGeneration" {
			if !validGeneratedImagePath(params.Item.SavedPath) {
				return agentapp.RuntimeEvent{}, false
			}
			event.Type = agentapp.RuntimeImageGenerated
			event.GeneratedImagePath = params.Item.SavedPath
			break
		}
		event.ToolName = toolName(params.Item.Type, params.Item.Server, params.Item.Tool)
		if event.ToolName == "" {
			return agentapp.RuntimeEvent{}, false
		}
		if notification.Method == "item/started" {
			event.Type = agentapp.RuntimeToolStarted
		} else {
			event.Type = agentapp.RuntimeToolCompleted
		}
	case "turn/completed":
		event.Type = agentapp.RuntimeTurnCompleted
		event.CodexTurnID = params.Turn.ID
		event.TurnStatus = mapTurnStatus(params.Turn.Status)
		if event.TurnStatus == agent.TurnFailed {
			event.FailureCode = "codex_turn_failed"
			if params.Turn.Error != nil {
				event.FailureSummary = safeSummary(errors.New(params.Turn.Error.Message))
			}
			if event.FailureSummary == "" {
				event.FailureSummary = "Codex Turn failed"
			}
		}
	default:
		return agentapp.RuntimeEvent{}, false
	}
	return event, event.CodexThreadID != ""
}

func toolName(kind, server, tool string) string {
	switch kind {
	case "mcpToolCall":
		if server != "" && tool != "" {
			return server + "/" + tool
		}
	case "webSearch":
		return "web_search"
	case "commandExecution":
		return "shell"
	case "fileChange":
		return "file_change"
	case "dynamicToolCall":
		return "dynamic_tool"
	case "collabAgentToolCall", "subAgentActivity":
		return "subagent"
	}
	return ""
}

func mapTurnStatus(value string) agent.TurnStatus {
	switch value {
	case "completed":
		return agent.TurnCompleted
	case "interrupted":
		return agent.TurnInterrupted
	default:
		return agent.TurnFailed
	}
}

func (s *Supervisor) emit(event agentapp.RuntimeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, channel := range s.subscribers[event.CodexThreadID] {
		select {
		case channel <- event:
		default:
			<-channel
			channel <- agentapp.RuntimeEvent{Type: agentapp.RuntimeConnectionFailure, CodexThreadID: event.CodexThreadID, FailureCode: "event_stream_overflow", FailureSummary: "Agent event stream fell behind; reload the Thread history", OccurredAt: time.Now().UTC()}
		}
	}
}

func (s *Supervisor) onExit(err error) {
	s.onExitGeneration(0, err)
}

func (s *Supervisor) onExitGeneration(generation uint64, err error) {
	s.mu.Lock()
	if s.stopping || s.closed || (generation != 0 && generation != s.startupGeneration) || (s.starting != nil && s.client == nil) {
		s.mu.Unlock()
		return
	}
	s.client = nil
	s.models = nil
	activeTurnCount := len(s.activeTurns)
	s.activeTurns = make(map[string]string)
	s.pendingTurns = make(map[string]*pendingTurnStart)
	s.finishCompactionsLocked(errors.New("Codex app-server disconnected"))
	s.compactedTurns = nil
	s.threadWorkspaces = make(map[string]string)
	s.snapshot.Health = agentapp.RuntimeDisconnected
	s.snapshot.FailureCode = "codex_disconnected"
	s.snapshot.FailureSummary = "Codex app-server disconnected"
	executable := s.snapshot.Executable
	version := s.snapshot.Version
	threadIDs := make([]string, 0, len(s.subscribers))
	for id := range s.subscribers {
		threadIDs = append(threadIDs, id)
	}
	s.mu.Unlock()
	s.options.Logger.Error("Codex app-server disconnected",
		"error", safeSummary(err),
		"executable", executable,
		"version", version,
		"active_turn_count", activeTurnCount,
		"subscribed_thread_count", len(threadIDs),
		"working_directory", s.options.WorkingDirectory)
	for _, threadID := range threadIDs {
		s.emit(agentapp.RuntimeEvent{Type: agentapp.RuntimeConnectionFailure, CodexThreadID: threadID, FailureCode: "codex_disconnected", FailureSummary: "Codex app-server disconnected", OccurredAt: time.Now().UTC()})
	}
}

func (s *Supervisor) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close Codex Runtime: context is required")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.stop(ctx, true)
}

// stop fences the old generation before cancellation and waits for its cleanup.
// Close is terminal; ConfigureNetwork uses a nonterminal reset instead.
// The caller must hold lifecycleMu.
func (s *Supervisor) stop(ctx context.Context, permanent bool) error {
	s.mu.Lock()
	if permanent {
		s.closed = true
	}
	s.stopping = true
	s.startupGeneration++
	attempt := s.starting
	if attempt != nil {
		attempt.cancel()
	}
	client := s.client
	s.client = nil
	s.models = nil
	s.startErr, s.startRetryAt = nil, time.Time{}
	active := make(map[string]string, len(s.activeTurns))
	for threadID, turnID := range s.activeTurns {
		active[threadID] = turnID
	}
	s.activeTurns = make(map[string]string)
	s.pendingTurns = make(map[string]*pendingTurnStart)
	s.finishCompactionsLocked(errors.New("Codex Runtime stopped during context compaction"))
	s.compactedTurns = nil
	s.threadWorkspaces = make(map[string]string)
	s.snapshot = agentapp.RuntimeSnapshot{Health: agentapp.RuntimeDisconnected, FailureCode: "codex_restarting", FailureSummary: "Codex Runtime is restarting"}
	if permanent {
		s.snapshot.FailureCode = "codex_closed"
		s.snapshot.FailureSummary = "Codex Runtime is closed"
	}
	s.mu.Unlock()
	var closeErr error
	if client != nil {
		timeout := s.options.ShutdownTimeout
		if timeout <= 0 {
			timeout = defaultShutdownTimeout
		}
		shutdown, cancel := context.WithTimeout(ctx, timeout)
		for threadID, turnID := range active {
			_ = client.Request(shutdown, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
		}
		closeErr = client.Close(shutdown)
		cancel()
	}
	if attempt != nil {
		select {
		case <-attempt.done:
		case <-ctx.Done():
			return errors.Join(closeErr, ctx.Err())
		}
	}
	return closeErr
}

func (s *Supervisor) closeClient(client protocolClient) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ShutdownTimeout)
	defer cancel()
	_ = client.Close(ctx)
}

func (s *Supervisor) setStartupRPCFailure(err error, executable, version string) {
	health, code := agentapp.RuntimeConfigurationError, "codex_configuration_error"
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
		health, code = agentapp.RuntimeIncompatible, "codex_incompatible"
	}
	s.setFailure(health, code, safeSummary(err), executable, version)
}

func (s *Supervisor) setFailure(health agentapp.RuntimeHealth, code, summary, executable, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = agentapp.RuntimeSnapshot{Health: health, FailureCode: code, FailureSummary: safeSummary(errors.New(summary)), Executable: executable, Version: version}
}

func (s *Supervisor) setUpgradeRequired(summary, executable, version, minimumVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = agentapp.RuntimeSnapshot{
		Health: agentapp.RuntimeIncompatible, Version: version, MinimumVersion: minimumVersion,
		FailureCode: "codex_upgrade_required", FailureSummary: safeSummary(errors.New(summary)), Executable: executable,
	}
}

func versionAtLeast(version, minimum string) (bool, error) {
	actual, actualPrerelease, err := parseSemanticVersion(version)
	if err != nil {
		return false, err
	}
	required, _, err := parseSemanticVersion(minimum)
	if err != nil {
		return false, err
	}
	for index := range actual {
		if actual[index] != required[index] {
			return actual[index] > required[index], nil
		}
	}
	return !actualPrerelease, nil
}

func parseSemanticVersion(value string) ([3]int, bool, error) {
	match := semanticVersionPattern.FindStringSubmatch(value)
	if len(match) != 6 {
		return [3]int{}, false, fmt.Errorf("invalid semantic version %q", value)
	}
	var parsed [3]int
	for index := range parsed {
		component, err := strconv.Atoi(match[index+1])
		if err != nil {
			return [3]int{}, false, fmt.Errorf("invalid semantic version %q", value)
		}
		parsed[index] = component
	}
	return parsed, match[4] != "", nil
}

func readVersion(ctx context.Context, executable string) (string, error) {
	command := exec.CommandContext(ctx, executable, "--version")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run codex --version: %w", err)
	}
	match := versionPattern.FindSubmatch(output)
	if len(match) != 2 {
		return "", errors.New("parse codex --version output")
	}
	return string(match[1]), nil
}

func safeSummary(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(strings.ReplaceAll(err.Error(), "\x00", ""))
	value = sensitiveSummaryPattern.ReplaceAllString(value, "${1}<redacted>")
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}

func validSessionWorkspace(root, candidate string) bool {
	if candidate == "" || !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	return err == nil && resolvedRelative != "." && resolvedRelative != ".." && !strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator))
}

func validMCPBindingURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1" && parsed.Port() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path != ""
}

func hasNonEmptyEnvironment(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) && len(entry) > len(prefix) && !strings.ContainsAny(entry[len(prefix):], "\r\n\x00") {
			return true
		}
	}
	return false
}
