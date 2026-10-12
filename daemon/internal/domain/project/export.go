package project

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const ExportSnapshotVersion = 1

var (
	ErrInvalidExportSnapshot = errors.New("invalid export snapshot")
	ErrInvalidExportJob      = errors.New("invalid export job")
)

type ExportJobState string

const (
	ExportJobQueued          ExportJobState = "queued"
	ExportJobRunning         ExportJobState = "running"
	ExportJobCancelRequested ExportJobState = "cancel_requested"
	ExportJobSucceeded       ExportJobState = "succeeded"
	ExportJobFailed          ExportJobState = "failed"
	ExportJobCancelled       ExportJobState = "cancelled"
)

func (s ExportJobState) IsValid() bool {
	switch s {
	case ExportJobQueued, ExportJobRunning, ExportJobCancelRequested, ExportJobSucceeded, ExportJobFailed, ExportJobCancelled:
		return true
	}
	return false
}
func (s ExportJobState) IsTerminal() bool {
	return s == ExportJobSucceeded || s == ExportJobFailed || s == ExportJobCancelled
}
func (s ExportJobState) IsActive() bool {
	return s == ExportJobQueued || s == ExportJobRunning || s == ExportJobCancelRequested
}

func CanTransitionExportJob(from, to ExportJobState) bool {
	switch from {
	case ExportJobQueued:
		return to == ExportJobRunning || to == ExportJobCancelled
	case ExportJobRunning:
		return to == ExportJobSucceeded || to == ExportJobFailed || to == ExportJobCancelRequested
	case ExportJobCancelRequested:
		return to == ExportJobCancelled || to == ExportJobFailed
	}
	return false
}

type ExportClipSnapshot struct {
	SceneID              SceneID        `json:"scene_id"`
	ClipID               ClipID         `json:"clip_id"`
	Position             int            `json:"position"`
	MediaSlotID          MediaSlotID    `json:"media_slot_id"`
	MediaSlotRevision    int64          `json:"media_slot_revision"`
	MediaVersionID       MediaVersionID `json:"media_version_id"`
	MediaVersionRevision int64          `json:"media_version_revision"`
	ArtifactID           ArtifactID     `json:"artifact_id"`
	TrimInMilliseconds   int64          `json:"trim_in_ms"`
	TrimOutMilliseconds  int64          `json:"trim_out_ms"`
}

func (s ExportClipSnapshot) Validate() error {
	if s.SceneID.Validate() != nil || s.ClipID.Validate() != nil || s.Position < 1 || s.MediaSlotID.Validate() != nil || s.MediaSlotRevision < 1 || s.MediaVersionID.Validate() != nil || s.MediaVersionRevision < 1 || s.ArtifactID.Validate() != nil || s.TrimInMilliseconds < 0 || s.TrimOutMilliseconds <= s.TrimInMilliseconds {
		return ErrInvalidExportSnapshot
	}
	expectedSlot, err := FixedMediaSlotID(s.ClipID.String(), MediaSlotClipVideo)
	if err != nil || expectedSlot != s.MediaSlotID {
		return ErrInvalidExportSnapshot
	}
	return nil
}

type ExportSkippedClip struct {
	SceneID  SceneID `json:"scene_id"`
	ClipID   ClipID  `json:"clip_id"`
	Position int     `json:"position"`
	Reason   string  `json:"reason"`
}

func (s ExportSkippedClip) Validate() error {
	return validateExportPosition(s.SceneID, s.ClipID, s.Position, s.Reason)
}

type ExportSnapshot struct {
	SchemaVersion      int                  `json:"schema_version"`
	StoryboardRevision int64                `json:"storyboard_revision"`
	OutputWidth        int                  `json:"output_width"`
	OutputHeight       int                  `json:"output_height"`
	Clips              []ExportClipSnapshot `json:"clips"`
	SkippedClips       []ExportSkippedClip  `json:"skipped_clips"`
}

// HasSameOutput reports whether two snapshots would render the same MP4.
// Coordination revisions are intentionally ignored; media revisions, ordering,
// trims, skipped clips, and output dimensions all affect reuse eligibility.
func (s ExportSnapshot) HasSameOutput(other ExportSnapshot) bool {
	if s.SchemaVersion != other.SchemaVersion || s.OutputWidth != other.OutputWidth || s.OutputHeight != other.OutputHeight || len(s.Clips) != len(other.Clips) || len(s.SkippedClips) != len(other.SkippedClips) {
		return false
	}
	for index := range s.Clips {
		left, right := s.Clips[index], other.Clips[index]
		if left.SceneID != right.SceneID || left.ClipID != right.ClipID || left.Position != right.Position || left.MediaSlotID != right.MediaSlotID || left.MediaVersionID != right.MediaVersionID || left.MediaVersionRevision != right.MediaVersionRevision || left.ArtifactID != right.ArtifactID || left.TrimInMilliseconds != right.TrimInMilliseconds || left.TrimOutMilliseconds != right.TrimOutMilliseconds {
			return false
		}
	}
	for index := range s.SkippedClips {
		if s.SkippedClips[index] != other.SkippedClips[index] {
			return false
		}
	}
	return true
}

func (s ExportSnapshot) Validate() error {
	if s.SchemaVersion != ExportSnapshotVersion || s.StoryboardRevision < 1 {
		return ErrInvalidExportSnapshot
	}
	if _, err := NewOutputSpec(s.OutputWidth, s.OutputHeight); err != nil || len(s.Clips)+len(s.SkippedClips) > 10_000 {
		return ErrInvalidExportSnapshot
	}
	positions := make(map[int]struct{}, len(s.Clips)+len(s.SkippedClips))
	clips := make(map[ClipID]struct{}, len(s.Clips)+len(s.SkippedClips))
	for _, item := range s.Clips {
		if item.Validate() != nil {
			return ErrInvalidExportSnapshot
		}
		if _, exists := positions[item.Position]; exists {
			return ErrInvalidExportSnapshot
		}
		if _, exists := clips[item.ClipID]; exists {
			return ErrInvalidExportSnapshot
		}
		positions[item.Position], clips[item.ClipID] = struct{}{}, struct{}{}
	}
	for _, item := range s.SkippedClips {
		if item.Validate() != nil {
			return ErrInvalidExportSnapshot
		}
		if _, exists := positions[item.Position]; exists {
			return ErrInvalidExportSnapshot
		}
		if _, exists := clips[item.ClipID]; exists {
			return ErrInvalidExportSnapshot
		}
		positions[item.Position], clips[item.ClipID] = struct{}{}, struct{}{}
	}
	for position := 1; position <= len(positions); position++ {
		if _, exists := positions[position]; !exists {
			return ErrInvalidExportSnapshot
		}
	}
	return nil
}

type ExportJob struct {
	ID                 ExportJobID
	ProjectID          ProjectID
	EpisodeID          EpisodeID
	OperationKey       string
	State              ExportJobState
	Snapshot           ExportSnapshot
	ProgressStage      string
	ProgressMessage    string
	OutputRelativePath string
	FailureCode        string
	FailureMessage     string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

func (j ExportJob) Validate() error {
	if j.ID.Validate() != nil || j.ProjectID.Validate() != nil || j.EpisodeID.Validate() != nil || !j.State.IsValid() || j.Snapshot.Validate() != nil || len(j.Snapshot.Clips) == 0 || !validExportText(j.OperationKey, 200, true) || !validExportText(j.ProgressStage, 100, false) || !validExportMessage(j.ProgressMessage, 500, false) || !validExportText(j.FailureCode, 100, false) || !validExportMessage(j.FailureMessage, 2_000, false) {
		return ErrInvalidExportJob
	}
	if err := validateMediaRevisionTimes(ErrInvalidExportJob, j.Revision, j.CreatedAt, j.UpdatedAt, nil); err != nil {
		return err
	}
	if j.OutputRelativePath != "" && (path.IsAbs(j.OutputRelativePath) || path.Clean(j.OutputRelativePath) != j.OutputRelativePath || !strings.HasPrefix(j.OutputRelativePath, "exports/") || !strings.HasSuffix(strings.ToLower(j.OutputRelativePath), ".mp4") || strings.Contains(j.OutputRelativePath, "\\")) {
		return ErrInvalidExportJob
	}
	if j.StartedAt != nil && (j.StartedAt.Before(j.CreatedAt) || j.StartedAt.After(j.UpdatedAt)) || j.FinishedAt != nil && (j.FinishedAt.Before(j.CreatedAt) || j.FinishedAt.After(j.UpdatedAt)) {
		return ErrInvalidExportJob
	}
	switch j.State {
	case ExportJobQueued:
		if j.StartedAt != nil || j.FinishedAt != nil || j.OutputRelativePath != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return ErrInvalidExportJob
		}
	case ExportJobRunning, ExportJobCancelRequested:
		if j.StartedAt == nil || j.FinishedAt != nil || j.OutputRelativePath != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return ErrInvalidExportJob
		}
	case ExportJobSucceeded:
		if j.StartedAt == nil || j.FinishedAt == nil || j.OutputRelativePath == "" || j.FailureCode != "" || j.FailureMessage != "" {
			return ErrInvalidExportJob
		}
	case ExportJobFailed:
		if j.StartedAt == nil || j.FinishedAt == nil || j.OutputRelativePath != "" || j.FailureCode == "" || j.FailureMessage == "" {
			return ErrInvalidExportJob
		}
	case ExportJobCancelled:
		if j.FinishedAt == nil || j.OutputRelativePath != "" || j.FailureCode != "" || j.FailureMessage != "" {
			return ErrInvalidExportJob
		}
	}
	return nil
}

func validateExportPosition(sceneID SceneID, clipID ClipID, position int, reason string) error {
	if sceneID.Validate() != nil || clipID.Validate() != nil || position < 1 || !validExportText(reason, 100, true) {
		return ErrInvalidExportSnapshot
	}
	return nil
}

func validExportText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00") && utf8.RuneCountInString(value) <= maximum && (!required || value != "")
}

func validExportMessage(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum && (!required || strings.TrimSpace(value) != "")
}

func (j ExportJob) String() string {
	return fmt.Sprintf("%s/%s", j.ProjectID, j.ID)
}
