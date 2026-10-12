package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateAndOpenExistingDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "项目 # %.db")
	database, err := Open(context.Background(), Options{
		Path: path,
		Kind: Project,
		Mode: CreateNew,
	})
	if err != nil {
		t.Fatalf("Open(CreateNew) error = %v", err)
	}

	if got := database.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
	assertConnectionPolicy(t, database)
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != privateDatabaseMode {
		t.Errorf("database permissions = %o, want %o", info.Mode().Perm(), privateDatabaseMode)
	}

	reopened, err := Open(context.Background(), Options{
		Path: path,
		Kind: Project,
		Mode: OpenExisting,
	})
	if err != nil {
		t.Fatalf("Open(OpenExisting) error = %v", err)
	}
	defer reopened.Close()
	assertConnectionPolicy(t, reopened)
}

func TestOpenUsesDistinctApplicationIDs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		kind Kind
		want int64
	}{
		{name: "studio", kind: Studio, want: studioApplicationID},
		{name: "project", kind: Project, want: projectApplicationID},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.name+".db")
			database, err := Open(context.Background(), Options{Path: path, Kind: test.kind, Mode: CreateNew})
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer database.Close()

			var got int64
			if err := database.QueryRow("PRAGMA application_id").Scan(&got); err != nil {
				t.Fatalf("read application_id: %v", err)
			}
			if got != test.want {
				t.Fatalf("application_id = %d, want %d", got, test.want)
			}
		})
	}
}

func TestCreateNewNeverReplacesExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "project.db")
	first, err := Open(context.Background(), Options{Path: path, Kind: Project, Mode: CreateNew})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(context.Background(), Options{Path: path, Kind: Project, Mode: CreateNew})
	if !errors.Is(err, ErrDatabaseExists) {
		t.Fatalf("second Open() error = %v, want ErrDatabaseExists", err)
	}
}

func TestOpenExistingNeverCreatesMissingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := Open(context.Background(), Options{Path: path, Kind: Project, Mode: OpenExisting})
	if !errors.Is(err, ErrDatabaseNotFound) {
		t.Fatalf("Open() error = %v, want ErrDatabaseNotFound", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing database was unexpectedly created: %v", statErr)
	}
}

func TestOpenRejectsWrongDatabaseKind(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "studio.db")
	database, err := Open(context.Background(), Options{Path: path, Kind: Studio, Mode: CreateNew})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(context.Background(), Options{Path: path, Kind: Project, Mode: OpenExisting})
	if !errors.Is(err, ErrDatabaseKindMismatch) {
		t.Fatalf("Open() error = %v, want ErrDatabaseKindMismatch", err)
	}
}

func TestOpenRejectsUnidentifiedSQLiteDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "unknown.db")
	database, err := Open(context.Background(), Options{Path: path, Kind: Studio, Mode: CreateNew})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := database.Exec("PRAGMA application_id = 0"); err != nil {
		t.Fatalf("clear application ID: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(context.Background(), Options{Path: path, Kind: Studio, Mode: OpenExisting})
	if !errors.Is(err, ErrDatabaseKindMismatch) {
		t.Fatalf("Open() error = %v, want ErrDatabaseKindMismatch", err)
	}
}

func TestOpenRejectsCorruptFileWithoutReplacingIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "corrupt.db")
	want := []byte("this is not a SQLite database")
	if err := os.WriteFile(path, want, privateDatabaseMode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Open(context.Background(), Options{Path: path, Kind: Project, Mode: OpenExisting}); err == nil {
		t.Fatal("Open() error = nil, want an error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("corrupt file was modified: got %q, want %q", got, want)
	}
}

func TestOpenRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	absolute := filepath.Join(t.TempDir(), "database.db")
	tests := []struct {
		name    string
		ctx     context.Context
		options Options
	}{
		{name: "nil context", options: Options{Path: absolute, Kind: Project, Mode: CreateNew}},
		{name: "relative path", ctx: context.Background(), options: Options{Path: "project.db", Kind: Project, Mode: CreateNew}},
		{name: "unknown kind", ctx: context.Background(), options: Options{Path: absolute, Kind: 99, Mode: CreateNew}},
		{name: "unknown mode", ctx: context.Background(), options: Options{Path: absolute, Kind: Project, Mode: 99}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Open(test.ctx, test.options); err == nil {
				t.Fatal("Open() error = nil, want an error")
			}
		})
	}
}

func TestOpenRejectsSymlinkDatabaseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	t.Parallel()

	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	database, err := Open(context.Background(), Options{Path: target, Kind: Project, Mode: CreateNew})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	link := filepath.Join(directory, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	_, err = Open(context.Background(), Options{Path: link, Kind: Project, Mode: OpenExisting})
	if !errors.Is(err, ErrInvalidDatabaseFile) {
		t.Fatalf("Open() error = %v, want ErrInvalidDatabaseFile", err)
	}
}

func TestFailedCreateRemovesReservedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cancelled.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, Options{Path: path, Kind: Project, Mode: CreateNew})
	if err == nil {
		t.Fatal("Open() error = nil, want an error")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("reserved database was not removed: %v", statErr)
	}
}

func assertConnectionPolicy(t *testing.T, database interface {
	QueryRow(query string, args ...any) *sql.Row
}) {
	t.Helper()

	settings := []struct {
		pragma string
		want   int64
	}{
		{pragma: "foreign_keys", want: 1},
		{pragma: "busy_timeout", want: busyTimeoutMS},
		{pragma: "synchronous", want: 3},
		{pragma: "trusted_schema", want: 0},
	}
	for _, setting := range settings {
		var got int64
		if err := database.QueryRow("PRAGMA " + setting.pragma).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", setting.pragma, err)
		}
		if got != setting.want {
			t.Errorf("%s = %d, want %d", setting.pragma, got, setting.want)
		}
	}

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		t.Errorf("journal_mode = %q, want delete", journalMode)
	}
}
