// Package seedance implements separate official Seedance 2.5 routes for
// Volcengine China and BytePlus Global. It never falls back across regions.
package seedance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const (
	ChinaCapabilityVersion         = "volcengine-seedance-2.5-v4@2026-09-13"
	GlobalCapabilityVersion        = "byteplus-seedance-2.5-v2@2026-09-13"
	maxRequestBytes                = 64 << 20
	maxResponseBytes         int64 = 1 << 20
	maxDownloadBytes         int64 = 2 << 30
	minimumReferenceValidity       = time.Hour
)

type routeSpec struct {
	id                                                 domain.GeneratorID
	endpointPreset, model, taskBase, capabilityVersion string
}

var chinaSpec = routeSpec{domain.GeneratorVideoSeedanceChina, providerconfig.EndpointVolcengineChina, "doubao-seedance-2-5-260628", "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks", ChinaCapabilityVersion}
var globalSpec = routeSpec{domain.GeneratorVideoSeedanceGlobal, providerconfig.EndpointBytePlusGlobal, "dreamina-seedance-2-5-260628", "https://operator.las.ap-southeast-1.bytepluses.com/api/v1/contents/generations/tasks", GlobalCapabilityVersion}

type Options struct {
	Configuration    providerconfig.RuntimeConfiguration
	WorkingDirectory string
	HTTPClient       *http.Client
	DeliveryBridge   DeliveryBridge
}
type Provider struct {
	configuration    providerconfig.RuntimeConfiguration
	workingDirectory string
	client           *http.Client
	spec             routeSpec
	deliveryBridge   DeliveryBridge
}

type DeliveryBridge interface {
	PrepareProviderDelivery(context.Context, string, string, string, string, string) (string, time.Time, error)
}

func NewChina(options Options) (*Provider, error)  { return newProvider(options, chinaSpec) }
func NewGlobal(options Options) (*Provider, error) { return newProvider(options, globalSpec) }
func newProvider(options Options, spec routeSpec) (*Provider, error) {
	if options.Configuration == nil || options.WorkingDirectory == "" || !filepath.IsAbs(options.WorkingDirectory) || filepath.Clean(options.WorkingDirectory) != options.WorkingDirectory {
		return nil, errors.New("create Seedance Provider: invalid dependencies")
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, errors.New("create Seedance Provider: working directory must exist")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 90 * time.Second, CheckRedirect: safeRedirect}
	}
	return &Provider{configuration: options.Configuration, workingDirectory: options.WorkingDirectory, client: options.HTTPClient, spec: spec, deliveryBridge: options.DeliveryBridge}, nil
}

func (p *Provider) ID() domain.GeneratorID { return p.spec.id }
func (p *Provider) ConcurrencyLimit() int {
	record, _, err := p.configuration.RuntimeProvider(context.Background(), p.spec.id)
	if err != nil || record.ConcurrencyLimit < 1 || record.ConcurrencyLimit > providerconfig.MaximumConcurrency {
		return 1
	}
	return record.ConcurrencyLimit
}
func (*Provider) SupportsRecovery(domain.GenerationInputSnapshot) bool { return true }

func (p *Provider) Capability(ctx context.Context) (projectapp.GenerationCapability, error) {
	record, _, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil {
		return projectapp.GenerationCapability{}, err
	}
	if !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("Seedance Provider configuration is stale")
	}
	return p.capability(record), nil
}

func (p *Provider) CapabilityModel(ctx context.Context, modelID string) (projectapp.GenerationCapability, error) {
	configuration, ok := p.configuration.(providerconfig.ModelRuntimeConfiguration)
	if !ok || !p.supportsModel(modelID) {
		return projectapp.GenerationCapability{}, errors.New("Seedance model selection is unavailable")
	}
	record, _, err := configuration.RuntimeProviderModel(ctx, p.spec.id, modelID)
	if err != nil || !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("Seedance Provider model is unavailable")
	}
	return p.capability(record), nil
}

func (p *Provider) capability(record providerconfig.Record) projectapp.GenerationCapability {
	images := []string{"image/jpeg", "image/png", "image/webp", "image/bmp", "image/tiff", "image/gif", "image/heic", "image/heif"}
	maximumDuration := int64(30)
	imageLimit, videoLimit, audioLimit := 30, 10, 10
	if record.ModelID != "dreamina-seedance-2-5-260628" && record.ModelID != "doubao-seedance-2-5-260628" {
		maximumDuration = 15
		imageLimit, videoLimit, audioLimit = 9, 3, 3
	}
	timedReferenceDuration := maximumDuration * 1_000
	return projectapp.GenerationCapability{GeneratorID: p.spec.id, ProviderModelID: record.ModelID, ProviderConnectionRevision: record.Revision, CapabilityVersion: p.spec.capabilityVersion, MediaKind: domain.MediaKindVideo,
		SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage, domain.ClipGenerationReference}, MinimumDurationSeconds: 4, MaximumDurationSeconds: maximumDuration,
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceLastImage, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceImage, MaximumCount: imageLimit, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceVideo, MaximumCount: videoLimit, MaximumBytes: 200 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: timedReferenceDuration, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}, SupportsAutomaticPreparation: true},
			{Role: domain.GenerationReferenceAudio, MaximumCount: audioLimit, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: timedReferenceDuration, MaximumTotalDurationMilliseconds: timedReferenceDuration, AcceptedMIMETypes: []string{"audio/wav", "audio/x-wav", "audio/mpeg"}},
		}, MaximumTotalTimedReferenceDurationMilliseconds: maximumDuration * 2_000, SupportsRecovery: true}
}

// Check performs an authenticated read of a deliberately nonexistent task on
// the exact configured generation route. A 400/404 proves the credential was
// accepted without creating a task or consuming generation quota.
func (p *Provider) Check(ctx context.Context, record providerconfig.Record, secret string) (providerconfig.CheckResult, error) {
	if !p.matches(record) || secret == "" {
		return providerconfig.CheckResult{}, errors.New("invalid Seedance check")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.spec.taskBase+"/shotforward-configuration-check", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return providerconfig.CheckResult{}, errors.New("Seedance is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound {
		return providerconfig.CheckResult{CapabilityVersion: p.spec.capabilityVersion}, nil
	}
	if response.StatusCode != http.StatusOK {
		return providerconfig.CheckResult{}, providerHTTPError(response, secret)
	}
	var result struct {
		ID, Model, Status string
	}
	if err := decodeBounded(response.Body, &result); err != nil || result.ID != "shotforward-configuration-check" || !p.supportsModel(result.Model) || result.Status == "" {
		return providerconfig.CheckResult{}, errors.New("Seedance check returned an invalid response")
	}
	return providerconfig.CheckResult{CapabilityVersion: p.spec.capabilityVersion}, nil
}

func (p *Provider) Start(ctx context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if err := request.Validate(); err != nil || request.Snapshot.GeneratorID != p.spec.id || request.Snapshot.CapabilityVersion != p.spec.capabilityVersion {
		return generation.GeneratorOperation{}, errors.New("start Seedance: invalid request")
	}
	record, secret, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil || !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("start Seedance: Provider is unavailable"))
	}
	modelID := request.Snapshot.ProviderModelID
	if modelID == "" { // Backward compatibility for queued Jobs created before model snapshots.
		modelID = record.ModelID
	}
	if !p.supportsModel(modelID) {
		return generation.GeneratorOperation{}, errors.New("start Seedance: invalid model snapshot")
	}
	payload, err := buildRequestWithDelivery(ctx, request, modelID, p.deliveryBridge)
	if err != nil {
		return generation.GeneratorOperation{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequestBytes {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorRequestTooLargeFailureCode, errors.New("start Seedance: request exceeds the 64 MiB API limit"))
	}
	httpRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.spec.taskBase, bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+secret)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("start Seedance: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorOperation{}, providerHTTPError(response, secret)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := decodeBounded(response.Body, &result); err != nil || !validOpaque(result.ID) {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("start Seedance: invalid upstream response"))
	}
	return generation.GeneratorOperation{ID: result.ID}, nil
}

func (p *Provider) Poll(ctx context.Context, operation generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	if operation.Validate() != nil || !validOpaque(operation.ID) {
		return generation.GeneratorStatus{}, errors.New("poll Seedance: invalid operation")
	}
	_, secret, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("poll Seedance: Provider is unavailable"))
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.spec.taskBase+"/"+url.PathEscape(operation.ID), nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("poll Seedance: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorStatus{}, providerHTTPError(response, secret)
	}
	var result struct {
		ID, Model, Status string
		RequestID         string `json:"request_id"`
		Error             struct {
			Code, Message string
		} `json:"error"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
	}
	if err := decodeBounded(response.Body, &result); err != nil || result.ID != operation.ID || !p.supportsModel(result.Model) {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll Seedance: invalid upstream response"))
	}
	switch result.Status {
	case "queued":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "queued", Message: "Waiting for Seedance 2.5"}, nil
	case "running":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "generating", Message: "Seedance 2.5 is generating video"}, nil
	case "failed", "expired":
		message := result.Error.Message
		if message == "" {
			message = "Seedance could not generate this video."
		}
		return generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: generation.ProviderTaskFailureCode, FailureMessage: generation.ProviderTaskFailureMessage("Seedance", result.Error.Code, message, result.RequestID, secret)}, nil
	case "cancelled":
		return generation.GeneratorStatus{State: generation.GeneratorStopped, Stage: "cancelled"}, nil
	case "succeeded":
		path, err := p.download(ctx, result.Content.VideoURL)
		if err != nil {
			return generation.GeneratorStatus{}, err
		}
		output := generation.GeneratorOutput{SourcePath: path}
		if expiresAt, ok := remoteReferenceExpiry(result.Content.VideoURL); ok {
			output.RemoteReferenceURL = result.Content.VideoURL
			output.RemoteReferenceExpiresAt = &expiresAt
		}
		return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Outputs: []generation.GeneratorOutput{output}}, nil
	default:
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll Seedance: unknown task state"))
	}
}
func (*Provider) Stop(context.Context, generation.GeneratorOperation) error {
	return errors.New("Seedance 2.5 does not support stopping a submitted task")
}

type mediaURL struct {
	URL string `json:"url"`
}
type content struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *mediaURL `json:"image_url,omitempty"`
	VideoURL *mediaURL `json:"video_url,omitempty"`
	AudioURL *mediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}
type createRequest struct {
	Model         string    `json:"model"`
	Content       []content `json:"content"`
	GenerateAudio bool      `json:"generate_audio"`
	Resolution    string    `json:"resolution"`
	Ratio         string    `json:"ratio"`
	Duration      int64     `json:"duration"`
	Watermark     bool      `json:"watermark"`
	Seed          *int64    `json:"seed,omitempty"`
}
type parameters struct {
	Resolution    string `json:"resolution"`
	GenerateAudio *bool  `json:"generate_audio"`
	Watermark     bool   `json:"watermark"`
	Seed          *int64 `json:"seed"`
}

func buildRequest(request generation.GeneratorRequest, model string) (createRequest, error) {
	return buildRequestWithDelivery(context.Background(), request, model, nil)
}

func buildRequestWithDelivery(ctx context.Context, request generation.GeneratorRequest, model string, bridge DeliveryBridge) (createRequest, error) {
	var params parameters
	decoder := json.NewDecoder(strings.NewReader(request.Snapshot.ParametersJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return createRequest{}, errors.New("start Seedance: invalid parameters")
	}
	if params.Resolution == "" {
		params.Resolution = "720p"
	}
	if params.Resolution != "480p" && params.Resolution != "720p" {
		return createRequest{}, errors.New("start Seedance: unsupported resolution")
	}
	if params.Seed != nil && (*params.Seed < -1 || *params.Seed > 4_294_967_295) {
		return createRequest{}, errors.New("start Seedance: invalid seed")
	}
	generateAudio := true
	if params.GenerateAudio != nil {
		generateAudio = *params.GenerateAudio
	}
	ratio, ok := outputRatio(request.Snapshot.OutputWidth, request.Snapshot.OutputHeight)
	if !ok {
		return createRequest{}, errors.New("start Seedance: unsupported output aspect ratio")
	}
	if request.Snapshot.GenerationMode == domain.ClipGenerationFirstImage || request.Snapshot.GenerationMode == domain.ClipGenerationFirstLastImage {
		ratio = "adaptive"
	}
	result := createRequest{Model: model, GenerateAudio: generateAudio, Resolution: params.Resolution, Ratio: ratio, Duration: request.Snapshot.RequestedDurationSeconds, Watermark: params.Watermark, Seed: params.Seed}
	if request.Snapshot.Prompt != "" {
		result.Content = append(result.Content, content{Type: "text", Text: request.Snapshot.Prompt})
	}
	if result.Duration < 4 || result.Duration > 30 {
		return createRequest{}, errors.New("start Seedance: invalid duration")
	}
	for _, reference := range request.References {
		if reference.Input.PreparationRequired {
			return createRequest{}, errors.New("start Seedance: required reference preparation is unavailable")
		}
		item := content{}
		switch reference.Input.Role {
		case domain.GenerationReferenceFirstImage:
			dataURL, err := referenceDataURL(reference)
			if err != nil {
				return createRequest{}, err
			}
			item.Type, item.Role, item.ImageURL = "image_url", "first_frame", &mediaURL{dataURL}
		case domain.GenerationReferenceLastImage:
			dataURL, err := referenceDataURL(reference)
			if err != nil {
				return createRequest{}, err
			}
			item.Type, item.Role, item.ImageURL = "image_url", "last_frame", &mediaURL{dataURL}
		case domain.GenerationReferenceImage:
			dataURL, err := referenceDataURL(reference)
			if err != nil {
				return createRequest{}, err
			}
			item.Type, item.Role, item.ImageURL = "image_url", "reference_image", &mediaURL{dataURL}
		case domain.GenerationReferenceVideo:
			videoURL := reference.Artifact.RemoteReferenceURL
			if !usableRemoteReference(reference.Artifact, time.Now().UTC()) {
				if bridge == nil {
					return createRequest{}, errors.New("start Seedance: reference video needs an unexpired original URL or ShotForward cloud login")
				}
				deliveryURL, expiresAt, deliveryErr := bridge.PrepareProviderDelivery(ctx, reference.AbsolutePath, reference.Artifact.MIMEType, "seedance", "reference_video", request.JobID.String())
				if deliveryErr != nil {
					return createRequest{}, generation.NewGeneratorFailure(generation.ReferenceDeliveryFailureCode, fmt.Errorf("start Seedance: prepare reference video delivery: %w", deliveryErr))
				}
				videoURL = deliveryURL
				parsed, parseErr := url.Parse(videoURL)
				if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || unsafeHost(parsed.Hostname()) || !expiresAt.After(time.Now().Add(5*time.Minute)) {
					return createRequest{}, errors.New("start Seedance: temporary reference video URL is invalid")
				}
			}
			item.Type, item.Role, item.VideoURL = "video_url", "reference_video", &mediaURL{videoURL}
		case domain.GenerationReferenceAudio:
			dataURL, err := referenceDataURL(reference)
			if err != nil {
				return createRequest{}, err
			}
			item.Type, item.Role, item.AudioURL = "audio_url", "reference_audio", &mediaURL{dataURL}
		default:
			return createRequest{}, errors.New("start Seedance: unsupported local reference role")
		}
		result.Content = append(result.Content, item)
	}
	if len(result.Content) == 0 {
		return createRequest{}, errors.New("start Seedance: content is required")
	}
	return result, nil
}

func usableRemoteReference(artifact domain.Artifact, now time.Time) bool {
	if artifact.RemoteReferenceURL == "" || artifact.RemoteReferenceExpiresAt == nil || !artifact.RemoteReferenceExpiresAt.After(now.Add(minimumReferenceValidity)) {
		return false
	}
	parsed, err := url.Parse(artifact.RemoteReferenceURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && !unsafeHost(parsed.Hostname())
}

func remoteReferenceExpiry(rawURL string) (time.Time, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}, false
	}
	query := parsed.Query()
	for _, fields := range [][2]string{{"X-Tos-Date", "X-Tos-Expires"}, {"X-Amz-Date", "X-Amz-Expires"}} {
		issuedAt, dateErr := time.Parse("20060102T150405Z", query.Get(fields[0]))
		seconds, expiresErr := strconv.ParseInt(query.Get(fields[1]), 10, 64)
		if dateErr == nil && expiresErr == nil && seconds > 0 {
			return issuedAt.Add(time.Duration(seconds) * time.Second), true
		}
	}
	return time.Time{}, false
}

func referenceDataURL(reference generation.GeneratorArtifactReference) (string, error) {
	info, err := os.Lstat(reference.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != reference.Artifact.ByteSize {
		return "", errors.New("start Seedance: reference file is unavailable")
	}
	data, err := os.ReadFile(reference.AbsolutePath)
	if err != nil {
		return "", errors.New("start Seedance: reference file is unreadable")
	}
	return "data:" + reference.Artifact.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
func (p *Provider) download(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || unsafeHost(parsed.Hostname()) {
		return "", errors.New("download Seedance result: unsafe URL")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	response, err := p.client.Do(request)
	if err != nil {
		return "", errors.New("download Seedance result: unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("download Seedance result: upstream rejected download")
	}
	file, err := os.CreateTemp(p.workingDirectory, "seedance-*.mp4")
	if err != nil {
		return "", errors.New("download Seedance result: create staging file")
	}
	path, keep := file.Name(), false
	defer func() {
		file.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, maxDownloadBytes+1))
	if copyErr != nil || written == 0 || written > maxDownloadBytes || file.Sync() != nil || file.Close() != nil {
		return "", errors.New("download Seedance result: invalid media")
	}
	keep = true
	return path, nil
}
func (p *Provider) matches(record providerconfig.Record) bool {
	return record.GeneratorID == p.spec.id && record.EndpointPreset == p.spec.endpointPreset && p.supportsModel(record.ModelID) && record.ResourceID == "" && record.WorkspaceID == ""
}

func (p *Provider) supportsModel(modelID string) bool {
	if p.spec.id == domain.GeneratorVideoSeedanceChina {
		return modelID == "doubao-seedance-2-5-260628" || modelID == "doubao-seedance-2-0-260128" || modelID == "doubao-seedance-2-0-fast-260128" || modelID == "doubao-seedance-2-0-mini-260615"
	}
	return modelID == "dreamina-seedance-2-5-260628" || modelID == "dreamina-seedance-2-0-260128" || modelID == "dreamina-seedance-2-0-fast-260128" || modelID == "dreamina-seedance-2-0-mini-260615"
}
func decodeBounded(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseBytes+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("extra response data")
	}
	return nil
}
func statusError(status int) error {
	if status == http.StatusTooManyRequests {
		return generation.NewRetryablePollError(errors.New("Seedance rate limit exceeded"))
	}
	if status >= http.StatusInternalServerError && status <= 599 {
		return generation.NewRetryablePollError(errors.New("Seedance service is unavailable"))
	}
	switch status {
	case 401, 403:
		return errors.New("Seedance authentication failed")
	case 402:
		return errors.New("Seedance balance is insufficient")
	case 400, 422:
		return errors.New("Seedance rejected the request")
	default:
		return errors.New("Seedance service is unavailable")
	}
}

func providerHTTPError(response *http.Response, secret string) error {
	err := generation.NewProviderHTTPError("Seedance", response.StatusCode, response.Header, response.Body, secret)
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError && response.StatusCode <= 599 {
		return generation.NewRetryablePollError(err)
	}
	return err
}
func validOpaque(value string) bool {
	return value != "" && len(value) <= 500 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}
func outputRatio(width, height int) (string, bool) {
	for ratio, d := range map[string][2]int{"21:9": {21, 9}, "16:9": {16, 9}, "4:3": {4, 3}, "1:1": {1, 1}, "3:4": {3, 4}, "9:16": {9, 16}} {
		if width*d[1] == height*d[0] {
			return ratio, true
		}
	}
	return "", false
}
func unsafeHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	return false
}
func safeRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 || request.URL.Scheme != "https" || request.URL.User != nil || unsafeHost(request.URL.Hostname()) {
		return errors.New("unsafe redirect")
	}
	request.Header.Del("Authorization")
	return nil
}

type Registry struct{ China, Global *Provider }

func (r Registry) Generator(id domain.GeneratorID) (generation.Generator, bool) {
	if r.China != nil && id == domain.GeneratorVideoSeedanceChina {
		return r.China, true
	}
	if r.Global != nil && id == domain.GeneratorVideoSeedanceGlobal {
		return r.Global, true
	}
	return nil, false
}

type Catalog struct{ China, Global *Provider }

func (c Catalog) Generator(ctx context.Context, id domain.GeneratorID) (projectapp.GenerationCapability, error) {
	if c.China != nil && id == domain.GeneratorVideoSeedanceChina {
		return c.China.Capability(ctx)
	}
	if c.Global != nil && id == domain.GeneratorVideoSeedanceGlobal {
		return c.Global.Capability(ctx)
	}
	return projectapp.GenerationCapability{}, errors.New("Seedance Provider not found")
}

func (c Catalog) GeneratorModel(ctx context.Context, id domain.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	if c.China != nil && id == domain.GeneratorVideoSeedanceChina {
		return c.China.CapabilityModel(ctx, modelID)
	}
	if c.Global != nil && id == domain.GeneratorVideoSeedanceGlobal {
		return c.Global.CapabilityModel(ctx, modelID)
	}
	return projectapp.GenerationCapability{}, errors.New("Seedance Provider not found")
}

var _ generation.Generator = (*Provider)(nil)
var _ providerconfig.Checker = (*Provider)(nil)
