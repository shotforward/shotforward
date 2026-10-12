package projecthttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type exportProjects struct {
	ProjectService
	preview     project.ExportSnapshot
	submitted   projectapp.SubmitExportJobInput
	job         project.ExportJob
	content     []byte
	contentType string
}

func (s *exportProjects) GetExportPreview(context.Context, projectapp.GetExportPreviewInput) (project.ExportSnapshot, error) {
	return s.preview, nil
}
func (s *exportProjects) SubmitExportJob(_ context.Context, input projectapp.SubmitExportJobInput) (project.ExportJob, error) {
	s.submitted = input
	return s.job, nil
}
func (s *exportProjects) GetArtifactContent(context.Context, projectapp.GetArtifactContentInput) (projectapp.ArtifactContentSource, error) {
	return projectapp.ArtifactContentSource{Content: readSeekCloser{Reader: bytes.NewReader(s.content)}, MIMEType: s.contentType, OriginalFilename: "clip.mp4", ByteSize: int64(len(s.content)), ModifiedAt: time.Unix(1_800_000_000, 0).UTC()}, nil
}
func (s *exportProjects) GetExportContent(context.Context, projectapp.GetExportJobInput) (projectapp.ArtifactContentSource, error) {
	return projectapp.ArtifactContentSource{Content: readSeekCloser{Reader: bytes.NewReader(s.content)}, MIMEType: s.contentType, OriginalFilename: "export.mp4", ByteSize: int64(len(s.content)), ModifiedAt: time.Unix(1_800_000_000, 0).UTC()}, nil
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

func TestExportPreviewAndSubmitExposeFrozenContract(t *testing.T) {
	service, projectID, episodeID, artifactID := exportHTTPFixture()
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	preview := httptest.NewRecorder()
	handler.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/episodes/"+episodeID.String()+"/preview", nil))
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"storyboard_revision":4`) || !strings.Contains(preview.Body.String(), artifactID.String()+`/content`) {
		t.Fatalf("preview = %d %s", preview.Code, preview.Body.String())
	}
	body := `{"operation_key":"ui-export-1","expected_storyboard_revision":4}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/episodes/"+episodeID.String()+"/exports", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.submitted.OperationKey != "ui-export-1" || service.submitted.ExpectedStoryboardRevision != 4 || service.submitted.EpisodeID != episodeID {
		t.Fatalf("submit = %d %s input=%#v", response.Code, response.Body.String(), service.submitted)
	}
}

func TestArtifactContentSupportsByteRanges(t *testing.T) {
	service, projectID, _, artifactID := exportHTTPFixture()
	service.content = []byte("0123456789")
	service.contentType = "video/mp4"
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/artifacts/"+artifactID.String()+"/content", nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	data, _ := io.ReadAll(response.Result().Body)
	if response.Code != http.StatusPartialContent || string(data) != "2345" || response.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range = %d %q headers=%v", response.Code, data, response.Header())
	}
}

func TestExportContentIsServedAsADownload(t *testing.T) {
	service, projectID, _, _ := exportHTTPFixture()
	service.content = []byte("exported-video")
	service.contentType = "video/mp4"
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/exports/"+service.job.ID.String()+"/content", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Disposition") != `attachment; filename=export.mp4` {
		t.Fatalf("download = %d disposition=%q", response.Code, response.Header().Get("Content-Disposition"))
	}
}

func exportHTTPFixture() (*exportProjects, project.ProjectID, project.EpisodeID, project.ArtifactID) {
	projectID := project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID := project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	sceneID := project.SceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	clipID := project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.FixedMediaSlotID(clipID.String(), project.MediaSlotClipVideo)
	versionID := project.MediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifactID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	snapshot := project.ExportSnapshot{SchemaVersion: project.ExportSnapshotVersion, StoryboardRevision: 4, OutputWidth: 720, OutputHeight: 1280, Clips: []project.ExportClipSnapshot{{SceneID: sceneID, ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 2, MediaVersionID: versionID, MediaVersionRevision: 1, ArtifactID: artifactID, TrimOutMilliseconds: 1000}}}
	at := time.Unix(1_800_000_000, 0).UTC()
	job := project.ExportJob{ID: project.ExportJobID("exj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ProjectID: projectID, EpisodeID: episodeID, OperationKey: "ui-export-1", State: project.ExportJobQueued, Snapshot: snapshot, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at}
	return &exportProjects{preview: snapshot, job: job}, projectID, episodeID, artifactID
}
