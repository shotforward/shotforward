package agentmcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const serverInstructions = "ShotForward project facts must come from these tools. At the start of every Turn, declare whether the user is discussing or requesting a persistent project change. Read an object before changing it. Persistent script, Asset definition/Prompt, Storyboard structure/Prompt, dialogue, or Clip reference-media binding edits must use a ChangeSet. Use one semantic scope per ChangeSet: one Asset, one Clip, one Scene with its Clips, or one Episode script; split cross-scope work into multiple ChangeSets. Recycle-bin tools are only for an explicit user request to inspect or restore deleted Assets; never consult deleted Assets for duplicate detection before creating an Asset. Asset restoration preserves the original ID, identity, Prompt, and media and must use its own ChangeSet. Build large Storyboards as one Scene ChangeSet at a time, re-reading Scenes after every applied Scene. Plan one complete dramatic beat per Clip and estimate its duration from spoken dialogue, physical action, reaction pauses, and camera movement. Portable Clips may be 4-15 seconds, but default to 8 seconds and normally use 6-10 seconds. Use 4-5 seconds only for an intentionally brief insert, reaction, empty shot, or transition. A dialogue speaker change is not by itself a split: keep a continuous exchange or connected actions in one Clip when place, primary subjects, reference set, and performance remain coherent, using Clip-local internal cuts if needed. Split only when time or place, primary subject, dramatic outcome, reference set, or camera plan materially changes. Preserve the Episode duration budget by merging adjacent micro-beats; never pad every short Clip merely to pass duration validation. Use append_clip_merge_operation when the user explicitly asks to merge existing adjacent Clips; it preserves the source Clips for Undo, inherits only the first source's current first frame and the last source's current last frame, and never inherits video or other media. Use append_clip_split_operation when the user explicitly asks to split one Clip; preserve narrative order, choose each result's duration from its content even when the total changes, make every result self-contained, and never emulate the split with separate create and delete operations. Each Clip owns three independent video Prompts for first-frame, first-and-last-frame, and reference generation. Creating a Clip initializes only its selected mode Prompt; changing one mode must not overwrite either other mode. For Storyboard creation, the request includes system-injected current-model rules: use its exact generator and mode-compatible provider model, write the model-aware editable Prompt, and keep exact @Asset names at semantic reference points so submission can deterministically compile provider labels. Default to reference mode unless the user's current request or editable storyboard template explicitly chooses another mode. Bind every depended-on Asset in intended reference order. For uploaded image, video, or audio media, call list_reference_media, keep its exact @name in the editable reference-mode Prompt, and include the complete ordered reference_artifact_ids set in append_prompt_operations; submission compiles those mentions to the selected provider's image/video/audio label syntax. Do not reject a Prompt merely for formatting or wording imperfections; only non-empty and technical generation constraints are enforced. Scene and Clip order is determined by operation order; never invent durable sequence numbers. Finalizing safely auto-applies the ChangeSet and the UI offers deterministic Undo. When the user explicitly asks to generate an image, call generate_conversation_image; it automatically uses images attached to the active Turn and adds the result to this conversation. Do not use a ChangeSet or a Runtime-native image tool for image generation. Generated conversation images are not automatically saved as Assets or applied to video units. Provider configuration, video/audio generation, preview, and export remain user-controlled UI actions. No file, credential, or arbitrary project access is available. Tool scope is fixed to this Session's Project and Episode."

const videoPromptAdaptationInstructions = " Manual model-specific video Prompt adaptation remains available when the user requests it, including after a model or generation-mode switch. When the user sends a card-prepared H3, Seedance, or Wan adaptation request, call get_video_prompt_adaptation_context with the exact Clip, generation mode, generator, and provider model from that request, then write only through append_video_prompt_adaptation. Its daemon-derived prompt_family and prompt_profile are authoritative. If the target mode is empty, context may return a source Prompt from another mode; always copy its source_generation_mode, source_family, and source_prompt_hash back exactly. Treat the returned references as the current bound set. Preserve that set by default. When the user explicitly @mentions an unbound uploaded image, video, or audio for this Clip, or explicitly asks to add, remove, or replace uploaded media, call list_reference_media, put exact @names at their semantic Prompt positions, and pass the complete desired ordered reference_artifact_ids set to append_video_prompt_adaptation. Never remove a binding merely because the user did not mention it. If a medium's role cannot be inferred safely from its name, description, source Prompt, and Clip context, ask all essential clarification questions together in chat and stop without beginning a ChangeSet. After the user replies, read the adaptation context again. When clear, submit every ready desired reference_key with one or more purposes and an optional note recording what to copy or avoid. H3 Base and Ref2VA retain fixed field names, format values, and timestamps after Shot 1, but write editable bodies in the user's requested language, otherwise keep the current creative language. Preserve original-language dialogue inside <d>[Language]...</d>, lyrics inside <lyrics>...</lyrics>, and visible text inside <on_screen_text>...</on_screen_text>. Submission prepares an English copy without modifying the editable Prompt. Retain <Subject N>, but use exact @reference names for image/video/audio media; submission deterministically compiles them to <Picture>/<Video>/<Audio> labels in first-mention order. Prompt content and structure are user-reviewable guidance and are not daemon-blocking validation. Never change the selected model, mode, duration, or another family version."

type cursorState struct {
	snapshot string
	offset   int
}

type turnPreparation struct {
	turnID agent.TurnID
	ready  chan struct{}
}

type bindingState struct {
	turnGate          sync.RWMutex
	scope             BindingScope
	preparing         *turnPreparation
	turn              *ActiveTurn
	readSet           *TurnReadSet
	drafts            map[changeset.ChangeSetID]bool // true after finalize; retained for idempotent replay
	cursors           map[string]cursorState
	intent            string
	target            string
	videoPromptTarget *videoPromptAdaptationTargetArguments
}

type Server struct {
	queries  ProjectQueries
	drafts   changesetapp.DraftCommands
	cleanup  changesetapp.DraftCleanup
	images   ImageCommands
	events   agentapp.EventPublisher
	random   io.Reader
	token    string
	tokenEnv string

	mutex       sync.RWMutex
	randomMutex sync.Mutex
	bindings    map[string]*bindingState
	bySession   map[agent.SessionID]string

	listener net.Listener
	http     *http.Server
	started  bool
}

func NewServer(options Options) (*Server, error) {
	if options.Queries == nil || options.Drafts == nil || options.DraftCleanup == nil {
		return nil, ErrInvalidConfiguration
	}
	randomSource := options.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	token, err := randomSecret(randomSource, 32)
	if err != nil {
		return nil, fmt.Errorf("create ShotForward MCP token: %w", err)
	}
	tokenEnv := strings.TrimSpace(options.TokenEnvironmentVariable)
	if tokenEnv == "" {
		tokenEnv = DefaultTokenEnvironmentVariable
	}
	if !validEnvironmentName(tokenEnv) {
		return nil, ErrInvalidConfiguration
	}
	if options.Events == nil {
		options.Events = agentapp.DiscardEvents{}
	}
	return &Server{
		queries:   options.Queries,
		drafts:    options.Drafts,
		cleanup:   options.DraftCleanup,
		images:    options.Images,
		events:    options.Events,
		random:    randomSource,
		token:     token,
		tokenEnv:  tokenEnv,
		bindings:  make(map[string]*bindingState),
		bySession: make(map[agent.SessionID]string),
	}, nil
}

func (s *Server) Start() error {
	if s == nil {
		return ErrInvalidConfiguration
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.started {
		return errors.New("ShotForward MCP server instances cannot be restarted")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for ShotForward MCP: %w", err)
	}
	s.listener = listener
	s.started = true
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       65 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	go func(server *http.Server, active net.Listener) {
		_ = server.Serve(active)
	}(s.http, listener)
	return nil
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	s.mutex.Lock()
	server := s.http
	s.http = nil
	s.listener = nil
	s.mutex.Unlock()
	var closeErrors []error
	if server != nil {
		if err := server.Shutdown(ctx); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	s.mutex.RLock()
	sessions := make([]agent.SessionID, 0, len(s.bySession))
	for sessionID := range s.bySession {
		sessions = append(sessions, sessionID)
	}
	s.mutex.RUnlock()
	for _, sessionID := range sessions {
		if err := s.UnregisterBinding(ctx, sessionID); err != nil && !errors.Is(err, ErrBindingNotFound) {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// Credential is intentionally the only way to retrieve the process-scoped
// secret that must be injected into the Codex app-server environment.
func (s *Server) Credential() Credential {
	if s == nil {
		return Credential{}
	}
	return Credential{EnvironmentVariable: s.tokenEnv, Value: s.token}
}

func (s *Server) RegisterBinding(ctx context.Context, scope BindingScope) (string, error) {
	if s == nil || ctx == nil || scope.SessionID.Validate() != nil || scope.ProjectID.Validate() != nil || scope.EpisodeID.Validate() != nil || !validRouteKey(scope.RouteKey) {
		return "", ErrInvalidBinding
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: scope.ProjectID, EpisodeID: scope.EpisodeID})
	if err != nil || episode.ProjectID != scope.ProjectID || episode.ID != scope.EpisodeID {
		return "", ErrInvalidBinding
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.listener == nil {
		return "", errors.New("ShotForward MCP server is not started")
	}
	if _, exists := s.bindings[scope.RouteKey]; exists {
		return "", ErrBindingConflict
	}
	if _, exists := s.bySession[scope.SessionID]; exists {
		return "", ErrBindingConflict
	}
	s.bindings[scope.RouteKey] = &bindingState{scope: scope, cursors: make(map[string]cursorState)}
	s.bySession[scope.SessionID] = scope.RouteKey
	return "http://" + s.listener.Addr().String() + "/mcp/" + url.PathEscape(scope.RouteKey), nil
}

// EnsureBinding restores a persisted Session route after daemon restart and is
// idempotent when the exact same immutable scope is already registered.
func (s *Server) EnsureBinding(ctx context.Context, scope BindingScope) (string, error) {
	if s == nil || ctx == nil || scope.SessionID.Validate() != nil || scope.ProjectID.Validate() != nil || scope.EpisodeID.Validate() != nil || !validRouteKey(scope.RouteKey) {
		return "", ErrInvalidBinding
	}
	s.mutex.RLock()
	existing, ok := s.bindingBySessionLocked(scope.SessionID)
	listener := s.listener
	s.mutex.RUnlock()
	if ok {
		if existing.scope != scope || listener == nil {
			return "", ErrBindingConflict
		}
		return "http://" + listener.Addr().String() + "/mcp/" + url.PathEscape(scope.RouteKey), nil
	}
	return s.RegisterBinding(ctx, scope)
}

// BindingRegistered reports whether the exact Session scope is already live
// in this process. It lets the scheduler distinguish a freshly verified
// Thread from a persisted Binding that must be restored after daemon restart.
func (s *Server) BindingRegistered(scope BindingScope) bool {
	if s == nil {
		return false
	}
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	existing, ok := s.bindingBySessionLocked(scope.SessionID)
	return ok && existing.scope == scope && s.listener != nil
}

func (s *Server) UnregisterBinding(ctx context.Context, sessionID agent.SessionID) error {
	if s == nil || ctx == nil || sessionID.Validate() != nil {
		return ErrInvalidBinding
	}
	s.mutex.RLock()
	route, exists := s.bySession[sessionID]
	if !exists {
		s.mutex.RUnlock()
		return ErrBindingNotFound
	}
	state := s.bindings[route]
	s.mutex.RUnlock()
	if state == nil {
		return ErrBindingNotFound
	}
	state.turnGate.Lock()
	defer state.turnGate.Unlock()
	s.mutex.Lock()
	if s.bindings[route] != state {
		s.mutex.Unlock()
		return ErrBindingNotFound
	}
	projectID := state.scope.ProjectID
	drafts := unfinalizedDrafts(state.drafts)
	var turnID agent.TurnID
	if state.turn != nil {
		turnID = state.turn.AgentTurnID
	}
	if state.preparing != nil {
		close(state.preparing.ready)
		state.preparing = nil
	}
	delete(s.bySession, sessionID)
	delete(s.bindings, route)
	s.mutex.Unlock()
	cleanupErr := s.discardDrafts(ctx, projectID, drafts)
	if turnID != "" {
		cleanupErr = errors.Join(cleanupErr, s.cleanup.DiscardTurnDrafts(ctx, projectID, turnID))
	}
	return cleanupErr
}

// PrepareTurn opens the narrow activation window before Codex receives the
// Turn request. Project tool calls that arrive in that window wait for
// ActivateTurn instead of incorrectly reporting that no Turn is active.
func (s *Server) PrepareTurn(sessionID agent.SessionID, turnID agent.TurnID) error {
	if s == nil || sessionID.Validate() != nil || turnID.Validate() != nil {
		return ErrInvalidBinding
	}
	s.mutex.RLock()
	state, ok := s.bindingBySessionLocked(sessionID)
	s.mutex.RUnlock()
	if !ok {
		return ErrBindingNotFound
	}
	state.turnGate.Lock()
	defer state.turnGate.Unlock()
	s.mutex.Lock()
	defer s.mutex.Unlock()
	current, stillRegistered := s.bindingBySessionLocked(sessionID)
	if !stillRegistered || current != state {
		return ErrBindingNotFound
	}
	if state.turn != nil || state.preparing != nil {
		return ErrTurnConflict
	}
	state.preparing = &turnPreparation{turnID: turnID, ready: make(chan struct{})}
	return nil
}

func (s *Server) ActivateTurn(sessionID agent.SessionID, turn ActiveTurn) error {
	if s == nil || sessionID.Validate() != nil || turn.AgentTurnID.Validate() != nil || turn.Focus.Validate() != nil || turn.VideoPromptTarget.Validate() != nil || !validProtocolID(turn.CodexThreadID) || !validProtocolID(turn.CodexTurnID) ||
		(turn.RuntimeKind != "" && turn.RuntimeKind != agentapp.RuntimeUserCodex && turn.RuntimeKind != agentapp.RuntimeShotForward) {
		return ErrInvalidBinding
	}
	for _, artifactID := range turn.ArtifactIDs {
		if artifactID.Validate() != nil {
			return ErrInvalidBinding
		}
	}
	s.mutex.RLock()
	state, ok := s.bindingBySessionLocked(sessionID)
	s.mutex.RUnlock()
	if !ok {
		return ErrBindingNotFound
	}
	if target := turn.VideoPromptTarget; target != nil && target.Focus != nil {
		if target.Focus.ClipID != turn.Focus.ClipID {
			return ErrInvalidBinding
		}
		clip, err := s.queries.GetClip(context.Background(), projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: target.Focus.ClipID})
		if err != nil || clip.Clip.EpisodeID != state.scope.EpisodeID || clip.Clip.IsDeleted() {
			return ErrInvalidBinding
		}
	}
	state.turnGate.Lock()
	defer state.turnGate.Unlock()
	s.mutex.Lock()
	defer s.mutex.Unlock()
	current, stillRegistered := s.bindingBySessionLocked(sessionID)
	if !stillRegistered || current != state {
		return ErrBindingNotFound
	}
	if state.turn != nil {
		return ErrTurnConflict
	}
	if state.preparing != nil && state.preparing.turnID != turn.AgentTurnID {
		return ErrTurnConflict
	}
	copy := turn
	copy.ArtifactIDs = append([]project.ArtifactID(nil), turn.ArtifactIDs...)
	copy.VideoPromptTarget = cloneVideoPromptContext(turn.VideoPromptTarget)
	state.turn = &copy
	state.readSet = newTurnReadSet()
	state.drafts = make(map[changeset.ChangeSetID]bool)
	state.intent = ""
	state.target = ""
	state.videoPromptTarget = nil
	clear(state.cursors)
	if state.preparing != nil {
		close(state.preparing.ready)
		state.preparing = nil
	}
	return nil
}

func (s *Server) EndTurn(ctx context.Context, sessionID agent.SessionID, turnID agent.TurnID) error {
	if s == nil || ctx == nil || sessionID.Validate() != nil || turnID.Validate() != nil {
		return ErrInvalidBinding
	}
	s.mutex.RLock()
	state, ok := s.bindingBySessionLocked(sessionID)
	s.mutex.RUnlock()
	if !ok {
		return ErrBindingNotFound
	}
	state.turnGate.Lock()
	defer state.turnGate.Unlock()
	s.mutex.Lock()
	current, stillRegistered := s.bindingBySessionLocked(sessionID)
	if !stillRegistered || current != state {
		s.mutex.Unlock()
		return ErrBindingNotFound
	}
	if state.turn == nil {
		if state.preparing != nil && state.preparing.turnID == turnID {
			close(state.preparing.ready)
			state.preparing = nil
			s.mutex.Unlock()
			return nil
		}
		s.mutex.Unlock()
		return ErrTurnNotActive
	}
	if state.turn.AgentTurnID != turnID {
		s.mutex.Unlock()
		return ErrTurnNotActive
	}
	projectID := state.scope.ProjectID
	drafts := unfinalizedDrafts(state.drafts)
	state.turn = nil
	state.readSet = nil
	state.drafts = nil
	state.videoPromptTarget = nil
	clear(state.cursors)
	s.mutex.Unlock()
	return errors.Join(s.discardDrafts(ctx, projectID, drafts), s.cleanup.DiscardTurnDrafts(ctx, projectID, turnID))
}

// CleanupTurn removes only unfinalized drafts by their persisted source. It is
// safe after process restart when no in-memory MCP binding still exists.
func (s *Server) CleanupTurn(ctx context.Context, projectID project.ProjectID, turnID agent.TurnID) error {
	if s == nil || ctx == nil || projectID.Validate() != nil || turnID.Validate() != nil {
		return ErrInvalidBinding
	}
	return s.cleanup.DiscardTurnDrafts(ctx, projectID, turnID)
}

func (s *Server) discardDrafts(ctx context.Context, projectID project.ProjectID, drafts []changeset.ChangeSetID) error {
	var cleanupErrors []error
	for _, id := range drafts {
		if err := s.cleanup.DiscardDraft(ctx, projectID, id); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("discard ChangeSet draft %s: %w", id, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func unfinalizedDrafts(tracked map[changeset.ChangeSetID]bool) []changeset.ChangeSetID {
	result := make([]changeset.ChangeSetID, 0, len(tracked))
	for id, finalized := range tracked {
		if !finalized {
			result = append(result, id)
		}
	}
	return result
}

func (s *Server) GenerateRouteKey() (string, error) {
	if s == nil {
		return "", ErrInvalidConfiguration
	}
	return s.randomSecret(24)
}

func (s *Server) randomSecret(bytes int) (string, error) {
	s.randomMutex.Lock()
	defer s.randomMutex.Unlock()
	return randomSecret(s.random, bytes)
}

func (s *Server) bindingBySessionLocked(sessionID agent.SessionID) (*bindingState, bool) {
	route, ok := s.bySession[sessionID]
	if !ok {
		return nil, false
	}
	state, ok := s.bindings[route]
	return state, ok
}

func (s *Server) acquireBindingGate(route string) (func(), error) {
	s.mutex.RLock()
	state := s.bindings[route]
	s.mutex.RUnlock()
	if state == nil {
		return nil, toolError("binding_unavailable", "Session binding is unavailable")
	}
	state.turnGate.RLock()
	s.mutex.RLock()
	stillRegistered := s.bindings[route] == state
	s.mutex.RUnlock()
	if !stillRegistered {
		state.turnGate.RUnlock()
		return nil, toolError("binding_unavailable", "Session binding is unavailable")
	}
	return state.turnGate.RUnlock, nil
}

func (s *Server) awaitPreparedTurn(ctx context.Context, route string) error {
	for {
		s.mutex.RLock()
		state := s.bindings[route]
		if state == nil {
			s.mutex.RUnlock()
			return toolError("binding_unavailable", "Session binding is unavailable")
		}
		if state.turn != nil && state.readSet != nil {
			s.mutex.RUnlock()
			return nil
		}
		var ready <-chan struct{}
		if state.preparing != nil {
			ready = state.preparing.ready
		}
		s.mutex.RUnlock()
		if ready == nil {
			return toolError("turn_not_active", "no active Agent Turn is bound to this Session")
		}
		select {
		case <-ctx.Done():
			return toolError("turn_not_active", "the Agent Turn ended before project tools became active")
		case <-ready:
		}
	}
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !requestFromLoopback(request) || request.Header.Get("Origin") != "" {
		response.WriteHeader(http.StatusForbidden)
		return
	}
	route := strings.TrimPrefix(request.URL.EscapedPath(), "/mcp/")
	if route == request.URL.EscapedPath() {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	decodedRoute, err := url.PathUnescape(route)
	if err != nil || strings.Contains(decodedRoute, "/") {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mutex.RLock()
	state := s.bindings[decodedRoute]
	expectedAuthorization := "Bearer " + s.token
	authorized := state != nil && subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte(expectedAuthorization)) == 1
	s.mutex.RUnlock()
	if !authorized {
		response.Header().Set("WWW-Authenticate", "Bearer")
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch request.Method {
	case http.MethodGet:
		response.Header().Set("Allow", "POST, DELETE")
		response.WriteHeader(http.StatusMethodNotAllowed)
	case http.MethodDelete:
		response.WriteHeader(http.StatusOK)
	case http.MethodPost:
		s.handlePost(response, request, decodedRoute)
	default:
		response.Header().Set("Allow", "POST, DELETE")
		response.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func requestFromLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	hostName, _, err := net.SplitHostPort(request.Host)
	return err == nil && hostName == "127.0.0.1"
}

func randomSecret(source io.Reader, bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := io.ReadFull(source, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validRouteKey(value string) bool {
	if len(value) < 32 || len(value) > 100 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validEnvironmentName(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validProtocolID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 500 && !strings.ContainsAny(value, "\r\n\x00")
}
