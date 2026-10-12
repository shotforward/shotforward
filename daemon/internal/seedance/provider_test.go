package seedance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestGlobalSeedanceOfficialContractAndRouteIsolation(t *testing.T) {
	t.Parallel()
	resultURL := "https://cdn.example/result.mp4?X-Tos-Date=20960904T000000Z&X-Tos-Expires=86400"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "cdn.example" && request.Header.Get("Authorization") != "Bearer global-private" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.String() {
		case globalSpec.taskBase + "/shotforward-configuration-check":
			return response(404, `{}`), nil
		case globalSpec.taskBase:
			var payload createRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Model != globalSpec.model || payload.Resolution != "720p" || payload.Duration != 5 || payload.Ratio != "adaptive" || !payload.GenerateAudio || len(payload.Content) != 2 || payload.Content[1].ImageURL == nil || payload.Content[1].Role != "first_frame" {
				t.Fatalf("payload = %#v", payload)
			}
			return response(200, `{"id":"lsd-task-1"}`), nil
		case globalSpec.taskBase + "/lsd-task-1":
			return response(200, `{"id":"lsd-task-1","model":"dreamina-seedance-2-5-260628","status":"succeeded","content":{"video_url":"`+resultURL+`"}}`), nil
		case resultURL:
			if request.Header.Get("Authorization") != "" {
				t.Fatal("credential leaked to CDN")
			}
			return response(200, "video"), nil
		default:
			t.Fatalf("unexpected route %s", request.URL)
			return nil, nil
		}
	})}
	provider, err := NewGlobal(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceGlobal}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	check, err := provider.Check(context.Background(), readyRecord(domain.GeneratorVideoSeedanceGlobal), "global-private")
	if err != nil || check.CapabilityVersion != GlobalCapabilityVersion {
		t.Fatalf("Check() = %#v, %v", check, err)
	}
	operation, err := provider.Start(context.Background(), seedanceRequest(t, domain.GeneratorVideoSeedanceGlobal))
	if err != nil || operation.ID != "lsd-task-1" {
		t.Fatalf("Start() = %#v, %v", operation, err)
	}
	status, err := provider.Poll(context.Background(), operation)
	if err != nil || status.State != generation.GeneratorSucceeded || status.Validate() != nil {
		t.Fatalf("Poll() = %#v, %v", status, err)
	}
	if data, err := os.ReadFile(status.Outputs[0].SourcePath); err != nil || string(data) != "video" {
		t.Fatalf("output = %q, %v", data, err)
	}
	if status.Outputs[0].RemoteReferenceURL != resultURL || status.Outputs[0].RemoteReferenceExpiresAt == nil {
		t.Fatalf("remote reference = %#v", status.Outputs[0])
	}
}

func TestBuildRequestUsesUnexpiredOriginalVideoURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "previous.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("V", 26))
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceVideo, ArtifactID: artifactID, TrimEndMilliseconds: 10_000}
	expiresAt := time.Now().UTC().Add(3 * time.Hour)
	artifact := domain.Artifact{ID: artifactID, ProjectID: projectID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/previous.mp4", SHA256: strings.Repeat("b", 64), ByteSize: 5, MIMEType: "video/mp4", OriginalFilename: "previous.mp4", Width: 1280, Height: 720, DurationMilliseconds: 10_000, FPSNumerator: 24, FPSDenominator: 1, RemoteReferenceURL: "https://cdn.example/previous.mp4?signature=private", RemoteReferenceExpiresAt: &expiresAt, CreatedAt: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)}
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo, Prompt: "continue @视频1", GenerationMode: domain.ClipGenerationReference, GeneratorID: domain.GeneratorVideoSeedanceGlobal, ProviderModelID: globalSpec.model, ProviderConnectionRevision: 1, CapabilityVersion: GlobalCapabilityVersion, OutputWidth: 1280, OutputHeight: 720, RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{input}, ParametersJSON: "{}", TargetSlotRevision: 1}
	request := generation.GeneratorRequest{JobID: domain.GenerationJobID("gjb_" + strings.Repeat("J", 26)), Snapshot: snapshot, ProjectRoot: root, References: []generation.GeneratorArtifactReference{{Input: input, Artifact: artifact, AbsolutePath: path}}}
	payload, err := buildRequest(request, globalSpec.model)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Content) != 2 || payload.Content[1].VideoURL == nil || payload.Content[1].VideoURL.URL != artifact.RemoteReferenceURL || payload.Content[1].Role != "reference_video" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestBuildRequestRejectsExpiredOriginalVideoURL(t *testing.T) {
	t.Parallel()
	request := seedanceRequest(t, domain.GeneratorVideoSeedanceGlobal)
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceVideo, ArtifactID: request.References[0].Artifact.ID, TrimEndMilliseconds: 5_000}
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	request.Snapshot.GenerationMode = domain.ClipGenerationReference
	request.Snapshot.References = []domain.GenerationArtifactReference{input}
	request.References[0].Input = input
	request.References[0].Artifact.Kind = domain.MediaKindVideo
	request.References[0].Artifact.MIMEType = "video/mp4"
	request.References[0].Artifact.Width = 720
	request.References[0].Artifact.Height = 1280
	request.References[0].Artifact.DurationMilliseconds = 5_000
	request.References[0].Artifact.FPSNumerator = 24
	request.References[0].Artifact.FPSDenominator = 1
	request.References[0].Artifact.RemoteReferenceURL = "https://cdn.example/expired.mp4"
	request.References[0].Artifact.RemoteReferenceExpiresAt = &expiresAt
	if _, err := buildRequest(request, globalSpec.model); err == nil || !strings.Contains(err.Error(), "unexpired original URL") {
		t.Fatalf("buildRequest error = %v", err)
	}
}

func TestBuildRequestUploadsLocalVideoThroughDeliveryBridge(t *testing.T) {
	t.Parallel()
	request := seedanceRequest(t, domain.GeneratorVideoSeedanceGlobal)
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceVideo, ArtifactID: request.References[0].Artifact.ID, TrimEndMilliseconds: 5_000}
	request.Snapshot.GenerationMode = domain.ClipGenerationReference
	request.Snapshot.References = []domain.GenerationArtifactReference{input}
	request.References[0].Input = input
	request.References[0].Artifact.Kind = domain.MediaKindVideo
	request.References[0].Artifact.MIMEType = "video/mp4"
	request.References[0].Artifact.Width = 720
	request.References[0].Artifact.Height = 1280
	request.References[0].Artifact.DurationMilliseconds = 5_000
	request.References[0].Artifact.FPSNumerator = 24
	request.References[0].Artifact.FPSDenominator = 1
	bridge := &deliveryBridgeStub{url: "https://media.example/d/dlv_1?s=secret", expiresAt: time.Now().Add(30 * time.Minute)}
	payload, err := buildRequestWithDelivery(context.Background(), request, globalSpec.model, bridge)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.path != request.References[0].AbsolutePath || bridge.mediaType != "video/mp4" || bridge.operationID != request.JobID.String() {
		t.Fatalf("bridge call=%#v", bridge)
	}
	if len(payload.Content) != 2 || payload.Content[1].VideoURL == nil || payload.Content[1].VideoURL.URL != bridge.url {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestBuildRequestCategorizesReferenceVideoDeliveryFailure(t *testing.T) {
	t.Parallel()
	request := seedanceRequest(t, domain.GeneratorVideoSeedanceGlobal)
	input := domain.GenerationArtifactReference{Role: domain.GenerationReferenceVideo, ArtifactID: request.References[0].Artifact.ID, TrimEndMilliseconds: 5_000}
	request.Snapshot.GenerationMode = domain.ClipGenerationReference
	request.Snapshot.References = []domain.GenerationArtifactReference{input}
	request.References[0].Input = input
	request.References[0].Artifact.Kind = domain.MediaKindVideo
	request.References[0].Artifact.MIMEType = "video/mp4"
	request.References[0].Artifact.DurationMilliseconds = 5_000
	bridge := &deliveryBridgeStub{err: errors.New("delivery unavailable")}

	_, err := buildRequestWithDelivery(context.Background(), request, globalSpec.model, bridge)
	var categorized interface{ GenerationFailureCode() string }
	if !errors.As(err, &categorized) || categorized.GenerationFailureCode() != generation.ReferenceDeliveryFailureCode {
		t.Fatalf("delivery error=%v category=%v", err, categorized)
	}
}

func TestChinaAndGlobalUseDifferentModelsEndpointsAndCapabilities(t *testing.T) {
	t.Parallel()
	china, _ := NewChina(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceChina}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	global, _ := NewGlobal(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceGlobal}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	if china.ID() == global.ID() || china.spec.taskBase == global.spec.taskBase || china.spec.model == global.spec.model || china.spec.capabilityVersion == global.spec.capabilityVersion {
		t.Fatal("regional routes are not isolated")
	}
	if _, err := china.Start(context.Background(), seedanceRequest(t, domain.GeneratorVideoSeedanceGlobal)); err == nil {
		t.Fatal("China Provider accepted Global snapshot")
	}
}

func TestChinaAndGlobalSupportTheirRegionalSeedanceModels(t *testing.T) {
	t.Parallel()
	china, _ := NewChina(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceChina}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	global, _ := NewGlobal(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceGlobal}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	for _, suffix := range []string{"2-5-260628", "2-0-260128", "2-0-fast-260128", "2-0-mini-260615"} {
		chinaModel := "doubao-seedance-" + suffix
		globalModel := "dreamina-seedance-" + suffix
		if !china.supportsModel(chinaModel) || china.supportsModel(globalModel) {
			t.Fatalf("China model routing is incorrect for %q", suffix)
		}
		if !global.supportsModel(globalModel) || global.supportsModel(chinaModel) {
			t.Fatalf("Global model routing is incorrect for %q", suffix)
		}
	}
}

func TestCapabilitiesMatchSeedanceModelGeneration(t *testing.T) {
	t.Parallel()
	provider, _ := NewGlobal(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceGlobal}, WorkingDirectory: t.TempDir(), HTTPClient: http.DefaultClient})
	latest := readyRecord(domain.GeneratorVideoSeedanceGlobal)
	legacy := latest
	legacy.ModelID = "dreamina-seedance-2-0-260128"

	assertLimits := func(capability projectapp.GenerationCapability, images, videos, audio int, maximumDuration int64) {
		t.Helper()
		limits := map[domain.GenerationArtifactRole]int{}
		for _, reference := range capability.ReferenceCapabilities {
			limits[reference.Role] = reference.MaximumCount
		}
		if limits[domain.GenerationReferenceImage] != images || limits[domain.GenerationReferenceVideo] != videos || limits[domain.GenerationReferenceAudio] != audio || capability.MaximumDurationSeconds != maximumDuration {
			t.Fatalf("capability limits = %#v, maximum duration = %d", limits, capability.MaximumDurationSeconds)
		}
	}

	assertLimits(provider.capability(latest), 30, 10, 10, 30)
	assertLimits(provider.capability(legacy), 9, 3, 3, 15)
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
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		result := response(http.StatusBadRequest, `{"error":{"code":"SensitiveContent","message":"Reference image may contain a real person."}}`)
		result.Header.Set("X-Request-ID", "seedance-request-1")
		return result, nil
	})}
	provider, _ := NewChina(Options{Configuration: readyConfig{id: domain.GeneratorVideoSeedanceChina}, WorkingDirectory: t.TempDir(), HTTPClient: client})
	_, err := provider.Start(context.Background(), seedanceRequest(t, domain.GeneratorVideoSeedanceChina))
	if err == nil || !strings.Contains(err.Error(), "SensitiveContent") || !strings.Contains(err.Error(), "real person") || !strings.Contains(err.Error(), "seedance-request-1") {
		t.Fatalf("Start() error = %v", err)
	}
}

func seedanceRequest(t *testing.T, id domain.GeneratorID) generation.GeneratorRequest {
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
	version := ChinaCapabilityVersion
	model := chinaSpec.model
	if id == domain.GeneratorVideoSeedanceGlobal {
		version = GlobalCapabilityVersion
		model = globalSpec.model
	}
	snapshot := domain.GenerationInputSnapshot{SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo, Prompt: "slow movement", GenerationMode: domain.ClipGenerationFirstImage, GeneratorID: id, ProviderModelID: model, ProviderConnectionRevision: 1, CapabilityVersion: version, OutputWidth: 720, OutputHeight: 1280, RequestedDurationSeconds: 5, References: []domain.GenerationArtifactReference{input}, ParametersJSON: "{}", TargetSlotRevision: 1}
	result := generation.GeneratorRequest{JobID: jobID, Snapshot: snapshot, ProjectRoot: root, References: []generation.GeneratorArtifactReference{{Input: input, Artifact: artifact, AbsolutePath: path}}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	return result
}
func readyRecord(id domain.GeneratorID) providerconfig.Record {
	spec := chinaSpec
	if id == domain.GeneratorVideoSeedanceGlobal {
		spec = globalSpec
	}
	return providerconfig.Record{Profile: providerconfig.Profile{GeneratorID: id, EndpointPreset: spec.endpointPreset, ModelID: spec.model, Enabled: true, ConcurrencyLimit: 1, CapabilityVersion: spec.capabilityVersion, Health: providerconfig.HealthReady, Revision: 2}, CredentialRef: "pcr_seedance"}
}

type readyConfig struct{ id domain.GeneratorID }

type deliveryBridgeStub struct {
	path, mediaType, provider, purpose, operationID string
	url                                             string
	expiresAt                                       time.Time
	err                                             error
}

func (b *deliveryBridgeStub) PrepareProviderDelivery(_ context.Context, path, mediaType, provider, purpose, operationID string) (string, time.Time, error) {
	b.path, b.mediaType, b.provider, b.purpose, b.operationID = path, mediaType, provider, purpose, operationID
	return b.url, b.expiresAt, b.err
}

func (c readyConfig) RuntimeProvider(_ context.Context, id domain.GeneratorID) (providerconfig.Record, string, error) {
	if id != c.id {
		return providerconfig.Record{}, "", io.EOF
	}
	return readyRecord(id), "global-private", nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
