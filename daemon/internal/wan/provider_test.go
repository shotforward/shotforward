package wan

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

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestOfficialContractCheckStartPollAndDownload(t *testing.T) {
	t.Parallel()
	record := readyRecord()
	base, _, err := route(record)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "cdn.example" && request.Header.Get("Authorization") != "Bearer wan-private" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/models":
			if request.URL.Query().Get("providers") != "wan" || request.URL.Query().Get("model") != "wan3.0-video" {
				t.Fatalf("model query = %q", request.URL.RawQuery)
			}
			return response(200, `{"output":{"models":[{"model":"wan3.0-video"}]}}`), nil
		case request.Method == http.MethodPost && request.URL.String() == base+generationPath:
			if request.Header.Get("X-DashScope-Async") != "enable" || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("headers = %#v", request.Header)
			}
			var payload createRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Model != "wan3.0-video" || payload.Parameters.Resolution != "1080P" || payload.Parameters.Ratio != "adaptive" || payload.Parameters.Duration != 5 || !payload.Parameters.Audio || !payload.Parameters.PromptExtend || len(payload.Input.Media) != 1 || payload.Input.Media[0].Type != "first_frame" || !strings.HasPrefix(payload.Input.Media[0].URL, "data:image/png;base64,") {
				t.Fatalf("payload = %#v", payload)
			}
			return response(200, `{"output":{"task_status":"PENDING","task_id":"wan-task-1"}}`), nil
		case request.Method == http.MethodGet && request.URL.String() == base+"/api/v1/tasks/wan-task-1":
			return response(200, `{"output":{"task_id":"wan-task-1","task_status":"SUCCEEDED","video_url":"https://cdn.example/result.mp4"}}`), nil
		case request.Method == http.MethodGet && request.URL.String() == "https://cdn.example/result.mp4":
			if request.Header.Get("Authorization") != "" {
				t.Fatal("credential leaked to result CDN")
			}
			return response(200, "video"), nil
		default:
			t.Fatalf("unexpected route %s %s", request.Method, request.URL)
			return nil, nil
		}
	})}
	provider, err := New(Options{Configuration: fixedConfiguration{record: record}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	check, err := provider.Check(context.Background(), record, "wan-private")
	if err != nil || check.CapabilityVersion != CapabilityVersion {
		t.Fatalf("Check() = %#v, %v", check, err)
	}
	capability, err := provider.Capability(context.Background())
	if err != nil || len(capability.ReferenceCapabilities) != 5 || capability.MinimumDurationSeconds != 4 || capability.MaximumDurationSeconds != 30 || capability.MaximumTotalTimedReferenceDurationMilliseconds != 30_000 {
		t.Fatalf("Capability() = %#v, %v", capability, err)
	}
	operation, err := provider.Start(context.Background(), wanRequest(t))
	if err != nil || operation.ID != "wan-task-1" {
		t.Fatalf("Start() = %#v, %v", operation, err)
	}
	status, err := provider.Poll(context.Background(), operation)
	if err != nil || status.State != generation.GeneratorSucceeded || status.Validate() != nil {
		t.Fatalf("Poll() = %#v, %v", status, err)
	}
	if data, err := os.ReadFile(status.Outputs[0].SourcePath); err != nil || string(data) != "video" {
		t.Fatalf("output = %q, %v", data, err)
	}
}

func TestRoutesRejectUnsupportedRegionAndWorkspaceInjection(t *testing.T) {
	t.Parallel()
	record := readyRecord()
	record.EndpointPreset = providerconfig.EndpointAlibabaHongKong
	if _, _, err := route(record); err == nil {
		t.Fatal("Hong Kong route is not listed for Wan 3.0")
	}
	record = readyRecord()
	record.WorkspaceID = "workspace.example.com/path"
	if _, _, err := route(record); err == nil {
		t.Fatal("workspace injection accepted")
	}
}

func TestBuildRequestRejectsVideoShorterThanFourSeconds(t *testing.T) {
	t.Parallel()
	request := wanRequest(t)
	request.Snapshot.RequestedDurationSeconds = 2
	if _, err := buildRequest(request, "wan3.0-video"); err == nil {
		t.Fatal("two-second Wan request was accepted")
	}
}

func TestBuildRequestUsesTemporaryOSSURLForReferenceAudio(t *testing.T) {
	t.Parallel()
	request := wanRequest(t)
	audioPath := filepath.Join(request.ProjectRoot, "voice.wav")
	audioData := []byte("audio-reference")
	if err := os.WriteFile(audioPath, audioData, 0o600); err != nil {
		t.Fatal(err)
	}
	audioID := domain.ArtifactID("art_" + strings.Repeat("U", 26))
	audioArtifact := domain.Artifact{ID: audioID, ProjectID: request.References[0].Artifact.ProjectID, Kind: domain.MediaKindAudio, RelativePath: "media/audio/voice.wav", SHA256: strings.Repeat("c", 64), ByteSize: int64(len(audioData)), MIMEType: "audio/wav", OriginalFilename: "voice.wav", DurationMilliseconds: 5_000, CreatedAt: request.References[0].Artifact.CreatedAt}
	audioInput := domain.GenerationArtifactReference{Role: domain.GenerationReferenceAudio, ArtifactID: audioID, TrimEndMilliseconds: 5_000}
	request.Snapshot.GenerationMode = domain.ClipGenerationReference
	request.Snapshot.References = []domain.GenerationArtifactReference{audioInput}
	request.References = []generation.GeneratorArtifactReference{{Input: audioInput, Artifact: audioArtifact, AbsolutePath: audioPath}}

	payload, err := buildRequestWithURLs(request, "wan3.0-video", map[domain.ArtifactID]string{audioID: "oss://temporary/voice.wav"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Input.Media) != 1 || payload.Input.Media[0].Type != "reference_audio" || payload.Input.Media[0].URL != "oss://temporary/voice.wav" {
		t.Fatalf("payload media = %#v", payload.Input.Media)
	}
}

func TestStartUploadsReferenceVideoAndUsesTemporaryOSSURL(t *testing.T) {
	t.Parallel()
	record := readyRecord()
	base, uploadBase, err := route(record)
	if err != nil {
		t.Fatal(err)
	}
	request := wanRequest(t)
	videoPath := filepath.Join(request.ProjectRoot, "previous.mp4")
	videoData := []byte("video-reference")
	if err := os.WriteFile(videoPath, videoData, 0o600); err != nil {
		t.Fatal(err)
	}
	videoID := domain.ArtifactID("art_" + strings.Repeat("V", 26))
	videoArtifact := domain.Artifact{ID: videoID, ProjectID: request.References[0].Artifact.ProjectID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/previous.mp4", SHA256: strings.Repeat("b", 64), ByteSize: int64(len(videoData)), MIMEType: "video/mp4", OriginalFilename: "previous.mp4", Width: 720, Height: 1280, DurationMilliseconds: 8_000, FPSNumerator: 24, FPSDenominator: 1, CreatedAt: request.References[0].Artifact.CreatedAt}
	videoInput := domain.GenerationArtifactReference{Role: domain.GenerationReferenceVideo, ArtifactID: videoID, TrimEndMilliseconds: 8_000}
	request.Snapshot.GenerationMode = domain.ClipGenerationReference
	request.Snapshot.References = []domain.GenerationArtifactReference{videoInput}
	request.References = []generation.GeneratorArtifactReference{{Input: videoInput, Artifact: videoArtifact, AbsolutePath: videoPath}}

	client := &http.Client{Transport: roundTripFunc(func(httpRequest *http.Request) (*http.Response, error) {
		switch {
		case httpRequest.Method == http.MethodGet && strings.HasPrefix(httpRequest.URL.String(), uploadBase+"/api/v1/uploads?"):
			return response(200, `{"data":{"policy":"policy","signature":"signature","upload_dir":"dashscope-instant/account/job","upload_host":"https://uploads.example.aliyuncs.com","max_file_size_mb":100,"oss_access_key_id":"access","x_oss_object_acl":"private","x_oss_forbid_overwrite":"true"}}`), nil
		case httpRequest.Method == http.MethodPost && httpRequest.URL.String() == "https://uploads.example.aliyuncs.com":
			if err := httpRequest.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			file, _, err := httpRequest.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if string(data) != string(videoData) || httpRequest.FormValue("key") != "dashscope-instant/account/job/previous.mp4" {
				t.Fatalf("uploaded video = %q, key = %q", data, httpRequest.FormValue("key"))
			}
			return response(200, ""), nil
		case httpRequest.Method == http.MethodPost && httpRequest.URL.String() == base+generationPath:
			if httpRequest.Header.Get("X-DashScope-OssResourceResolve") != "enable" {
				t.Fatal("OSS resolution header is missing")
			}
			var payload createRequest
			if err := json.NewDecoder(httpRequest.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Input.Media) != 1 || payload.Input.Media[0].Type != "reference_video" || payload.Input.Media[0].URL != "oss://dashscope-instant/account/job/previous.mp4" {
				t.Fatalf("payload media = %#v", payload.Input.Media)
			}
			return response(200, `{"output":{"task_status":"PENDING","task_id":"wan-video-task"}}`), nil
		default:
			t.Fatalf("unexpected route %s %s", httpRequest.Method, httpRequest.URL)
			return nil, nil
		}
	})}
	provider, err := New(Options{Configuration: fixedConfiguration{record: record}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := provider.Start(context.Background(), request)
	if err != nil || operation.ID != "wan-video-task" {
		t.Fatalf("Start() = %#v, %v", operation, err)
	}
}

func TestStatusErrorMarksOnlyRateLimitsAndServerErrorsRetryable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		if !generation.IsRetryablePollError(statusError(status)) {
			t.Fatalf("status %d was not retryable", status)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusNotFound} {
		if generation.IsRetryablePollError(statusError(status)) {
			t.Fatalf("status %d was retryable", status)
		}
	}
}

func TestStartPreservesOfficialProviderError(t *testing.T) {
	t.Parallel()
	record := readyRecord()
	base, _, err := route(record)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != base+generationPath {
			t.Fatalf("unexpected route %s", request.URL)
		}
		result := response(http.StatusBadRequest, `{"request_id":"wan-request-1","code":"InvalidParameter","message":"duration is unsupported"}`)
		return result, nil
	})}
	provider, _ := New(Options{Configuration: fixedConfiguration{record: record}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	_, err = provider.Start(context.Background(), wanRequest(t))
	if err == nil || !strings.Contains(err.Error(), "InvalidParameter") || !strings.Contains(err.Error(), "duration is unsupported") || !strings.Contains(err.Error(), "wan-request-1") {
		t.Fatalf("Start() error = %v", err)
	}
}

func wanRequest(t *testing.T) generation.GeneratorRequest {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "first.png")
	data := []byte("png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	jobID := domain.GenerationJobID("gjb_" + strings.Repeat("J", 26))
	at := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	artifact := domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/images/first.png", SHA256: strings.Repeat("a", 64), ByteSize: int64(len(data)), MIMEType: "image/png", OriginalFilename: "first.png", Width: 720, Height: 1280, CreatedAt: at}
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceFirstImage, ArtifactID: artifactID}
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo, Prompt: "slow movement", GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: "wan3.0-video", ProviderConnectionRevision: 1, CapabilityVersion: CapabilityVersion, OutputWidth: 720, OutputHeight: 1280, RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{input}, ParametersJSON: "{}", TargetSlotRevision: 1}
	result := generation.GeneratorRequest{JobID: jobID, Snapshot: snapshot, ProjectRoot: root, References: []generation.GeneratorArtifactReference{{Input: input, Artifact: artifact, AbsolutePath: path}}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	return result
}

func readyRecord() providerconfig.Record {
	return providerconfig.Record{Profile: providerconfig.Profile{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: providerconfig.EndpointAlibabaChina, ModelID: "wan3.0-video", WorkspaceID: "llm-workspace123", Enabled: true, ConcurrencyLimit: 2, CapabilityVersion: CapabilityVersion, Health: providerconfig.HealthReady, Revision: 2}, CredentialRef: "pcr_wan"}
}

type fixedConfiguration struct{ record providerconfig.Record }

func (c fixedConfiguration) RuntimeProvider(context.Context, domain.GeneratorID) (providerconfig.Record, string, error) {
	return c.record, "wan-private", nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
