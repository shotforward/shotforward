package shotforwardcloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/projecthttp"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

// Exercise the real public HTTP, application, persistence, scheduler, and cloud
// adapter path. Only the cloud transport and image file preparation are mocked;
// all project data and image bytes live in t.TempDir().
func TestAssetImageMultipleReferencesReachCloudInOrderAndAreIdempotent(t *testing.T) {
	t.Parallel()
	// This deadline covers database migrations and image processing as well as
	// the mocked requests; allow slower Windows CI filesystems to finish.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	var referenceBytes [][]byte
	var uploaded [][]byte
	var inputIDs []string
	var taskInputs []string
	var taskKey string
	var taskCount int
	output := referencePNG(t, 1536, 864, color.RGBA{R: 180, G: 100, B: 40, A: 255})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-test", RefreshToken: "refresh-test", ExpiresIn: 900}), nil
		case "/v1/models":
			return jsonResponse(http.StatusOK, map[string]any{"data": []Model{{ID: "shotforward-image-test", ProductKind: "image"}}}), nil
		case "/v1/media-inputs":
			var input struct {
				Checksum string `json:"checksum_sha256"`
				ByteSize int    `json:"byte_size"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				return nil, err
			}
			index := len(inputIDs)
			if index >= len(referenceBytes) {
				return nil, errors.New("unexpected extra reference upload")
			}
			digest := sha256.Sum256(referenceBytes[index])
			if input.Checksum != hex.EncodeToString(digest[:]) || input.ByteSize != len(referenceBytes[index]) {
				t.Errorf("reference %d changed order or bytes: %+v", index, input)
			}
			id := fmt.Sprintf("input-%d", index+1)
			inputIDs = append(inputIDs, id)
			return jsonResponse(http.StatusCreated, map[string]any{"id": id, "upload": map[string]any{"url": "https://cloud.example/uploads/" + id, "required_headers": map[string]string{"Content-Type": "image/png"}}}), nil
		case "/v1/image-tasks":
			var task struct {
				InputIDs []string `json:"input_artifact_ids"`
				Model    string   `json:"model"`
			}
			if err := json.NewDecoder(request.Body).Decode(&task); err != nil {
				return nil, err
			}
			taskCount++
			taskInputs, taskKey = task.InputIDs, request.Header.Get("Idempotency-Key")
			if task.Model != "shotforward-image-test" || !reflect.DeepEqual(taskInputs, inputIDs) {
				t.Errorf("cloud task lost model or ordered inputs: %+v", task)
			}
			return jsonResponse(http.StatusCreated, map[string]any{"id": "task-test", "kind": "image", "status": "queued"}), nil
		case "/v1/tasks/task-test":
			return jsonResponse(http.StatusOK, map[string]any{"id": "task-test", "kind": "image", "status": "succeeded", "outputs": []map[string]string{{"id": "output-test", "media_type": "image/png"}}}), nil
		case "/v1/tasks/task-test/artifacts/output-test/access":
			return jsonResponse(http.StatusOK, map[string]any{"url": "https://cloud.example/output.png"}), nil
		case "/output.png":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(output))}, nil
		default:
			if strings.HasPrefix(request.URL.Path, "/uploads/input-") {
				content, err := io.ReadAll(request.Body)
				index := len(uploaded)
				if err != nil || index >= len(referenceBytes) || !bytes.Equal(content, referenceBytes[index]) || request.Method != http.MethodPut {
					return nil, errors.New("reference upload did not preserve ordered bytes")
				}
				uploaded = append(uploaded, content)
				return jsonResponse(http.StatusOK, map[string]any{"id": inputIDs[index], "status": "ready", "media_type": "image/png", "byte_size": len(content), "width": 32, "height": 32}), nil
			}
			return nil, fmt.Errorf("unexpected mock cloud request: %s %s", request.Method, request.URL.Path)
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ExchangeDesktopCode(ctx, "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{Manager: manager}
	capability, err := catalog.GeneratorModel(ctx, project.GeneratorImageShotForward, "shotforward-image-test")
	if err != nil || len(capability.ReferenceCapabilities) != 1 || capability.ReferenceCapabilities[0].MaximumCount != 5 {
		t.Fatalf("actual cloud image reference capability = %+v, %v", capability, err)
	}
	studioStore, err := studio.Open(ctx, filepath.Join(root, "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	processor := referenceImageProcessor{}
	service, err := projectapp.NewService(projectapp.Dependencies{IDs: projectapp.NewSecureIDGenerator(), Clock: projectapp.UTCClock{}, Workspace: platformpaths.ApplicationProjectWorkspace{}, Stores: projectstore.ApplicationStoreFactory{}, Library: library, Importer: scriptimport.FileImporter{}, Registry: registry, MediaFiles: processor, GenerationCatalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: filepath.Join(root, "project"), Name: "Multi-reference test", OutputWidth: 1280, OutputHeight: 720})
	if err != nil {
		t.Fatal(err)
	}
	episode, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []project.ArtifactID
	var fixtureBytes [][]byte
	for index := 0; index < 6; index++ {
		asset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, Kind: project.AssetKindProp, Name: fmt.Sprintf("Reference %d", index+1)})
		if err != nil {
			t.Fatal(err)
		}
		content := referencePNG(t, 32, 32, color.RGBA{R: byte(index * 30), G: 100, B: 200, A: 255})
		path := filepath.Join(root, fmt.Sprintf("reference-%d.png", index))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		slotID, _ := project.FixedMediaSlotID(asset.Asset.Asset.ID.String(), project.MediaSlotAssetPrimaryImage)
		media, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: created.Project.ID, MediaSlotID: slotID, SourcePath: path, ExpectedSlotRevision: 1, MakeCurrent: true})
		if err != nil {
			t.Fatal(err)
		}
		artifacts, fixtureBytes = append(artifacts, media.Current.ArtifactID), append(fixtureBytes, content)
	}
	target, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{ProjectID: created.Project.ID, EpisodeID: episode.Episode.ID, Kind: project.AssetKindCharacter, Name: "Target"})
	if err != nil {
		t.Fatal(err)
	}
	slotID, _ := project.FixedMediaSlotID(target.Asset.Asset.ID.String(), project.MediaSlotAssetPrimaryImage)
	var references []map[string]string
	for _, index := range []int{4, 1, 3, 0, 2} {
		references = append(references, map[string]string{"role": string(project.GenerationReferenceImage), "artifact_id": artifacts[index].String()})
		referenceBytes = append(referenceBytes, fixtureBytes[index])
	}
	handler, err := projecthttp.NewHandler(projecthttp.Options{Projects: service, Catalog: library})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(key string, revision int64, refs []map[string]string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"episode_id": episode.Episode.ID.String(), "operation_key": key, "targets": []map[string]any{{"media_slot_id": slotID.String(), "expected_slot_revision": revision, "prompt": "Use all five reference images in order.", "generator_id": project.GeneratorImageShotForward, "provider_model_id": "shotforward-image-test", "references": refs, "parameters": map[string]any{}}}})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+created.Project.ID.String()+"/generation/jobs", bytes.NewReader(body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := submit("multiple-references", 1, references)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit five references: %d %s", response.Code, response.Body.String())
	}
	var public struct {
		Jobs []struct {
			ID project.GenerationJobID `json:"generation_job_id"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil || len(public.Jobs) != 1 {
		t.Fatalf("public generation response = %s, %v", response.Body.String(), err)
	}
	job, err := service.GetGenerationJob(ctx, projectapp.GetGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: public.Jobs[0].ID})
	if err != nil || len(job.Snapshot.References) != 5 {
		t.Fatalf("frozen multiple references = %+v, %v", job, err)
	}
	for index, ref := range job.Snapshot.References {
		if ref.ArtifactID.String() != references[index]["artifact_id"] {
			t.Fatalf("snapshot reference %d lost input order: %+v", index, ref)
		}
	}
	generator, err := NewGenerator(manager, project.GeneratorImageShotForward, filepath.Join(root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := generation.NewScheduler(generation.SchedulerOptions{Registry: registry, Generators: referenceGeneratorRegistry{generator}, MediaFiles: processor, IDs: projectapp.NewSecureIDGenerator(), Clock: projectapp.UTCClock{}, GlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := scheduler.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("process multiple-reference job = %v, %v", worked, err)
	}
	completed, err := service.GetGenerationJob(ctx, projectapp.GetGenerationJobInput{ProjectID: created.Project.ID, GenerationJobID: job.ID})
	if err != nil || completed.State != project.GenerationJobSucceeded || len(uploaded) != 5 || taskCount != 1 || taskKey != "desktop-"+job.ID.String() {
		t.Fatalf("multi-reference cloud result = %+v, %v; uploads=%d tasks=%d key=%q", completed, err, len(uploaded), taskCount, taskKey)
	}
	repeated := submit("multiple-references", 1, references)
	if repeated.Code != http.StatusAccepted || !strings.Contains(repeated.Body.String(), job.ID.String()) {
		t.Fatalf("idempotent repeat = %d %s", repeated.Code, repeated.Body.String())
	}
	if worked, err := scheduler.ProcessNext(ctx); err != nil || worked || taskCount != 1 || len(uploaded) != 5 {
		t.Fatalf("repeat submitted duplicate provider work: worked=%v err=%v tasks=%d uploads=%d", worked, err, taskCount, len(uploaded))
	}
	reordered := append([]map[string]string(nil), references...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	conflict := submit("multiple-references", 1, reordered)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"idempotency_conflict"`) {
		t.Fatalf("reordered idempotency input = %d %s", conflict.Code, conflict.Body.String())
	}
	slot, err := service.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: created.Project.ID, MediaSlotID: slotID})
	if err != nil {
		t.Fatal(err)
	}
	tooMany := append(append([]map[string]string(nil), references...), map[string]string{"role": string(project.GenerationReferenceImage), "artifact_id": artifacts[5].String()})
	rejected := submit("too-many-references", slot.Slot.Revision, tooMany)
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), `"code":"capability_unsupported"`) || !strings.Contains(rejected.Body.String(), `"constraint":"reference_count"`) || !strings.Contains(rejected.Body.String(), `"actual":6`) || !strings.Contains(rejected.Body.String(), `"maximum":5`) {
		t.Fatalf("six references were not rejected with the model limit: %d %s", rejected.Code, rejected.Body.String())
	}
	if worked, err := scheduler.ProcessNext(ctx); err != nil || worked || taskCount != 1 || len(uploaded) != 5 {
		t.Fatalf("invalid references reached provider: worked=%v err=%v tasks=%d uploads=%d", worked, err, taskCount, len(uploaded))
	}
}

func referencePNG(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for index := 0; index < len(value.Pix); index += 4 {
		value.Pix[index], value.Pix[index+1], value.Pix[index+2], value.Pix[index+3] = fill.R, fill.G, fill.B, fill.A
	}
	var content bytes.Buffer
	if err := png.Encode(&content, value); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}

type referenceGeneratorRegistry struct{ generator generation.Generator }

func (r referenceGeneratorRegistry) Generator(id project.GeneratorID) (generation.Generator, bool) {
	return r.generator, id == r.generator.ID()
}

// This PNG-only test processor keeps the end-to-end queue test independent of
// an installed ffmpeg while validating and copying the actual fixture bytes.
type referenceImageProcessor struct{}

func (referenceImageProcessor) Prepare(_ context.Context, request projectapp.PrepareMediaRequest) (projectapp.PreparedMedia, error) {
	content, err := os.ReadFile(request.SourcePath)
	if err != nil {
		return projectapp.PreparedMedia{}, err
	}
	configuration, err := png.DecodeConfig(bytes.NewReader(content))
	if err != nil || request.Kind != project.MediaKindImage {
		return projectapp.PreparedMedia{}, errors.New("invalid test PNG")
	}
	if request.RequiredWidth != 0 && (configuration.Width != request.RequiredWidth || configuration.Height != request.RequiredHeight) {
		return projectapp.PreparedMedia{}, errors.New("test PNG dimensions differ from generation target")
	}
	digest := sha256.Sum256(content)
	return projectapp.PreparedMedia{Token: request.SourcePath, Kind: project.MediaKindImage, Extension: ".png", SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(content)), MIMEType: "image/png", OriginalFilename: filepath.Base(request.SourcePath), Width: configuration.Width, Height: configuration.Height}, nil
}

func (referenceImageProcessor) PrepareVideoFrame(context.Context, projectapp.PrepareVideoFrameRequest) (projectapp.PreparedMedia, error) {
	return projectapp.PreparedMedia{}, errors.New("video preparation is not used by this image test")
}

func (referenceImageProcessor) Install(_ context.Context, root string, prepared projectapp.PreparedMedia, id project.ArtifactID) (projectapp.InstalledMedia, error) {
	content, err := os.ReadFile(prepared.Token)
	if err != nil {
		return projectapp.InstalledMedia{}, err
	}
	relative := "media/images/" + id.String() + ".png"
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return projectapp.InstalledMedia{}, err
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return projectapp.InstalledMedia{}, err
	}
	return projectapp.InstalledMedia{RelativePath: relative}, nil
}

func (referenceImageProcessor) Discard(string, projectapp.PreparedMedia) error { return nil }
func (referenceImageProcessor) Remove(root string, installed projectapp.InstalledMedia) error {
	return os.Remove(filepath.Join(root, filepath.FromSlash(installed.RelativePath)))
}
