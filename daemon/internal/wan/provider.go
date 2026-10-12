// Package wan implements the official Alibaba Cloud Model Studio Wan 3.0
// video-generation route. Endpoint, workspace, model, and credentials remain
// pinned to one configured region.
package wan

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const (
	CapabilityVersion       = "alibaba-model-studio-wan-3.0-v1@2026-08-06"
	maxRequestBytes         = 64 << 20
	maxResponseBytes  int64 = 1 << 20
	maxDownloadBytes  int64 = 2 << 30
	generationPath          = "/api/v1/services/aigc/video-generation/video-synthesis"
)

var endpointRegions = map[string]string{
	providerconfig.EndpointAlibabaChina:     "cn-beijing",
	providerconfig.EndpointAlibabaSingapore: "ap-southeast-1",
	providerconfig.EndpointAlibabaTokyo:     "ap-northeast-1",
	providerconfig.EndpointAlibabaFrankfurt: "eu-central-1",
	providerconfig.EndpointAlibabaVirginia:  "us-east-1",
}

type Options struct {
	Configuration    providerconfig.RuntimeConfiguration
	WorkingDirectory string
	HTTPClient       *http.Client
}

type Provider struct {
	configuration    providerconfig.RuntimeConfiguration
	workingDirectory string
	client           *http.Client
}

func New(options Options) (*Provider, error) {
	if options.Configuration == nil || options.WorkingDirectory == "" || !filepath.IsAbs(options.WorkingDirectory) || filepath.Clean(options.WorkingDirectory) != options.WorkingDirectory {
		return nil, errors.New("create Wan 3.0 Provider: invalid dependencies")
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, errors.New("create Wan 3.0 Provider: working directory must exist")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 90 * time.Second, CheckRedirect: safeRedirect}
	}
	return &Provider{configuration: options.Configuration, workingDirectory: options.WorkingDirectory, client: options.HTTPClient}, nil
}

func (*Provider) ID() domain.GeneratorID                               { return domain.GeneratorVideoWanOfficial }
func (*Provider) SupportsRecovery(domain.GenerationInputSnapshot) bool { return true }
func (p *Provider) ConcurrencyLimit() int {
	record, _, err := p.configuration.RuntimeProvider(context.Background(), domain.GeneratorVideoWanOfficial)
	if err != nil || record.ConcurrencyLimit < 1 || record.ConcurrencyLimit > providerconfig.MaximumConcurrency {
		return 1
	}
	return record.ConcurrencyLimit
}

func (p *Provider) Capability(ctx context.Context) (projectapp.GenerationCapability, error) {
	record, _, err := p.configuration.RuntimeProvider(ctx, domain.GeneratorVideoWanOfficial)
	if err != nil {
		return projectapp.GenerationCapability{}, err
	}
	if _, _, err := route(record); err != nil || record.CapabilityVersion != CapabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("Wan 3.0 Provider configuration is stale")
	}
	images := []string{"image/jpeg", "image/png", "image/bmp", "image/webp"}
	return projectapp.GenerationCapability{
		GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: record.ModelID, ProviderConnectionRevision: record.Revision, CapabilityVersion: CapabilityVersion, MediaKind: domain.MediaKindVideo,
		SupportedVideoModes:    []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage, domain.ClipGenerationReference},
		MinimumDurationSeconds: 4, MaximumDurationSeconds: 30,
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceLastImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceImage, MaximumCount: 10, MaximumBytes: 20 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceVideo, MaximumCount: 5, MaximumBytes: 100 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}, SupportsAutomaticPreparation: true},
			{Role: domain.GenerationReferenceAudio, MaximumCount: 5, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/wav", "audio/x-wav", "audio/mpeg"}},
		},
		MaximumTotalTimedReferenceDurationMilliseconds: 30_000, SupportsRecovery: true,
	}, nil
}

func (p *Provider) CapabilityModel(ctx context.Context, modelID string) (projectapp.GenerationCapability, error) {
	configuration, ok := p.configuration.(providerconfig.ModelRuntimeConfiguration)
	if !ok {
		return projectapp.GenerationCapability{}, errors.New("Wan 3.0 model selection is unavailable")
	}
	record, _, err := configuration.RuntimeProviderModel(ctx, domain.GeneratorVideoWanOfficial, modelID)
	if err != nil || record.CapabilityVersion != CapabilityVersion {
		return projectapp.GenerationCapability{}, errors.New("Wan 3.0 Provider model is unavailable")
	}
	images := []string{"image/jpeg", "image/png", "image/bmp", "image/webp"}
	return projectapp.GenerationCapability{GeneratorID: domain.GeneratorVideoWanOfficial, ProviderModelID: record.ModelID, ProviderConnectionRevision: record.Revision, CapabilityVersion: CapabilityVersion, MediaKind: domain.MediaKindVideo, SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage, domain.ClipGenerationReference}, MinimumDurationSeconds: 4, MaximumDurationSeconds: 30, ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: images}, {Role: domain.GenerationReferenceLastImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: images}, {Role: domain.GenerationReferenceImage, MaximumCount: 10, MaximumBytes: 20 << 20, AcceptedMIMETypes: images}, {Role: domain.GenerationReferenceVideo, MaximumCount: 5, MaximumBytes: 100 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}, SupportsAutomaticPreparation: true}, {Role: domain.GenerationReferenceAudio, MaximumCount: 5, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/wav", "audio/x-wav", "audio/mpeg"}}}, MaximumTotalTimedReferenceDurationMilliseconds: 30_000, SupportsRecovery: true}, nil
}

// Check lists the exact Wan model in the configured region. It authenticates
// the API key without creating a generation task or consuming generation quota.
func (p *Provider) Check(ctx context.Context, record providerconfig.Record, secret string) (providerconfig.CheckResult, error) {
	_, modelListBase, err := route(record)
	if err != nil || secret == "" {
		return providerconfig.CheckResult{}, errors.New("invalid Wan 3.0 check")
	}
	query := url.Values{"providers": {"wan"}, "model": {record.ModelID}, "page_no": {"1"}, "page_size": {"1"}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, modelListBase+"/api/v1/models?"+query.Encode(), nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return providerconfig.CheckResult{}, errors.New("Wan 3.0 is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return providerconfig.CheckResult{}, providerHTTPError(response, secret)
	}
	var result struct {
		Output struct {
			Models []struct {
				Model string `json:"model"`
			} `json:"models"`
		} `json:"output"`
	}
	if err := decodeBounded(response.Body, &result); err != nil || len(result.Output.Models) != 1 || result.Output.Models[0].Model != record.ModelID {
		return providerconfig.CheckResult{}, errors.New("Wan 3.0 check returned an invalid response")
	}
	return providerconfig.CheckResult{CapabilityVersion: CapabilityVersion}, nil
}

func (p *Provider) Start(ctx context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if err := request.Validate(); err != nil || request.Snapshot.GeneratorID != domain.GeneratorVideoWanOfficial || request.Snapshot.CapabilityVersion != CapabilityVersion {
		return generation.GeneratorOperation{}, errors.New("start Wan 3.0: invalid request")
	}
	record, secret, err := p.configuration.RuntimeProvider(ctx, domain.GeneratorVideoWanOfficial)
	if err != nil || record.CapabilityVersion != CapabilityVersion {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("start Wan 3.0: Provider is unavailable"))
	}
	base, uploadBase, err := route(record)
	if err != nil {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("start Wan 3.0: Provider is unavailable"))
	}
	modelID := request.Snapshot.ProviderModelID
	if modelID == "" { // Jobs created before Provider model snapshots use their original configured model.
		modelID = record.ModelID
	}
	if modelID != "wan3.0-video" && modelID != "wan3.0-video-prime" {
		return generation.GeneratorOperation{}, errors.New("start Wan 3.0: invalid model snapshot")
	}
	uploadedURLs := make(map[domain.ArtifactID]string)
	for _, reference := range request.References {
		if reference.Input.Role != domain.GenerationReferenceVideo && reference.Input.Role != domain.GenerationReferenceAudio {
			continue
		}
		uploadedURLs[reference.Artifact.ID], err = p.uploadTemporaryFile(ctx, uploadBase, secret, modelID, reference)
		if err != nil {
			return generation.GeneratorOperation{}, err
		}
	}
	payload, err := buildRequestWithURLs(request, modelID, uploadedURLs)
	if err != nil {
		return generation.GeneratorOperation{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequestBytes {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorRequestTooLargeFailureCode, errors.New("start Wan 3.0: request exceeds the 64 MiB client limit"))
	}
	httpRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+generationPath, bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+secret)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-DashScope-Async", "enable")
	if len(uploadedURLs) > 0 {
		httpRequest.Header.Set("X-DashScope-OssResourceResolve", "enable")
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("start Wan 3.0: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorOperation{}, providerHTTPError(response, secret)
	}
	var result taskResponse
	if err := decodeBounded(response.Body, &result); err != nil || !validOpaque(result.Output.TaskID) {
		return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("start Wan 3.0: invalid upstream response"))
	}
	return generation.GeneratorOperation{ID: result.Output.TaskID}, nil
}

func (p *Provider) Poll(ctx context.Context, operation generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	if operation.Validate() != nil || !validOpaque(operation.ID) {
		return generation.GeneratorStatus{}, errors.New("poll Wan 3.0: invalid operation")
	}
	record, secret, err := p.configuration.RuntimeProvider(ctx, domain.GeneratorVideoWanOfficial)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("poll Wan 3.0: Provider is unavailable"))
	}
	base, _, err := route(record)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorUnavailableFailureCode, errors.New("poll Wan 3.0: Provider is unavailable"))
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/tasks/"+url.PathEscape(operation.ID), nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := p.client.Do(request)
	if err != nil {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.ProviderUnreachableFailureCode, errors.New("poll Wan 3.0: upstream unavailable"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generation.GeneratorStatus{}, providerHTTPError(response, secret)
	}
	var result taskResponse
	if err := decodeBounded(response.Body, &result); err != nil || result.Output.TaskID != operation.ID {
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll Wan 3.0: invalid upstream response"))
	}
	switch result.Output.TaskStatus {
	case "PENDING":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "queued", Message: "Waiting for Wan 3.0"}, nil
	case "RUNNING":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: "generating", Message: "Wan 3.0 is generating video"}, nil
	case "FAILED", "UNKNOWN":
		message := result.Output.Message
		if message == "" {
			message = "Wan 3.0 could not generate this video."
		}
		return generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: generation.ProviderTaskFailureCode, FailureMessage: generation.ProviderTaskFailureMessage("Wan 3.0", result.Output.Code, message, result.RequestID, secret)}, nil
	case "CANCELED":
		return generation.GeneratorStatus{State: generation.GeneratorStopped, Stage: "cancelled"}, nil
	case "SUCCEEDED":
		path, err := p.download(ctx, result.Output.VideoURL)
		if err != nil {
			return generation.GeneratorStatus{}, err
		}
		return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Outputs: []generation.GeneratorOutput{{SourcePath: path}}}, nil
	default:
		return generation.GeneratorStatus{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, errors.New("poll Wan 3.0: unknown task state"))
	}
}

func (*Provider) Stop(context.Context, generation.GeneratorOperation) error {
	return errors.New("Wan 3.0 does not support stopping a submitted task")
}

type media struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}
type input struct {
	Prompt string  `json:"prompt,omitempty"`
	Media  []media `json:"media,omitempty"`
}
type createParameters struct {
	Resolution   string `json:"resolution"`
	Ratio        string `json:"ratio"`
	Duration     int64  `json:"duration"`
	Audio        bool   `json:"audio"`
	Seed         *int64 `json:"seed,omitempty"`
	PromptExtend bool   `json:"prompt_extend"`
	Watermark    bool   `json:"watermark"`
}
type createRequest struct {
	Model      string           `json:"model"`
	Input      input            `json:"input"`
	Parameters createParameters `json:"parameters"`
}
type parameters struct {
	Resolution   string `json:"resolution"`
	Audio        *bool  `json:"audio"`
	Seed         *int64 `json:"seed"`
	PromptExtend *bool  `json:"prompt_extend"`
	Watermark    bool   `json:"watermark"`
}
type taskResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
		Code       string `json:"code"`
		Message    string `json:"message"`
	} `json:"output"`
}

func buildRequest(request generation.GeneratorRequest, model string) (createRequest, error) {
	return buildRequestWithURLs(request, model, nil)
}

func buildRequestWithURLs(request generation.GeneratorRequest, model string, uploadedURLs map[domain.ArtifactID]string) (createRequest, error) {
	var params parameters
	decoder := json.NewDecoder(strings.NewReader(request.Snapshot.ParametersJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return createRequest{}, errors.New("start Wan 3.0: invalid parameters")
	}
	if params.Resolution == "" {
		params.Resolution = "1080P"
	}
	if params.Resolution != "480P" && params.Resolution != "720P" && params.Resolution != "1080P" {
		return createRequest{}, errors.New("start Wan 3.0: unsupported resolution")
	}
	if params.Seed != nil && (*params.Seed < -1 || *params.Seed > 2_147_483_647) {
		return createRequest{}, errors.New("start Wan 3.0: invalid seed")
	}
	audio, promptExtend := true, true
	if params.Audio != nil {
		audio = *params.Audio
	}
	if params.PromptExtend != nil {
		promptExtend = *params.PromptExtend
	}
	ratio, ok := outputRatio(request.Snapshot.OutputWidth, request.Snapshot.OutputHeight)
	if !ok {
		return createRequest{}, errors.New("start Wan 3.0: unsupported output aspect ratio")
	}
	if request.Snapshot.GenerationMode == domain.ClipGenerationFirstImage || request.Snapshot.GenerationMode == domain.ClipGenerationFirstLastImage {
		ratio = "adaptive"
	}
	if request.Snapshot.RequestedDurationSeconds < 4 || request.Snapshot.RequestedDurationSeconds > 30 {
		return createRequest{}, errors.New("start Wan 3.0: invalid duration")
	}
	result := createRequest{Model: model, Input: input{Prompt: request.Snapshot.Prompt}, Parameters: createParameters{
		Resolution: params.Resolution, Ratio: ratio, Duration: request.Snapshot.RequestedDurationSeconds,
		Audio: audio, Seed: params.Seed, PromptExtend: promptExtend, Watermark: params.Watermark,
	}}
	for _, reference := range request.References {
		if reference.Input.PreparationRequired {
			return createRequest{}, errors.New("start Wan 3.0: required reference preparation is unavailable")
		}
		kind := ""
		referenceURL := ""
		switch reference.Input.Role {
		case domain.GenerationReferenceFirstImage:
			kind = "first_frame"
		case domain.GenerationReferenceLastImage:
			kind = "last_frame"
		case domain.GenerationReferenceImage:
			kind = "reference_image"
		case domain.GenerationReferenceVideo:
			kind = "reference_video"
			referenceURL = uploadedURLs[reference.Artifact.ID]
			if !strings.HasPrefix(referenceURL, "oss://") {
				return createRequest{}, errors.New("start Wan 3.0: reference video upload is unavailable")
			}
		case domain.GenerationReferenceAudio:
			kind = "reference_audio"
			referenceURL = uploadedURLs[reference.Artifact.ID]
			if !strings.HasPrefix(referenceURL, "oss://") {
				return createRequest{}, errors.New("start Wan 3.0: reference audio upload is unavailable")
			}
		default:
			return createRequest{}, errors.New("start Wan 3.0: unsupported local reference role")
		}
		if referenceURL == "" {
			var err error
			referenceURL, err = referenceDataURL(reference)
			if err != nil {
				return createRequest{}, err
			}
		}
		result.Input.Media = append(result.Input.Media, media{Type: kind, URL: referenceURL})
	}
	if result.Input.Prompt == "" && len(result.Input.Media) == 0 {
		return createRequest{}, errors.New("start Wan 3.0: input is required")
	}
	return result, nil
}

func referenceDataURL(reference generation.GeneratorArtifactReference) (string, error) {
	info, err := os.Lstat(reference.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != reference.Artifact.ByteSize || info.Size() > 20<<20 {
		return "", errors.New("start Wan 3.0: reference file is unavailable")
	}
	data, err := os.ReadFile(reference.AbsolutePath)
	if err != nil {
		return "", errors.New("start Wan 3.0: reference file is unreadable")
	}
	return "data:" + reference.Artifact.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

type uploadPolicyResponse struct {
	Data struct {
		Policy             string `json:"policy"`
		Signature          string `json:"signature"`
		UploadDirectory    string `json:"upload_dir"`
		UploadHost         string `json:"upload_host"`
		OSSAccessKeyID     string `json:"oss_access_key_id"`
		OSSObjectACL       string `json:"x_oss_object_acl"`
		OSSForbidOverwrite string `json:"x_oss_forbid_overwrite"`
		MaximumFileSizeMB  int64  `json:"max_file_size_mb"`
	} `json:"data"`
}

func (p *Provider) uploadTemporaryFile(ctx context.Context, uploadBase, secret, model string, reference generation.GeneratorArtifactReference) (string, error) {
	policyURL := uploadBase + "/api/v1/uploads?action=getPolicy&model=" + url.QueryEscape(model)
	policyRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, policyURL, nil)
	policyRequest.Header.Set("Authorization", "Bearer "+secret)
	policyRequest.Header.Set("Content-Type", "application/json")
	policyResponse, err := p.client.Do(policyRequest)
	if err != nil {
		return "", errors.New("start Wan 3.0: temporary upload is unavailable")
	}
	defer policyResponse.Body.Close()
	if policyResponse.StatusCode != http.StatusOK {
		return "", providerHTTPError(policyResponse, secret)
	}
	var policy uploadPolicyResponse
	if err := decodeBounded(policyResponse.Body, &policy); err != nil {
		return "", errors.New("start Wan 3.0: invalid temporary upload credential")
	}
	uploadHost, err := url.Parse(policy.Data.UploadHost)
	if err != nil || uploadHost.Scheme != "https" || uploadHost.User != nil || uploadHost.Host == "" || !strings.HasSuffix(strings.ToLower(uploadHost.Hostname()), ".aliyuncs.com") || strings.Contains(policy.Data.UploadDirectory, "..") || strings.Trim(policy.Data.UploadDirectory, "/") == "" || policy.Data.Policy == "" || policy.Data.Signature == "" || policy.Data.OSSAccessKeyID == "" {
		return "", errors.New("start Wan 3.0: invalid temporary upload credential")
	}
	info, err := os.Lstat(reference.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != reference.Artifact.ByteSize || info.Size() < 1 || policy.Data.MaximumFileSizeMB > 0 && info.Size() > policy.Data.MaximumFileSizeMB<<20 {
		return "", errors.New("start Wan 3.0: reference media is unavailable for upload")
	}
	filename := filepath.Base(reference.Artifact.OriginalFilename)
	if filename == "." || filename == "" {
		filename = reference.Artifact.ID.String() + filepath.Ext(reference.AbsolutePath)
	}
	if reference.Artifact.MIMEType == "video/mp4" && strings.ToLower(filepath.Ext(filename)) != ".mp4" {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".mp4"
	}
	key := strings.TrimRight(policy.Data.UploadDirectory, "/") + "/" + filename
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeResult := make(chan error, 1)
	go func() {
		fields := []struct{ name, value string }{
			{"OSSAccessKeyId", policy.Data.OSSAccessKeyID}, {"policy", policy.Data.Policy}, {"Signature", policy.Data.Signature},
			{"key", key}, {"x-oss-object-acl", policy.Data.OSSObjectACL}, {"x-oss-forbid-overwrite", policy.Data.OSSForbidOverwrite}, {"success_action_status", "200"},
		}
		var writeErr error
		for _, field := range fields {
			if writeErr = multipartWriter.WriteField(field.name, field.value); writeErr != nil {
				break
			}
		}
		if writeErr == nil {
			var file *os.File
			file, writeErr = os.Open(reference.AbsolutePath)
			if writeErr == nil {
				var part io.Writer
				part, writeErr = multipartWriter.CreateFormFile("file", filename)
				if writeErr == nil {
					_, writeErr = io.Copy(part, file)
				}
				_ = file.Close()
			}
		}
		if closeErr := multipartWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
		writeResult <- writeErr
	}()
	uploadRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, uploadHost.String(), reader)
	uploadRequest.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	uploadResponse, requestErr := p.client.Do(uploadRequest)
	_ = reader.Close()
	writeErr := <-writeResult
	if uploadResponse != nil {
		defer uploadResponse.Body.Close()
	}
	if requestErr != nil || writeErr != nil || uploadResponse == nil || uploadResponse.StatusCode != http.StatusOK {
		return "", errors.New("start Wan 3.0: reference media upload failed")
	}
	return "oss://" + key, nil
}

func route(record providerconfig.Record) (string, string, error) {
	region, ok := endpointRegions[record.EndpointPreset]
	if record.GeneratorID != domain.GeneratorVideoWanOfficial || !ok || !validWorkspaceID(record.WorkspaceID) || record.ResourceID != "" || record.ModelID != "wan3.0-video" && record.ModelID != "wan3.0-video-prime" {
		return "", "", errors.New("invalid Wan 3.0 route")
	}
	base := "https://" + record.WorkspaceID + "." + region + ".maas.aliyuncs.com"
	modelListBase := base
	if region == "ap-southeast-1" {
		modelListBase = "https://dashscope-intl.aliyuncs.com"
	}
	return base, modelListBase, nil
}

func validWorkspaceID(value string) bool {
	if value == "" || len(value) > 128 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func (p *Provider) download(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || unsafeHost(parsed.Hostname()) {
		return "", errors.New("download Wan 3.0 result: unsafe URL")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	response, err := p.client.Do(request)
	if err != nil {
		return "", errors.New("download Wan 3.0 result: unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("download Wan 3.0 result: upstream rejected download")
	}
	file, err := os.CreateTemp(p.workingDirectory, "wan-*.mp4")
	if err != nil {
		return "", errors.New("download Wan 3.0 result: create staging file")
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
		return "", errors.New("download Wan 3.0 result: invalid media")
	}
	keep = true
	return path, nil
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
		return generation.NewRetryablePollError(errors.New("Wan 3.0 rate limit exceeded"))
	}
	if status >= http.StatusInternalServerError && status <= 599 {
		return generation.NewRetryablePollError(errors.New("Wan 3.0 service is unavailable"))
	}
	switch status {
	case 401, 403:
		return errors.New("Wan 3.0 authentication failed")
	case 402:
		return errors.New("Wan 3.0 balance is insufficient")
	case 400, 422:
		return errors.New("Wan 3.0 rejected the request")
	default:
		return errors.New("Wan 3.0 service is unavailable")
	}
}

func providerHTTPError(response *http.Response, secret string) error {
	err := generation.NewProviderHTTPError("Wan 3.0", response.StatusCode, response.Header, response.Body, secret)
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError && response.StatusCode <= 599 {
		return generation.NewRetryablePollError(err)
	}
	return err
}

func validOpaque(value string) bool {
	return value != "" && len(value) <= 500 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func outputRatio(width, height int) (string, bool) {
	for ratio, dimensions := range map[string][2]int{"16:9": {16, 9}, "4:3": {4, 3}, "1:1": {1, 1}, "3:4": {3, 4}, "9:16": {9, 16}} {
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

type Registry struct{ Provider *Provider }

func (r Registry) Generator(id domain.GeneratorID) (generation.Generator, bool) {
	if r.Provider != nil && id == domain.GeneratorVideoWanOfficial {
		return r.Provider, true
	}
	return nil, false
}

type Catalog struct{ Provider *Provider }

func (c Catalog) Generator(ctx context.Context, id domain.GeneratorID) (projectapp.GenerationCapability, error) {
	if c.Provider == nil || id != domain.GeneratorVideoWanOfficial {
		return projectapp.GenerationCapability{}, errors.New("Wan 3.0 Provider not found")
	}
	return c.Provider.Capability(ctx)
}

func (c Catalog) GeneratorModel(ctx context.Context, id domain.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	if c.Provider == nil || id != domain.GeneratorVideoWanOfficial {
		return projectapp.GenerationCapability{}, errors.New("Wan 3.0 Provider not found")
	}
	return c.Provider.CapabilityModel(ctx, modelID)
}

var _ generation.Generator = (*Provider)(nil)
var _ providerconfig.Checker = (*Provider)(nil)
