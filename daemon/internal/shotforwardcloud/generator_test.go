package shotforwardcloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestImageGeneratorUsesDirectCloudTaskAPI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	referencePath := filepath.Join(root, "reference.png")
	if err := os.WriteFile(referencePath, []byte("reference-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	var createdTask map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/media-inputs":
			return jsonResponse(http.StatusCreated, map[string]any{
				"id": "input-1",
				"upload": map[string]any{
					"url":              "https://cloud.example/uploads/input-1",
					"required_headers": map[string]string{"Content-Type": "image/png"},
				},
			}), nil
		case "/uploads/input-1":
			content, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPut || string(content) != "reference-image" || request.Header.Get("Authorization") != "Bearer access-1" {
				t.Errorf("unexpected upload request: method=%s authorization=%q body=%q", request.Method, request.Header.Get("Authorization"), content)
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "input-1", "status": "ready", "media_type": "image/png",
				"byte_size": len("reference-image"), "width": 1024, "height": 1024,
			}), nil
		case "/v1/image-tasks":
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("image task did not include an idempotency key")
			}
			if err := json.NewDecoder(request.Body).Decode(&createdTask); err != nil {
				t.Errorf("decode image task: %v", err)
			}
			return jsonResponse(http.StatusCreated, map[string]any{"id": "task-1", "kind": "image", "status": "queued"}), nil
		case "/v1/tasks/task-1":
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "task-1", "kind": "image", "status": "succeeded",
				"outputs": []map[string]string{{"id": "artifact-1", "media_type": "image/png"}},
			}), nil
		case "/v1/tasks/task-1/artifacts/artifact-1/access":
			return jsonResponse(http.StatusOK, map[string]any{"url": "https://objects.example/artifact.png"}), nil
		case "/artifact.png":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("generated-image"))}, nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(manager, domain.GeneratorImageShotForward, filepath.Join(root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}

	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: artifactID}
	request := generation.GeneratorRequest{
		JobID:       domain.GenerationJobID("gjb_" + strings.Repeat("J", 26)),
		ProjectRoot: root,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindImage,
			Prompt: "A cinematic city", GeneratorID: domain.GeneratorImageShotForward,
			ProviderModelID: "shotforward-image-1", ProviderConnectionRevision: 1, CapabilityVersion: cloudImageCapabilityVersion,
			OutputWidth: 1536, OutputHeight: 864, References: []domain.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
		References: []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/reference.png",
				SHA256: strings.Repeat("a", 64), ByteSize: int64(len("reference-image")), MIMEType: "image/png", OriginalFilename: "reference.png",
				Width: 1024, Height: 1024, CreatedAt: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
			AbsolutePath: referencePath,
		}},
	}
	operation, err := generator.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID != "task-1" || createdTask["model"] != "shotforward-image-1" || createdTask["size"] != "1536x864" {
		t.Fatalf("operation=%#v task=%#v", operation, createdTask)
	}
	inputIDs, ok := createdTask["input_artifact_ids"].([]any)
	if !ok || len(inputIDs) != 1 || inputIDs[0] != "input-1" {
		t.Fatalf("input_artifact_ids=%#v", createdTask["input_artifact_ids"])
	}

	status, err := generator.Poll(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != generation.GeneratorSucceeded || len(status.Outputs) != 1 {
		t.Fatalf("status=%#v", status)
	}
	content, err := os.ReadFile(status.Outputs[0].SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "generated-image" {
		t.Fatalf("downloaded output=%q", content)
	}
}

func TestCloudImageSizeUsesNativeAssetCanvasesAndLegacyFrameCanvases(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		width, height int
		want          string
	}{
		{name: "portrait asset", width: 864, height: 1536, want: "864x1536"},
		{name: "landscape asset", width: 1536, height: 864, want: "1536x864"},
		{name: "square asset", width: 1536, height: 1536, want: "1536x1536"},
		{name: "portrait frame", width: 1080, height: 1920, want: "720x1280"},
		{name: "landscape frame", width: 1920, height: 1080, want: "1280x720"},
		{name: "square frame", width: 720, height: 720, want: "1024x1024"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := cloudImageSize(test.width, test.height); got != test.want {
				t.Fatalf("cloudImageSize(%d, %d) = %q, want %q", test.width, test.height, got, test.want)
			}
		})
	}
}

func TestImageGeneratorRejectsReferenceUploadThatIsNotReady(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	referencePath := filepath.Join(root, "reference.png")
	if err := os.WriteFile(referencePath, []byte("reference-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	taskCreated := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/media-inputs":
			return jsonResponse(http.StatusCreated, map[string]any{
				"id": "input-1",
				"upload": map[string]any{
					"url":              "https://cloud.example/uploads/input-1",
					"required_headers": map[string]string{"Content-Type": "image/png"},
				},
			}), nil
		case "/uploads/input-1":
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "input-1", "status": "processing", "media_type": "image/png",
				"byte_size": len("reference-image"), "width": 1024, "height": 1024,
			}), nil
		case "/v1/image-tasks":
			taskCreated = true
			t.Fatal("image task was created before its reference upload became ready")
			return nil, nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(manager, domain.GeneratorImageShotForward, filepath.Join(root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}

	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: artifactID}
	_, err = generator.Start(context.Background(), generation.GeneratorRequest{
		JobID:       domain.GenerationJobID("gjb_" + strings.Repeat("J", 26)),
		ProjectRoot: root,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindImage,
			Prompt: "A cinematic city", GeneratorID: domain.GeneratorImageShotForward,
			ProviderModelID: "shotforward-image-1", ProviderConnectionRevision: 1, CapabilityVersion: cloudImageCapabilityVersion,
			OutputWidth: 1280, OutputHeight: 720, References: []domain.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
		References: []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/reference.png",
				SHA256: strings.Repeat("a", 64), ByteSize: int64(len("reference-image")), MIMEType: "image/png", OriginalFilename: "reference.png",
				Width: 1024, Height: 1024, CreatedAt: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
			AbsolutePath: referencePath,
		}},
	})
	code, message := generation.PublicGeneratorError("generation_start_failed", err)
	if err == nil || code != generation.GeneratorProtocolFailureCode || message != ErrCloudResponse.Error() {
		t.Fatalf("Start() error=%v public failure=(%q, %q)", err, code, message)
	}
	if taskCreated {
		t.Fatal("image task was created with a non-ready reference upload")
	}
}

func TestCloudStopContinuesPollingWhenTaskWasAlreadyClaimed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		canCancel bool
		postCode  int
		wantPost  bool
	}{{"already_running", false, 0, false}, {"claim_raced_cancel", true, http.StatusConflict, true}} {
		t.Run(test.name, func(t *testing.T) {
			posted := false
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch {
				case request.URL.Path == "/v1/auth/desktop-code/exchanges":
					return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
				case request.URL.Path == "/v1/tasks/task-1" && request.Method == http.MethodGet:
					return jsonResponse(http.StatusOK, map[string]any{"id": "task-1", "status": "running", "can_cancel": test.canCancel}), nil
				case request.URL.Path == "/v1/tasks/task-1/cancel" && request.Method == http.MethodPost:
					posted = true
					return jsonResponse(test.postCode, map[string]any{"error": map[string]any{"code": "task_not_cancellable"}}), nil
				default:
					t.Fatalf("unexpected request %s %s", request.Method, request.URL)
					return nil, nil
				}
			})}
			manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
			if err != nil {
				t.Fatal(err)
			}
			if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
				t.Fatal(err)
			}
			generator, err := NewGenerator(manager, domain.GeneratorImageShotForward, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = generator.Stop(context.Background(), generation.GeneratorOperation{ID: "task-1"}); err != nil {
				t.Fatal(err)
			}
			if posted != test.wantPost {
				t.Fatalf("posted=%t want=%t", posted, test.wantPost)
			}
		})
	}
}

func TestVideoGeneratorUsesDirectCloudTaskAPI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	referencePath := filepath.Join(root, "first-frame.png")
	if err := os.WriteFile(referencePath, []byte("first-frame-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	var createdTask map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/media-inputs":
			return jsonResponse(http.StatusCreated, map[string]any{
				"id": "input-first-frame",
				"upload": map[string]any{
					"url":              "https://cloud.example/uploads/input-first-frame",
					"required_headers": map[string]string{"Content-Type": "image/png"},
				},
			}), nil
		case "/uploads/input-first-frame":
			content, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPut || string(content) != "first-frame-image" || request.Header.Get("Authorization") != "Bearer access-1" {
				t.Errorf("unexpected upload request: method=%s authorization=%q body=%q", request.Method, request.Header.Get("Authorization"), content)
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "input-first-frame", "status": "ready", "media_type": "image/png",
				"byte_size": len("first-frame-image"), "width": 720, "height": 1280,
			}), nil
		case "/v1/video-tasks":
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("video task did not include an idempotency key")
			}
			if err := json.NewDecoder(request.Body).Decode(&createdTask); err != nil {
				t.Errorf("decode video task: %v", err)
			}
			return jsonResponse(http.StatusAccepted, map[string]any{"id": "task-video-1", "kind": "video", "status": "queued"}), nil
		case "/v1/tasks/task-video-1":
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "task-video-1", "kind": "video", "status": "succeeded",
				"outputs": []map[string]string{{"id": "artifact-video-1", "media_type": "video/mp4"}},
			}), nil
		case "/v1/tasks/task-video-1/artifacts/artifact-video-1/access":
			return jsonResponse(http.StatusOK, map[string]any{"url": "https://objects.example/artifact.mp4"}), nil
		case "/artifact.mp4":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("generated-video"))}, nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(manager, domain.GeneratorVideoH3ShotForward, filepath.Join(root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}

	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceFirstImage, ArtifactID: artifactID}
	request := generation.GeneratorRequest{
		JobID:       domain.GenerationJobID("gjb_" + strings.Repeat("V", 26)),
		ProjectRoot: root,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo,
			Prompt: "A slow cinematic camera move", GenerationMode: domain.ClipGenerationFirstImage,
			GeneratorID: domain.GeneratorVideoH3ShotForward, ProviderModelID: "h3-video",
			ProviderConnectionRevision: 1, CapabilityVersion: cloudH3CapabilityVersion,
			OutputWidth: 1280, OutputHeight: 720, RequestedDurationSeconds: 5,
			References: []domain.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
		References: []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/first-frame.png",
				SHA256: strings.Repeat("b", 64), ByteSize: int64(len("first-frame-image")), MIMEType: "image/png", OriginalFilename: "first-frame.png",
				Width: 1280, Height: 720, CreatedAt: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
			AbsolutePath: referencePath,
		}},
	}
	operation, err := generator.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID != "task-video-1" || createdTask["model"] != "h3-video" || createdTask["duration"] != float64(5) || createdTask["size"] != "1280x720" {
		t.Fatalf("operation=%#v task=%#v", operation, createdTask)
	}
	inputs, ok := createdTask["inputs"].([]any)
	if !ok || len(inputs) != 1 {
		t.Fatalf("routed video inputs=%#v", createdTask["inputs"])
	}
	first, firstOK := inputs[0].(map[string]any)
	if !firstOK || first["artifact_id"] != "input-first-frame" || first["role"] != "first_image" || createdTask["generation_mode"] != "first_image" {
		t.Fatalf("routed video inputs=%#v mode=%#v", createdTask["inputs"], createdTask["generation_mode"])
	}

	status, err := generator.Poll(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != generation.GeneratorSucceeded || len(status.Outputs) != 1 || filepath.Ext(status.Outputs[0].SourcePath) != ".mp4" {
		t.Fatalf("status=%#v", status)
	}
	content, err := os.ReadFile(status.Outputs[0].SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "generated-video" {
		t.Fatalf("downloaded output=%q", content)
	}
}

func TestVideoGeneratorRoutesAudioReference(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	referencePath := filepath.Join(root, "dialogue.mp3")
	if err := os.WriteFile(referencePath, []byte("reference-audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	var createdTask map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/media-inputs":
			return jsonResponse(http.StatusCreated, map[string]any{
				"id": "input-audio",
				"upload": map[string]any{
					"url":              "https://cloud.example/uploads/input-audio",
					"required_headers": map[string]string{"Content-Type": "audio/mpeg"},
				},
			}), nil
		case "/uploads/input-audio":
			return jsonResponse(http.StatusOK, map[string]any{
				"id": "input-audio", "status": "ready", "media_type": "audio/mpeg",
				"byte_size": len("reference-audio"),
			}), nil
		case "/v1/video-tasks":
			if err := json.NewDecoder(request.Body).Decode(&createdTask); err != nil {
				t.Errorf("decode video task: %v", err)
			}
			return jsonResponse(http.StatusAccepted, map[string]any{"id": "task-audio-1", "kind": "video", "status": "queued"}), nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(manager, domain.GeneratorVideoH3ShotForward, filepath.Join(root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}

	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("U", 26))
	reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceAudio, ArtifactID: artifactID, TrimEndMilliseconds: 10_000}
	operation, err := generator.Start(context.Background(), generation.GeneratorRequest{
		JobID:       domain.GenerationJobID("gjb_" + strings.Repeat("A", 26)),
		ProjectRoot: root,
		Snapshot: domain.GenerationInputSnapshot{
			SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo,
			Prompt: "Follow the reference dialogue", GenerationMode: domain.ClipGenerationReference,
			GeneratorID: domain.GeneratorVideoH3ShotForward, ProviderModelID: "h3-ref2va",
			ProviderConnectionRevision: 1, CapabilityVersion: cloudH3CapabilityVersion,
			OutputWidth: 1280, OutputHeight: 720, RequestedDurationSeconds: 10,
			References: []domain.GenerationArtifactReference{reference}, ParametersJSON: "{}", TargetSlotRevision: 1,
		},
		References: []generation.GeneratorArtifactReference{{
			Input: reference,
			Artifact: domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindAudio, RelativePath: "media/audio/dialogue.mp3",
				SHA256: strings.Repeat("c", 64), ByteSize: int64(len("reference-audio")), MIMEType: "audio/mpeg", OriginalFilename: "dialogue.mp3",
				DurationMilliseconds: 10_000, AudioSampleRate: 44_100, AudioChannels: 2, CreatedAt: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)},
			AbsolutePath: referencePath,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs, ok := createdTask["inputs"].([]any)
	if operation.ID != "task-audio-1" || !ok || len(inputs) != 1 {
		t.Fatalf("operation=%#v routed inputs=%#v", operation, createdTask["inputs"])
	}
	input, ok := inputs[0].(map[string]any)
	if !ok || input["artifact_id"] != "input-audio" || input["role"] != "audio_reference" {
		t.Fatalf("routed audio input=%#v", inputs[0])
	}
}

func TestVideoCatalogPublishesContinuousDurationRange(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/models":
			return jsonResponse(http.StatusOK, map[string]any{"data": []Model{{ID: "h3-fl2va", ProductKind: "video"}, {ID: "h3-ref2va", ProductKind: "video"}}}), nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}

	for _, modelID := range []string{"h3-fl2va", "h3-ref2va"} {
		capability, catalogErr := (Catalog{Manager: manager}).GeneratorModel(context.Background(), domain.GeneratorVideoH3ShotForward, modelID)
		if catalogErr != nil {
			t.Fatalf("model %s: %v", modelID, catalogErr)
		}
		if len(capability.SupportedDurations) != 0 || capability.MinimumDurationSeconds != 4 || capability.MaximumDurationSeconds != 15 {
			t.Fatalf("model %s duration capability = discrete %v, range %d-%d", modelID, capability.SupportedDurations, capability.MinimumDurationSeconds, capability.MaximumDurationSeconds)
		}
	}
}

func TestVideoCatalogUsesPublishedCloudReferenceLimits(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/models":
			return jsonResponse(http.StatusOK, map[string]any{"data": []Model{cloudVideoModel("MiniMax-H3-Turbo-Ref2VA", "ref2va", []string{"reference"}, 10)}}), nil
		default:
			t.Fatalf("unexpected cloud request: %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	hosted, err := (Catalog{Manager: manager}).GeneratorModel(context.Background(), domain.GeneratorVideoH3ShotForward, "MiniMax-H3-Turbo-Ref2VA")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosted.ReferenceCapabilities) != 3 || hosted.ReferenceCapabilities[0].MaximumCount != 9 || hosted.ReferenceCapabilities[1].MaximumBytes != 512<<20 || len(hosted.ReferenceCapabilities[2].RequiresAnyOf) != 2 || hosted.MaximumTotalReferences != 12 {
		t.Fatalf("published cloud limits were not preserved: %#v", hosted)
	}
}
