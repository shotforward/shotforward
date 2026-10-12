// Package minimaxh3 implements the official MiniMax H3 V2 asynchronous video
// API. Tests replace the HTTP transport; production never accepts a custom
// endpoint from user configuration.
package minimaxh3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const (
	chinaBaseURL                  = "https://api.minimax.cn"
	globalBaseURL                 = "https://api.minimax.io"
	baseURL                       = globalBaseURL
	ChinaCapabilityVersion        = "minimax-h3-cn-v3@2026-09-19"
	GlobalCapabilityVersion       = "minimax-h3-global-v3@2026-09-19"
	CapabilityVersion             = GlobalCapabilityVersion
	pollInterval                  = 10 * time.Second
	maxResponseBytes        int64 = 1 << 20
	maxRequestBytes               = 64 << 20
	maxDownloadBytes        int64 = 2 << 30
)

type routeSpec struct {
	id                domain.GeneratorID
	endpointPreset    string
	baseURL           string
	capabilityVersion string
}

var chinaSpec = routeSpec{domain.GeneratorVideoH3China, providerconfig.EndpointMiniMaxChina, chinaBaseURL, ChinaCapabilityVersion}
var globalSpec = routeSpec{domain.GeneratorVideoH3Official, providerconfig.EndpointMiniMaxGlobal, globalBaseURL, GlobalCapabilityVersion}

type Options struct {
	Configuration    providerconfig.RuntimeConfiguration
	WorkingDirectory string
	HTTPClient       *http.Client
}

type Provider struct {
	configuration    providerconfig.RuntimeConfiguration
	workingDirectory string
	client           *http.Client
	spec             routeSpec
}

func New(options Options) (*Provider, error)       { return NewGlobal(options) }
func NewChina(options Options) (*Provider, error)  { return newProvider(options, chinaSpec) }
func NewGlobal(options Options) (*Provider, error) { return newProvider(options, globalSpec) }

func newProvider(options Options, spec routeSpec) (*Provider, error) {
	if options.Configuration == nil || options.WorkingDirectory == "" || !filepath.IsAbs(options.WorkingDirectory) || filepath.Clean(options.WorkingDirectory) != options.WorkingDirectory {
		return nil, errors.New("create MiniMax H3 Provider: invalid dependencies")
	}
	info, err := os.Stat(options.WorkingDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("create MiniMax H3 Provider: working directory must exist")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 90 * time.Second, CheckRedirect: safeRedirect}
	}
	if spec.id == "" || spec.endpointPreset == "" || spec.baseURL == "" || spec.capabilityVersion == "" {
		return nil, errors.New("create MiniMax H3 Provider: endpoint is required")
	}
	spec.baseURL = strings.TrimRight(spec.baseURL, "/")
	return &Provider{configuration: options.Configuration, workingDirectory: options.WorkingDirectory, client: options.HTTPClient, spec: spec}, nil
}

func (p *Provider) ID() domain.GeneratorID                             { return p.spec.id }
func (*Provider) SupportsRecovery(domain.GenerationInputSnapshot) bool { return true }
func (*Provider) PollInterval() time.Duration                          { return pollInterval }
func (p *Provider) ConcurrencyLimit() int {
	record, _, err := p.configuration.RuntimeProvider(context.Background(), p.spec.id)
	if err != nil || record.ConcurrencyLimit < 1 || record.ConcurrencyLimit > providerconfig.MaximumConcurrency {
		return 1
	}
	return record.ConcurrencyLimit
}

func (p *Provider) Capability(ctx context.Context) (projectapp.GenerationCapability, error) {
	record, _, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil {
		return projectapp.GenerationCapability{}, err
	}
	if !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("MiniMax H3 Provider configuration is stale")
	}
	return p.capability(record), nil
}

func (p *Provider) CapabilityModel(ctx context.Context, modelID string) (projectapp.GenerationCapability, error) {
	configuration, ok := p.configuration.(providerconfig.ModelRuntimeConfiguration)
	if !ok || !supportedModel(modelID) {
		return projectapp.GenerationCapability{}, errors.New("MiniMax H3 model selection is unavailable")
	}
	record, _, err := configuration.RuntimeProviderModel(ctx, p.spec.id, modelID)
	if err != nil || !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("MiniMax H3 model is unavailable")
	}
	return p.capability(record), nil
}

func (p *Provider) capability(record providerconfig.Record) projectapp.GenerationCapability {
	images := []string{"image/jpeg", "image/png", "image/webp", "image/heic", "image/heif"}
	minimumDuration := int64(4)
	if record.ModelID == "MiniMax-H3-Max" {
		minimumDuration = 5
	}
	return projectapp.GenerationCapability{GeneratorID: p.spec.id, ProviderModelID: record.ModelID, ProviderConnectionRevision: record.Revision, CapabilityVersion: p.spec.capabilityVersion, MediaKind: domain.MediaKindVideo,
		SupportedVideoModes:    []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage, domain.ClipGenerationReference},
		MinimumDurationSeconds: minimumDuration, MaximumDurationSeconds: 15,
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceLastImage, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceImage, MaximumCount: 9, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceVideo, MaximumCount: 3, MaximumBytes: 50 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}, SupportsAutomaticPreparation: true},
			{Role: domain.GenerationReferenceAudio, MaximumCount: 3, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/wav", "audio/x-wav", "audio/mpeg"}},
		}, MaximumTotalTimedReferenceDurationMilliseconds: 30_000, SupportsRecovery: true}
}

// Check uses MiniMax's authenticated model-list endpoint. It never creates a
// video task and therefore does not consume generation quota.
func (p *Provider) Check(ctx context.Context, record providerconfig.Record, secret string) (providerconfig.CheckResult, error) {
	if !p.matches(record) || secret == "" {
		return providerconfig.CheckResult{}, errors.New("invalid MiniMax H3 check")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.spec.baseURL+"/v1/models", nil)
	if err != nil {
		return providerconfig.CheckResult{}, errors.New("create MiniMax H3 check")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return providerconfig.CheckResult{}, errors.New("MiniMax H3 is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return providerconfig.CheckResult{}, providerHTTPError(response, secret)
	}
	var payload struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := decodeBounded(response.Body, &payload); err != nil || payload.Object != "list" {
		return providerconfig.CheckResult{}, errors.New("MiniMax H3 check returned an invalid response")
	}
	return providerconfig.CheckResult{CapabilityVersion: p.spec.capabilityVersion}, nil
}

func (p *Provider) Start(ctx context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if err := request.Validate(); err != nil || request.Snapshot.GeneratorID != p.spec.id || request.Snapshot.TargetMediaKind != domain.MediaKindVideo || !supportedModel(request.Snapshot.ProviderModelID) || request.Snapshot.ProviderConnectionRevision < 1 || request.Snapshot.CapabilityVersion != p.spec.capabilityVersion {
		return generation.GeneratorOperation{}, errors.New("start MiniMax H3: invalid request")
	}
	record, secret, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("start MiniMax H3: Provider is unavailable"))
	}
	if !p.matches(record) || record.CapabilityVersion != p.spec.capabilityVersion {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("start MiniMax H3: Provider configuration changed"))
	}
	payload, err := buildRequest(request)
	if err != nil {
		return generation.GeneratorOperation{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequestBytes {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorRequestTooLargeFailureCode, errors.New("start MiniMax H3: request exceeds the 64 MiB API limit"))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.spec.baseURL+"/v2/video_generation", bytes.NewReader(body))
	if err != nil {
		return generation.GeneratorOperation{}, errors.New("start MiniMax H3: create request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+secret)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("start MiniMax H3: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorOperation{}, providerHTTPError(response, secret)
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err := decodeBounded(response.Body, &result); err != nil || !validOpaque(result.TaskID) {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("start MiniMax H3: invalid upstream response"))
	}
	return generation.GeneratorOperation{ID: result.TaskID}, nil
}

func (p *Provider) Poll(ctx context.Context, operation generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	if operation.Validate() != nil || !validOpaque(operation.ID) {
		return generation.GeneratorStatus{}, errors.New("poll MiniMax H3: invalid operation")
	}
	_, secret, err := p.configuration.RuntimeProvider(ctx, p.spec.id)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("poll MiniMax H3: Provider is unavailable"))
	}
	target := p.spec.baseURL + "/v2/query/video_generation/" + url.PathEscape(operation.ID)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("poll MiniMax H3: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorStatus{}, providerHTTPError(response, secret)
	}
	var result struct {
		RequestID string `json:"request_id"`
		Task      struct {
			ID, Model, Status string
			Code, Message     string
			Error             struct {
				Code, Message string
			} `json:"error"`
			Content struct {
				URL string `json:"url"`
			} `json:"content"`
		} `json:"task"`
	}
	if err := decodeBounded(response.Body, &result); err != nil || result.Task.ID != operation.ID || !supportedModel(result.Task.Model) {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll MiniMax H3: invalid upstream response"))
	}
	switch result.Task.Status {
	case "queued":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "queued", Message: "Waiting for MiniMax H3"}, nil
	case "running":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "generating", Message: "MiniMax H3 is generating video"}, nil
	case "failed":
		code, message := result.Task.Error.Code, result.Task.Error.Message
		if code == "" {
			code = result.Task.Code
		}
		if message == "" {
			message = result.Task.Message
		}
		if message == "" {
			message = "MiniMax H3 could not generate this video."
		}
		return generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: generation.ProviderTaskFailureCode, FailureMessage: generation.ProviderTaskFailureMessage("MiniMax H3", code, message, result.RequestID, secret)}, nil
	case "cancelled":
		return generation.GeneratorStatus{State: generation.GeneratorStopped, Stage: "cancelled"}, nil
	case "succeeded":
		path, err := p.download(ctx, operation.ID, result.Task.Content.URL)
		if err != nil {
			return generation.GeneratorStatus{}, err
		}
		return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Outputs: []generation.GeneratorOutput{{SourcePath: path}}}, nil
	default:
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll MiniMax H3: unknown task state"))
	}
}

func (*Provider) Stop(context.Context, generation.GeneratorOperation) error {
	return errors.New("MiniMax H3 does not support stopping a submitted task")
}

type requestContent struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *mediaURL `json:"image_url,omitempty"`
	VideoURL *mediaURL `json:"video_url,omitempty"`
	AudioURL *mediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}
type mediaURL struct {
	URL string `json:"url"`
}
type createRequest struct {
	Model      string           `json:"model"`
	Content    []requestContent `json:"content"`
	Resolution string           `json:"resolution"`
	Duration   int64            `json:"duration"`
	Ratio      string           `json:"ratio"`
}
type parameters struct {
	Resolution string `json:"resolution"`
}

func buildRequest(request generation.GeneratorRequest) (createRequest, error) {
	var params parameters
	decoder := json.NewDecoder(strings.NewReader(request.Snapshot.ParametersJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return createRequest{}, errors.New("start MiniMax H3: invalid parameters")
	}
	if params.Resolution == "" {
		params.Resolution = defaultResolution(request.Snapshot.ProviderModelID, request.Snapshot.OutputWidth, request.Snapshot.OutputHeight)
	}
	if !supportedResolution(request.Snapshot.ProviderModelID, params.Resolution) {
		return createRequest{}, errors.New("start MiniMax H3: unsupported resolution")
	}
	ratio, ok := outputRatio(request.Snapshot.OutputWidth, request.Snapshot.OutputHeight)
	if !ok {
		return createRequest{}, errors.New("start MiniMax H3: unsupported output aspect ratio")
	}
	if request.Snapshot.GenerationMode == domain.ClipGenerationFirstImage || request.Snapshot.GenerationMode == domain.ClipGenerationFirstLastImage {
		ratio = "adaptive"
	}
	result := createRequest{Model: request.Snapshot.ProviderModelID, Content: []requestContent{{Type: "text", Text: request.Snapshot.Prompt}}, Resolution: params.Resolution, Duration: request.Snapshot.RequestedDurationSeconds, Ratio: ratio}
	minimumDuration := int64(4)
	if request.Snapshot.ProviderModelID == "MiniMax-H3-Max" {
		minimumDuration = 5
	}
	if strings.TrimSpace(request.Snapshot.Prompt) == "" || utf8.RuneCountInString(request.Snapshot.Prompt) > 7_000 || result.Duration < minimumDuration || result.Duration > 15 {
		return createRequest{}, errors.New("start MiniMax H3: prompt or duration is invalid")
	}
	for _, reference := range request.References {
		if reference.Input.PreparationRequired {
			return createRequest{}, errors.New("start MiniMax H3: required reference preparation is unavailable")
		}
		dataURL, err := referenceDataURL(reference)
		if err != nil {
			return createRequest{}, err
		}
		item := requestContent{}
		switch reference.Input.Role {
		case domain.GenerationReferenceFirstImage:
			item.Type, item.Role, item.ImageURL = "image_url", "first_frame", &mediaURL{URL: dataURL}
		case domain.GenerationReferenceLastImage:
			item.Type, item.Role, item.ImageURL = "image_url", "last_frame", &mediaURL{URL: dataURL}
		case domain.GenerationReferenceImage:
			item.Type, item.Role, item.ImageURL = "image_url", "reference_image", &mediaURL{URL: dataURL}
		case domain.GenerationReferenceVideo:
			item.Type, item.Role, item.VideoURL = "video_url", "reference_video", &mediaURL{URL: dataURL}
		case domain.GenerationReferenceAudio:
			item.Type, item.Role, item.AudioURL = "audio_url", "reference_audio", &mediaURL{URL: dataURL}
		default:
			return createRequest{}, errors.New("start MiniMax H3: unsupported reference role")
		}
		result.Content = append(result.Content, item)
	}
	return result, nil
}

func defaultResolution(modelID string, width, height int) string {
	if modelID == "MiniMax-H3-Max" && min(width, height) <= 480 {
		return "480P"
	}
	if modelID == "MiniMax-H3" && width > 768 && height > 768 {
		return "2K"
	}
	return "768P"
}

func supportedResolution(modelID, resolution string) bool {
	switch modelID {
	case "MiniMax-H3":
		return resolution == "768P" || resolution == "2K"
	case "MiniMax-H3-Max":
		return resolution == "480P" || resolution == "768P"
	default:
		return false
	}
}

func supportedModel(modelID string) bool {
	return modelID == "MiniMax-H3" || modelID == "MiniMax-H3-Max"
}

func (p *Provider) matches(record providerconfig.Record) bool {
	return record.GeneratorID == p.spec.id && record.EndpointPreset == p.spec.endpointPreset && supportedModel(record.ModelID) && record.ResourceID == "" && record.WorkspaceID == ""
}

func referenceDataURL(reference generation.GeneratorArtifactReference) (string, error) {
	info, err := os.Lstat(reference.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != reference.Artifact.ByteSize {
		return "", errors.New("start MiniMax H3: reference file is unavailable")
	}
	data, err := os.ReadFile(reference.AbsolutePath)
	if err != nil {
		return "", errors.New("start MiniMax H3: reference file is unreadable")
	}
	return "data:" + reference.Artifact.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (p *Provider) download(ctx context.Context, taskID, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || unsafeHost(parsed.Hostname()) {
		return "", errors.New("download MiniMax H3 result: unsafe URL")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	response, err := p.client.Do(request)
	if err != nil {
		return "", errors.New("download MiniMax H3 result: unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("download MiniMax H3 result: upstream rejected download")
	}
	file, err := os.CreateTemp(p.workingDirectory, "minimax-h3-*.mp4")
	if err != nil {
		return "", errors.New("download MiniMax H3 result: create staging file")
	}
	path := file.Name()
	keep := false
	defer func() {
		file.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(response.Body, maxDownloadBytes+1))
	if err != nil || written == 0 || written > maxDownloadBytes || file.Sync() != nil || file.Close() != nil {
		return "", errors.New("download MiniMax H3 result: invalid media")
	}
	keep = true
	_ = taskID
	return path, nil
}

func decodeBounded(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseBytes+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("response contains extra data")
	}
	return nil
}

func statusError(status int) error {
	if status == http.StatusTooManyRequests {
		return generation.NewRetryablePollError(errors.New("MiniMax rate limit exceeded"))
	}
	if status >= http.StatusInternalServerError && status <= 599 {
		return generation.NewRetryablePollError(errors.New("MiniMax service is unavailable"))
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("MiniMax authentication failed")
	case http.StatusPaymentRequired:
		return errors.New("MiniMax balance is insufficient")
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return errors.New("MiniMax rejected the request")
	default:
		return errors.New("MiniMax service is unavailable")
	}
}

func providerHTTPError(response *http.Response, secret string) error {
	err := generation.NewProviderHTTPError("MiniMax H3", response.StatusCode, response.Header, response.Body, secret)
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError && response.StatusCode <= 599 {
		return generation.NewRetryablePollError(err)
	}
	return err
}

func validOpaque(value string) bool {
	return value != "" && len(value) <= 500 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}
func outputRatio(width, height int) (string, bool) {
	for ratio, dimensions := range map[string][2]int{"21:9": {21, 9}, "16:9": {16, 9}, "4:3": {4, 3}, "1:1": {1, 1}, "3:4": {3, 4}, "9:16": {9, 16}} {
		if width*dimensions[1] == height*dimensions[0] {
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
	if r.China != nil && id == domain.GeneratorVideoH3China {
		return r.China, true
	}
	if r.Global != nil && id == domain.GeneratorVideoH3Official {
		return r.Global, true
	}
	return nil, false
}

type Catalog struct{ China, Global *Provider }

func (c Catalog) provider(id domain.GeneratorID) *Provider {
	if id == domain.GeneratorVideoH3China {
		return c.China
	}
	if id == domain.GeneratorVideoH3Official {
		return c.Global
	}
	return nil
}

func (c Catalog) Generator(ctx context.Context, id domain.GeneratorID) (projectapp.GenerationCapability, error) {
	provider := c.provider(id)
	if provider == nil {
		return projectapp.GenerationCapability{}, errors.New("MiniMax H3 Provider not found")
	}
	return provider.Capability(ctx)
}

func (c Catalog) GeneratorModel(ctx context.Context, id domain.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	provider := c.provider(id)
	if provider == nil {
		return projectapp.GenerationCapability{}, errors.New("MiniMax H3 Provider not found")
	}
	return provider.CapabilityModel(ctx, modelID)
}

var _ generation.Generator = (*Provider)(nil)
var _ providerconfig.Checker = (*Provider)(nil)
