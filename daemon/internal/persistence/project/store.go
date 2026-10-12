// Package projectstore owns the per-project SQLite schema and persistence
// operations. It maps durable records to the project Domain without exposing
// database types outside this adapter.
package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

// Store is the single owner of a migrated project database connection.
type Store struct {
	database *sql.DB
}

// Open creates or opens a project database and applies every supported forward
// migration before exposing it to repository operations.
func Open(ctx context.Context, path string, mode sharedsqlite.OpenMode) (_ *Store, returnErr error) {
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{
		Path: path,
		Kind: sharedsqlite.Project,
		Mode: mode,
	})
	if err != nil {
		return nil, fmt.Errorf("open project store: %w", err)
	}

	ready := false
	defer func() {
		if ready {
			return
		}
		if closeErr := database.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close rejected project store: %w", closeErr))
		}
		if mode == sharedsqlite.CreateNew {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete project store: %w", removeErr))
			}
		}
	}()

	if _, err := sharedsqlite.Migrate(ctx, database, migrations); err != nil {
		return nil, fmt.Errorf("open project store: %w", err)
	}

	ready = true
	return &Store{database: database}, nil
}

// Close releases the project database connection.
func (s *Store) Close() error {
	if s == nil || s.database == nil {
		return nil
	}
	if err := s.database.Close(); err != nil {
		return fmt.Errorf("close project store: %w", err)
	}
	return nil
}

func (s *Store) requireDatabase() (*sql.DB, error) {
	if s == nil || s.database == nil {
		return nil, errors.New("project store is required")
	}
	return s.database, nil
}
