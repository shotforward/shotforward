package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	"github.com/shotforward/shotforward/daemon/internal/config"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
)

func TestCheckCodexCapabilitiesOnStartupChecksLanguageAndImage(t *testing.T) {
	runtime := &hostTestRuntime{}
	providers := &hostTestProviderConfigurations{profile: providerconfig.Profile{
		GeneratorID: project.GeneratorImageCodex,
		Health:      providerconfig.HealthReady,
	}}

	checkCodexCapabilitiesOnStartup(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime,
		providers,
	)

	if calls := runtime.snapshotCalls.Load(); calls != 1 {
		t.Fatalf("language capability checks = %d, want 1", calls)
	}
	if calls := providers.checkCalls.Load(); calls != 1 {
		t.Fatalf("image capability checks = %d, want 1", calls)
	}
}

type startupRuntime struct {
	*hostTestRuntime
	snapshot func(context.Context, agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error)
	models   func(context.Context, agentapp.RuntimeKind) ([]agentapp.Model, error)
	threads  atomic.Int32
	turns    atomic.Int32
	selected atomic.Int32
}

func (r *startupRuntime) SnapshotFor(ctx context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
	return r.snapshot(ctx, kind)
}
func (r *startupRuntime) ModelsFor(ctx context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	return r.models(ctx, kind)
}
func (r *startupRuntime) ActiveRuntime(context.Context) (agentapp.RuntimeKind, error) {
	r.selected.Add(1)
	return agentapp.RuntimeUserCodex, nil
}
func (r *startupRuntime) SelectRuntime(context.Context, agentapp.RuntimeKind) error {
	r.selected.Add(1)
	return nil
}
func (r *startupRuntime) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	r.threads.Add(1)
	return agentapp.Thread{}, errors.New("startup must not create a Thread")
}
func (r *startupRuntime) StartTurn(context.Context, agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	r.turns.Add(1)
	return agentapp.RuntimeTurn{}, errors.New("startup must not create a Turn")
}

func awaitStartup(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background startup check did not complete")
	}
}

func TestStartupWarmsBothRuntimesIndependentlyWithoutSelectingOrCreatingWork(t *testing.T) {
	t.Parallel()
	localStarted, cloudModels, releaseLocal := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(releaseLocal)
	runtime := &startupRuntime{hostTestRuntime: &hostTestRuntime{}}
	runtime.snapshot = func(ctx context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
		if kind == agentapp.RuntimeUserCodex {
			close(localStarted)
			select {
			case <-releaseLocal:
			case <-ctx.Done():
				return agentapp.RuntimeSnapshot{}, ctx.Err()
			}
			return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeNotInstalled}, errors.New("local executable absent")
		}
		return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}, nil
	}
	runtime.models = func(_ context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
		if kind != agentapp.RuntimeShotForward {
			t.Error("failed local Runtime should not attempt model enumeration")
			return nil, errors.New("local models are unavailable")
		}
		close(cloudModels)
		return []agentapp.Model{{ID: "ready-cloud-model"}}, nil
	}
	providers := &hostTestProviderConfigurations{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		checkCodexCapabilitiesOnStartup(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), runtime, providers)
	}()
	awaitStartup(t, localStarted)
	awaitStartup(t, cloudModels)
	select {
	case <-done:
		t.Fatal("slow Runtime was not still independently warming")
	default:
	}
	select {
	case releaseLocal <- struct{}{}:
	case <-time.After(2 * time.Second):
		t.Fatal("local check was not waiting independently")
	}
	awaitStartup(t, done)
	if runtime.selected.Load() != 0 || runtime.threads.Load() != 0 || runtime.turns.Load() != 0 || runtime.snapshotCalls.Load() != 0 || providers.checkCalls.Load() != 1 {
		t.Fatal("warmup selected a Runtime, created real work, or skipped the image check")
	}
}

func TestStartupWarmupHonorsHostCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan agentapp.RuntimeKind, 2)
	runtime := &startupRuntime{hostTestRuntime: &hostTestRuntime{}}
	runtime.snapshot = func(ctx context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
		started <- kind
		<-ctx.Done()
		return agentapp.RuntimeSnapshot{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		checkCodexCapabilitiesOnStartup(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime, &hostTestProviderConfigurations{})
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("both Runtime checks did not start")
		}
	}
	cancel()
	awaitStartup(t, done)
}

type startupModelRefresher struct {
	refresh func(context.Context) ([]agentapp.Model, bool, error)
}

func (r startupModelRefresher) RefreshAgentModelsIfSignedIn(ctx context.Context) ([]agentapp.Model, bool, error) {
	return r.refresh(ctx)
}

func TestBackgroundCatalogRefreshHasBoundedContextAndStopsWithHost(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	refresher := startupModelRefresher{refresh: func(ctx context.Context) ([]agentapp.Model, bool, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > agentModelRefreshTimeout || time.Until(deadline) <= 0 {
			t.Error("background refresh did not receive its own bounded deadline")
		}
		close(started)
		<-ctx.Done()
		return nil, true, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgentModelRefreshLoop(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), refresher, ticks)
	}()
	ticks <- time.Now()
	awaitStartup(t, started)
	cancel()
	awaitStartup(t, done)
}

func TestHostBootstrapAndHTTPDoNotWaitForRuntimeWarmup(t *testing.T) {
	root := t.TempDir()
	directories := platformpaths.ApplicationDirs{Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "data", "logs"), Runtime: filepath.Join(root, "data", "runtime")}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	started := make(chan agentapp.RuntimeKind, 2)
	runtime := &startupRuntime{hostTestRuntime: &hostTestRuntime{}}
	runtime.snapshot = func(ctx context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
		started <- kind
		<-ctx.Done()
		return agentapp.RuntimeSnapshot{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunWithOptions(ctx, config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
			Directories: directories, Listener: listener, BootstrapWriter: writer,
			GenerationCatalog: hostTestGenerationCatalog{}, Generators: hostTestGeneratorRegistry{generator: &hostTestGenerator{}}, MediaFiles: hostTestMediaFiles{}, ExportProcessor: hostTestExportProcessor{}, ProviderConfigurations: &hostTestProviderConfigurations{},
			RuntimeFactory: func(config.Config, platformpaths.ApplicationDirs, agentmcp.Credential) (RuntimeResource, error) {
				return RuntimeResource{Runtime: runtime, Close: runtime.Close}, nil
			},
		})
	}()
	announcement := make(chan bootstrapAnnouncement, 1)
	go func() {
		var value bootstrapAnnouncement
		if err := json.NewDecoder(reader).Decode(&value); err == nil {
			announcement <- value
		}
	}()
	var address string
	select {
	case value := <-announcement:
		address = value.Address
	case err := <-result:
		t.Fatalf("host stopped before bootstrap: %v", err)
	// Allow database initialization on slower CI filesystems. The Runtime mock
	// remains blocked until cancellation, so a synchronous warmup still fails.
	case <-time.After(30 * time.Second):
		t.Fatal("bootstrap did not complete while Runtime warmup remained blocked")
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("background Runtime warmup did not start")
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(address + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP health while warming = %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host did not cancel and join background warmup")
	}
}
