package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

var initialTestMigrations = []Migration{
	{
		Version: 1,
		Name:    "create_clips",
		SQL: `
CREATE TABLE clips (
    id TEXT PRIMARY KEY,
    prompt TEXT NOT NULL
);
INSERT INTO clips (id, prompt) VALUES ('clip-1', 'rainy street');
`,
	},
}

var upgradedTestMigrations = []Migration{
	initialTestMigrations[0],
	{
		Version: 2,
		Name:    "add_clip_duration",
		SQL: `
ALTER TABLE clips
ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 60000;
`,
	},
}

func TestMigrateUpgradesOldDataAndIsIdempotent(t *testing.T) {
	t.Parallel()

	database := newMigrationTestDatabase(t)
	first, err := Migrate(context.Background(), database, initialTestMigrations)
	if err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	assertMigrationResult(t, first, 0, 1, []int{1})

	second, err := Migrate(context.Background(), database, upgradedTestMigrations)
	if err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	assertMigrationResult(t, second, 1, 2, []int{2})

	var prompt string
	var durationMS int
	if err := database.QueryRow(
		"SELECT prompt, duration_ms FROM clips WHERE id = ?",
		"clip-1",
	).Scan(&prompt, &durationMS); err != nil {
		t.Fatalf("read migrated clip: %v", err)
	}
	if prompt != "rainy street" || durationMS != 60000 {
		t.Fatalf("migrated clip = (%q, %d), want (%q, %d)", prompt, durationMS, "rainy street", 60000)
	}

	third, err := Migrate(context.Background(), database, upgradedTestMigrations)
	if err != nil {
		t.Fatalf("idempotent Migrate() error = %v", err)
	}
	assertMigrationResult(t, third, 2, 2, nil)

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM clips").Scan(&count); err != nil {
		t.Fatalf("count clips: %v", err)
	}
	if count != 1 {
		t.Fatalf("clip count = %d, want 1", count)
	}
}

func TestMigrateRollsBackFailedVersion(t *testing.T) {
	t.Parallel()

	database := newMigrationTestDatabase(t)
	if _, err := Migrate(context.Background(), database, initialTestMigrations); err != nil {
		t.Fatalf("initial Migrate() error = %v", err)
	}

	failing := []Migration{
		initialTestMigrations[0],
		{
			Version: 2,
			Name:    "failing_change",
			SQL: `
CREATE TABLE should_be_rolled_back (id TEXT PRIMARY KEY);
INSERT INTO table_that_does_not_exist (id) VALUES ('failure');
`,
		},
	}
	result, err := Migrate(context.Background(), database, failing)
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("Migrate() error = %v, want ErrMigrationFailed", err)
	}
	assertMigrationResult(t, result, 1, 1, nil)

	version, err := SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	if tableExists(t, database, "should_be_rolled_back") {
		t.Fatal("table from failed migration still exists")
	}
}

func TestMigrateRejectsNewerDatabase(t *testing.T) {
	t.Parallel()

	database := newMigrationTestDatabase(t)
	if _, err := database.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("set future schema version: %v", err)
	}

	result, err := Migrate(context.Background(), database, upgradedTestMigrations)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Migrate() error = %v, want ErrSchemaTooNew", err)
	}
	assertMigrationResult(t, result, 3, 3, nil)

	var versionError *SchemaTooNewError
	if !errors.As(err, &versionError) {
		t.Fatalf("Migrate() error = %T, want *SchemaTooNewError", err)
	}
	if versionError.DatabaseVersion != 3 || versionError.SupportedVersion != 2 {
		t.Fatalf("SchemaTooNewError = %+v", versionError)
	}
}

func TestMigrateRejectsInvalidPlanBeforeWriting(t *testing.T) {
	t.Parallel()

	database := newMigrationTestDatabase(t)
	invalidPlans := []struct {
		name       string
		migrations []Migration
	}{
		{
			name: "version does not start at one",
			migrations: []Migration{{
				Version: 2,
				Name:    "start_at_two",
				SQL:     "CREATE TABLE invalid (id TEXT);",
			}},
		},
		{
			name: "invalid name",
			migrations: []Migration{{
				Version: 1,
				Name:    "Invalid Name",
				SQL:     "CREATE TABLE invalid (id TEXT);",
			}},
		},
		{
			name: "empty SQL",
			migrations: []Migration{{
				Version: 1,
				Name:    "empty_sql",
				SQL:     "  ",
			}},
		},
	}
	for _, test := range invalidPlans {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Migrate(context.Background(), database, test.migrations); !errors.Is(err, ErrInvalidMigrationPlan) {
				t.Fatalf("Migrate() error = %v, want ErrInvalidMigrationPlan", err)
			}
		})
	}

	version, err := SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != 0 {
		t.Fatalf("schema version = %d, want 0", version)
	}
	if tableExists(t, database, "invalid") {
		t.Fatal("invalid migration plan modified the database")
	}
}

func TestMigrateValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	database := newMigrationTestDatabase(t)
	if _, err := Migrate(nil, database, nil); err == nil {
		t.Fatal("Migrate(nil, database, nil) error = nil, want an error")
	}
	if _, err := Migrate(context.Background(), nil, nil); err == nil {
		t.Fatal("Migrate(context, nil, nil) error = nil, want an error")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Migrate(cancelled, database, initialTestMigrations); !errors.Is(err, context.Canceled) {
		t.Fatalf("Migrate(cancelled) error = %v, want context.Canceled", err)
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != 0 {
		t.Fatalf("schema version = %d, want 0", version)
	}
}

func newMigrationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "migration.db")
	database, err := Open(context.Background(), Options{
		Path: path,
		Kind: Project,
		Mode: CreateNew,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	return database
}

func tableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()

	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?",
		name,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_schema: %v", err)
	}
	return count != 0
}

func assertMigrationResult(t *testing.T, got MigrationResult, from, to int, applied []int) {
	t.Helper()

	if got.FromVersion != from || got.ToVersion != to {
		t.Errorf("MigrationResult versions = %d -> %d, want %d -> %d", got.FromVersion, got.ToVersion, from, to)
	}
	if len(got.Applied) != len(applied) {
		t.Fatalf("MigrationResult.Applied = %v, want %v", got.Applied, applied)
	}
	for index := range applied {
		if got.Applied[index] != applied[index] {
			t.Fatalf("MigrationResult.Applied = %v, want %v", got.Applied, applied)
		}
	}
}
