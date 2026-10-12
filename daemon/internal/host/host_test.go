package host

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	"github.com/shotforward/shotforward/daemon/internal/config"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
)

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	root := t.TempDir()
	listener := &closedListener{address: testAddress("127.0.0.1:12345")}
	options := Options{
		Directories: platformpaths.ApplicationDirs{
			Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"),
			Logs: filepath.Join(root, "data", "logs"), Runtime: filepath.Join(root, "data", "runtime"),
		},
		Listener: listener, BootstrapWriter: &bytes.Buffer{},
	}
	if err := RunWithOptions(ctx, config.Default(), logger, options); err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if !listener.closed {
		t.Fatal("listener was not closed")
	}
	if strings.Contains(output.String(), "pairing") {
		t.Fatalf("logs contain bootstrap material: %q", output.String())
	}
}

type closedListener struct {
	address net.Addr
	closed  bool
}

func (l *closedListener) Accept() (net.Conn, error) { return nil, errors.New("listener closed") }
func (l *closedListener) Close() error              { l.closed = true; return nil }
func (l *closedListener) Addr() net.Addr            { return l.address }

type testAddress string

func (a testAddress) Network() string { return "tcp" }
func (a testAddress) String() string  { return string(a) }

func TestRunValidatesDependencies(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if err := Run(nil, config.Default(), logger, &bytes.Buffer{}); err == nil {
		t.Fatal("Run(nil, config, logger) error = nil, want an error")
	}
	if err := Run(context.Background(), config.Default(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("Run(context, config, nil) error = nil, want an error")
	}
	if err := Run(context.Background(), config.Default(), logger, nil); err == nil {
		t.Fatal("Run(context, config, logger, nil) error = nil, want an error")
	}
	invalid := config.Default()
	invalid.ListenAddress = ":0"
	if err := Run(context.Background(), invalid, logger, &bytes.Buffer{}); err == nil {
		t.Fatal("Run(context, invalid config, logger) error = nil, want an error")
	}
}

func TestRunWithOptionsRejectsNonLoopbackListener(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	listener := &closedListener{address: testAddress("0.0.0.0:12345")}
	err := RunWithOptions(context.Background(), config.Default(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Options{
		Directories: platformpaths.ApplicationDirs{
			Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"),
			Logs: filepath.Join(root, "data", "logs"), Runtime: filepath.Join(root, "data", "runtime"),
		},
		Listener: listener, BootstrapWriter: &bytes.Buffer{},
	})
	if err == nil || !listener.closed {
		t.Fatalf("RunWithOptions() error = %v, listener closed = %v", err, listener.closed)
	}
}

func TestAgentSessionWorkspacesStayInsideCodexWorkingDirectory(t *testing.T) {
	t.Parallel()

	directories := platformpaths.ApplicationDirs{Runtime: filepath.Join(t.TempDir(), "runtime")}
	codexRoot := codexWorkingDirectory(directories)
	workspaceRoot := filepath.Join(codexRoot, "agent-sessions")
	relative, err := filepath.Rel(codexRoot, workspaceRoot)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("Agent Session workspace %q must be contained by Codex root %q", workspaceRoot, codexRoot)
	}
}

func TestDeferredProviderConfigurationForwardsModelSelection(t *testing.T) {
	t.Parallel()

	configuration := &deferredProviderConfiguration{target: modelProviderConfiguration{}}
	record, secret, err := configuration.RuntimeProviderModel(
		context.Background(),
		project.GeneratorVideoWanOfficial,
		"wan3.0-video",
	)
	if err != nil || record.ModelID != "wan3.0-video" || secret != "secret" {
		t.Fatalf("RuntimeProviderModel() = %#v, %q, %v", record, secret, err)
	}
}

type modelProviderConfiguration struct{}

func (modelProviderConfiguration) RuntimeProvider(context.Context, project.GeneratorID) (providerconfig.Record, string, error) {
	return providerconfig.Record{}, "secret", nil
}

func (modelProviderConfiguration) RuntimeProviderModel(_ context.Context, id project.GeneratorID, modelID string) (providerconfig.Record, string, error) {
	if id != project.GeneratorVideoWanOfficial {
		return providerconfig.Record{}, "", errors.New("unexpected Generator")
	}
	return providerconfig.Record{Profile: providerconfig.Profile{GeneratorID: id, ModelID: modelID}}, "secret", nil
}
