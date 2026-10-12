package projecthttp

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type assetProjects struct {
	ProjectService
	assets                      []project.EpisodeAsset
	createInput                 projectapp.CreateAssetInput
	importInput                 projectapp.ImportMediaInput
	assignInput                 projectapp.AssignArtifactInput
	frameInput                  projectapp.ExtractVideoFrameInput
	trimInput                   projectapp.UpdateVideoTrimInput
	importBytes                 []byte
	media                       project.MediaSlotDetails
	referenceArtifacts          []project.Artifact
	referenceUploadInput        projectapp.UploadClipReferenceMediaInput
	referenceUploadBytes        []byte
	referenceDeleteInput        projectapp.DeleteClipReferenceMediaInput
	episodeReferenceMedia       []project.EpisodeReferenceMedia
	episodeReferenceUpdateInput projectapp.UpdateEpisodeReferenceMediaInput
	episodeReferenceDeleteInput projectapp.DeleteEpisodeReferenceMediaInput
	episodeReferenceAttachInput projectapp.AttachClipReferenceMediaInput
}

type trashProjects struct {
	ProjectService
	result projectapp.ProjectTrashResult
}

func (s *trashProjects) ListProjectTrash(context.Context, project.ProjectID) (projectapp.ProjectTrashResult, error) {
	return s.result, nil
}

func (s *assetProjects) ListEpisodeAssets(context.Context, projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error) {
	return s.assets, nil
}

func (s *assetProjects) CreateAsset(_ context.Context, input projectapp.CreateAssetInput) (projectapp.AssetResult, error) {
	s.createInput = input
	return projectapp.AssetResult{Asset: s.assets[0]}, nil
}

func (s *assetProjects) GetMediaSlot(context.Context, projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error) {
	return s.media, nil
}

func (s *assetProjects) ImportMedia(_ context.Context, input projectapp.ImportMediaInput) (project.MediaSlotDetails, error) {
	s.importInput = input
	s.importBytes, _ = os.ReadFile(input.SourcePath)
	return s.media, nil
}

func (s *assetProjects) AssignArtifact(_ context.Context, input projectapp.AssignArtifactInput) (project.MediaSlotDetails, error) {
	s.assignInput = input
	return s.media, nil
}

func (s *assetProjects) ExtractVideoFrame(_ context.Context, input projectapp.ExtractVideoFrameInput) (project.MediaSlotDetails, error) {
	s.frameInput = input
	return s.media, nil
}

func (s *assetProjects) UpdateVideoTrim(_ context.Context, input projectapp.UpdateVideoTrimInput) (project.MediaSlotDetails, error) {
	s.trimInput = input
	return s.media, nil
}

func (s *assetProjects) ListClipReferenceMedia(context.Context, projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error) {
	return s.referenceArtifacts, nil
}

func (s *assetProjects) UploadClipReferenceMedia(_ context.Context, input projectapp.UploadClipReferenceMediaInput) (project.Artifact, error) {
	s.referenceUploadInput = input
	s.referenceUploadBytes, _ = os.ReadFile(input.SourcePath)
	return s.referenceArtifacts[0], nil
}

func (s *assetProjects) DeleteClipReferenceMedia(_ context.Context, input projectapp.DeleteClipReferenceMediaInput) error {
	s.referenceDeleteInput = input
	return nil
}

func (s *assetProjects) ListEpisodeReferenceMedia(context.Context, projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error) {
	return s.episodeReferenceMedia, nil
}

func (s *assetProjects) UploadEpisodeReferenceMedia(context.Context, projectapp.UploadEpisodeReferenceMediaInput) (project.EpisodeReferenceMedia, error) {
	return s.episodeReferenceMedia[0], nil
}

func (s *assetProjects) UpdateEpisodeReferenceMedia(_ context.Context, input projectapp.UpdateEpisodeReferenceMediaInput) (project.EpisodeReferenceMedia, error) {
	s.episodeReferenceUpdateInput = input
	value := s.episodeReferenceMedia[0]
	value.Name = input.Name
	value.Description = input.Description
	value.Revision++
	return value, nil
}

func (s *assetProjects) DeleteEpisodeReferenceMedia(_ context.Context, input projectapp.DeleteEpisodeReferenceMediaInput) error {
	s.episodeReferenceDeleteInput = input
	return nil
}

func (s *assetProjects) AttachClipReferenceMedia(_ context.Context, input projectapp.AttachClipReferenceMediaInput) error {
	s.episodeReferenceAttachInput = input
	return nil
}

func TestEpisodeAssetRoutesExposeStableAssetAndSlotContract(t *testing.T) {
	service, projectID, episodeID, assetID, slotID := assetHTTPFixture()
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/episodes/"+episodeID.String()+"/assets", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"asset_id":"`+assetID.String()+`"`) || !strings.Contains(list.Body.String(), `"primary_image_slot_id":"`+slotID.String()+`"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	createBody := `{"kind":"character","name":"阿澄","prompt":"雨夜人物"}`
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/episodes/"+episodeID.String()+"/assets", strings.NewReader(createBody))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated || service.createInput.Name != "阿澄" || service.createInput.Kind != project.AssetKindCharacter {
		t.Fatalf("create = %d %s input=%#v", createResponse.Code, createResponse.Body.String(), service.createInput)
	}

	media := httptest.NewRecorder()
	handler.ServeHTTP(media, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/media-slots/"+slotID.String(), nil))
	if media.Code != http.StatusOK || !strings.Contains(media.Body.String(), `"current_media_version_id":""`) || !strings.Contains(media.Body.String(), `"versions":[]`) {
		t.Fatalf("media = %d %s", media.Code, media.Body.String())
	}
}

func TestImportMediaUsesRevisionAndNeverAcceptsUnknownFields(t *testing.T) {
	service, projectID, _, _, slotID := assetHTTPFixture()
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	path := "/api/v1/projects/" + projectID.String() + "/media-slots/" + slotID.String() + "/imports"
	body := `{"source_path":"/tmp/reference.png","expected_slot_revision":1,"make_current":true}`
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.importInput.SourcePath != "/tmp/reference.png" || service.importInput.ExpectedSlotRevision != 1 || !service.importInput.MakeCurrent {
		t.Fatalf("import = %d %s input=%#v", response.Code, response.Body.String(), service.importInput)
	}

	unknown := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.TrimSuffix(body, "}")+`,"relative_path":"escape"}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}
}

func TestAssignArtifactCreatesSlotVersionWithoutUploadingAgain(t *testing.T) {
	service, projectID, _, _, slotID := assetHTTPFixture()
	artifactID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	path := "/api/v1/projects/" + projectID.String() + "/media-slots/" + slotID.String() + "/artifacts"
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"artifact_id":"`+artifactID.String()+`","expected_slot_revision":3,"make_current":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || service.assignInput.ProjectID != projectID || service.assignInput.MediaSlotID != slotID || service.assignInput.ArtifactID != artifactID || service.assignInput.ExpectedSlotRevision != 3 || !service.assignInput.MakeCurrent {
		t.Fatalf("assign = %d %s input=%#v", response.Code, response.Body.String(), service.assignInput)
	}
}

func TestUploadMediaStagesDeviceFileWithoutMakingReferenceCurrent(t *testing.T) {
	service, projectID, _, _, slotID := assetHTTPFixture()
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("expected_slot_revision", "1")
	_ = writer.WriteField("make_current", "false")
	part, err := writer.CreateFormFile("file", "reference.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(part, "tiny image fixture"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + projectID.String() + "/media-slots/" + slotID.String() + "/uploads"
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || service.importInput.ExpectedSlotRevision != 1 || service.importInput.MakeCurrent || string(service.importBytes) != "tiny image fixture" {
		t.Fatalf("upload = %d %s input=%#v bytes=%q", response.Code, response.Body.String(), service.importInput, service.importBytes)
	}
	if _, err := os.Stat(service.importInput.SourcePath); !os.IsNotExist(err) {
		t.Fatalf("staged upload was not removed: %v", err)
	}
}

func TestUpdateVideoTrimUsesVersionRevisionAndRejectsUnknownFields(t *testing.T) {
	service, projectID, _, _, slotID := assetHTTPFixture()
	versionID := project.MediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	path := "/api/v1/projects/" + projectID.String() + "/media-slots/" + slotID.String() + "/versions/" + versionID.String()

	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"trim_in_ms":1200,"trim_out_ms":5400,"expected_version_revision":3}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.trimInput.MediaSlotID != slotID || service.trimInput.MediaVersionID != versionID || service.trimInput.TrimInMilliseconds != 1200 || service.trimInput.TrimOutMilliseconds != 5400 || service.trimInput.ExpectedVersionRevision != 3 {
		t.Fatalf("trim = %d %s input=%#v", response.Code, response.Body.String(), service.trimInput)
	}

	unknown := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"trim_in_ms":0,"trim_out_ms":1000,"expected_version_revision":3,"extra":true}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}
}

func TestExtractVideoFrameRouteUsesSourceSlotAndTargetRevision(t *testing.T) {
	service, projectID, _, _, targetSlotID := assetHTTPFixture()
	sourceSlotID := project.MediaSlotID("msl_BBCDEFGHIJKLMNOPQRSTUVWXYZ")
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	path := "/api/v1/projects/" + projectID.String() + "/media-slots/" + targetSlotID.String() + "/frame-extractions"
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"source_video_media_slot_id":"`+sourceSlotID.String()+`","expected_target_revision":4}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || service.frameInput.ProjectID != projectID || service.frameInput.TargetMediaSlotID != targetSlotID || service.frameInput.SourceVideoMediaSlotID != sourceSlotID || service.frameInput.ExpectedTargetRevision != 4 {
		t.Fatalf("frame extraction = %d %s input=%#v", response.Code, response.Body.String(), service.frameInput)
	}
}

func TestMediaUploadErrorsHaveActionableHTTPResponses(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{projectapp.ErrMediaImportUnsupported, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{projectapp.ErrMediaImportInvalid, http.StatusUnprocessableEntity, "invalid_media"},
		{projectapp.ErrMediaAspectRatioMismatch, http.StatusUnprocessableEntity, "aspect_ratio_mismatch"},
		{projectapp.ErrMediaOutputSpecMismatch, http.StatusUnprocessableEntity, "output_spec_mismatch"},
		{projectapp.ErrMediaImportUnreadable, http.StatusUnprocessableEntity, "unreadable_media"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeMappedError(response, test.err)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	if maxMediaUploadBytes != 100<<20 {
		t.Fatalf("maxMediaUploadBytes = %d", maxMediaUploadBytes)
	}
}

func TestClipReferenceMediaRoutesKeepUploadsOutsideOutputSlots(t *testing.T) {
	service, projectID, episodeID, _, _ := assetHTTPFixture()
	clipID := project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifactID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	service.referenceArtifacts = []project.Artifact{{
		ID: artifactID, ProjectID: projectID, Kind: project.MediaKindAudio,
		RelativePath: "media/audio/reference.wav", SHA256: strings.Repeat("a", 64), ByteSize: 12,
		MIMEType: "audio/wav", OriginalFilename: "对白参考.wav", DurationMilliseconds: 1000,
		AudioSampleRate: 48000, AudioChannels: 1, CreatedAt: time.Now().UTC(),
	}}
	handler, _ := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	base := "/api/v1/projects/" + projectID.String() + "/episodes/" + episodeID.String() + "/clips/" + clipID.String() + "/reference-media"

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, base, nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"original_filename":"对白参考.wav"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("kind", "audio")
	part, err := writer.CreateFormFile("file", "对白参考.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "wave fixture")
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, base, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	upload := httptest.NewRecorder()
	handler.ServeHTTP(upload, request)
	if upload.Code != http.StatusCreated || service.referenceUploadInput.Kind != project.MediaKindAudio || service.referenceUploadInput.OriginalFilename != "对白参考.wav" || string(service.referenceUploadBytes) != "wave fixture" {
		t.Fatalf("upload = %d %s input=%#v bytes=%q", upload.Code, upload.Body.String(), service.referenceUploadInput, service.referenceUploadBytes)
	}
	if _, err := os.Stat(service.referenceUploadInput.SourcePath); !os.IsNotExist(err) {
		t.Fatalf("staged reference upload was not removed: %v", err)
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, base+"/"+artifactID.String(), nil))
	if remove.Code != http.StatusNoContent || service.referenceDeleteInput.ArtifactID != artifactID {
		t.Fatalf("delete = %d %s input=%#v", remove.Code, remove.Body.String(), service.referenceDeleteInput)
	}
}

func TestEpisodeReferenceMediaLibraryRoutesManageAndReuseUploads(t *testing.T) {
	service, projectID, episodeID, _, _ := assetHTTPFixture()
	clipID := project.ClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifactID := project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	at := time.Unix(1_800_000_000, 0).UTC()
	service.episodeReferenceMedia = []project.EpisodeReferenceMedia{{
		EpisodeID: episodeID,
		Artifact: project.Artifact{
			ID: artifactID, ProjectID: projectID, Kind: project.MediaKindVideo,
			OriginalFilename: "动作参考.mp4", DurationMilliseconds: 8_000,
		},
		Name: "动作参考", Description: "人物回头", UsageCount: 2, Revision: 3, CreatedAt: at, UpdatedAt: at,
	}}
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + projectID.String() + "/episodes/" + episodeID.String() + "/reference-media"

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, base, nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"name":"动作参考"`) || !strings.Contains(list.Body.String(), `"usage_count":2`) {
		t.Fatalf("list Episode reference media = %d %s", list.Code, list.Body.String())
	}

	update := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPatch, base+"/"+artifactID.String(), strings.NewReader(`{"name":"回头动作","description":"两个视频单元复用","expected_revision":3}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(update, updateRequest)
	if update.Code != http.StatusOK || service.episodeReferenceUpdateInput.Name != "回头动作" || service.episodeReferenceUpdateInput.ExpectedRevision != 3 {
		t.Fatalf("update Episode reference media = %d %s input=%#v", update.Code, update.Body.String(), service.episodeReferenceUpdateInput)
	}

	attach := httptest.NewRecorder()
	attachPath := "/api/v1/projects/" + projectID.String() + "/episodes/" + episodeID.String() + "/clips/" + clipID.String() + "/reference-media/" + artifactID.String()
	handler.ServeHTTP(attach, httptest.NewRequest(http.MethodPut, attachPath, nil))
	if attach.Code != http.StatusNoContent || service.episodeReferenceAttachInput.ClipID != clipID || service.episodeReferenceAttachInput.ArtifactID != artifactID {
		t.Fatalf("attach Episode reference media = %d %s input=%#v", attach.Code, attach.Body.String(), service.episodeReferenceAttachInput)
	}

	remove := httptest.NewRecorder()
	removeRequest := httptest.NewRequest(http.MethodDelete, base+"/"+artifactID.String(), strings.NewReader(`{"expected_revision":3}`))
	removeRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(remove, removeRequest)
	if remove.Code != http.StatusNoContent || service.episodeReferenceDeleteInput.ExpectedRevision != 3 {
		t.Fatalf("delete Episode reference media = %d %s input=%#v", remove.Code, remove.Body.String(), service.episodeReferenceDeleteInput)
	}
}

func TestProjectTrashReturnsScenesWithNestedClipsAndIndependentClips(t *testing.T) {
	projectID := project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID := project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	sceneID := project.SceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	nestedID := project.ClipID("clp_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	plainID := project.ClipID("clp_345678ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	service := &trashProjects{result: projectapp.ProjectTrashResult{
		Project: project.Project{ID: projectID},
		Scenes: []project.StoryboardScene{{
			Scene: project.Scene{ID: sceneID, ProjectID: projectID, EpisodeID: episodeID, Title: "Deleted scene"},
			Clips: []project.ClipDetails{{Clip: project.Clip{ID: nestedID, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Title: "Nested clip"}}},
		}},
		Clips:               []project.ClipDetails{{Clip: project.Clip{ID: plainID, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Title: "Independent clip"}}},
		StoryboardRevisions: map[project.EpisodeID]int64{episodeID: 7},
	}}
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/trash", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"scene_id":"`+sceneID.String()+`"`) || !strings.Contains(body, `"clip_id":"`+nestedID.String()+`"`) || !strings.Contains(body, `"clip_id":"`+plainID.String()+`"`) || !strings.Contains(body, `"`+episodeID.String()+`":7`) {
		t.Fatalf("trash = %d %s", response.Code, body)
	}
}

func TestMapArtifactExposesRemoteExpiryWithoutLeakingSignedURL(t *testing.T) {
	expiresAt := time.Unix(1_900_000_000, 0).UTC()
	mapped := mapArtifact(project.Artifact{
		RemoteReferenceURL:       "https://example.com/video.mp4?X-Tos-Signature=secret",
		RemoteReferenceExpiresAt: &expiresAt,
	})
	if _, ok := mapped["remote_reference_url"]; ok {
		t.Fatal("mapArtifact leaked the remote reference URL")
	}
	if got, ok := mapped["remote_reference_expires_at"].(time.Time); !ok || !got.Equal(expiresAt) {
		t.Fatalf("remote_reference_expires_at = %#v", mapped["remote_reference_expires_at"])
	}
}

func assetHTTPFixture() (*assetProjects, project.ProjectID, project.EpisodeID, project.AssetID, project.MediaSlotID) {
	projectID := project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID := project.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	assetID := project.AssetID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	identityID := project.AssetIdentityID("asi_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	slotID, _ := project.FixedMediaSlotID(assetID.String(), project.MediaSlotAssetPrimaryImage)
	at := time.Unix(1_800_000_000, 0).UTC()
	asset := project.EpisodeAsset{
		Identity: project.AssetIdentity{ID: identityID, ProjectID: projectID, Kind: project.AssetKindCharacter, Name: "阿澄", Revision: 1, CreatedAt: at, UpdatedAt: at},
		Asset:    project.Asset{ID: assetID, ProjectID: projectID, EpisodeID: episodeID, IdentityID: identityID, Prompt: "雨夜人物", Revision: 1, CreatedAt: at, UpdatedAt: at},
	}
	media := project.MediaSlotDetails{Slot: project.MediaSlot{ID: slotID, ProjectID: projectID, EpisodeID: episodeID, OwnerKind: project.MediaOwnerAsset, OwnerID: assetID.String(), Kind: project.MediaSlotAssetPrimaryImage, Revision: 1, CreatedAt: at, UpdatedAt: at}, Versions: []project.MediaVersion{}, Artifacts: map[project.ArtifactID]project.Artifact{}}
	return &assetProjects{assets: []project.EpisodeAsset{asset}, media: media}, projectID, episodeID, assetID, slotID
}
