package codexruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestImageGeneratorRealCapability(t *testing.T) {
	if os.Getenv("SHOTFORWARD_RUN_CODEX_IMAGE_CAPABILITY_TEST") != "1" {
		t.Skip("set SHOTFORWARD_RUN_CODEX_IMAGE_CAPABILITY_TEST=1 to inspect the installed Codex capability without generating an image")
	}
	generator, err := NewImageGenerator(ImageGeneratorOptions{WorkingDirectory: filepath.Clean(t.TempDir()), ClientVersion: "shotforward-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := generator.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	capability, err := generator.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capability.GeneratorID != domain.GeneratorImageCodex || capability.CapabilityVersion == "" {
		t.Fatalf("capability = %#v", capability)
	}
}

// This opt-in test uses one image generation from the signed-in Codex
// subscription. Keep it separate from the capability probe so normal and
// release test runs never consume the user's image allowance.
func TestImageGeneratorRealGeneration(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_LIVE_CODEX_IMAGE") != "1" {
		t.Skip("set SHOTFORWARD_TEST_LIVE_CODEX_IMAGE=1 to generate one image with the installed Codex")
	}
	root := filepath.Clean(t.TempDir())
	generator, err := NewImageGenerator(ImageGeneratorOptions{
		WorkingDirectory:  root,
		ClientVersion:     "shotforward-live-image-test",
		GenerationTimeout: 8 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := generator.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	capability, err := generator.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := validImageGeneratorRequest(root, capability.CapabilityVersion, 0)
	request.Snapshot.Prompt = "A minimal blue circle centered on a warm white background, clean flat graphic"
	if source := strings.TrimSpace(os.Getenv("SHOTFORWARD_TEST_LIVE_CODEX_IMAGE_REFERENCE")); source != "" {
		referencePath, pathErr := filepath.Abs(source)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		info, statErr := os.Stat(referencePath)
		if statErr != nil {
			t.Fatal(statErr)
		}
		mimeType := "image/png"
		switch strings.ToLower(filepath.Ext(referencePath)) {
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".webp":
			mimeType = "image/webp"
		}
		reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: domain.ArtifactID("art_" + strings.Repeat("R", 26))}
		request.Snapshot.Prompt = "Create a clean visual variant based on the attached reference image. Preserve its central subject and recognizable design language."
		request.Snapshot.References = []domain.GenerationArtifactReference{reference}
		request.References = []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: domain.Artifact{ID: reference.ArtifactID, ProjectID: domain.ProjectID(testProjectID), Kind: domain.MediaKindImage,
				RelativePath: "media/images/live-reference" + filepath.Ext(referencePath), SHA256: strings.Repeat("a", 64), ByteSize: info.Size(),
				MIMEType: mimeType, OriginalFilename: filepath.Base(referencePath), Width: 1, Height: 1, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)},
			AbsolutePath: referencePath,
		}}
	}
	operation, err := generator.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := generator.Poll(context.Background(), operation)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == generation.GeneratorSucceeded {
			if len(status.Outputs) != 1 || !validGeneratedImagePath(status.Outputs[0].SourcePath) || status.Validate() != nil {
				t.Fatalf("invalid generated output: %#v", status)
			}
			return
		}
		if status.State == generation.GeneratorFailed {
			t.Fatalf("live image generation failed: code=%s message=%s", status.FailureCode, status.FailureMessage)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("live image generation did not finish before the deadline")
}

func TestImageGeneratorCapabilityAndSuccessfulOperation(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	output := filepath.Join(root, "generated.png")
	if err := os.WriteFile(output, []byte("fake image"), 0o600); err != nil {
		t.Fatal(err)
	}
	turnRequests := make(chan map[string]any, 1)
	formal := &fakeProtocolClient{}
	formal.handler = imageRuntimeHandler(func(params map[string]any) {
		turnRequests <- params
	})
	generator, starts := testImageGenerator(t, root, formal)
	if len(*starts) != 0 {
		t.Fatal("Codex image Generator was not lazy")
	}

	if generator.ConcurrencyLimit() != 5 || generator.SupportsRecovery(domain.GenerationInputSnapshot{}) {
		t.Fatalf("concurrency = %d, recovery = true", generator.ConcurrencyLimit())
	}
	capability, err := generator.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capability.GeneratorID != domain.GeneratorImageCodex || capability.CapabilityVersion != "codex-subscription-image-v3@0.153.0" || capability.SupportsStop || capability.SupportsRecovery || len(capability.ReferenceCapabilities) != 1 || capability.ReferenceCapabilities[0].MaximumCount != 5 {
		t.Fatalf("capability = %#v", capability)
	}
	if len(*starts) != 2 || !containsArgument((*starts)[1], `mcp_servers."shotforward"={enabled=false,url="http://127.0.0.1:1"}`) {
		t.Fatalf("formal Codex arguments do not isolate ShotForward MCP: %v", (*starts)[1])
	}

	request := validImageGeneratorRequest(root, capability.CapabilityVersion, 0)
	for index, content := range []string{"reference one", "reference two"} {
		suffix := string(rune('A' + index))
		referencePath := filepath.Join(root, "reference-"+suffix+".png")
		if err := os.WriteFile(referencePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: domain.ArtifactID("art_" + strings.Repeat(suffix, 26))}
		request.Snapshot.References = append(request.Snapshot.References, reference)
		request.References = append(request.References, generation.GeneratorArtifactReference{
			Input: reference,
			Artifact: domain.Artifact{
				ID: reference.ArtifactID, ProjectID: domain.ProjectID(testProjectID), Kind: domain.MediaKindImage,
				RelativePath: "media/images/reference-" + suffix + ".png", SHA256: strings.Repeat(strings.ToLower(suffix), 64), ByteSize: int64(len(content)),
				MIMEType: "image/png", OriginalFilename: "reference-" + suffix + ".png", Width: 64, Height: 64,
				CreatedAt: time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC),
			},
			AbsolutePath: referencePath,
		})
	}
	operation, err := generator.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	params := <-turnRequests
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 3 {
		t.Fatalf("turn input = %#v", params["input"])
	}
	prompt := input[0]["text"].(string)
	if input[0]["type"] != "text" || !strings.Contains(prompt, "16:9") || !strings.Contains(prompt, "required Image 1 through Image 2") || !strings.Contains(prompt, "num_last_images_to_include=2") {
		t.Fatalf("turn input = %#v", params["input"])
	}
	for index, want := range []string{"reference one", "reference two"} {
		stagedReference, _ := input[index+1]["path"].(string)
		if input[index+1]["type"] != "localImage" || filepath.Dir(stagedReference) != root || stagedReference == request.References[index].AbsolutePath {
			t.Fatalf("turn input = %#v", params["input"])
		}
		if content, err := os.ReadFile(stagedReference); err != nil || string(content) != want {
			t.Fatalf("staged reference %d = %q, %v", index, content, err)
		}
	}
	formal.emit("item/completed", map[string]any{"threadId": "thr_image", "item": map[string]any{"id": "img_1", "type": "imageGeneration", "status": "completed", "savedPath": output}})
	formal.emit("turn/completed", map[string]any{"threadId": "thr_image", "turn": map[string]any{"id": "turn_image", "status": "completed"}})
	status := pollImageTerminal(t, generator, operation)
	if status.State != generation.GeneratorSucceeded || len(status.Outputs) != 1 || status.Outputs[0].SourcePath != output || status.Validate() != nil {
		t.Fatalf("status = %#v", status)
	}
}

func TestCodexImagePromptUsesCloudAlignedFramePlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		width, height       int
		aspect, orientation string
		referenceSize       string
	}{
		{name: "portrait 480p", width: 480, height: 854, aspect: "9:16", orientation: "portrait", referenceSize: "480x854"},
		{name: "portrait 720p", width: 720, height: 1280, aspect: "9:16", orientation: "portrait", referenceSize: "720x1280"},
		{name: "portrait 1.5K asset", width: 864, height: 1536, aspect: "9:16", orientation: "portrait", referenceSize: "864x1536"},
		{name: "portrait 1080p", width: 1080, height: 1920, aspect: "9:16", orientation: "portrait", referenceSize: "1080x1920"},
		{name: "landscape 480p", width: 854, height: 480, aspect: "16:9", orientation: "landscape", referenceSize: "854x480"},
		{name: "landscape 720p", width: 1280, height: 720, aspect: "16:9", orientation: "landscape", referenceSize: "1280x720"},
		{name: "landscape 1.5K asset", width: 1536, height: 864, aspect: "16:9", orientation: "landscape", referenceSize: "1536x864"},
		{name: "landscape 1080p", width: 1920, height: 1080, aspect: "16:9", orientation: "landscape", referenceSize: "1920x1080"},
		{name: "square 480p", width: 480, height: 480, aspect: "1:1", orientation: "square", referenceSize: "480x480"},
		{name: "square 720p", width: 720, height: 720, aspect: "1:1", orientation: "square", referenceSize: "720x720"},
		{name: "square 1.5K asset", width: 1536, height: 1536, aspect: "1:1", orientation: "square", referenceSize: "1536x1536"},
		{name: "square 1080p", width: 1080, height: 1080, aspect: "1:1", orientation: "square", referenceSize: "1080x1080"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			prompt := codexImagePrompt(domain.GenerationInputSnapshot{
				Prompt: "A cinematic frame", OutputWidth: test.width, OutputHeight: test.height,
			})
			for _, want := range []string{
				"native " + test.aspect + " " + test.orientation + " canvas",
				"using " + test.referenceSize + " pixels as the reference generation size",
				fmt.Sprintf("final delivery size is %dx%d pixels", test.width, test.height),
				"Do not add borders, letterboxing, pillarboxing, or an inset frame",
				"central 80% safe area",
				"A cinematic frame",
			} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("prompt %q does not contain %q", prompt, want)
				}
			}
			if strings.Contains(prompt, "240:427") || strings.Contains(prompt, "427:240") {
				t.Fatalf("prompt uses a nonstandard 480p aspect ratio: %q", prompt)
			}
		})
	}
}

func TestImageGeneratorRejectsNonSubscriptionAuthentication(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	formal := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "account/read":
			return map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}, nil
		default:
			return nil, errors.New("unexpected formal call: " + method)
		}
	}}
	generator, _ := testImageGenerator(t, root, formal)
	if _, err := generator.Capability(context.Background()); err == nil || !strings.Contains(err.Error(), "subscription login") {
		t.Fatalf("Capability() error = %v", err)
	}
	formal.mu.Lock()
	closed := formal.closed
	formal.mu.Unlock()
	if !closed {
		t.Fatal("formal client was not closed after authentication rejection")
	}
}

func TestImageGeneratorMapsSubscriptionQuotaExhaustion(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	turnRequests := make(chan struct{}, 1)
	formal := &fakeProtocolClient{}
	formal.handler = imageRuntimeHandler(func(map[string]any) { turnRequests <- struct{}{} })
	generator, _ := testImageGenerator(t, root, formal)
	capability, err := generator.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := generator.Start(context.Background(), validImageGeneratorRequest(root, capability.CapabilityVersion, 0))
	if err != nil {
		t.Fatal(err)
	}
	<-turnRequests
	formal.emit("item/completed", map[string]any{"threadId": "thr_image", "item": map[string]any{
		"id": "img_limited", "type": "imageGeneration", "status": "failed",
		"failure": map[string]any{"type": "usageLimitExceeded", "limitId": "images", "resetsAt": 1_788_000_000},
	}})
	formal.emit("turn/completed", map[string]any{"threadId": "thr_image", "turn": map[string]any{"id": "turn_image", "status": "failed", "error": map[string]any{"message": "usage limit"}}})
	status := pollImageTerminal(t, generator, operation)
	if status.State != generation.GeneratorFailed || status.FailureCode != "codex_image_quota_exhausted" || status.Validate() != nil {
		t.Fatalf("status = %#v", status)
	}
}

func TestImageGeneratorRejectsMultipleOrUnsafeResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		paths func(string) []string
	}{
		{name: "multiple", paths: func(root string) []string {
			first := filepath.Join(root, "one.png")
			second := filepath.Join(root, "two.png")
			_ = os.WriteFile(first, []byte("one"), 0o600)
			_ = os.WriteFile(second, []byte("two"), 0o600)
			return []string{first, second}
		}},
		{name: "symlink", paths: func(root string) []string {
			target := filepath.Join(root, "target.png")
			link := filepath.Join(root, "link.png")
			_ = os.WriteFile(target, []byte("image"), 0o600)
			_ = os.Symlink(target, link)
			return []string{link}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Clean(t.TempDir())
			turnRequests := make(chan struct{}, 1)
			formal := &fakeProtocolClient{}
			formal.handler = imageRuntimeHandler(func(map[string]any) { turnRequests <- struct{}{} })
			generator, _ := testImageGenerator(t, root, formal)
			capability, err := generator.Capability(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			operation, err := generator.Start(context.Background(), validImageGeneratorRequest(root, capability.CapabilityVersion, 0))
			if err != nil {
				t.Fatal(err)
			}
			<-turnRequests
			for index, path := range test.paths(root) {
				formal.emit("item/completed", map[string]any{"threadId": "thr_image", "item": map[string]any{"id": "img_" + strconv.Itoa(index), "type": "imageGeneration", "status": "completed", "savedPath": path}})
			}
			formal.emit("turn/completed", map[string]any{"threadId": "thr_image", "turn": map[string]any{"id": "turn_image", "status": "completed"}})
			status := pollImageTerminal(t, generator, operation)
			if status.State != generation.GeneratorFailed || status.FailureCode != "codex_protocol_error" || status.Validate() != nil {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestImageGeneratorEnforcesFiveActiveOperations(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	var mutex sync.Mutex
	threadNumber := 0
	formal := &fakeProtocolClient{handler: func(method string, _ map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "account/read":
			return map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}, nil
		case "modelProvider/capabilities/read":
			return map[string]any{"imageGeneration": true}, nil
		case "thread/start":
			mutex.Lock()
			threadNumber++
			value := threadNumber
			mutex.Unlock()
			return map[string]any{"thread": map[string]any{"id": "thr_" + strconv.Itoa(value)}}, nil
		case "turn/start":
			return map[string]any{"turn": map[string]any{"id": "turn_pending"}}, nil
		case "turn/interrupt":
			return map[string]any{}, nil
		default:
			return nil, errors.New("unexpected formal call: " + method)
		}
	}}
	generator, _ := testImageGenerator(t, root, formal)
	capability, err := generator.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 5; index++ {
		if _, err := generator.Start(context.Background(), validImageGeneratorRequest(root, capability.CapabilityVersion, index)); err != nil {
			t.Fatalf("Start(%d) error = %v", index, err)
		}
	}
	if _, err := generator.Start(context.Background(), validImageGeneratorRequest(root, capability.CapabilityVersion, 5)); err == nil || !strings.Contains(err.Error(), "concurrency limit") {
		t.Fatalf("sixth Start() error = %v", err)
	}
}

func testImageGenerator(t *testing.T, root string, formal *fakeProtocolClient) (*ImageGenerator, *[][]string) {
	t.Helper()
	probe := standardProbe()
	clients := []*fakeProtocolClient{probe, formal}
	starts := &[][]string{}
	deps := dependencies{
		discover: func(string) (string, error) { return "/bin/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.153.0", nil },
		newClient: func(_ string, args []string, _ string, _ []string, _ time.Duration, notify func(rpcNotification), exit func(error)) (protocolClient, error) {
			*starts = append(*starts, append([]string(nil), args...))
			client := clients[0]
			clients = clients[1:]
			client.onNotification = notify
			client.onExit = exit
			return client, nil
		},
	}
	generator, err := newImageGenerator(ImageGeneratorOptions{WorkingDirectory: root, ClientVersion: "test", GenerationTimeout: time.Minute}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := generator.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return generator, starts
}

func imageRuntimeHandler(onTurn func(map[string]any)) func(string, map[string]any) (any, error) {
	return func(method string, params map[string]any) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{}, nil
		case "account/read":
			return map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}, nil
		case "modelProvider/capabilities/read":
			return map[string]any{"imageGeneration": true}, nil
		case "thread/start":
			config, ok := params["config"].(map[string]any)
			if !ok || config["web_search"] != "disabled" {
				return nil, errors.New("image Thread did not disable web search")
			}
			return map[string]any{"thread": map[string]any{"id": "thr_image"}}, nil
		case "turn/start":
			onTurn(params)
			return map[string]any{"turn": map[string]any{"id": "turn_image"}}, nil
		case "turn/interrupt":
			return map[string]any{}, nil
		default:
			return nil, errors.New("unexpected formal call: " + method)
		}
	}
}

func validImageGeneratorRequest(root, capabilityVersion string, index int) generation.GeneratorRequest {
	suffix := strings.Repeat("A", 25) + string(rune('A'+index))
	return generation.GeneratorRequest{
		JobID:       domain.GenerationJobID("gjb_" + suffix),
		ProjectRoot: root,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion:      domain.GenerationSnapshotVersion,
			TargetMediaKind:    domain.MediaKindImage,
			Prompt:             "A quiet observatory above the clouds",
			GeneratorID:        domain.GeneratorImageCodex,
			CapabilityVersion:  capabilityVersion,
			OutputWidth:        1920,
			OutputHeight:       1080,
			ParametersJSON:     "{}",
			TargetSlotRevision: 1,
		},
	}
}

func pollImageTerminal(t *testing.T, generator *ImageGenerator, operation generation.GeneratorOperation) generation.GeneratorStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := generator.Poll(context.Background(), operation)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != generation.GeneratorRunning {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Codex image operation did not reach a terminal state")
	return generation.GeneratorStatus{}
}

func TestImageGeneratorLogsUnexpectedAppServerExit(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	root := filepath.Clean(t.TempDir())
	generator, err := newImageGenerator(ImageGeneratorOptions{WorkingDirectory: root, Logger: logger}, dependencies{
		discover: func(string) (string, error) { return "/bin/codex", nil },
		version:  func(context.Context, string) (string, error) { return "0.153.0", nil },
		newClient: func(string, []string, string, []string, time.Duration, func(rpcNotification), func(error)) (protocolClient, error) {
			return nil, errors.New("unused")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operationID := "codex-image/gjb_SXD6BBC3COFTUKTBLJHMUO25UI"
	done := make(chan struct{})
	generator.clientEpoch = 1
	generator.operations[operationID] = &imageOperation{
		status: generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "generating"},
		done:   done,
	}
	generator.active = 1
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1; stderr: fatal transport error")
	generator.watchExit(1, exited)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("image operation was not failed after app-server exit")
	}
	message := logs.String()
	for _, expected := range []string{"Codex image app-server disconnected", "fatal transport error", operationID, "affected_operation_count=1"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("log %q does not contain %q", message, expected)
		}
	}
}
