// Package agenthttp implements the authenticated localhost HTTP and SSE
// transport for Agent Sessions and ChangeSet review.
package agenthttp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	"github.com/shotforward/shotforward/daemon/internal/agentscheduler"
	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const MaxJSONRequestBytes = 128 * 1024
const maxAgentMediaUploadBytes = 25 * 1024 * 1024
const runtimeQueryTimeout = 30 * time.Second

var ErrInvalidConfiguration = errors.New("invalid Agent HTTP configuration")
var diagnosticSecretPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s]+|(sk-[a-z0-9_-]+)|((?:token|secret|api[_-]?key)\s*[=:]\s*)[^\s,;]+`)

type RuntimeQueries interface {
	Snapshot(context.Context) (agentapp.RuntimeSnapshot, error)
	Models(context.Context) ([]agentapp.Model, error)
}

type RuntimeNetwork interface {
	NetworkSettings(context.Context) (agentapp.RuntimeNetworkSettings, error)
	ConfigureNetwork(context.Context, agentapp.RuntimeNetworkSettings) error
}

type SessionCommands interface {
	List(context.Context, project.ProjectID, project.EpisodeID, bool) ([]agent.AgentSession, error)
	Create(context.Context, agentapp.CreateSessionInput) (agent.AgentSession, error)
	Snapshot(context.Context, agent.SessionID) (agentapp.SessionSnapshot, error)
	Conversation(context.Context, agent.SessionID, string, int) (agentapp.ConversationPage, error)
	Archive(context.Context, agent.SessionID, int64) (agent.AgentSession, error)
}

type TurnCommands interface {
	SubmitInput(context.Context, agentscheduler.SubmitInput) (agentscheduler.SubmitResult, error)
	InterruptTurn(context.Context, agent.SessionID, agent.TurnID) (agent.AgentTurn, error)
}

type AgentMediaCommands interface {
	UploadAgentConversationMedia(context.Context, projectapp.UploadAgentConversationMediaInput) (project.AgentConversationMedia, error)
	PrepareAgentInputMedia(context.Context, projectapp.PrepareAgentInputMediaInput) ([]projectapp.AgentInputMedia, error)
	BindAgentInputMedia(context.Context, projectapp.BindAgentInputMediaInput) error
}

// ChangeSetCommands resolves globally unique ChangeSet IDs across the open
// project registry. Apply and Reject are intentionally absent from MCP.
type ChangeSetCommands interface {
	Get(context.Context, changeset.ChangeSetID) (changeset.ChangeSet, error)
	Operations(context.Context, changeset.ChangeSetID, int, int) ([]changeset.Operation, error)
	Apply(context.Context, changeset.ChangeSetID, int64, string) (changesetapp.CommandResult, error)
	Reject(context.Context, changeset.ChangeSetID, int64, string, string) (changesetapp.CommandResult, error)
	Undo(context.Context, changeset.ChangeSetID, string) (changesetapp.CommandResult, error)
}

type EventStream interface {
	Capture(agent.SessionID, func() error) (string, error)
	Subscribe(context.Context, agent.SessionID, string) (<-chan agentsse.Event, func(), error)
}

type Security struct {
	BearerToken    string
	CSRFToken      string
	AllowedHosts   []string
	AllowedOrigins []string
}

type Options struct {
	Runtime    RuntimeQueries
	Network    RuntimeNetwork
	Sessions   SessionCommands
	Turns      TurnCommands
	ChangeSets ChangeSetCommands
	Events     EventStream
	Media      AgentMediaCommands
	Security   Security
	Random     io.Reader
	Logger     *slog.Logger
}

type Handler struct {
	runtime    RuntimeQueries
	network    RuntimeNetwork
	sessions   SessionCommands
	turns      TurnCommands
	changeSets ChangeSetCommands
	events     EventStream
	media      AgentMediaCommands
	security   Security
	random     io.Reader
	logger     *slog.Logger
	counter    atomic.Uint64
}

func NewHandler(options Options) (*Handler, error) {
	if options.Runtime == nil || options.Sessions == nil || options.Turns == nil || options.ChangeSets == nil || options.Events == nil ||
		!validSecret(options.Security.BearerToken) || !validSecret(options.Security.CSRFToken) ||
		!validLocalValues(options.Security.AllowedHosts, validAllowedHost) || !validLocalValues(options.Security.AllowedOrigins, validAllowedOrigin) {
		return nil, ErrInvalidConfiguration
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{runtime: options.Runtime, network: options.Network, sessions: options.Sessions, turns: options.Turns,
		changeSets: options.ChangeSets, events: options.Events, media: options.Media, security: options.Security, random: options.Random, logger: options.Logger}, nil
}

func safeDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	summary := strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(err.Error())
	summary = diagnosticSecretPattern.ReplaceAllString(summary, `${1}${3}[redacted]`)
	const maximumBytes = 1_024
	if len(summary) > maximumBytes {
		summary = summary[:maximumBytes]
		for !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
		summary += "…"
	}
	return summary
}

func validLocalValues(values []string, validate func(string) bool) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validate(value) {
			return false
		}
	}
	return true
}

func validAllowedHost(value string) bool {
	host := value
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func validAllowedOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "tauri" {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	requestID := h.requestID()
	response.Header().Set("X-Request-ID", requestID)
	if member(h.security.AllowedOrigins, request.Header.Get("Origin")) {
		response.Header().Set("Access-Control-Allow-Origin", request.Header.Get("Origin"))
		response.Header().Set("Vary", "Origin")
	}
	if err := h.authorize(request); err != nil {
		h.writeError(response, requestID, err)
		return
	}
	if request.Method == http.MethodOptions {
		response.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		response.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Last-Event-ID, X-CSRF-Token")
		response.WriteHeader(http.StatusNoContent)
		return
	}
	segments := splitPath(request.URL.Path)
	switch {
	case equalSegments(segments, "api", "v1", "agent", "runtime"):
		h.runtimeSnapshot(response, request, requestID)
	case equalSegments(segments, "api", "v1", "agent", "runtime", "network"):
		h.runtimeNetwork(response, request, requestID)
	case len(segments) == 7 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "projects" && segments[4] == "episodes" && segments[6] == "agent-sessions":
		h.projectSessions(response, request, requestID, segments[3], segments[5])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "snapshot" && request.Method == http.MethodGet:
		h.sessionSnapshot(response, request, requestID, segments[3])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "messages" && request.Method == http.MethodGet:
		h.sessionMessages(response, request, requestID, segments[3])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "inputs" && request.Method == http.MethodPost:
		h.submitInput(response, request, requestID, segments[3])
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "media" && request.Method == http.MethodPost:
		h.uploadConversationMedia(response, request, requestID, segments[3])
	case len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "turns" && strings.HasSuffix(segments[5], ":interrupt") && request.Method == http.MethodPost:
		h.interruptTurn(response, request, requestID, segments[3], strings.TrimSuffix(segments[5], ":interrupt"))
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && strings.HasSuffix(segments[3], ":archive") && request.Method == http.MethodPost:
		h.archiveSession(response, request, requestID, strings.TrimSuffix(segments[3], ":archive"))
	case len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "agent-sessions" && segments[4] == "events" && request.Method == http.MethodGet:
		h.streamEvents(response, request, requestID, segments[3])
	case len(segments) >= 4 && len(segments) <= 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "change-sets":
		h.changeSet(response, request, requestID, segments[3:])
	default:
		h.writeError(response, requestID, apiError(http.StatusNotFound, "not_found", "route not found", false, "Check the API route."))
	}
}

func (h *Handler) runtimeSnapshot(w http.ResponseWriter, r *http.Request, requestID string) {
	startedAt := time.Now()
	controller, selectable := h.runtime.(agentapp.RuntimeSelectionController)
	if r.Method == http.MethodPut {
		if !selectable {
			h.writeError(w, requestID, apiError(http.StatusNotImplemented, "runtime_selection_unavailable", "Agent Runtime selection is unavailable", false, "Use the desktop application."))
			return
		}
		var body struct {
			RuntimeKind agentapp.RuntimeKind `json:"runtime_kind"`
		}
		if err := decodeJSON(r, &body); err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		if err := controller.SelectRuntime(r.Context(), body.RuntimeKind); err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
	} else if r.Method != http.MethodGet {
		h.methodNotAllowed(w, requestID)
		return
	}
	activeKind := agentapp.RuntimeUserCodex
	var err error
	if selectable {
		activeKind, err = controller.ActiveRuntime(r.Context())
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
	}
	targetKind := activeKind
	if requested := r.URL.Query().Get("runtime_kind"); requested != "" {
		targetKind = agentapp.RuntimeKind(requested)
		if targetKind != agentapp.RuntimeShotForward && targetKind != agentapp.RuntimeUserCodex {
			h.writeError(w, requestID, apiError(http.StatusBadRequest, "invalid_runtime_kind", "runtime_kind is invalid", false, "Choose shotforward or user_codex."))
			return
		}
	}
	refresh := false
	switch r.URL.Query().Get("refresh") {
	case "", "false":
	case "true":
		refresh = true
	default:
		h.writeError(w, requestID, apiError(http.StatusBadRequest, "invalid_refresh", "refresh must be true or false", false, "Use refresh=true to bypass the model cache."))
		return
	}
	queryContext, cancelQuery := context.WithTimeout(r.Context(), runtimeQueryTimeout)
	defer cancelQuery()
	snapshotStartedAt := time.Now()
	var snapshot agentapp.RuntimeSnapshot
	if selector, ok := h.runtime.(agentapp.RuntimeSnapshotSelector); ok {
		snapshot, err = selector.SnapshotFor(queryContext, targetKind)
	} else {
		if targetKind != activeKind {
			h.writeError(w, requestID, apiError(http.StatusNotImplemented, "runtime_selection_unavailable", "individual Agent Runtime inspection is unavailable", false, "Use the active Agent Runtime."))
			return
		}
		snapshot, err = h.runtime.Snapshot(queryContext)
	}
	snapshotDuration := time.Since(snapshotStartedAt)
	if err != nil {
		if snapshot.Validate() != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
	}
	var models []agentapp.Model
	modelsStartedAt := time.Now()
	if err == nil {
		if selector, ok := h.runtime.(agentapp.RuntimeModelSelector); ok {
			if refresher, refreshable := h.runtime.(agentapp.RuntimeModelRefreshSelector); refresh && refreshable {
				models, err = refresher.RefreshModelsFor(queryContext, targetKind)
			} else {
				models, err = selector.ModelsFor(queryContext, targetKind)
			}
		} else {
			models, err = h.runtime.Models(queryContext)
		}
		if err != nil {
			h.logger.WarnContext(r.Context(), "selected Agent Runtime models unavailable",
				"request_id", requestID, "runtime_kind", targetKind, "error", safeDiagnosticError(err))
			models = nil
		}
	}
	modelsDuration := time.Since(modelsStartedAt)
	modelValues := runtimeModelDTOs(models, targetKind)
	result := map[string]any{
		"active_runtime_kind": activeKind, "target_runtime_kind": targetKind,
		"runtime": runtimeSnapshotDTO(snapshot), "models": modelValues, "request_id": requestID,
		"runtimes":       map[string]any{string(targetKind): runtimeSnapshotDTO(snapshot)},
		"runtime_models": map[string]any{string(targetKind): modelValues},
	}
	if h.network != nil {
		if settings, settingsErr := h.network.NetworkSettings(queryContext); settingsErr == nil {
			result["network"] = map[string]any{"proxy_url": settings.ProxyURL}
		}
	}
	totalDuration := time.Since(startedAt)
	if totalDuration >= time.Second {
		h.logger.InfoContext(r.Context(), "Agent Runtime query completed",
			"request_id", requestID, "runtime_kind", targetKind, "refresh", refresh,
			"snapshot_ms", snapshotDuration.Milliseconds(), "models_ms", modelsDuration.Milliseconds(), "total_ms", totalDuration.Milliseconds())
	}
	h.writeJSON(w, http.StatusOK, result)
}

func runtimeModelDTOs(models []agentapp.Model, fallbackKind agentapp.RuntimeKind) []modelDTO {
	values := make([]modelDTO, 0, len(models))
	for _, model := range models {
		if model.Hidden {
			continue
		}
		kind := model.RuntimeKind
		if kind == "" {
			kind = fallbackKind
		}
		values = append(values, modelDTO{ID: model.ID, DisplayName: model.DisplayName, RuntimeKind: string(kind)})
	}
	return values
}

func runtimeSnapshotDTO(snapshot agentapp.RuntimeSnapshot) map[string]any {
	return map[string]any{
		"health": snapshot.Health, "version": snapshot.Version, "failure_code": snapshot.FailureCode,
		"failure_summary": snapshot.FailureSummary, "minimum_version": snapshot.MinimumVersion,
		"safe_execution_available":       snapshot.SafeExecutionAvailable,
		"safe_execution_backend":         snapshot.SafeExecutionBackend,
		"safe_execution_failure_code":    snapshot.SafeExecutionFailureCode,
		"safe_execution_failure_summary": snapshot.SafeExecutionFailureSummary,
	}
}

func (h *Handler) runtimeNetwork(w http.ResponseWriter, r *http.Request, requestID string) {
	if h.network == nil {
		h.writeError(w, requestID, apiError(http.StatusNotImplemented, "runtime_network_unavailable", "Codex Runtime network settings are unavailable", false, "Use the desktop application."))
		return
	}
	if r.Method == http.MethodGet {
		settings, err := h.network.NetworkSettings(r.Context())
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"network": map[string]any{"proxy_url": settings.ProxyURL}, "request_id": requestID})
		return
	}
	if r.Method != http.MethodPut {
		h.methodNotAllowed(w, requestID)
		return
	}
	var body struct {
		ProxyURL string `json:"proxy_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	settings := agentapp.RuntimeNetworkSettings{ProxyURL: body.ProxyURL}
	if err := h.network.ConfigureNetwork(r.Context(), settings); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	settings, err := h.network.NetworkSettings(r.Context())
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"network": map[string]any{"proxy_url": settings.ProxyURL}, "request_id": requestID})
}

func (h *Handler) projectSessions(w http.ResponseWriter, r *http.Request, requestID, rawProjectID, rawEpisodeID string) {
	projectID, projectErr := project.ParseProjectID(rawProjectID)
	episodeID, episodeErr := project.ParseEpisodeID(rawEpisodeID)
	if projectErr != nil || episodeErr != nil {
		h.writeMappedError(w, requestID, agentscheduler.ErrInvalidAgentInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		values, err := h.sessions.List(r.Context(), projectID, episodeID, r.URL.Query().Get("include_archived") == "true")
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		activeRuntime := agentapp.RuntimeUserCodex
		if controller, ok := h.runtime.(agentapp.RuntimeSelectionController); ok {
			activeRuntime, err = controller.ActiveRuntime(r.Context())
			if err != nil {
				h.writeMappedError(w, requestID, err)
				return
			}
		}
		items := make([]sessionDTO, 0, len(values))
		for _, value := range values {
			if value.RuntimeKind != string(activeRuntime) {
				continue
			}
			items = append(items, mapSession(value))
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"sessions": items, "request_id": requestID})
	case http.MethodPost:
		startedAt := time.Now()
		var body struct {
			ModelID           string               `json:"model_id"`
			RuntimeKind       agentapp.RuntimeKind `json:"runtime_kind"`
			WebSearchDisabled bool                 `json:"web_search_disabled"`
		}
		if err := decodeJSON(r, &body); err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		runtimeKind := body.RuntimeKind
		if runtimeKind == "" {
			if controller, ok := h.runtime.(agentapp.RuntimeSelectionController); ok {
				selected, selectionErr := controller.ActiveRuntime(r.Context())
				if selectionErr != nil {
					h.writeMappedError(w, requestID, selectionErr)
					return
				}
				runtimeKind = selected
			} else {
				runtimeKind = agentapp.RuntimeUserCodex
			}
		}
		h.logger.InfoContext(r.Context(), "Agent session creation started",
			"request_id", requestID, "project_id", projectID.String(), "episode_id", episodeID.String(),
			"model_id", body.ModelID, "runtime_kind", runtimeKind)
		value, err := h.sessions.Create(r.Context(), agentapp.CreateSessionInput{ProjectID: projectID, EpisodeID: episodeID, ModelID: body.ModelID, RuntimeKind: runtimeKind, WebSearchDisabled: body.WebSearchDisabled})
		if err != nil {
			mapped := mapError(err)
			h.logger.ErrorContext(r.Context(), "Agent session creation failed",
				"request_id", requestID, "project_id", projectID.String(), "episode_id", episodeID.String(),
				"model_id", body.ModelID, "runtime_kind", runtimeKind, "error_code", mapped.Code,
				"elapsed_ms", time.Since(startedAt).Milliseconds(), "error", safeDiagnosticError(err))
			h.writeError(w, requestID, mapped)
			return
		}
		h.logger.InfoContext(r.Context(), "Agent session creation completed",
			"request_id", requestID, "session_id", value.ID.String(), "model_id", value.ModelID,
			"runtime_kind", runtimeKind, "elapsed_ms", time.Since(startedAt).Milliseconds())
		h.writeJSON(w, http.StatusCreated, map[string]any{"session": mapSession(value), "request_id": requestID})
	default:
		h.methodNotAllowed(w, requestID)
	}
}

func (h *Handler) sessionSnapshot(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	var value agentapp.SessionSnapshot
	cursor, err := h.events.Capture(sessionID, func() error {
		var readErr error
		value, readErr = h.sessions.Snapshot(r.Context(), sessionID)
		return readErr
	})
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	turns := make([]turnDTO, 0, len(value.Turns))
	for _, turn := range value.Turns {
		turns = append(turns, mapTurn(turn))
	}
	changeSets := make([]changeSetDTO, 0, len(value.ChangeSets))
	for _, item := range value.ChangeSets {
		changeSets = append(changeSets, mapChangeSet(item))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"session": mapSession(value.Session), "turns": turns, "change_sets": changeSets, "cursor": cursor, "request_id": requestID})
}

func (h *Handler) sessionMessages(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	cursor, limit, err := conversationPagination(r)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	page, err := h.sessions.Conversation(r.Context(), sessionID, cursor, limit)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	turns := make([]conversationTurnDTO, 0, len(page.Turns))
	for _, value := range page.Turns {
		messages := make([]conversationMessageDTO, 0, len(value.Messages))
		for _, message := range value.Messages {
			messages = append(messages, conversationMessageDTO{ID: message.ID, ClientOperationID: message.ClientOperationID, Role: string(message.Role), Phase: string(message.Phase), Text: message.Text})
		}
		media := make([]conversationMediaDTO, 0, len(value.Media))
		for _, item := range value.Media {
			media = append(media, mapConversationMedia(item))
		}
		turns = append(turns, conversationTurnDTO{Turn: mapTurn(value.Turn), Messages: messages, Media: media})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"turns": turns, "next_cursor": page.NextCursor, "request_id": requestID})
}

func (h *Handler) submitInput(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	var body struct {
		ClientOperationID string                       `json:"client_operation_id"`
		ModelID           string                       `json:"model_id"`
		Text              string                       `json:"text"`
		VideoPromptTarget *agentmcp.VideoPromptContext `json:"video_prompt_target,omitempty"`
		Focus             struct {
			SceneID string `json:"scene_id"`
			ClipID  string `json:"clip_id"`
			AssetID string `json:"asset_id"`
		} `json:"focus"`
		Attachments []struct {
			ArtifactID string `json:"artifact_id"`
			Name       string `json:"name"`
			Origin     string `json:"origin"`
		} `json:"attachments"`
	}
	if err := decodeJSON(r, &body); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	focus, err := parseFocus(body.Focus.SceneID, body.Focus.ClipID, body.Focus.AssetID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	if body.VideoPromptTarget.Validate() != nil || (body.VideoPromptTarget != nil && body.VideoPromptTarget.Focus != nil && body.VideoPromptTarget.Focus.ClipID != focus.ClipID) {
		h.writeMappedError(w, requestID, agentscheduler.ErrInvalidAgentInput)
		return
	}
	imagePaths := make([]string, 0, len(body.Attachments))
	artifactIDs := make([]project.ArtifactID, 0, len(body.Attachments))
	if len(body.Attachments) > 0 {
		if h.media == nil {
			h.writeError(w, requestID, apiError(http.StatusNotImplemented, "agent_media_unavailable", "Agent image attachments are unavailable", false, "Restart ShotForward and try again."))
			return
		}
		snapshot, snapshotErr := h.sessions.Snapshot(r.Context(), sessionID)
		if snapshotErr != nil {
			h.writeMappedError(w, requestID, snapshotErr)
			return
		}
		references := make([]projectapp.AgentConversationMediaReference, 0, len(body.Attachments))
		for _, attachment := range body.Attachments {
			artifactID, parseErr := project.ParseArtifactID(attachment.ArtifactID)
			if parseErr != nil {
				h.writeMappedError(w, requestID, projectapp.ErrInvalidRequest)
				return
			}
			references = append(references, projectapp.AgentConversationMediaReference{ArtifactID: artifactID, Name: attachment.Name, Origin: project.AgentConversationMediaOrigin(attachment.Origin)})
		}
		prepared, prepareErr := h.media.PrepareAgentInputMedia(r.Context(), projectapp.PrepareAgentInputMediaInput{
			ProjectID: snapshot.Session.ProjectID, EpisodeID: snapshot.Session.EpisodeID, SessionID: sessionID.String(), ClientOperationID: body.ClientOperationID, References: references,
		})
		if prepareErr != nil {
			h.writeMappedError(w, requestID, prepareErr)
			return
		}
		for _, item := range prepared {
			imagePaths = append(imagePaths, item.AbsolutePath)
			artifactIDs = append(artifactIDs, item.Media.Artifact.ID)
		}
	}
	result, err := h.turns.SubmitInput(r.Context(), agentscheduler.SubmitInput{AgentSessionID: sessionID, ClientOperationID: body.ClientOperationID, ModelID: body.ModelID, Text: body.Text, ImagePaths: imagePaths, ArtifactIDs: artifactIDs, Focus: focus, VideoPromptTarget: body.VideoPromptTarget})
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	if len(artifactIDs) > 0 {
		snapshot, snapshotErr := h.sessions.Snapshot(r.Context(), sessionID)
		if snapshotErr != nil {
			h.writeMappedError(w, requestID, snapshotErr)
			return
		}
		if bindErr := h.media.BindAgentInputMedia(r.Context(), projectapp.BindAgentInputMediaInput{
			ProjectID: snapshot.Session.ProjectID, EpisodeID: snapshot.Session.EpisodeID, SessionID: sessionID.String(), ClientOperationID: body.ClientOperationID, TurnID: result.Turn.ID.String(), ArtifactIDs: artifactIDs,
		}); bindErr != nil {
			h.writeMappedError(w, requestID, bindErr)
			return
		}
	}
	h.writeJSON(w, http.StatusAccepted, map[string]any{"action": result.Action, "turn": mapTurn(result.Turn), "replayed": result.Replay, "request_id": requestID})
}

func (h *Handler) uploadConversationMedia(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	if h.media == nil {
		h.writeError(w, requestID, apiError(http.StatusNotImplemented, "agent_media_unavailable", "Agent image attachments are unavailable", false, "Restart ShotForward and try again."))
		return
	}
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	snapshot, err := h.sessions.Snapshot(r.Context(), sessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentMediaUploadBytes+1024*1024)
	if err := r.ParseMultipartForm(maxAgentMediaUploadBytes); err != nil {
		h.writeError(w, requestID, apiError(http.StatusBadRequest, "invalid_agent_media", "The image upload is invalid or too large", false, "Choose an image smaller than 25 MB."))
		return
	}
	operationID := strings.TrimSpace(r.FormValue("client_operation_id"))
	file, header, err := r.FormFile("file")
	if err != nil || operationID == "" {
		h.writeMappedError(w, requestID, projectapp.ErrInvalidRequest)
		return
	}
	defer file.Close()
	extension := filepath.Ext(header.Filename)
	if len(extension) > 12 {
		extension = ""
	}
	temporary, err := os.CreateTemp("", "shotforward-agent-media-*"+extension)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, copyErr := io.Copy(temporary, io.LimitReader(file, maxAgentMediaUploadBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil || written > maxAgentMediaUploadBytes {
		h.writeError(w, requestID, apiError(http.StatusBadRequest, "invalid_agent_media", "The image upload could not be read", false, "Choose an image smaller than 25 MB."))
		return
	}
	media, err := h.media.UploadAgentConversationMedia(r.Context(), projectapp.UploadAgentConversationMediaInput{
		ProjectID: snapshot.Session.ProjectID, EpisodeID: snapshot.Session.EpisodeID, SessionID: sessionID.String(), ClientOperationID: operationID, SourcePath: temporaryPath, OriginalFilename: header.Filename,
	})
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{"media": mapConversationMedia(media), "request_id": requestID})
}

func (h *Handler) interruptTurn(w http.ResponseWriter, r *http.Request, requestID, rawSessionID, rawTurnID string) {
	sessionID, sessionErr := agent.ParseSessionID(rawSessionID)
	turnID, turnErr := agent.ParseTurnID(rawTurnID)
	if sessionErr != nil || turnErr != nil {
		h.writeMappedError(w, requestID, agentscheduler.ErrInvalidAgentInput)
		return
	}
	if err := decodeEmptyJSON(r); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	turn, err := h.turns.InterruptTurn(r.Context(), sessionID, turnID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"turn": mapTurn(turn), "request_id": requestID})
}

func (h *Handler) archiveSession(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	var body struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(r, &body); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	value, err := h.sessions.Archive(r.Context(), sessionID, body.ExpectedRevision)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"session": mapSession(value), "request_id": requestID})
}

func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request, requestID, rawSessionID string) {
	sessionID, err := agent.ParseSessionID(rawSessionID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	// Authorization scope is proven by the authoritative Session query before
	// opening a potentially long-lived stream.
	if _, err := h.sessions.Snapshot(r.Context(), sessionID); err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	after := r.Header.Get("Last-Event-ID")
	if after == "" {
		after = r.URL.Query().Get("after")
	}
	events, cancel, err := h.events.Subscribe(r.Context(), sessionID, after)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	defer cancel()
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeMappedError(w, requestID, errors.New("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			data, marshalErr := json.Marshal(mapEvent(event.Value))
			if marshalErr != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Value.Type, data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (h *Handler) changeSet(w http.ResponseWriter, r *http.Request, requestID string, tail []string) {
	if len(tail) < 1 {
		h.writeError(w, requestID, apiError(http.StatusNotFound, "not_found", "route not found", false, "Check the API route."))
		return
	}
	action := ""
	rawID := tail[0]
	if len(tail) == 2 && tail[1] == "operations" {
		action = "operations"
	} else if len(tail) == 1 && strings.HasSuffix(rawID, ":apply") {
		action, rawID = "apply", strings.TrimSuffix(rawID, ":apply")
	} else if len(tail) == 1 && strings.HasSuffix(rawID, ":reject") {
		action, rawID = "reject", strings.TrimSuffix(rawID, ":reject")
	} else if len(tail) == 1 && strings.HasSuffix(rawID, ":undo") {
		action, rawID = "undo", strings.TrimSuffix(rawID, ":undo")
	} else if len(tail) != 1 {
		h.writeError(w, requestID, apiError(http.StatusNotFound, "not_found", "route not found", false, "Check the API route."))
		return
	}
	id, err := changeset.ParseChangeSetID(rawID)
	if err != nil {
		h.writeMappedError(w, requestID, err)
		return
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		value, err := h.changeSets.Get(r.Context(), id)
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"change_set": mapChangeSet(value), "request_id": requestID})
	case action == "operations" && r.Method == http.MethodGet:
		after, limit, err := pagination(r)
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		values, err := h.changeSets.Operations(r.Context(), id, after, limit)
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		items := make([]operationDTO, 0, len(values))
		for _, value := range values {
			items = append(items, mapOperation(value))
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"operations": items, "request_id": requestID})
	case (action == "apply" || action == "reject" || action == "undo") && r.Method == http.MethodPost:
		var body struct {
			ExpectedRevision int64  `json:"expected_revision"`
			OperationKey     string `json:"operation_key"`
			Reason           string `json:"reason"`
		}
		if err := decodeJSON(r, &body); err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		var result changesetapp.CommandResult
		if action == "apply" {
			result, err = h.changeSets.Apply(r.Context(), id, body.ExpectedRevision, body.OperationKey)
		} else if action == "reject" {
			result, err = h.changeSets.Reject(r.Context(), id, body.ExpectedRevision, body.Reason, body.OperationKey)
		} else {
			result, err = h.changeSets.Undo(r.Context(), id, body.OperationKey)
		}
		if err != nil {
			h.writeMappedError(w, requestID, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"change_set": mapChangeSet(result.ChangeSet), "replayed": result.Replayed, "request_id": requestID})
	default:
		h.methodNotAllowed(w, requestID)
	}
}
