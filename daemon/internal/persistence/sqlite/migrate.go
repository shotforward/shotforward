package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	// ErrInvalidMigrationPlan means the binary contains missing, duplicate, or
	// malformed migrations. This is a development/release error, not a user data
	// error.
	ErrInvalidMigrationPlan = errors.New("invalid migration plan")
	// ErrSchemaTooNew means the database was written by a newer ShotForward
	// version and must not be opened by this binary.
	ErrSchemaTooNew = errors.New("database schema is newer than this application")
	// ErrMigrationFailed means one migration could not be applied atomically.
	ErrMigrationFailed = errors.New("database migration failed")
)

// Migration is one forward-only database schema transition. Versions start at
// one and remain contiguous for the lifetime of a Studio or Project schema.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrationResult describes the schema transition performed by Migrate.
type MigrationResult struct {
	FromVersion int
	ToVersion   int
	Applied     []int
}

// SchemaTooNewError exposes the versions needed for a useful client message.
type SchemaTooNewError struct {
	DatabaseVersion  int
	SupportedVersion int
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf(
		"%v: database version %d, highest supported version %d",
		ErrSchemaTooNew,
		e.DatabaseVersion,
		e.SupportedVersion,
	)
}

func (e *SchemaTooNewError) Unwrap() error {
	return ErrSchemaTooNew
}

// MigrationError identifies the exact transition that failed while retaining
// both the stable category and underlying SQLite error.
type MigrationError struct {
	Version int
	Name    string
	Cause   error
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("%v at version %d (%s): %v", ErrMigrationFailed, e.Version, e.Name, e.Cause)
}

func (e *MigrationError) Unwrap() []error {
	return []error{ErrMigrationFailed, e.Cause}
}

// SchemaVersion reads SQLite's application-controlled user_version value.
func SchemaVersion(ctx context.Context, database *sql.DB) (int, error) {
	if ctx == nil {
		return 0, fmt.Errorf("read schema version: context is required")
	}
	if database == nil {
		return 0, fmt.Errorf("read schema version: database is required")
	}

	var version int64
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version < 0 || version > math.MaxInt32 {
		return 0, fmt.Errorf("read schema version: invalid value %d", version)
	}

	return int(version), nil
}

// Migrate upgrades a database to the latest supplied version. The caller must
// run it before exposing the database to repositories or concurrent work.
func Migrate(ctx context.Context, database *sql.DB, migrations []Migration) (MigrationResult, error) {
	result := MigrationResult{}
	if ctx == nil {
		return result, fmt.Errorf("migrate database: context is required")
	}
	if database == nil {
		return result, fmt.Errorf("migrate database: database is required")
	}
	if err := validateMigrationPlan(migrations); err != nil {
		return result, fmt.Errorf("migrate database: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("migrate database: %w", err)
	}

	current, err := SchemaVersion(ctx, database)
	if err != nil {
		return result, fmt.Errorf("migrate database: %w", err)
	}
	result.FromVersion = current
	result.ToVersion = current

	latest := len(migrations)
	if current > latest {
		return result, &SchemaTooNewError{
			DatabaseVersion:  current,
			SupportedVersion: latest,
		}
	}

	for _, migration := range migrations[current:] {
		if err := applyMigration(ctx, database, migration); err != nil {
			return result, err
		}
		result.ToVersion = migration.Version
		result.Applied = append(result.Applied, migration.Version)
	}

	actual, err := SchemaVersion(ctx, database)
	if err != nil {
		return result, fmt.Errorf("migrate database: verify final version: %w", err)
	}
	if actual != latest {
		return result, fmt.Errorf("migrate database: final version %d, want %d", actual, latest)
	}

	return result, nil
}

func validateMigrationPlan(migrations []Migration) error {
	for index, migration := range migrations {
		expectedVersion := index + 1
		if migration.Version != expectedVersion {
			return fmt.Errorf(
				"%w: migration at index %d has version %d, want %d",
				ErrInvalidMigrationPlan,
				index,
				migration.Version,
				expectedVersion,
			)
		}
		if !validMigrationName(migration.Name) {
			return fmt.Errorf(
				"%w: migration version %d has invalid name %q",
				ErrInvalidMigrationPlan,
				migration.Version,
				migration.Name,
			)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf(
				"%w: migration version %d (%s) has empty SQL",
				ErrInvalidMigrationPlan,
				migration.Version,
				migration.Name,
			)
		}
		if strings.ContainsRune(migration.SQL, 0) {
			return fmt.Errorf(
				"%w: migration version %d (%s) contains a null byte",
				ErrInvalidMigrationPlan,
				migration.Version,
				migration.Name,
			)
		}
	}

	return nil
}

func validMigrationName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		if character >= 'a' && character <= 'z' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && character == '_' {
			continue
		}
		return false
	}
	return true
}

func applyMigration(ctx context.Context, database *sql.DB, migration Migration) (returnErr error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return &MigrationError{Version: migration.Version, Name: migration.Name, Cause: err}
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, fmt.Errorf("roll back migration: %w", rollbackErr))
		}
	}()

	if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
		return &MigrationError{Version: migration.Version, Name: migration.Name, Cause: err}
	}
	if _, err := transaction.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA user_version = %d", migration.Version),
	); err != nil {
		return &MigrationError{Version: migration.Version, Name: migration.Name, Cause: err}
	}
	if err := transaction.Commit(); err != nil {
		return &MigrationError{Version: migration.Version, Name: migration.Name, Cause: err}
	}
	committed = true

	return nil
}
