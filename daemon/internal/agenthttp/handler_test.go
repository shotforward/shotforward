package agenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentscheduler"
	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestRuntimeRestoreFailureMapsToSafeServiceUnavailableError(t *testing.T) {
	mapped := mapError(errors.Join(agentsession.ErrRuntimeNotReady, errors.New("private Runtime detail")))
	if mapped.status != http.StatusServiceUnavailable || mapped.Code != "runtime_unavailable" {
		t.Fatalf("mapped error = %#v", mapped)
	}
	if strings.Contains(mapped.Message, "private Runtime detail") {
		t.Fatalf("mapped error leaked internal detail: %#v", mapped)
	}
}

func TestSessionCreationFailureWritesRedactedDiagnostic(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	api := &fakeAPI{createErr: errors.New("thread/start failed: Bearer private-token api_key=private-key")}
	handler, err := NewHandler(Options{
		Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{},
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024)), Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(http.MethodPost,
		"/api/v1/projects/"+testProject.String()+"/episodes/"+testEpisode.String()+"/agent-sessions",
		strings.NewReader(`{"model_id":"visible","runtime_kind":"user_codex","web_search_disabled":true}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	diagnostic := logs.String()
	for _, expected := range []string{"Agent session creation failed", "request_id=", "error_code=external_failure", "thread/start failed"} {
		if !strings.Contains(diagnostic, expected) {
			t.Fatalf("diagnostic missing %q: %s", expected, diagnostic)
		}
	}
	if strings.Contains(diagnostic, "private-token") || strings.Contains(diagnostic, "private-key") {
		t.Fatalf("diagnostic leaked a secret: %s", diagnostic)
	}
}

func TestRuntimeNetworkSettingsAreReturnedAndUpdated(t *testing.T) {
	handler, api := testHandler(t, fakeEvents{})

	request := authorizedRequest(http.MethodPut, "/api/v1/agent/runtime/network", strings.NewReader(`{"proxy_url":"http://127.0.0.1:7890"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update network status=%d body=%s", response.Code, response.Body.String())
	}
	if api.proxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("proxyURL=%q", api.proxyURL)
	}

	request = authorizedRequest(http.MethodGet, "/api/v1/agent/runtime/network", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"proxy_url":"http://127.0.0.1:7890"`) {
		t.Fatalf("network settings status=%d body=%s", response.Code, response.Body.String())
	}

	request = authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"proxy_url":"http://127.0.0.1:7890"`) {
		t.Fatalf("runtime snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRuntimeFailureSnapshotRemainsVisible(t *testing.T) {
	api := &fakeAPI{
		snapshot: agentapp.RuntimeSnapshot{
			Health: agentapp.RuntimeIncompatible, Version: "0.155.0", MinimumVersion: "0.155.1",
			FailureCode: "codex_upgrade_required", FailureSummary: "Update local Codex",
		},
		snapshotErr: errors.New("local Codex is too old"),
	}
	handler, err := NewHandler(Options{
		Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{},
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"failure_code":"codex_upgrade_required"`) ||
		!strings.Contains(response.Body.String(), `"minimum_version":"0.155.1"`) {
		t.Fatalf("runtime snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRuntimeSnapshotIncludesSafeExecutionStatus(t *testing.T) {
	api := &fakeAPI{snapshot: agentapp.RuntimeSnapshot{
		Health: agentapp.RuntimeReady, Version: "0.200.0",
		SafeExecutionAvailable: true, SafeExecutionBackend: "codex_linux_sandbox",
	}}
	handler, err := NewHandler(Options{
		Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{},
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"safe_execution_available":true`) ||
		!strings.Contains(response.Body.String(), `"safe_execution_backend":"codex_linux_sandbox"`) {
		t.Fatalf("runtime snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}

type routedRuntimeProbe struct {
	active                       agentapp.RuntimeKind
	snapshots, models, refreshes map[agentapp.RuntimeKind]int
}

func newRoutedRuntimeProbe() *routedRuntimeProbe {
	return &routedRuntimeProbe{active: agentapp.RuntimeShotForward, snapshots: map[agentapp.RuntimeKind]int{}, models: map[agentapp.RuntimeKind]int{}, refreshes: map[agentapp.RuntimeKind]int{}}
}

func (r *routedRuntimeProbe) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return r.SnapshotFor(context.Background(), r.active)
}

func (r *routedRuntimeProbe) Models(context.Context) ([]agentapp.Model, error) {
	return r.ModelsFor(context.Background(), r.active)
}

func (r *routedRuntimeProbe) SnapshotFor(_ context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
	r.snapshots[kind]++
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady, Version: string(kind)}, nil
}

func (r *routedRuntimeProbe) ModelsFor(_ context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	r.models[kind]++
	return []agentapp.Model{{ID: string(kind) + "-model", DisplayName: string(kind), RuntimeKind: kind}}, nil
}

func (r *routedRuntimeProbe) RefreshModelsFor(_ context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	r.refreshes[kind]++
	return []agentapp.Model{{ID: string(kind) + "-model", DisplayName: string(kind), RuntimeKind: kind}}, nil
}

func (r *routedRuntimeProbe) ActiveRuntime(context.Context) (agentapp.RuntimeKind, error) {
	return r.active, nil
}

func (r *routedRuntimeProbe) SelectRuntime(_ context.Context, kind agentapp.RuntimeKind) error {
	r.active = kind
	return nil
}

func TestRuntimeEndpointLoadsOnlyTheRequestedRuntime(t *testing.T) {
	probe := newRoutedRuntimeProbe()
	api := &fakeAPI{}
	handler, err := NewHandler(Options{
		Runtime: probe, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{},
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024)),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || probe.snapshots[agentapp.RuntimeShotForward] != 1 || probe.models[agentapp.RuntimeShotForward] != 1 || probe.snapshots[agentapp.RuntimeUserCodex] != 0 || probe.models[agentapp.RuntimeUserCodex] != 0 {
		t.Fatalf("default status=%d snapshots=%v models=%v body=%s", response.Code, probe.snapshots, probe.models, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"user_codex"`) {
		t.Fatalf("default response eagerly included inactive Runtime: %s", response.Body.String())
	}

	request = authorizedRequest(http.MethodGet, "/api/v1/agent/runtime?runtime_kind=user_codex&refresh=true", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || probe.snapshots[agentapp.RuntimeUserCodex] != 1 || probe.refreshes[agentapp.RuntimeUserCodex] != 1 || probe.models[agentapp.RuntimeUserCodex] != 0 {
		t.Fatalf("targeted status=%d snapshots=%v models=%v refreshes=%v body=%s", response.Code, probe.snapshots, probe.models, probe.refreshes, response.Body.String())
	}
}

const (
	testBearer    = "bearer_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	testCSRF      = "csrf_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	testHost      = "127.0.0.1:41234"
	testOrigin    = "http://127.0.0.1:3415"
	testProject   = project.ProjectID("prj_AAAAAAAAAAAAAAAAAAAAAAAAAA")
	testEpisode   = project.EpisodeID("ep_BBBBBBBBBBBBBBBBBBBBBBBBBB")
	testSession   = agent.SessionID("ags_CCCCCCCCCCCCCCCCCCCCCCCCCC")
	testTurn      = agent.TurnID("agt_DDDDDDDDDDDDDDDDDDDDDDDDDD")
	testChangeSet = changeset.ChangeSetID("cst_EEEEEEEEEEEEEEEEEEEEEEEEEE")
)

type fakeAPI struct {
	submitted   agentscheduler.SubmitInput
	applied     bool
	proxyURL    string
	createErr   error
	snapshot    agentapp.RuntimeSnapshot
	snapshotErr error
}

type fakeAgentMedia struct {
	uploadInput  projectapp.UploadAgentConversationMediaInput
	uploadBytes  []byte
	prepareInput projectapp.PrepareAgentInputMediaInput
	prepared     []projectapp.AgentInputMedia
	bindInput    projectapp.BindAgentInputMediaInput
}

func (f *fakeAgentMedia) UploadAgentConversationMedia(_ context.Context, input projectapp.UploadAgentConversationMediaInput) (project.AgentConversationMedia, error) {
	f.uploadInput = input
	f.uploadBytes, _ = os.ReadFile(input.SourcePath)
	artifactID, _ := project.ParseArtifactID("art_FFFFFFFFFFFFFFFFFFFFFFFFFF")
	return project.AgentConversationMedia{
		EpisodeID: input.EpisodeID, SessionID: input.SessionID, ClientOperationID: input.ClientOperationID,
		Artifact: project.Artifact{ID: artifactID, ProjectID: input.ProjectID, Kind: project.MediaKindImage,
			RelativePath: "media/images/upload.png", SHA256: strings.Repeat("f", 64), ByteSize: int64(len(f.uploadBytes)),
			MIMEType: "image/png", OriginalFilename: input.OriginalFilename, Width: 64, Height: 36, CreatedAt: testTime},
		Name: input.OriginalFilename, Origin: project.AgentConversationMediaUploaded, CreatedAt: testTime,
	}, nil
}

func (f *fakeAgentMedia) PrepareAgentInputMedia(_ context.Context, input projectapp.PrepareAgentInputMediaInput) ([]projectapp.AgentInputMedia, error) {
	f.prepareInput = input
	return append([]projectapp.AgentInputMedia(nil), f.prepared...), nil
}

func (f *fakeAgentMedia) BindAgentInputMedia(_ context.Context, input projectapp.BindAgentInputMediaInput) error {
	f.bindInput = input
	return nil
}

func (f *fakeAPI) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	if f.snapshot.Health != "" {
		return f.snapshot, f.snapshotErr
	}
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady, Version: "1.2.3", Executable: "/private/path/codex"}, nil
}
func (f *fakeAPI) Models(context.Context) ([]agentapp.Model, error) {
	return []agentapp.Model{{ID: "visible", DisplayName: "Visible"}, {ID: "hidden", DisplayName: "Hidden", Hidden: true}}, nil
}
func (f *fakeAPI) NetworkSettings(context.Context) (agentapp.RuntimeNetworkSettings, error) {
	return agentapp.RuntimeNetworkSettings{ProxyURL: f.proxyURL}, nil
}
func (f *fakeAPI) ConfigureNetwork(_ context.Context, settings agentapp.RuntimeNetworkSettings) error {
	f.proxyURL = settings.ProxyURL
	return nil
}
func (f *fakeAPI) List(context.Context, project.ProjectID, project.EpisodeID, bool) ([]agent.AgentSession, error) {
	return []agent.AgentSession{testSessionValue()}, nil
}
func (f *fakeAPI) Create(context.Context, agentapp.CreateSessionInput) (agent.AgentSession, error) {
	if f.createErr != nil {
		return agent.AgentSession{}, f.createErr
	}
	return testSessionValue(), nil
}
func (f *fakeAPI) SnapshotSession() agentapp.SessionSnapshot { return agentapp.SessionSnapshot{} }
func (f *fakeAPI) Archive(_ context.Context, _ agent.SessionID, _ int64) (agent.AgentSession, error) {
	value := testSessionValue()
	at := testTime.Add(time.Second)
	value.Status, value.Revision, value.ArchivedAt, value.UpdatedAt = agent.SessionArchived, 2, &at, at
	return value, nil
}
func (f *fakeAPI) SnapshotAgent(_ context.Context, _ agent.SessionID) (agentapp.SessionSnapshot, error) {
	return agentapp.SessionSnapshot{Session: testSessionValue(), Turns: []agent.AgentTurn{testTurnValue()}, ChangeSets: []changeset.ChangeSet{testChangeSetValue()}}, nil
}
func (f *fakeAPI) ConversationAgent(_ context.Context, _ agent.SessionID, cursor string, limit int) (agentapp.ConversationPage, error) {
	return agentapp.ConversationPage{Turns: []agentapp.ConversationTurn{{Turn: testTurnValue(), Messages: []agentapp.ConversationMessage{
		{ID: "agm_public", ClientOperationID: "request-1", Role: agentapp.ConversationUser, Text: "Improve it"},
		{ID: "agm_reply", Role: agentapp.ConversationAssistant, Phase: agentapp.ConversationPhaseFinal, Text: "Done"},
	}}}, NextCursor: "older-page"}, nil
}

// Snapshot is already used by RuntimeQueries, so a small wrapper supplies the
// SessionCommands method with its distinct return type.
type fakeSessions struct{ api *fakeAPI }

func (f fakeSessions) List(ctx context.Context, p project.ProjectID, e project.EpisodeID, archived bool) ([]agent.AgentSession, error) {
	return f.api.List(ctx, p, e, archived)
}
func (f fakeSessions) Create(ctx context.Context, input agentapp.CreateSessionInput) (agent.AgentSession, error) {
	return f.api.Create(ctx, input)
}
func (f fakeSessions) Snapshot(ctx context.Context, id agent.SessionID) (agentapp.SessionSnapshot, error) {
	return f.api.SnapshotAgent(ctx, id)
}
func (f fakeSessions) Conversation(ctx context.Context, id agent.SessionID, cursor string, limit int) (agentapp.ConversationPage, error) {
	return f.api.ConversationAgent(ctx, id, cursor, limit)
}
func (f fakeSessions) Archive(ctx context.Context, id agent.SessionID, revision int64) (agent.AgentSession, error) {
	return f.api.Archive(ctx, id, revision)
}

func (f *fakeAPI) SubmitInput(_ context.Context, input agentscheduler.SubmitInput) (agentscheduler.SubmitResult, error) {
	f.submitted = input
	return agentscheduler.SubmitResult{Action: agent.InputQueueTurn, Turn: testTurnValue()}, nil
}
func (f *fakeAPI) InterruptTurn(context.Context, agent.SessionID, agent.TurnID) (agent.AgentTurn, error) {
	return testTurnValue(), nil
}
func (f *fakeAPI) Get(context.Context, changeset.ChangeSetID) (changeset.ChangeSet, error) {
	return testChangeSetValue(), nil
}
func (f *fakeAPI) Operations(context.Context, changeset.ChangeSetID, int, int) ([]changeset.Operation, error) {
	return nil, nil
}
func (f *fakeAPI) Apply(context.Context, changeset.ChangeSetID, int64, string) (changesetapp.CommandResult, error) {
	f.applied = true
	return changesetapp.CommandResult{ChangeSet: testChangeSetValue()}, nil
}
func (f *fakeAPI) Reject(context.Context, changeset.ChangeSetID, int64, string, string) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{ChangeSet: testChangeSetValue()}, nil
}

func (f *fakeAPI) Undo(context.Context, changeset.ChangeSetID, string) (changesetapp.CommandResult, error) {
	return changesetapp.CommandResult{ChangeSet: testChangeSetValue()}, nil
}

type fakeEvents struct{ values []agentsse.Event }

func (f fakeEvents) Capture(_ agent.SessionID, read func() error) (string, error) {
	if err := read(); err != nil {
		return "", err
	}
	return "EPOCH:7", nil
}
func (f fakeEvents) Subscribe(context.Context, agent.SessionID, string) (<-chan agentsse.Event, func(), error) {
	result := make(chan agentsse.Event, len(f.values))
	for _, value := range f.values {
		result <- value
	}
	close(result)
	return result, func() {}, nil
}

func TestRuntimeEndpointEnforcesLocalSecurityAndRedactsPrivateRuntimeData(t *testing.T) {
	handler, _ := testHandler(t, fakeEvents{})
	for name, mutate := range map[string]func(*http.Request){
		"remote": func(r *http.Request) { r.RemoteAddr = "203.0.113.1:80" },
		"host":   func(r *http.Request) { r.Host = "attacker.example" },
		"origin": func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		"auth":   func(r *http.Request) { r.Header.Del("Authorization") },
	} {
		t.Run(name, func(t *testing.T) {
			request := authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
			mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 {
				t.Fatalf("status = %d", response.Code)
			}
			assertApplicationError(t, response.Body.Bytes())
		})
	}

	request := authorizedRequest(http.MethodGet, "/api/v1/agent/runtime", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "/private/path") || strings.Contains(body, "hidden") || !strings.Contains(body, `"display_name":"Visible"`) {
		t.Fatalf("unsafe or malformed runtime response: %s", body)
	}
}

func TestInputEndpointRequiresCSRFAndReturnsQueueActionWithoutCodexIDs(t *testing.T) {
	handler, api := testHandler(t, fakeEvents{})
	path := "/api/v1/agent-sessions/" + testSession.String() + "/inputs"
	body := `{"client_operation_id":"client-1","model_id":"visible","text":"Improve it","focus":{}}`
	request := authorizedRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Del("X-CSRF-Token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.Code)
	}

	request = authorizedRequest(http.MethodPost, path, strings.NewReader(body))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if api.submitted.Text != "Improve it" || !strings.Contains(response.Body.String(), `"action":"queue_turn"`) {
		t.Fatalf("submission = %#v response=%s", api.submitted, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "codex") {
		t.Fatalf("Codex identity leaked: %s", response.Body.String())
	}
}

func TestAgentMediaUploadAndAttachmentSubmission(t *testing.T) {
	api := &fakeAPI{}
	media := &fakeAgentMedia{}
	handler, err := NewHandler(Options{
		Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: fakeEvents{}, Media: media,
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024)),
	})
	if err != nil {
		t.Fatal(err)
	}

	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	if err := writer.WriteField("client_operation_id", "media-operation-1"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "reference.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-png-content")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploadRequest := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/media", &upload)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploadResponse.Code, uploadResponse.Body.String())
	}
	if media.uploadInput.ProjectID != testProject || media.uploadInput.EpisodeID != testEpisode || media.uploadInput.SessionID != testSession.String() ||
		media.uploadInput.ClientOperationID != "media-operation-1" || media.uploadInput.OriginalFilename != "reference.png" || string(media.uploadBytes) != "fake-png-content" {
		t.Fatalf("upload input=%#v bytes=%q", media.uploadInput, media.uploadBytes)
	}
	if !strings.Contains(uploadResponse.Body.String(), `"origin":"uploaded"`) || !strings.Contains(uploadResponse.Body.String(), `"artifact_id":"art_FFFFFFFFFFFFFFFFFFFFFFFFFF"`) {
		t.Fatalf("upload response=%s", uploadResponse.Body.String())
	}

	artifactID, _ := project.ParseArtifactID("art_FFFFFFFFFFFFFFFFFFFFFFFFFF")
	media.prepared = []projectapp.AgentInputMedia{{
		Media: project.AgentConversationMedia{EpisodeID: testEpisode, SessionID: testSession.String(), ClientOperationID: "media-operation-1",
			Artifact: project.Artifact{ID: artifactID, ProjectID: testProject, Kind: project.MediaKindImage}, Name: "reference.png", Origin: project.AgentConversationMediaUploaded, CreatedAt: testTime},
		AbsolutePath: "/project/media/images/reference.png",
	}}
	inputBody := `{"client_operation_id":"media-operation-1","model_id":"visible","text":"Use this image","focus":{},"attachments":[{"artifact_id":"art_FFFFFFFFFFFFFFFFFFFFFFFFFF","name":"reference.png","origin":"uploaded"}]}`
	inputRequest := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/inputs", strings.NewReader(inputBody))
	inputResponse := httptest.NewRecorder()
	handler.ServeHTTP(inputResponse, inputRequest)
	if inputResponse.Code != http.StatusAccepted {
		t.Fatalf("input status=%d body=%s", inputResponse.Code, inputResponse.Body.String())
	}
	if len(media.prepareInput.References) != 1 || media.prepareInput.References[0].ArtifactID != artifactID ||
		len(api.submitted.ImagePaths) != 1 || api.submitted.ImagePaths[0] != "/project/media/images/reference.png" ||
		len(api.submitted.ArtifactIDs) != 1 || api.submitted.ArtifactIDs[0] != artifactID {
		t.Fatalf("prepared=%#v submitted=%#v", media.prepareInput, api.submitted)
	}
	if media.bindInput.TurnID != testTurn.String() || media.bindInput.ClientOperationID != "media-operation-1" || len(media.bindInput.ArtifactIDs) != 1 || media.bindInput.ArtifactIDs[0] != artifactID {
		t.Fatalf("bind input=%#v", media.bindInput)
	}
}

func TestConversationEndpointReturnsOnlyDisplaySafeMessages(t *testing.T) {
	handler, _ := testHandler(t, fakeEvents{})
	request := authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/messages?limit=20", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"role":"user"`, `"text":"Improve it"`, `"client_operation_id":"request-1"`, `"role":"assistant"`, `"phase":"final_answer"`, `"next_cursor":"older-page"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	if strings.Count(body, `"client_operation_id":"request-1"`) != 1 || strings.Contains(body, `"client_operation_id":""`) {
		t.Fatalf("operation provenance should be optional and user-only: %s", body)
	}
	if strings.Contains(body, "private-thread") || strings.Contains(body, "private-turn") || strings.Contains(strings.ToLower(body), "codex") {
		t.Fatalf("conversation response leaked Runtime identity: %s", body)
	}
}

func TestInputEndpointRejectsUnknownJSONAndMalformedScope(t *testing.T) {
	handler, _ := testHandler(t, fakeEvents{})
	for _, test := range []struct{ path, body string }{
		{"/api/v1/agent-sessions/" + testSession.String() + "/inputs", `{"client_operation_id":"x","text":"y","focus":{},"unexpected":true}`},
		{"/api/v1/projects/not-a-project/episodes/" + testEpisode.String() + "/agent-sessions", `{}`},
	} {
		request := authorizedRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", test.path, response.Code, response.Body.String())
		}
		assertApplicationError(t, response.Body.Bytes())
	}
}

func TestSSEUsesStableCursorAndSafeStudioEvent(t *testing.T) {
	event := agentapp.StudioEvent{Type: agentapp.EventAssistantDelta, SessionID: testSession, TurnID: testTurn, TextDelta: "hello", OccurredAt: testTime}
	handler, _ := testHandler(t, fakeEvents{values: []agentsse.Event{{ID: "EPOCH:8", Value: event}}})
	request := authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/events", nil)
	request.Header.Set("Last-Event-ID", "EPOCH:7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "id: EPOCH:8\nevent: assistant.delta") || !strings.Contains(body, `"text_delta":"hello"`) {
		t.Fatalf("SSE response = %d %q", response.Code, body)
	}
	if strings.Contains(strings.ToLower(body), "codex") {
		t.Fatalf("SSE leaked Runtime identity: %s", body)
	}
}

func TestSSEAcceptsBrowserCursorQuery(t *testing.T) {
	event := agentapp.StudioEvent{Type: agentapp.EventAssistantDelta, SessionID: testSession, TurnID: testTurn, TextDelta: "hello", OccurredAt: testTime}
	handler, _ := testHandler(t, fakeEvents{values: []agentsse.Event{{ID: "EPOCH:8", Value: event}}})
	request := authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/events?after=EPOCH%3A7", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "id: EPOCH:8") {
		t.Fatalf("SSE response = %d %q", response.Code, response.Body.String())
	}
}

func TestChangeSetApplyIsAnAuthenticatedLocalUICommand(t *testing.T) {
	handler, api := testHandler(t, fakeEvents{})
	request := authorizedRequest(http.MethodPost, "/api/v1/change-sets/"+testChangeSet.String()+":apply", strings.NewReader(`{"expected_revision":3,"operation_key":"apply-1"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !api.applied {
		t.Fatalf("status=%d applied=%v body=%s", response.Code, api.applied, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "codex_thread") || strings.Contains(response.Body.String(), "codex_turn") {
		t.Fatalf("ChangeSet response leaked Runtime identity: %s", response.Body.String())
	}
}

func TestDocumentedAgentAndChangeSetRoutesAreReachable(t *testing.T) {
	handler, _ := testHandler(t, fakeEvents{})
	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/api/v1/projects/" + testProject.String() + "/episodes/" + testEpisode.String() + "/agent-sessions", "", http.StatusOK},
		{http.MethodPost, "/api/v1/projects/" + testProject.String() + "/episodes/" + testEpisode.String() + "/agent-sessions", `{"model_id":"visible","web_search_disabled":true}`, http.StatusCreated},
		{http.MethodGet, "/api/v1/agent-sessions/" + testSession.String() + "/snapshot", "", http.StatusOK},
		{http.MethodGet, "/api/v1/agent-sessions/" + testSession.String() + "/messages?limit=20", "", http.StatusOK},
		{http.MethodPost, "/api/v1/agent-sessions/" + testSession.String() + "/turns/" + testTurn.String() + ":interrupt", "", http.StatusOK},
		{http.MethodPost, "/api/v1/agent-sessions/" + testSession.String() + ":archive", `{"expected_revision":1}`, http.StatusOK},
		{http.MethodGet, "/api/v1/change-sets/" + testChangeSet.String(), "", http.StatusOK},
		{http.MethodGet, "/api/v1/change-sets/" + testChangeSet.String() + "/operations?after_position=0&limit=50", "", http.StatusOK},
		{http.MethodPost, "/api/v1/change-sets/" + testChangeSet.String() + ":reject", `{"expected_revision":3,"operation_key":"reject-1","reason":"Not needed"}`, http.StatusOK},
		{http.MethodPost, "/api/v1/change-sets/" + testChangeSet.String() + ":undo", `{"operation_key":"undo-1"}`, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request := authorizedRequest(test.method, test.path, body)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			lowerBody := strings.ToLower(response.Body.String())
			if strings.Contains(lowerBody, "codex_thread") || strings.Contains(lowerBody, "codex_session") {
				t.Fatalf("response leaked Runtime identity: %s", response.Body.String())
			}
		})
	}
}

func testHandler(t *testing.T, events fakeEvents) (*Handler, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	handler, err := NewHandler(Options{Runtime: api, Network: api, Sessions: fakeSessions{api}, Turns: api, ChangeSets: api, Events: events,
		Security: Security{BearerToken: testBearer, CSRFToken: testCSRF, AllowedHosts: []string{testHost}, AllowedOrigins: []string{testOrigin}},
		Random:   bytes.NewReader(make([]byte, 1_024))})
	if err != nil {
		t.Fatal(err)
	}
	return handler, api
}

func authorizedRequest(method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, "http://"+testHost+path, body)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Host = testHost
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Authorization", "Bearer "+testBearer)
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", testCSRF)
	}
	return request
}

func assertApplicationError(t *testing.T, data []byte) {
	t.Helper()
	var response struct {
		Error applicationError `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode error: %v body=%s", err, data)
	}
	if response.Error.Code == "" || response.Error.Message == "" || response.Error.RequestID == "" || response.Error.Details == nil || response.Error.SuggestedAction == "" {
		t.Fatalf("incomplete ApplicationError: %#v", response.Error)
	}
}

var testTime = time.UnixMilli(1_700_000_000_000).UTC()

func testSessionValue() agent.AgentSession {
	return agent.AgentSession{ID: testSession, ProjectID: testProject, EpisodeID: testEpisode, CodexThreadID: "private-thread", CodexSessionID: "private-session",
		ModelID: "visible", Status: agent.SessionActive, Revision: 1, CreatedAt: testTime, UpdatedAt: testTime}
}

func testTurnValue() agent.AgentTurn {
	return agent.AgentTurn{ID: testTurn, AgentSessionID: testSession, ModelID: "visible", CodexTurnID: "private-turn", ClientOperationID: "client-1", Status: agent.TurnQueued,
		Dispatch: agent.DispatchQueued, Revision: 1, CreatedAt: testTime}
}

func testChangeSetValue() changeset.ChangeSet {
	finalized := testTime
	return changeset.ChangeSet{ID: testChangeSet, ProjectID: testProject, EpisodeID: testEpisode,
		Source: changeset.Source{AgentSessionID: testSession, AgentTurnID: testTurn, CodexThreadID: "private-thread", CodexTurnID: "private-turn"},
		Title:  "Prompt proposal", Status: changeset.StatusPending, OperationCount: 1, Revision: 3, CreatedAt: testTime, UpdatedAt: testTime, FinalizedAt: &finalized}
}
