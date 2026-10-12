package shotforwardcloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

type cloudTask struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	CanCancel bool    `json:"can_cancel"`
	Outputs   []struct {
		ID        string `json:"id"`
		MediaType string `json:"media_type"`
	} `json:"outputs"`
	Error *struct{ Code, Message string } `json:"error"`
}

const (
	cloudImageCapabilityVersion = "shotforward-cloud-v1"
	cloudH3CapabilityVersion    = "shotforward-cloud-h3-v2@2026-09-28"
)

type Generator struct {
	manager          *Manager
	id               domain.GeneratorID
	kind             domain.MediaKind
	workingDirectory string
}

func NewGenerator(manager *Manager, id domain.GeneratorID, workingDirectory string) (*Generator, error) {
	if manager == nil || (id != domain.GeneratorImageShotForward && id != domain.GeneratorVideoH3ShotForward) || !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, errors.New("invalid ShotForward cloud Generator configuration")
	}
	if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(workingDirectory, 0o700); err != nil {
		return nil, err
	}
	return &Generator{manager: manager, id: id, kind: id.MediaKind(), workingDirectory: workingDirectory}, nil
}

func (g *Generator) ID() domain.GeneratorID                               { return g.id }
func (g *Generator) ConcurrencyLimit() int                                { return 5 }
func (g *Generator) SupportsRecovery(domain.GenerationInputSnapshot) bool { return true }
func (g *Generator) PollInterval() time.Duration                          { return 2 * time.Second }

func (g *Generator) Start(ctx context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if request.Validate() != nil || request.Snapshot.GeneratorID != g.id || request.Snapshot.TargetMediaKind != g.kind || strings.TrimSpace(request.Snapshot.ProviderModelID) == "" {
		return generation.GeneratorOperation{}, errors.New("invalid ShotForward cloud generation request")
	}
	inputIDs := make([]string, 0, len(request.References))
	routedInputs := make([]map[string]string, 0, len(request.References))
	for _, reference := range request.References {
		content, err := os.ReadFile(reference.AbsolutePath)
		if err != nil {
			return generation.GeneratorOperation{}, err
		}
		digest := sha256.Sum256(content)
		var upload struct {
			ID     string `json:"id"`
			Upload struct {
				URL             string            `json:"url"`
				RequiredHeaders map[string]string `json:"required_headers"`
			} `json:"upload"`
		}
		if err = g.manager.DoAuthenticated(ctx, "POST", "/v1/media-inputs", map[string]any{
			"media_type": reference.Artifact.MIMEType, "byte_size": len(content), "checksum_sha256": hex.EncodeToString(digest[:]),
		}, &upload); err != nil {
			return generation.GeneratorOperation{}, err
		}
		var accepted struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			MediaType string `json:"media_type"`
			ByteSize  int64  `json:"byte_size"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		}
		if err = g.manager.UploadAuthenticatedJSON(ctx, upload.Upload.URL, content, upload.Upload.RequiredHeaders, &accepted); err != nil {
			return generation.GeneratorOperation{}, err
		}
		requiresDimensions := reference.Artifact.Kind == domain.MediaKindImage || reference.Artifact.Kind == domain.MediaKindVideo
		if accepted.ID != upload.ID || accepted.Status != "ready" || accepted.MediaType != reference.Artifact.MIMEType || accepted.ByteSize != int64(len(content)) || requiresDimensions && (accepted.Width < 1 || accepted.Height < 1) {
			return generation.GeneratorOperation{}, generation.NewGeneratorFailure(generation.GeneratorProtocolFailureCode, ErrCloudResponse)
		}
		inputIDs = append(inputIDs, upload.ID)
		routedInputs = append(routedInputs, map[string]string{
			"artifact_id": upload.ID,
			"role":        string(reference.Input.Role),
		})
	}
	snapshot := request.Snapshot
	body := map[string]any{"model": snapshot.ProviderModelID, "prompt": snapshot.Prompt, "n": 1, "input_artifact_ids": inputIDs}
	path := "/v1/image-tasks"
	if g.kind == domain.MediaKindImage {
		body["quality"] = "high"
		body["size"] = cloudImageSize(snapshot.OutputWidth, snapshot.OutputHeight)
	} else {
		path = "/v1/video-tasks"
		body["size"] = cloudVideoSize(snapshot.OutputWidth, snapshot.OutputHeight)
		body["duration"] = snapshot.RequestedDurationSeconds
		body["generation_mode"] = snapshot.GenerationMode
		body["inputs"] = routedInputs
		delete(body, "input_artifact_ids")
	}
	var task cloudTask
	if err := g.manager.DoAuthenticatedWithHeaders(ctx, "POST", path, body, &task, map[string]string{"Idempotency-Key": "desktop-" + request.JobID.String()}); err != nil {
		return generation.GeneratorOperation{}, err
	}
	if strings.TrimSpace(task.ID) == "" {
		return generation.GeneratorOperation{}, ErrCloudResponse
	}
	return generation.GeneratorOperation{ID: task.ID}, nil
}

func (g *Generator) Poll(ctx context.Context, operation generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	if operation.Validate() != nil {
		return generation.GeneratorStatus{}, errors.New("invalid ShotForward cloud operation")
	}
	var task cloudTask
	if err := g.manager.DoAuthenticated(ctx, "GET", "/v1/tasks/"+operation.ID, nil, &task); err != nil {
		return generation.GeneratorStatus{}, generation.NewRetryablePollError(err)
	}
	switch task.Status {
	case "queued", "leased", "running", "validating", "retry_wait":
		return generation.GeneratorStatus{State: generation.GeneratorRunning, Stage: task.Status, Message: fmt.Sprintf("ShotForward cloud generation %.0f%%", task.Progress*100)}, nil
	case "cancelled":
		return generation.GeneratorStatus{State: generation.GeneratorStopped, Stage: "cancelled", Message: "ShotForward cloud generation cancelled"}, nil
	case "failed":
		code, message := "cloud_generation_failed", "ShotForward cloud generation failed"
		if task.Error != nil {
			if task.Error.Code != "" {
				code = task.Error.Code
			}
			if task.Error.Message != "" {
				message = task.Error.Message
			}
		}
		if businessCode := cloudFailureCode(code); businessCode != "" {
			return generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", FailureCode: businessCode, FailureMessage: "ShotForward account cannot accept this request"}, nil
		}
		return generation.GeneratorStatus{State: generation.GeneratorFailed, Stage: "failed", Message: message, FailureCode: generation.ProviderTaskFailureCode, FailureMessage: generation.ProviderTaskFailureMessage("ShotForward", code, message, "")}, nil
	case "succeeded":
		if len(task.Outputs) != 1 {
			return generation.GeneratorStatus{}, ErrCloudResponse
		}
		var access struct {
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		path := "/v1/tasks/" + operation.ID + "/artifacts/" + task.Outputs[0].ID + "/access"
		if err := g.manager.DoAuthenticated(ctx, "POST", path, map[string]string{"purpose": "download"}, &access); err != nil {
			return generation.GeneratorStatus{}, generation.NewRetryablePollError(err)
		}
		extension := extensionForMedia(task.Outputs[0].MediaType, g.kind)
		target := filepath.Join(g.workingDirectory, operation.ID+extension)
		temporary, err := os.CreateTemp(g.workingDirectory, ".download-*")
		if err != nil {
			return generation.GeneratorStatus{}, err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if err = temporary.Chmod(0o600); err == nil {
			err = g.manager.DownloadAuthenticated(ctx, access.URL, temporary)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return generation.GeneratorStatus{}, generation.NewRetryablePollError(err)
		}
		if err = os.Rename(temporaryPath, target); err != nil {
			return generation.GeneratorStatus{}, err
		}
		return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "succeeded", Message: "ShotForward cloud generation completed", Outputs: []generation.GeneratorOutput{{SourcePath: target}}}, nil
	default:
		return generation.GeneratorStatus{}, ErrCloudResponse
	}
}

func (g *Generator) Stop(ctx context.Context, operation generation.GeneratorOperation) error {
	if operation.Validate() != nil {
		return errors.New("invalid ShotForward cloud operation")
	}
	var current cloudTask
	if err := g.manager.DoAuthenticated(ctx, "GET", "/v1/tasks/"+operation.ID, nil, &current); err != nil {
		return err
	}
	if !current.CanCancel {
		return nil
	}
	err := g.manager.DoAuthenticated(ctx, "POST", "/v1/tasks/"+operation.ID+"/cancel", nil, &cloudTask{})
	if IsHTTPStatus(err, http.StatusConflict) {
		// Claim and cancellation race under the same server-side row lock. Once
		// claimed, the result remains authoritative and polling must continue.
		return nil
	}
	return err
}

func cloudImageSize(width, height int) string {
	// Asset generation uses the hosted 1.5K canvases directly. Timeline frames
	// retain the legacy orientation mapping and are normalized by the daemon to
	// their exact project output size after download.
	switch [2]int{width, height} {
	case [2]int{1536, 1536}, [2]int{1536, 864}, [2]int{864, 1536}:
		return fmt.Sprintf("%dx%d", width, height)
	}
	if width == height {
		return "1024x1024"
	}
	if width > height {
		return "1280x720"
	}
	return "720x1280"
}

func cloudVideoSize(width, height int) string {
	if width == height {
		return "1024x1024"
	}
	if width > height {
		return "1280x720"
	}
	return "720x1280"
}

func extensionForMedia(mediaType string, kind domain.MediaKind) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	}
	if kind == domain.MediaKindVideo {
		return ".mp4"
	}
	return ".png"
}

type Registry struct{ Image, Video *Generator }

func (r Registry) Generator(id domain.GeneratorID) (generation.Generator, bool) {
	if r.Image != nil && id == domain.GeneratorImageShotForward {
		return r.Image, true
	}
	if r.Video != nil && id == domain.GeneratorVideoH3ShotForward {
		return r.Video, true
	}
	return nil, false
}

type Catalog struct{ Manager *Manager }

func (c Catalog) Generator(ctx context.Context, id domain.GeneratorID) (projectapp.GenerationCapability, error) {
	models, err := c.Manager.Models(ctx)
	if err != nil {
		return projectapp.GenerationCapability{}, err
	}
	wanted := "image"
	if id == domain.GeneratorVideoH3ShotForward {
		wanted = "video"
	} else if id != domain.GeneratorImageShotForward {
		return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
	}
	sortCloudModels(models)
	for _, model := range models {
		if model.ProductKind == wanted && modelAvailable(model) {
			return c.GeneratorModel(ctx, id, model.ID)
		}
	}
	return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
}

func (c Catalog) GeneratorModel(ctx context.Context, id domain.GeneratorID, modelID string) (projectapp.GenerationCapability, error) {
	models, err := c.Manager.Models(ctx)
	if err != nil {
		return projectapp.GenerationCapability{}, err
	}
	wanted := "image"
	kind := domain.MediaKindImage
	if id == domain.GeneratorVideoH3ShotForward {
		wanted, kind = "video", domain.MediaKindVideo
	} else if id != domain.GeneratorImageShotForward {
		return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
	}
	var found *Model
	for _, model := range models {
		if model.ID == modelID && model.ProductKind == wanted && modelAvailable(model) {
			copy := model
			found = &copy
			break
		}
	}
	if found == nil {
		return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
	}
	if kind == domain.MediaKindVideo {
		capability, capabilityErr := videoModelCapability(*found)
		if capabilityErr != nil {
			return projectapp.GenerationCapability{}, generation.ErrGeneratorNotFound
		}
		return capability, nil
	}
	capabilityVersion := cloudImageCapabilityVersion
	if found.CapabilityVersion != "" {
		capabilityVersion = found.CapabilityVersion
	}
	capability := projectapp.GenerationCapability{GeneratorID: id, ProviderModelID: modelID, ProviderConnectionRevision: 1, CapabilityVersion: capabilityVersion, MediaKind: kind, SupportsStop: true, SupportsRecovery: true,
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: domain.GenerationReferenceImage, MaximumCount: 5, MaximumBytes: 256 << 20, AcceptedMIMETypes: []string{"image/png", "image/jpeg", "image/webp"}, SupportsAutomaticPreparation: true}}}
	return capability, nil
}
