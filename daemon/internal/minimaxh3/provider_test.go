package minimaxh3

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestOfficialH3CheckStartPollAndDownloadContract(t *testing.T) {
	t.Parallel()
	var queries atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "api.minimax.io" && request.Header.Get("Authorization") != "Bearer minimax-private" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodGet && request.URL.String() == baseURL+"/v1/models":
			return jsonResponse(http.StatusOK, `{"object":"list","data":[]}`), nil
		case request.Method == http.MethodPost && request.URL.String() == baseURL+"/v2/video_generation":
			var payload createRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Model != "MiniMax-H3" || payload.Resolution != "768P" || payload.Duration != 5 || payload.Ratio != "adaptive" || len(payload.Content) != 2 || payload.Content[1].Role != "first_frame" || payload.Content[1].ImageURL == nil || !strings.HasPrefix(payload.Content[1].ImageURL.URL, "data:image/png;base64,") {
				t.Fatalf("create payload = %#v", payload)
			}
			return jsonResponse(http.StatusOK, `{"task_id":"task_123"}`), nil
		case request.Method == http.MethodGet && request.URL.String() == baseURL+"/v2/query/video_generation/task_123":
			if queries.Add(1) == 1 {
				return jsonResponse(http.StatusOK, `{"task":{"id":"task_123","model":"MiniMax-H3","status":"running","content":{}}}`), nil
			}
			return jsonResponse(http.StatusOK, `{"task":{"id":"task_123","model":"MiniMax-H3","status":"succeeded","content":{"url":"https://cdn.example/output.mp4"}}}`), nil
		case request.Method == http.MethodGet && request.URL.String() == "https://cdn.example/output.mp4":
			if request.Header.Get("Authorization") != "" {
				t.Fatal("API key was forwarded to result CDN")
			}
			return bytesResponse(http.StatusOK, "synthetic-video"), nil
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
			return nil, nil
		}
	})}
	provider, err := New(Options{Configuration: readyConfiguration{}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	check, err := provider.Check(context.Background(), readyRecord(), "minimax-private")
	if err != nil || check.CapabilityVersion != CapabilityVersion {
		t.Fatalf("Check() = %#v, %v", check, err)
	}
	request := h3Request(t)
	operation, err := provider.Start(context.Background(), request)
	if err != nil || operation.ID != "task_123" {
		t.Fatalf("Start() = %#v, %v", operation, err)
	}
	status, err := provider.Poll(context.Background(), operation)
	if err != nil || status.State != generation.GeneratorRunning {
		t.Fatalf("Poll(running) = %#v, %v", status, err)
	}
	status, err = provider.Poll(context.Background(), operation)
	if err != nil || status.State != generation.GeneratorSucceeded || status.Validate() != nil {
		t.Fatalf("Poll(succeeded) = %#v, %v", status, err)
	}
	data, err := os.ReadFile(status.Outputs[0].SourcePath)
	if err != nil || string(data) != "synthetic-video" {
		t.Fatalf("download = %q, %v", data, err)
	}
}

func TestOfficialH3ResolutionAndPollingContract(t *testing.T) {
	t.Parallel()
	if defaultResolution("MiniMax-H3", 720, 1280) != "768P" || defaultResolution("MiniMax-H3", 1280, 720) != "768P" || defaultResolution("MiniMax-H3", 1080, 1920) != "2K" || defaultResolution("MiniMax-H3-Max", 1080, 1920) != "768P" || defaultResolution("MiniMax-H3-Max", 480, 854) != "480P" {
		t.Fatal("automatic H3 resolution selection does not preserve project output quality")
	}
	provider, err := New(Options{Configuration: readyConfiguration{}, WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if provider.PollInterval() != 10*time.Second {
		t.Fatalf("PollInterval() = %s", provider.PollInterval())
	}
}

func TestRegionalH3RoutesAndH3MaxContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		spec        routeSpec
		constructor func(Options) (*Provider, error)
	}{
		{name: "China", spec: chinaSpec, constructor: NewChina},
		{name: "Global", spec: globalSpec, constructor: NewGlobal},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := readyRecord()
			record.GeneratorID = test.spec.id
			record.EndpointPreset = test.spec.endpointPreset
			record.ModelID = "MiniMax-H3-Max"
			record.CapabilityVersion = test.spec.capabilityVersion
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != test.spec.baseURL+"/v1/models" || request.Header.Get("Authorization") != "Bearer region-secret" {
					t.Fatalf("regional check request = %s, Authorization = %q", request.URL, request.Header.Get("Authorization"))
				}
				return jsonResponse(http.StatusOK, `{"object":"list","data":[]}`), nil
			})}
			provider, err := test.constructor(Options{Configuration: staticConfiguration{record: record, secret: "region-secret"}, WorkingDirectory: t.TempDir(), HTTPClient: client})
			if err != nil || provider.ID() != test.spec.id {
				t.Fatalf("Provider = %#v, %v", provider, err)
			}
			check, err := provider.Check(context.Background(), record, "region-secret")
			if err != nil || check.CapabilityVersion != test.spec.capabilityVersion {
				t.Fatalf("Check() = %#v, %v", check, err)
			}
			capability, err := provider.CapabilityModel(context.Background(), "MiniMax-H3-Max")
			if err != nil || capability.GeneratorID != test.spec.id || capability.ProviderModelID != "MiniMax-H3-Max" || capability.MinimumDurationSeconds != 5 || capability.MaximumDurationSeconds != 15 {
				t.Fatalf("H3 Max capability = %#v, %v", capability, err)
			}
		})
	}

	request := h3Request(t)
	request.Snapshot.ProviderModelID = "MiniMax-H3-Max"
	request.Snapshot.RequestedDurationSeconds = 5
	payload, err := buildRequest(request)
	if err != nil || payload.Model != "MiniMax-H3-Max" || payload.Resolution != "768P" {
		t.Fatalf("H3 Max payload = %#v, %v", payload, err)
	}
	request.Snapshot.RequestedDurationSeconds = 4
	if _, err := buildRequest(request); err == nil {
		t.Fatal("H3 Max accepted a four-second request")
	}
	request.Snapshot.RequestedDurationSeconds = 5
	request.Snapshot.ParametersJSON = `{"resolution":"2K"}`
	if _, err := buildRequest(request); err == nil {
		t.Fatal("H3 Max accepted 2K output")
	}
}

func TestOfficialH3ErrorsNeverExposeCredentialOrUpstreamBody(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return bytesResponse(http.StatusUnauthorized, `{"error":{"code":"invalid_api_key","message":"minimax-private rejected"},"debug":"must-not-be-public"}`), nil
	})}
	provider, _ := New(Options{Configuration: readyConfiguration{}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	_, err := provider.Check(context.Background(), readyRecord(), "minimax-private")
	if err == nil || strings.Contains(err.Error(), "minimax-private") || strings.Contains(err.Error(), "must-not-be-public") || !strings.Contains(err.Error(), "invalid_api_key") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestOfficialH3RejectsPreparedReferenceAndUnsafeDownload(t *testing.T) {
	t.Parallel()
	request := h3Request(t)
	request.References[0].Input.PreparationRequired = true
	request.Snapshot.References[0].PreparationRequired = true
	if _, err := buildRequest(request); err == nil {
		t.Fatal("buildRequest() accepted an unavailable derivative")
	}
	provider, _ := New(Options{Configuration: readyConfiguration{}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	if _, err := provider.download(context.Background(), "task", "http://127.0.0.1/internal"); err == nil {
		t.Fatal("download() accepted an unsafe URL")
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

func h3Request(t *testing.T) generation.GeneratorRequest {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "first.png")
	data := []byte("synthetic-png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	jobID := domain.GenerationJobID("gjb_" + strings.Repeat("J", 26))
	at := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	artifact := domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/images/first.png",
		SHA256: strings.Repeat("a", 64), ByteSize: int64(len(data)), MIMEType: "image/png", OriginalFilename: "first.png", Width: 720, Height: 1280, CreatedAt: at}
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceFirstImage, ArtifactID: artifactID}
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo,
		Prompt: "A slow camera move", GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: domain.GeneratorVideoH3Official,
		ProviderModelID: "MiniMax-H3", ProviderConnectionRevision: 2, CapabilityVersion: CapabilityVersion, OutputWidth: 720, OutputHeight: 1280, RequestedDurationSeconds: 5,
		References: []domain.GenerationArtifactReference{input}, ParametersJSON: "{}", TargetSlotRevision: 1}
	request := generation.GeneratorRequest{JobID: jobID, Snapshot: snapshot, ProjectRoot: root,
		References: []generation.GeneratorArtifactReference{{Input: input, Artifact: artifact, AbsolutePath: path}}}
	if err := request.Validate(); err != nil {
		t.Fatalf("test request invalid: %v", err)
	}
	return request
}

func readyRecord() providerconfig.Record {
	return providerconfig.Record{Profile: providerconfig.Profile{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: providerconfig.EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", Enabled: true, ConcurrencyLimit: 2,
		CapabilityVersion: CapabilityVersion, Health: providerconfig.HealthReady, Revision: 2}, CredentialRef: "pcr_H3"}
}

type readyConfiguration struct{}

func (readyConfiguration) RuntimeProvider(context.Context, domain.GeneratorID) (providerconfig.Record, string, error) {
	return readyRecord(), "minimax-private", nil
}

type staticConfiguration struct {
	record providerconfig.Record
	secret string
}

func (c staticConfiguration) RuntimeProvider(context.Context, domain.GeneratorID) (providerconfig.Record, string, error) {
	return c.record, c.secret, nil
}

func (c staticConfiguration) RuntimeProviderModel(_ context.Context, _ domain.GeneratorID, modelID string) (providerconfig.Record, string, error) {
	c.record.ModelID = modelID
	return c.record, c.secret, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func jsonResponse(status int, body string) *http.Response {
	response := bytesResponse(status, body)
	response.Header.Set("Content-Type", "application/json")
	return response
}
func bytesResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
