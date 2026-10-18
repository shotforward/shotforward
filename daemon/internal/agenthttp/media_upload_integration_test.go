package agenthttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/mediafiles"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	"github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

// This is an in-process HTTP/storage integration test, not a live Agent test.
// Every image and database is synthetic and belongs to t.TempDir. The real
// Processor probes/installs media; no Runtime process or listener is started.
func TestAgentUploadRealProcessorPreservesImageContentAndBindsInput(t *testing.T) {
	for _, test := range []struct {
		name, format, filename, claimedMIME, wantMIME, extension string
	}{
		{"jpeg", "jpeg", "portrait.jpg", "image/jpeg", "image/jpeg", ".jpg"},
		{"jpeg-named-png", "jpeg", "模特全身图.png", "image/png", "image/jpeg", ".jpg"},
		{"png-named-jpeg", "png", "clipboard.jpg", "image/jpeg", "image/png", ".png"},
		{"no-extension", "jpeg", "clipboard-image", "application/octet-stream", "image/jpeg", ".jpg"},
		{"webp-named-jpeg", "webp", "reference.jpg", "image/jpeg", "image/webp", ".webp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealAgentUploadFixture(t)
			content := realAgentUploadImage(t, test.format)
			const operationID = "real-upload-and-input"
			response := fixture.upload(t, operationID, test.filename, test.claimedMIME, content)
			if response.Code != http.StatusCreated {
				t.Fatalf("real image upload status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Media conversationMediaDTO `json:"media"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			media := body.Media
			width, height := 64, 36
			if test.format == "webp" {
				width, height = 1, 1
			}
			if media.MIMEType != test.wantMIME || media.OriginalFilename != test.filename || media.Name != test.filename || media.Origin != "uploaded" || media.Width != width || media.Height != height || media.ByteSize != int64(len(content)) {
				t.Fatalf("upload metadata does not describe the original image: %+v", media)
			}
			artifactID, err := project.ParseArtifactID(media.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := fixture.store.Artifact(context.Background(), artifactID)
			if err != nil {
				t.Fatal(err)
			}
			wantRelative := "media/images/" + media.ArtifactID + test.extension
			if artifact.RelativePath != wantRelative || artifact.MIMEType != test.wantMIME || artifact.OriginalFilename != test.filename || artifact.Width != width || artifact.Height != height {
				t.Fatalf("installed Artifact metadata is incorrect: %+v", artifact)
			}
			installedPath := filepath.Join(fixture.root, filepath.FromSlash(wantRelative))
			installed, err := os.ReadFile(installedPath)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			if !bytes.Equal(installed, content) || artifact.SHA256 != hex.EncodeToString(sum[:]) || artifact.ByteSize != int64(len(content)) {
				t.Fatal("Agent upload changed original image bytes or stored an incorrect SHA-256")
			}
			if strings.Contains(response.Body.String(), fixture.root) || strings.Contains(response.Body.String(), "relative_path") {
				t.Fatal("HTTP upload response exposed an internal filesystem path")
			}
			fixture.assertUploadCleaned(t)

			input, err := json.Marshal(map[string]any{
				"client_operation_id": operationID, "model_id": "visible", "text": "看看这个", "focus": map[string]any{},
				"attachments": []map[string]any{{"artifact_id": media.ArtifactID, "name": media.Name, "origin": media.Origin}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/inputs", bytes.NewReader(input))
			accepted := httptest.NewRecorder()
			fixture.handler.ServeHTTP(accepted, request)
			if accepted.Code != http.StatusAccepted {
				t.Fatalf("input using installed image status=%d body=%s", accepted.Code, accepted.Body.String())
			}
			if len(fixture.api.submitted.ImagePaths) != 1 || fixture.api.submitted.ImagePaths[0] != installedPath || len(fixture.api.submitted.ArtifactIDs) != 1 || fixture.api.submitted.ArtifactIDs[0] != artifactID {
				t.Fatalf("HTTP input did not resolve the installed original: %+v", fixture.api.submitted)
			}
			bound, err := fixture.store.AgentConversationMediaForOperation(context.Background(), testEpisode, testSession.String(), operationID)
			if err != nil || len(bound) != 1 || bound[0].TurnID != testTurn.String() || bound[0].Artifact.ID != artifactID {
				t.Fatalf("HTTP input did not persist its media binding: %+v, %v", bound, err)
			}
		})
	}
}

func TestAgentUploadRealProcessorDeduplicatesContentWithDifferentFilenames(t *testing.T) {
	fixture := newRealAgentUploadFixture(t)
	content := realAgentUploadImage(t, "jpeg")
	var ids []string
	for index, filename := range []string{"first.png", "second.jpg"} {
		operationID := []string{"deduplicate-first", "deduplicate-second"}[index]
		response := fixture.upload(t, operationID, filename, "image/png", content)
		if response.Code != http.StatusCreated {
			t.Fatalf("duplicate content upload status=%d body=%s", response.Code, response.Body.String())
		}
		var body struct {
			Media conversationMediaDTO `json:"media"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, body.Media.ArtifactID)
		// The immutable Artifact keeps its first original filename. Each
		// conversation attachment retains the name of that particular upload.
		if body.Media.Name != filename || body.Media.OriginalFilename != "first.png" || body.Media.MIMEType != "image/jpeg" {
			t.Fatalf("deduplicated metadata lost upload identity: %+v", body.Media)
		}
		fixture.assertUploadCleaned(t)
	}
	if ids[0] != ids[1] {
		t.Fatalf("identical bytes created separate Artifacts: %v", ids)
	}
	files, err := os.ReadDir(filepath.Join(fixture.root, "media", "images"))
	if err != nil || len(files) != 1 {
		t.Fatalf("duplicate content installed multiple files: %v, %v", files, err)
	}
	media, err := fixture.store.AgentConversationMediaList(context.Background(), testEpisode, testSession.String())
	if err != nil || len(media) != 2 || media[0].Artifact.ID != media[1].Artifact.ID {
		t.Fatalf("duplicate upload conversation records=%+v, %v", media, err)
	}
}

func TestAgentUploadRealProcessorRejectsInvalidImagesWithoutStagingLeaks(t *testing.T) {
	fixture := newRealAgentUploadFixture(t)
	for _, test := range []struct {
		name    string
		content []byte
	}{
		{"plain-text", []byte("this is not a PNG image")},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="36"><rect width="64" height="36"/></svg>`)},
		{"truncated-jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.upload(t, "invalid-"+test.name, "claimed.png", "image/png", test.content)
			if response.Code != http.StatusUnsupportedMediaType && response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid real image status=%d body=%s", response.Code, response.Body.String())
			}
			assertApplicationError(t, response.Body.Bytes())
			if strings.Contains(response.Body.String(), fixture.root) || strings.Contains(response.Body.String(), fixture.media.sourcePath) {
				t.Fatal("invalid upload response exposed an internal filesystem path")
			}
			fixture.assertUploadCleaned(t)
		})
	}
	media, err := fixture.store.AgentConversationMediaList(context.Background(), testEpisode, testSession.String())
	if err != nil || len(media) != 0 {
		t.Fatalf("invalid image persisted media records: %+v, %v", media, err)
	}
	files, err := os.ReadDir(filepath.Join(fixture.root, "media", "images"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("invalid image installed files: %v", files)
	}
}

// Explicit opt-in reads exactly the configured local image, never a default
// user path. HTTP processing and every database/file write remain temporary.
func TestAgentUploadRealProcessorLocalImageOptional(t *testing.T) {
	path := os.Getenv("SHOTFORWARD_TEST_AGENT_IMAGE_PATH")
	if path == "" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_IMAGE_PATH to verify one read-only local image")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("configured image must be a clean absolute file path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("configured image must be a regular file: %v", err)
	}
	if info.Size() < 1 || info.Size() > maxAgentMediaUploadBytes {
		t.Fatal("configured image must fit the Agent upload limit")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeSHA := sha256.Sum256(before)
	wantMIME := http.DetectContentType(before)
	extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}[wantMIME]
	if extension == "" {
		t.Fatalf("configured image has unsupported content type %q", wantMIME)
	}
	fixture := newRealAgentUploadFixture(t)
	claimedMIME := mime.TypeByExtension(filepath.Ext(path))
	if claimedMIME == "" {
		claimedMIME = "application/octet-stream"
	}
	response := fixture.upload(t, "opt-in-local-image", filepath.Base(path), claimedMIME, before)
	if response.Code != http.StatusCreated {
		t.Fatalf("local image upload status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Media conversationMediaDTO `json:"media"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	artifactID, err := project.ParseArtifactID(body.Media.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := fixture.store.Artifact(context.Background(), artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.MIMEType != wantMIME || artifact.RelativePath != "media/images/"+artifact.ID.String()+extension || artifact.OriginalFilename != filepath.Base(path) || artifact.SHA256 != hex.EncodeToString(beforeSHA[:]) || artifact.ByteSize != int64(len(before)) {
		t.Fatalf("local image metadata was not preserved/normalized: %+v", artifact)
	}
	installed, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(artifact.RelativePath)))
	if err != nil || !bytes.Equal(installed, before) {
		t.Fatalf("installed copy changed the original bytes: %v", err)
	}
	fixture.assertUploadCleaned(t)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterSHA := sha256.Sum256(after); afterSHA != beforeSHA || !bytes.Equal(before, after) {
		t.Fatal("read-only local image changed during upload verification")
	}
	t.Logf("verified read-only source and installed copy: content_type=%s dimensions=%dx%d bytes=%d; source SHA-256 unchanged", artifact.MIMEType, artifact.Width, artifact.Height, artifact.ByteSize)
}

type realAgentUploadClock struct{}

func (realAgentUploadClock) Now() time.Time { return testTime }

type tracedRealAgentMedia struct {
	*projectapp.Service
	sourcePath string
}

func (m *tracedRealAgentMedia) UploadAgentConversationMedia(ctx context.Context, input projectapp.UploadAgentConversationMediaInput) (project.AgentConversationMedia, error) {
	m.sourcePath = input.SourcePath
	return m.Service.UploadAgentConversationMedia(ctx, input)
}

type realAgentUploadFixture struct {
	root    string
	store   projectapp.ProjectStore
	handler *Handler
	api     *fakeAPI
	media   *tracedRealAgentMedia
}

func newRealAgentUploadFixture(t *testing.T) realAgentUploadFixture {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	processor, err := mediafiles.NewProcessor(ffprobe, ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	studioStore, err := studio.Open(ctx, filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = studioStore.Close() })
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	factory := projectstore.ApplicationStoreFactory{}
	store, err := factory.Create(ctx, filepath.Join(root, "project.db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := project.NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(ctx,
		project.Project{ID: testProject, Name: "Synthetic Agent upload", Output: output, Revision: 1, CreatedAt: testTime, UpdatedAt: testTime},
		project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: testEpisode, ProjectID: testProject, SequenceNumber: 1, Title: "Synthetic Episode", TargetDuration: time.Minute, Revision: 1, CreatedAt: testTime, UpdatedAt: testTime}, Script: project.Script{Revision: 1, UpdatedAt: testTime}},
	); err != nil {
		store.Close()
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	if _, _, err := registry.Register(projectapp.ProjectHandle{ProjectID: testProject, RootPath: root, Store: store}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.CloseAll() })
	service, err := projectapp.NewService(projectapp.Dependencies{
		IDs: projectapp.NewSecureIDGenerator(), Clock: realAgentUploadClock{}, Workspace: platformpaths.ApplicationProjectWorkspace{},
		Stores: factory, Library: library, Importer: scriptimport.FileImporter{}, Registry: registry, MediaFiles: processor,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	media := &tracedRealAgentMedia{Service: service}
	handler, err := NewHandler(Options{
		Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{}, Media: media,
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return realAgentUploadFixture{root: root, store: store, handler: handler, api: api, media: media}
}

func (f realAgentUploadFixture) upload(t *testing.T, operationID, filename, claimedMIME string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("client_operation_id", operationID); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", claimedMIME)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/media", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f realAgentUploadFixture) assertUploadCleaned(t *testing.T) {
	t.Helper()
	if f.media.sourcePath == "" {
		t.Fatal("upload did not reach the real Project service")
	}
	if _, err := os.Stat(f.media.sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HTTP upload temporary file remains: %v", err)
	}
	files, err := os.ReadDir(filepath.Join(f.root, "media", ".staging"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("image upload left staging files: %v", files)
	}
}

func realAgentUploadImage(t *testing.T, format string) []byte {
	t.Helper()
	if format == "webp" {
		// A genuine 1x1 WebP fixture needs only the installed decoder, not a
		// libwebp encoder (which is absent from some bundled FFmpeg builds).
		content, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	source := image.NewNRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 5), B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if format == "jpeg" {
		if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatal(err)
		}
		return encoded.Bytes()
	}
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
