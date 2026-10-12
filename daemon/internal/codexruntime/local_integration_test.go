package codexruntime

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

// This opt-in test performs only local app-server discovery and capability
// reads. It never starts a Thread or a model Turn.
func TestInstalledCodexRuntimeContract(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_LOCAL_CODEX") != "1" {
		t.Skip("set SHOTFORWARD_TEST_LOCAL_CODEX=1 to probe the installed Codex")
	}
	runtime, err := NewSupervisor(Options{
		ConfiguredExecutable: os.Getenv("SHOTFORWARD_TEST_CODEX_EXECUTABLE"),
		WorkingDirectory:     t.TempDir(),
		ClientVersion:        "integration-test",
		RequestTimeout:       30 * time.Second,
		Logger:               slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := runtime.Close(ctx); closeErr != nil {
			t.Errorf("close Runtime: %v", closeErr)
		}
	}()

	snapshot, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("probe installed Codex: %v (snapshot: %+v)", err, snapshot)
	}
	if snapshot.Health != agentapp.RuntimeReady && snapshot.Health != agentapp.RuntimeLoginRequired {
		t.Fatalf("unexpected Runtime health: %+v", snapshot)
	}
	if !snapshot.SafeExecutionAvailable {
		t.Fatalf("installed Codex sandbox is unavailable: %+v", snapshot)
	}
	if snapshot.Health == agentapp.RuntimeReady {
		models, err := runtime.Models(context.Background())
		if err != nil || len(models) == 0 {
			t.Fatalf("read installed Codex models: count=%d error=%v", len(models), err)
		}
	}
}
