package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const (
	codexImageConcurrency      = 5
	codexMaximumImageBytes     = 256 << 20
	codexMaximumReferenceBytes = 20 << 20
	codexMaximumReferences     = 5
	codexImageVersion          = "codex-subscription-image-v3"
	codexImageInstructions     = "You are an isolated image-generation worker. For each request, invoke the built-in image generation capability exactly once. If the user Turn contains images, they are all required generation references: include exactly all attached conversation images in the image-generation call by setting num_last_images_to_include to their count, and do not replace them with textual filenames. Do not run shell commands, browse the web, edit project files, or merely describe an image. Finish after the image-generation item completes."
)

var ErrCodexImageStopUnsupported = errors.New("Codex subscription image generation cannot be stopped safely")

type ImageGeneratorOptions struct {
	ConfiguredExecutable string
	WorkingDirectory     string
	Environment          []string
	ClientVersion        string
	Logger               *slog.Logger
	RequestTimeout       time.Duration
	GenerationTimeout    time.Duration
	ShutdownTimeout      time.Duration
}

type imageOperation struct {
	threadID   string
	turnID     string
	imagePaths []string
	imageState []string
	imageLimit bool
	status     generation.GeneratorStatus
	terminal   bool
	done       chan struct{}
}

type ImageGenerator struct {
	options ImageGeneratorOptions
	deps    dependencies

	mu                sync.Mutex
	client            protocolClient
	starting          chan struct{}
	stopping          bool
	capabilityVersion string
	clientEpoch       uint64
	operations        map[string]*imageOperation
	threadOperations  map[string]string
	active            int
}

var _ generation.Generator = (*ImageGenerator)(nil)

func NewImageGenerator(options ImageGeneratorOptions) (*ImageGenerator, error) {
	deps := dependencies{
		discover: DiscoverExecutable,
		version:  readVersion,
		newClient: func(executable string, args []string, cwd string, env []string, timeout time.Duration, notification func(rpcNotification), exited func(error)) (protocolClient, error) {
			return newRPCClient(executable, args, cwd, env, timeout, startOSProcess, notification, exited)
		},
	}
	return newImageGenerator(options, deps)
}

func newImageGenerator(options ImageGeneratorOptions, deps dependencies) (*ImageGenerator, error) {
	if options.WorkingDirectory == "" || !filepath.IsAbs(options.WorkingDirectory) || filepath.Clean(options.WorkingDirectory) != options.WorkingDirectory {
		return nil, errors.New("create Codex image Generator: clean absolute working directory is required")
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, errors.New("create Codex image Generator: working directory must exist")
	}
	if options.ClientVersion == "" {
		options.ClientVersion = "dev"
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.GenerationTimeout <= 0 {
		options.GenerationTimeout = 8 * time.Minute
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = append([]string(nil), options.Environment...)
	if deps.discover == nil || deps.version == nil || deps.newClient == nil {
		return nil, errors.New("create Codex image Generator: dependencies are required")
	}
	return &ImageGenerator{
		options:          options,
		deps:             deps,
		operations:       make(map[string]*imageOperation),
		threadOperations: make(map[string]string),
	}, nil
}

func (g *ImageGenerator) ID() domain.GeneratorID { return domain.GeneratorImageCodex }

func (g *ImageGenerator) ConcurrencyLimit() int { return codexImageConcurrency }

func (g *ImageGenerator) SupportsRecovery(domain.GenerationInputSnapshot) bool { return false }

func (g *ImageGenerator) Capability(ctx context.Context) (projectapp.GenerationCapability, error) {
	if err := g.ensureStarted(ctx); err != nil {
		return projectapp.GenerationCapability{}, err
	}
	g.mu.Lock()
	version := g.capabilityVersion
	g.mu.Unlock()
	return projectapp.GenerationCapability{
		GeneratorID:       domain.GeneratorImageCodex,
		CapabilityVersion: version,
		MediaKind:         domain.MediaKindImage,
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{
			Role:                         domain.GenerationReferenceImage,
			MaximumCount:                 codexMaximumReferences,
			MaximumBytes:                 20 << 20,
			AcceptedMIMETypes:            []string{"image/jpeg", "image/png", "image/webp"},
			SupportsAutomaticPreparation: true,
		}},
		SupportsStop:     false,
		SupportsRecovery: false,
	}, nil
}

func (g *ImageGenerator) Start(ctx context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if ctx == nil {
		return generation.GeneratorOperation{}, errors.New("start Codex image generation: context is required")
	}
	if err := ctx.Err(); err != nil {
		return generation.GeneratorOperation{}, err
	}
	if err := validateCodexImageRequest(request); err != nil {
		return generation.GeneratorOperation{}, err
	}
	operationID := "codex-image/" + request.JobID.String()
	g.mu.Lock()
	if g.stopping {
		g.mu.Unlock()
		return generation.GeneratorOperation{}, errors.New("start Codex image generation: Generator is closing")
	}
	if _, exists := g.operations[operationID]; exists {
		g.mu.Unlock()
		return generation.GeneratorOperation{}, errors.New("start Codex image generation: operation already exists")
	}
	if g.active >= codexImageConcurrency {
		g.mu.Unlock()
		return generation.GeneratorOperation{}, errors.New("start Codex image generation: concurrency limit reached")
	}
	g.operations[operationID] = &imageOperation{done: make(chan struct{}), status: generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "starting", Message: "Starting Codex image generation"}}
	g.active++
	g.mu.Unlock()

	go g.run(operationID, request)
	return generation.GeneratorOperation{ID: operationID}, nil
}

func validateCodexImageRequest(request generation.GeneratorRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("start Codex image generation: %w", err)
	}
	if request.Snapshot.GeneratorID != domain.GeneratorImageCodex || request.Snapshot.TargetMediaKind != domain.MediaKindImage || len(request.References) > codexMaximumReferences {
		return errors.New("start Codex image generation: unsupported inputs")
	}
	for _, reference := range request.References {
		if reference.Input.Role != domain.GenerationReferenceImage || reference.Artifact.Kind != domain.MediaKindImage {
			return errors.New("start Codex image generation: unsupported reference")
		}
	}
	var parameters map[string]json.RawMessage
	if json.Unmarshal([]byte(request.Snapshot.ParametersJSON), &parameters) != nil || len(parameters) != 0 {
		return errors.New("start Codex image generation: parameters are unsupported")
	}
	return nil
}

func (g *ImageGenerator) Poll(ctx context.Context, operation generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	if ctx == nil || operation.Validate() != nil {
		return generation.GeneratorStatus{}, errors.New("poll Codex image generation: invalid request")
	}
	if err := ctx.Err(); err != nil {
		return generation.GeneratorStatus{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	value := g.operations[operation.ID]
	if value == nil {
		return generation.GeneratorStatus{}, errors.New("poll Codex image generation: operation was not found")
	}
	status := value.status
	status.Outputs = append([]generation.GeneratorOutput(nil), status.Outputs...)
	if value.terminal {
		delete(g.operations, operation.ID)
	}
	return status, nil
}

func (g *ImageGenerator) Stop(context.Context, generation.GeneratorOperation) error {
	return ErrCodexImageStopUnsupported
}

func (g *ImageGenerator) run(operationID string, request generation.GeneratorRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), g.options.GenerationTimeout)
	defer cancel()
	g.mu.Lock()
	operation := g.operations[operationID]
	var done <-chan struct{}
	if operation != nil {
		done = operation.done
	}
	g.mu.Unlock()
	if done == nil {
		return
	}
	if err := g.ensureStarted(ctx); err != nil {
		g.fail(operationID, "codex_unavailable", safeSummary(err))
		return
	}

	g.mu.Lock()
	client := g.client
	g.mu.Unlock()
	if client == nil {
		g.fail(operationID, "codex_unavailable", "Codex image generation is unavailable")
		return
	}
	g.mu.Lock()
	capabilityVersion := g.capabilityVersion
	g.mu.Unlock()
	if request.Snapshot.CapabilityVersion != capabilityVersion {
		g.fail(operationID, "capability_changed", "Codex image generation capability changed after the Job was created")
		return
	}
	referencePaths := make([]string, 0, len(request.References))
	defer func() {
		for _, path := range referencePaths {
			_ = os.Remove(path)
		}
	}()
	for _, reference := range request.References {
		referencePath, stageErr := g.stageReference(reference.AbsolutePath)
		if stageErr != nil {
			g.fail(operationID, "reference_unreadable", "The reference image could not be prepared for Codex")
			return
		}
		referencePaths = append(referencePaths, referencePath)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	threadParams := map[string]any{
		"ephemeral":        true,
		"cwd":              g.options.WorkingDirectory,
		"approvalPolicy":   "never",
		"baseInstructions": codexImageInstructions,
		"serviceName":      "shotforward_image_generator",
		"config": map[string]any{
			"apps":       map[string]any{"_default": map[string]any{"enabled": false}},
			"web_search": "disabled",
		},
	}
	if err := client.Request(ctx, "thread/start", threadParams, &thread); err != nil || strings.TrimSpace(thread.Thread.ID) == "" {
		if err == nil {
			err = errors.New("thread/start returned an incomplete Thread")
		}
		g.fail(operationID, "codex_thread_failed", safeSummary(err))
		return
	}
	g.mu.Lock()
	operation = g.operations[operationID]
	if operation == nil || operation.terminal {
		g.mu.Unlock()
		return
	}
	operation.threadID = thread.Thread.ID
	operation.status = generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "generating", Message: "Codex is generating an image"}
	g.threadOperations[thread.Thread.ID] = operationID
	g.mu.Unlock()

	input := []map[string]any{{"type": "text", "text": codexImagePrompt(request.Snapshot)}}
	for _, referencePath := range referencePaths {
		input = append(input, map[string]any{"type": "localImage", "path": referencePath})
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := client.Request(ctx, "turn/start", map[string]any{
		"threadId": thread.Thread.ID, "input": input, "cwd": g.options.WorkingDirectory,
		"approvalPolicy": "never", "turnTrigger": "shotforward_media_worker",
	}, &turn); err != nil || strings.TrimSpace(turn.Turn.ID) == "" {
		if err == nil {
			err = errors.New("turn/start returned an incomplete Turn")
		}
		g.fail(operationID, "codex_turn_failed", safeSummary(err))
		return
	}
	g.mu.Lock()
	if operation := g.operations[operationID]; operation != nil && !operation.terminal {
		operation.turnID = turn.Turn.ID
	}
	g.mu.Unlock()

	select {
	case <-done:
		return
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			g.fail(operationID, "generation_timeout", "Codex image generation timed out")
		}
	}
}

func codexImagePrompt(snapshot domain.GenerationInputSnapshot) string {
	aspect, orientation, referenceSize := codexImageFrame(snapshot.OutputWidth, snapshot.OutputHeight)
	prompt := snapshot.Prompt
	if prompt == "" {
		prompt = "Create a coherent image based on the attached reference image."
	}
	referenceInstruction := ""
	if referenceCount := len(snapshot.References); referenceCount > 0 {
		referenceInstruction = fmt.Sprintf("The %d images attached to this Turn are required Image 1 through Image %d, in attachment order. Preserve their referenced identities, palettes, and line language unless the prompt explicitly changes them. Include all %d conversation images in the image-generation call by setting num_last_images_to_include=%d; do not refer to them only by filename.\n", referenceCount, referenceCount, referenceCount, referenceCount)
	}
	return fmt.Sprintf(`Generate exactly one image for this prompt.
%s
Create it in a native %s %s canvas, using %s pixels as the reference generation size.
The final delivery size is %dx%d pixels.
Do not add borders, letterboxing, pillarboxing, or an inset frame.
Keep all essential subjects inside the central 80%% safe area because the image may be proportionally resized and center-cropped to the final delivery size.

%s`, referenceInstruction, aspect, orientation, referenceSize, snapshot.OutputWidth, snapshot.OutputHeight, prompt)
}

func (g *ImageGenerator) stageReference(source string) (_ string, returnErr error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > codexMaximumReferenceBytes {
		return "", errors.New("invalid Codex reference image")
	}
	extension := strings.ToLower(filepath.Ext(source))
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		return "", errors.New("unsupported Codex reference image")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	output, err := os.CreateTemp(g.options.WorkingDirectory, ".reference-*"+extension)
	if err != nil {
		return "", err
	}
	path := output.Name()
	defer func() {
		if returnErr != nil {
			_ = os.Remove(path)
		}
	}()
	if err = output.Chmod(0o600); err == nil {
		var copied int64
		copied, err = io.Copy(output, io.LimitReader(input, codexMaximumReferenceBytes+1))
		if err == nil && (copied < 1 || copied > codexMaximumReferenceBytes) {
			err = errors.New("invalid Codex reference image size")
		}
	}
	if syncErr := output.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

func codexImageFrame(width, height int) (aspect, orientation, referenceSize string) {
	referenceSize = fmt.Sprintf("%dx%d", width, height)
	switch {
	case width == height:
		return "1:1", "square", referenceSize
	case width > height:
		return "16:9", "landscape", referenceSize
	default:
		return "9:16", "portrait", referenceSize
	}
}

func (g *ImageGenerator) onNotification(notification rpcNotification) {
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			Type      string `json:"type"`
			Status    string `json:"status"`
			SavedPath string `json:"savedPath"`
			Failure   *struct {
				Type string `json:"type"`
			} `json:"failure"`
		} `json:"item"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" {
		return
	}
	g.mu.Lock()
	operationID := g.threadOperations[params.ThreadID]
	operation := g.operations[operationID]
	if operation == nil || operation.terminal {
		g.mu.Unlock()
		return
	}
	switch notification.Method {
	case "item/completed":
		if params.Item.Type == "imageGeneration" {
			operation.imagePaths = append(operation.imagePaths, params.Item.SavedPath)
			operation.imageState = append(operation.imageState, params.Item.Status)
			operation.imageLimit = operation.imageLimit || params.Item.Failure != nil && params.Item.Failure.Type == "usageLimitExceeded"
		}
		g.mu.Unlock()
	case "turn/completed":
		if operation.imageLimit {
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "codex_image_quota_exhausted", FailureMessage: "Codex image generation usage limit was reached"})
			g.mu.Unlock()
			return
		}
		if params.Turn.Status == "failed" {
			message := "Codex image generation failed"
			if params.Turn.Error != nil && strings.TrimSpace(params.Turn.Error.Message) != "" {
				message = safeSummary(errors.New(params.Turn.Error.Message))
			}
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "codex_generation_failed", FailureMessage: message})
			g.mu.Unlock()
			return
		}
		if params.Turn.Status != "completed" {
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "codex_generation_interrupted", FailureMessage: "Codex image generation did not complete"})
			g.mu.Unlock()
			return
		}
		paths := append([]string(nil), operation.imagePaths...)
		if len(paths) != 1 || len(operation.imageState) != 1 || operation.imageState[0] != "completed" || !validGeneratedImagePath(paths[0]) {
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "codex_protocol_error", FailureMessage: "Codex image generation returned an invalid image result"})
			g.mu.Unlock()
			return
		}
		g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Message: "Image generated", Outputs: []generation.GeneratorOutput{{SourcePath: paths[0]}}})
		g.mu.Unlock()
	default:
		g.mu.Unlock()
	}
}

func validGeneratedImagePath(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > codexMaximumImageBytes {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func (g *ImageGenerator) fail(operationID, code, message string) {
	if strings.TrimSpace(message) == "" {
		message = code
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if operation := g.operations[operationID]; operation != nil && !operation.terminal {
		g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: code, FailureMessage: message})
	}
}

func (g *ImageGenerator) finishLocked(operation *imageOperation, status generation.GeneratorStatus) {
	operation.status = status
	operation.terminal = true
	delete(g.threadOperations, operation.threadID)
	close(operation.done)
	if g.active > 0 {
		g.active--
	}
}

func (g *ImageGenerator) ensureStarted(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start Codex image Generator: context is required")
	}
	for {
		g.mu.Lock()
		if g.stopping {
			g.mu.Unlock()
			return errors.New("start Codex image Generator: Generator is closing")
		}
		if g.client != nil {
			g.mu.Unlock()
			return nil
		}
		if g.starting != nil {
			wait := g.starting
			g.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		wait := make(chan struct{})
		g.starting = wait
		g.mu.Unlock()

		err := g.start(ctx)
		if err != nil {
			g.options.Logger.Error("Codex image app-server startup failed", "error", safeSummary(err))
		}
		g.mu.Lock()
		g.starting = nil
		close(wait)
		g.mu.Unlock()
		return err
	}
}

func (g *ImageGenerator) start(ctx context.Context) error {
	executable, err := g.deps.discover(g.options.ConfiguredExecutable)
	if err != nil {
		return err
	}
	versionContext, cancelVersion := context.WithTimeout(ctx, g.options.RequestTimeout)
	version, err := g.deps.version(versionContext, executable)
	cancelVersion()
	if err != nil {
		return err
	}
	baseArgs := baseConfigArguments(nil)
	probe, err := g.deps.newClient(executable, append([]string{"app-server", "--stdio"}, baseArgs...), g.options.WorkingDirectory, g.options.Environment, g.options.RequestTimeout, nil, nil)
	if err != nil {
		return err
	}
	if err := initialize(ctx, probe, g.options.ClientVersion); err != nil {
		g.closeClient(probe)
		return err
	}
	disableArgs, err := inspectExtensions(ctx, probe)
	g.closeClient(probe)
	if err != nil {
		return err
	}
	disableArgs = append(disableArgs, "-c", `mcp_servers."shotforward"={enabled=false,url="http://127.0.0.1:1"}`)
	exited := make(chan error, 1)
	client, err := g.deps.newClient(executable, append(append([]string{"app-server", "--stdio"}, baseArgs...), disableArgs...), g.options.WorkingDirectory, g.options.Environment, g.options.RequestTimeout, g.onNotification, func(err error) {
		select {
		case exited <- err:
		default:
		}
	})
	if err != nil {
		return err
	}
	if err := initialize(ctx, client, g.options.ClientVersion); err != nil {
		g.closeClient(client)
		return err
	}
	var account struct {
		Account json.RawMessage `json:"account"`
	}
	if err := client.Request(ctx, "account/read", map[string]any{"refreshToken": false}, &account); err != nil {
		g.closeClient(client)
		return err
	}
	var identity struct {
		Type string `json:"type"`
	}
	if len(account.Account) == 0 || string(account.Account) == "null" || json.Unmarshal(account.Account, &identity) != nil || identity.Type != "chatgpt" {
		g.closeClient(client)
		return errors.New("Codex ChatGPT subscription login is required")
	}
	var capabilities struct {
		ImageGeneration bool `json:"imageGeneration"`
	}
	if err := client.Request(ctx, "modelProvider/capabilities/read", map[string]any{}, &capabilities); err != nil {
		g.closeClient(client)
		return err
	}
	if !capabilities.ImageGeneration {
		g.closeClient(client)
		return errors.New("Codex image generation capability is unavailable")
	}
	select {
	case <-exited:
		return errors.New("Codex app-server disconnected during image Generator startup")
	default:
	}
	g.mu.Lock()
	if g.stopping {
		g.mu.Unlock()
		g.closeClient(client)
		return errors.New("Codex image Generator is closing")
	}
	g.client = client
	g.clientEpoch++
	epoch := g.clientEpoch
	g.capabilityVersion = codexImageVersion + "@" + version
	g.mu.Unlock()
	g.options.Logger.Info("Codex image app-server started",
		"executable", executable,
		"version", version,
		"capability_version", codexImageVersion+"@"+version)
	go g.watchExit(epoch, exited)
	return nil
}

func (g *ImageGenerator) watchExit(epoch uint64, exited <-chan error) {
	exitErr := <-exited
	g.mu.Lock()
	if g.stopping || g.clientEpoch != epoch {
		g.mu.Unlock()
		return
	}
	g.client = nil
	affected := make([]string, 0, len(g.operations))
	for operationID, operation := range g.operations {
		if !operation.terminal {
			affected = append(affected, operationID)
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "codex_disconnected", FailureMessage: "Codex app-server disconnected during image generation"})
		}
	}
	g.mu.Unlock()
	sort.Strings(affected)
	g.options.Logger.Error("Codex image app-server disconnected",
		"error", safeSummary(exitErr),
		"affected_operation_count", len(affected),
		"affected_operation_ids", strings.Join(affected, ","))
}

func (g *ImageGenerator) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close Codex image Generator: context is required")
	}
	g.mu.Lock()
	g.stopping = true
	client := g.client
	g.client = nil
	active := make([][2]string, 0, g.active)
	for _, operation := range g.operations {
		if !operation.terminal {
			if operation.threadID != "" && operation.turnID != "" {
				active = append(active, [2]string{operation.threadID, operation.turnID})
			}
			g.finishLocked(operation, generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: "generator_closed", FailureMessage: "Codex image Generator closed before the operation completed"})
		}
	}
	g.mu.Unlock()
	if client == nil {
		return nil
	}
	shutdown, cancel := context.WithTimeout(ctx, g.options.ShutdownTimeout)
	defer cancel()
	for _, turn := range active {
		_ = client.Request(shutdown, "turn/interrupt", map[string]any{"threadId": turn[0], "turnId": turn[1]}, nil)
	}
	return client.Close(shutdown)
}

func (g *ImageGenerator) closeClient(client protocolClient) {
	ctx, cancel := context.WithTimeout(context.Background(), g.options.ShutdownTimeout)
	defer cancel()
	_ = client.Close(ctx)
}

type ImageGeneratorRegistry struct{ Provider *ImageGenerator }

func (r ImageGeneratorRegistry) Generator(id domain.GeneratorID) (generation.Generator, bool) {
	if r.Provider == nil || id != domain.GeneratorImageCodex {
		return nil, false
	}
	return r.Provider, true
}

type ImageGenerationCatalog struct{ Provider *ImageGenerator }

func (c ImageGenerationCatalog) Generator(ctx context.Context, id domain.GeneratorID) (projectapp.GenerationCapability, error) {
	if c.Provider == nil || id != domain.GeneratorImageCodex {
		return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
	}
	return c.Provider.Capability(ctx)
}
