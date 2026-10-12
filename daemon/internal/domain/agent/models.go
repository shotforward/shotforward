package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrInvalidSession          = errors.New("invalid Agent Session")
	ErrInvalidSessionState     = errors.New("invalid Agent Session state transition")
	ErrInvalidTurn             = errors.New("invalid Agent Turn")
	ErrInvalidTurnState        = errors.New("invalid Agent Turn state transition")
	ErrRuntimeTurnAlreadyBound = errors.New("Agent Turn already has a Runtime Turn")
	ErrInvalidMCPBinding       = errors.New("invalid Agent MCP Binding")
	ErrInvalidMCPBindingState  = errors.New("invalid Agent MCP Binding state transition")
	ErrInvalidInputOperation   = errors.New("invalid Agent input operation")
)

type SessionStatus string

const (
	SessionActive             SessionStatus = "active"
	SessionHistoryUnavailable SessionStatus = "history_unavailable"
	SessionArchived           SessionStatus = "archived"
)

type AgentSession struct {
	ID                SessionID
	ProjectID         project.ProjectID
	EpisodeID         project.EpisodeID
	CodexThreadID     string
	CodexSessionID    string
	ModelID           string
	RuntimeKind       string
	CachedTitle       string
	WebSearchDisabled bool
	Status            SessionStatus
	Revision          int64
	ArchivedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type InputAction string

const (
	InputQueueTurn InputAction = "queue_turn"
	InputSteerTurn InputAction = "steer_turn"
)

type InputOperation struct {
	AgentSessionID     SessionID
	ClientOperationID  string
	RequestFingerprint string
	InputText          string
	Action             InputAction
	AgentTurnID        TurnID
	CreatedAt          time.Time
}

func (o InputOperation) Validate() error {
	if o.AgentSessionID.Validate() != nil || o.AgentTurnID.Validate() != nil ||
		!validText(o.ClientOperationID, 200, true) ||
		!validSHA256(o.RequestFingerprint) ||
		(o.InputText != "" && (!utf8.ValidString(o.InputText) || len(o.InputText) > 128*1024 || strings.ContainsRune(o.InputText, 0))) ||
		(o.Action != InputQueueTurn && o.Action != InputSteerTurn) ||
		!validTime(o.CreatedAt) {
		return ErrInvalidInputOperation
	}
	return nil
}

func (s AgentSession) Validate() error {
	if s.ID.Validate() != nil || s.ProjectID.Validate() != nil || s.EpisodeID.Validate() != nil {
		return ErrInvalidSession
	}
	if !validText(s.CodexThreadID, 500, true) ||
		!validText(s.CodexSessionID, 500, true) ||
		!validText(s.ModelID, 200, true) || (s.RuntimeKind != "" && s.RuntimeKind != "user_codex" && s.RuntimeKind != "shotforward") ||
		!validText(s.CachedTitle, 300, false) {
		return ErrInvalidSession
	}
	if s.Status != SessionActive && s.Status != SessionHistoryUnavailable && s.Status != SessionArchived {
		return ErrInvalidSession
	}
	if s.Revision < 1 || !validTime(s.CreatedAt) || !validTime(s.UpdatedAt) || s.UpdatedAt.Before(s.CreatedAt) {
		return ErrInvalidSession
	}
	if (s.Status == SessionArchived) != (s.ArchivedAt != nil) {
		return ErrInvalidSession
	}
	if s.ArchivedAt != nil && (!validTime(*s.ArchivedAt) || s.ArchivedAt.Before(s.CreatedAt) || !s.ArchivedAt.Equal(s.UpdatedAt)) {
		return ErrInvalidSession
	}
	return nil
}

func (s AgentSession) ValidateNew() error {
	if s.Validate() != nil || s.Status != SessionActive || s.Revision != 1 ||
		s.ArchivedAt != nil || !s.CreatedAt.Equal(s.UpdatedAt) {
		return ErrInvalidSession
	}
	return nil
}

func (s AgentSession) Transition(next SessionStatus, at time.Time) (AgentSession, error) {
	if err := s.Validate(); err != nil || !validTime(at) || at.Before(s.UpdatedAt) {
		return AgentSession{}, ErrInvalidSession
	}
	allowed := (s.Status == SessionActive && (next == SessionHistoryUnavailable || next == SessionArchived)) ||
		(s.Status == SessionHistoryUnavailable && (next == SessionActive || next == SessionArchived))
	if !allowed {
		return AgentSession{}, ErrInvalidSessionState
	}
	s.Status = next
	s.UpdatedAt = at
	s.Revision++
	s.ArchivedAt = nil
	if next == SessionArchived {
		archivedAt := at
		s.ArchivedAt = &archivedAt
	}
	return s, s.Validate()
}

// ValidateReplacement proves that next is one legal optimistic update of s.
// Persistence adapters use it to prevent callers from constructing a valid
// snapshot that bypasses the Session state machine.
func (s AgentSession) ValidateReplacement(next AgentSession) error {
	if s.Validate() != nil || next.Validate() != nil || next.Revision != s.Revision+1 ||
		s.ID != next.ID || s.ProjectID != next.ProjectID || s.EpisodeID != next.EpisodeID ||
		s.CodexThreadID != next.CodexThreadID || s.CodexSessionID != next.CodexSessionID ||
		s.ModelID != next.ModelID || normalizedRuntimeKind(s.RuntimeKind) != normalizedRuntimeKind(next.RuntimeKind) || s.WebSearchDisabled != next.WebSearchDisabled ||
		!s.CreatedAt.Equal(next.CreatedAt) || next.UpdatedAt.Before(s.UpdatedAt) {
		return ErrInvalidSession
	}
	if s.Status == next.Status {
		if s.Status == SessionArchived || s.CachedTitle == next.CachedTitle {
			return ErrInvalidSessionState
		}
		return nil
	}
	allowed := (s.Status == SessionActive && (next.Status == SessionHistoryUnavailable || next.Status == SessionArchived)) ||
		(s.Status == SessionHistoryUnavailable && (next.Status == SessionActive || next.Status == SessionArchived))
	if !allowed {
		return ErrInvalidSessionState
	}
	return nil
}

func normalizedRuntimeKind(value string) string {
	if value == "" {
		return "user_codex"
	}
	return value
}

type TurnStatus string

const (
	TurnQueued      TurnStatus = "queued"
	TurnRunning     TurnStatus = "running"
	TurnWaitingUser TurnStatus = "waiting_user"
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	TurnFailed      TurnStatus = "failed"
)

func (s TurnStatus) IsTerminal() bool {
	return s == TurnCompleted || s == TurnInterrupted || s == TurnFailed
}

type DispatchState string

const (
	DispatchQueued      DispatchState = "queued"
	Dispatching         DispatchState = "dispatching"
	DispatchInflight    DispatchState = "inflight"
	DispatchReconciling DispatchState = "reconciling"
	DispatchTerminal    DispatchState = "terminal"
)

type FocusSnapshot struct {
	SceneID project.SceneID
	ClipID  project.ClipID
	AssetID project.AssetID
}

func (f FocusSnapshot) Validate() error {
	if f.SceneID != "" && f.SceneID.Validate() != nil {
		return ErrInvalidTurn
	}
	if f.ClipID != "" && f.ClipID.Validate() != nil {
		return ErrInvalidTurn
	}
	if f.AssetID != "" && f.AssetID.Validate() != nil {
		return ErrInvalidTurn
	}
	return nil
}

type Failure struct {
	Code    string
	Summary string
}

func (f Failure) Validate() error {
	if !validText(f.Code, 100, true) || !validMultilineText(f.Summary, 2_000, true) {
		return ErrInvalidTurn
	}
	return nil
}

type AgentTurn struct {
	ID                TurnID
	AgentSessionID    SessionID
	ModelID           string
	CodexTurnID       string
	ClientOperationID string
	Focus             FocusSnapshot
	Status            TurnStatus
	Dispatch          DispatchState
	Failure           *Failure
	Revision          int64
	CreatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
}

func (t AgentTurn) Validate() error {
	if t.ID.Validate() != nil || t.AgentSessionID.Validate() != nil ||
		!validText(t.ModelID, 200, true) ||
		!validText(t.CodexTurnID, 500, false) ||
		!validText(t.ClientOperationID, 200, true) ||
		t.Focus.Validate() != nil || t.Revision < 1 || !validTime(t.CreatedAt) {
		return ErrInvalidTurn
	}
	if !validTurnStatus(t.Status) || !validDispatchState(t.Dispatch) || !validTurnStateCombination(t) {
		return ErrInvalidTurn
	}
	if t.StartedAt != nil && (!validTime(*t.StartedAt) || t.StartedAt.Before(t.CreatedAt)) {
		return ErrInvalidTurn
	}
	if t.CompletedAt != nil && (!validTime(*t.CompletedAt) || t.CompletedAt.Before(t.CreatedAt) || (t.StartedAt != nil && t.CompletedAt.Before(*t.StartedAt))) {
		return ErrInvalidTurn
	}
	if t.Status == TurnFailed {
		if t.Failure == nil || t.Failure.Validate() != nil {
			return ErrInvalidTurn
		}
	} else if t.Failure != nil {
		return ErrInvalidTurn
	}
	return nil
}

func (t AgentTurn) ValidateNew() error {
	if t.Validate() != nil || t.Status != TurnQueued || t.Dispatch != DispatchQueued ||
		t.CodexTurnID != "" || t.Failure != nil || t.Revision != 1 ||
		t.StartedAt != nil || t.CompletedAt != nil {
		return ErrInvalidTurn
	}
	return nil
}

func (t AgentTurn) BindRuntimeTurn(codexTurnID string) (AgentTurn, error) {
	if err := t.Validate(); err != nil || !validText(codexTurnID, 500, true) {
		return AgentTurn{}, ErrInvalidTurn
	}
	if t.CodexTurnID != "" {
		return AgentTurn{}, ErrRuntimeTurnAlreadyBound
	}
	if t.Status != TurnQueued || (t.Dispatch != Dispatching && t.Dispatch != DispatchQueued) {
		return AgentTurn{}, ErrInvalidTurnState
	}
	t.CodexTurnID = codexTurnID
	t.Revision++
	return t, t.Validate()
}

func (t AgentTurn) Transition(nextStatus TurnStatus, nextDispatch DispatchState, at time.Time, failure *Failure) (AgentTurn, error) {
	if err := t.Validate(); err != nil || !validTime(at) || at.Before(t.CreatedAt) {
		return AgentTurn{}, ErrInvalidTurn
	}
	if !allowedTurnTransition(t.Status, nextStatus) || !allowedDispatchTransition(t.Dispatch, nextDispatch) {
		return AgentTurn{}, ErrInvalidTurnState
	}
	t.Status = nextStatus
	t.Dispatch = nextDispatch
	t.Failure = failure
	if nextStatus == TurnRunning && t.StartedAt == nil {
		startedAt := at
		t.StartedAt = &startedAt
	}
	if nextStatus.IsTerminal() {
		completedAt := at
		t.CompletedAt = &completedAt
	}
	t.Revision++
	return t, t.Validate()
}

// ValidateReplacement proves that next was reachable from t without allowing
// a repository caller to skip the public state machine.
func (t AgentTurn) ValidateReplacement(next AgentTurn) error {
	if t.Validate() != nil || next.Validate() != nil || next.Revision != t.Revision+1 ||
		t.ID != next.ID || t.AgentSessionID != next.AgentSessionID ||
		t.ModelID != next.ModelID ||
		t.ClientOperationID != next.ClientOperationID || t.Focus != next.Focus ||
		!t.CreatedAt.Equal(next.CreatedAt) {
		return ErrInvalidTurn
	}
	if t.CodexTurnID != "" && t.CodexTurnID != next.CodexTurnID {
		return ErrInvalidTurnState
	}
	if t.CodexTurnID == "" && next.CodexTurnID != "" &&
		(t.Status != TurnQueued || next.Status != TurnQueued) {
		return ErrInvalidTurnState
	}
	if !allowedTurnTransition(t.Status, next.Status) || !allowedDispatchTransition(t.Dispatch, next.Dispatch) {
		return ErrInvalidTurnState
	}
	if t.StartedAt != nil && (next.StartedAt == nil || !t.StartedAt.Equal(*next.StartedAt)) {
		return ErrInvalidTurnState
	}
	if t.CompletedAt != nil || (t.Failure != nil && next.Failure != t.Failure) {
		return ErrInvalidTurnState
	}
	if t.Status == next.Status && t.Dispatch == next.Dispatch && t.CodexTurnID == next.CodexTurnID {
		return ErrInvalidTurnState
	}
	return nil
}

func validTurnStatus(value TurnStatus) bool {
	return value == TurnQueued || value == TurnRunning || value == TurnWaitingUser || value == TurnCompleted || value == TurnInterrupted || value == TurnFailed
}

func validDispatchState(value DispatchState) bool {
	return value == DispatchQueued || value == Dispatching || value == DispatchInflight || value == DispatchReconciling || value == DispatchTerminal
}

func validTurnStateCombination(turn AgentTurn) bool {
	switch turn.Status {
	case TurnQueued:
		return (turn.Dispatch == DispatchQueued || turn.Dispatch == Dispatching) && turn.StartedAt == nil && turn.CompletedAt == nil
	case TurnRunning, TurnWaitingUser:
		return (turn.Dispatch == DispatchInflight || turn.Dispatch == DispatchReconciling) && turn.StartedAt != nil && turn.CompletedAt == nil
	case TurnCompleted, TurnInterrupted, TurnFailed:
		return turn.Dispatch == DispatchTerminal && turn.CompletedAt != nil
	default:
		return false
	}
}

func allowedTurnTransition(current, next TurnStatus) bool {
	if current == next {
		return current == TurnQueued || current == TurnRunning || current == TurnWaitingUser
	}
	switch current {
	case TurnQueued:
		return next == TurnRunning || next == TurnInterrupted || next == TurnFailed
	case TurnRunning:
		return next == TurnWaitingUser || next == TurnCompleted || next == TurnInterrupted || next == TurnFailed
	case TurnWaitingUser:
		return next == TurnRunning || next == TurnInterrupted || next == TurnFailed
	default:
		return false
	}
}

func allowedDispatchTransition(current, next DispatchState) bool {
	if current == next {
		return current != DispatchTerminal
	}
	switch current {
	case DispatchQueued:
		return next == Dispatching || next == DispatchTerminal
	case Dispatching:
		return next == DispatchInflight || next == DispatchReconciling || next == DispatchTerminal
	case DispatchInflight:
		return next == DispatchReconciling || next == DispatchTerminal
	case DispatchReconciling:
		return next == DispatchInflight || next == DispatchTerminal
	default:
		return false
	}
}

type MCPBindingStatus string

const (
	MCPBindingPreparing MCPBindingStatus = "preparing"
	MCPBindingReady     MCPBindingStatus = "ready"
	MCPBindingFailed    MCPBindingStatus = "failed"
)

type MCPBinding struct {
	ID             MCPBindingID
	AgentSessionID SessionID
	WorkspacePath  string
	RouteKey       string
	Status         MCPBindingStatus
	Failure        *Failure
	Revision       int64
	LastVerifiedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (b MCPBinding) Validate() error {
	if b.ID.Validate() != nil || b.AgentSessionID.Validate() != nil ||
		strings.TrimSpace(b.WorkspacePath) == "" || !filepath.IsAbs(b.WorkspacePath) || filepath.Clean(b.WorkspacePath) != b.WorkspacePath ||
		!validText(b.RouteKey, 200, true) || b.Revision < 1 ||
		!validTime(b.CreatedAt) || !validTime(b.UpdatedAt) || b.UpdatedAt.Before(b.CreatedAt) {
		return ErrInvalidMCPBinding
	}
	if b.Status != MCPBindingPreparing && b.Status != MCPBindingReady && b.Status != MCPBindingFailed {
		return ErrInvalidMCPBinding
	}
	if b.Status == MCPBindingReady {
		if b.LastVerifiedAt == nil || !validTime(*b.LastVerifiedAt) || b.LastVerifiedAt.Before(b.CreatedAt) || b.Failure != nil {
			return ErrInvalidMCPBinding
		}
	} else if b.LastVerifiedAt != nil {
		return ErrInvalidMCPBinding
	}
	if b.Status == MCPBindingFailed {
		if b.Failure == nil || b.Failure.Validate() != nil {
			return ErrInvalidMCPBinding
		}
	} else if b.Failure != nil {
		return ErrInvalidMCPBinding
	}
	return nil
}

func (b MCPBinding) ValidateNew() error {
	if b.Validate() != nil || (b.Status != MCPBindingPreparing && b.Status != MCPBindingReady) ||
		b.Revision != 1 || !b.CreatedAt.Equal(b.UpdatedAt) {
		return ErrInvalidMCPBinding
	}
	return nil
}

func (b MCPBinding) Transition(next MCPBindingStatus, at time.Time, failure *Failure) (MCPBinding, error) {
	if err := b.Validate(); err != nil || !validTime(at) || at.Before(b.UpdatedAt) {
		return MCPBinding{}, ErrInvalidMCPBinding
	}
	allowed := (b.Status == MCPBindingPreparing && (next == MCPBindingReady || next == MCPBindingFailed)) ||
		((b.Status == MCPBindingReady || b.Status == MCPBindingFailed) && next == MCPBindingPreparing)
	if !allowed {
		return MCPBinding{}, ErrInvalidMCPBindingState
	}
	b.Status = next
	b.UpdatedAt = at
	b.Revision++
	b.Failure = failure
	b.LastVerifiedAt = nil
	if next == MCPBindingReady {
		verifiedAt := at
		b.LastVerifiedAt = &verifiedAt
	}
	return b, b.Validate()
}

func (b MCPBinding) ValidateReplacement(next MCPBinding) error {
	if b.Validate() != nil || next.Validate() != nil || next.Revision != b.Revision+1 ||
		b.ID != next.ID || b.AgentSessionID != next.AgentSessionID ||
		b.WorkspacePath != next.WorkspacePath || b.RouteKey != next.RouteKey ||
		!b.CreatedAt.Equal(next.CreatedAt) || next.UpdatedAt.Before(b.UpdatedAt) {
		return ErrInvalidMCPBinding
	}
	allowed := (b.Status == MCPBindingPreparing && (next.Status == MCPBindingReady || next.Status == MCPBindingFailed)) ||
		((b.Status == MCPBindingReady || b.Status == MCPBindingFailed) && next.Status == MCPBindingPreparing)
	if !allowed {
		return ErrInvalidMCPBindingState
	}
	return nil
}

func validText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00") && utf8.RuneCountInString(value) <= maximum && value == strings.TrimSpace(value) && (!required || value != "")
}

func validMultilineText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum && strings.TrimSpace(value) == value && (!required || value != "")
}

func validTime(value time.Time) bool {
	return !value.IsZero() && value.UnixMilli() >= 0 && value.Equal(time.UnixMilli(value.UnixMilli()))
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}
