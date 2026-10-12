// Package agentapp defines application-facing Agent use-case contracts. Runtime
// adapters translate their native protocol into these types at the boundary.
package agentapp

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type RuntimeHealth string

type RuntimeKind string

const (
	RuntimeUserCodex   RuntimeKind = "user_codex"
	RuntimeShotForward RuntimeKind = "shotforward"
)

const (
	RuntimeNotInstalled       RuntimeHealth = "not_installed"
	RuntimeLoginRequired      RuntimeHealth = "login_required"
	RuntimeStarting           RuntimeHealth = "starting"
	RuntimeReady              RuntimeHealth = "ready"
	RuntimeIncompatible       RuntimeHealth = "incompatible"
	RuntimeConfigurationError RuntimeHealth = "configuration_error"
	RuntimeDisconnected       RuntimeHealth = "disconnected"
)

type RuntimeSnapshot struct {
	Health                      RuntimeHealth
	Version                     string
	MinimumVersion              string
	Executable                  string
	FailureCode                 string
	FailureSummary              string
	SafeExecutionAvailable      bool
	SafeExecutionBackend        string
	SafeExecutionFailureCode    string
	SafeExecutionFailureSummary string
}

// RuntimeNetworkSettings contains the user-configurable network boundary for
// the local Codex child process. ProxyURL is deliberately limited by the
// adapter to a loopback proxy so credentials are never redirected to an
// arbitrary remote host.
type RuntimeNetworkSettings struct {
	ProxyURL string
}

// RuntimeNetworkConfigurator is an optional capability implemented by local
// runtimes that can persist network settings and restart their child process.
type RuntimeNetworkConfigurator interface {
	NetworkSettings(context.Context) (RuntimeNetworkSettings, error)
	ConfigureNetwork(context.Context, RuntimeNetworkSettings) error
}

// RuntimeSnapshotSelector exposes the health of one routed Runtime without
// allowing another ready Runtime to mask its installation or version errors.
type RuntimeSnapshotSelector interface {
	SnapshotFor(context.Context, RuntimeKind) (RuntimeSnapshot, error)
}

type RuntimeModelSelector interface {
	ModelsFor(context.Context, RuntimeKind) ([]Model, error)
}

// RuntimeModelRefreshSelector bypasses any product-catalog cache for an
// explicit user refresh while leaving ordinary reads fast and deduplicated.
type RuntimeModelRefreshSelector interface {
	RefreshModelsFor(context.Context, RuntimeKind) ([]Model, error)
}

// RuntimeSelectionController owns the single Agent endpoint selected for new
// conversations. Credentials for the other endpoint remain configured; only
// dispatch is mutually exclusive.
type RuntimeSelectionController interface {
	ActiveRuntime(context.Context) (RuntimeKind, error)
	SelectRuntime(context.Context, RuntimeKind) error
}

var (
	ErrInvalidRuntimeSelection = errors.New("invalid Agent Runtime selection")
	ErrRuntimeSwitchBlocked    = errors.New("Agent Runtime cannot be switched while a Turn is active")
	ErrThreadUnavailable       = errors.New("persisted Agent Thread is unavailable")
)

var ErrInvalidRuntimeNetworkSettings = errors.New("invalid Agent Runtime network settings")

type Model struct {
	ID          string
	DisplayName string
	Hidden      bool
	RuntimeKind RuntimeKind
}

type LoginMode string

const (
	LoginChatGPTBrowser    LoginMode = "chatgpt_browser"
	LoginChatGPTDeviceCode LoginMode = "chatgpt_device_code"
)

type LoginChallenge struct {
	LoginID         string
	AuthURL         string
	VerificationURL string
	UserCode        string
}

type Thread struct {
	CodexThreadID  string
	CodexSessionID string
	Title          string
	ModelID        string
	Turns          []RuntimeTurnSnapshot
}

type ThreadRequest struct {
	RuntimeKind       RuntimeKind
	AgentSessionID    agent.SessionID
	ProjectID         project.ProjectID
	EpisodeID         project.EpisodeID
	ModelID           string
	WorkspacePath     string
	MCPBindingURL     string
	WebSearchDisabled bool
}

var shotForwardMCPToolNames = []string{
	"declare_turn_intent",
	"get_session_context",
	"get_episode_context",
	"read_script",
	"list_assets",
	"list_reference_media",
	"get_asset",
	"list_deleted_assets",
	"get_deleted_asset",
	"list_prior_asset_candidates",
	"get_prior_asset_candidate",
	"list_scenes",
	"get_scene",
	"get_clips",
	"get_clip",
	"get_video_prompt_adaptation_context",
	"generate_conversation_image",
	"begin_change_set",
	"append_prompt_operations",
	"append_video_prompt_adaptation",
	"append_asset_operations",
	"restore_asset",
	"append_storyboard_operations",
	"append_clip_merge_operation",
	"append_clip_split_operation",
	"append_script_operation",
	"finalize_change_set",
}

// ShotForwardMCPToolNames is the single allowlist shared by the Runtime
// configuration and the MCP server. Callers receive a copy.
func ShotForwardMCPToolNames() []string {
	return append([]string(nil), shotForwardMCPToolNames...)
}

type FocusSnapshot struct {
	SceneID project.SceneID
	ClipID  project.ClipID
	AssetID project.AssetID
}

type TurnRequest struct {
	CodexThreadID string
	ModelID       string
	Text          string
	ImagePaths    []string
	Focus         FocusSnapshot
}

type RuntimeTurn struct {
	CodexTurnID string
}

// RuntimeTurnSnapshot is the deliberately small part of persisted Codex
// history needed for conservative recovery. Message and item content remains
// owned by Codex and never crosses this application port.
type RuntimeTurnSnapshot struct {
	CodexTurnID string
	Status      agent.TurnStatus
}

type ConversationRole string

const (
	ConversationUser      ConversationRole = "user"
	ConversationAssistant ConversationRole = "assistant"
)

type ConversationPhase string

const (
	ConversationPhaseUnknown    ConversationPhase = ""
	ConversationPhaseCommentary ConversationPhase = "commentary"
	ConversationPhaseFinal      ConversationPhase = "final_answer"
)

// RuntimeConversationMessage is the display-safe subset of a Codex ThreadItem.
// Tool inputs/results, reasoning, commands, and raw protocol values never cross
// this application port.
type RuntimeConversationMessage struct {
	ItemID string
	Role   ConversationRole
	Phase  ConversationPhase
	Text   string
}

type RuntimeConversationTurn struct {
	CodexTurnID string
	Status      agent.TurnStatus
	Messages    []RuntimeConversationMessage
}

type RuntimeConversationPage struct {
	Turns      []RuntimeConversationTurn
	NextCursor string
}

type RuntimeEventType string

const (
	RuntimeTurnStarted       RuntimeEventType = "turn_started"
	RuntimeAssistantDelta    RuntimeEventType = "assistant_delta"
	RuntimeToolStarted       RuntimeEventType = "tool_started"
	RuntimeToolCompleted     RuntimeEventType = "tool_completed"
	RuntimeImageGenerated    RuntimeEventType = "image_generated"
	RuntimeWaitingUser       RuntimeEventType = "waiting_user"
	RuntimeTurnCompleted     RuntimeEventType = "turn_completed"
	RuntimeConnectionFailure RuntimeEventType = "connection_failure"
)

type RuntimeEvent struct {
	Type               RuntimeEventType
	CodexThreadID      string
	CodexTurnID        string
	ItemID             string
	ToolName           string
	TextDelta          string
	TurnStatus         agent.TurnStatus
	FailureCode        string
	FailureSummary     string
	GeneratedImagePath string
	SandboxedExecution bool
	OccurredAt         time.Time
}

// Runtime is the only application dependency on a local Agent engine. It does
// not expose JSON-RPC envelopes, Codex Item unions, tokens, or account details.
type Runtime interface {
	Snapshot(context.Context) (RuntimeSnapshot, error)
	Models(context.Context) ([]Model, error)
	StartLogin(context.Context, LoginMode) (LoginChallenge, error)
	StartThread(context.Context, ThreadRequest) (Thread, error)
	ResumeThread(context.Context, string, ThreadRequest) (Thread, error)
	ReadThread(context.Context, string) (Thread, error)
	ListConversation(context.Context, string, string, int) (RuntimeConversationPage, error)
	StartTurn(context.Context, TurnRequest) (RuntimeTurn, error)
	SteerTurn(context.Context, string, string, string) error
	InterruptTurn(context.Context, string, string) error
	Subscribe(context.Context, string) (<-chan RuntimeEvent, error)
}

// ImageSteeringRuntime is implemented by Runtimes that can add local image
// inputs while steering an active Turn.
type ImageSteeringRuntime interface {
	SteerTurnWithImages(context.Context, string, string, string, []string) error
}

var ErrInvalidRuntimeValue = errors.New("invalid Agent Runtime value")

func (s RuntimeSnapshot) Validate() error {
	if !validRuntimeHealth(s.Health) || !validRuntimeText(s.Version, 100, false) ||
		!validRuntimeText(s.MinimumVersion, 100, false) ||
		!validRuntimeText(s.Executable, 4_096, false) ||
		!validRuntimeText(s.FailureCode, 100, false) ||
		!validRuntimeSummary(s.FailureSummary, 2_000) ||
		!validRuntimeText(s.SafeExecutionBackend, 100, false) ||
		!validRuntimeText(s.SafeExecutionFailureCode, 100, false) ||
		!validRuntimeSummary(s.SafeExecutionFailureSummary, 2_000) {
		return ErrInvalidRuntimeValue
	}
	failed := s.Health == RuntimeNotInstalled || s.Health == RuntimeIncompatible || s.Health == RuntimeConfigurationError || s.Health == RuntimeDisconnected
	if failed != (s.FailureCode != "" && s.FailureSummary != "") {
		return ErrInvalidRuntimeValue
	}
	if (s.FailureCode == "codex_upgrade_required") != (s.MinimumVersion != "") {
		return ErrInvalidRuntimeValue
	}
	safeExecutionFailed := s.SafeExecutionFailureCode != "" || s.SafeExecutionFailureSummary != ""
	if (s.SafeExecutionFailureCode == "") != (s.SafeExecutionFailureSummary == "") ||
		(s.SafeExecutionAvailable && (s.SafeExecutionBackend == "" || safeExecutionFailed)) ||
		(!s.SafeExecutionAvailable && s.SafeExecutionBackend != "") {
		return ErrInvalidRuntimeValue
	}
	return nil
}

func validRuntimeHealth(value RuntimeHealth) bool {
	return value == RuntimeNotInstalled || value == RuntimeLoginRequired || value == RuntimeStarting || value == RuntimeReady || value == RuntimeIncompatible || value == RuntimeConfigurationError || value == RuntimeDisconnected
}

func validRuntimeText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00") &&
		len([]rune(value)) <= maximum && value == strings.TrimSpace(value) && (!required || value != "")
}

func validRuntimeSummary(value string, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		len([]rune(value)) <= maximum && value == strings.TrimSpace(value)
}
