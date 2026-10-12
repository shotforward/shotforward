package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrAgentTemplateRevisionConflict = errors.New("Agent template settings revision conflict")

type AgentTemplateSettings struct {
	Locale    string
	Overrides map[string]string
	Revision  int64
}

func (s *Store) AgentTemplates(ctx context.Context, initialLocale string, at time.Time) (AgentTemplateSettings, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("read Agent templates: %w", err)
	}
	if validateContext(ctx) != nil || !validTemplateLocale(initialLocale) || at.IsZero() {
		return AgentTemplateSettings{}, errors.New("read Agent templates: invalid request")
	}
	stamp := toMilliseconds(at)
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_template_preferences (singleton_id, locale, revision, created_at_ms, updated_at_ms)
VALUES (1, ?, 1, ?, ?)
ON CONFLICT DO NOTHING`, initialLocale, stamp, stamp); err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("initialize Agent templates: %w", err)
	}
	return readAgentTemplates(ctx, database)
}

func (s *Store) ReplaceAgentTemplateOverrides(ctx context.Context, overrides map[string]string, expectedRevision int64, at time.Time) (AgentTemplateSettings, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: %w", err)
	}
	if validateContext(ctx) != nil || expectedRevision < 1 || at.IsZero() || !validTemplateOverrides(overrides) {
		return AgentTemplateSettings{}, errors.New("update Agent templates: invalid request")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: begin: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE agent_template_preferences
SET revision = revision + 1, updated_at_ms = ?
WHERE singleton_id = 1 AND revision = ?`, toMilliseconds(at), expectedRevision)
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: affected rows: %w", err)
	}
	if rows != 1 {
		return AgentTemplateSettings{}, ErrAgentTemplateRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM agent_template_overrides"); err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: clear overrides: %w", err)
	}
	for key, content := range overrides {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO agent_template_overrides (template_key, content, updated_at_ms)
VALUES (?, ?, ?)`, key, content, toMilliseconds(at)); err != nil {
			return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: write override: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("update Agent templates: commit: %w", err)
	}
	return readAgentTemplates(ctx, database)
}

func readAgentTemplates(ctx context.Context, database *sql.DB) (AgentTemplateSettings, error) {
	value := AgentTemplateSettings{Overrides: map[string]string{}}
	if err := database.QueryRowContext(ctx, `
SELECT locale, revision FROM agent_template_preferences WHERE singleton_id = 1`).Scan(&value.Locale, &value.Revision); err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("read Agent template preferences: %w", err)
	}
	rows, err := database.QueryContext(ctx, `SELECT template_key, content FROM agent_template_overrides ORDER BY template_key`)
	if err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("read Agent template overrides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, content string
		if err := rows.Scan(&key, &content); err != nil {
			return AgentTemplateSettings{}, fmt.Errorf("read Agent template override: %w", err)
		}
		value.Overrides[key] = content
	}
	if err := rows.Err(); err != nil {
		return AgentTemplateSettings{}, fmt.Errorf("read Agent template overrides: %w", err)
	}
	return value, nil
}

func validTemplateLocale(value string) bool { return value == "zh-CN" || value == "en" }

func validTemplateOverrides(values map[string]string) bool {
	if len(values) > 12 {
		return false
	}
	for key, content := range values {
		if !validTemplateKey(key) || strings.TrimSpace(content) == "" || len(content) > 32*1024 {
			return false
		}
	}
	return true
}

func validTemplateKey(value string) bool {
	switch value {
	case "script.create", "script.extract_assets", "asset.add", "asset.edit_prompt", "storyboard.plan", "storyboard.add_clip",
		"prompt.first_frame", "prompt.last_frame", "prompt.video", "prompt.video.adapt.h3", "prompt.video.adapt.seedance", "prompt.video.adapt.wan3":
		return true
	default:
		return false
	}
}
