package studio

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestOpenCreatesAndMigratesStudioStore(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "studio.db")
	store, err := Open(context.Background(), path, sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	defer store.Close()

	version, err := sharedsqlite.SchemaVersion(context.Background(), store.database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}

	var tableName string
	if err := store.database.QueryRowContext(
		context.Background(),
		"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = 'project_library_entries'",
	).Scan(&tableName); err != nil {
		t.Fatalf("read project library table: %v", err)
	}
	if tableName != "project_library_entries" {
		t.Fatalf("table name = %q, want project_library_entries", tableName)
	}
}

func TestAgentTemplateOverridesKeepInitialLocaleAndCanReset(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	defer store.Close()

	at := time.Date(2026, time.September, 12, 3, 0, 0, 0, time.UTC)
	initial, err := store.AgentTemplates(context.Background(), "zh-CN", at)
	if err != nil {
		t.Fatalf("AgentTemplates() error = %v", err)
	}
	if initial.Locale != "zh-CN" || initial.Revision != 1 || len(initial.Overrides) != 0 {
		t.Fatalf("initial settings = %#v", initial)
	}

	updated, err := store.ReplaceAgentTemplateOverrides(context.Background(), map[string]string{
		"script.create": "Custom script prompt",
		"prompt.video":  "Custom video prompt",
	}, initial.Revision, at.Add(time.Second))
	if err != nil {
		t.Fatalf("ReplaceAgentTemplateOverrides() error = %v", err)
	}
	if updated.Revision != 2 || updated.Overrides["prompt.video"] != "Custom video prompt" || updated.Overrides["script.create"] != "Custom script prompt" {
		t.Fatalf("updated settings = %#v", updated)
	}

	persisted, err := store.AgentTemplates(context.Background(), "en", at.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AgentTemplates(second locale) error = %v", err)
	}
	if persisted.Locale != "zh-CN" {
		t.Fatalf("locale = %q, want first-use locale zh-CN", persisted.Locale)
	}

	reset, err := store.ReplaceAgentTemplateOverrides(context.Background(), map[string]string{}, updated.Revision, at.Add(3*time.Second))
	if err != nil {
		t.Fatalf("reset overrides error = %v", err)
	}
	if len(reset.Overrides) != 0 || reset.Revision != 3 {
		t.Fatalf("reset settings = %#v", reset)
	}
}

func TestOpenExistingRejectsNewerStudioSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "studio.db")
	store, err := Open(context.Background(), path, sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	if _, err := store.database.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatalf("set newer schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(context.Background(), path, sharedsqlite.OpenExisting)
	if !errors.Is(err, sharedsqlite.ErrSchemaTooNew) {
		t.Fatalf("Open(OpenExisting) error = %v, want ErrSchemaTooNew", err)
	}
}
