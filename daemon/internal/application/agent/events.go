package agentapp

import (
	"strings"
	"time"
	"unicode/utf8"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
)

// StudioEventType is the stable transport-facing Agent event union. It never
// exposes Codex protocol identifiers, tool arguments, tool results, or RPC
// envelopes.
type StudioEventType string

const (
	EventTurnQueued            StudioEventType = "turn.queued"
	EventTurnStarted           StudioEventType = "turn.started"
	EventTurnRetrying          StudioEventType = "turn.retrying"
	EventTurnWaitingUser       StudioEventType = "turn.waiting_user"
	EventAssistantDelta        StudioEventType = "assistant.delta"
	EventToolStarted           StudioEventType = "tool.started"
	EventToolCompleted         StudioEventType = "tool.completed"
	EventChangeSetPending      StudioEventType = "change_set.pending"
	EventChangeSetApplied      StudioEventType = "change_set.applied"
	EventTurnCompleted         StudioEventType = "turn.completed"
	EventRuntimeError          StudioEventType = "runtime.error"
	EventSessionResyncRequired StudioEventType = "session.resync_required"
)

type StudioEvent struct {
	Type        StudioEventType
	SessionID   agent.SessionID
	TurnID      agent.TurnID
	ChangeSetID changeset.ChangeSetID
	TextDelta   string
	ToolName    string
	TurnStatus  agent.TurnStatus
	FailureCode string
	Summary     string
	OccurredAt  time.Time
}

func (e StudioEvent) Validate() error {
	if e.SessionID.Validate() != nil || !validEventTime(e.OccurredAt) || !validStudioEventType(e.Type) {
		return ErrInvalidRuntimeValue
	}
	if e.TurnID != "" && e.TurnID.Validate() != nil || e.ChangeSetID != "" && e.ChangeSetID.Validate() != nil {
		return ErrInvalidRuntimeValue
	}
	if !utf8.ValidString(e.TextDelta) || strings.ContainsRune(e.TextDelta, 0) || len(e.TextDelta) > 64*1024 ||
		!validRuntimeText(e.ToolName, 100, false) || !validRuntimeText(e.FailureCode, 100, false) ||
		!validRuntimeSummary(e.Summary, 2_000) {
		return ErrInvalidRuntimeValue
	}
	switch e.Type {
	case EventTurnQueued, EventTurnStarted, EventTurnWaitingUser:
		if e.TurnID == "" {
			return ErrInvalidRuntimeValue
		}
	case EventTurnRetrying:
		if e.TurnID == "" || e.FailureCode != RuntimeRetryingFailureCode || e.TurnStatus != "" ||
			e.Summary != "" || e.TextDelta != "" || e.ToolName != "" || e.ChangeSetID != "" {
			return ErrInvalidRuntimeValue
		}
	case EventAssistantDelta:
		if e.TurnID == "" || e.TextDelta == "" {
			return ErrInvalidRuntimeValue
		}
	case EventToolStarted, EventToolCompleted:
		if e.TurnID == "" || e.ToolName == "" {
			return ErrInvalidRuntimeValue
		}
	case EventChangeSetPending, EventChangeSetApplied:
		if e.TurnID == "" || e.ChangeSetID == "" {
			return ErrInvalidRuntimeValue
		}
	case EventTurnCompleted:
		if e.TurnID == "" || !e.TurnStatus.IsTerminal() {
			return ErrInvalidRuntimeValue
		}
	case EventRuntimeError:
		if e.FailureCode == "" || e.Summary == "" {
			return ErrInvalidRuntimeValue
		}
	case EventSessionResyncRequired:
		if e.Summary == "" {
			return ErrInvalidRuntimeValue
		}
	}
	return nil
}

type EventPublisher interface {
	Publish(StudioEvent)
}

type DiscardEvents struct{}

func (DiscardEvents) Publish(StudioEvent) {}

func validStudioEventType(value StudioEventType) bool {
	switch value {
	case EventTurnQueued, EventTurnStarted, EventTurnRetrying, EventTurnWaitingUser, EventAssistantDelta,
		EventToolStarted, EventToolCompleted, EventChangeSetPending, EventChangeSetApplied, EventTurnCompleted,
		EventRuntimeError, EventSessionResyncRequired:
		return true
	default:
		return false
	}
}

func validEventTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 && value.Nanosecond()%int(time.Millisecond) == 0
}
