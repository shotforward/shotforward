package agenthttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agenthttp"
	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	"github.com/shotforward/shotforward/daemon/internal/agentscheduler"
	"github.com/shotforward/shotforward/daemon/internal/agentsession"
	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

const (
	e2eBearer = "e2e_bearer_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	e2eCSRF   = "e2e_csrf_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	e2eHost   = "127.0.0.1:41415"
	e2eOrigin = "http://127.0.0.1:3415"
)

var (
	e2eProjectID    = project.ProjectID("prj_WWWWWWWWWWWWWWWWWWWWWWWWWW")
	e2eEpisodeID    = project.EpisodeID("ep_XXXXXXXXXXXXXXXXXXXXXXXXXX")
	e2eOtherEpisode = project.EpisodeID("ep_YYYYYYYYYYYYYYYYYYYYYYYYYY")
	e2eAssetID      = project.AssetID("ast_AAAAAAAAAAAAAAAAAAAAAAAAAA")
	e2eOtherAssetID = project.AssetID("ast_BBBBBBBBBBBBBBBBBBBBBBBBBB")
	e2eBaseTime     = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
)

func TestPromptChangeSetEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	fixture := newE2EFixture(t, func(token agentmcp.Credential, workspace string) agentapp.Runtime {
		return newScriptedRuntime(token.Value)
	})
	ctx := context.Background()
	sessionID := fixture.createSession(t, ctx)

	firstCursor, err := fixture.events.Cursor(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	firstTurn := fixture.submitAndProcess(t, ctx, sessionID, "e2e-input-1", "cinematic sunrise prompt")
	applied := fixture.singleAppliedForTurn(t, ctx, sessionID, firstTurn)
	if applied.Source.AgentTurnID != firstTurn {
		t.Fatalf("applied ChangeSet Turn = %s, want %s", applied.Source.AgentTurnID, firstTurn)
	}
	fixture.assertEventSequence(t, ctx, sessionID, firstCursor, []agentapp.StudioEventType{
		agentapp.EventTurnQueued, agentapp.EventTurnStarted, agentapp.EventToolStarted,
		agentapp.EventToolCompleted, agentapp.EventChangeSetApplied, agentapp.EventAssistantDelta,
		agentapp.EventTurnCompleted,
	})

	snapshot := fixture.request(t, ctx, http.MethodGet, "/api/v1/agent-sessions/"+sessionID.String()+"/snapshot", "")
	if snapshot.Code != http.StatusOK || !strings.Contains(snapshot.Body.String(), applied.ID.String()) || !strings.Contains(snapshot.Body.String(), `"status":"applied"`) {
		t.Fatalf("Session snapshot status=%d body=%s", snapshot.Code, snapshot.Body.String())
	}
	for _, privateKey := range []string{`"codex_thread_id"`, `"codex_session_id"`, `"codex_turn_id"`, `"mcp_token"`} {
		if strings.Contains(strings.ToLower(snapshot.Body.String()), privateKey) {
			t.Fatalf("Session snapshot leaked private Runtime field %s", privateKey)
		}
	}
	asset := fixture.asset(t, ctx, e2eAssetID)
	if asset.Prompt != "cinematic sunrise prompt" {
		t.Fatalf("applied Prompt = %q", asset.Prompt)
	}

	fixture.undo(t, ctx, applied.ID, "e2e-undo-1")
	if got := fixture.asset(t, ctx, e2eAssetID).Prompt; got != "original prompt" {
		t.Fatalf("Undo did not restore the original Prompt: %q", got)
	}
	secondTurn := fixture.submitAndProcess(t, ctx, sessionID, "e2e-input-2", "proposal that must conflict")
	conflicting := fixture.singleAppliedForTurn(t, ctx, sessionID, secondTurn)
	asset = fixture.asset(t, ctx, e2eAssetID)
	manualRevision := asset.Revision
	asset.Prompt = "manual editor value"
	asset.Revision++
	asset.UpdatedAt = fixture.clock.Now()
	if err := fixture.projectStore.UpdateAsset(ctx, asset, manualRevision, asset.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	conflict := fixture.request(t, ctx, http.MethodPost, "/api/v1/change-sets/"+conflicting.ID.String()+":undo",
		`{"operation_key":"e2e-undo-conflict"}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"change_set_precondition_failed"`) || !strings.Contains(conflict.Body.String(), "Refresh the current project state") || strings.Contains(conflict.Body.String(), "recorded conflicts") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if got := fixture.asset(t, ctx, e2eAssetID).Prompt; got != "manual editor value" {
		t.Fatalf("conflicted Undo overwrote Prompt: %q", got)
	}

	queued := fixture.submit(t, ctx, sessionID, "e2e-input-restart", "must not be replayed")
	restarted, err := agentscheduler.NewTurnScheduler(agentscheduler.SchedulerOptions{Store: fixture.studioStore, Runtime: fixture.runtime,
		MCP: fixture.mcp, IDs: agentapp.NewSecureIDGenerator(), Clock: fixture.clock, Events: fixture.events})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.studioStore.AgentTurn(ctx, queued)
	if err != nil || recovered.Status != agent.TurnInterrupted {
		t.Fatalf("recovered queued Turn = %#v, %v", recovered, err)
	}

	fixture.assertMCPBoundary(t, ctx, sessionID)
}

func TestAssetExtractionEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	fixture := newE2EFixture(t, func(token agentmcp.Credential, _ string) agentapp.Runtime {
		return newScriptedRuntime(token.Value)
	})
	ctx := context.Background()
	sessionID := fixture.createSession(t, ctx)
	turnID := fixture.submitAndProcess(t, ctx, sessionID, "e2e-extract-input", "extract:brass-key")
	applied := fixture.singleAppliedForTurn(t, ctx, sessionID, turnID)
	operations, err := fixture.changes.Operations(ctx, applied.ID, 0, 10)
	if err != nil || len(operations) != 1 || operations[0].AssetCreation == nil {
		t.Fatalf("extraction operations = %+v, %v", operations, err)
	}
	creation := operations[0].AssetCreation
	if creation.Kind != project.AssetKindProp || creation.Name != "Brass Key" || creation.SourceAssetID != "" {
		t.Fatalf("Asset creation = %+v", creation)
	}
	created, err := fixture.projectStore.Asset(ctx, creation.AssetID)
	if err != nil || created.Identity.Name != "Brass Key" || created.Asset.Prompt != "aged brass key, cinematic macro detail" {
		t.Fatalf("created extracted Asset = %+v, %v", created, err)
	}
	fixture.undo(t, ctx, applied.ID, "e2e-extract-undo")
	_, err = fixture.projectStore.Asset(ctx, creation.AssetID)
	if !errors.Is(err, projectapp.ErrAssetNotFound) || fixture.asset(t, ctx, e2eAssetID).Prompt != "original prompt" {
		t.Fatalf("extraction Undo did not remove only the newly created Asset: %v", err)
	}
}

func TestStoryboardChangeSetEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	fixture := newE2EFixture(t, func(token agentmcp.Credential, _ string) agentapp.Runtime {
		return newScriptedRuntime(token.Value)
	})
	ctx := context.Background()
	sessionID := fixture.createSession(t, ctx)
	turnID := fixture.submitAndProcess(t, ctx, sessionID, "e2e-storyboard-input", "storyboard:create")
	applied := fixture.singleAppliedForTurn(t, ctx, sessionID, turnID)
	operations, err := fixture.changes.Operations(ctx, applied.ID, 0, 10)
	if err != nil || len(operations) != 3 || operations[0].SceneCreation == nil || operations[1].ClipCreation == nil || operations[2].ClipAssetRef == nil {
		t.Fatalf("Storyboard operations = %+v, %v", operations, err)
	}
	operationResponse := fixture.request(t, ctx, http.MethodGet, "/api/v1/change-sets/"+applied.ID.String()+"/operations?limit=10", "")
	if operationResponse.Code != http.StatusOK || !strings.Contains(operationResponse.Body.String(), `"scene_creation"`) || !strings.Contains(operationResponse.Body.String(), `"clip_asset_reference_creation"`) {
		t.Fatalf("Storyboard review DTO status=%d body=%s", operationResponse.Code, operationResponse.Body.String())
	}
	storyboard, err := fixture.projectStore.Storyboard(ctx, e2eEpisodeID)
	if err != nil || storyboard.Revision != 2 || len(storyboard.Scenes) != 1 || len(storyboard.Scenes[0].Clips) != 1 || len(storyboard.Scenes[0].Clips[0].AssetReferences) != 1 || storyboard.Scenes[0].Clips[0].AssetReferences[0].AssetID != e2eAssetID {
		t.Fatalf("applied Storyboard = %+v, %v", storyboard, err)
	}
	clip := storyboard.Scenes[0].Clips[0].Clip
	var promptSet struct {
		Families map[string]struct {
			Prompt          string `json:"prompt"`
			Profile         string `json:"profile"`
			GeneratorID     string `json:"generator_id"`
			ProviderModelID string `json:"provider_model_id"`
		} `json:"families"`
	}
	if json.Unmarshal([]byte(clip.VideoPrompts.Reference), &promptSet) != nil || promptSet.Families["h3"].Profile != "h3_ref2va@1" || promptSet.Families["h3"].GeneratorID != e2eVideoPromptContext().GeneratorID || promptSet.Families["h3"].ProviderModelID != e2eVideoPromptContext().ProviderModelID || !strings.Contains(promptSet.Families["h3"].Prompt, "subject_definitions") || clip.VideoPrompts.FirstImage != "" || clip.VideoPrompts.FirstLastImage != "" {
		t.Fatalf("public selected model context did not produce the expected isolated H3 variant: %+v", clip.VideoPrompts)
	}
	fixture.undo(t, ctx, applied.ID, "e2e-storyboard-undo")
	restored, err := fixture.projectStore.Storyboard(ctx, e2eEpisodeID)
	if err != nil || restored.Revision != 3 || len(restored.Scenes) != 0 {
		t.Fatalf("Storyboard Undo did not restore the empty Storyboard: %+v, %v", restored, err)
	}
}

// This opt-in test uses the installed Codex and one synthetic model Turn. It
// is intentionally separate because it consumes the user's Codex allowance.
func TestInstalledCodexPromptChangeSetEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_LIVE_AGENT") != "1" {
		t.Skip("set SHOTFORWARD_TEST_LIVE_AGENT=1 to run one local Codex Turn")
	}
	fixture := newE2EFixture(t, func(token agentmcp.Credential, workspace string) agentapp.Runtime {
		environment := append([]string(nil), os.Environ()...)
		environment = append(environment, token.EnvironmentVariable+"="+token.Value)
		runtime, err := codexruntime.NewSupervisor(codexruntime.Options{WorkingDirectory: workspace, Environment: environment,
			ClientVersion: "wp-07i-e2e", RequestTimeout: 90 * time.Second, ShutdownTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := runtime.Close(ctx); err != nil {
				t.Errorf("close Codex Runtime: %v", err)
			}
		})
		return runtime
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	snapshot, err := fixture.runtime.Snapshot(ctx)
	if err != nil || snapshot.Health != agentapp.RuntimeReady {
		t.Skipf("installed Codex is not ready: health=%s error=%v", snapshot.Health, err)
	}
	sessionID := fixture.createSession(t, ctx)
	turnID := fixture.submitAndProcess(t, ctx, sessionID, "live-e2e-input-1",
		"Use the ShotForward tools now. Read the only asset, create one ChangeSet that changes its Prompt exactly to 'live verified prompt', and finalize it to auto-apply. Do not merely explain the change.")
	fixture.singleAppliedForTurn(t, ctx, sessionID, turnID)
	if fixture.asset(t, ctx, e2eAssetID).Prompt != "live verified prompt" {
		t.Fatal("live ChangeSet was not atomically auto-applied")
	}
}

type runtimeFactory func(agentmcp.Credential, string) agentapp.Runtime

type e2eFixture struct {
	handler      *agenthttp.Handler
	runtime      agentapp.Runtime
	scheduler    *agentscheduler.TurnScheduler
	mcp          *agentmcp.Server
	events       *agentsse.Broker
	studioStore  *studio.Store
	projectStore projectapp.ProjectStore
	changes      *changesetapp.ReviewCatalog
	resolver     *projectstore.ApplicationChangeSetResolver
	clock        *e2eClock
	modelID      string
	sessions     *capturingSessions
}

func newE2EFixture(t *testing.T, makeRuntime runtimeFactory) *e2eFixture {
	t.Helper()
	ctx := context.Background()
	clock := &e2eClock{value: time.Now().UTC().Truncate(time.Millisecond)}
	studioStore, err := studio.Open(ctx, filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = studioStore.Close() })
	applicationAgentStore, err := studio.NewApplicationAgentStore(studioStore)
	if err != nil {
		t.Fatal(err)
	}

	registry := projectapp.NewProjectStoreRegistry()
	projectPath := filepath.Join(t.TempDir(), "project.db")
	projectStore, err := (projectstore.ApplicationStoreFactory{}).Create(ctx, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	initializeE2EProject(t, ctx, projectStore)
	if _, _, err := registry.Register(projectapp.ProjectHandle{ProjectID: e2eProjectID, RootPath: filepath.Dir(projectPath), Store: projectStore}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.CloseAll() })
	resolver, err := projectstore.NewApplicationChangeSetResolver(registry)
	if err != nil {
		t.Fatal(err)
	}
	changeService, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, resolver)
	if err != nil {
		t.Fatal(err)
	}
	review, err := changesetapp.NewReviewCatalog(changeService, resolver)
	if err != nil {
		t.Fatal(err)
	}
	events, err := agentsse.NewBroker(clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := agentmcp.NewServer(agentmcp.Options{Queries: e2eQueries{store: projectStore, projectID: e2eProjectID},
		Drafts: changeService, DraftCleanup: changeService, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mcp.Close(closeCtx); err != nil {
			t.Errorf("close MCP: %v", err)
		}
	})
	workspaceRoot := filepath.Join(t.TempDir(), "agent-workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := makeRuntime(mcp.Credential(), workspaceRoot)
	runtimeSnapshot, err := runtime.Snapshot(ctx)
	if err != nil && runtimeSnapshot.Health != agentapp.RuntimeLoginRequired {
		t.Fatal(err)
	}
	models, err := runtime.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modelID := ""
	for _, model := range models {
		if !model.Hidden {
			modelID = model.ID
			break
		}
	}
	if modelID == "" {
		t.Fatal("Runtime returned no visible model")
	}
	workspace, err := agentsession.NewIsolatedWorkspace(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	sessionService, err := agentsession.NewService(agentsession.Options{Store: applicationAgentStore, Changes: resolver, Runtime: runtime,
		MCP: mcp, Workspace: workspace, IDs: agentapp.NewSecureIDGenerator(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := agentscheduler.NewTurnScheduler(agentscheduler.SchedulerOptions{Store: studioStore, Runtime: runtime,
		MCP: mcp, IDs: agentapp.NewSecureIDGenerator(), Clock: clock, Events: events, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &capturingSessions{service: sessionService}
	handler, err := agenthttp.NewHandler(agenthttp.Options{Runtime: runtime, Sessions: sessions, Turns: scheduler, ChangeSets: review, Events: events,
		Security: agenthttp.Security{BearerToken: e2eBearer, CSRFToken: e2eCSRF, AllowedHosts: []string{e2eHost}, AllowedOrigins: []string{e2eOrigin}}})
	if err != nil {
		t.Fatal(err)
	}
	return &e2eFixture{handler: handler, runtime: runtime, scheduler: scheduler, mcp: mcp, events: events, studioStore: studioStore,
		projectStore: projectStore, changes: review, resolver: resolver, clock: clock, modelID: modelID, sessions: sessions}
}

func initializeE2EProject(t *testing.T, ctx context.Context, store projectapp.ProjectStore) {
	t.Helper()
	output, err := project.NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	value := project.Project{ID: e2eProjectID, Name: "WP-07I Project", Output: output, Revision: 1, CreatedAt: e2eBaseTime, UpdatedAt: e2eBaseTime}
	first := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: e2eEpisodeID, ProjectID: e2eProjectID, SequenceNumber: 1,
		Title: "Episode One", TargetDuration: time.Minute, Revision: 1, CreatedAt: e2eBaseTime, UpdatedAt: e2eBaseTime},
		Script: project.Script{Markdown: "## Rooftop\nThe hero finds an old brass key in the rain.", Revision: 1, UpdatedAt: e2eBaseTime}}
	if err := store.Initialize(ctx, value, first); err != nil {
		t.Fatal(err)
	}
	secondAt := e2eBaseTime.Add(time.Second)
	second := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: e2eOtherEpisode, ProjectID: e2eProjectID, SequenceNumber: 2,
		Title: "Episode Two", TargetDuration: time.Minute, Revision: 1, CreatedAt: secondAt, UpdatedAt: secondAt},
		Script: project.Script{Revision: 1, UpdatedAt: secondAt}}
	if err := store.CreateEpisode(ctx, second, secondAt); err != nil {
		t.Fatal(err)
	}
	createE2EAsset(t, ctx, store, e2eAssetID, "asi_AAAAAAAAAAAAAAAAAAAAAAAAAA", e2eEpisodeID, "Hero", "original prompt", e2eBaseTime.Add(2*time.Second))
	createE2EAsset(t, ctx, store, e2eOtherAssetID, "asi_BBBBBBBBBBBBBBBBBBBBBBBBBB", e2eOtherEpisode, "Other", "foreign prompt", e2eBaseTime.Add(3*time.Second))
}

func createE2EAsset(t *testing.T, ctx context.Context, store projectapp.ProjectStore, id project.AssetID, identityID project.AssetIdentityID, episodeID project.EpisodeID, name, prompt string, at time.Time) {
	t.Helper()
	identity := project.AssetIdentity{ID: identityID, ProjectID: e2eProjectID, Kind: project.AssetKindCharacter, Name: name, Revision: 1, CreatedAt: at, UpdatedAt: at}
	asset := project.Asset{ID: id, ProjectID: e2eProjectID, EpisodeID: episodeID, IdentityID: identityID, Prompt: prompt, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
		t.Fatal(err)
	}
}

func (f *e2eFixture) createSession(t *testing.T, ctx context.Context) agent.SessionID {
	t.Helper()
	response := f.request(t, ctx, http.MethodPost, "/api/v1/projects/"+e2eProjectID.String()+"/episodes/"+e2eEpisodeID.String()+"/agent-sessions",
		fmt.Sprintf(`{"model_id":%q,"web_search_disabled":true}`, f.modelID))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Session status=%d error=%v body=%s", response.Code, f.sessions.last(), response.Body.String())
	}
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id, err := agent.ParseSessionID(body.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type capturingSessions struct {
	mutex   sync.Mutex
	service *agentsession.Service
	err     error
}

func (s *capturingSessions) List(ctx context.Context, projectID project.ProjectID, episodeID project.EpisodeID, archived bool) ([]agent.AgentSession, error) {
	return s.service.List(ctx, projectID, episodeID, archived)
}
func (s *capturingSessions) Create(ctx context.Context, input agentapp.CreateSessionInput) (agent.AgentSession, error) {
	value, err := s.service.Create(ctx, input)
	s.mutex.Lock()
	s.err = err
	s.mutex.Unlock()
	return value, err
}
func (s *capturingSessions) Snapshot(ctx context.Context, id agent.SessionID) (agentapp.SessionSnapshot, error) {
	return s.service.Snapshot(ctx, id)
}
func (s *capturingSessions) Conversation(ctx context.Context, id agent.SessionID, cursor string, limit int) (agentapp.ConversationPage, error) {
	return s.service.Conversation(ctx, id, cursor, limit)
}
func (s *capturingSessions) Archive(ctx context.Context, id agent.SessionID, revision int64) (agent.AgentSession, error) {
	return s.service.Archive(ctx, id, revision)
}
func (s *capturingSessions) last() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.err
}

func (f *e2eFixture) submit(t *testing.T, ctx context.Context, sessionID agent.SessionID, operationID, text string) agent.TurnID {
	t.Helper()
	input := map[string]any{"client_operation_id": operationID, "model_id": f.modelID, "text": text, "focus": map[string]any{"asset_id": e2eAssetID.String()}}
	if strings.HasPrefix(text, "storyboard:") {
		input["video_prompt_target"] = e2eVideoPromptContext()
	}
	payload, _ := json.Marshal(input)
	response := f.request(t, ctx, http.MethodPost, "/api/v1/agent-sessions/"+sessionID.String()+"/inputs", string(payload))
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit input status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id, err := agent.ParseTurnID(body.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *e2eFixture) submitAndProcess(t *testing.T, ctx context.Context, sessionID agent.SessionID, operationID, text string) agent.TurnID {
	t.Helper()
	id := f.submit(t, ctx, sessionID, operationID, text)
	processed, err := f.scheduler.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("process Turn = %v, %v", processed, err)
	}
	turn, err := f.studioStore.AgentTurn(ctx, id)
	if err != nil || turn.Status != agent.TurnCompleted {
		failureCode, failureSummary := "", ""
		if turn.Failure != nil {
			failureCode, failureSummary = turn.Failure.Code, turn.Failure.Summary
		}
		t.Fatalf("completed Turn status=%s failure=%s/%s error=%v", turn.Status, failureCode, failureSummary, err)
	}
	return id
}

func (f *e2eFixture) singleAppliedForTurn(t *testing.T, ctx context.Context, sessionID agent.SessionID, turnID agent.TurnID) changeset.ChangeSet {
	t.Helper()
	values, err := f.resolver.ChangeSetsForAgentSession(ctx, e2eProjectID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var applied []changeset.ChangeSet
	for _, value := range values {
		if value.Status == changeset.StatusPending {
			t.Fatalf("auto-finalize left a pending ChangeSet: %s", value.ID)
		}
		if value.Source.AgentTurnID == turnID && value.Status == changeset.StatusApplied && value.RevertsChangeSetID == "" {
			applied = append(applied, value)
		}
	}
	if len(applied) != 1 {
		t.Fatalf("original applied ChangeSet count for Turn %s = %d", turnID, len(applied))
	}
	return applied[0]
}

func (f *e2eFixture) undo(t *testing.T, ctx context.Context, id changeset.ChangeSetID, operationKey string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"operation_key": operationKey})
	response := f.request(t, ctx, http.MethodPost, "/api/v1/change-sets/"+id.String()+":undo", string(payload))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"applied"`) {
		t.Fatalf("Undo status=%d body=%s", response.Code, response.Body.String())
	}
}

func e2eVideoPromptContext() *agentmcp.VideoPromptContext {
	return &agentmcp.VideoPromptContext{GeneratorID: "video.h3.shotforward", ProviderModelID: "MiniMax-H3-Turbo-Ref2VA", KeyframeProviderModelID: "MiniMax-H3-Turbo-FL2VA"}
}

func (f *e2eFixture) asset(t *testing.T, ctx context.Context, id project.AssetID) project.Asset {
	t.Helper()
	value, err := f.projectStore.Asset(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return value.Asset
}

func (f *e2eFixture) request(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, "http://"+e2eHost+path, reader).WithContext(ctx)
	request.RemoteAddr, request.Host = "127.0.0.1:54321", e2eHost
	request.Header.Set("Origin", e2eOrigin)
	request.Header.Set("Authorization", "Bearer "+e2eBearer)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", e2eCSRF)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *e2eFixture) assertEventSequence(t *testing.T, ctx context.Context, sessionID agent.SessionID, cursor string, expected []agentapp.StudioEventType) {
	t.Helper()
	streamCtx, cancelContext := context.WithCancel(ctx)
	events, cancel, err := f.events.Subscribe(streamCtx, sessionID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	var values []agentapp.StudioEventType
	for len(values) < 100 {
		select {
		case event := <-events:
			values = append(values, event.Value.Type)
			if event.Value.Type == agentapp.EventTurnCompleted {
				cancel()
				cancelContext()
				if containsSequence(values, expected) {
					return
				}
				t.Fatalf("Studio event sequence = %v", values)
			}
		case <-time.After(time.Second):
			cancel()
			cancelContext()
			t.Fatalf("timed out reading Studio events: %v", values)
		}
	}
	t.Fatal("Studio event sequence exceeded bound")
}

func containsSequence(values, expected []agentapp.StudioEventType) bool {
	position := 0
	for _, value := range values {
		if position < len(expected) && value == expected[position] {
			position++
		}
	}
	return position == len(expected)
}

func (f *e2eFixture) assertMCPBoundary(t *testing.T, ctx context.Context, sessionID agent.SessionID) {
	t.Helper()
	binding, err := f.studioStore.AgentMCPBinding(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := f.mcp.EnsureBinding(ctx, agentmcp.BindingScope{RouteKey: binding.RouteKey, SessionID: sessionID, ProjectID: e2eProjectID, EpisodeID: e2eEpisodeID})
	if err != nil {
		t.Fatal(err)
	}
	syntheticTurn := agent.TurnID("agt_ZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	if err := f.mcp.ActivateTurn(sessionID, agentmcp.ActiveTurn{AgentTurnID: syntheticTurn, CodexThreadID: "boundary-thread", CodexTurnID: "boundary-turn"}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.mcp.EndTurn(ctx, sessionID, syntheticTurn) }()
	credential := f.mcp.Credential()
	tools := mcpRPC(t, endpoint, credential.Value, "tools/list", map[string]any{})
	encoded, _ := json.Marshal(tools)
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(encoded, &listed); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	joinedNames := strings.ToLower(strings.Join(names, ","))
	for _, prohibited := range []string{"generation", "apply_change_set", "reject_change_set", "shell", "file"} {
		if strings.Contains(joinedNames, prohibited) {
			t.Fatalf("prohibited MCP capability %q was exposed", prohibited)
		}
	}
	foreign := mcpTool(t, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": e2eOtherAssetID.String()})
	if !foreign.IsError || !strings.Contains(foreign.Text, "object_outside_scope") {
		t.Fatalf("cross-Episode read was not rejected: error=%v", foreign.IsError)
	}
	intent := mcpTool(t, endpoint, credential.Value, "declare_turn_intent", map[string]any{"intent": "project_change", "target": "storyboard"})
	if intent.IsError {
		t.Fatalf("declare boundary intent failed: %+v", intent)
	}
	begin := mcpTool(t, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "Boundary Storyboard", "operation_key": "boundary-storyboard-begin"})
	var draft struct {
		ChangeSetID string `json:"change_set_id"`
		Revision    int64  `json:"revision"`
	}
	if begin.IsError || json.Unmarshal([]byte(begin.Text), &draft) != nil {
		t.Fatalf("boundary ChangeSet begin failed: %+v", begin)
	}
	foreignProposal := mcpTool(t, endpoint, credential.Value, "append_storyboard_operations", map[string]any{
		"change_set_id": draft.ChangeSetID, "expected_change_set_revision": draft.Revision, "operation_key": "boundary-storyboard-append",
		"operations": []map[string]any{
			{"type": "create_scene", "ref": "boundary-scene", "title": "Boundary Scene"},
			{"type": "create_clip", "ref": "foreign", "scene_ref": "boundary-scene", "title": "Foreign Asset Clip", "planned_duration_seconds": 6,
				"generation_mode": "reference", "generator_id": e2eVideoPromptContext().GeneratorID, "provider_model_id": e2eVideoPromptContext().ProviderModelID, "video_prompt": "@Other waits."},
			{"type": "add_clip_asset_reference", "clip_ref": "foreign", "asset_id": e2eOtherAssetID.String()},
		},
	})
	if !foreignProposal.IsError || !strings.Contains(foreignProposal.Text, "object_outside_scope") {
		t.Fatalf("cross-Episode Storyboard Asset proposal was not rejected: %+v", foreignProposal)
	}
	unknown := mcpTool(t, endpoint, credential.Value, "start_generation_job", map[string]any{})
	if !unknown.IsError || !strings.Contains(unknown.Text, "unknown_tool") {
		t.Fatalf("Generation tool call was not rejected: error=%v", unknown.IsError)
	}
}

type e2eClock struct {
	mutex sync.Mutex
	value time.Time
}

func (c *e2eClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.value = c.value.Add(time.Millisecond)
	return c.value
}

type e2eQueries struct {
	store     projectapp.ProjectStore
	projectID project.ProjectID
}

func (q e2eQueries) GetEpisode(ctx context.Context, input projectapp.GetEpisodeInput) (project.Episode, error) {
	if input.ProjectID != q.projectID {
		return project.Episode{}, projectapp.ErrEpisodeNotFound
	}
	return q.store.Episode(ctx, input.EpisodeID)
}
func (q e2eQueries) ListEpisodeAssets(ctx context.Context, input projectapp.ListEpisodeAssetsInput) ([]project.EpisodeAsset, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrEpisodeNotFound
	}
	return q.store.EpisodeAssets(ctx, input.EpisodeID, input.IncludeDeleted)
}
func (q e2eQueries) FindPriorAssetCandidates(context.Context, projectapp.FindPriorAssetCandidatesInput) ([]projectapp.PriorAssetCandidate, error) {
	return nil, nil
}
func (q e2eQueries) GetAsset(ctx context.Context, input projectapp.GetAssetInput) (project.EpisodeAsset, error) {
	if input.ProjectID != q.projectID {
		return project.EpisodeAsset{}, projectapp.ErrAssetNotFound
	}
	return q.store.Asset(ctx, input.AssetID)
}
func (q e2eQueries) GetClip(ctx context.Context, input projectapp.GetClipInput) (project.ClipDetails, error) {
	if input.ProjectID != q.projectID {
		return project.ClipDetails{}, projectapp.ErrClipNotFound
	}
	return q.store.Clip(ctx, input.ClipID)
}

func (q e2eQueries) GetStoryboard(ctx context.Context, input projectapp.GetStoryboardInput) (projectapp.StoryboardResult, error) {
	if input.ProjectID != q.projectID {
		return projectapp.StoryboardResult{}, projectapp.ErrEpisodeNotFound
	}
	storyboard, err := q.store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return projectapp.StoryboardResult{}, err
	}
	episode, err := q.store.Episode(ctx, input.EpisodeID)
	if err != nil {
		return projectapp.StoryboardResult{}, err
	}
	return projectapp.StoryboardResult{Storyboard: storyboard, TargetDuration: episode.TargetDuration}, nil
}

func (q e2eQueries) GetMediaSlot(ctx context.Context, input projectapp.GetMediaSlotInput) (project.MediaSlotDetails, error) {
	if input.ProjectID != q.projectID {
		return project.MediaSlotDetails{}, projectapp.ErrMediaSlotNotFound
	}
	return q.store.MediaSlot(ctx, input.MediaSlotID, input.IncludeDeleted)
}

func (q e2eQueries) ListClipReferenceMedia(ctx context.Context, input projectapp.ListClipReferenceMediaInput) ([]project.Artifact, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrClipNotFound
	}
	return q.store.ClipReferenceArtifacts(ctx, input.ClipID)
}

func (q e2eQueries) ListEpisodeReferenceMedia(ctx context.Context, input projectapp.ListEpisodeReferenceMediaInput) ([]project.EpisodeReferenceMedia, error) {
	if input.ProjectID != q.projectID {
		return nil, projectapp.ErrEpisodeNotFound
	}
	return q.store.EpisodeReferenceMediaList(ctx, input.EpisodeID, input.IncludeDeleted)
}

type scriptedRuntime struct {
	mutex      sync.Mutex
	token      string
	threads    map[string]agentapp.ThreadRequest
	events     map[string][]chan agentapp.RuntimeEvent
	history    map[string][]agentapp.RuntimeTurnSnapshot
	nextThread int
	nextTurn   int
}

func newScriptedRuntime(token string) *scriptedRuntime {
	return &scriptedRuntime{token: token, threads: make(map[string]agentapp.ThreadRequest), events: make(map[string][]chan agentapp.RuntimeEvent), history: make(map[string][]agentapp.RuntimeTurnSnapshot)}
}
func (r *scriptedRuntime) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady, Version: "scripted-e2e"}, nil
}
func (r *scriptedRuntime) Models(context.Context) ([]agentapp.Model, error) {
	return []agentapp.Model{{ID: "scripted", DisplayName: "Scripted E2E"}}, nil
}
func (r *scriptedRuntime) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, errors.New("login is not supported")
}
func (r *scriptedRuntime) StartThread(_ context.Context, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	result := mcpToolRaw(request.MCPBindingURL, r.token, "get_session_context", map[string]any{})
	if result.err != nil || result.value.IsError {
		return agentapp.Thread{}, errors.New("scripted Runtime could not verify MCP scope")
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.nextThread++
	id := fmt.Sprintf("scripted-thread-%d", r.nextThread)
	r.threads[id] = request
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: "scripted-session-" + fmt.Sprint(r.nextThread), Title: "E2E Session", ModelID: request.ModelID}, nil
}
func (r *scriptedRuntime) ResumeThread(_ context.Context, id string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	r.mutex.Lock()
	existing, ok := r.threads[id]
	r.mutex.Unlock()
	if !ok || existing.AgentSessionID != request.AgentSessionID || existing.ProjectID != request.ProjectID || existing.EpisodeID != request.EpisodeID {
		return agentapp.Thread{}, errors.New("scripted Runtime scope mismatch")
	}
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: "scripted-session-1", ModelID: request.ModelID}, nil
}
func (r *scriptedRuntime) ReadThread(_ context.Context, id string) (agentapp.Thread, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	request, ok := r.threads[id]
	if !ok {
		return agentapp.Thread{}, errors.New("Thread not found")
	}
	return agentapp.Thread{CodexThreadID: id, CodexSessionID: "scripted-session-1", ModelID: request.ModelID, Turns: append([]agentapp.RuntimeTurnSnapshot(nil), r.history[id]...)}, nil
}
func (r *scriptedRuntime) ListConversation(_ context.Context, id, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return agentapp.RuntimeConversationPage{}, nil
}
func (r *scriptedRuntime) StartTurn(_ context.Context, request agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	r.mutex.Lock()
	r.nextTurn++
	id := fmt.Sprintf("scripted-turn-%d", r.nextTurn)
	thread := r.threads[request.CodexThreadID]
	r.mutex.Unlock()
	// The public submission path injects selected-model rules into the Runtime
	// request, while this fake executes only its tiny synthetic user command.
	prompt := strings.TrimPrefix(request.Text, e2eVideoPromptContext().InstructionText()+"\n\n")
	go r.execute(request.CodexThreadID, id, thread.MCPBindingURL, prompt)
	return agentapp.RuntimeTurn{CodexTurnID: id}, nil
}
func (r *scriptedRuntime) SteerTurn(context.Context, string, string, string) error { return nil }
func (r *scriptedRuntime) InterruptTurn(context.Context, string, string) error     { return nil }
func (r *scriptedRuntime) Subscribe(ctx context.Context, threadID string) (<-chan agentapp.RuntimeEvent, error) {
	result := make(chan agentapp.RuntimeEvent, 64)
	r.mutex.Lock()
	r.events[threadID] = append(r.events[threadID], result)
	r.mutex.Unlock()
	go func() { <-ctx.Done() }()
	return result, nil
}
func (r *scriptedRuntime) execute(threadID, turnID, endpoint, prompt string) {
	if strings.HasPrefix(prompt, "extract:") {
		r.executeExtraction(threadID, turnID, endpoint)
		return
	}
	if strings.HasPrefix(prompt, "storyboard:") {
		r.executeStoryboard(threadID, turnID, endpoint)
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	var read mcpToolResult
	for time.Now().Before(deadline) {
		result := mcpToolRaw(endpoint, r.token, "get_asset", map[string]any{"asset_id": e2eAssetID.String()})
		if result.err == nil && !result.value.IsError {
			read = result.value
			break
		}
		time.Sleep(time.Millisecond)
	}
	if read.Text == "" {
		r.complete(threadID, turnID, agent.TurnFailed, "MCP Turn did not become active")
		return
	}
	r.toolEvent(threadID, turnID, "get_asset")
	if err := declareRuntimeProjectChange(endpoint, r.token, "prompts"); err != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "declare project change failed")
		return
	}
	begin := mcpToolRaw(endpoint, r.token, "begin_change_set", map[string]any{"title": "E2E Prompt proposal", "operation_key": "begin-" + turnID})
	if begin.err != nil || begin.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "begin ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "begin_change_set")
	var identity struct {
		ChangeSetID string `json:"change_set_id"`
		Revision    int64  `json:"revision"`
	}
	if json.Unmarshal([]byte(begin.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "invalid begin result")
		return
	}
	appendResult := mcpToolRaw(endpoint, r.token, "append_prompt_operations", map[string]any{
		"change_set_id": identity.ChangeSetID, "expected_change_set_revision": identity.Revision, "operation_key": "append-" + turnID,
		"operations": []map[string]any{{"type": "update_asset_prompt", "asset_id": e2eAssetID.String(), "new_prompt": prompt}},
	})
	if appendResult.err != nil || appendResult.value.IsError || json.Unmarshal([]byte(appendResult.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "append ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "append_prompt_operations")
	finalize := mcpToolRaw(endpoint, r.token, "finalize_change_set", map[string]any{"change_set_id": identity.ChangeSetID,
		"expected_change_set_revision": identity.Revision, "operation_key": "finalize-" + turnID})
	if finalize.err != nil || finalize.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "finalize ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "finalize_change_set")
	r.emit(threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: threadID, CodexTurnID: turnID, TextDelta: "Proposal ready"})
	r.complete(threadID, turnID, agent.TurnCompleted, "")
}

func (r *scriptedRuntime) executeStoryboard(threadID, turnID, endpoint string) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		read := mcpToolRaw(endpoint, r.token, "read_script", map[string]any{})
		if read.err == nil && !read.value.IsError {
			r.toolEvent(threadID, turnID, "read_script")
			break
		}
		time.Sleep(time.Millisecond)
	}
	for name, arguments := range map[string]map[string]any{"list_scenes": {}, "get_asset": {"asset_id": e2eAssetID.String()}} {
		result := mcpToolRaw(endpoint, r.token, name, arguments)
		if result.err != nil || result.value.IsError {
			r.complete(threadID, turnID, agent.TurnFailed, name+" failed")
			return
		}
		r.toolEvent(threadID, turnID, name)
	}
	modelContext := mcpToolRaw(endpoint, r.token, "get_episode_context", map[string]any{})
	var modelPayload struct {
		Targets map[string]struct {
			GeneratorID     string `json:"generator_id"`
			ProviderModelID string `json:"provider_model_id"`
			Profile         string `json:"prompt_profile"`
			Instructions    string `json:"instructions"`
		} `json:"video_prompt_targets"`
	}
	if modelContext.err != nil || modelContext.value.IsError || json.Unmarshal([]byte(modelContext.value.Text), &modelPayload) != nil || modelPayload.Targets["reference"].Profile != "h3_ref2va@1" || !strings.Contains(modelPayload.Targets["reference"].Instructions, "subject_definitions") {
		r.complete(threadID, turnID, agent.TurnFailed, "selected model context failed: "+modelContext.value.Text)
		return
	}
	r.toolEvent(threadID, turnID, "get_episode_context")
	target := modelPayload.Targets["reference"]
	if err := declareRuntimeProjectChange(endpoint, r.token, "storyboard"); err != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "declare Storyboard change failed")
		return
	}
	begin := mcpToolRaw(endpoint, r.token, "begin_change_set", map[string]any{"title": "Create Storyboard", "operation_key": "storyboard-begin-" + turnID})
	if begin.err != nil || begin.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "begin Storyboard ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "begin_change_set")
	var identity struct {
		ChangeSetID string `json:"change_set_id"`
		Revision    int64  `json:"revision"`
	}
	if json.Unmarshal([]byte(begin.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "invalid Storyboard begin result")
		return
	}
	appendResult := mcpToolRaw(endpoint, r.token, "append_storyboard_operations", map[string]any{
		"change_set_id": identity.ChangeSetID, "expected_change_set_revision": identity.Revision, "operation_key": "storyboard-append-" + turnID,
		"operations": []map[string]any{
			{"type": "create_scene", "ref": "roof", "title": "Rooftop", "source": map[string]any{"start_byte": 0, "end_byte": 10}},
			{"type": "create_clip", "ref": "key", "scene_ref": "roof", "title": "Find the key", "planned_duration_seconds": 6, "generation_mode": "reference", "generator_id": target.GeneratorID, "provider_model_id": target.ProviderModelID,
				"video_prompt": "subject_definitions:\n<Subject 1>: @Hero\nsummary:\nThe hero finds a brass key on the rooftop.\nretention_analysis:\nPreserve @Hero identity.\ndetailed_description:\n<Subject 1> bends and picks up the key as the camera slowly pushes in.\noverall_soundscape:\nRain and quiet rooftop ambience.\nnon_diegetic_music:\nNone."},
			{"type": "add_clip_asset_reference", "clip_ref": "key", "asset_id": e2eAssetID.String()},
		},
	})
	if appendResult.err != nil || appendResult.value.IsError || json.Unmarshal([]byte(appendResult.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, fmt.Sprintf("append Storyboard ChangeSet failed: err=%v tool=%s", appendResult.err, appendResult.value.Text))
		return
	}
	r.toolEvent(threadID, turnID, "append_storyboard_operations")
	finalize := mcpToolRaw(endpoint, r.token, "finalize_change_set", map[string]any{"change_set_id": identity.ChangeSetID, "expected_change_set_revision": identity.Revision, "operation_key": "storyboard-finalize-" + turnID})
	if finalize.err != nil || finalize.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "finalize Storyboard ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "finalize_change_set")
	r.emit(threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: threadID, CodexTurnID: turnID, TextDelta: "Storyboard proposal ready"})
	r.complete(threadID, turnID, agent.TurnCompleted, "")
}

func (r *scriptedRuntime) executeExtraction(threadID, turnID, endpoint string) {
	deadline := time.Now().Add(2 * time.Second)
	scriptRead := false
	for time.Now().Before(deadline) {
		read := mcpToolRaw(endpoint, r.token, "read_script", map[string]any{})
		if read.err == nil && !read.value.IsError {
			r.toolEvent(threadID, turnID, "read_script")
			scriptRead = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !scriptRead {
		r.complete(threadID, turnID, agent.TurnFailed, "read script failed")
		return
	}
	assets := mcpToolRaw(endpoint, r.token, "list_assets", map[string]any{})
	if assets.err != nil || assets.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "list Assets failed")
		return
	}
	r.toolEvent(threadID, turnID, "list_assets")
	if err := declareRuntimeProjectChange(endpoint, r.token, "assets"); err != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "declare Asset change failed")
		return
	}
	begin := mcpToolRaw(endpoint, r.token, "begin_change_set", map[string]any{"title": "Extract missing Assets", "operation_key": "extract-begin-" + turnID})
	if begin.err != nil || begin.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "begin extraction ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "begin_change_set")
	var identity struct {
		ChangeSetID string `json:"change_set_id"`
		Revision    int64  `json:"revision"`
	}
	if json.Unmarshal([]byte(begin.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "invalid extraction begin result")
		return
	}
	appendResult := mcpToolRaw(endpoint, r.token, "append_asset_operations", map[string]any{
		"change_set_id": identity.ChangeSetID, "expected_change_set_revision": identity.Revision, "operation_key": "extract-append-" + turnID,
		"operations": []map[string]any{{"kind": "prop", "name": "Brass Key", "prompt": "aged brass key, cinematic macro detail"}},
	})
	if appendResult.err != nil || appendResult.value.IsError || json.Unmarshal([]byte(appendResult.value.Text), &identity) != nil {
		r.complete(threadID, turnID, agent.TurnFailed, "append extraction ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "append_asset_operations")
	finalize := mcpToolRaw(endpoint, r.token, "finalize_change_set", map[string]any{"change_set_id": identity.ChangeSetID, "expected_change_set_revision": identity.Revision, "operation_key": "extract-finalize-" + turnID})
	if finalize.err != nil || finalize.value.IsError {
		r.complete(threadID, turnID, agent.TurnFailed, "finalize extraction ChangeSet failed")
		return
	}
	r.toolEvent(threadID, turnID, "finalize_change_set")
	r.emit(threadID, agentapp.RuntimeEvent{Type: agentapp.RuntimeAssistantDelta, CodexThreadID: threadID, CodexTurnID: turnID, TextDelta: "Asset proposal ready"})
	r.complete(threadID, turnID, agent.TurnCompleted, "")
}
func (r *scriptedRuntime) toolEvent(threadID, turnID, name string) {
	for _, kind := range []agentapp.RuntimeEventType{agentapp.RuntimeToolStarted, agentapp.RuntimeToolCompleted} {
		r.emit(threadID, agentapp.RuntimeEvent{Type: kind, CodexThreadID: threadID, CodexTurnID: turnID, ToolName: "shotforward/" + name})
	}
}
func (r *scriptedRuntime) complete(threadID, turnID string, status agent.TurnStatus, summary string) {
	r.mutex.Lock()
	r.history[threadID] = append(r.history[threadID], agentapp.RuntimeTurnSnapshot{CodexTurnID: turnID, Status: status})
	r.mutex.Unlock()
	event := agentapp.RuntimeEvent{Type: agentapp.RuntimeTurnCompleted, CodexThreadID: threadID, CodexTurnID: turnID, TurnStatus: status}
	if status == agent.TurnFailed {
		event.FailureCode, event.FailureSummary = "scripted_failure", summary
	}
	r.emit(threadID, event)
}
func (r *scriptedRuntime) emit(threadID string, event agentapp.RuntimeEvent) {
	r.mutex.Lock()
	targets := append([]chan agentapp.RuntimeEvent(nil), r.events[threadID]...)
	r.mutex.Unlock()
	for _, target := range targets {
		target <- event
	}
}

type mcpToolResult struct {
	Text    string
	IsError bool
}
type rawMCPResult struct {
	value mcpToolResult
	err   error
}

func mcpToolRaw(endpoint, token, name string, arguments map[string]any) rawMCPResult {
	value, err := mcpRPCRequest(endpoint, token, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return rawMCPResult{err: err}
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	encoded, _ := json.Marshal(value)
	if err := json.Unmarshal(encoded, &result); err != nil || len(result.Content) == 0 {
		return rawMCPResult{err: errors.New("invalid MCP tool response")}
	}
	return rawMCPResult{value: mcpToolResult{Text: result.Content[0].Text, IsError: result.IsError}}
}

func declareRuntimeProjectChange(endpoint, token, target string) error {
	result := mcpToolRaw(endpoint, token, "declare_turn_intent", map[string]any{"intent": "project_change", "target": target})
	if result.err != nil {
		return result.err
	}
	if result.value.IsError {
		return errors.New("project change intent was rejected")
	}
	return nil
}

func mcpTool(t *testing.T, endpoint, token, name string, arguments map[string]any) mcpToolResult {
	t.Helper()
	result := mcpToolRaw(endpoint, token, name, arguments)
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.value
}

func mcpRPC(t *testing.T, endpoint, token, method string, parameters map[string]any) any {
	t.Helper()
	value, err := mcpRPCRequest(endpoint, token, method, parameters)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mcpRPCRequest(endpoint, token, method string, parameters map[string]any) (any, error) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": parameters})
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var envelope struct {
		Result any `json:"result"`
		Error  any `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, agentmcp.MaxToolResultBytes+4096)).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("MCP RPC failed")
	}
	return envelope.Result, nil
}
