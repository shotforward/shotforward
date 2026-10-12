// Package agentsession coordinates Agent Session lifecycle across Studio
// persistence, the isolated workspace, Codex Runtime, and ShotForward MCP.
package agentsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrInvalidConfiguration = errors.New("invalid Agent Session service configuration")
	ErrInvalidRequest       = errors.New("invalid Agent Session request")
	ErrSessionNotFound      = errors.New("Agent Session not found")
	ErrRuntimeNotReady      = errors.New("Agent Runtime is not ready")
	ErrModelUnavailable     = errors.New("Agent model is unavailable")
	ErrSessionHasActiveTurn = errors.New("Agent Session has an active Turn")
	ErrRevisionConflict     = errors.New("Agent Session revision conflict")
)

type Store interface {
	CreateAgentSessionWithBinding(context.Context, agent.AgentSession, agent.MCPBinding) error
	AgentSession(context.Context, agent.SessionID) (agent.AgentSession, error)
	AgentSessions(context.Context, project.ProjectID, project.EpisodeID, bool) ([]agent.AgentSession, error)
	SaveAgentSession(context.Context, agent.AgentSession, int64) error
	AgentTurns(context.Context, agent.SessionID) ([]agent.AgentTurn, error)
	AgentInputOperations(context.Context, agent.SessionID) ([]agent.InputOperation, error)
	AgentMCPBinding(context.Context, agent.SessionID) (agent.MCPBinding, error)
	SaveAgentMCPBinding(context.Context, agent.MCPBinding, int64) error
}

type SessionChangeSets interface {
	ChangeSetsForAgentSession(context.Context, project.ProjectID, agent.SessionID) ([]changeset.ChangeSet, error)
}

type MCPBindings interface {
	GenerateRouteKey() (string, error)
	RegisterBinding(context.Context, agentmcp.BindingScope) (string, error)
	EnsureBinding(context.Context, agentmcp.BindingScope) (string, error)
	UnregisterBinding(context.Context, agent.SessionID) error
	BindingRegistered(agentmcp.BindingScope) bool
}

type Workspace interface {
	Prepare(context.Context, agent.SessionID) (string, error)
	Restore(context.Context, agent.SessionID, string) error
	Discard(string) error
}

type Clock interface{ Now() time.Time }

type ConversationMedia interface {
	ListAgentConversationMedia(context.Context, projectapp.ListAgentConversationMediaInput) ([]project.AgentConversationMedia, error)
}

type Options struct {
	Store     Store
	Changes   SessionChangeSets
	Runtime   agentapp.Runtime
	MCP       MCPBindings
	Workspace Workspace
	IDs       agentapp.IDGenerator
	Clock     Clock
	Media     ConversationMedia
	Logger    *slog.Logger
}

type Service struct {
	store     Store
	changes   SessionChangeSets
	runtime   agentapp.Runtime
	mcp       MCPBindings
	workspace Workspace
	ids       agentapp.IDGenerator
	clock     Clock
	media     ConversationMedia
	logger    *slog.Logger
	restoreMu sync.Mutex
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Changes == nil || options.Runtime == nil || options.MCP == nil ||
		options.Workspace == nil || options.IDs == nil || options.Clock == nil {
		return nil, ErrInvalidConfiguration
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{store: options.Store, changes: options.Changes, runtime: options.Runtime, mcp: options.MCP,
		workspace: options.Workspace, ids: options.IDs, clock: options.Clock, media: options.Media, logger: options.Logger}, nil
}

func (s *Service) List(ctx context.Context, projectID project.ProjectID, episodeID project.EpisodeID, includeArchived bool) ([]agent.AgentSession, error) {
	if ctx == nil || projectID.Validate() != nil || episodeID.Validate() != nil {
		return nil, ErrInvalidRequest
	}
	return s.store.AgentSessions(ctx, projectID, episodeID, includeArchived)
}

// restoreSession rebuilds process-local MCP state and resumes the persisted
// Codex Thread. A transient restore failure never downgrades an active Session;
// the next snapshot, conversation request, or queued Turn can retry it.
func (s *Service) restoreSession(ctx context.Context, candidate agent.AgentSession) (agent.AgentSession, error) {
	s.restoreMu.Lock()
	defer s.restoreMu.Unlock()

	session, err := s.store.AgentSession(ctx, candidate.ID)
	if err != nil {
		return candidate, err
	}
	if session.Status == agent.SessionArchived {
		return session, nil
	}
	binding, err := s.store.AgentMCPBinding(ctx, session.ID)
	if err != nil {
		return session, err
	}
	scope := agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: session.ID, ProjectID: session.ProjectID, EpisodeID: session.EpisodeID}
	if session.Status == agent.SessionActive && binding.Status == agent.MCPBindingReady && s.mcp.BindingRegistered(scope) {
		return session, nil
	}
	if err := s.workspace.Restore(ctx, session.ID, binding.WorkspacePath); err != nil {
		return session, err
	}
	if binding.Status != agent.MCPBindingPreparing {
		at, timeErr := s.now()
		if timeErr != nil {
			return session, timeErr
		}
		preparing, transitionErr := binding.Transition(agent.MCPBindingPreparing, at, nil)
		if transitionErr != nil {
			return session, transitionErr
		}
		if err := s.store.SaveAgentMCPBinding(ctx, preparing, binding.Revision); err != nil {
			return session, err
		}
		binding = preparing
	}
	endpoint, err := s.mcp.EnsureBinding(ctx, scope)
	if err != nil {
		return session, err
	}
	request := agentapp.ThreadRequest{AgentSessionID: session.ID, ProjectID: session.ProjectID, EpisodeID: session.EpisodeID,
		RuntimeKind: runtimeKind(session.RuntimeKind), ModelID: session.ModelID, WorkspacePath: binding.WorkspacePath, MCPBindingURL: endpoint, WebSearchDisabled: session.WebSearchDisabled}
	thread, err := s.runtime.ResumeThread(ctx, session.CodexThreadID, request)
	if err != nil {
		if errors.Is(err, agentapp.ErrThreadUnavailable) {
			unavailable, transitionErr := s.markThreadUnavailable(ctx, session, binding)
			if transitionErr != nil {
				return session, errors.Join(fmt.Errorf("%w: resume persisted Agent Thread: %w", ErrRuntimeNotReady, err), transitionErr)
			}
			session = unavailable
		}
		return session, fmt.Errorf("%w: resume persisted Agent Thread: %w", ErrRuntimeNotReady, err)
	}
	if thread.CodexThreadID != session.CodexThreadID || thread.CodexSessionID != session.CodexSessionID {
		return session, errors.New("Runtime resumed a different Agent Session or Thread")
	}
	at, err := s.now()
	if err != nil {
		return session, err
	}
	ready, err := binding.Transition(agent.MCPBindingReady, at, nil)
	if err != nil {
		return session, err
	}
	if err := s.store.SaveAgentMCPBinding(ctx, ready, binding.Revision); err != nil {
		return session, err
	}
	if session.Status == agent.SessionHistoryUnavailable {
		restored, transitionErr := session.Transition(agent.SessionActive, at)
		if transitionErr != nil {
			return session, transitionErr
		}
		if err := s.store.SaveAgentSession(ctx, restored, session.Revision); err != nil {
			return session, err
		}
		session = restored
	}
	return session, nil
}

func (s *Service) markThreadUnavailable(ctx context.Context, session agent.AgentSession, binding agent.MCPBinding) (agent.AgentSession, error) {
	at, err := s.now()
	if err != nil {
		return session, err
	}
	if binding.Status == agent.MCPBindingPreparing {
		failure := &agent.Failure{Code: "thread_unavailable", Summary: "The persisted Agent Thread is no longer available."}
		failed, transitionErr := binding.Transition(agent.MCPBindingFailed, at, failure)
		if transitionErr != nil {
			return session, transitionErr
		}
		if err := s.store.SaveAgentMCPBinding(ctx, failed, binding.Revision); err != nil {
			return session, err
		}
	}
	if session.Status != agent.SessionActive {
		return session, nil
	}
	unavailable, err := session.Transition(agent.SessionHistoryUnavailable, at)
	if err != nil {
		return session, err
	}
	if err := s.store.SaveAgentSession(ctx, unavailable, session.Revision); err != nil {
		return session, err
	}
	return unavailable, nil
}

func hasNonTerminalTurn(turns []agent.AgentTurn) bool {
	for _, turn := range turns {
		if !turn.Status.IsTerminal() {
			return true
		}
	}
	return false
}

func (s *Service) Create(ctx context.Context, input agentapp.CreateSessionInput) (_ agent.AgentSession, returnErr error) {
	if input.RuntimeKind == "" {
		input.RuntimeKind = agentapp.RuntimeUserCodex
	}
	if ctx == nil || input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil || !validModelID(input.ModelID) ||
		(input.RuntimeKind != agentapp.RuntimeUserCodex && input.RuntimeKind != agentapp.RuntimeShotForward) {
		return agent.AgentSession{}, ErrInvalidRequest
	}
	startedAt, stageStartedAt := time.Now(), time.Now()
	logger := s.logger.With("project_id", input.ProjectID.String(), "episode_id", input.EpisodeID.String(), "runtime_kind", input.RuntimeKind)
	defer func() {
		logger.InfoContext(ctx, "Agent session preparation completed",
			"elapsed_ms", time.Since(startedAt).Milliseconds(), "succeeded", returnErr == nil)
	}()
	logStage := func(stage string, err error) {
		logger.InfoContext(ctx, "Agent session preparation stage completed",
			"stage", stage, "elapsed_ms", time.Since(stageStartedAt).Milliseconds(), "succeeded", err == nil)
		stageStartedAt = time.Now()
	}
	if controller, ok := s.runtime.(agentapp.RuntimeSelectionController); ok {
		selected, err := controller.ActiveRuntime(ctx)
		logStage("runtime_selection", err)
		if err != nil {
			return agent.AgentSession{}, err
		}
		if selected != input.RuntimeKind {
			return agent.AgentSession{}, agentapp.ErrInvalidRuntimeSelection
		}
	}
	var runtimeSnapshot agentapp.RuntimeSnapshot
	var err error
	if selector, ok := s.runtime.(agentapp.RuntimeSnapshotSelector); ok {
		runtimeSnapshot, err = selector.SnapshotFor(ctx, input.RuntimeKind)
	} else {
		runtimeSnapshot, err = s.runtime.Snapshot(ctx)
	}
	logStage("runtime_snapshot", err)
	if err != nil || runtimeSnapshot.Health != agentapp.RuntimeReady {
		return agent.AgentSession{}, errors.Join(ErrRuntimeNotReady, err)
	}
	var models []agentapp.Model
	if selector, ok := s.runtime.(agentapp.RuntimeModelSelector); ok {
		models, err = selector.ModelsFor(ctx, input.RuntimeKind)
	} else {
		models, err = s.runtime.Models(ctx)
	}
	logStage("models", err)
	if err != nil {
		return agent.AgentSession{}, fmt.Errorf("create Agent Session: list models: %w", err)
	}
	available := false
	for _, model := range models {
		modelRuntime := model.RuntimeKind
		if modelRuntime == "" {
			modelRuntime = agentapp.RuntimeUserCodex
		}
		if model.ID == input.ModelID && !model.Hidden && modelRuntime == input.RuntimeKind {
			available = true
			break
		}
	}
	if !available {
		return agent.AgentSession{}, ErrModelUnavailable
	}
	sessionID, err := s.ids.NewSessionID()
	if err != nil {
		return agent.AgentSession{}, err
	}
	bindingID, err := s.ids.NewMCPBindingID()
	if err != nil {
		return agent.AgentSession{}, err
	}
	workspacePath, err := s.workspace.Prepare(ctx, sessionID)
	logStage("workspace", err)
	if err != nil {
		return agent.AgentSession{}, fmt.Errorf("create Agent Session workspace: %w", err)
	}
	cleanupWorkspace := true
	defer func() {
		if cleanupWorkspace {
			returnErr = errors.Join(returnErr, s.workspace.Discard(workspacePath))
		}
	}()
	route, err := s.mcp.GenerateRouteKey()
	if err != nil {
		return agent.AgentSession{}, err
	}
	endpoint, err := s.mcp.RegisterBinding(ctx, agentmcp.BindingScope{RouteKey: route, SessionID: sessionID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID})
	logStage("binding_register", err)
	if err != nil {
		return agent.AgentSession{}, err
	}
	cleanupBinding := true
	defer func() {
		if cleanupBinding {
			returnErr = errors.Join(returnErr, s.mcp.UnregisterBinding(context.WithoutCancel(ctx), sessionID))
		}
	}()
	thread, err := s.runtime.StartThread(ctx, agentapp.ThreadRequest{AgentSessionID: sessionID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID,
		RuntimeKind: input.RuntimeKind, ModelID: input.ModelID, WorkspacePath: workspacePath, MCPBindingURL: endpoint, WebSearchDisabled: input.WebSearchDisabled})
	logStage("thread_start_and_scope_verify", err)
	if err != nil {
		return agent.AgentSession{}, fmt.Errorf("create Agent Session Thread: %w", err)
	}
	at, err := s.now()
	if err != nil {
		return agent.AgentSession{}, err
	}
	session := agent.AgentSession{ID: sessionID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID,
		CodexThreadID: thread.CodexThreadID, CodexSessionID: thread.CodexSessionID, ModelID: input.ModelID, RuntimeKind: string(input.RuntimeKind),
		CachedTitle: thread.Title, WebSearchDisabled: input.WebSearchDisabled, Status: agent.SessionActive,
		Revision: 1, CreatedAt: at, UpdatedAt: at}
	binding := agent.MCPBinding{ID: bindingID, AgentSessionID: sessionID, WorkspacePath: workspacePath, RouteKey: route,
		Status: agent.MCPBindingReady, Revision: 1, LastVerifiedAt: &at, CreatedAt: at, UpdatedAt: at}
	if session.ValidateNew() != nil || binding.ValidateNew() != nil {
		return agent.AgentSession{}, errors.New("create Agent Session: Runtime returned invalid identity or title")
	}
	err = s.store.CreateAgentSessionWithBinding(ctx, session, binding)
	logStage("persist", err)
	if err != nil {
		return agent.AgentSession{}, err
	}
	cleanupWorkspace, cleanupBinding = false, false
	return session, nil
}

func runtimeKind(value string) agentapp.RuntimeKind {
	if value == string(agentapp.RuntimeShotForward) {
		return agentapp.RuntimeShotForward
	}
	return agentapp.RuntimeUserCodex
}

func (s *Service) Snapshot(ctx context.Context, sessionID agent.SessionID) (agentapp.SessionSnapshot, error) {
	if ctx == nil || sessionID.Validate() != nil {
		return agentapp.SessionSnapshot{}, ErrInvalidRequest
	}
	session, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		return agentapp.SessionSnapshot{}, err
	}
	turns, err := s.store.AgentTurns(ctx, sessionID)
	if err != nil {
		return agentapp.SessionSnapshot{}, err
	}
	if !hasNonTerminalTurn(turns) && (session.Status == agent.SessionActive || session.Status == agent.SessionHistoryUnavailable) {
		session, err = s.restoreSession(ctx, session)
		if err != nil {
			// Persisted Turns remain authoritative even when their Codex Thread can
			// no longer be resumed. Returning them prevents clients from retaining
			// a stale queued snapshot after the scheduler has already failed a Turn.
			if len(turns) == 0 {
				return agentapp.SessionSnapshot{}, fmt.Errorf("restore Agent Session: %w", err)
			}
		}
	}
	if len(turns) > 0 {
		session = s.backfillSessionTitle(ctx, session)
	}
	sets, err := s.changes.ChangeSetsForAgentSession(ctx, session.ProjectID, sessionID)
	if err != nil {
		return agentapp.SessionSnapshot{}, err
	}
	for _, value := range sets {
		if value.ProjectID != session.ProjectID || value.EpisodeID != session.EpisodeID || value.Source.AgentSessionID != sessionID {
			return agentapp.SessionSnapshot{}, errors.New("Agent Session snapshot contains an object outside its scope")
		}
	}
	return agentapp.SessionSnapshot{Session: session, Turns: turns, ChangeSets: sets}, nil
}

func (s *Service) Conversation(ctx context.Context, sessionID agent.SessionID, cursor string, limit int) (agentapp.ConversationPage, error) {
	if ctx == nil || sessionID.Validate() != nil || len(cursor) > 4*1024 || strings.ContainsRune(cursor, 0) || limit < 1 || limit > 50 {
		return agentapp.ConversationPage{}, ErrInvalidRequest
	}
	session, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		return agentapp.ConversationPage{}, err
	}
	turns, err := s.store.AgentTurns(ctx, sessionID)
	if err != nil {
		return agentapp.ConversationPage{}, err
	}
	if !hasNonTerminalTurn(turns) && (session.Status == agent.SessionActive || session.Status == agent.SessionHistoryUnavailable) {
		session, err = s.restoreSession(ctx, session)
		if err != nil {
			return agentapp.ConversationPage{}, fmt.Errorf("restore Agent Session: %w", err)
		}
	}
	if len(turns) > 0 {
		session = s.backfillSessionTitle(ctx, session)
	}
	// Only persisted ShotForward Turns can appear in this conversation. A new
	// empty Session has no Runtime history to map; avoid another app-server RPC
	// (and media/input reads) immediately after its verified thread/start.
	if len(turns) == 0 {
		return agentapp.ConversationPage{Turns: []agentapp.ConversationTurn{}}, nil
	}
	byCodexID := make(map[string]agent.AgentTurn, len(turns))
	byTurnID := make(map[agent.TurnID]agent.AgentTurn, len(turns))
	for _, turn := range turns {
		if turn.AgentSessionID != sessionID {
			continue
		}
		byTurnID[turn.ID] = turn
		if turn.CodexTurnID != "" {
			byCodexID[turn.CodexTurnID] = turn
		}
	}
	inputs, err := s.store.AgentInputOperations(ctx, sessionID)
	if err != nil {
		return agentapp.ConversationPage{}, err
	}
	inputsByTurn := make(map[agent.TurnID][]agent.InputOperation)
	for _, input := range inputs {
		if input.AgentSessionID != sessionID {
			continue
		}
		if _, exists := byTurnID[input.AgentTurnID]; !exists {
			continue
		}
		inputsByTurn[input.AgentTurnID] = append(inputsByTurn[input.AgentTurnID], input)
	}
	mediaByTurn := make(map[string][]project.AgentConversationMedia)
	if s.media != nil {
		media, mediaErr := s.media.ListAgentConversationMedia(ctx, projectapp.ListAgentConversationMediaInput{
			ProjectID: session.ProjectID, EpisodeID: session.EpisodeID, SessionID: session.ID.String(),
		})
		if mediaErr != nil {
			return agentapp.ConversationPage{}, mediaErr
		}
		for _, item := range media {
			if item.TurnID != "" {
				mediaByTurn[item.TurnID] = append(mediaByTurn[item.TurnID], item)
			}
		}
	}
	runtimePage, err := s.runtime.ListConversation(ctx, session.CodexThreadID, cursor, limit)
	if err != nil {
		return agentapp.ConversationPage{}, fmt.Errorf("%w: read Agent conversation: %w", ErrRuntimeNotReady, err)
	}
	result := agentapp.ConversationPage{Turns: make([]agentapp.ConversationTurn, 0, len(runtimePage.Turns)), NextCursor: runtimePage.NextCursor}
	present := make(map[agent.TurnID]bool, len(runtimePage.Turns))
	for _, runtimeTurn := range runtimePage.Turns {
		turn, exists := byCodexID[runtimeTurn.CodexTurnID]
		if !exists {
			continue
		}
		present[turn.ID] = true
		result.Turns = append(result.Turns, agentapp.ConversationTurn{
			Turn: turn, Messages: mergeConversationMessages(sessionID, runtimeTurn.Messages, inputsByTurn[turn.ID]), Media: mediaByTurn[turn.ID.String()],
		})
	}
	if cursor == "" {
		for _, turn := range missingFirstPageTurns(turns, result.Turns, present, inputsByTurn, sessionID, limit) {
			result.Turns = append(result.Turns, agentapp.ConversationTurn{
				Turn: turn, Messages: mergeConversationMessages(sessionID, nil, inputsByTurn[turn.ID]), Media: mediaByTurn[turn.ID.String()],
			})
		}
	}
	sortConversationTurns(result.Turns)
	return result, nil
}

// Runtime history includes private context prepended at dispatch time, whereas
// Studio persists what the user actually typed. Unwrap only a known ShotForward
// envelope that ends in the same Turn's exact queued input. Do not strip text
// from assistant messages, historical inputs without provenance, or a user who
// deliberately typed/pasted the context themselves.
func conversationUserInputText(runtimeText string, inputs []agent.InputOperation) string {
	for _, input := range inputs {
		if runtimeText == input.InputText {
			return runtimeText
		}
	}
	if !strings.HasPrefix(runtimeText, agentmcp.VideoPromptContextInstructionPreamble+"\n") {
		return runtimeText
	}
	for _, input := range inputs {
		if input.Action == agent.InputQueueTurn && input.InputText != "" && strings.HasSuffix(runtimeText, "\n\n"+input.InputText) {
			return input.InputText
		}
	}
	return runtimeText
}

// backfillSessionTitle repairs sessions created before first-input titles were
// cached. Metadata repair is best-effort and must never make history unreadable.
func (s *Service) backfillSessionTitle(ctx context.Context, session agent.AgentSession) agent.AgentSession {
	if !agentapp.IsDefaultSessionTitle(session.CachedTitle) {
		return session
	}
	thread, err := s.runtime.ReadThread(ctx, session.CodexThreadID)
	if err != nil {
		return session
	}
	title := agentapp.SessionTitleFromText(thread.Title)
	if title == "" || agentapp.IsDefaultSessionTitle(title) {
		return session
	}
	updated := session
	updated.CachedTitle = title
	updated.Revision++
	if err := s.store.SaveAgentSession(ctx, updated, session.Revision); err != nil {
		if current, readErr := s.store.AgentSession(ctx, session.ID); readErr == nil {
			return current
		}
		return session
	}
	return updated
}

func publicMessageID(sessionID agent.SessionID, runtimeItemID string) string {
	digest := sha256.Sum256([]byte(sessionID.String() + "\x00" + runtimeItemID))
	return "agm_" + hex.EncodeToString(digest[:16])
}

func (s *Service) Archive(ctx context.Context, sessionID agent.SessionID, expectedRevision int64) (agent.AgentSession, error) {
	if ctx == nil || sessionID.Validate() != nil || expectedRevision < 1 {
		return agent.AgentSession{}, ErrInvalidRequest
	}
	current, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		return agent.AgentSession{}, err
	}
	if current.Status == agent.SessionArchived {
		return current, nil
	}
	if current.Revision != expectedRevision {
		return agent.AgentSession{}, ErrRevisionConflict
	}
	turns, err := s.store.AgentTurns(ctx, sessionID)
	if err != nil {
		return agent.AgentSession{}, err
	}
	for _, turn := range turns {
		if !turn.Status.IsTerminal() {
			return agent.AgentSession{}, ErrSessionHasActiveTurn
		}
	}
	at, err := s.now()
	if err != nil {
		return agent.AgentSession{}, err
	}
	archived, err := current.Transition(agent.SessionArchived, at)
	if err != nil {
		return agent.AgentSession{}, err
	}
	if err := s.store.SaveAgentSession(ctx, archived, expectedRevision); err != nil {
		return agent.AgentSession{}, err
	}
	_ = s.mcp.UnregisterBinding(context.WithoutCancel(ctx), sessionID)
	return archived, nil
}

func (s *Service) now() (time.Time, error) {
	value := s.clock.Now()
	if value.IsZero() || value.Location() != time.UTC || value.UnixMilli() < 0 || value.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("Agent Session Clock returned an invalid timestamp")
	}
	return value, nil
}

func validModelID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00") && len([]rune(value)) <= 200
}
