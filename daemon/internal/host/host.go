// Package host owns construction and lifetime of the local ShotForward daemon.
package host

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agenthttp"
	"github.com/shotforward/shotforward/daemon/internal/agentimage"
	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	"github.com/shotforward/shotforward/daemon/internal/agentscheduler"
	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	"github.com/shotforward/shotforward/daemon/internal/agenttemplatehttp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	"github.com/shotforward/shotforward/daemon/internal/config"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/exportjob"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	"github.com/shotforward/shotforward/daemon/internal/localhttp"
	"github.com/shotforward/shotforward/daemon/internal/mediafiles"
	"github.com/shotforward/shotforward/daemon/internal/minimaxh3"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	"github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/projecthttp"
	"github.com/shotforward/shotforward/daemon/internal/providerhttp"
	"github.com/shotforward/shotforward/daemon/internal/runtimerouter"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
	"github.com/shotforward/shotforward/daemon/internal/securecredentials"
	"github.com/shotforward/shotforward/daemon/internal/seedance"
	"github.com/shotforward/shotforward/daemon/internal/shotforwardcloud"
	"github.com/shotforward/shotforward/daemon/internal/taskhttp"
	"github.com/shotforward/shotforward/daemon/internal/tasksse"
	"github.com/shotforward/shotforward/daemon/internal/wan"
	"github.com/shotforward/shotforward/daemon/internal/webui"
)

const (
	shutdownTimeout           = 5 * time.Second
	startupCodexCheckTimeout  = 30 * time.Second
	agentModelRefreshInterval = 30 * time.Second
	agentModelRefreshTimeout  = 15 * time.Second
	backgroundProcessCount    = 4
)

var ErrAlreadyRunning = errors.New("ShotForward daemon is already running")

type RuntimeResource struct {
	Runtime agentapp.Runtime
	Close   func(context.Context) error
}

type RuntimeFactory func(config.Config, platformpaths.ApplicationDirs, agentmcp.Credential) (RuntimeResource, error)

// Options supplies process-owned resources for integration tests. Production
// callers use Run, which resolves standard per-user paths and opens a listener.
type Options struct {
	Directories            platformpaths.ApplicationDirs
	Listener               net.Listener
	BootstrapWriter        io.Writer
	Random                 io.Reader
	Clock                  projectapp.Clock
	RuntimeFactory         RuntimeFactory
	GenerationCatalog      projectapp.GenerationCapabilityCatalog
	PromptTranslator       projectapp.GenerationPromptTranslator
	Generators             generation.GeneratorRegistry
	MediaFiles             projectapp.MediaFileProcessor
	ExportProcessor        exportjob.Processor
	ProviderConfigurations providerhttp.Service
}

type bootstrapAnnouncement struct {
	Address     string `json:"address"`
	PairingCode string `json:"pairing_code"`
	InstanceID  string `json:"instance_id"`
}

// Run resolves production resources and serves until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger, bootstrapWriter io.Writer) error {
	if ctx == nil || logger == nil || bootstrapWriter == nil {
		return errors.New("run host: context, logger, and bootstrap writer are required")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("run host: %w", err)
	}
	directories, err := platformpaths.ResolveApplicationDirs()
	if err != nil {
		return fmt.Errorf("run host: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("run host: listen: %w", err)
	}
	return RunWithOptions(ctx, cfg, logger, Options{Directories: directories, Listener: listener, BootstrapWriter: bootstrapWriter})
}

// RunWithOptions constructs all adapters, publishes the one-time bootstrap
// announcement, and closes resources in reverse dependency order.
func RunWithOptions(ctx context.Context, cfg config.Config, logger *slog.Logger, options Options) (returnErr error) {
	if ctx == nil || logger == nil || options.Listener == nil || options.BootstrapWriter == nil || options.Directories.Validate() != nil {
		return errors.New("run host: invalid dependencies")
	}
	if !isLoopbackListener(options.Listener) {
		_ = options.Listener.Close()
		return errors.New("run host: listener must use a literal loopback address")
	}
	if err := cfg.Validate(); err != nil {
		_ = options.Listener.Close()
		return fmt.Errorf("run host: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = options.Listener.Close()
		return nil
	}
	if err := options.Directories.Ensure(); err != nil {
		_ = options.Listener.Close()
		return err
	}
	randomSource := &lockedReader{reader: options.Random}
	if options.Random == nil {
		randomSource.reader = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = projectapp.UTCClock{}
	}
	credentialStore := securecredentials.NewStore(options.Directories.Data)
	cloudHTTPClient := shotforwardcloud.NewNetworkHTTPClient(filepath.Join(options.Directories.Data, "codex-network.json"))
	cloudManager, err := shotforwardcloud.NewManagerWithWebURL(cfg.CloudAPIURL, cfg.CloudWebURL, credentialStore, cloudHTTPClient)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	cloudHandler, err := shotforwardcloud.NewHandler(cloudManager, logger)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	taskEvents, err := tasksse.NewBroker(clock, randomSource)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	instanceID, err := randomIdentifier(randomSource)
	if err != nil {
		_ = options.Listener.Close()
		return fmt.Errorf("run host: create instance identity: %w", err)
	}
	hostAddress := options.Listener.Addr().String()
	origin := "http://" + hostAddress
	cloudProxyToken, err := randomIdentifier(randomSource)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	cfg.CloudProxyURL = origin + "/api/v1/cloud/openai"
	cfg.CloudProxyToken = cloudProxyToken

	descriptor, err := acquireRendezvous(ctx, options.Directories.Runtime, rendezvousRecord{Address: hostAddress, InstanceID: instanceID, ProcessID: os.Getpid()})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, descriptor.Close()) }()

	studioStore, err := openStudioStore(ctx, filepath.Join(options.Directories.Data, "studio.db"))
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, studioStore.Close()) }()
	runtimeSelectionStore, err := studio.NewApplicationRuntimeSelectionStore(studioStore)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}

	registry := projectapp.NewProjectStoreRegistry()
	defer func() { returnErr = errors.Join(returnErr, registry.CloseAll()) }()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	ids := projectapp.NewSecureIDGenerator()
	mediaProcessor := options.MediaFiles
	if mediaProcessor == nil {
		value, processorErr := mediafiles.NewProcessor("ffprobe", "ffmpeg")
		if processorErr != nil {
			_ = options.Listener.Close()
			return processorErr
		}
		mediaProcessor = value
	}
	generatorRegistry := options.Generators
	generationCatalog := options.GenerationCatalog
	if generatorRegistry == nil && generationCatalog == nil {
		workingDirectory := filepath.Join(options.Directories.Runtime, "codex-media")
		if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
			_ = options.Listener.Close()
			return fmt.Errorf("create Codex image Generator directory: %w", err)
		}
		if err := os.Chmod(workingDirectory, 0o700); err != nil {
			_ = options.Listener.Close()
			return fmt.Errorf("secure Codex image Generator directory: %w", err)
		}
		codexImages, imageErr := codexruntime.NewImageGenerator(codexruntime.ImageGeneratorOptions{
			ConfiguredExecutable: cfg.CodexExecutable,
			WorkingDirectory:     workingDirectory,
			Environment:          os.Environ(),
			ClientVersion:        "shotforwardd",
			Logger:               logger,
		})
		if imageErr != nil {
			_ = options.Listener.Close()
			return imageErr
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			returnErr = errors.Join(returnErr, codexImages.Close(closeCtx))
		}()
		generatorRegistry = codexruntime.ImageGeneratorRegistry{Provider: codexImages}
		generationCatalog = codexruntime.ImageGenerationCatalog{Provider: codexImages}
		cloudImage, cloudErr := shotforwardcloud.NewGenerator(cloudManager, project.GeneratorImageShotForward, filepath.Join(options.Directories.Runtime, "shotforward-cloud-images"))
		if cloudErr != nil {
			_ = options.Listener.Close()
			return cloudErr
		}
		cloudVideo, cloudErr := shotforwardcloud.NewGenerator(cloudManager, project.GeneratorVideoH3ShotForward, filepath.Join(options.Directories.Runtime, "shotforward-cloud-videos"))
		if cloudErr != nil {
			_ = options.Listener.Close()
			return cloudErr
		}
		generatorRegistry = combinedGeneratorRegistry{generatorRegistry, shotforwardcloud.Registry{Image: cloudImage, Video: cloudVideo}}
		generationCatalog = combinedGenerationCatalog{base: generationCatalog, cloud: shotforwardcloud.Catalog{Manager: cloudManager}}
	}
	if generatorRegistry == nil {
		generatorRegistry = emptyGeneratorRegistry{}
	}
	if generationCatalog == nil {
		generationCatalog = emptyGenerationCatalog{}
	}
	providerConfigurations := options.ProviderConfigurations
	if providerConfigurations == nil {
		h3ChinaDirectory := filepath.Join(options.Directories.Runtime, "minimax-h3-cn")
		h3GlobalDirectory := filepath.Join(options.Directories.Runtime, "minimax-h3-global")
		for name, directory := range map[string]string{"MiniMax H3 China": h3ChinaDirectory, "MiniMax H3 Global": h3GlobalDirectory} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				_ = options.Listener.Close()
				return fmt.Errorf("create %s Provider directory: %w", name, err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				_ = options.Listener.Close()
				return fmt.Errorf("secure %s Provider directory: %w", name, err)
			}
		}
		runtimeConfiguration := &deferredProviderConfiguration{}
		h3China, h3Err := minimaxh3.NewChina(minimaxh3.Options{Configuration: runtimeConfiguration, WorkingDirectory: h3ChinaDirectory})
		if h3Err != nil {
			_ = options.Listener.Close()
			return h3Err
		}
		h3Global, h3Err := minimaxh3.NewGlobal(minimaxh3.Options{Configuration: runtimeConfiguration, WorkingDirectory: h3GlobalDirectory})
		if h3Err != nil {
			_ = options.Listener.Close()
			return h3Err
		}
		seedanceChinaDirectory := filepath.Join(options.Directories.Runtime, "seedance-cn")
		seedanceGlobalDirectory := filepath.Join(options.Directories.Runtime, "seedance-global")
		for name, directory := range map[string]string{"Seedance China": seedanceChinaDirectory, "Seedance Global": seedanceGlobalDirectory} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				_ = options.Listener.Close()
				return fmt.Errorf("create %s Provider directory: %w", name, err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				_ = options.Listener.Close()
				return fmt.Errorf("secure %s Provider directory: %w", name, err)
			}
		}
		seedanceChina, seedanceErr := seedance.NewChina(seedance.Options{Configuration: runtimeConfiguration, WorkingDirectory: seedanceChinaDirectory, DeliveryBridge: cloudManager})
		if seedanceErr != nil {
			_ = options.Listener.Close()
			return seedanceErr
		}
		seedanceGlobal, seedanceErr := seedance.NewGlobal(seedance.Options{Configuration: runtimeConfiguration, WorkingDirectory: seedanceGlobalDirectory, DeliveryBridge: cloudManager})
		if seedanceErr != nil {
			_ = options.Listener.Close()
			return seedanceErr
		}
		wanDirectory := filepath.Join(options.Directories.Runtime, "wan")
		if err := os.MkdirAll(wanDirectory, 0o700); err != nil {
			_ = options.Listener.Close()
			return fmt.Errorf("create Wan 3.0 Provider directory: %w", err)
		}
		if err := os.Chmod(wanDirectory, 0o700); err != nil {
			_ = options.Listener.Close()
			return fmt.Errorf("secure Wan 3.0 Provider directory: %w", err)
		}
		wanProvider, wanErr := wan.New(wan.Options{Configuration: runtimeConfiguration, WorkingDirectory: wanDirectory})
		if wanErr != nil {
			_ = options.Listener.Close()
			return wanErr
		}
		profileStore, profileErr := studio.NewApplicationProviderProfileStore(studioStore)
		if profileErr != nil {
			_ = options.Listener.Close()
			return profileErr
		}
		configurationService, profileErr := providerconfig.NewService(providerconfig.Dependencies{Store: profileStore,
			Credentials: credentialStore, IDs: providerconfig.NewSecureIDGenerator(), Clock: clock,
			Checkers: providerconfig.MapCheckerRegistry{project.GeneratorImageCodex: generationCapabilityChecker{catalog: generationCatalog},
				project.GeneratorVideoH3China:        h3China,
				project.GeneratorVideoH3Official:     h3Global,
				project.GeneratorVideoSeedanceChina:  seedanceChina,
				project.GeneratorVideoSeedanceGlobal: seedanceGlobal,
				project.GeneratorVideoWanOfficial:    wanProvider}})
		if profileErr != nil {
			_ = options.Listener.Close()
			return profileErr
		}
		providerConfigurations = configurationService
		runtimeConfiguration.target = configurationService
		generatorRegistry = combinedGeneratorRegistry{generatorRegistry, minimaxh3.Registry{China: h3China, Global: h3Global}, seedance.Registry{China: seedanceChina, Global: seedanceGlobal}, wan.Registry{Provider: wanProvider}}
		generationCatalog = combinedGenerationCatalog{base: generationCatalog, h3: minimaxh3.Catalog{China: h3China, Global: h3Global}, seedance: seedance.Catalog{China: seedanceChina, Global: seedanceGlobal}, wan: wan.Catalog{Provider: wanProvider}}
	}
	providerConfigurations = shotforwardcloud.ProviderService{Base: providerConfigurations, Manager: cloudManager}
	generationScheduler, err := generation.NewScheduler(generation.SchedulerOptions{Registry: registry, Generators: generatorRegistry,
		MediaFiles: mediaProcessor, IDs: ids, Clock: clock, GlobalConcurrency: providerconfig.MaximumConcurrency, PollInterval: 2 * time.Second, Events: taskEvents})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	exportProcessor := options.ExportProcessor
	if exportProcessor == nil {
		exportProcessor, err = exportjob.NewFFmpegProcessor("ffmpeg")
		if err != nil {
			_ = options.Listener.Close()
			return err
		}
	}
	exportScheduler, err := exportjob.NewScheduler(exportjob.Options{Registry: registry, Processor: exportProcessor, Clock: clock, Events: taskEvents})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	promptTranslator := options.PromptTranslator
	var productionTranslator *generationPromptTranslator
	if promptTranslator == nil {
		productionTranslator = &generationPromptTranslator{}
		promptTranslator = productionTranslator
	}
	projectService, err := projectapp.NewService(projectapp.Dependencies{IDs: ids, Clock: clock,
		Workspace: platformpaths.ApplicationProjectWorkspace{}, Stores: projectstore.ApplicationStoreFactory{}, Library: library,
		Importer: scriptimport.FileImporter{}, Registry: registry, MediaFiles: mediaProcessor,
		GenerationCatalog: generationCatalog, GenerationQueue: generationScheduler, GenerationEvents: taskEvents, PromptTranslator: promptTranslator,
		ExportQueue: exportScheduler, ExportEvents: taskEvents, ProjectFiles: platformpaths.ApplicationProjectFileReader{}})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	registeredProjects, err := library.Projects(ctx)
	if err != nil {
		_ = options.Listener.Close()
		return fmt.Errorf("restore registered Projects: %w", err)
	}
	for _, entry := range registeredProjects {
		if _, restoreErr := projectService.RestoreProject(ctx, projectapp.RestoreProjectInput{ProjectID: entry.ProjectID, RootPath: entry.RootPath}); restoreErr != nil {
			logger.WarnContext(ctx, "registered Project could not be restored", "project_id", entry.ProjectID.String(), "error_code", "project_unavailable")
		}
	}
	resolver, err := projectstore.NewApplicationChangeSetResolver(registry)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	changeService, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, resolver)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	review, err := changesetapp.NewReviewCatalog(changeService, resolver)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	events, err := agentsse.NewBroker(clock, randomSource)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	agentImages, err := agentimage.NewService(agentimage.Options{Projects: projectService, Generators: generatorRegistry, Catalog: generationCatalog, IDs: ids})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	mcp, err := agentmcp.NewServer(agentmcp.Options{Queries: projectService, Drafts: changeService, DraftCleanup: changeService, Images: agentImages, Events: events, Random: randomSource})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	if err := mcp.Start(); err != nil {
		_ = options.Listener.Close()
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, mcp.Close(closeCtx))
	}()

	runtimeFactory := options.RuntimeFactory
	if runtimeFactory == nil {
		runtimeFactory = func(cfg config.Config, directories platformpaths.ApplicationDirs, credential agentmcp.Credential) (RuntimeResource, error) {
			return productionRuntime(cfg, directories, credential, runtimeSelectionStore, cloudManager, logger)
		}
	}
	runtimeResource, err := runtimeFactory(cfg, options.Directories, mcp.Credential())
	if err != nil || runtimeResource.Runtime == nil || runtimeResource.Close == nil {
		_ = options.Listener.Close()
		return errors.Join(errors.New("run host: create Agent Runtime"), err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, runtimeResource.Close(closeCtx))
	}()
	if productionTranslator != nil {
		closeWorkers, initErr := productionTranslator.initialize(cfg, options.Directories, runtimeResource.Runtime, cloudManager, logger)
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			returnErr = errors.Join(returnErr, closeWorkers(closeCtx))
		}()
		if initErr != nil {
			_ = options.Listener.Close()
			return fmt.Errorf("create prompt translation workers: %w", initErr)
		}
	}

	agentStore, err := studio.NewApplicationAgentStore(studioStore)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	workspaceRoot := filepath.Join(codexWorkingDirectory(options.Directories), "agent-sessions")
	workspace, err := agentsession.NewIsolatedWorkspace(workspaceRoot)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	sessionService, err := agentsession.NewService(agentsession.Options{Store: agentStore, Changes: resolver, Runtime: runtimeResource.Runtime,
		MCP: mcp, Workspace: workspace, IDs: agentapp.NewSecureIDGenerator(), Clock: clock, Media: projectService, Logger: logger})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	agentScheduler, err := agentscheduler.NewTurnScheduler(agentscheduler.SchedulerOptions{Store: studioStore, Runtime: runtimeResource.Runtime,
		MCP: mcp, IDs: agentapp.NewSecureIDGenerator(), Clock: clock, Events: events, Media: projectService})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	projectHandler, err := projecthttp.NewHandler(projecthttp.Options{Projects: projectService, Catalog: library})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	providerHandler, err := providerhttp.NewHandler(providerConfigurations)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	agentTemplateHandler, err := agenttemplatehttp.NewHandler(studioStore)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	taskHandler, err := taskhttp.NewHandler(registry, taskEvents)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	credentials, pairingCode, err := newHTTPCredentials(randomSource, cfg.PairingCode)
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	bearer, csrf := credentials.InternalTokens()
	var runtimeNetwork agenthttp.RuntimeNetwork
	if configurable, ok := runtimeResource.Runtime.(agenthttp.RuntimeNetwork); ok {
		runtimeNetwork = configurable
	}
	agentHandler, err := agenthttp.NewHandler(agenthttp.Options{Runtime: runtimeResource.Runtime, Network: runtimeNetwork, Sessions: sessionService, Turns: agentScheduler, ChangeSets: review, Events: events,
		Media: projectService, Security: agenthttp.Security{BearerToken: bearer, CSRFToken: csrf, AllowedHosts: []string{hostAddress}, AllowedOrigins: []string{origin}}, Random: randomSource, Logger: logger})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}
	application := routeApplication(projectHandler, agentHandler, taskHandler, providerHandler, agentTemplateHandler, cloudHandler)
	gateway, err := localhttp.NewGateway(localhttp.GatewayOptions{Application: application, Static: webui.NewHandler(), Credentials: credentials, AllowedHost: hostAddress, AllowedOrigin: origin, InstanceID: instanceID, ServiceBearer: cloudProxyToken})
	if err != nil {
		_ = options.Listener.Close()
		return err
	}

	server := &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 * 1024}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	serverErrors := make(chan error, 1)
	go func() {
		err := server.Serve(options.Listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErrors <- err
	}()
	type processResult struct {
		name string
		err  error
	}
	processResults := make(chan processResult, backgroundProcessCount)
	go func() { processResults <- processResult{name: "Agent scheduler", err: agentScheduler.Run(runCtx)} }()
	go func() {
		processResults <- processResult{name: "Generation scheduler", err: generationScheduler.Run(runCtx)}
	}()
	go func() { processResults <- processResult{name: "Export scheduler", err: exportScheduler.Run(runCtx)} }()
	go func() {
		runAgentCapabilityMaintenance(runCtx, logger, runtimeResource.Runtime, providerConfigurations, cloudManager)
		processResults <- processResult{name: "Agent capability maintenance"}
	}()

	announcement := bootstrapAnnouncement{Address: origin, PairingCode: pairingCode, InstanceID: instanceID}
	if err := json.NewEncoder(options.BootstrapWriter).Encode(announcement); err != nil {
		cancelRun()
		_ = server.Close()
		for range backgroundProcessCount {
			<-processResults
		}
		<-serverErrors
		return fmt.Errorf("run host: write bootstrap announcement: %w", err)
	}
	logger.InfoContext(ctx, "ShotForward daemon started", "address", origin, "log_level", cfg.LogLevel.String(), "instance_id", instanceID,
		"cloud_api_url", cfg.CloudAPIURL, "cloud_web_url", cfg.CloudWebURL)

	serverStopped, stoppedProcesses := false, 0
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		serverStopped = true
		if err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("serve localhost HTTP: %w", err))
		}
		cancelRun()
	case result := <-processResults:
		stoppedProcesses = 1
		if result.err != nil && !(runCtx.Err() != nil && errors.Is(result.err, runCtx.Err())) {
			returnErr = errors.Join(returnErr, fmt.Errorf("run %s: %w", result.name, result.err))
		}
		cancelRun()
	}

	cancelRun()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := server.Shutdown(closeCtx); err != nil {
		returnErr = errors.Join(returnErr, fmt.Errorf("shutdown localhost HTTP: %w", err))
	}
	cancelClose()
	for stoppedProcesses < backgroundProcessCount {
		select {
		case result := <-processResults:
			stoppedProcesses++
			if result.err != nil && !(runCtx.Err() != nil && errors.Is(result.err, runCtx.Err())) {
				returnErr = errors.Join(returnErr, fmt.Errorf("stop %s: %w", result.name, result.err))
			}
		case <-time.After(shutdownTimeout):
			returnErr = errors.Join(returnErr, errors.New("stop background schedulers: timed out"))
			stoppedProcesses = backgroundProcessCount
		}
	}
	if !serverStopped {
		select {
		case err := <-serverErrors:
			if err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("stop localhost HTTP: %w", err))
			}
		default:
		}
	}
	logger.Info("ShotForward daemon stopped", "reason", ctx.Err())
	return returnErr
}

// checkCodexCapabilitiesOnStartup warms both routed Runtimes independently and
// records image Provider health. It never resolves or changes ActiveRuntime,
// creates a Thread/Turn, or prevents the daemon from serving HTTP/bootstrap.
func checkCodexCapabilitiesOnStartup(ctx context.Context, logger *slog.Logger, runtime agentapp.Runtime, providers providerhttp.Service) {
	var checks sync.WaitGroup
	snapshots, routedSnapshots := runtime.(agentapp.RuntimeSnapshotSelector)
	models, routedModels := runtime.(agentapp.RuntimeModelSelector)
	routed := routedSnapshots && routedModels
	checkRuntime := func(kind agentapp.RuntimeKind) {
		defer checks.Done()
		checkCtx, cancel := context.WithTimeout(ctx, startupCodexCheckTimeout)
		defer cancel()
		started := time.Now()
		var snapshot agentapp.RuntimeSnapshot
		var err error
		if routed {
			snapshot, err = snapshots.SnapshotFor(checkCtx, kind)
		} else {
			snapshot, err = runtime.Snapshot(checkCtx)
		}
		logger.InfoContext(checkCtx, "Agent Runtime startup snapshot completed", "runtime_kind", kind,
			"health", snapshot.Health, "failure_code", snapshot.FailureCode, "elapsed_ms", time.Since(started).Milliseconds())
		if err != nil || snapshot.Health != agentapp.RuntimeReady {
			return
		}
		started = time.Now()
		var values []agentapp.Model
		if routed {
			values, err = models.ModelsFor(checkCtx, kind)
		} else {
			values, err = runtime.Models(checkCtx)
		}
		logger.InfoContext(checkCtx, "Agent Runtime startup models completed", "runtime_kind", kind,
			"model_count", len(values), "available", err == nil, "elapsed_ms", time.Since(started).Milliseconds())
	}
	if routed {
		checks.Add(2)
		go checkRuntime(agentapp.RuntimeUserCodex)
		go checkRuntime(agentapp.RuntimeShotForward)
	} else if _, selectable := runtime.(agentapp.RuntimeSelectionController); !selectable {
		checks.Add(1)
		go checkRuntime(agentapp.RuntimeUserCodex)
	}
	checks.Add(1)
	go func() {
		defer checks.Done()
		checkCtx, cancel := context.WithTimeout(ctx, startupCodexCheckTimeout)
		defer cancel()
		started := time.Now()
		profile, err := providers.Check(checkCtx, project.GeneratorImageCodex)
		if err != nil {
			logger.WarnContext(checkCtx, "Codex image capability startup check completed",
				"health", profile.Health, "failure_code", profile.FailureCode, "elapsed_ms", time.Since(started).Milliseconds())
			return
		}
		logger.InfoContext(checkCtx, "Codex image capability startup check completed",
			"health", profile.Health, "failure_code", profile.FailureCode, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	checks.Wait()
}

type backgroundAgentModelRefresher interface {
	RefreshAgentModelsIfSignedIn(context.Context) ([]agentapp.Model, bool, error)
}

func runAgentCapabilityMaintenance(ctx context.Context, logger *slog.Logger, runtime agentapp.Runtime, providers providerhttp.Service, cloud backgroundAgentModelRefresher) {
	startupDone := make(chan struct{})
	go func() {
		defer close(startupDone)
		checkCodexCapabilitiesOnStartup(ctx, logger, runtime, providers)
	}()
	ticker := time.NewTicker(agentModelRefreshInterval)
	defer ticker.Stop()
	runAgentModelRefreshLoop(ctx, logger, cloud, ticker.C)
	<-startupDone
}

func runAgentModelRefreshLoop(ctx context.Context, logger *slog.Logger, cloud backgroundAgentModelRefresher, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if cloud == nil {
				continue
			}
			refreshCtx, cancel := context.WithTimeout(ctx, agentModelRefreshTimeout)
			started := time.Now()
			values, refreshed, err := cloud.RefreshAgentModelsIfSignedIn(refreshCtx)
			cancel()
			if refreshed {
				logger.InfoContext(ctx, "ShotForward Agent model cache background refresh completed",
					"model_count", len(values), "available", err == nil, "elapsed_ms", time.Since(started).Milliseconds())
			}
		}
	}
}

func newHTTPCredentials(random io.Reader, configuredPairingCode string) (*localhttp.Credentials, string, error) {
	if configuredPairingCode != "" {
		return localhttp.NewReusableCredentials(random, configuredPairingCode)
	}
	return localhttp.NewCredentials(random)
}

type emptyGenerationCatalog struct{}

func (emptyGenerationCatalog) Generator(context.Context, project.GeneratorID) (projectapp.GenerationCapability, error) {
	return projectapp.GenerationCapability{}, errors.New("no Generation Provider is installed")
}

type generationCapabilityChecker struct {
	catalog projectapp.GenerationCapabilityCatalog
}

type deferredProviderConfiguration struct {
	target providerconfig.RuntimeConfiguration
}

func (c *deferredProviderConfiguration) RuntimeProvider(ctx context.Context, id project.GeneratorID) (providerconfig.Record, string, error) {
	if c == nil || c.target == nil {
		return providerconfig.Record{}, "", errors.New("Provider configuration is not ready")
	}
	return c.target.RuntimeProvider(ctx, id)
}

func (c *deferredProviderConfiguration) RuntimeProviderModel(ctx context.Context, id project.GeneratorID, modelID string) (providerconfig.Record, string, error) {
	if c == nil || c.target == nil {
		return providerconfig.Record{}, "", errors.New("Provider configuration is not ready")
	}
	target, ok := c.target.(providerconfig.ModelRuntimeConfiguration)
	if !ok {
		return providerconfig.Record{}, "", errors.New("Provider model configuration is not available")
	}
	return target.RuntimeProviderModel(ctx, id, modelID)
}

type combinedGeneratorRegistry []generation.GeneratorRegistry

func (r combinedGeneratorRegistry) Generator(id project.GeneratorID) (generation.Generator, bool) {
	for _, registry := range r {
		if value, ok := registry.Generator(id); ok {
			return value, true
		}
	}
	return nil, false
}

type combinedGenerationCatalog struct {
	base, cloud, h3, seedance, wan projectapp.GenerationCapabilityCatalog
}

func (c combinedGenerationCatalog) Generator(ctx context.Context, id project.GeneratorID) (projectapp.GenerationCapability, error) {
	if c.cloud != nil && (id == project.GeneratorImageShotForward || id == project.GeneratorVideoH3ShotForward) {
		return c.cloud.Generator(ctx, id)
	}
	if id == project.GeneratorVideoH3China || id == project.GeneratorVideoH3Official {
		return c.h3.Generator(ctx, id)
	}
	if id == project.GeneratorVideoSeedanceChina || id == project.GeneratorVideoSeedanceGlobal {
		return c.seedance.Generator(ctx, id)
	}
	if id == project.GeneratorVideoWanOfficial {
		return c.wan.Generator(ctx, id)
	}
	return c.base.Generator(ctx, id)
}

func (c combinedGenerationCatalog) GeneratorModel(ctx context.Context, id project.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	var catalog projectapp.GenerationCapabilityCatalog
	switch {
	case c.cloud != nil && (id == project.GeneratorImageShotForward || id == project.GeneratorVideoH3ShotForward):
		catalog = c.cloud
	case id == project.GeneratorVideoH3China || id == project.GeneratorVideoH3Official:
		catalog = c.h3
	case id == project.GeneratorVideoSeedanceChina || id == project.GeneratorVideoSeedanceGlobal:
		catalog = c.seedance
	case id == project.GeneratorVideoWanOfficial:
		catalog = c.wan
	default:
		catalog = c.base
	}
	if selected, ok := catalog.(projectapp.ModelGenerationCapabilityCatalog); ok {
		return selected.GeneratorModel(ctx, id, modelID)
	}
	return projectapp.GenerationCapability{}, errors.New("selected Provider model not found")
}

func (c generationCapabilityChecker) Check(ctx context.Context, record providerconfig.Record, _ string) (providerconfig.CheckResult, error) {
	if c.catalog == nil || record.GeneratorID != project.GeneratorImageCodex {
		return providerconfig.CheckResult{}, errors.New("unsupported capability check")
	}
	capability, err := c.catalog.Generator(ctx, record.GeneratorID)
	if err != nil {
		return providerconfig.CheckResult{}, err
	}
	return providerconfig.CheckResult{CapabilityVersion: capability.CapabilityVersion}, nil
}

type emptyGeneratorRegistry struct{}

func (emptyGeneratorRegistry) Generator(project.GeneratorID) (generation.Generator, bool) {
	return nil, false
}

func isLoopbackListener(listener net.Listener) bool {
	if listener == nil || listener.Addr() == nil {
		return false
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func productionRuntime(cfg config.Config, directories platformpaths.ApplicationDirs, credential agentmcp.Credential, selection runtimerouter.SelectionStore, cloudManager *shotforwardcloud.Manager, logger *slog.Logger) (RuntimeResource, error) {
	if cloudManager == nil {
		return RuntimeResource{}, errors.New("ShotForward cloud manager is required")
	}
	workingDirectory := filepath.Join(codexWorkingDirectory(directories), "user")
	sessionWorkspaceRoot := filepath.Join(codexWorkingDirectory(directories), "agent-sessions")
	for _, directory := range []string{workingDirectory, sessionWorkspaceRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return RuntimeResource{}, fmt.Errorf("create Codex Runtime directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return RuntimeResource{}, fmt.Errorf("secure Codex Runtime directory: %w", err)
		}
	}
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, credential.EnvironmentVariable+"="+credential.Value)
	userRuntime, err := codexruntime.NewSupervisor(codexruntime.Options{ConfiguredExecutable: cfg.CodexExecutable, WorkingDirectory: workingDirectory, MinimumVersion: codexruntime.MinimumSupportedUserCodexVersion,
		SessionWorkspaceRoot: sessionWorkspaceRoot, Environment: environment, ClientVersion: "shotforwardd", MCPTokenEnvironmentVariable: credential.EnvironmentVariable,
		NetworkSettingsPath: filepath.Join(directories.Data, "codex-network.json"), Logger: logger})
	if err != nil {
		return RuntimeResource{}, err
	}
	var embedded agentapp.Runtime = runtimerouter.Unavailable{SnapshotValue: agentapp.RuntimeSnapshot{Health: agentapp.RuntimeNotInstalled, FailureCode: "embedded_codex_not_installed", FailureSummary: "Bundled ShotForward Codex Runtime is unavailable"}}
	var closeEmbedded func(context.Context) error = func(context.Context) error { return nil }
	if cfg.EmbeddedCodexExecutable != "" {
		embeddedDirectory := filepath.Join(codexWorkingDirectory(directories), "shotforward")
		embeddedHome := filepath.Join(directories.Data, "shotforward-codex-home")
		for _, directory := range []string{embeddedDirectory, embeddedHome} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				_ = userRuntime.Close(context.Background())
				return RuntimeResource{}, err
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				_ = userRuntime.Close(context.Background())
				return RuntimeResource{}, err
			}
		}
		embeddedEnvironment := isolatedEnvironment(os.Environ(), map[string]string{
			credential.EnvironmentVariable: credential.Value,
			"CODEX_HOME":                   embeddedHome,
			"SHOTFORWARD_EMBEDDED_API_KEY": cfg.CloudProxyToken,
		})
		shotForwardRuntime, runtimeErr := codexruntime.NewSupervisor(codexruntime.Options{ConfiguredExecutable: cfg.EmbeddedCodexExecutable, WorkingDirectory: embeddedDirectory, Environment: embeddedEnvironment, ClientVersion: "shotforwardd", MCPTokenEnvironmentVariable: credential.EnvironmentVariable,
			SessionWorkspaceRoot: sessionWorkspaceRoot, DirectToolModelCatalogPath: filepath.Join(embeddedHome, "model-catalog.json"), Logger: logger,
			AdditionalConfig: shotForwardCodexProviderConfig(cfg)})
		if runtimeErr != nil {
			_ = userRuntime.Close(context.Background())
			return RuntimeResource{}, runtimeErr
		}
		embedded, closeEmbedded = shotForwardRuntime, shotForwardRuntime.Close
	}
	router, err := runtimerouter.NewWithShotForwardModels(userRuntime, embedded, cloudAgentModelSource{manager: cloudManager}, selection)
	if err != nil {
		_ = userRuntime.Close(context.Background())
		_ = closeEmbedded(context.Background())
		return RuntimeResource{}, err
	}
	return RuntimeResource{Runtime: router, Close: func(ctx context.Context) error { return errors.Join(userRuntime.Close(ctx), closeEmbedded(ctx)) }}, nil
}

type cloudAgentModelSource struct{ manager *shotforwardcloud.Manager }

func (s cloudAgentModelSource) BeginAgentTurn(threadID string) func() string {
	return s.manager.BeginAgentTurn(threadID)
}

func (s cloudAgentModelSource) EndAgentTurn(threadID string) string {
	return s.manager.EndAgentTurn(threadID)
}

func (s cloudAgentModelSource) Models(ctx context.Context) ([]agentapp.Model, error) {
	return s.manager.AgentModels(ctx)
}

func (s cloudAgentModelSource) RefreshModels(ctx context.Context) ([]agentapp.Model, error) {
	return s.manager.RefreshAgentModels(ctx)
}

func isolatedEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		if _, replaced := values[name]; !replaced {
			result = append(result, item)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func codexWorkingDirectory(directories platformpaths.ApplicationDirs) string {
	return filepath.Join(directories.Runtime, "codex")
}

func openStudioStore(ctx context.Context, path string) (*studio.Store, error) {
	mode := sharedsqlite.OpenExisting
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		mode = sharedsqlite.CreateNew
	} else if err != nil {
		return nil, fmt.Errorf("inspect Studio database: %w", err)
	}
	return studio.Open(ctx, path, mode)
}

func routeApplication(projects, agent, tasks, providers, agentTemplates, cloud http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if strings.HasPrefix(path, "/api/v1/cloud/") {
			cloud.ServeHTTP(response, request)
			return
		}
		if strings.HasPrefix(path, "/api/v1/agent-templates") {
			agentTemplates.ServeHTTP(response, request)
			return
		}
		if strings.HasPrefix(path, "/api/v1/generation-providers") {
			providers.ServeHTTP(response, request)
			return
		}
		if strings.HasPrefix(path, "/api/v1/tasks/") {
			tasks.ServeHTTP(response, request)
			return
		}
		if strings.HasPrefix(path, "/api/v1/agent/") || strings.HasPrefix(path, "/api/v1/agent-sessions/") ||
			strings.HasPrefix(path, "/api/v1/change-sets/") || strings.HasSuffix(path, "/agent-sessions") {
			agent.ServeHTTP(response, request)
			return
		}
		projects.ServeHTTP(response, request)
	})
}

func randomIdentifier(random io.Reader) (string, error) {
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}

type lockedReader struct {
	mutex  sync.Mutex
	reader io.Reader
}

func (r *lockedReader) Read(target []byte) (int, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.reader.Read(target)
}
