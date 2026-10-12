package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrProjectLibraryEntryNotFound means no registered project has the given ID.
	ErrProjectLibraryEntryNotFound = errors.New("project library entry not found")
	// ErrProjectLibraryEntryConflict means the project ID or immutable root path
	// is already registered.
	ErrProjectLibraryEntryConflict = errors.New("project library entry conflicts with an existing entry")
	ErrProjectHasActiveAgentTurns  = errors.New("project has active Agent turns")
)

// ProjectSnapshot is the rebuildable project-card data cached from project.db.
// It is never the source of truth for project content.
type ProjectSnapshot struct {
	DisplayName      string
	CoverCacheKey    string
	Width            int
	Height           int
	EpisodeCount     int
	ProjectUpdatedAt time.Time
}

// ProjectLibraryEntry is one registered project plus its cached summary.
type ProjectLibraryEntry struct {
	ProjectID    string
	RootPath     string
	Snapshot     ProjectSnapshot
	AddedAt      time.Time
	LastOpenedAt *time.Time
	RefreshedAt  time.Time
}

// ProjectSnapshotRefresh identifies a project and replaces only its rebuildable
// cache. A refresh cannot move a project or change when it was registered.
type ProjectSnapshotRefresh struct {
	ProjectID   string
	Snapshot    ProjectSnapshot
	RefreshedAt time.Time
}

// AddProject registers a project at an immutable root path.
func (s *Store) AddProject(ctx context.Context, entry ProjectLibraryEntry) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("add project library entry: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("add project library entry: %w", err)
	}
	if err := validateProjectLibraryEntry(entry); err != nil {
		return fmt.Errorf("add project library entry: %w", err)
	}

	result, err := database.ExecContext(ctx, `
INSERT INTO project_library_entries (
    project_id,
    root_path,
    display_name_snapshot,
    cover_cache_key,
    width_snapshot,
    height_snapshot,
    episode_count_snapshot,
    project_updated_at_ms_snapshot,
    added_at_ms,
    last_opened_at_ms,
    refreshed_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING
`,
		entry.ProjectID,
		entry.RootPath,
		entry.Snapshot.DisplayName,
		nullableString(entry.Snapshot.CoverCacheKey),
		entry.Snapshot.Width,
		entry.Snapshot.Height,
		entry.Snapshot.EpisodeCount,
		toMilliseconds(entry.Snapshot.ProjectUpdatedAt),
		toMilliseconds(entry.AddedAt),
		nullableTimeMilliseconds(entry.LastOpenedAt),
		toMilliseconds(entry.RefreshedAt),
	)
	if err != nil {
		return fmt.Errorf("add project library entry: insert: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("add project library entry: read affected rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("add project library entry: %w", ErrProjectLibraryEntryConflict)
	}
	return nil
}

// Project returns one registered project by its stable project ID.
func (s *Store) Project(ctx context.Context, projectID string) (ProjectLibraryEntry, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return ProjectLibraryEntry{}, fmt.Errorf("read project library entry: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return ProjectLibraryEntry{}, fmt.Errorf("read project library entry: %w", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return ProjectLibraryEntry{}, fmt.Errorf("read project library entry: %w", err)
	}

	entry, err := scanProjectLibraryEntry(database.QueryRowContext(ctx, selectProjectSQL+" WHERE project_id = ?", projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectLibraryEntry{}, fmt.Errorf("read project library entry %q: %w", projectID, ErrProjectLibraryEntryNotFound)
	}
	if err != nil {
		return ProjectLibraryEntry{}, fmt.Errorf("read project library entry %q: %w", projectID, err)
	}
	return entry, nil
}

// Projects lists registered projects by most recently opened, then most
// recently added. Entries never disappear merely because their roots are
// currently unavailable.
func (s *Store) Projects(ctx context.Context) ([]ProjectLibraryEntry, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list project library entries: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("list project library entries: %w", err)
	}

	rows, err := database.QueryContext(ctx, selectProjectSQL+`
 ORDER BY
    last_opened_at_ms IS NULL ASC,
    last_opened_at_ms DESC,
    added_at_ms DESC,
    project_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list project library entries: query: %w", err)
	}
	defer rows.Close()

	entries := make([]ProjectLibraryEntry, 0)
	for rows.Next() {
		entry, err := scanProjectLibraryEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("list project library entries: scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list project library entries: iterate: %w", err)
	}
	return entries, nil
}

// RefreshProject replaces the cached project-card summary without changing the
// project identity, root path, registration time, or recent-open ordering.
func (s *Store) RefreshProject(ctx context.Context, refresh ProjectSnapshotRefresh) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("refresh project library entry: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("refresh project library entry: %w", err)
	}
	if err := validateProjectID(refresh.ProjectID); err != nil {
		return fmt.Errorf("refresh project library entry: %w", err)
	}
	if err := validateSnapshot(refresh.Snapshot); err != nil {
		return fmt.Errorf("refresh project library entry: %w", err)
	}
	if err := validateTimestamp("refreshed at", refresh.RefreshedAt); err != nil {
		return fmt.Errorf("refresh project library entry: %w", err)
	}

	result, err := database.ExecContext(ctx, `
UPDATE project_library_entries
SET display_name_snapshot = ?,
    cover_cache_key = ?,
    width_snapshot = ?,
    height_snapshot = ?,
    episode_count_snapshot = ?,
    project_updated_at_ms_snapshot = ?,
    refreshed_at_ms = ?
WHERE project_id = ?
`,
		refresh.Snapshot.DisplayName,
		nullableString(refresh.Snapshot.CoverCacheKey),
		refresh.Snapshot.Width,
		refresh.Snapshot.Height,
		refresh.Snapshot.EpisodeCount,
		toMilliseconds(refresh.Snapshot.ProjectUpdatedAt),
		toMilliseconds(refresh.RefreshedAt),
		refresh.ProjectID,
	)
	if err != nil {
		return fmt.Errorf("refresh project library entry %q: update: %w", refresh.ProjectID, err)
	}
	return requireOneAffectedRow(result, "refresh project library entry", refresh.ProjectID)
}

// MarkProjectOpened changes only the recent-open timestamp.
func (s *Store) MarkProjectOpened(ctx context.Context, projectID string, openedAt time.Time) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("mark project opened: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("mark project opened: %w", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return fmt.Errorf("mark project opened: %w", err)
	}
	if err := validateTimestamp("opened at", openedAt); err != nil {
		return fmt.Errorf("mark project opened: %w", err)
	}

	result, err := database.ExecContext(
		ctx,
		"UPDATE project_library_entries SET last_opened_at_ms = ? WHERE project_id = ?",
		toMilliseconds(openedAt),
		projectID,
	)
	if err != nil {
		return fmt.Errorf("mark project %q opened: update: %w", projectID, err)
	}
	return requireOneAffectedRow(result, "mark project opened", projectID)
}

// DeleteProjectRegistration removes only the Studio registry row. Application
// code may call it only as the final step of the separately coordinated
// permanent-delete use case; this repository does not delete project files.
func (s *Store) DeleteProjectRegistration(ctx context.Context, projectID string) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("delete project library entry: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return fmt.Errorf("delete project library entry: %w", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return fmt.Errorf("delete project library entry: %w", err)
	}

	result, err := database.ExecContext(ctx, "DELETE FROM project_library_entries WHERE project_id = ?", projectID)
	if err != nil {
		return fmt.Errorf("delete project library entry %q: %w", projectID, err)
	}
	return requireOneAffectedRow(result, "delete project library entry", projectID)
}

func (s *Store) ProjectHasActiveAgentTurns(ctx context.Context, projectID string) (bool, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return false, fmt.Errorf("inspect Project Agent turns: %w", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return false, err
	}
	var count int
	err = database.QueryRowContext(ctx, `SELECT count(*) FROM agent_turns t JOIN agent_sessions s ON s.id=t.agent_session_id WHERE s.project_id=? AND t.status IN ('queued','running','waiting_user')`, projectID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect Project Agent turns: %w", err)
	}
	return count != 0, nil
}

func (s *Store) EpisodeHasActiveAgentTurns(ctx context.Context, projectID, episodeID string) (bool, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return false, fmt.Errorf("inspect Episode Agent turns: %w", err)
	}
	var count int
	err = database.QueryRowContext(ctx, `SELECT count(*) FROM agent_turns t JOIN agent_sessions s ON s.id=t.agent_session_id WHERE s.project_id=? AND s.episode_id=? AND t.status IN ('queued','running','waiting_user')`, projectID, episodeID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect Episode Agent turns: %w", err)
	}
	return count != 0, nil
}

// DeleteProjectData removes Studio-owned projections and conversations after
// the portable project directory has been permanently deleted.
func (s *Store) DeleteProjectData(ctx context.Context, projectID string) (returnErr error) {
	database, err := s.requireDatabase()
	if err != nil {
		return err
	}
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete Project data: begin: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_turns t JOIN agent_sessions s ON s.id=t.agent_session_id WHERE s.project_id=? AND t.status IN ('queued','running','waiting_user')`, projectID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrProjectHasActiveAgentTurns
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_sessions WHERE project_id=?`, projectID); err != nil {
		return fmt.Errorf("delete Project Agent sessions: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM project_library_entries WHERE project_id=?`, projectID)
	if err != nil {
		return fmt.Errorf("delete Project registration: %w", err)
	}
	if err := requireOneAffectedRow(result, "delete project library entry", projectID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete Project data: commit: %w", err)
	}
	return nil
}

const selectProjectSQL = `
SELECT
    project_id,
    root_path,
    display_name_snapshot,
    cover_cache_key,
    width_snapshot,
    height_snapshot,
    episode_count_snapshot,
    project_updated_at_ms_snapshot,
    added_at_ms,
    last_opened_at_ms,
    refreshed_at_ms
FROM project_library_entries`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProjectLibraryEntry(row rowScanner) (ProjectLibraryEntry, error) {
	var entry ProjectLibraryEntry
	var coverCacheKey sql.NullString
	var projectUpdatedAtMS int64
	var addedAtMS int64
	var lastOpenedAtMS sql.NullInt64
	var refreshedAtMS int64

	err := row.Scan(
		&entry.ProjectID,
		&entry.RootPath,
		&entry.Snapshot.DisplayName,
		&coverCacheKey,
		&entry.Snapshot.Width,
		&entry.Snapshot.Height,
		&entry.Snapshot.EpisodeCount,
		&projectUpdatedAtMS,
		&addedAtMS,
		&lastOpenedAtMS,
		&refreshedAtMS,
	)
	if err != nil {
		return ProjectLibraryEntry{}, err
	}

	if coverCacheKey.Valid {
		entry.Snapshot.CoverCacheKey = coverCacheKey.String
	}
	entry.Snapshot.ProjectUpdatedAt = fromMilliseconds(projectUpdatedAtMS)
	entry.AddedAt = fromMilliseconds(addedAtMS)
	if lastOpenedAtMS.Valid {
		value := fromMilliseconds(lastOpenedAtMS.Int64)
		entry.LastOpenedAt = &value
	}
	entry.RefreshedAt = fromMilliseconds(refreshedAtMS)
	return entry, nil
}

func validateProjectLibraryEntry(entry ProjectLibraryEntry) error {
	if err := validateProjectID(entry.ProjectID); err != nil {
		return err
	}
	if strings.TrimSpace(entry.RootPath) == "" {
		return errors.New("project root path is required")
	}
	if !filepath.IsAbs(entry.RootPath) {
		return fmt.Errorf("project root path must be absolute: %q", entry.RootPath)
	}
	if filepath.Clean(entry.RootPath) != entry.RootPath {
		return fmt.Errorf("project root path must be clean: %q", entry.RootPath)
	}
	if err := validateSnapshot(entry.Snapshot); err != nil {
		return err
	}
	if err := validateTimestamp("added at", entry.AddedAt); err != nil {
		return err
	}
	if entry.LastOpenedAt != nil {
		if err := validateTimestamp("last opened at", *entry.LastOpenedAt); err != nil {
			return err
		}
	}
	return validateTimestamp("refreshed at", entry.RefreshedAt)
}

func validateSnapshot(snapshot ProjectSnapshot) error {
	if strings.TrimSpace(snapshot.DisplayName) == "" {
		return errors.New("display name snapshot is required")
	}
	if snapshot.CoverCacheKey != "" {
		if strings.TrimSpace(snapshot.CoverCacheKey) == "" ||
			strings.ContainsAny(snapshot.CoverCacheKey, `/\\`) ||
			snapshot.CoverCacheKey == "." || snapshot.CoverCacheKey == ".." {
			return fmt.Errorf("cover cache key is invalid: %q", snapshot.CoverCacheKey)
		}
	}
	if snapshot.Width <= 0 || snapshot.Height <= 0 {
		return fmt.Errorf("snapshot dimensions must be positive, got %dx%d", snapshot.Width, snapshot.Height)
	}
	if snapshot.EpisodeCount < 0 {
		return fmt.Errorf("episode count must not be negative: %d", snapshot.EpisodeCount)
	}
	return validateTimestamp("project updated at", snapshot.ProjectUpdatedAt)
}

func validateProjectID(projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return errors.New("project ID is required")
	}
	return nil
}

func validateTimestamp(label string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", label)
	}
	if value.UnixMilli() < 0 {
		return fmt.Errorf("%s must not be before the Unix epoch", label)
	}
	return nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTimeMilliseconds(value *time.Time) any {
	if value == nil {
		return nil
	}
	return toMilliseconds(*value)
}

func toMilliseconds(value time.Time) int64 {
	return value.UnixMilli()
}

func fromMilliseconds(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func requireOneAffectedRow(result sql.Result, operation, projectID string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %q: read affected rows: %w", operation, projectID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s %q: %w", operation, projectID, ErrProjectLibraryEntryNotFound)
	}
	return nil
}
