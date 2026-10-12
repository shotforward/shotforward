package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	"github.com/shotforward/shotforward/daemon/internal/config"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/exportjob"
	"github.com/shotforward/shotforward/daemon/internal/generation"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
)

func TestHostEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_HOST_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_HOST_E2E=1 to run loopback host integration")
	}

	root := t.TempDir()
	directories := platformpaths.ApplicationDirs{
		Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"),
		Logs: filepath.Join(root, "data", "logs"), Runtime: filepath.Join(root, "data", "runtime"),
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	bootstrapReader, bootstrapWriter := io.Pipe()
	defer bootstrapReader.Close()
	var logs bytes.Buffer
	runtime := &hostTestRuntime{}
	generator := &hostTestGenerator{}
	providerConfigurations := &hostTestProviderConfigurations{}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunWithOptions(ctx, config.Default(), slog.New(slog.NewTextHandler(&logs, nil)), Options{
			Directories: directories, Listener: listener, BootstrapWriter: bootstrapWriter,
			GenerationCatalog: hostTestGenerationCatalog{}, Generators: hostTestGeneratorRegistry{generator: generator}, MediaFiles: hostTestMediaFiles{}, ExportProcessor: hostTestExportProcessor{},
			ProviderConfigurations: providerConfigurations,
			RuntimeFactory: func(config.Config, platformpaths.ApplicationDirs, agentmcp.Credential) (RuntimeResource, error) {
				return RuntimeResource{Runtime: runtime, Close: runtime.Close}, nil
			},
		})
	}()

	var announcement bootstrapAnnouncement
	if err := json.NewDecoder(bootstrapReader).Decode(&announcement); err != nil {
		cancel()
		t.Fatalf("decode bootstrap announcement: %v", err)
	}
	if announcement.Address == "" || announcement.PairingCode == "" || announcement.InstanceID == "" {
		cancel()
		t.Fatalf("bootstrap announcement is incomplete: %#v", announcement)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	origin := announcement.Address
	health := hostRequest(t, client, http.MethodGet, origin+"/healthz", "", "", "")
	if health.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("health status = %d", health.StatusCode)
	}
	health.Body.Close()

	bootstrap := hostRequest(t, client, http.MethodPost, origin+"/api/v1/bootstrap", `{"pairing_code":"`+announcement.PairingCode+`"}`, origin, "")
	if bootstrap.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("bootstrap status = %d: %s", bootstrap.StatusCode, limitedBody(bootstrap))
	}
	var bootstrapBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	decodeHostResponse(t, bootstrap, &bootstrapBody)
	if bootstrapBody.CSRFToken == "" {
		cancel()
		t.Fatal("bootstrap returned an empty CSRF token")
	}
	replay := hostRequest(t, &http.Client{Timeout: 5 * time.Second}, http.MethodPost, origin+"/api/v1/bootstrap", `{"pairing_code":"`+announcement.PairingCode+`"}`, origin, "")
	if replay.StatusCode != http.StatusUnauthorized {
		cancel()
		t.Fatalf("pairing replay status = %d", replay.StatusCode)
	}
	replay.Body.Close()
	configuredProvider := hostRequest(t, client, http.MethodPut, origin+"/api/v1/generation-providers/video.h3.official",
		`{"endpoint_preset":"minimax_global","model_id":"MiniMax-H3","resource_id":"","workspace_id":"","api_key":"host-secret","enabled":true,"concurrency_limit":1,"expected_revision":0}`,
		origin, bootstrapBody.CSRFToken)
	providerBody := limitedBody(configuredProvider)
	if configuredProvider.StatusCode != http.StatusCreated || strings.Contains(providerBody, "host-secret") || !strings.Contains(providerBody, `"credential_configured":true`) {
		cancel()
		t.Fatalf("configure Provider response = %d: %s", configuredProvider.StatusCode, providerBody)
	}

	projectRoot := filepath.Join(root, "portable-project")
	createBody, _ := json.Marshal(map[string]any{"root_path": projectRoot, "name": "Host Integration", "description": "WP-05D", "output_width": 720, "output_height": 1280})
	created := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects", string(createBody), origin, bootstrapBody.CSRFToken)
	if created.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create project status = %d: %s", created.StatusCode, limitedBody(created))
	}
	var projectResult struct {
		Project struct {
			ProjectID string `json:"project_id"`
		} `json:"project"`
		Episodes []struct {
			EpisodeID      string `json:"episode_id"`
			ScriptRevision int64  `json:"script_revision"`
		} `json:"episodes"`
	}
	decodeHostResponse(t, created, &projectResult)
	if projectResult.Project.ProjectID == "" || len(projectResult.Episodes) != 0 {
		cancel()
		t.Fatalf("unexpected create response: %#v", projectResult)
	}
	projectID := projectResult.Project.ProjectID
	firstEpisode := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes", `{"target_duration_ms":60000}`, origin, bootstrapBody.CSRFToken)
	if firstEpisode.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create first Episode status = %d: %s", firstEpisode.StatusCode, limitedBody(firstEpisode))
	}
	var firstEpisodeResult struct {
		Episode struct {
			EpisodeID string `json:"episode_id"`
		} `json:"episode"`
	}
	decodeHostResponse(t, firstEpisode, &firstEpisodeResult)
	episodeID := firstEpisodeResult.Episode.EpisodeID
	episodeResponse := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID, "", origin, "")
	if episodeResponse.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("get Episode status = %d: %s", episodeResponse.StatusCode, limitedBody(episodeResponse))
	}
	var episodeResult struct {
		Project struct {
			ProjectID string `json:"project_id"`
		} `json:"project"`
		Episode struct {
			EpisodeID string `json:"episode_id"`
		} `json:"episode"`
		Episodes []struct {
			EpisodeID string `json:"episode_id"`
		} `json:"episodes"`
	}
	decodeHostResponse(t, episodeResponse, &episodeResult)
	if episodeResult.Project.ProjectID != projectID || episodeResult.Episode.EpisodeID != episodeID || len(episodeResult.Episodes) != 1 {
		cancel()
		t.Fatalf("unexpected Episode response: %#v", episodeResult)
	}
	updated := hostRequest(t, client, http.MethodPatch, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID, `{"title":"第一集：雨夜","target_duration_ms":90000,"expected_revision":1}`, origin, bootstrapBody.CSRFToken)
	if updated.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("update Episode status = %d: %s", updated.StatusCode, limitedBody(updated))
	}
	updated.Body.Close()

	withoutCSRF := hostRequest(t, client, http.MethodPut, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/script", `{"markdown":"blocked","expected_revision":1}`, origin, "")
	if withoutCSRF.StatusCode != http.StatusForbidden {
		cancel()
		t.Fatalf("missing CSRF status = %d", withoutCSRF.StatusCode)
	}
	withoutCSRF.Body.Close()

	saved := hostRequest(t, client, http.MethodPut, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/script", `{"markdown":"# 第一幕\n雨夜。","expected_revision":1}`, origin, bootstrapBody.CSRFToken)
	if saved.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("save script status = %d: %s", saved.StatusCode, limitedBody(saved))
	}
	saved.Body.Close()
	emptyStoryboard := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/storyboard", "", origin, "")
	if emptyStoryboard.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("get empty Storyboard status = %d: %s", emptyStoryboard.StatusCode, limitedBody(emptyStoryboard))
	}
	emptyStoryboard.Body.Close()
	createdScene := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/scenes", `{"title":"雨夜街道","description":"林晚入场","expected_storyboard_revision":1}`, origin, bootstrapBody.CSRFToken)
	if createdScene.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create Scene status = %d: %s", createdScene.StatusCode, limitedBody(createdScene))
	}
	var sceneResult struct {
		Scene struct {
			SceneID string `json:"scene_id"`
		} `json:"scene"`
	}
	decodeHostResponse(t, createdScene, &sceneResult)
	createdClip := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips",
		`{"scene_id":"`+sceneResult.Scene.SceneID+`","title":"撑伞入场","planned_duration_seconds":5,"generation_mode":"first_image","expected_storyboard_revision":2}`, origin, bootstrapBody.CSRFToken)
	if createdClip.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create Clip status = %d: %s", createdClip.StatusCode, limitedBody(createdClip))
	}
	var clipResult struct {
		Clip struct {
			ClipID string `json:"clip_id"`
		} `json:"clip"`
	}
	decodeHostResponse(t, createdClip, &clipResult)
	validation := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/storyboard/validation", "", origin, "")
	validationBody := limitedBody(validation)
	if validation.StatusCode != http.StatusOK || !strings.Contains(validationBody, "first_frame_missing") {
		cancel()
		t.Fatalf("Storyboard validation status = %d: %s", validation.StatusCode, validationBody)
	}
	clipID, err := project.ParseClipID(clipResult.Clip.ClipID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	firstImageSlotID, _ := project.FixedMediaSlotID(clipID.String(), project.MediaSlotClipFirstImage)
	readinessBody := `{"episode_id":"` + episodeID + `","target":{"media_slot_id":"` + firstImageSlotID.String() + `","expected_slot_revision":1,"prompt":"雨夜撑伞入场","generator_id":"image.shotforward","parameters":{}}}`
	readiness := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/generation/readiness", readinessBody, origin, bootstrapBody.CSRFToken)
	if readiness.StatusCode != http.StatusOK || !strings.Contains(limitedBody(readiness), `"ready":true`) {
		cancel()
		t.Fatalf("Generation readiness status = %d", readiness.StatusCode)
	}
	submitBody := `{"episode_id":"` + episodeID + `","operation_key":"host-generate-1","targets":[{"media_slot_id":"` + firstImageSlotID.String() + `","expected_slot_revision":1,"prompt":"雨夜撑伞入场","generator_id":"image.shotforward","parameters":{}}]}`
	submitted := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/generation/jobs", submitBody, origin, bootstrapBody.CSRFToken)
	if submitted.StatusCode != http.StatusAccepted {
		cancel()
		t.Fatalf("submit Generation status = %d: %s", submitted.StatusCode, limitedBody(submitted))
	}
	var generationBatch struct {
		Jobs []struct {
			GenerationJobID string `json:"generation_job_id"`
		} `json:"jobs"`
	}
	decodeHostResponse(t, submitted, &generationBatch)
	if len(generationBatch.Jobs) != 1 {
		cancel()
		t.Fatalf("Generation jobs = %#v", generationBatch.Jobs)
	}
	jobURL := origin + "/api/v1/projects/" + projectID + "/generation/jobs/" + generationBatch.Jobs[0].GenerationJobID
	deadline := time.Now().Add(5 * time.Second)
	for {
		jobResponse := hostRequest(t, client, http.MethodGet, jobURL, "", origin, "")
		body := limitedBody(jobResponse)
		if jobResponse.StatusCode != http.StatusOK {
			cancel()
			t.Fatalf("get GenerationJob status = %d: %s", jobResponse.StatusCode, body)
		}
		if strings.Contains(body, `"state":"succeeded"`) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("GenerationJob did not succeed: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	tasks := hostRequest(t, client, http.MethodGet, origin+"/api/v1/tasks/snapshot", "", origin, "")
	tasksBody := limitedBody(tasks)
	if tasks.StatusCode != http.StatusOK || !strings.Contains(tasksBody, generationBatch.Jobs[0].GenerationJobID) || strings.Contains(tasksBody, "雨夜撑伞入场") {
		cancel()
		t.Fatalf("Task snapshot status = %d: %s", tasks.StatusCode, tasksBody)
	}
	videoBytes := []byte("host-video")
	videoDigest := sha256.Sum256(videoBytes)
	videoPath := filepath.Join(projectRoot, "media", "videos", "host-video.mp4")
	if err := os.WriteFile(videoPath, videoBytes, 0o600); err != nil {
		cancel()
		t.Fatal(err)
	}
	projectDatabase, err := projectstore.Open(context.Background(), filepath.Join(projectRoot, "project.db"), sharedsqlite.OpenExisting)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	parsedProjectID, _ := project.ParseProjectID(projectID)
	videoSlotID, _ := project.FixedMediaSlotID(clipID.String(), project.MediaSlotClipVideo)
	videoArtifactID := project.ArtifactID("art_ZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	videoVersionID := project.MediaVersionID("mvr_ZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	videoAt := time.Now().UTC().Truncate(time.Millisecond)
	videoArtifact := project.Artifact{ID: videoArtifactID, ProjectID: parsedProjectID, Kind: project.MediaKindVideo, RelativePath: "media/videos/host-video.mp4", SHA256: hex.EncodeToString(videoDigest[:]), ByteSize: int64(len(videoBytes)), MIMEType: "video/mp4", OriginalFilename: "host-video.mp4", Width: 720, Height: 1280, DurationMilliseconds: 1000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: videoAt}
	videoVersion := project.MediaVersion{ID: videoVersionID, SlotID: videoSlotID, ArtifactID: videoArtifactID, SequenceNumber: 1, TrimOutMilliseconds: 1000, Revision: 1, CreatedAt: videoAt, UpdatedAt: videoAt}
	if err := projectDatabase.AddMediaVersion(context.Background(), videoArtifact, videoVersion, 1, true, videoAt); err != nil {
		_ = projectDatabase.Close()
		cancel()
		t.Fatal(err)
	}
	if err := projectDatabase.Close(); err != nil {
		cancel()
		t.Fatal(err)
	}
	preview := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/preview", "", origin, "")
	previewBody := limitedBody(preview)
	if preview.StatusCode != http.StatusOK || !strings.Contains(previewBody, `"included_clip_count":1`) || !strings.Contains(previewBody, `"export_duration_ms":1000`) {
		cancel()
		t.Fatalf("Export preview = %d: %s", preview.StatusCode, previewBody)
	}
	mediaRequest, _ := http.NewRequest(http.MethodGet, origin+"/api/v1/projects/"+projectID+"/artifacts/"+videoArtifactID.String()+"/content", nil)
	mediaRequest.Header.Set("Origin", origin)
	mediaRequest.Header.Set("Range", "bytes=1-4")
	mediaResponse, err := client.Do(mediaRequest)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	mediaBody := limitedBody(mediaResponse)
	if mediaResponse.StatusCode != http.StatusPartialContent || mediaBody != "ost-" {
		cancel()
		t.Fatalf("preview media range = %d %q", mediaResponse.StatusCode, mediaBody)
	}
	exportResponse := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/exports", `{"operation_key":"host-export-1","expected_storyboard_revision":3}`, origin, bootstrapBody.CSRFToken)
	if exportResponse.StatusCode != http.StatusAccepted {
		cancel()
		t.Fatalf("submit ExportJob = %d: %s", exportResponse.StatusCode, limitedBody(exportResponse))
	}
	var exportResult struct {
		Job struct {
			ExportJobID string `json:"export_job_id"`
		} `json:"job"`
	}
	decodeHostResponse(t, exportResponse, &exportResult)
	exportURL := origin + "/api/v1/projects/" + projectID + "/exports/" + exportResult.Job.ExportJobID
	deadline = time.Now().Add(5 * time.Second)
	for {
		statusResponse := hostRequest(t, client, http.MethodGet, exportURL, "", origin, "")
		statusBody := limitedBody(statusResponse)
		if statusResponse.StatusCode != http.StatusOK {
			cancel()
			t.Fatalf("get ExportJob = %d: %s", statusResponse.StatusCode, statusBody)
		}
		if strings.Contains(statusBody, `"state":"succeeded"`) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("ExportJob did not succeed: %s", statusBody)
		}
		time.Sleep(20 * time.Millisecond)
	}
	exported := hostRequest(t, client, http.MethodGet, exportURL+"/content", "", origin, "")
	if exported.StatusCode != http.StatusOK || limitedBody(exported) != "host-export" {
		cancel()
		t.Fatalf("Export content status = %d", exported.StatusCode)
	}
	updatedClip := hostRequest(t, client, http.MethodPatch, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips/"+clipResult.Clip.ClipID,
		`{"title":"撑伞入场","planned_duration_seconds":5,"generation_mode":"first_last_image","expected_revision":1}`, origin, bootstrapBody.CSRFToken)
	if updatedClip.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("update Clip status = %d: %s", updatedClip.StatusCode, limitedBody(updatedClip))
	}
	updatedClip.Body.Close()
	secondEpisode := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes", `{"target_duration_ms":60000}`, origin, bootstrapBody.CSRFToken)
	if secondEpisode.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create second Episode status = %d: %s", secondEpisode.StatusCode, limitedBody(secondEpisode))
	}
	var secondEpisodeResult struct {
		Episode struct {
			EpisodeID string `json:"episode_id"`
		} `json:"episode"`
	}
	decodeHostResponse(t, secondEpisode, &secondEpisodeResult)
	wrongEpisodeMutation := hostRequest(t, client, http.MethodPatch, origin+"/api/v1/projects/"+projectID+"/episodes/"+secondEpisodeResult.Episode.EpisodeID+"/clips/"+clipResult.Clip.ClipID,
		`{"title":"不应写入","planned_duration_seconds":5,"generation_mode":"first_image","expected_revision":2}`, origin, bootstrapBody.CSRFToken)
	if wrongEpisodeMutation.StatusCode != http.StatusNotFound {
		cancel()
		t.Fatalf("cross-Episode Clip mutation status = %d", wrongEpisodeMutation.StatusCode)
	}
	wrongEpisodeMutation.Body.Close()
	unchangedClip := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips/"+clipResult.Clip.ClipID, "", origin, "")
	unchangedBody := limitedBody(unchangedClip)
	if unchangedClip.StatusCode != http.StatusOK || strings.Contains(unchangedBody, "不应写入") || !strings.Contains(unchangedBody, `"revision":2`) {
		cancel()
		t.Fatalf("Clip changed through cross-Episode route: %s", unchangedBody)
	}
	deletedClip := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips/"+clipResult.Clip.ClipID+":delete",
		`{"expected_revision":2,"expected_storyboard_revision":3}`, origin, bootstrapBody.CSRFToken)
	if deletedClip.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("delete Clip status = %d: %s", deletedClip.StatusCode, limitedBody(deletedClip))
	}
	deletedClip.Body.Close()
	restoredClip := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips/"+clipResult.Clip.ClipID+":restore",
		`{"expected_revision":3,"expected_storyboard_revision":4}`, origin, bootstrapBody.CSRFToken)
	if restoredClip.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("restore Clip status = %d: %s", restoredClip.StatusCode, limitedBody(restoredClip))
	}
	restoredClip.Body.Close()
	unconfirmedScene := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/scenes/"+sceneResult.Scene.SceneID+":delete",
		`{"expected_revision":1,"expected_storyboard_revision":5,"confirm_non_empty":false}`, origin, bootstrapBody.CSRFToken)
	if unconfirmedScene.StatusCode != http.StatusConflict || !strings.Contains(limitedBody(unconfirmedScene), "confirmation_required") {
		cancel()
		t.Fatalf("unconfirmed Scene delete status = %d", unconfirmedScene.StatusCode)
	}
	deletedScene := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/scenes/"+sceneResult.Scene.SceneID+":delete",
		`{"expected_revision":1,"expected_storyboard_revision":5,"confirm_non_empty":true}`, origin, bootstrapBody.CSRFToken)
	if deletedScene.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("delete Scene status = %d: %s", deletedScene.StatusCode, limitedBody(deletedScene))
	}
	deletedScene.Body.Close()
	restoredScene := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/scenes/"+sceneResult.Scene.SceneID+":restore",
		`{"expected_revision":2,"expected_storyboard_revision":6,"confirm_non_empty":false}`, origin, bootstrapBody.CSRFToken)
	if restoredScene.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("restore Scene status = %d: %s", restoredScene.StatusCode, limitedBody(restoredScene))
	}
	restoredScene.Body.Close()

	createdForSplit := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips",
		`{"scene_id":"`+sceneResult.Scene.SceneID+`","title":"待拆镜头","planned_duration_seconds":6,"generation_mode":"first_image","expected_storyboard_revision":7}`, origin, bootstrapBody.CSRFToken)
	if createdForSplit.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create Clip for split status = %d: %s", createdForSplit.StatusCode, limitedBody(createdForSplit))
	}
	var splitSource struct {
		Clip struct {
			ClipID   string `json:"clip_id"`
			Revision int64  `json:"revision"`
		} `json:"clip"`
	}
	decodeHostResponse(t, createdForSplit, &splitSource)
	split := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips/"+splitSource.Clip.ClipID+":split",
		`{"expected_revision":1,"expected_storyboard_revision":8,"replacements":[{"title":"拆分 A","planned_duration_seconds":6,"generation_mode":"first_image","asset_ids":[]},{"title":"拆分 B","planned_duration_seconds":6,"generation_mode":"first_image","asset_ids":[]}]}`, origin, bootstrapBody.CSRFToken)
	if split.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("split Clip status = %d: %s", split.StatusCode, limitedBody(split))
	}
	var splitResult struct {
		Created []struct {
			ClipID             string   `json:"clip_id"`
			Revision           int64    `json:"revision"`
			DerivedFromClipIDs []string `json:"derived_from_clip_ids"`
		} `json:"created_clips"`
		Storyboard struct {
			Revision int64 `json:"revision"`
		} `json:"storyboard"`
	}
	decodeHostResponse(t, split, &splitResult)
	if len(splitResult.Created) != 2 || splitResult.Storyboard.Revision != 9 || len(splitResult.Created[0].DerivedFromClipIDs) != 1 || splitResult.Created[0].DerivedFromClipIDs[0] != splitSource.Clip.ClipID {
		cancel()
		t.Fatalf("unexpected split result: %#v", splitResult)
	}
	mergeBody, _ := json.Marshal(map[string]any{
		"sources": []map[string]any{
			{"clip_id": splitResult.Created[0].ClipID, "expected_revision": splitResult.Created[0].Revision},
			{"clip_id": splitResult.Created[1].ClipID, "expected_revision": splitResult.Created[1].Revision},
		},
		"replacement":                  map[string]any{"title": "重新合并", "planned_duration_seconds": 12, "generation_mode": "first_image", "asset_ids": []string{}},
		"expected_storyboard_revision": 9,
	})
	merged := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/clips:merge", string(mergeBody), origin, bootstrapBody.CSRFToken)
	if merged.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("merge Clips status = %d: %s", merged.StatusCode, limitedBody(merged))
	}
	var mergeResult struct {
		Created []struct {
			ClipID             string   `json:"clip_id"`
			DerivedFromClipIDs []string `json:"derived_from_clip_ids"`
		} `json:"created_clips"`
		Replaced []struct {
			DeletedAt *time.Time `json:"deleted_at"`
		} `json:"replaced_clips"`
		Storyboard struct {
			Revision int64 `json:"revision"`
		} `json:"storyboard"`
	}
	decodeHostResponse(t, merged, &mergeResult)
	if len(mergeResult.Created) != 1 || len(mergeResult.Created[0].DerivedFromClipIDs) != 2 || len(mergeResult.Replaced) != 2 || mergeResult.Replaced[0].DeletedAt == nil || mergeResult.Storyboard.Revision != 10 {
		cancel()
		t.Fatalf("unexpected merge result: %#v", mergeResult)
	}

	runtimeResponse := hostRequest(t, client, http.MethodGet, origin+"/api/v1/agent/runtime", "", origin, "")
	if runtimeResponse.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("runtime status = %d: %s", runtimeResponse.StatusCode, limitedBody(runtimeResponse))
	}
	runtimeResponse.Body.Close()
	session := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/agent-sessions", `{"model_id":"fake-model","web_search_disabled":true}`, origin, bootstrapBody.CSRFToken)
	if session.StatusCode != http.StatusCreated {
		cancel()
		t.Fatalf("create Agent Session status = %d: %s", session.StatusCode, limitedBody(session))
	}
	session.Body.Close()

	closed := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+":close", `{}`, origin, bootstrapBody.CSRFToken)
	if closed.StatusCode != http.StatusNoContent {
		cancel()
		t.Fatalf("close project status = %d: %s", closed.StatusCode, limitedBody(closed))
	}
	closed.Body.Close()
	unavailable := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID, "", origin, "")
	if unavailable.StatusCode != http.StatusConflict {
		cancel()
		t.Fatalf("closed project status = %d", unavailable.StatusCode)
	}
	unavailable.Body.Close()
	reopened := hostRequest(t, client, http.MethodPost, origin+"/api/v1/projects/"+projectID+":open", `{}`, origin, bootstrapBody.CSRFToken)
	if reopened.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("reopen project status = %d: %s", reopened.StatusCode, limitedBody(reopened))
	}
	reopened.Body.Close()
	script := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects/"+projectID+"/episodes/"+episodeID+"/script", "", origin, "")
	if script.StatusCode != http.StatusOK || !strings.Contains(limitedBody(script), "第一幕") {
		cancel()
		t.Fatalf("restored script status = %d", script.StatusCode)
	}

	wrongOrigin := hostRequest(t, client, http.MethodGet, origin+"/api/v1/projects", "", "http://127.0.0.1:1", "")
	if wrongOrigin.StatusCode != http.StatusForbidden {
		cancel()
		t.Fatalf("wrong Origin status = %d", wrongOrigin.StatusCode)
	}
	wrongOrigin.Body.Close()
	wrongHostRequest, err := http.NewRequest(http.MethodGet, origin+"/api/v1/projects", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	wrongHostRequest.Host = "attacker.example"
	wrongHostRequest.Header.Set("Origin", origin)
	wrongHost, err := client.Do(wrongHostRequest)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if wrongHost.StatusCode != http.StatusForbidden {
		cancel()
		t.Fatalf("wrong Host status = %d", wrongHost.StatusCode)
	}
	wrongHost.Body.Close()

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host did not stop")
	}
	if !runtime.closed.Load() {
		t.Fatal("Agent Runtime was not closed")
	}
	if strings.Contains(logs.String(), announcement.PairingCode) {
		t.Fatal("pairing code leaked to logs")
	}
	if _, err := os.Stat(filepath.Join(directories.Data, "studio.db")); err != nil {
		t.Fatalf("Studio database is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directories.Runtime, rendezvousFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rendezvous remains after shutdown: %v", err)
	}

	// Simulate the narrow crash window after a durable queued->running claim but
	// before the external operation identity can be recorded.
	projectDatabase, err = projectstore.Open(context.Background(), filepath.Join(projectRoot, "project.db"), sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	storedProject, err := projectDatabase.Project(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsedEpisodeID, _ := project.ParseEpisodeID(episodeID)
	orphanSlotID, _ := project.FixedMediaSlotID(mergeResult.Created[0].ClipID, project.MediaSlotClipFirstImage)
	batchID, _ := project.ParseGenerationBatchID("gbt_" + strings.Repeat("Y", 26))
	jobID, _ := project.ParseGenerationJobID("gjb_" + strings.Repeat("Y", 26))
	at := storedProject.UpdatedAt.Add(time.Second)
	batch := project.GenerationBatch{ID: batchID, ProjectID: storedProject.ID, EpisodeID: parsedEpisodeID, OperationKey: "host-recovery-gap", RequestHash: strings.Repeat("e", 64), CreatedAt: at}
	snapshot := project.GenerationInputSnapshot{SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage, Prompt: "不可重复提交", GeneratorID: project.GeneratorImageShotForward, CapabilityVersion: "host-test-1", OutputWidth: storedProject.Output.Width(), OutputHeight: storedProject.Output.Height(), ParametersJSON: "{}", TargetSlotRevision: 1}
	orphan := project.GenerationJob{ID: jobID, BatchID: batchID, ProjectID: storedProject.ID, EpisodeID: parsedEpisodeID, TargetSlotID: orphanSlotID, State: project.GenerationJobQueued, Snapshot: snapshot, StopSupported: true, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := projectDatabase.CreateGenerationBatch(context.Background(), batch, []project.GenerationJob{orphan}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := projectDatabase.ClaimGenerationJob(context.Background(), jobID, 1, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := projectDatabase.Close(); err != nil {
		t.Fatal(err)
	}

	restartListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	restartReader, restartWriter := io.Pipe()
	restartRuntime := &hostTestRuntime{}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartResult := make(chan error, 1)
	go func() {
		restartResult <- RunWithOptions(restartCtx, config.Default(), slog.New(slog.NewTextHandler(&logs, nil)), Options{
			Directories: directories, Listener: restartListener, BootstrapWriter: restartWriter,
			GenerationCatalog: hostTestGenerationCatalog{}, Generators: hostTestGeneratorRegistry{generator: generator}, MediaFiles: hostTestMediaFiles{}, ExportProcessor: hostTestExportProcessor{},
			RuntimeFactory: func(config.Config, platformpaths.ApplicationDirs, agentmcp.Credential) (RuntimeResource, error) {
				return RuntimeResource{Runtime: restartRuntime, Close: restartRuntime.Close}, nil
			},
		})
	}()
	var restartAnnouncement bootstrapAnnouncement
	if err := json.NewDecoder(restartReader).Decode(&restartAnnouncement); err != nil {
		restartCancel()
		t.Fatal(err)
	}
	restartJar, _ := cookiejar.New(nil)
	restartClient := &http.Client{Jar: restartJar, Timeout: 5 * time.Second}
	restartBootstrap := hostRequest(t, restartClient, http.MethodPost, restartAnnouncement.Address+"/api/v1/bootstrap", `{"pairing_code":"`+restartAnnouncement.PairingCode+`"}`, restartAnnouncement.Address, "")
	if restartBootstrap.StatusCode != http.StatusOK {
		restartCancel()
		t.Fatalf("restart bootstrap status = %d: %s", restartBootstrap.StatusCode, limitedBody(restartBootstrap))
	}
	restartBootstrap.Body.Close()
	recoveryURL := restartAnnouncement.Address + "/api/v1/projects/" + projectID + "/generation/jobs/" + jobID.String()
	recoveryDeadline := time.Now().Add(5 * time.Second)
	for {
		response := hostRequest(t, restartClient, http.MethodGet, recoveryURL, "", restartAnnouncement.Address, "")
		body := limitedBody(response)
		if response.StatusCode == http.StatusOK && strings.Contains(body, `"state":"failed"`) && strings.Contains(body, `"failure_code":"execution_interrupted"`) {
			break
		}
		if time.Now().After(recoveryDeadline) {
			restartCancel()
			t.Fatalf("orphaned GenerationJob was not reconciled: %d %s", response.StatusCode, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if generator.startCalls.Load() != 1 {
		restartCancel()
		t.Fatalf("Generator Start calls = %d, interrupted Job was resubmitted", generator.startCalls.Load())
	}
	restartCancel()
	select {
	case err := <-restartResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restarted host did not stop")
	}
}

func TestHostStartupFailureCleansResources(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_HOST_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_HOST_E2E=1 to run loopback host integration")
	}

	root := t.TempDir()
	directories := platformpaths.ApplicationDirs{
		Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"),
		Logs: filepath.Join(root, "data", "logs"), Runtime: filepath.Join(root, "data", "runtime"),
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	runtime := &hostTestRuntime{}
	err = RunWithOptions(context.Background(), config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Directories: directories, Listener: listener, BootstrapWriter: failingWriter{},
		RuntimeFactory: func(config.Config, platformpaths.ApplicationDirs, agentmcp.Credential) (RuntimeResource, error) {
			return RuntimeResource{Runtime: runtime, Close: runtime.Close}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "write bootstrap announcement") {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if !runtime.closed.Load() {
		t.Fatal("Runtime remains open after startup failure")
	}
	if _, err := os.Stat(filepath.Join(directories.Runtime, rendezvousFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rendezvous remains after startup failure: %v", err)
	}
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("HTTP listener remains open after startup failure")
	}
}

func hostRequest(t *testing.T, client *http.Client, method, target, body, origin, csrf string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return response
}

func decodeHostResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func limitedBody(response *http.Response) string {
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))
	return string(data)
}

type hostTestProviderConfigurations struct {
	mu         sync.Mutex
	profile    providerconfig.Profile
	checkCalls atomic.Int32
}

func (s *hostTestProviderConfigurations) Profiles(context.Context) ([]providerconfig.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []providerconfig.Profile{s.profile}, nil
}
func (s *hostTestProviderConfigurations) Profile(context.Context, project.GeneratorID) (providerconfig.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile, nil
}
func (s *hostTestProviderConfigurations) Configure(_ context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = providerconfig.Profile{GeneratorID: input.GeneratorID, EndpointPreset: input.EndpointPreset, ModelID: input.ModelID,
		ResourceID: input.ResourceID, WorkspaceID: input.WorkspaceID, Enabled: input.Enabled, ConcurrencyLimit: input.ConcurrencyLimit,
		CredentialConfigured: input.APIKey != "", Health: providerconfig.HealthUnchecked, Revision: 1,
		CreatedAt: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)}
	return s.profile, nil
}
func (s *hostTestProviderConfigurations) ValidateAndConfigure(ctx context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	return s.Configure(ctx, input)
}
func (s *hostTestProviderConfigurations) Check(context.Context, project.GeneratorID) (providerconfig.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkCalls.Add(1)
	return s.profile, nil
}
func (s *hostTestProviderConfigurations) Delete(context.Context, providerconfig.DeleteInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = providerconfig.Profile{}
	return nil
}

type hostTestRuntime struct {
	closed        atomic.Bool
	snapshotCalls atomic.Int32
}

type hostTestGenerationCatalog struct{}

func (hostTestGenerationCatalog) Generator(context.Context, project.GeneratorID) (projectapp.GenerationCapability, error) {
	return projectapp.GenerationCapability{GeneratorID: project.GeneratorImageShotForward, CapabilityVersion: "host-test-1", MediaKind: project.MediaKindImage, SupportsStop: true, SupportsRecovery: true}, nil
}

type hostTestGeneratorRegistry struct{ generator generation.Generator }

func (r hostTestGeneratorRegistry) Generator(id project.GeneratorID) (generation.Generator, bool) {
	return r.generator, r.generator != nil && id == project.GeneratorImageShotForward
}

type hostTestGenerator struct{ startCalls atomic.Int32 }

func (*hostTestGenerator) ID() project.GeneratorID                               { return project.GeneratorImageShotForward }
func (*hostTestGenerator) ConcurrencyLimit() int                                 { return 1 }
func (*hostTestGenerator) SupportsRecovery(project.GenerationInputSnapshot) bool { return true }
func (g *hostTestGenerator) Start(_ context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	g.startCalls.Add(1)
	return generation.GeneratorOperation{ID: request.JobID.String()}, nil
}
func (*hostTestGenerator) Poll(context.Context, generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Outputs: []generation.GeneratorOutput{{SourcePath: "/generator/host-output.png"}}}, nil
}
func (*hostTestGenerator) Stop(context.Context, generation.GeneratorOperation) error { return nil }

type hostTestMediaFiles struct{}

func (hostTestMediaFiles) Prepare(_ context.Context, request projectapp.PrepareMediaRequest) (projectapp.PreparedMedia, error) {
	return projectapp.PreparedMedia{Token: "host-output.png", Kind: request.Kind, Extension: ".png", SHA256: strings.Repeat("c", 64), ByteSize: 100, MIMEType: "image/png", OriginalFilename: "host-output.png", Width: request.RequiredWidth, Height: request.RequiredHeight}, nil
}
func (f hostTestMediaFiles) PrepareVideoFrame(ctx context.Context, request projectapp.PrepareVideoFrameRequest) (projectapp.PreparedMedia, error) {
	return f.Prepare(ctx, projectapp.PrepareMediaRequest{SourcePath: request.SourceRelativePath, Kind: project.MediaKindImage, RequiredWidth: request.RequiredWidth, RequiredHeight: request.RequiredHeight})
}
func (hostTestMediaFiles) Install(_ context.Context, _ string, prepared projectapp.PreparedMedia, id project.ArtifactID) (projectapp.InstalledMedia, error) {
	return projectapp.InstalledMedia{RelativePath: "media/images/" + id.String() + prepared.Extension}, nil
}
func (hostTestMediaFiles) Discard(string, projectapp.PreparedMedia) error { return nil }
func (hostTestMediaFiles) Remove(string, projectapp.InstalledMedia) error { return nil }

type hostTestExportProcessor struct{}

func (hostTestExportProcessor) Export(_ context.Context, request exportjob.Request, progress exportjob.Progress) (string, error) {
	if err := progress("transcoding", "Preparing test export"); err != nil {
		return "", err
	}
	relative := "exports/" + request.JobID.String() + ".mp4"
	return relative, os.WriteFile(filepath.Join(request.ProjectRoot, filepath.FromSlash(relative)), []byte("host-export"), 0o600)
}
func (hostTestExportProcessor) RemoveOutput(root, relative string) error {
	return os.Remove(filepath.Join(root, filepath.FromSlash(relative)))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic startup output failure")
}

func (r *hostTestRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	r.snapshotCalls.Add(1)
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady, Version: "test", Executable: "/test/codex"}, nil
}
func (r *hostTestRuntime) Models(context.Context) ([]agentapp.Model, error) {
	return []agentapp.Model{{ID: "fake-model", DisplayName: "Fake Model"}}, nil
}
func (r *hostTestRuntime) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, errors.New("not implemented")
}
func (r *hostTestRuntime) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{CodexThreadID: "thread-private", CodexSessionID: "session-private", Title: "Host Integration", ModelID: "fake-model"}, nil
}
func (r *hostTestRuntime) ResumeThread(context.Context, string, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New("not implemented")
}
func (r *hostTestRuntime) ReadThread(context.Context, string) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New("not implemented")
}
func (r *hostTestRuntime) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, nil
}
func (r *hostTestRuntime) StartTurn(context.Context, agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	return agentapp.RuntimeTurn{}, errors.New("not implemented")
}
func (r *hostTestRuntime) SteerTurn(context.Context, string, string, string) error {
	return errors.New("not implemented")
}
func (r *hostTestRuntime) InterruptTurn(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (r *hostTestRuntime) Subscribe(context.Context, string) (<-chan agentapp.RuntimeEvent, error) {
	return nil, errors.New("not implemented")
}
func (r *hostTestRuntime) Close(context.Context) error { r.closed.Store(true); return nil }
