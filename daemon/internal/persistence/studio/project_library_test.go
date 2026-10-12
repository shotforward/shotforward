package studio

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestProjectLibraryLifecyclePersistsAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "studio.db")
	store := newTestStore(t, path)

	alpha := testProjectEntry("project-alpha", filepath.Join(t.TempDir(), "alpha"), testTime(1))
	alpha.LastOpenedAt = nil
	alpha.Snapshot.CoverCacheKey = ""
	beta := testProjectEntry("project-beta", filepath.Join(t.TempDir(), "beta"), testTime(2))
	betaOpenedAt := testTime(3)
	beta.LastOpenedAt = &betaOpenedAt

	if err := store.AddProject(ctx, alpha); err != nil {
		t.Fatalf("AddProject(alpha) error = %v", err)
	}
	if err := store.AddProject(ctx, beta); err != nil {
		t.Fatalf("AddProject(beta) error = %v", err)
	}

	entries, err := store.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects() error = %v", err)
	}
	if got := projectIDs(entries); !reflect.DeepEqual(got, []string{"project-beta", "project-alpha"}) {
		t.Fatalf("Projects() IDs = %v, want recent project first", got)
	}
	if entries[1].Snapshot.CoverCacheKey != "" {
		t.Fatalf("nullable cover cache key = %q, want empty", entries[1].Snapshot.CoverCacheKey)
	}

	refreshedAt := testTime(4)
	refresh := ProjectSnapshotRefresh{
		ProjectID: "project-alpha",
		Snapshot: ProjectSnapshot{
			DisplayName:      "Alpha Revised",
			CoverCacheKey:    "project-alpha-v2.webp",
			Width:            1080,
			Height:           1920,
			EpisodeCount:     7,
			ProjectUpdatedAt: testTime(5),
		},
		RefreshedAt: refreshedAt,
	}
	if err := store.RefreshProject(ctx, refresh); err != nil {
		t.Fatalf("RefreshProject() error = %v", err)
	}
	alphaOpenedAt := testTime(6)
	if err := store.MarkProjectOpened(ctx, alpha.ProjectID, alphaOpenedAt); err != nil {
		t.Fatalf("MarkProjectOpened() error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatalf("Open(OpenExisting) error = %v", err)
	}
	defer reopened.Close()

	got, err := reopened.Project(ctx, alpha.ProjectID)
	if err != nil {
		t.Fatalf("Project(alpha) error = %v", err)
	}
	want := alpha
	want.Snapshot = refresh.Snapshot
	want.RefreshedAt = refreshedAt
	want.LastOpenedAt = &alphaOpenedAt
	assertProjectEntry(t, got, want)
	if got.RootPath != alpha.RootPath {
		t.Fatalf("root path changed during refresh: got %q, want %q", got.RootPath, alpha.RootPath)
	}
	if !got.AddedAt.Equal(alpha.AddedAt) {
		t.Fatalf("added time changed during refresh: got %v, want %v", got.AddedAt, alpha.AddedAt)
	}

	entries, err = reopened.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects() after restart error = %v", err)
	}
	if gotIDs := projectIDs(entries); !reflect.DeepEqual(gotIDs, []string{"project-alpha", "project-beta"}) {
		t.Fatalf("Projects() IDs after restart = %v, want alpha first", gotIDs)
	}

	if err := reopened.DeleteProjectRegistration(ctx, beta.ProjectID); err != nil {
		t.Fatalf("DeleteProjectRegistration(beta) error = %v", err)
	}
	if _, err := reopened.Project(ctx, beta.ProjectID); !errors.Is(err, ErrProjectLibraryEntryNotFound) {
		t.Fatalf("Project(deleted beta) error = %v, want ErrProjectLibraryEntryNotFound", err)
	}
}

func TestAddProjectRejectsIdentityAndPathConflicts(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	ctx := context.Background()
	first := testProjectEntry("project-1", filepath.Join(t.TempDir(), "one"), testTime(1))
	if err := store.AddProject(ctx, first); err != nil {
		t.Fatalf("AddProject(first) error = %v", err)
	}

	sameID := testProjectEntry(first.ProjectID, filepath.Join(t.TempDir(), "two"), testTime(2))
	if err := store.AddProject(ctx, sameID); !errors.Is(err, ErrProjectLibraryEntryConflict) {
		t.Fatalf("AddProject(same ID) error = %v, want ErrProjectLibraryEntryConflict", err)
	}
	samePath := testProjectEntry("project-2", first.RootPath, testTime(3))
	if err := store.AddProject(ctx, samePath); !errors.Is(err, ErrProjectLibraryEntryConflict) {
		t.Fatalf("AddProject(same path) error = %v, want ErrProjectLibraryEntryConflict", err)
	}
}

func TestProjectLibraryReportsMissingEntries(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	ctx := context.Background()

	if _, err := store.Project(ctx, "missing"); !errors.Is(err, ErrProjectLibraryEntryNotFound) {
		t.Fatalf("Project() error = %v, want ErrProjectLibraryEntryNotFound", err)
	}
	if err := store.RefreshProject(ctx, ProjectSnapshotRefresh{
		ProjectID:   "missing",
		Snapshot:    testProjectEntry("unused", filepath.Join(t.TempDir(), "unused"), testTime(1)).Snapshot,
		RefreshedAt: testTime(2),
	}); !errors.Is(err, ErrProjectLibraryEntryNotFound) {
		t.Fatalf("RefreshProject() error = %v, want ErrProjectLibraryEntryNotFound", err)
	}
	if err := store.MarkProjectOpened(ctx, "missing", testTime(3)); !errors.Is(err, ErrProjectLibraryEntryNotFound) {
		t.Fatalf("MarkProjectOpened() error = %v, want ErrProjectLibraryEntryNotFound", err)
	}
	if err := store.DeleteProjectRegistration(ctx, "missing"); !errors.Is(err, ErrProjectLibraryEntryNotFound) {
		t.Fatalf("DeleteProjectRegistration() error = %v, want ErrProjectLibraryEntryNotFound", err)
	}
}

func TestProjectLibraryRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()
	valid := testProjectEntry("project-1", filepath.Join(t.TempDir(), "valid"), testTime(1))

	tests := []struct {
		name   string
		mutate func(*ProjectLibraryEntry)
	}{
		{name: "missing project ID", mutate: func(entry *ProjectLibraryEntry) { entry.ProjectID = " " }},
		{name: "relative root", mutate: func(entry *ProjectLibraryEntry) { entry.RootPath = "project" }},
		{name: "unclean root", mutate: func(entry *ProjectLibraryEntry) {
			entry.RootPath += string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(entry.RootPath)
		}},
		{name: "missing name", mutate: func(entry *ProjectLibraryEntry) { entry.Snapshot.DisplayName = "" }},
		{name: "invalid cover key", mutate: func(entry *ProjectLibraryEntry) { entry.Snapshot.CoverCacheKey = "covers/project.webp" }},
		{name: "invalid width", mutate: func(entry *ProjectLibraryEntry) { entry.Snapshot.Width = 0 }},
		{name: "negative episodes", mutate: func(entry *ProjectLibraryEntry) { entry.Snapshot.EpisodeCount = -1 }},
		{name: "missing project update", mutate: func(entry *ProjectLibraryEntry) { entry.Snapshot.ProjectUpdatedAt = time.Time{} }},
		{name: "missing added time", mutate: func(entry *ProjectLibraryEntry) { entry.AddedAt = time.Time{} }},
		{name: "missing refresh time", mutate: func(entry *ProjectLibraryEntry) { entry.RefreshedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := valid
			test.mutate(&entry)
			if err := store.AddProject(context.Background(), entry); err == nil {
				t.Fatal("AddProject() error = nil, want validation error")
			}
		})
	}

	if err := store.AddProject(nil, valid); err == nil {
		t.Fatal("AddProject(nil context) error = nil, want validation error")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Projects(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Projects(cancelled context) error = %v, want context.Canceled", err)
	}
}

func TestProjectsReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, filepath.Join(t.TempDir(), "studio.db"))
	defer store.Close()

	entries, err := store.Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects() error = %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("Projects() = %#v, want non-nil empty slice", entries)
	}
}

func newTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path, sharedsqlite.CreateNew)
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}
	return store
}

func testProjectEntry(projectID, rootPath string, base time.Time) ProjectLibraryEntry {
	lastOpenedAt := base.Add(2 * time.Second)
	return ProjectLibraryEntry{
		ProjectID: projectID,
		RootPath:  rootPath,
		Snapshot: ProjectSnapshot{
			DisplayName:      "Project " + projectID,
			CoverCacheKey:    projectID + ".webp",
			Width:            720,
			Height:           1280,
			EpisodeCount:     2,
			ProjectUpdatedAt: base.Add(time.Second),
		},
		AddedAt:      base,
		LastOpenedAt: &lastOpenedAt,
		RefreshedAt:  base.Add(time.Second),
	}
}

func testTime(offset int) time.Time {
	return time.Date(2026, time.September, 2, 10, 0, offset, 123_000_000, time.FixedZone("test", 8*60*60))
}

func projectIDs(entries []ProjectLibraryEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ProjectID)
	}
	return ids
}

func assertProjectEntry(t *testing.T, got, want ProjectLibraryEntry) {
	t.Helper()
	if got.ProjectID != want.ProjectID || got.RootPath != want.RootPath {
		t.Fatalf("entry identity = (%q, %q), want (%q, %q)", got.ProjectID, got.RootPath, want.ProjectID, want.RootPath)
	}
	if got.Snapshot.DisplayName != want.Snapshot.DisplayName ||
		got.Snapshot.CoverCacheKey != want.Snapshot.CoverCacheKey ||
		got.Snapshot.Width != want.Snapshot.Width ||
		got.Snapshot.Height != want.Snapshot.Height ||
		got.Snapshot.EpisodeCount != want.Snapshot.EpisodeCount {
		t.Fatalf("entry snapshot = %#v, want %#v", got.Snapshot, want.Snapshot)
	}
	if !got.Snapshot.ProjectUpdatedAt.Equal(want.Snapshot.ProjectUpdatedAt) ||
		!got.AddedAt.Equal(want.AddedAt) ||
		!got.RefreshedAt.Equal(want.RefreshedAt) {
		t.Fatalf("entry timestamps = %#v, want %#v", got, want)
	}
	if (got.LastOpenedAt == nil) != (want.LastOpenedAt == nil) ||
		(got.LastOpenedAt != nil && !got.LastOpenedAt.Equal(*want.LastOpenedAt)) {
		t.Fatalf("last opened at = %v, want %v", got.LastOpenedAt, want.LastOpenedAt)
	}
}
