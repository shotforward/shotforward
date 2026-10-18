package agentscheduler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const (
	MaxAgentInputBytes   = 128 * 1024
	defaultPollInterval  = time.Second
	maximumRecoveryTurns = agent.MaxGlobalActiveTurns + agent.MaxGlobalQueuedTurns
)

var (
	ErrInvalidScheduler         = errors.New("invalid Agent Turn scheduler configuration")
	ErrInvalidAgentInput        = errors.New("invalid Agent input")
	ErrInputIdempotencyConflict = errors.New("Agent input idempotency conflict")
	ErrSessionNotActive         = errors.New("Agent Session is not active")
	ErrTurnNotActive            = errors.New("Agent Turn is not active")
	ErrQueuedInputUnavailable   = errors.New("queued Agent input is unavailable")
)

// SchedulerStore is the Studio persistence surface needed by Turn scheduling.
// Implementations must make AdmitAgentInput atomic.
type SchedulerStore interface {
	AgentSession(context.Context, agent.SessionID) (agent.AgentSession, error)
	AgentTurn(context.Context, agent.TurnID) (agent.AgentTurn, error)
	AgentMCPBinding(context.Context, agent.SessionID) (agent.MCPBinding, error)
	AdmitAgentInput(context.Context, agent.AgentTurn, string, string, string) (agent.InputOperation, bool, error)
	QueuedAgentTurns(context.Context, int) ([]agent.AgentTurn, error)
	AgentTurnsForRecovery(context.Context, int) ([]agent.AgentTurn, error)
	SaveAgentTurn(context.Context, agent.AgentTurn, int64) error
	SaveAgentMCPBinding(context.Context, agent.MCPBinding, int64) error
}

type MCPController interface {
	BindingRegistered(agentmcp.BindingScope) bool
	EnsureBinding(context.Context, agentmcp.BindingScope) (string, error)
	PrepareTurn(agent.SessionID, agent.TurnID) error
	ActivateTurn(agent.SessionID, agentmcp.ActiveTurn) error
	EndTurn(context.Context, agent.SessionID, agent.TurnID) error
	CleanupTurn(context.Context, project.ProjectID, agent.TurnID) error
}

var _ MCPController = (*agentmcp.Server)(nil)

type SchedulerClock interface{ Now() time.Time }

type AgentMediaImporter interface {
	ImportAgentGeneratedMedia(context.Context, projectapp.ImportAgentGeneratedMediaInput) (project.AgentConversationMedia, error)
}

type SchedulerOptions struct {
	Store        SchedulerStore
	Runtime      agentapp.Runtime
	MCP          MCPController
	IDs          agentapp.IDGenerator
	Clock        SchedulerClock
	Events       agentapp.EventPublisher
	Media        AgentMediaImporter
	PollInterval time.Duration
}

type SubmitInput struct {
	AgentSessionID    agent.SessionID
	ClientOperationID string
	ModelID           string
	Text              string
	ImagePaths        []string
	ArtifactIDs       []project.ArtifactID
	Focus             agent.FocusSnapshot
	VideoPromptTarget *agentmcp.VideoPromptContext
}

type SubmitResult struct {
	Action agent.InputAction
	Turn   agent.AgentTurn
	Replay bool
}

type queuedInput struct {
	text              string
	imagePaths        []string
	artifactIDs       []project.ArtifactID
	focus             agent.FocusSnapshot
	videoPromptTarget *agentmcp.VideoPromptContext
}

// TurnScheduler keeps queued plaintext in memory for dispatch and persists the
// submitted text for conversation rendering. It still never reconstructs or
// automatically replays an input after process loss.
type TurnScheduler struct {
	store        SchedulerStore
	runtime      agentapp.Runtime
	mcp          MCPController
	ids          agentapp.IDGenerator
	clock        SchedulerClock
	events       agentapp.EventPublisher
	media        AgentMediaImporter
	pollInterval time.Duration
	wake         chan struct{}

	mutex      sync.Mutex
	recovered  bool
	queued     map[agent.TurnID]queuedInput
	active     map[agent.TurnID]struct{}
	compacting map[agent.SessionID]struct{}
	waitGroup  sync.WaitGroup
}

func NewTurnScheduler(options SchedulerOptions) (*TurnScheduler, error) {
	if options.Store == nil || options.Runtime == nil || options.MCP == nil || options.IDs == nil || options.Clock == nil {
		return nil, ErrInvalidScheduler
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.Events == nil {
		options.Events = agentapp.DiscardEvents{}
	}
	return &TurnScheduler{
		store: options.Store, runtime: options.Runtime, mcp: options.MCP, ids: options.IDs, clock: options.Clock,
		events: options.Events, media: options.Media, pollInterval: options.PollInterval, wake: make(chan struct{}, 1), queued: make(map[agent.TurnID]queuedInput), active: make(map[agent.TurnID]struct{}), compacting: make(map[agent.SessionID]struct{}),
	}, nil
}

func (s *TurnScheduler) SubmitInput(ctx context.Context, input SubmitInput) (SubmitResult, error) {
	if ctx == nil || input.AgentSessionID.Validate() != nil || input.Focus.Validate() != nil ||
		!validOneLine(input.ClientOperationID, 200) || !validOneLine(input.ModelID, 200) || !validAgentInputText(input.Text) || !validInputImages(input.ImagePaths, input.ArtifactIDs) || !validVideoPromptContext(input.VideoPromptTarget, input.Focus) {
		return SubmitResult{}, ErrInvalidAgentInput
	}
	input.VideoPromptTarget = copyVideoPromptContext(input.VideoPromptTarget)
	at, err := s.now()
	if err != nil {
		return SubmitResult{}, err
	}
	turnID, err := s.ids.NewTurnID()
	if err != nil {
		return SubmitResult{}, fmt.Errorf("submit Agent input: generate Turn ID: %w", err)
	}
	candidate := agent.AgentTurn{
		ID: turnID, AgentSessionID: input.AgentSessionID, ClientOperationID: input.ClientOperationID,
		ModelID: input.ModelID,
		Focus:   input.Focus, Status: agent.TurnQueued, Dispatch: agent.DispatchQueued, Revision: 1, CreatedAt: at,
	}
	fingerprint := inputFingerprint(input)

	s.mutex.Lock()
	compacting, compactionErr := s.contextCompactingLocked(ctx, input.AgentSessionID)
	if compactionErr != nil {
		s.mutex.Unlock()
		return SubmitResult{}, compactionErr
	}
	if compacting {
		s.mutex.Unlock()
		return SubmitResult{}, agentapp.ErrContextCompactionBusy
	}
	operation, created, err := s.store.AdmitAgentInput(ctx, candidate, fingerprint, input.Text, agentapp.SessionTitleFromText(input.Text))
	if err != nil {
		s.mutex.Unlock()
		return SubmitResult{}, mapAdmissionError(err)
	}
	if created && operation.Action == agent.InputQueueTurn {
		s.queued[operation.AgentTurnID] = queuedInput{text: input.Text, imagePaths: append([]string(nil), input.ImagePaths...), artifactIDs: append([]project.ArtifactID(nil), input.ArtifactIDs...), focus: input.Focus, videoPromptTarget: input.VideoPromptTarget}
	}
	s.mutex.Unlock()

	turn, err := s.store.AgentTurn(ctx, operation.AgentTurnID)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("submit Agent input: read admitted Turn: %w", err)
	}
	result := SubmitResult{Action: operation.Action, Turn: turn, Replay: !created}
	if !created {
		return result, nil
	}
	if operation.Action == agent.InputQueueTurn {
		s.publish(agentapp.StudioEvent{Type: agentapp.EventTurnQueued, SessionID: turn.AgentSessionID, TurnID: turn.ID, OccurredAt: turn.CreatedAt})
	}
	if operation.Action == agent.InputSteerTurn {
		session, err := s.store.AgentSession(ctx, input.AgentSessionID)
		if err != nil {
			return result, fmt.Errorf("submit Agent input: read Session for steer: %w", err)
		}
		if turn.CodexTurnID == "" || (turn.Status != agent.TurnRunning && turn.Status != agent.TurnWaitingUser) {
			return result, ErrTurnNotActive
		}
		var steerErr error
		if len(input.ImagePaths) > 0 {
			imageRuntime, ok := s.runtime.(agentapp.ImageSteeringRuntime)
			if !ok {
				return result, fmt.Errorf("submit Agent input: steer Runtime Turn: image input is unavailable")
			}
			steerErr = imageRuntime.SteerTurnWithImages(ctx, session.CodexThreadID, turn.CodexTurnID, input.Text, input.ImagePaths)
		} else {
			steerErr = s.runtime.SteerTurn(ctx, session.CodexThreadID, turn.CodexTurnID, input.Text)
		}
		if steerErr != nil {
			return result, fmt.Errorf("submit Agent input: steer Runtime Turn: %w", steerErr)
		}
		return result, nil
	}
	s.Notify()
	return result, nil
}

// CompactSession reserves an idle Session against new input admission. The
// Runtime must wait for its native completion notification before returning nil;
// a command acknowledgement alone does not finish this operation.
func (s *TurnScheduler) CompactSession(ctx context.Context, sessionID agent.SessionID) error {
	if ctx == nil || sessionID.Validate() != nil {
		return ErrInvalidAgentInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, ok := s.runtime.(agentapp.ContextCompactingRuntime)
	if !ok {
		return agentapp.ErrContextCompactionUnavailable
	}
	s.mutex.Lock()
	if _, exists := s.compacting[sessionID]; exists {
		s.mutex.Unlock()
		return agentapp.ErrContextCompactionBusy
	}
	session, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		s.mutex.Unlock()
		return err
	}
	if session.Status != agent.SessionActive {
		s.mutex.Unlock()
		return ErrSessionNotActive
	}
	if status, ok := s.runtime.(agentapp.ContextCompactionStatusRuntime); ok && status.ContextCompactionInProgress(session.CodexThreadID) {
		s.mutex.Unlock()
		return agentapp.ErrContextCompactionBusy
	}
	turns, err := s.store.AgentTurnsForRecovery(ctx, maximumRecoveryTurns)
	if err != nil {
		s.mutex.Unlock()
		return fmt.Errorf("compact Agent Session: read pending Turns: %w", err)
	}
	for _, turn := range turns {
		if turn.AgentSessionID == sessionID && !turn.Status.IsTerminal() {
			s.mutex.Unlock()
			return agentapp.ErrContextCompactionBusy
		}
	}
	s.compacting[sessionID] = struct{}{}
	s.mutex.Unlock()
	defer func() {
		s.mutex.Lock()
		delete(s.compacting, sessionID)
		s.mutex.Unlock()
	}()
	return runtime.CompactThread(ctx, session.CodexThreadID)
}

func (s *TurnScheduler) IsContextCompacting(sessionID agent.SessionID) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	compacting, _ := s.contextCompactingLocked(context.Background(), sessionID)
	return compacting
}

// The native operation can outlive an HTTP wait that was cancelled. Keep input
// admission closed until the Runtime releases that Thread, without persisting a
// fake Turn or keeping the cancelled request's scheduler reservation alive.
func (s *TurnScheduler) contextCompactingLocked(ctx context.Context, sessionID agent.SessionID) (bool, error) {
	if _, compacting := s.compacting[sessionID]; compacting {
		return true, nil
	}
	status, ok := s.runtime.(agentapp.ContextCompactionStatusRuntime)
	if !ok {
		return false, nil
	}
	session, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return status.ContextCompactionInProgress(session.CodexThreadID), nil
}

func (s *TurnScheduler) InterruptTurn(ctx context.Context, sessionID agent.SessionID, turnID agent.TurnID) (agent.AgentTurn, error) {
	if ctx == nil || sessionID.Validate() != nil || turnID.Validate() != nil {
		return agent.AgentTurn{}, ErrInvalidAgentInput
	}
	turn, err := s.store.AgentTurn(ctx, turnID)
	if err != nil {
		return agent.AgentTurn{}, err
	}
	if turn.AgentSessionID != sessionID {
		return agent.AgentTurn{}, ErrTurnNotActive
	}
	if turn.Status.IsTerminal() {
		return turn, nil
	}
	if turn.Status == agent.TurnQueued && turn.Dispatch == agent.DispatchQueued {
		s.mutex.Lock()
		delete(s.queued, turn.ID)
		s.mutex.Unlock()
		return s.finish(ctx, turn, agent.TurnInterrupted, nil)
	}
	if turn.CodexTurnID == "" {
		return s.finish(ctx, turn, agent.TurnInterrupted, nil)
	}
	session, err := s.store.AgentSession(ctx, sessionID)
	if err != nil {
		return agent.AgentTurn{}, err
	}
	if err := s.runtime.InterruptTurn(ctx, session.CodexThreadID, turn.CodexTurnID); err != nil {
		return turn, fmt.Errorf("interrupt Agent Turn: %w", err)
	}
	return turn, nil
}

func (s *TurnScheduler) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *TurnScheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run Agent Turn scheduler: context is required")
	}
	if err := s.Recover(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			s.waitGroup.Wait()
			return s.interruptUndispatched()
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// Recover performs the one startup sweep. Plain queued inputs are interrupted;
// dispatched Turns are queried by native ID and never replayed.
func (s *TurnScheduler) Recover(ctx context.Context) error {
	if ctx == nil {
		return errors.New("recover Agent Turns: context is required")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.recovered {
		return nil
	}
	turns, err := s.store.AgentTurnsForRecovery(ctx, maximumRecoveryTurns)
	if err != nil {
		return fmt.Errorf("recover Agent Turns: %w", err)
	}
	for _, turn := range turns {
		if _, retained := s.queued[turn.ID]; retained && turn.Status == agent.TurnQueued && turn.Dispatch == agent.DispatchQueued {
			continue
		}
		if turn.Dispatch == agent.DispatchQueued || turn.CodexTurnID == "" {
			if _, err := s.finish(ctx, turn, agent.TurnInterrupted, nil); err != nil {
				return fmt.Errorf("recover queued Agent Turn: %w", err)
			}
			continue
		}
		if err := s.reconcile(ctx, turn); err != nil {
			return err
		}
	}
	s.recovered = true
	return nil
}

// ProcessNext dispatches one in-memory queued input synchronously and is useful
// for deterministic integration tests.
func (s *TurnScheduler) ProcessNext(ctx context.Context) (bool, error) {
	if err := s.Recover(ctx); err != nil {
		return false, err
	}
	turns, err := s.store.QueuedAgentTurns(ctx, 1)
	if err != nil || len(turns) == 0 {
		return false, err
	}
	s.mutex.Lock()
	if len(s.active) >= agent.MaxGlobalActiveTurns {
		s.mutex.Unlock()
		return false, agent.ErrDispatchCapacityReached
	}
	s.active[turns[0].ID] = struct{}{}
	s.mutex.Unlock()
	err = s.process(ctx, turns[0])
	s.release(turns[0].ID)
	return true, err
}

func (s *TurnScheduler) dispatch(ctx context.Context) {
	turns, err := s.store.QueuedAgentTurns(ctx, agent.MaxGlobalQueuedTurns)
	if err != nil {
		return
	}
	for _, turn := range turns {
		s.mutex.Lock()
		if len(s.active) >= agent.MaxGlobalActiveTurns {
			s.mutex.Unlock()
			return
		}
		if _, exists := s.active[turn.ID]; exists {
			s.mutex.Unlock()
			continue
		}
		s.active[turn.ID] = struct{}{}
		s.waitGroup.Add(1)
		s.mutex.Unlock()
		go func(turn agent.AgentTurn) {
			defer s.waitGroup.Done()
			defer s.release(turn.ID)
			_ = s.process(ctx, turn)
		}(turn)
	}
}

func (s *TurnScheduler) process(ctx context.Context, turn agent.AgentTurn) error {
	s.mutex.Lock()
	input, available := s.queued[turn.ID]
	delete(s.queued, turn.ID)
	s.mutex.Unlock()
	if !available {
		_, err := s.finish(context.WithoutCancel(ctx), turn, agent.TurnInterrupted, nil)
		if err != nil {
			return err
		}
		return ErrQueuedInputUnavailable
	}
	at, err := s.now()
	if err != nil {
		return err
	}
	dispatching, err := turn.Transition(agent.TurnQueued, agent.Dispatching, at, nil)
	if err != nil {
		return err
	}
	if err := s.store.SaveAgentTurn(ctx, dispatching, turn.Revision); err != nil {
		return err
	}
	turn = dispatching
	session, err := s.store.AgentSession(ctx, turn.AgentSessionID)
	if err != nil || session.Status != agent.SessionActive {
		return s.fail(ctx, turn, "session_unavailable", errors.Join(err, ErrSessionNotActive))
	}
	if err := s.prepareRuntimeSession(ctx, session); err != nil {
		return s.fail(ctx, turn, "runtime_session_unavailable", err)
	}

	subscriptionContext, cancelSubscription := context.WithCancel(ctx)
	defer cancelSubscription()
	events, err := s.runtime.Subscribe(subscriptionContext, session.CodexThreadID)
	if err != nil {
		return s.fail(ctx, turn, "runtime_subscription_failed", err)
	}
	if err := s.mcp.PrepareTurn(session.ID, turn.ID); err != nil {
		return s.fail(ctx, turn, "mcp_turn_activation_failed", err)
	}
	defer func() {
		_ = s.mcp.EndTurn(context.WithoutCancel(ctx), session.ID, turn.ID)
	}()
	runtimeTurn, err := s.runtime.StartTurn(ctx, agentapp.TurnRequest{CodexThreadID: session.CodexThreadID, ModelID: turn.ModelID, Text: videoPromptInputText(input.text, input.videoPromptTarget), ImagePaths: input.imagePaths, Focus: runtimeFocus(input.focus)})
	if err != nil {
		return s.fail(ctx, turn, "runtime_start_failed", err)
	}
	bound, err := turn.BindRuntimeTurn(runtimeTurn.CodexTurnID)
	if err != nil {
		s.bestEffortInterrupt(session.CodexThreadID, runtimeTurn.CodexTurnID)
		return s.fail(ctx, turn, "runtime_turn_invalid", err)
	}
	if err := s.store.SaveAgentTurn(ctx, bound, turn.Revision); err != nil {
		s.bestEffortInterrupt(session.CodexThreadID, runtimeTurn.CodexTurnID)
		return err
	}
	turn = bound
	if err := s.mcp.ActivateTurn(session.ID, agentmcp.ActiveTurn{AgentTurnID: turn.ID, CodexThreadID: session.CodexThreadID, CodexTurnID: turn.CodexTurnID, RuntimeKind: agentapp.RuntimeKind(session.RuntimeKind), ArtifactIDs: append([]project.ArtifactID(nil), input.artifactIDs...), Focus: turn.Focus, VideoPromptTarget: copyVideoPromptContext(input.videoPromptTarget)}); err != nil {
		s.bestEffortInterrupt(session.CodexThreadID, turn.CodexTurnID)
		return s.fail(ctx, turn, "mcp_turn_activation_failed", err)
	}
	at, err = s.now()
	if err != nil {
		return err
	}
	running, err := turn.Transition(agent.TurnRunning, agent.DispatchInflight, at, nil)
	if err != nil {
		return s.fail(ctx, turn, "turn_state_invalid", err)
	}
	if err := s.store.SaveAgentTurn(ctx, running, turn.Revision); err != nil {
		s.bestEffortInterrupt(session.CodexThreadID, turn.CodexTurnID)
		return err
	}
	turn = running
	s.publish(agentapp.StudioEvent{Type: agentapp.EventTurnStarted, SessionID: turn.AgentSessionID, TurnID: turn.ID, OccurredAt: at})
	for {
		select {
		case <-ctx.Done():
			s.bestEffortInterrupt(session.CodexThreadID, turn.CodexTurnID)
			_, finishErr := s.finish(context.Background(), turn, agent.TurnInterrupted, nil)
			return finishErr
		case event, ok := <-events:
			if !ok {
				return s.reconcile(context.WithoutCancel(ctx), turn)
			}
			if event.CodexThreadID != session.CodexThreadID || (event.CodexTurnID != "" && event.CodexTurnID != turn.CodexTurnID) {
				continue
			}
			switch event.Type {
			case agentapp.RuntimeTurnRetrying:
				// Retry progress must identify this exact native Turn. It does not
				// change durable status, release the MCP binding, or resend work.
				if event.CodexTurnID != turn.CodexTurnID {
					continue
				}
				s.publish(agentapp.StudioEvent{Type: agentapp.EventTurnRetrying, SessionID: turn.AgentSessionID, TurnID: turn.ID, FailureCode: agentapp.RuntimeRetryingFailureCode, OccurredAt: s.eventTime(event.OccurredAt)})
			case agentapp.RuntimeWaitingUser:
				turn, err = s.changeActiveState(ctx, turn, agent.TurnWaitingUser)
				if err != nil {
					return err
				}
				s.publish(agentapp.StudioEvent{Type: agentapp.EventTurnWaitingUser, SessionID: turn.AgentSessionID, TurnID: turn.ID, OccurredAt: s.eventTime(event.OccurredAt)})
			case agentapp.RuntimeTurnStarted:
				if turn.Status == agent.TurnWaitingUser {
					turn, err = s.changeActiveState(ctx, turn, agent.TurnRunning)
					if err != nil {
						return err
					}
				}
			case agentapp.RuntimeAssistantDelta:
				s.publish(agentapp.StudioEvent{Type: agentapp.EventAssistantDelta, SessionID: turn.AgentSessionID, TurnID: turn.ID, TextDelta: event.TextDelta, OccurredAt: s.eventTime(event.OccurredAt)})
			case agentapp.RuntimeToolStarted:
				s.publish(agentapp.StudioEvent{Type: agentapp.EventToolStarted, SessionID: turn.AgentSessionID, TurnID: turn.ID, ToolName: event.ToolName, OccurredAt: s.eventTime(event.OccurredAt)})
				if !allowedRuntimeTool(event.ToolName, event.SandboxedExecution) {
					s.bestEffortInterrupt(session.CodexThreadID, turn.CodexTurnID)
					return s.fail(ctx, turn, "runtime_configuration_error", errors.New("Runtime attempted a prohibited tool"))
				}
			case agentapp.RuntimeToolCompleted:
				s.publish(agentapp.StudioEvent{Type: agentapp.EventToolCompleted, SessionID: turn.AgentSessionID, TurnID: turn.ID, ToolName: event.ToolName, Summary: event.FailureSummary, OccurredAt: s.eventTime(event.OccurredAt)})
			case agentapp.RuntimeImageGenerated:
				if s.media == nil {
					return s.fail(ctx, turn, "generated_media_import_failed", errors.New("Agent media importer is unavailable"))
				}
				if _, err := s.media.ImportAgentGeneratedMedia(ctx, projectapp.ImportAgentGeneratedMediaInput{
					ProjectID: session.ProjectID, EpisodeID: session.EpisodeID, SessionID: session.ID.String(), TurnID: turn.ID.String(), SourcePath: event.GeneratedImagePath,
				}); err != nil {
					return s.fail(ctx, turn, "generated_media_import_failed", err)
				}
			case agentapp.RuntimeConnectionFailure:
				return s.reconcile(context.WithoutCancel(ctx), turn)
			case agentapp.RuntimeTurnCompleted:
				status := event.TurnStatus
				if !status.IsTerminal() {
					status = agent.TurnFailed
				}
				var failure *agent.Failure
				if status == agent.TurnFailed {
					failure = safeFailure(event.FailureCode, event.FailureSummary, "codex_turn_failed", "Codex Turn failed")
				}
				_, err := s.finish(ctx, turn, status, failure)
				return err
			}
		}
	}
}

func (s *TurnScheduler) prepareRuntimeSession(ctx context.Context, session agent.AgentSession) error {
	binding, err := s.store.AgentMCPBinding(ctx, session.ID)
	if err != nil {
		return err
	}
	scope := agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: session.ID, ProjectID: session.ProjectID, EpisodeID: session.EpisodeID}
	if binding.Status == agent.MCPBindingReady && s.mcp.BindingRegistered(scope) {
		return nil
	}
	if binding.Status != agent.MCPBindingPreparing {
		at, timeErr := s.now()
		if timeErr != nil {
			return timeErr
		}
		preparing, transitionErr := binding.Transition(agent.MCPBindingPreparing, at, nil)
		if transitionErr != nil {
			return transitionErr
		}
		if err := s.store.SaveAgentMCPBinding(ctx, preparing, binding.Revision); err != nil {
			return err
		}
		binding = preparing
	}
	endpoint, err := s.mcp.EnsureBinding(ctx, scope)
	if err != nil {
		s.markBindingFailed(context.WithoutCancel(ctx), binding, err)
		return err
	}
	request := agentapp.ThreadRequest{RuntimeKind: agentapp.RuntimeKind(session.RuntimeKind), AgentSessionID: session.ID, ProjectID: session.ProjectID, EpisodeID: session.EpisodeID, ModelID: session.ModelID, WorkspacePath: binding.WorkspacePath, MCPBindingURL: endpoint, WebSearchDisabled: session.WebSearchDisabled}
	thread, err := s.runtime.ResumeThread(ctx, session.CodexThreadID, request)
	if err != nil || thread.CodexThreadID != session.CodexThreadID || thread.CodexSessionID != session.CodexSessionID {
		cause := err
		if cause == nil {
			cause = errors.New("Runtime resumed a different Session or Thread")
		}
		s.markBindingFailed(context.WithoutCancel(ctx), binding, cause)
		return cause
	}
	at, err := s.now()
	if err != nil {
		return err
	}
	ready, err := binding.Transition(agent.MCPBindingReady, at, nil)
	if err != nil {
		return err
	}
	if err := s.store.SaveAgentMCPBinding(ctx, ready, binding.Revision); err != nil {
		return err
	}
	return nil
}

func (s *TurnScheduler) markBindingFailed(ctx context.Context, binding agent.MCPBinding, _ error) {
	at, err := s.now()
	if err != nil {
		return
	}
	failure := safeFailure("mcp_binding_failed", "ShotForward MCP binding verification failed", "mcp_binding_failed", "ShotForward MCP binding failed")
	failed, err := binding.Transition(agent.MCPBindingFailed, at, failure)
	if err == nil {
		_ = s.store.SaveAgentMCPBinding(ctx, failed, binding.Revision)
	}
}

func (s *TurnScheduler) reconcile(ctx context.Context, turn agent.AgentTurn) error {
	current, err := s.store.AgentTurn(ctx, turn.ID)
	if err != nil || current.Status.IsTerminal() {
		return err
	}
	turn = current
	if turn.Dispatch != agent.DispatchReconciling {
		at, timeErr := s.now()
		if timeErr != nil {
			return timeErr
		}
		status := turn.Status
		if status == agent.TurnQueued {
			status = agent.TurnRunning
		}
		next, transitionErr := turn.Transition(status, agent.DispatchReconciling, at, nil)
		if transitionErr != nil {
			return transitionErr
		}
		if err := s.store.SaveAgentTurn(ctx, next, turn.Revision); err != nil {
			return err
		}
		turn = next
	}
	_ = s.mcp.EndTurn(ctx, turn.AgentSessionID, turn.ID)
	session, err := s.store.AgentSession(ctx, turn.AgentSessionID)
	if err != nil {
		return s.fail(ctx, turn, "reconciliation_failed", err)
	}
	thread, err := s.runtime.ReadThread(ctx, session.CodexThreadID)
	if err != nil {
		return s.fail(ctx, turn, "reconciliation_failed", err)
	}
	if thread.CodexThreadID != session.CodexThreadID || thread.CodexSessionID != session.CodexSessionID {
		return s.fail(ctx, turn, "reconciliation_failed", errors.New("Runtime history scope mismatch"))
	}
	for _, persisted := range thread.Turns {
		if persisted.CodexTurnID != turn.CodexTurnID {
			continue
		}
		if persisted.Status.IsTerminal() {
			var failure *agent.Failure
			if persisted.Status == agent.TurnFailed {
				failure = safeFailure("codex_turn_failed", "Codex recorded the Turn as failed", "codex_turn_failed", "Codex Turn failed")
			}
			_, finishErr := s.finish(ctx, turn, persisted.Status, failure)
			return finishErr
		}
		return s.fail(ctx, turn, "reconciliation_inconclusive", errors.New("Codex Turn was still active after Runtime interruption"))
	}
	return s.fail(ctx, turn, "reconciliation_inconclusive", errors.New("Codex Turn was not present in persisted Thread history"))
}

func (s *TurnScheduler) changeActiveState(ctx context.Context, turn agent.AgentTurn, status agent.TurnStatus) (agent.AgentTurn, error) {
	at, err := s.now()
	if err != nil {
		return agent.AgentTurn{}, err
	}
	next, err := turn.Transition(status, agent.DispatchInflight, at, nil)
	if err != nil {
		return agent.AgentTurn{}, err
	}
	if err := s.store.SaveAgentTurn(ctx, next, turn.Revision); err != nil {
		return agent.AgentTurn{}, err
	}
	return next, nil
}

func (s *TurnScheduler) fail(ctx context.Context, turn agent.AgentTurn, code string, cause error) error {
	var business interface{ GenerationFailureCode() string }
	if errors.As(cause, &business) && generation.IsCloudBusinessFailure(business.GenerationFailureCode()) {
		code = business.GenerationFailureCode()
	}
	failure := safeFailure(code, schedulerFailureSummary(code), code, "Agent Turn failed")
	s.publish(agentapp.StudioEvent{Type: agentapp.EventRuntimeError, SessionID: turn.AgentSessionID, TurnID: turn.ID, FailureCode: failure.Code, Summary: failure.Summary, OccurredAt: s.eventTime(time.Time{})})
	_, err := s.finish(context.WithoutCancel(ctx), turn, agent.TurnFailed, failure)
	return err
}

func (s *TurnScheduler) finish(ctx context.Context, turn agent.AgentTurn, status agent.TurnStatus, failure *agent.Failure) (agent.AgentTurn, error) {
	current, err := s.store.AgentTurn(ctx, turn.ID)
	if err != nil {
		return agent.AgentTurn{}, err
	}
	cleanupErr := s.mcp.EndTurn(ctx, current.AgentSessionID, current.ID)
	if errors.Is(cleanupErr, agentmcp.ErrTurnNotActive) || errors.Is(cleanupErr, agentmcp.ErrBindingNotFound) {
		cleanupErr = nil
	}
	session, sessionErr := s.store.AgentSession(ctx, current.AgentSessionID)
	var draftCleanupErr error
	if sessionErr == nil {
		draftCleanupErr = s.mcp.CleanupTurn(ctx, session.ProjectID, current.ID)
	}
	if cleanupErr := errors.Join(cleanupErr, sessionErr, draftCleanupErr); cleanupErr != nil {
		return current, cleanupErr
	}
	if current.Status.IsTerminal() {
		return current, nil
	}
	at, err := s.now()
	if err != nil {
		return agent.AgentTurn{}, err
	}
	next, err := current.Transition(status, agent.DispatchTerminal, at, failure)
	if err != nil {
		return agent.AgentTurn{}, err
	}
	if err := s.store.SaveAgentTurn(ctx, next, current.Revision); err != nil {
		return agent.AgentTurn{}, err
	}
	s.publish(agentapp.StudioEvent{Type: agentapp.EventTurnCompleted, SessionID: next.AgentSessionID, TurnID: next.ID, TurnStatus: next.Status, FailureCode: failureCode(next.Failure), Summary: failureSummary(next.Failure), OccurredAt: at})
	return next, nil
}

func (s *TurnScheduler) interruptUndispatched() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	turns, err := s.store.AgentTurnsForRecovery(ctx, maximumRecoveryTurns)
	if err != nil {
		return err
	}
	var result []error
	for _, turn := range turns {
		if turn.Status == agent.TurnQueued {
			_, finishErr := s.finish(ctx, turn, agent.TurnInterrupted, nil)
			result = append(result, finishErr)
		}
	}
	return errors.Join(result...)
}

func (s *TurnScheduler) release(turnID agent.TurnID) {
	s.mutex.Lock()
	delete(s.active, turnID)
	s.mutex.Unlock()
	s.Notify()
}

func (s *TurnScheduler) bestEffortInterrupt(threadID, turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.runtime.InterruptTurn(ctx, threadID, turnID)
}

func (s *TurnScheduler) now() (time.Time, error) {
	value := s.clock.Now()
	if value.IsZero() || value.Location() != time.UTC || value.Nanosecond()%int(time.Millisecond) != 0 || value.UnixMilli() < 0 {
		return time.Time{}, errors.New("Agent Turn scheduler Clock returned an invalid timestamp")
	}
	return value, nil
}

func (s *TurnScheduler) eventTime(candidate time.Time) time.Time {
	if !candidate.IsZero() {
		candidate = candidate.UTC().Truncate(time.Millisecond)
		if candidate.UnixMilli() >= 0 {
			return candidate
		}
	}
	value, err := s.now()
	if err != nil {
		return time.UnixMilli(0).UTC()
	}
	return value
}

func (s *TurnScheduler) publish(event agentapp.StudioEvent) {
	s.events.Publish(event)
}

func failureCode(value *agent.Failure) string {
	if value == nil {
		return ""
	}
	return value.Code
}

func failureSummary(value *agent.Failure) string {
	if value == nil {
		return ""
	}
	return value.Summary
}

func runtimeFocus(value agent.FocusSnapshot) agentapp.FocusSnapshot {
	return agentapp.FocusSnapshot{SceneID: value.SceneID, ClipID: value.ClipID, AssetID: value.AssetID}
}

func allowedRuntimeTool(name string, sandboxedExecution bool) bool {
	if name == "web_search" {
		return true
	}
	if name == "shell" {
		return sandboxedExecution
	}
	const prefix = "shotforward/"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	requested := strings.TrimPrefix(name, prefix)
	for _, allowed := range agentapp.ShotForwardMCPToolNames() {
		if requested == allowed {
			return true
		}
	}
	return false
}

func inputFingerprint(input SubmitInput) string {
	hash := sha256.New()
	values := []string{input.ModelID, input.Text, input.Focus.SceneID.String(), input.Focus.ClipID.String(), input.Focus.AssetID.String()}
	if input.VideoPromptTarget != nil {
		encoded, _ := json.Marshal(input.VideoPromptTarget)
		values = append(values, "video_prompt_target", string(encoded))
	}
	for _, artifactID := range input.ArtifactIDs {
		values = append(values, artifactID.String())
	}
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func validVideoPromptContext(target *agentmcp.VideoPromptContext, focus agent.FocusSnapshot) bool {
	return target.Validate() == nil && (target == nil || target.Focus == nil || target.Focus.ClipID == focus.ClipID)
}

func copyVideoPromptContext(target *agentmcp.VideoPromptContext) *agentmcp.VideoPromptContext {
	if target == nil {
		return nil
	}
	copy := *target
	if target.Focus != nil {
		focus := *target.Focus
		copy.Focus = &focus
	}
	return &copy
}

func videoPromptInputText(text string, target *agentmcp.VideoPromptContext) string {
	if guidance := target.InstructionText(); guidance != "" {
		return guidance + "\n\n" + text
	}
	return text
}

func validInputImages(paths []string, artifactIDs []project.ArtifactID) bool {
	if len(paths) != len(artifactIDs) || len(paths) > 5 {
		return false
	}
	seen := make(map[project.ArtifactID]struct{}, len(artifactIDs))
	for index, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || artifactIDs[index].Validate() != nil {
			return false
		}
		if _, exists := seen[artifactIDs[index]]; exists {
			return false
		}
		seen[artifactIDs[index]] = struct{}{}
	}
	return true
}

func validAgentInputText(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && len(value) <= MaxAgentInputBytes && strings.TrimSpace(value) != ""
}

func validOneLine(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len([]rune(value)) <= maximum && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func safeFailure(code, summary, fallbackCode, fallbackSummary string) *agent.Failure {
	code = strings.TrimSpace(code)
	if !validOneLine(code, 100) {
		code = fallbackCode
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = fallbackSummary
	}
	if !utf8.ValidString(summary) {
		summary = fallbackSummary
	}
	runes := []rune(strings.ReplaceAll(summary, "\x00", ""))
	if len(runes) > 2_000 {
		runes = runes[:2_000]
	}
	if len(runes) == 0 {
		runes = []rune(fallbackSummary)
	}
	return &agent.Failure{Code: code, Summary: string(runes)}
}

func schedulerFailureSummary(code string) string {
	switch code {
	case "session_unavailable":
		return "Agent Session is unavailable"
	case "runtime_session_unavailable":
		return "Codex Thread or ShotForward MCP binding is unavailable"
	case "runtime_subscription_failed":
		return "Codex event subscription failed"
	case "runtime_start_failed":
		return "Codex Turn could not start"
	case "runtime_turn_invalid":
		return "Codex returned an invalid Turn"
	case "mcp_turn_activation_failed":
		return "ShotForward MCP could not bind the active Turn"
	case "turn_state_invalid":
		return "Agent Turn state could not be updated"
	case "runtime_configuration_error":
		return "Codex attempted a prohibited capability"
	case "reconciliation_failed":
		return "Codex Turn reconciliation failed"
	case "reconciliation_inconclusive":
		return "Codex Turn completion could not be proven after interruption"
	default:
		return "Agent Turn failed"
	}
}

func mapAdmissionError(err error) error {
	switch {
	case errors.Is(err, agent.ErrSessionQueueFull), errors.Is(err, agent.ErrGlobalQueueFull):
		return err
	case errors.Is(err, agent.ErrInputIdempotencyConflict):
		return ErrInputIdempotencyConflict
	case errors.Is(err, agent.ErrSessionNotActive):
		return ErrSessionNotActive
	default:
		return err
	}
}
