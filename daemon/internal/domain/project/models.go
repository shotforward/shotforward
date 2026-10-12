// Package project defines the Project aggregate and its Episode children. It
// contains no filesystem, SQLite, HTTP, or Agent Runtime concerns.
package project

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxScriptCharacters      = 50_000
	MinEpisodeTargetDuration = 15 * time.Second
	MaxEpisodeTargetDuration = 30 * time.Minute
)

var (
	ErrInvalidProject = errors.New("invalid project")
	ErrInvalidEpisode = errors.New("invalid episode")
	ErrInvalidScript  = errors.New("invalid episode script")
)

// Project contains only durable, project-wide product facts.
type Project struct {
	ID          ProjectID
	Name        string
	Description string
	Output      OutputSpec
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (p Project) Validate() error {
	if err := p.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProject, err)
	}
	if err := validateSingleLine("name", p.Name); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProject, err)
	}
	if strings.ContainsRune(p.Description, 0) {
		return fmt.Errorf("%w: description contains a null byte", ErrInvalidProject)
	}
	if !p.Output.IsValid() {
		return fmt.Errorf("%w: %w", ErrInvalidProject, ErrInvalidOutputSpec)
	}
	if p.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidProject)
	}
	if err := validateTimestamp("created at", p.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProject, err)
	}
	if err := validateTimestamp("updated at", p.UpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProject, err)
	}
	return nil
}

// EpisodeMetadata is the lightweight part of an Episode used by list queries.
type EpisodeMetadata struct {
	ID             EpisodeID
	ProjectID      ProjectID
	SequenceNumber int
	Title          string
	TargetDuration time.Duration
	Revision       int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

func (e EpisodeMetadata) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	if err := e.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	if e.SequenceNumber < 1 {
		return fmt.Errorf("%w: sequence number must be at least 1", ErrInvalidEpisode)
	}
	if err := validateSingleLine("title", e.Title); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	if e.TargetDuration <= 0 || e.TargetDuration%time.Millisecond != 0 {
		return fmt.Errorf("%w: target duration must be a positive whole number of milliseconds", ErrInvalidEpisode)
	}
	if e.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidEpisode)
	}
	if err := validateTimestamp("created at", e.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	if err := validateTimestamp("updated at", e.UpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	if e.DeletedAt != nil {
		if err := validateTimestamp("deleted at", *e.DeletedAt); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
		}
		if e.DeletedAt.Before(e.CreatedAt) {
			return fmt.Errorf("%w: deleted at must not be before created at", ErrInvalidEpisode)
		}
		if e.DeletedAt.After(e.UpdatedAt) {
			return fmt.Errorf("%w: deleted at must not be after updated at", ErrInvalidEpisode)
		}
	}
	return nil
}

func (e EpisodeMetadata) IsDeleted() bool { return e.DeletedAt != nil }

// EpisodeSummary adds script freshness to list metadata without loading the
// potentially large Markdown body.
type EpisodeSummary struct {
	EpisodeMetadata
	ScriptRevision  int64
	ScriptUpdatedAt time.Time
}

func (e EpisodeSummary) Validate() error {
	if err := e.EpisodeMetadata.Validate(); err != nil {
		return err
	}
	if e.ScriptRevision < 1 {
		return fmt.Errorf("%w: script revision must be at least 1", ErrInvalidEpisode)
	}
	if err := validateTimestamp("script updated at", e.ScriptUpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	return nil
}

// Script is the independently revisioned Markdown field group owned by an
// Episode. It has no identity or lifecycle separate from that Episode.
type Script struct {
	Markdown  string
	Revision  int64
	UpdatedAt time.Time
}

func (s Script) Validate() error {
	if err := ValidateScriptMarkdown(s.Markdown); err != nil {
		return err
	}
	if s.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidScript)
	}
	if err := validateTimestamp("updated at", s.UpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScript, err)
	}
	return nil
}

// ValidateScriptMarkdown checks only hard storage invariants. Editor format
// compatibility is intentionally reported separately as non-blocking diagnostics.
func ValidateScriptMarkdown(markdown string) error {
	if !utf8.ValidString(markdown) {
		return fmt.Errorf("%w: Markdown must be valid UTF-8", ErrInvalidScript)
	}
	if strings.ContainsRune(markdown, 0) {
		return fmt.Errorf("%w: Markdown contains a null byte", ErrInvalidScript)
	}
	if strings.ContainsRune(markdown, '\r') {
		return fmt.Errorf("%w: Markdown must use normalized LF line endings", ErrInvalidScript)
	}
	if ScriptCharacterCount(markdown) > MaxScriptCharacters {
		return fmt.Errorf("%w: Markdown exceeds %d characters", ErrInvalidScript, MaxScriptCharacters)
	}
	return nil
}

// NormalizeMarkdown converts platform line endings before validation/storage.
func NormalizeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

// ScriptCharacterCount uses Unicode code points so the product limit behaves
// consistently for Chinese and other multi-byte UTF-8 text.
func ScriptCharacterCount(value string) int {
	return utf8.RuneCountInString(value)
}

// Episode combines lightweight metadata with its owned script field group.
type Episode struct {
	EpisodeMetadata
	Script Script
}

// Summary returns the lightweight Episode projection without script Markdown.
func (e Episode) Summary() EpisodeSummary {
	return EpisodeSummary{
		EpisodeMetadata: e.EpisodeMetadata,
		ScriptRevision:  e.Script.Revision,
		ScriptUpdatedAt: e.Script.UpdatedAt,
	}
}

func (e Episode) Validate() error {
	if err := e.EpisodeMetadata.Validate(); err != nil {
		return err
	}
	if err := e.Script.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisode, err)
	}
	return nil
}

func validateSingleLine(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not start or end with whitespace", label)
	}
	if strings.ContainsAny(value, "\r\n") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be a single line", label)
	}
	return nil
}

func validateTimestamp(label string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", label)
	}
	if value.Location() != time.UTC {
		return fmt.Errorf("%s must use UTC", label)
	}
	if value.UnixMilli() < 0 {
		return fmt.Errorf("%s must not be before the Unix epoch", label)
	}
	if value.Nanosecond()%int(time.Millisecond) != 0 {
		return fmt.Errorf("%s must use millisecond precision", label)
	}
	return nil
}
