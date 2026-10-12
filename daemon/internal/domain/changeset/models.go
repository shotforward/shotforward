package changeset

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	MaxOperations  = 2_000
	MaxAppendBatch = 100
)

var (
	ErrInvalidChangeSet      = errors.New("invalid ChangeSet")
	ErrInvalidChangeSetState = errors.New("invalid ChangeSet state transition")
	ErrInvalidOperation      = errors.New("invalid ChangeSet Operation")
	ErrInvalidPrecondition   = errors.New("invalid ChangeSet Precondition")
	ErrInvalidConflict       = errors.New("invalid ChangeSet Conflict")
)

type Status string

const (
	StatusDraft      Status = "draft"
	StatusPending    Status = "pending"
	StatusApplied    Status = "applied"
	StatusRejected   Status = "rejected"
	StatusConflicted Status = "conflicted"
)

func (s Status) IsTerminal() bool {
	return s == StatusApplied || s == StatusRejected || s == StatusConflicted
}

type Source struct {
	AgentSessionID agent.SessionID
	AgentTurnID    agent.TurnID
	CodexThreadID  string
	CodexTurnID    string
}

func (s Source) Validate() error {
	if s.AgentSessionID.Validate() != nil || s.AgentTurnID.Validate() != nil || !validSingleLine(s.CodexThreadID, 500, true) || !validSingleLine(s.CodexTurnID, 500, true) {
		return ErrInvalidChangeSet
	}
	return nil
}

type ChangeSet struct {
	ID                    ChangeSetID
	ProjectID             project.ProjectID
	EpisodeID             project.EpisodeID
	Source                Source
	Title                 string
	Summary               string
	Status                Status
	OperationCount        int
	Revision              int64
	FailureCode           string
	FailureSummary        string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	FinalizedAt           *time.Time
	ResolvedAt            *time.Time
	RevertsChangeSetID    ChangeSetID
	RevertedByChangeSetID ChangeSetID
}

func (c ChangeSet) Validate() error {
	if c.ID.Validate() != nil || c.ProjectID.Validate() != nil || c.EpisodeID.Validate() != nil || c.Source.Validate() != nil || !validSingleLine(c.Title, 200, true) || !validMultiline(c.Summary, 2_000, false) || c.OperationCount < 0 || c.OperationCount > MaxOperations || c.Revision < 1 || !validTime(c.CreatedAt) || !validTime(c.UpdatedAt) || c.UpdatedAt.Before(c.CreatedAt) {
		return ErrInvalidChangeSet
	}
	if c.RevertsChangeSetID != "" && c.RevertsChangeSetID.Validate() != nil || c.RevertedByChangeSetID != "" && c.RevertedByChangeSetID.Validate() != nil {
		return ErrInvalidChangeSet
	}
	if c.Status != StatusDraft && c.Status != StatusPending && !c.Status.IsTerminal() {
		return ErrInvalidChangeSet
	}
	if c.FinalizedAt != nil && (!validTime(*c.FinalizedAt) || c.FinalizedAt.Before(c.CreatedAt)) {
		return ErrInvalidChangeSet
	}
	if c.ResolvedAt != nil && (!validTime(*c.ResolvedAt) || c.ResolvedAt.Before(c.CreatedAt)) {
		return ErrInvalidChangeSet
	}
	switch c.Status {
	case StatusDraft:
		if c.FinalizedAt != nil || c.ResolvedAt != nil || c.FailureCode != "" || c.FailureSummary != "" {
			return ErrInvalidChangeSet
		}
	case StatusPending:
		if c.OperationCount == 0 || c.FinalizedAt == nil || c.ResolvedAt != nil || c.FailureCode != "" || c.FailureSummary != "" {
			return ErrInvalidChangeSet
		}
	case StatusApplied:
		if c.OperationCount == 0 || c.FinalizedAt == nil || c.ResolvedAt == nil || c.FailureCode != "" || c.FailureSummary != "" {
			return ErrInvalidChangeSet
		}
	case StatusRejected:
		if c.OperationCount == 0 || c.FinalizedAt == nil || c.ResolvedAt == nil || c.FailureCode != "user_rejected" || !validMultiline(c.FailureSummary, 2_000, true) {
			return ErrInvalidChangeSet
		}
	case StatusConflicted:
		if c.OperationCount == 0 || c.FinalizedAt == nil || c.ResolvedAt == nil || !validSingleLine(c.FailureCode, 100, true) || !validMultiline(c.FailureSummary, 2_000, true) {
			return ErrInvalidChangeSet
		}
	}
	return nil
}

func (c ChangeSet) ValidateNew() error {
	if c.Validate() != nil || c.Status != StatusDraft || c.OperationCount != 0 || c.Revision != 1 || !c.CreatedAt.Equal(c.UpdatedAt) {
		return ErrInvalidChangeSet
	}
	return nil
}

type Conflict struct {
	ID          ConflictID
	ChangeSetID ChangeSetID
	OperationID OperationID
	Code        string
	Summary     string
	DetectedAt  time.Time
}

func (c Conflict) Validate() error {
	if c.ID.Validate() != nil || c.ChangeSetID.Validate() != nil || (c.OperationID != "" && c.OperationID.Validate() != nil) || !validSingleLine(c.Code, 100, true) || !validMultiline(c.Summary, 2_000, true) || !validTime(c.DetectedAt) {
		return ErrInvalidConflict
	}
	return nil
}

type CommandType string

const (
	CommandBegin    CommandType = "begin"
	CommandAppend   CommandType = "append"
	CommandFinalize CommandType = "finalize"
	CommandApply    CommandType = "apply"
	CommandReject   CommandType = "reject"
)

type CommandReceipt struct {
	ProjectID    project.ProjectID
	OperationKey string
	RequestHash  string
	Command      CommandType
	ChangeSetID  ChangeSetID
	CreatedAt    time.Time
}

func (r CommandReceipt) Validate() error {
	if r.ProjectID.Validate() != nil || r.ChangeSetID.Validate() != nil || !validSingleLine(r.OperationKey, 200, true) || len(r.RequestHash) != 64 || r.RequestHash != strings.ToLower(r.RequestHash) || !validTime(r.CreatedAt) {
		return ErrInvalidChangeSet
	}
	if _, err := hex.DecodeString(r.RequestHash); err != nil {
		return ErrInvalidChangeSet
	}
	if r.Command != CommandBegin && r.Command != CommandAppend && r.Command != CommandFinalize && r.Command != CommandApply && r.Command != CommandReject {
		return ErrInvalidChangeSet
	}
	return nil
}

func validTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 && value.Nanosecond()%int(time.Millisecond) == 0
}
func validSingleLine(value string, max int, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00") && utf8.RuneCountInString(value) <= max && (!required || value != "")
}
func validMultiline(value string, max int, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\x00") && utf8.RuneCountInString(value) <= max && (!required || value != "")
}
