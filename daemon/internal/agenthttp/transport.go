package agenthttp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentscheduler"
	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrNotFound         = errors.New("Agent HTTP object not found")
	ErrRevisionConflict = errors.New("Agent HTTP revision conflict")
	ErrInvalidState     = errors.New("Agent HTTP invalid state")
)

type applicationError struct {
	Code            string         `json:"code"`
	Message         string         `json:"message"`
	Details         map[string]any `json:"details"`
	Retryable       bool           `json:"retryable"`
	RequestID       string         `json:"request_id"`
	SuggestedAction string         `json:"suggested_action"`
	status          int
}

func (e *applicationError) Error() string { return e.Message }

func apiError(status int, code, message string, retryable bool, action string) *applicationError {
	return &applicationError{status: status, Code: code, Message: message, Details: map[string]any{}, Retryable: retryable, SuggestedAction: action}
}

func (h *Handler) authorize(r *http.Request) *applicationError {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return apiError(http.StatusForbidden, "forbidden", "request source is not loopback", false, "Use the local ShotForward application.")
	}
	if !member(h.security.AllowedHosts, r.Host) {
		return apiError(http.StatusForbidden, "forbidden", "request Host is not allowed", false, "Reload the local application.")
	}
	origin := r.Header.Get("Origin")
	if !member(h.security.AllowedOrigins, origin) {
		return apiError(http.StatusForbidden, "forbidden", "request Origin is not allowed", false, "Reload the local application.")
	}
	if r.Method == http.MethodOptions {
		return nil
	}
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) || !secretEqual(strings.TrimPrefix(authorization, prefix), h.security.BearerToken) {
		return apiError(http.StatusUnauthorized, "authentication_required", "local session authentication is required", false, "Reload the local application.")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !secretEqual(r.Header.Get("X-CSRF-Token"), h.security.CSRFToken) {
		return apiError(http.StatusForbidden, "csrf_rejected", "CSRF validation failed", false, "Reload the local application.")
	}
	return nil
}

func (h *Handler) requestID() string {
	buffer := make([]byte, 10)
	if _, err := io.ReadFull(h.random, buffer); err == nil {
		return "req_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)
	}
	return "req_fallback_" + strconv.FormatUint(h.counter.Add(1), 10)
}

func (h *Handler) writeMappedError(w http.ResponseWriter, requestID string, err error) {
	h.writeError(w, requestID, mapError(err))
}

func (h *Handler) writeError(w http.ResponseWriter, requestID string, err *applicationError) {
	err.RequestID = requestID
	h.writeJSON(w, err.status, map[string]any{"error": err})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, requestID string) {
	h.writeError(w, requestID, apiError(http.StatusMethodNotAllowed, "invalid_argument", "HTTP method is not allowed", false, "Use the documented endpoint method."))
}

func mapError(err error) *applicationError {
	var direct *applicationError
	if errors.As(err, &direct) {
		return direct
	}
	switch {
	case errors.Is(err, agentapp.ErrRuntimeSwitchBlocked):
		return apiError(http.StatusConflict, "runtime_switch_blocked", "Agent Runtime cannot be switched while a Turn is active", false, "Wait for the active Turn to finish or interrupt it first.")
	case errors.Is(err, ErrNotFound), errors.Is(err, agentsession.ErrSessionNotFound), errors.Is(err, changesetapp.ErrChangeSetNotFound), errors.Is(err, projectapp.ErrProjectStoreNotOpen), errors.Is(err, projectapp.ErrEpisodeNotFound):
		return apiError(http.StatusNotFound, "not_found", "requested object was not found", false, "Refresh the current view.")
	case errors.Is(err, ErrRevisionConflict), errors.Is(err, agentsession.ErrRevisionConflict), errors.Is(err, changesetapp.ErrChangeSetRevisionConflict), errors.Is(err, projectapp.ErrRevisionConflict):
		return apiError(http.StatusConflict, "revision_conflict", "the object changed since it was read", false, "Refresh and review the latest value.")
	case errors.Is(err, agentscheduler.ErrInputIdempotencyConflict), errors.Is(err, agent.ErrInputIdempotencyConflict), errors.Is(err, changesetapp.ErrChangeSetOperationConflict):
		return apiError(http.StatusConflict, "idempotency_conflict", "operation key was reused with different input", false, "Retry with a new operation key.")
	case errors.Is(err, agent.ErrSessionQueueFull), errors.Is(err, agent.ErrGlobalQueueFull):
		return apiError(http.StatusTooManyRequests, "turn_queue_full", "the Agent Turn queue is full", true, "Wait for a queued Turn to finish.")
	case errors.Is(err, changesetapp.ErrChangeSetConflicted):
		return apiError(http.StatusConflict, "change_set_conflicted", "the ChangeSet no longer applies cleanly", false, "Review the recorded conflicts.")
	case errors.Is(err, changesetapp.ErrChangeSetPreconditionFailed):
		return apiError(http.StatusConflict, "change_set_precondition_failed", "the project changed and the ChangeSet precondition no longer holds", false, "Refresh the current project state before retrying or preparing a new change.")
	case errors.Is(err, ErrInvalidState), errors.Is(err, agentsession.ErrSessionHasActiveTurn), errors.Is(err, changesetapp.ErrChangeSetStateConflict), errors.Is(err, agentscheduler.ErrSessionNotActive), errors.Is(err, agentscheduler.ErrTurnNotActive), errors.Is(err, agent.ErrSessionNotActive):
		return apiError(http.StatusConflict, "invalid_state", "the operation is not allowed in the current state", false, "Refresh the Agent Session.")
	case errors.Is(err, changesetapp.ErrInvalidRequest), errors.Is(err, agentsession.ErrInvalidRequest), errors.Is(err, agentsession.ErrModelUnavailable), errors.Is(err, projectapp.ErrInvalidRequest),
		errors.Is(err, agentscheduler.ErrInvalidAgentInput), errors.Is(err, agent.ErrInvalidSessionID),
		errors.Is(err, agent.ErrInvalidTurnID), errors.Is(err, changeset.ErrInvalidChangeSetID), errors.Is(err, agentapp.ErrInvalidRuntimeNetworkSettings), errors.Is(err, agentapp.ErrInvalidRuntimeSelection):
		return apiError(http.StatusBadRequest, "invalid_argument", "request input is invalid", false, "Correct the request and retry.")
	case errors.Is(err, agentsession.ErrRuntimeNotReady):
		return apiError(http.StatusServiceUnavailable, "runtime_unavailable", "Agent Runtime is unavailable", true, "Check Codex setup and retry.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return apiError(http.StatusRequestTimeout, "interrupted", "request was interrupted", true, "Retry the request.")
	default:
		return apiError(http.StatusInternalServerError, "external_failure", "the operation could not be completed", true, "Retry or inspect the local diagnostics.")
	}
}

func decodeJSON(r *http.Request, target any) error {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return agentscheduler.ErrInvalidAgentInput
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, MaxJSONRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return agentscheduler.ErrInvalidAgentInput
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return agentscheduler.ErrInvalidAgentInput
	}
	return nil
}

func decodeEmptyJSON(r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	var body struct{}
	return decodeJSON(r, &body)
}

func parseFocus(sceneValue, clipValue, assetValue string) (agent.FocusSnapshot, error) {
	var result agent.FocusSnapshot
	var err error
	if sceneValue != "" {
		result.SceneID, err = project.ParseSceneID(sceneValue)
		if err != nil {
			return agent.FocusSnapshot{}, agentscheduler.ErrInvalidAgentInput
		}
	}
	if clipValue != "" {
		result.ClipID, err = project.ParseClipID(clipValue)
		if err != nil {
			return agent.FocusSnapshot{}, agentscheduler.ErrInvalidAgentInput
		}
	}
	if assetValue != "" {
		result.AssetID, err = project.ParseAssetID(assetValue)
		if err != nil {
			return agent.FocusSnapshot{}, agentscheduler.ErrInvalidAgentInput
		}
	}
	return result, nil
}

func pagination(r *http.Request) (int, int, error) {
	after, limit := 0, 50
	var err error
	if raw := r.URL.Query().Get("after_position"); raw != "" {
		after, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, agentscheduler.ErrInvalidAgentInput
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, agentscheduler.ErrInvalidAgentInput
		}
	}
	if after < 0 || limit < 1 || limit > 100 {
		return 0, 0, agentscheduler.ErrInvalidAgentInput
	}
	return after, limit, nil
}

func conversationPagination(r *http.Request) (string, int, error) {
	cursor, limit := r.URL.Query().Get("cursor"), 20
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return "", 0, agentscheduler.ErrInvalidAgentInput
		}
	}
	if len(cursor) > 4*1024 || strings.ContainsRune(cursor, 0) || limit < 1 || limit > 50 {
		return "", 0, agentscheduler.ErrInvalidAgentInput
	}
	return cursor, limit, nil
}

func splitPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func equalSegments(value []string, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	for index := range value {
		if value[index] != expected[index] {
			return false
		}
	}
	return true
}

func validSecret(value string) bool {
	return len(value) >= 32 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func secretEqual(left, right string) bool {
	leftHash, rightHash := sha256.Sum256([]byte(left)), sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func member(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type sessionDTO struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	EpisodeID         string     `json:"episode_id"`
	ModelID           string     `json:"model_id"`
	RuntimeKind       string     `json:"runtime_kind"`
	Title             string     `json:"title"`
	WebSearchDisabled bool       `json:"web_search_disabled"`
	Status            string     `json:"status"`
	Revision          int64      `json:"revision"`
	ArchivedAt        *time.Time `json:"archived_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type modelDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	RuntimeKind string `json:"runtime_kind"`
}

func mapSession(value agent.AgentSession) sessionDTO {
	return sessionDTO{ID: value.ID.String(), ProjectID: value.ProjectID.String(), EpisodeID: value.EpisodeID.String(), ModelID: value.ModelID, RuntimeKind: normalizedSessionRuntime(value.RuntimeKind),
		Title: value.CachedTitle, WebSearchDisabled: value.WebSearchDisabled, Status: string(value.Status), Revision: value.Revision,
		ArchivedAt: value.ArchivedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func normalizedSessionRuntime(value string) string {
	if value == "" {
		return string(agentapp.RuntimeUserCodex)
	}
	return value
}

type turnDTO struct {
	ID                string      `json:"id"`
	AgentSessionID    string      `json:"agent_session_id"`
	ModelID           string      `json:"model_id"`
	ClientOperationID string      `json:"client_operation_id"`
	Focus             focusDTO    `json:"focus"`
	Status            string      `json:"status"`
	Failure           *failureDTO `json:"failure,omitempty"`
	Revision          int64       `json:"revision"`
	CreatedAt         time.Time   `json:"created_at"`
	StartedAt         *time.Time  `json:"started_at,omitempty"`
	CompletedAt       *time.Time  `json:"completed_at,omitempty"`
}

type conversationMessageDTO struct {
	ID                string `json:"id"`
	ClientOperationID string `json:"client_operation_id,omitempty"`
	Role              string `json:"role"`
	Phase             string `json:"phase,omitempty"`
	Text              string `json:"text"`
}

type conversationTurnDTO struct {
	Turn     turnDTO                  `json:"turn"`
	Messages []conversationMessageDTO `json:"messages"`
	Media    []conversationMediaDTO   `json:"media"`
}

type conversationMediaDTO struct {
	ArtifactID       string    `json:"artifact_id"`
	Name             string    `json:"name"`
	Origin           string    `json:"origin"`
	MIMEType         string    `json:"mime_type"`
	OriginalFilename string    `json:"original_filename"`
	ByteSize         int64     `json:"byte_size"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	CreatedAt        time.Time `json:"created_at"`
}

func mapConversationMedia(value project.AgentConversationMedia) conversationMediaDTO {
	return conversationMediaDTO{ArtifactID: value.Artifact.ID.String(), Name: value.Name, Origin: string(value.Origin),
		MIMEType: value.Artifact.MIMEType, OriginalFilename: value.Artifact.OriginalFilename, ByteSize: value.Artifact.ByteSize,
		Width: value.Artifact.Width, Height: value.Artifact.Height, CreatedAt: value.CreatedAt}
}

func mapTurn(value agent.AgentTurn) turnDTO {
	result := turnDTO{ID: value.ID.String(), AgentSessionID: value.AgentSessionID.String(), ModelID: value.ModelID, ClientOperationID: value.ClientOperationID,
		Focus:  focusDTO{SceneID: value.Focus.SceneID.String(), ClipID: value.Focus.ClipID.String(), AssetID: value.Focus.AssetID.String()},
		Status: string(value.Status), Revision: value.Revision,
		CreatedAt: value.CreatedAt, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt}
	if value.Failure != nil {
		result.Failure = &failureDTO{Code: value.Failure.Code, Summary: value.Failure.Summary}
	}
	return result
}

type failureDTO struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type changeSetDTO struct {
	ID                    string     `json:"id"`
	ProjectID             string     `json:"project_id"`
	EpisodeID             string     `json:"episode_id"`
	AgentSessionID        string     `json:"agent_session_id"`
	AgentTurnID           string     `json:"agent_turn_id"`
	Title                 string     `json:"title"`
	Summary               string     `json:"summary"`
	Status                string     `json:"status"`
	OperationCount        int        `json:"operation_count"`
	Revision              int64      `json:"revision"`
	FailureCode           string     `json:"failure_code,omitempty"`
	FailureSummary        string     `json:"failure_summary,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	FinalizedAt           *time.Time `json:"finalized_at,omitempty"`
	ResolvedAt            *time.Time `json:"resolved_at,omitempty"`
	RevertsChangeSetID    string     `json:"reverts_change_set_id,omitempty"`
	RevertedByChangeSetID string     `json:"reverted_by_change_set_id,omitempty"`
}

func mapChangeSet(value changeset.ChangeSet) changeSetDTO {
	return changeSetDTO{ID: value.ID.String(), ProjectID: value.ProjectID.String(), EpisodeID: value.EpisodeID.String(),
		AgentSessionID: value.Source.AgentSessionID.String(), AgentTurnID: value.Source.AgentTurnID.String(), Title: value.Title,
		Summary: value.Summary, Status: string(value.Status), OperationCount: value.OperationCount, Revision: value.Revision,
		FailureCode: value.FailureCode, FailureSummary: value.FailureSummary, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		FinalizedAt: value.FinalizedAt, ResolvedAt: value.ResolvedAt, RevertsChangeSetID: value.RevertsChangeSetID.String(), RevertedByChangeSetID: value.RevertedByChangeSetID.String()}
}

type focusDTO struct {
	SceneID string `json:"scene_id,omitempty"`
	ClipID  string `json:"clip_id,omitempty"`
	AssetID string `json:"asset_id,omitempty"`
}

type assetPromptDTO struct {
	AssetID string `json:"asset_id"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type clipPromptsDTO struct {
	ClipID                     string                     `json:"clip_id"`
	FirstBefore                string                     `json:"first_before"`
	FirstAfter                 string                     `json:"first_after"`
	LastBefore                 string                     `json:"last_before"`
	LastAfter                  string                     `json:"last_after"`
	VideoMode                  project.ClipGenerationMode `json:"video_mode"`
	VideoBefore                string                     `json:"video_before"`
	VideoAfter                 string                     `json:"video_after"`
	AssetIDsBefore             []string                   `json:"asset_ids_before,omitempty"`
	AssetIDsAfter              []string                   `json:"asset_ids_after,omitempty"`
	ReferenceArtifactIDsBefore []string                   `json:"reference_artifact_ids_before,omitempty"`
	ReferenceArtifactIDsAfter  []string                   `json:"reference_artifact_ids_after,omitempty"`
}

type assetCreationDTO struct {
	AssetID       string `json:"asset_id"`
	IdentityID    string `json:"identity_id"`
	SourceAssetID string `json:"source_asset_id,omitempty"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Prompt        string `json:"prompt"`
}

type sceneCreationDTO struct {
	Ref                    string                     `json:"ref"`
	SceneID                string                     `json:"scene_id"`
	Position               int                        `json:"position"`
	Title                  string                     `json:"title"`
	Description            string                     `json:"description"`
	Source                 *project.SceneScriptSource `json:"source,omitempty"`
	ExpectedScriptRevision int64                      `json:"expected_script_revision"`
}

type clipCreationDTO struct {
	Ref                    string                     `json:"ref"`
	SceneRef               string                     `json:"scene_ref"`
	ClipID                 string                     `json:"clip_id"`
	SceneID                string                     `json:"scene_id"`
	Position               int                        `json:"position"`
	Title                  string                     `json:"title"`
	Description            string                     `json:"description"`
	Dialogue               string                     `json:"dialogue"`
	CreativeIntent         string                     `json:"creative_intent"`
	PlannedDurationSeconds int64                      `json:"planned_duration_seconds"`
	FirstImagePrompt       string                     `json:"first_image_prompt"`
	LastImagePrompt        string                     `json:"last_image_prompt"`
	VideoPrompt            string                     `json:"video_prompt"`
	GenerationMode         project.ClipGenerationMode `json:"generation_mode"`
	ExpectedScriptRevision int64                      `json:"expected_script_revision"`
}

type clipAssetReferenceCreationDTO struct {
	ClipRef                string `json:"clip_ref"`
	ClipID                 string `json:"clip_id"`
	AssetID                string `json:"asset_id"`
	Position               int    `json:"position"`
	ExpectedScriptRevision int64  `json:"expected_script_revision"`
}

type episodeScriptDTO struct {
	EpisodeID string `json:"episode_id"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

type changeSetRevertDTO struct {
	OriginalChangeSetID string `json:"original_change_set_id"`
}

type clipMergeDTO struct {
	SourceClipIDs          []string `json:"source_clip_ids"`
	ReplacementClipID      string   `json:"replacement_clip_id"`
	Title                  string   `json:"title"`
	PlannedDurationSeconds int64    `json:"planned_duration_seconds"`
	AssetIDs               []string `json:"asset_ids"`
	InheritedFirstFrame    bool     `json:"inherited_first_frame"`
	InheritedLastFrame     bool     `json:"inherited_last_frame"`
}

type clipSplitReplacementDTO struct {
	ClipID                 string `json:"clip_id"`
	Title                  string `json:"title"`
	PlannedDurationSeconds int64  `json:"planned_duration_seconds"`
}

type clipSplitDTO struct {
	SourceClipID                 string                    `json:"source_clip_id"`
	SourceTitle                  string                    `json:"source_title"`
	SourcePlannedDurationSeconds int64                     `json:"source_planned_duration_seconds"`
	Replacements                 []clipSplitReplacementDTO `json:"replacements"`
	InheritedFirstFrame          bool                      `json:"inherited_first_frame"`
	InheritedLastFrame           bool                      `json:"inherited_last_frame"`
}

type assetRestorationDTO struct {
	AssetID string `json:"asset_id"`
}

type operationDTO struct {
	ID               string                         `json:"id"`
	Position         int                            `json:"position"`
	Type             string                         `json:"type"`
	TargetKind       string                         `json:"target_kind"`
	TargetID         string                         `json:"target_id"`
	ExpectedRevision int64                          `json:"expected_revision"`
	AssetPrompt      *assetPromptDTO                `json:"asset_prompt,omitempty"`
	ClipPrompts      *clipPromptsDTO                `json:"clip_prompts,omitempty"`
	AssetCreation    *assetCreationDTO              `json:"asset_creation,omitempty"`
	AssetRestoration *assetRestorationDTO           `json:"asset_restoration,omitempty"`
	SceneCreation    *sceneCreationDTO              `json:"scene_creation,omitempty"`
	ClipCreation     *clipCreationDTO               `json:"clip_creation,omitempty"`
	ClipAssetRef     *clipAssetReferenceCreationDTO `json:"clip_asset_reference_creation,omitempty"`
	ClipMerge        *clipMergeDTO                  `json:"clip_merge,omitempty"`
	ClipSplit        *clipSplitDTO                  `json:"clip_split,omitempty"`
	EpisodeScript    *episodeScriptDTO              `json:"episode_script,omitempty"`
	ChangeSetRevert  *changeSetRevertDTO            `json:"change_set_revert,omitempty"`
}

func mapOperation(value changeset.Operation) operationDTO {
	result := operationDTO{ID: value.ID.String(), Position: value.Position, Type: string(value.Type), TargetKind: string(value.Precondition.TargetKind),
		TargetID: value.Precondition.TargetID, ExpectedRevision: value.Precondition.ExpectedRevision}
	if value.AssetPrompt != nil {
		result.AssetPrompt = &assetPromptDTO{AssetID: value.AssetPrompt.AssetID.String(), Before: value.AssetPrompt.Before, After: value.AssetPrompt.After}
	}
	if value.ClipPrompts != nil {
		assetIDsBefore := make([]string, len(value.ClipPrompts.AssetIDsBefore))
		for index, id := range value.ClipPrompts.AssetIDsBefore {
			assetIDsBefore[index] = id.String()
		}
		assetIDsAfter := make([]string, len(value.ClipPrompts.AssetIDsAfter))
		for index, id := range value.ClipPrompts.AssetIDsAfter {
			assetIDsAfter[index] = id.String()
		}
		referenceIDsBefore := make([]string, len(value.ClipPrompts.ReferenceArtifactIDsBefore))
		for index, id := range value.ClipPrompts.ReferenceArtifactIDsBefore {
			referenceIDsBefore[index] = id.String()
		}
		referenceIDsAfter := make([]string, len(value.ClipPrompts.ReferenceArtifactIDsAfter))
		for index, id := range value.ClipPrompts.ReferenceArtifactIDsAfter {
			referenceIDsAfter[index] = id.String()
		}
		result.ClipPrompts = &clipPromptsDTO{ClipID: value.ClipPrompts.ClipID.String(), FirstBefore: value.ClipPrompts.FirstBefore,
			FirstAfter: value.ClipPrompts.FirstAfter, LastBefore: value.ClipPrompts.LastBefore, LastAfter: value.ClipPrompts.LastAfter,
			VideoMode: value.ClipPrompts.VideoMode, VideoBefore: value.ClipPrompts.VideoBefore, VideoAfter: value.ClipPrompts.VideoAfter,
			AssetIDsBefore: assetIDsBefore, AssetIDsAfter: assetIDsAfter,
			ReferenceArtifactIDsBefore: referenceIDsBefore, ReferenceArtifactIDsAfter: referenceIDsAfter}
	}
	if value.AssetCreation != nil {
		result.AssetCreation = &assetCreationDTO{AssetID: value.AssetCreation.AssetID.String(), IdentityID: value.AssetCreation.IdentityID.String(), SourceAssetID: value.AssetCreation.SourceAssetID.String(), Kind: string(value.AssetCreation.Kind), Name: value.AssetCreation.Name, Prompt: value.AssetCreation.Prompt}
	}
	if value.AssetRestoration != nil {
		result.AssetRestoration = &assetRestorationDTO{AssetID: value.AssetRestoration.AssetID.String()}
	}
	if value.SceneCreation != nil {
		creation := value.SceneCreation
		result.SceneCreation = &sceneCreationDTO{Ref: creation.Ref, SceneID: creation.Scene.ID.String(), Position: creation.Scene.Position,
			Title: creation.Scene.Title, Description: creation.Scene.Description, Source: creation.Scene.Source, ExpectedScriptRevision: creation.ExpectedScriptRevision}
	}
	if value.ClipCreation != nil {
		creation, clip := value.ClipCreation, value.ClipCreation.Clip
		result.ClipCreation = &clipCreationDTO{Ref: creation.Ref, SceneRef: creation.SceneRef, ClipID: clip.ID.String(), SceneID: clip.SceneID.String(),
			Position: clip.Position, Title: clip.Title, Description: clip.Description, Dialogue: clip.Dialogue, CreativeIntent: clip.CreativeIntent,
			PlannedDurationSeconds: clip.PlannedDurationSeconds, FirstImagePrompt: clip.FirstImagePrompt, LastImagePrompt: clip.LastImagePrompt,
			VideoPrompt: clip.VideoPrompts.ForMode(clip.GenerationMode), GenerationMode: clip.GenerationMode, ExpectedScriptRevision: creation.ExpectedScriptRevision}
	}
	if value.ClipAssetRef != nil {
		creation := value.ClipAssetRef
		result.ClipAssetRef = &clipAssetReferenceCreationDTO{ClipRef: creation.ClipRef, ClipID: creation.ClipID.String(), AssetID: creation.AssetID.String(), Position: creation.Position, ExpectedScriptRevision: creation.ExpectedScriptRevision}
	}
	if value.ClipMerge != nil {
		merge := value.ClipMerge
		sourceIDs := make([]string, 0, len(merge.Sources))
		for _, source := range merge.Sources {
			sourceIDs = append(sourceIDs, source.ID.String())
		}
		assetIDs := make([]string, 0, len(merge.AssetIDs))
		for _, assetID := range merge.AssetIDs {
			assetIDs = append(assetIDs, assetID.String())
		}
		result.ClipMerge = &clipMergeDTO{SourceClipIDs: sourceIDs, ReplacementClipID: merge.Replacement.ID.String(), Title: merge.Replacement.Title,
			PlannedDurationSeconds: merge.Replacement.PlannedDurationSeconds, AssetIDs: assetIDs,
			InheritedFirstFrame: merge.FirstFrame.SourceVersion != nil, InheritedLastFrame: merge.LastFrame.SourceVersion != nil}
	}
	if value.ClipSplit != nil {
		split := value.ClipSplit
		replacements := make([]clipSplitReplacementDTO, 0, len(split.Replacements))
		for _, replacement := range split.Replacements {
			replacements = append(replacements, clipSplitReplacementDTO{ClipID: replacement.Clip.ID.String(), Title: replacement.Clip.Title, PlannedDurationSeconds: replacement.Clip.PlannedDurationSeconds})
		}
		result.ClipSplit = &clipSplitDTO{SourceClipID: split.Source.ID.String(), SourceTitle: split.Source.Title, SourcePlannedDurationSeconds: split.Source.PlannedDurationSeconds, Replacements: replacements,
			InheritedFirstFrame: split.FirstFrame.SourceVersion != nil, InheritedLastFrame: split.LastFrame.SourceVersion != nil}
	}
	if value.EpisodeScript != nil {
		result.EpisodeScript = &episodeScriptDTO{EpisodeID: value.EpisodeScript.EpisodeID.String(), Before: value.EpisodeScript.Before, After: value.EpisodeScript.After}
	}
	if value.ChangeSetRevert != nil {
		result.ChangeSetRevert = &changeSetRevertDTO{OriginalChangeSetID: value.ChangeSetRevert.OriginalChangeSetID.String()}
	}
	return result
}

type eventDTO struct {
	Type        string    `json:"type"`
	SessionID   string    `json:"session_id"`
	TurnID      string    `json:"turn_id,omitempty"`
	ChangeSetID string    `json:"change_set_id,omitempty"`
	TextDelta   string    `json:"text_delta,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	TurnStatus  string    `json:"turn_status,omitempty"`
	FailureCode string    `json:"failure_code,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func mapEvent(value agentapp.StudioEvent) eventDTO {
	return eventDTO{Type: string(value.Type), SessionID: value.SessionID.String(), TurnID: value.TurnID.String(), ChangeSetID: value.ChangeSetID.String(),
		TextDelta: value.TextDelta, ToolName: value.ToolName, TurnStatus: string(value.TurnStatus), FailureCode: value.FailureCode,
		Summary: value.Summary, OccurredAt: value.OccurredAt}
}

var _ SessionCommands = (*agentsession.Service)(nil)
var _ TurnCommands = (*agentscheduler.TurnScheduler)(nil)
var _ ChangeSetCommands = (*changesetapp.ReviewCatalog)(nil)
var _ EventStream = (*agentsse.Broker)(nil)
