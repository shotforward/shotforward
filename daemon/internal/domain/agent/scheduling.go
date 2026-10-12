package agent

import "errors"

const (
	MaxGlobalActiveTurns  = 5
	MaxGlobalQueuedTurns  = 50
	MaxSessionActiveTurns = 1
	MaxSessionQueuedTurns = 1
)

var (
	ErrInvalidSchedulingSnapshot = errors.New("invalid Agent scheduling snapshot")
	ErrSessionQueueFull          = errors.New("Agent Session queue is full")
	ErrGlobalQueueFull           = errors.New("global Agent queue is full")
	ErrDispatchCapacityReached   = errors.New("Agent Turn dispatch capacity reached")
	ErrInputIdempotencyConflict  = errors.New("Agent input idempotency conflict")
	ErrSessionNotActive          = errors.New("Agent Session is not active")
)

type AdmissionDecision string

const (
	AdmissionQueueTurn AdmissionDecision = "queue_turn"
	AdmissionSteerTurn AdmissionDecision = "steer_turn"
)

type SchedulingSnapshot struct {
	GlobalActive  int
	GlobalQueued  int
	SessionActive int
	SessionQueued int
}

func (s SchedulingSnapshot) Validate() error {
	if s.GlobalActive < 0 || s.GlobalActive > MaxGlobalActiveTurns ||
		s.GlobalQueued < 0 || s.GlobalQueued > MaxGlobalQueuedTurns ||
		s.SessionActive < 0 || s.SessionActive > MaxSessionActiveTurns ||
		s.SessionQueued < 0 || s.SessionQueued > MaxSessionQueuedTurns ||
		s.SessionActive > s.GlobalActive || s.SessionQueued > s.GlobalQueued {
		return ErrInvalidSchedulingSnapshot
	}
	return nil
}

func (s SchedulingSnapshot) AdmitInput() (AdmissionDecision, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.SessionActive == 1 {
		return AdmissionSteerTurn, nil
	}
	if s.SessionQueued == MaxSessionQueuedTurns {
		return "", ErrSessionQueueFull
	}
	if s.GlobalQueued == MaxGlobalQueuedTurns {
		return "", ErrGlobalQueueFull
	}
	return AdmissionQueueTurn, nil
}

func (s SchedulingSnapshot) CanDispatch() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.SessionActive != 0 || s.SessionQueued == 0 || s.GlobalActive == MaxGlobalActiveTurns {
		return ErrDispatchCapacityReached
	}
	return nil
}
