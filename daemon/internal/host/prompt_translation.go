package host

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	"github.com/shotforward/shotforward/daemon/internal/config"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/prompttranslation"
	"github.com/shotforward/shotforward/daemon/internal/shotforwardcloud"
)

type textPromptTranslator interface {
	Translate(context.Context, string, string) (string, error)
}

// Initialized before the HTTP listener is served. This breaks the construction
// dependency on project MCP without giving translation access to that MCP.
type generationPromptTranslator struct {
	selection agentapp.RuntimeSelectionController
	models    agentapp.RuntimeModelSelector
	workers   map[agentapp.RuntimeKind]textPromptTranslator
}

func (t *generationPromptTranslator) Translate(ctx context.Context, options projectapp.GenerationPromptTranslationOptions, prompt string) (string, error) {
	kind := agentapp.RuntimeKind(options.RuntimeKind)
	if t.selection == nil || t.models == nil || (kind != agentapp.RuntimeUserCodex && kind != agentapp.RuntimeShotForward) || strings.TrimSpace(options.ModelID) == "" {
		return "", projectapp.ErrPromptTranslationUnavailable
	}
	active, err := t.selection.ActiveRuntime(ctx)
	if err != nil {
		return "", projectapp.ErrPromptTranslationUnavailable
	}
	if active != kind {
		return "", projectapp.ErrPromptTranslationSelectionChanged
	}
	models, err := t.models.ModelsFor(ctx, kind)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", projectapp.ErrPromptTranslationUnavailable
	}
	validModel := false
	for _, model := range models {
		if model.ID == options.ModelID && !model.Hidden && (model.RuntimeKind == "" || model.RuntimeKind == kind) {
			validModel = true
			break
		}
	}
	if !validModel {
		return "", projectapp.ErrPromptTranslationSelectionChanged
	}
	worker := t.workers[kind]
	if worker == nil {
		return "", projectapp.ErrPromptTranslationUnavailable
	}
	translated, err := worker.Translate(ctx, options.ModelID, prompt)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", projectapp.ErrPromptTranslationTimeout
		}
		var invalid *prompttranslation.ValidationError
		if errors.As(err, &invalid) {
			return "", projectapp.ErrPromptTranslationInvalid
		}
		var business *codexruntime.TextWorkerFailure
		if errors.As(err, &business) {
			switch business.Code {
			case "shotforward_negative_balance", "shotforward_insufficient_points", "shotforward_wallet_not_found":
				return "", &projectapp.GenerationPromptTranslationFailure{Code: business.Code}
			}
		}
		return "", projectapp.ErrPromptTranslationFailed
	}
	// Do not finish a submission against an endpoint the user has just changed.
	if active, err := t.selection.ActiveRuntime(ctx); err != nil || active != kind {
		return "", projectapp.ErrPromptTranslationSelectionChanged
	}
	return translated, nil
}

func (t *generationPromptTranslator) initialize(cfg config.Config, directories platformpaths.ApplicationDirs, runtime agentapp.Runtime, manager *shotforwardcloud.Manager, logger *slog.Logger) (func(context.Context) error, error) {
	t.selection, _ = runtime.(agentapp.RuntimeSelectionController)
	t.models, _ = runtime.(agentapp.RuntimeModelSelector)
	t.workers = make(map[agentapp.RuntimeKind]textPromptTranslator)
	var workers []*codexruntime.TextWorker
	closeWorkers := func(ctx context.Context) error {
		var err error
		for _, worker := range workers {
			err = errors.Join(err, worker.Close(ctx))
		}
		return err
	}
	for _, kind := range []agentapp.RuntimeKind{agentapp.RuntimeUserCodex, agentapp.RuntimeShotForward} {
		if kind == agentapp.RuntimeShotForward && cfg.EmbeddedCodexExecutable == "" {
			continue
		}
		directory := filepath.Join(codexWorkingDirectory(directories), "text-workers", string(kind))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return closeWorkers, err
		}
		options := codexruntime.TextWorkerOptions{ConfiguredExecutable: cfg.CodexExecutable, WorkingDirectory: directory, Environment: os.Environ(), ClientVersion: "shotforwardd", MinimumVersion: codexruntime.MinimumSupportedUserCodexVersion, NetworkSettingsPath: filepath.Join(directories.Data, "codex-network.json"), Logger: logger}
		if kind == agentapp.RuntimeShotForward {
			embeddedHome := filepath.Join(directories.Data, "shotforward-codex-home")
			options.ConfiguredExecutable = cfg.EmbeddedCodexExecutable
			options.MinimumVersion = ""
			options.NetworkSettingsPath = ""
			options.Environment = isolatedEnvironment(os.Environ(), map[string]string{"CODEX_HOME": embeddedHome, "SHOTFORWARD_EMBEDDED_API_KEY": cfg.CloudProxyToken})
			options.AdditionalConfig = shotForwardCodexProviderConfig(cfg)
			options.DirectToolModelCatalogPath = filepath.Join(embeddedHome, "model-catalog.json")
			options.BeginTurn = manager.BeginAgentTurn
		}
		worker, err := codexruntime.NewTextWorker(options)
		if err != nil {
			return closeWorkers, err
		}
		workers = append(workers, worker)
		t.workers[kind] = prompttranslation.New(worker)
	}
	return closeWorkers, nil
}

func shotForwardCodexProviderConfig(cfg config.Config) []string {
	return []string{
		`model_provider="shotforward"`,
		`model_providers.shotforward.name="ShotForward"`,
		`model_providers.shotforward.base_url=` + strconv.Quote(cfg.CloudProxyURL),
		`model_providers.shotforward.env_key="SHOTFORWARD_EMBEDDED_API_KEY"`,
		`model_providers.shotforward.wire_api="responses"`,
	}
}
