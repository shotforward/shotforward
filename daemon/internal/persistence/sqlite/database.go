// Package sqlite owns the shared SQLite connection policy used by ShotForward
// persistence adapters.
//
// It deliberately contains no product schema or repository queries. Studio and
// project stores use this package but keep independent migrations and schemas.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	modernsqlite "modernc.org/sqlite"
)

const (
	privateDatabaseMode os.FileMode = 0o600
	busyTimeoutMS                   = 5_000

	// Application IDs use readable four-byte values in the SQLite header:
	// "SFST" for the global Studio database and "SFPR" for a project database.
	studioApplicationID  int64 = 0x53465354
	projectApplicationID int64 = 0x53465052
)

var (
	// ErrDatabaseExists means CreateNew targeted an existing filesystem entry.
	ErrDatabaseExists = errors.New("database already exists")
	// ErrDatabaseNotFound means OpenExisting targeted no filesystem entry.
	ErrDatabaseNotFound = errors.New("database not found")
	// ErrInvalidDatabaseFile means the target is not a regular, direct file.
	ErrInvalidDatabaseFile = errors.New("invalid database file")
	// ErrDatabaseKindMismatch means the SQLite application ID does not match the
	// requested Studio or Project database kind.
	ErrDatabaseKindMismatch = errors.New("database kind mismatch")
)

// Kind identifies a ShotForward database file format.
type Kind uint8

const (
	Studio Kind = iota + 1
	Project
)

func (k Kind) String() string {
	switch k {
	case Studio:
		return "studio"
	case Project:
		return "project"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

func (k Kind) applicationID() (int64, error) {
	switch k {
	case Studio:
		return studioApplicationID, nil
	case Project:
		return projectApplicationID, nil
	default:
		return 0, fmt.Errorf("unsupported database kind %s", k)
	}
}

// OpenMode makes database creation explicit. OpenExisting never creates a file,
// and CreateNew never replaces one.
type OpenMode uint8

const (
	CreateNew OpenMode = iota + 1
	OpenExisting
)

// Options describes one database connection.
type Options struct {
	Path string
	Kind Kind
	Mode OpenMode
}

// Open creates or opens a ShotForward SQLite database and verifies its identity
// and connection policy before returning it.
func Open(ctx context.Context, options Options) (_ *sql.DB, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("open SQLite database: context is required")
	}
	applicationID, err := options.Kind.applicationID()
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	path, err := validateDatabasePath(options.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", options.Kind, err)
	}

	created, err := prepareDatabaseFile(path, options.Mode)
	if err != nil {
		return nil, fmt.Errorf("open %s database %q: %w", options.Kind, path, err)
	}
	if created {
		defer func() {
			if returnErr == nil {
				return
			}
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete database %q: %w", path, removeErr))
			}
		}()
	}

	connector, err := modernsqlite.NewConnector(databaseURI(path))
	if err != nil {
		return nil, fmt.Errorf("open %s database %q: create connector: %w", options.Kind, path, err)
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	database.SetConnMaxIdleTime(0)

	ready := false
	defer func() {
		if ready {
			return
		}
		if closeErr := database.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close rejected database %q: %w", path, closeErr))
		}
	}()

	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("open %s database %q: connect: %w", options.Kind, path, err)
	}
	connectionClosed := false
	defer func() {
		if connectionClosed {
			return
		}
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release database connection %q: %w", path, closeErr))
		}
	}()

	if created {
		if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return nil, fmt.Errorf("open %s database %q: set application ID: %w", options.Kind, path, err)
		}
	}
	if err := verifyApplicationID(ctx, connection, options.Kind, applicationID); err != nil {
		return nil, fmt.Errorf("open %s database %q: %w", options.Kind, path, err)
	}
	if err := configureAndVerify(ctx, connection); err != nil {
		return nil, fmt.Errorf("open %s database %q: %w", options.Kind, path, err)
	}
	if err := os.Chmod(path, privateDatabaseMode); err != nil {
		return nil, fmt.Errorf("open %s database %q: set permissions: %w", options.Kind, path, err)
	}
	if err := connection.Close(); err != nil {
		return nil, fmt.Errorf("open %s database %q: release initialization connection: %w", options.Kind, path, err)
	}
	connectionClosed = true

	ready = true
	return database, nil
}

func validateDatabasePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("database path is required")
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("database path must be absolute: %q", value)
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(filepath.VolumeName(value), `\\`) {
		return "", fmt.Errorf("database path must not use a Windows network share: %q", value)
	}

	return filepath.Clean(value), nil
}

func prepareDatabaseFile(path string, mode OpenMode) (bool, error) {
	switch mode {
	case CreateNew:
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, privateDatabaseMode)
		if errors.Is(err, os.ErrExist) {
			return false, ErrDatabaseExists
		}
		if err != nil {
			return false, err
		}
		if err := file.Close(); err != nil {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return false, errors.Join(err, fmt.Errorf("remove incomplete database %q: %w", path, removeErr))
			}
			return false, err
		}
		return true, nil
	case OpenExisting:
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, ErrDatabaseNotFound
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, ErrInvalidDatabaseFile
		}
		return false, nil
	default:
		return false, fmt.Errorf("unsupported open mode %d", mode)
	}
}

func databaseURI(path string) string {
	slashed := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && filepath.VolumeName(path) != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}

	uri := url.URL{Scheme: "file", Path: slashed}
	query := uri.Query()
	query.Set("mode", "rw")
	query.Set("_busy_timeout", fmt.Sprintf("%d", busyTimeoutMS))
	query.Set("_foreign_keys", "1")
	query.Set("_synchronous", "EXTRA")
	query.Set("_dqs", "0")
	query.Set("_defensive", "1")
	query.Add("_pragma", "trusted_schema(OFF)")
	uri.RawQuery = query.Encode()

	return uri.String()
}

func verifyApplicationID(ctx context.Context, connection *sql.Conn, kind Kind, expected int64) error {
	var actual int64
	if err := connection.QueryRowContext(ctx, "PRAGMA application_id").Scan(&actual); err != nil {
		return fmt.Errorf("read application ID: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("%w: expected %s application ID %d, got %d", ErrDatabaseKindMismatch, kind, expected, actual)
	}
	return nil
}

func configureAndVerify(ctx context.Context, connection *sql.Conn) error {
	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journalMode); err != nil {
		return fmt.Errorf("set journal mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		return fmt.Errorf("verify journal mode: got %q, want %q", journalMode, "delete")
	}

	integerSettings := []struct {
		name string
		want int64
	}{
		{name: "foreign_keys", want: 1},
		{name: "busy_timeout", want: busyTimeoutMS},
		{name: "synchronous", want: 3},
		{name: "trusted_schema", want: 0},
	}
	for _, setting := range integerSettings {
		var actual int64
		if err := connection.QueryRowContext(ctx, "PRAGMA "+setting.name).Scan(&actual); err != nil {
			return fmt.Errorf("read %s setting: %w", setting.name, err)
		}
		if actual != setting.want {
			return fmt.Errorf("verify %s setting: got %d, want %d", setting.name, actual, setting.want)
		}
	}

	return nil
}
