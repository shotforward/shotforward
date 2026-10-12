package shotforwardcloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

// TestLiveShotForwardH3MockVideo is an opt-in, billable conformance test for
// a deployed H3 lane (the historical test name is retained). It uses the desktop
// cloud Generator, including reference upload and MP4 download. Select an exact
// real model with SHOTFORWARD_TEST_H3_MODEL; no fallback model is permitted.
func TestLiveShotForwardH3MockVideo(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_H3_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_H3_E2E=1 to run the billable H3 integration")
	}
	code := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_CLOUD_CODE"))
	if code == "" {
		t.Fatal("SHOTFORWARD_TEST_CLOUD_CODE is required")
	}
	cloudURL := strings.TrimSpace(os.Getenv("SHOTFORWARD_CLOUD_URL"))
	if cloudURL == "" {
		cloudURL = "https://test.shotforward.com"
	}

	testTimeout := 45 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_TIMEOUT")); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil || parsed <= 0 {
			t.Fatalf("SHOTFORWARD_TEST_H3_TIMEOUT must be a positive duration: %q", raw)
		}
		testTimeout = parsed
	}
	testDuration := int64(5)
	if raw := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_DURATION")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed < 4 || parsed > 15 {
			t.Fatalf("SHOTFORWARD_TEST_H3_DURATION must be an integer between 4 and 15: %q", raw)
		}
		testDuration = parsed
	}
	testMode := project.ClipGenerationFirstImage
	referenceRole := project.GenerationReferenceFirstImage
	if raw := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_GENERATION_MODE")); raw != "" {
		switch raw {
		case string(project.ClipGenerationFirstImage):
		case string(project.ClipGenerationReference):
			testMode = project.ClipGenerationReference
			referenceRole = project.GenerationReferenceImage
		default:
			t.Fatalf("SHOTFORWARD_TEST_H3_GENERATION_MODE must be %q or %q: %q", project.ClipGenerationFirstImage, project.ClipGenerationReference, raw)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	manager, err := NewManager(cloudURL, &liveCredentials{}, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(ctx, code); err != nil {
		t.Fatalf("exchange desktop code: %v", err)
	}
	registerLiveLogout(t, manager)
	accountBefore, err := manager.Account(ctx)
	if err != nil || !accountBefore.SignedIn || accountBefore.User == nil || accountBefore.Wallet == nil {
		t.Fatalf("load cloud account: signed_in=%v error=%v", accountBefore.SignedIn, err)
	}
	models, err := manager.Models(ctx)
	if err != nil {
		t.Fatalf("load cloud models: %v", err)
	}
	preferredModel := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_MODEL"))
	if preferredModel == "" {
		preferredModel = "h3-video"
	}
	videoModel := modelFor(models, "video", preferredModel)
	if videoModel != preferredModel {
		t.Fatalf("required H3 model %q is unavailable; refusing another model", preferredModel)
	}
	expectedPoints := float64(0)
	if raw := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_POINTS_PER_SECOND")); raw != "" {
		rate, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil || rate <= 0 {
			t.Fatal("SHOTFORWARD_TEST_H3_POINTS_PER_SECOND must be positive")
		}
		expectedPoints = rate * float64(testDuration)
		var quote struct {
			ReservedPoints float64 `json:"reserved_points"`
			BillingUnit    string  `json:"billing_unit"`
		}
		if err = manager.DoAuthenticated(ctx, http.MethodPost, "/v1/video-tasks/quote", map[string]any{"model": videoModel, "duration": testDuration, "n": 1, "size": "1280x720"}, &quote); err != nil {
			t.Fatalf("quote H3 task: %v", err)
		}
		if quote.ReservedPoints != expectedPoints || quote.BillingUnit != "accepted_second" {
			t.Fatalf("unexpected H3 quote: points=%g unit=%s", quote.ReservedPoints, quote.BillingUnit)
		}
	}

	root := t.TempDir()
	referencePath := filepath.Join(root, "first-frame.png")
	referenceFile, err := os.OpenFile(referencePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	frame := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for offset := 0; offset < len(frame.Pix); offset += 4 {
		frame.Pix[offset], frame.Pix[offset+1], frame.Pix[offset+2], frame.Pix[offset+3] = 34, 74, 112, 255
	}
	if err = png.Encode(referenceFile, frame); err != nil {
		_ = referenceFile.Close()
		t.Fatal(err)
	}
	if err = referenceFile.Close(); err != nil {
		t.Fatal(err)
	}
	referenceBytes, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(referenceBytes)
	projectID := project.ProjectID("prj_" + strings.Repeat("H", 26))
	artifactID := project.ArtifactID("art_" + strings.Repeat("F", 26))
	reference := project.GenerationArtifactReference{Role: referenceRole, ArtifactID: artifactID}
	generator, err := NewGenerator(manager, project.GeneratorVideoH3ShotForward, filepath.Join(root, "videos"))
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := projectapp.NewSecureIDGenerator().NewGenerationJobID()
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	resumedTaskID := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_EXISTING_TASK_ID"))
	operation := generation.GeneratorOperation{ID: resumedTaskID}
	if resumedTaskID == "" {
		operation, err = generator.Start(ctx, generation.GeneratorRequest{
			JobID:       jobID,
			ProjectRoot: root,
			Snapshot: project.GenerationInputSnapshot{
				SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindVideo,
				Prompt:         "A calm blue cinematic frame with a subtle forward camera move, no text.",
				GenerationMode: testMode, GeneratorID: project.GeneratorVideoH3ShotForward,
				ProviderModelID: videoModel, ProviderConnectionRevision: 1, CapabilityVersion: cloudH3CapabilityVersion,
				OutputWidth: 1280, OutputHeight: 720, RequestedDurationSeconds: testDuration,
				References: []project.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
			},
			References: []generation.GeneratorArtifactReference{{
				Input: reference,
				Artifact: project.Artifact{ID: artifactID, ProjectID: projectID, Kind: project.MediaKindImage,
					RelativePath: "media/first-frame.png", SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(referenceBytes)),
					MIMEType: "image/png", OriginalFilename: "first-frame.png", Width: 1280, Height: 720, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)},
				AbsolutePath: referencePath,
			}},
		})
		if err != nil {
			t.Fatalf("start H3 video task: %v", err)
		}
	}
	submittedAt := time.Now()
	t.Logf("H3 task submitted: model=%s mode=%s task_id=%s", videoModel, testMode, operation.ID)
	var videoPath string
	for videoPath == "" {
		status, pollErr := generator.Poll(ctx, operation)
		if pollErr != nil {
			if generation.IsRetryablePollError(pollErr) {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(generator.PollInterval()):
					continue
				}
			}
			t.Fatalf("poll H3 video task: %v", pollErr)
		}
		switch status.State {
		case generation.GeneratorRunning:
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(generator.PollInterval()):
			}
		case generation.GeneratorSucceeded:
			if len(status.Outputs) != 1 {
				t.Fatalf("H3 returned %d outputs", len(status.Outputs))
			}
			videoPath = status.Outputs[0].SourcePath
		case generation.GeneratorFailed, generation.GeneratorStopped:
			t.Fatalf("H3 video task ended in %s: %s", status.State, status.FailureMessage)
		default:
			t.Fatalf("unexpected H3 state %q", status.State)
		}
	}
	video, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(video) < 12 || string(video[4:8]) != "ftyp" || http.DetectContentType(video[:min(len(video), 512)]) != "video/mp4" {
		t.Fatalf("downloaded H3 artifact is not an MP4: bytes=%d", len(video))
	}
	completedAt := time.Now()
	accountAfter, err := manager.Account(ctx)
	if err != nil || accountAfter.Wallet == nil {
		t.Fatalf("reload cloud wallet: %v", err)
	}
	chargedPoints := accountBefore.Wallet.AvailablePoints - accountAfter.Wallet.AvailablePoints
	if resumedTaskID == "" && expectedPoints > 0 && chargedPoints != expectedPoints {
		t.Fatalf("H3 charge mismatch: got=%g expected=%g", chargedPoints, expectedPoints)
	}
	if output := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_H3_OUTPUT")); output != "" {
		if err = os.WriteFile(output, video, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(video)
		t.Logf("H3 downloaded output: bytes=%d sha256=%x", len(video), digest)
	}
	if resumedTaskID == "" && chargedPoints <= 0 {
		t.Fatalf("H3 task was not charged: before=%g after=%g", accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints)
	}
	t.Logf("live H3 E2E passed: user=%s model=%s generation_mode=%s requested_seconds=%d resumed=%t video_bytes=%d submit_seconds=%.3f wait_and_download_seconds=%.3f total_seconds=%.3f charged_points=%g points=%g->%g task_id=%s",
		accountBefore.User.ID, videoModel, testMode, testDuration, resumedTaskID != "", len(video), submittedAt.Sub(startedAt).Seconds(), completedAt.Sub(submittedAt).Seconds(), completedAt.Sub(startedAt).Seconds(), chargedPoints, accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints, operation.ID)
}

// TestLiveShotForwardReferenceImage is an opt-in, billable end-to-end test of
// the desktop cloud image adapter. It reserves and uploads a real reference,
// validates the ready acknowledgement, creates a task with its artifact ID,
// waits for completion, and downloads the generated image.
func TestLiveShotForwardReferenceImage(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_CLOUD_REFERENCE_IMAGE_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_CLOUD_REFERENCE_IMAGE_E2E=1 to run the billable cloud reference-image integration")
	}
	code := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_CLOUD_CODE"))
	if code == "" {
		t.Fatal("SHOTFORWARD_TEST_CLOUD_CODE is required")
	}
	cloudURL := strings.TrimSpace(os.Getenv("SHOTFORWARD_CLOUD_URL"))
	if cloudURL == "" {
		cloudURL = "https://api.shotforward.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	manager, err := NewManager(cloudURL, &liveCredentials{}, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(ctx, code); err != nil {
		t.Fatalf("exchange desktop code: %v", err)
	}
	registerLiveLogout(t, manager)
	accountBefore, err := manager.Account(ctx)
	if err != nil || !accountBefore.SignedIn || accountBefore.User == nil || accountBefore.Wallet == nil {
		t.Fatalf("load cloud account: signed_in=%v error=%v", accountBefore.SignedIn, err)
	}
	models, err := manager.Models(ctx)
	if err != nil {
		t.Fatalf("load cloud models: %v", err)
	}
	imageModel := modelFor(models, "image", "gpt-image")
	if imageModel == "" {
		t.Fatal("required cloud image model is unavailable")
	}

	root := t.TempDir()
	referencePath := filepath.Join(root, "reference.png")
	referenceFile, err := os.OpenFile(referencePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	referenceImage := image.NewRGBA(image.Rect(0, 0, 720, 720))
	for y := 0; y < 720; y++ {
		for x := 0; x < 720; x++ {
			offset := referenceImage.PixOffset(x, y)
			r, g, b := uint8(245), uint8(239), uint8(225)
			if x >= 180 && x < 540 && y >= 180 && y < 540 {
				r, g, b = 32, 92, 158
			}
			referenceImage.Pix[offset], referenceImage.Pix[offset+1], referenceImage.Pix[offset+2], referenceImage.Pix[offset+3] = r, g, b, 255
		}
	}
	if err = png.Encode(referenceFile, referenceImage); err != nil {
		_ = referenceFile.Close()
		t.Fatal(err)
	}
	if err = referenceFile.Close(); err != nil {
		t.Fatal(err)
	}
	referenceBytes, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(referenceBytes)
	projectID := project.ProjectID("prj_" + strings.Repeat("R", 26))
	artifactID := project.ArtifactID("art_" + strings.Repeat("I", 26))
	reference := project.GenerationArtifactReference{Role: project.GenerationReferenceImage, ArtifactID: artifactID}
	generator, err := NewGenerator(manager, project.GeneratorImageShotForward, filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := projectapp.NewSecureIDGenerator().NewGenerationJobID()
	if err != nil {
		t.Fatal(err)
	}
	operation, err := generator.Start(ctx, generation.GeneratorRequest{
		JobID:       jobID,
		ProjectRoot: root,
		Snapshot: project.GenerationInputSnapshot{
			SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage,
			Prompt:      "Create a polished editorial variation of the attached geometric reference. Preserve the centered blue square and warm ivory background, add subtle paper texture and studio lighting, no text.",
			GeneratorID: project.GeneratorImageShotForward, ProviderModelID: imageModel,
			ProviderConnectionRevision: 1, CapabilityVersion: cloudImageCapabilityVersion,
			OutputWidth: 1536, OutputHeight: 1536, References: []project.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
		References: []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: project.Artifact{ID: artifactID, ProjectID: projectID, Kind: project.MediaKindImage,
				RelativePath: "media/reference.png", SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(referenceBytes)),
				MIMEType: "image/png", OriginalFilename: "reference.png", Width: 720, Height: 720, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)},
			AbsolutePath: referencePath,
		}},
	})
	if err != nil {
		t.Fatalf("start cloud reference-image task: %v", err)
	}
	t.Logf("cloud reference-image task started: task_id=%s model=%s reference_bytes=%d", operation.ID, imageModel, len(referenceBytes))

	var imagePath string
	for imagePath == "" {
		status, pollErr := generator.Poll(ctx, operation)
		if pollErr != nil {
			if generation.IsRetryablePollError(pollErr) {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(generator.PollInterval()):
					continue
				}
			}
			t.Fatalf("poll cloud reference-image task: %v", pollErr)
		}
		switch status.State {
		case generation.GeneratorRunning:
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(generator.PollInterval()):
			}
		case generation.GeneratorSucceeded:
			if len(status.Outputs) != 1 {
				t.Fatalf("cloud reference-image task returned %d outputs", len(status.Outputs))
			}
			imagePath = status.Outputs[0].SourcePath
		case generation.GeneratorFailed, generation.GeneratorStopped:
			t.Fatalf("cloud reference-image task %s ended in %s: code=%s message=%s", operation.ID, status.State, status.FailureCode, status.FailureMessage)
		default:
			t.Fatalf("unexpected cloud reference-image state %q", status.State)
		}
	}
	generated, err := os.Open(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	configuration, _, decodeErr := image.DecodeConfig(generated)
	closeErr := generated.Close()
	if decodeErr != nil || closeErr != nil || configuration.Width != 1536 || configuration.Height != 1536 {
		t.Fatalf("downloaded cloud artifact is not a valid image: config=%#v decode_error=%v close_error=%v", configuration, decodeErr, closeErr)
	}
	info, err := os.Stat(imagePath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("downloaded cloud artifact is empty: info=%v error=%v", info, err)
	}
	accountAfter, err := manager.Account(ctx)
	if err != nil || accountAfter.Wallet == nil {
		t.Fatalf("reload cloud wallet: %v", err)
	}
	if accountAfter.Wallet.AvailablePoints >= accountBefore.Wallet.AvailablePoints {
		t.Fatalf("billable cloud reference-image call did not reduce available points: before=%g after=%g", accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints)
	}
	t.Logf("live cloud reference-image E2E passed: user=%s model=%s reference_bytes=%d image_bytes=%d dimensions=%dx%d points=%g->%g",
		accountBefore.User.ID, imageModel, len(referenceBytes), info.Size(), configuration.Width, configuration.Height,
		accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints)
}

// TestLiveShotForwardAgentAndImage is an opt-in, billable test against the
// deployed ShotForward test cloud. It verifies the same client adapters used
// by the desktop host while keeping cloud credentials out of test output.
func TestLiveShotForwardAgentAndImage(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_CLOUD_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_CLOUD_E2E=1 to run the billable cloud integration")
	}
	code := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_CLOUD_CODE"))
	executable := strings.TrimSpace(os.Getenv("SHOTFORWARD_EMBEDDED_CODEX_EXECUTABLE"))
	if code == "" || executable == "" {
		t.Fatal("SHOTFORWARD_TEST_CLOUD_CODE and SHOTFORWARD_EMBEDDED_CODEX_EXECUTABLE are required")
	}
	cloudURL := strings.TrimSpace(os.Getenv("SHOTFORWARD_CLOUD_URL"))
	if cloudURL == "" {
		cloudURL = "https://test.shotforward.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	credentials := &liveCredentials{}
	manager, err := NewManager(cloudURL, credentials, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(ctx, code); err != nil {
		t.Fatalf("exchange desktop code: %v", err)
	}
	registerLiveLogout(t, manager)
	if credentials.empty() {
		t.Fatal("desktop exchange did not persist the refresh credential")
	}
	accountBefore, err := manager.Account(ctx)
	if err != nil || !accountBefore.SignedIn || accountBefore.User == nil || accountBefore.Wallet == nil {
		t.Fatalf("load cloud account: signed_in=%v error=%v", accountBefore.SignedIn, err)
	}
	models, err := manager.Models(ctx)
	if err != nil {
		t.Fatalf("load cloud models: %v", err)
	}
	responseModel, imageModel := modelFor(models, "responses", "gpt-5.6-luna"), modelFor(models, "image", "gpt-image")
	if responseModel == "" || imageModel == "" {
		t.Fatalf("required live models unavailable: responses=%v image=%v", responseModel != "", imageModel != "")
	}

	proxyHandler, err := NewHandler(manager, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(proxyHandler)
	defer proxy.Close()
	sessionID := agent.SessionID("ags_" + strings.Repeat("A", 26))
	projectID := project.ProjectID("prj_" + strings.Repeat("P", 26))
	episodeID := project.EpisodeID("ep_" + strings.Repeat("E", 26))
	drafts := &liveDrafts{}
	mcp, err := agentmcp.NewServer(agentmcp.Options{
		Queries: &liveProjectQueries{episode: project.Episode{EpisodeMetadata: project.EpisodeMetadata{
			ID: episodeID, ProjectID: projectID,
		}}},
		Drafts: drafts, DraftCleanup: drafts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = mcp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if closeErr := mcp.Close(closeCtx); closeErr != nil {
			t.Errorf("close ShotForward MCP: %v", closeErr)
		}
	}()
	mcpURL, err := mcp.RegisterBinding(ctx, agentmcp.BindingScope{
		RouteKey: "route_" + strings.Repeat("R", 32), SessionID: sessionID, ProjectID: projectID, EpisodeID: episodeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpCredential := mcp.Credential()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	workspace := filepath.Join(runtimeRoot, "session")
	codexHome, err := os.MkdirTemp("/tmp/shotforward-live-e2e", "codex-home-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for attempt := 0; attempt < 5; attempt++ {
			if removeErr := os.RemoveAll(codexHome); removeErr == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	for _, directory := range []string{runtimeRoot, workspace, codexHome} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtimeEnvironment := append([]string(nil), os.Environ()...)
	runtimeEnvironment = append(runtimeEnvironment,
		"CODEX_HOME="+codexHome,
		"SHOTFORWARD_EMBEDDED_API_KEY=local-capability-only",
		mcpCredential.EnvironmentVariable+"="+mcpCredential.Value,
	)
	runtime, err := codexruntime.NewSupervisor(codexruntime.Options{
		ConfiguredExecutable:       executable,
		WorkingDirectory:           runtimeRoot,
		DirectToolModelCatalogPath: filepath.Join(codexHome, "model-catalog.json"),
		Environment:                runtimeEnvironment,
		ClientVersion:              "shotforward-cloud-live-e2e",
		RequestTimeout:             2 * time.Minute,
		ShutdownTimeout:            10 * time.Second,
		BaseInstructions:           "Follow the user request exactly. Use the ShotForward tools supplied by the client for project facts.",
		AdditionalConfig: []string{
			`model_provider="shotforward"`,
			`model_providers.shotforward.name="ShotForward"`,
			`model_providers.shotforward.base_url=` + strconv.Quote(proxy.URL+"/api/v1/cloud/openai"),
			`model_providers.shotforward.env_key="SHOTFORWARD_EMBEDDED_API_KEY"`,
			`model_providers.shotforward.wire_api="responses"`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		if closeErr := runtime.Close(closeCtx); closeErr != nil {
			t.Errorf("close bundled Codex runtime: %v", closeErr)
		}
	}()
	snapshot, err := runtime.Snapshot(ctx)
	if err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Fatalf("bundled Codex runtime not ready: health=%s error=%v", snapshot.Health, err)
	}
	runtimeModels, err := runtime.Models(ctx)
	if err != nil || !hasAgentModel(runtimeModels, responseModel) {
		t.Fatalf("bundled runtime model %q unavailable: %v", responseModel, err)
	}
	thread, err := runtime.StartThread(ctx, agentapp.ThreadRequest{
		AgentSessionID: sessionID,
		ProjectID:      projectID,
		EpisodeID:      episodeID,
		ModelID:        responseModel,
		WorkspacePath:  workspace,
		MCPBindingURL:  mcpURL,
	})
	if err != nil {
		t.Fatalf("start bundled Codex thread: %v", err)
	}
	events, err := runtime.Subscribe(ctx, thread.CodexThreadID)
	if err != nil {
		t.Fatal(err)
	}
	agentTurnID := agent.TurnID("agt_" + strings.Repeat("T", 28))
	if err := mcp.PrepareTurn(sessionID, agentTurnID); err != nil {
		t.Fatalf("prepare live MCP Turn: %v", err)
	}
	defer func() { _ = mcp.EndTurn(context.Background(), sessionID, agentTurnID) }()
	turn, err := runtime.StartTurn(ctx, agentapp.TurnRequest{
		CodexThreadID: thread.CodexThreadID,
		ModelID:       responseModel,
		Text:          "Call declare_turn_intent for discussion, then call read_script. After both tools complete, reply with exactly SHOTFORWARD_AGENT_E2E_OK and nothing else.",
	})
	if err != nil {
		t.Fatalf("start bundled Codex turn: %v", err)
	}
	if err := mcp.ActivateTurn(sessionID, agentmcp.ActiveTurn{AgentTurnID: agentTurnID, CodexThreadID: thread.CodexThreadID, CodexTurnID: turn.CodexTurnID}); err != nil {
		t.Fatalf("activate live MCP Turn: %v", err)
	}
	var answer strings.Builder
	completed := false
	toolsCompleted := map[string]bool{}
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event, ok := <-events:
			if !ok {
				t.Fatal("bundled Codex event stream closed before completion")
			}
			if event.CodexTurnID != "" && event.CodexTurnID != turn.CodexTurnID {
				continue
			}
			if event.Type == agentapp.RuntimeAssistantDelta {
				answer.WriteString(event.TextDelta)
			}
			if event.Type == agentapp.RuntimeToolCompleted {
				toolsCompleted[event.ToolName] = true
			}
			if event.Type == agentapp.RuntimeConnectionFailure {
				t.Fatalf("bundled Codex connection failure: %s", event.FailureSummary)
			}
			if event.Type == agentapp.RuntimeTurnCompleted {
				if event.TurnStatus != agent.TurnCompleted {
					t.Fatalf("bundled Codex turn status=%s failure=%s", event.TurnStatus, event.FailureSummary)
				}
				completed = true
			}
		}
	}
	if strings.TrimSpace(answer.String()) != "SHOTFORWARD_AGENT_E2E_OK" {
		t.Fatalf("unexpected bundled Agent reply: %q", strings.TrimSpace(answer.String()))
	}
	for _, name := range []string{"shotforward/declare_turn_intent", "shotforward/read_script"} {
		if !toolsCompleted[name] {
			t.Fatalf("bundled Agent did not complete required tool %q: %v", name, toolsCompleted)
		}
	}

	generator, err := NewGenerator(manager, project.GeneratorImageShotForward, filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := projectapp.NewSecureIDGenerator().NewGenerationJobID()
	if err != nil {
		t.Fatalf("generate unique live image job ID: %v", err)
	}
	operation, err := generator.Start(ctx, generation.GeneratorRequest{
		JobID:       jobID,
		ProjectRoot: workspace,
		Snapshot: project.GenerationInputSnapshot{
			SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage,
			Prompt:      "A single polished red origami crane centered on a neutral gray studio background, softbox lighting, no text.",
			GeneratorID: project.GeneratorImageShotForward, ProviderModelID: imageModel,
			ProviderConnectionRevision: 1, CapabilityVersion: cloudImageCapabilityVersion,
			OutputWidth: 720, OutputHeight: 720, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
	})
	if err != nil {
		t.Fatalf("start direct cloud image task: %v", err)
	}
	var imagePath string
	for imagePath == "" {
		status, pollErr := generator.Poll(ctx, operation)
		if pollErr != nil {
			if generation.IsRetryablePollError(pollErr) {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(generator.PollInterval()):
					continue
				}
			}
			t.Fatalf("poll direct cloud image task: %v", pollErr)
		}
		switch status.State {
		case generation.GeneratorRunning:
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(generator.PollInterval()):
			}
		case generation.GeneratorSucceeded:
			imagePath = status.Outputs[0].SourcePath
		case generation.GeneratorFailed, generation.GeneratorStopped:
			t.Fatalf("direct cloud image task ended in %s: %s", status.State, status.FailureMessage)
		default:
			t.Fatalf("unexpected direct cloud image state %q", status.State)
		}
	}
	image, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	mediaType := http.DetectContentType(image)
	if len(image) == 0 || !strings.HasPrefix(mediaType, "image/") {
		t.Fatalf("downloaded cloud artifact is not an image: bytes=%d media_type=%s", len(image), mediaType)
	}
	accountAfter, err := manager.Account(ctx)
	if err != nil || accountAfter.Wallet == nil {
		t.Fatalf("reload cloud wallet: %v", err)
	}
	if accountAfter.Wallet.AvailablePoints >= accountBefore.Wallet.AvailablePoints {
		t.Fatalf("billable live calls did not reduce available points: before=%g after=%g", accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints)
	}
	t.Logf("live cloud E2E passed: user=%s response_model=%s image_model=%s image_bytes=%d points=%g->%g",
		accountBefore.User.ID, responseModel, imageModel, len(image), accountBefore.Wallet.AvailablePoints, accountAfter.Wallet.AvailablePoints)
}

func modelFor(models []Model, kind, preferred string) string {
	for _, model := range models {
		if model.ProductKind == kind && model.ID == preferred {
			return model.ID
		}
	}
	for _, model := range models {
		if model.ProductKind == kind {
			return model.ID
		}
	}
	return ""
}

func registerLiveLogout(t *testing.T, manager *Manager) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := manager.Logout(ctx); err != nil {
			t.Errorf("revoke live cloud session: %v", err)
		}
	})
}

func hasAgentModel(models []agentapp.Model, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

type liveCredentials struct {
	mu    sync.Mutex
	value string
}

type liveProjectQueries struct{ episode project.Episode }

func (q *liveProjectQueries) GetEpisode(_ context.Context, input projectapp.GetEpisodeInput) (project.Episode, error) {
	if input.ProjectID != q.episode.ProjectID || input.EpisodeID != q.episode.ID {
		return project.Episode{}, projectapp.ErrEpisodeNotFound
	}
	return q.episode, nil
}

func (*liveProjectQueries) ListEpisodeAssets(context.Context, projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error) {
	return nil, errLiveUnused
}

func (*liveProjectQueries) FindPriorAssetCandidates(context.Context, projectapp.FindPriorAssetCandidatesInput) ([]projectapp.PriorAssetCandidate, error) {
	return nil, errLiveUnused
}

func (*liveProjectQueries) GetAsset(context.Context, projectapp.GetAssetInput) (project.EpisodeAsset, error) {
	return project.EpisodeAsset{}, errLiveUnused
}

func (*liveProjectQueries) GetClip(context.Context, projectapp.GetClipInput) (project.ClipDetails, error) {
	return project.ClipDetails{}, errLiveUnused
}

func (*liveProjectQueries) GetStoryboard(context.Context, projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error) {
	return projectapp.StoryboardResult{}, errLiveUnused
}

func (*liveProjectQueries) GetMediaSlot(context.Context, projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error) {
	return project.MediaSlotDetails{}, errLiveUnused
}

func (*liveProjectQueries) ListClipReferenceMedia(context.Context, projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error) {
	return nil, errLiveUnused
}

func (*liveProjectQueries) ListEpisodeReferenceMedia(context.Context, projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error) {
	return nil, errLiveUnused
}

type liveDrafts struct{}

func (*liveDrafts) BeginDraft(context.Context, changesetapp.BeginDraftInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{}, errLiveUnused
}

func (*liveDrafts) AppendOperations(context.Context, changesetapp.AppendOperationsInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{}, errLiveUnused
}

func (*liveDrafts) Finalize(context.Context, changesetapp.FinalizeInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{}, errLiveUnused
}

func (*liveDrafts) Apply(context.Context, changesetapp.ApplyInput) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{}, errLiveUnused
}

func (*liveDrafts) DiscardDraft(context.Context, project.ProjectID, changeset.ChangeSetID) error {
	return nil
}

func (*liveDrafts) DiscardTurnDrafts(context.Context, project.ProjectID, agent.TurnID) error {
	return nil
}

var errLiveUnused = errors.New("live E2E fixture does not expose this operation")

func (s *liveCredentials) Put(_ context.Context, _, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}

func (s *liveCredentials) Get(context.Context, string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.value != "", nil
}

func (s *liveCredentials) Delete(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := s.value != ""
	s.value = ""
	return found, nil
}

func (s *liveCredentials) empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value == ""
}
