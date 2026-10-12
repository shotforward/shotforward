package agentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

func TestMCPUploadedAudioAdaptationAppliesAndUndoesThroughRealServices(t *testing.T) {
	t.Parallel()
	for _, runtimeKind := range []agentapp.RuntimeKind{agentapp.RuntimeUserCodex, agentapp.RuntimeShotForward} {
		t.Run(string(runtimeKind), func(t *testing.T) {
			t.Parallel()
			testMCPUploadedAudioAdaptation(t, runtimeKind)
		})
	}
}

func testMCPUploadedAudioAdaptation(t *testing.T, runtimeKind agentapp.RuntimeKind) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := (projectstore.ApplicationStoreFactory{}).Create(ctx, filepath.Join(root, "project.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &integrationClock{at: time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()
	output, _ := project.NewOutputSpec(720, 1280)
	projectValue := project.Project{ID: testProjectID, Name: "音频适配集成测试", Output: output, Revision: 1, CreatedAt: now, UpdatedAt: now}
	episode := project.Episode{EpisodeMetadata: project.EpisodeMetadata{ID: testEpisodeID, ProjectID: projectValue.ID, SequenceNumber: 1, Title: "当前视频", TargetDuration: time.Minute, Revision: 1, CreatedAt: now, UpdatedAt: now}, Script: project.Script{Revision: 1, UpdatedAt: now}}
	if err := store.Initialize(ctx, projectValue, episode); err != nil {
		t.Fatal(err)
	}
	otherEpisode := episode
	otherEpisode.ID = project.EpisodeID("ep_" + strings.Repeat("O", 26))
	otherEpisode.SequenceNumber = 2
	otherEpisode.Title = "其他视频"
	if err := store.CreateEpisode(ctx, otherEpisode, clock.Now()); err != nil {
		t.Fatal(err)
	}

	at := clock.Now()
	scene := project.Scene{ID: project.SceneID("scn_" + strings.Repeat("M", 26)), ProjectID: projectValue.ID, EpisodeID: episode.ID, Position: project.StoryboardOrderStep, Title: "对白场景", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = clock.Now()
	clip := project.Clip{
		ID: testClipID, ProjectID: projectValue.ID, EpisodeID: episode.ID, SceneID: scene.ID, Position: project.StoryboardOrderStep,
		Title: "四个视觉参考和布布音频", PlannedDurationSeconds: 8, GenerationMode: project.ClipGenerationReference,
		FirstImagePrompt: "original first frame", LastImagePrompt: "original last frame",
		VideoPrompts: project.ClipVideoPrompts{FirstImage: "original first-image video", FirstLastImage: "original first-last-image video", Reference: "original reference video"},
		Revision:     1, CreatedAt: at, UpdatedAt: at,
	}
	wanTarget := videoPromptTarget{Family: "wan3", Profile: "wan3@1", GeneratorID: string(project.GeneratorVideoWanOfficial), ProviderModelID: "wan3.0-video-prime"}
	clip.VideoPrompts.Reference, err = parseVideoPromptSet(clip.VideoPrompts.Reference).withVariant(wanTarget, wanTarget.GeneratorID, wanTarget.ProviderModelID, "independent Wan prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	assetNames := []string{}
	for index, suffix := range []string{"A", "B", "C", "D"} {
		at := clock.Now()
		identity := project.AssetIdentity{ID: project.AssetIdentityID("asi_" + strings.Repeat(suffix, 26)), ProjectID: projectValue.ID, Kind: project.AssetKindCharacter, Name: "视觉" + suffix, Revision: 1, CreatedAt: at, UpdatedAt: at}
		asset := project.Asset{ID: project.AssetID("ast_" + strings.Repeat(suffix, 26)), ProjectID: projectValue.ID, EpisodeID: episode.ID, IdentityID: identity.ID, Prompt: "视觉身份参考" + suffix, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
			t.Fatal(err)
		}
		slotID, err := project.FixedMediaSlotID(asset.ID.String(), project.MediaSlotAssetPrimaryImage)
		if err != nil {
			t.Fatal(err)
		}
		image := project.Artifact{ID: project.ArtifactID("art_" + strings.Repeat(suffix, 26)), ProjectID: projectValue.ID, Kind: project.MediaKindImage, RelativePath: "media/" + suffix + ".png", SHA256: strings.Repeat(strings.ToLower(suffix), 64), ByteSize: 128, MIMEType: "image/png", OriginalFilename: suffix + ".png", Width: 720, Height: 1280, CreatedAt: at}
		version := project.MediaVersion{ID: project.MediaVersionID("mvr_" + strings.Repeat(suffix, 26)), SlotID: slotID, ArtifactID: image.ID, SequenceNumber: 1, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if err := store.AddMediaVersion(ctx, image, version, 1, true, at); err != nil {
			t.Fatal(err)
		}
		if err := store.BindClipAsset(ctx, clip.ID, asset.ID, int64(index+1), clock.Now()); err != nil {
			t.Fatal(err)
		}
		assetNames = append(assetNames, "@"+identity.Name)
	}
	before, err := store.Clip(ctx, clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	at = clock.Now()
	audio := project.Artifact{ID: project.ArtifactID("art_" + strings.Repeat("U", 26)), ProjectID: projectValue.ID, Kind: project.MediaKindAudio, RelativePath: "media/audio/bubu.mp3", SHA256: strings.Repeat("e", 64), ByteSize: 128, MIMEType: "audio/mpeg", OriginalFilename: "bubu.mp3", DurationMilliseconds: 15_000, AudioSampleRate: 44_100, AudioChannels: 1, CreatedAt: at}
	// Project-wide content deduplication reuses this Artifact across Episodes.
	// Register the other Episode first to exercise the former ambiguous JOIN.
	for _, registration := range []struct {
		episodeID   project.EpisodeID
		name        string
		description string
	}{
		{otherEpisode.ID, "其他集的布布声音", "另一集独立的声音描述"},
		{episode.ID, "布布音频", "布布对白音色参考"},
	} {
		at := clock.Now()
		if err := store.PutEpisodeReferenceMedia(ctx, project.EpisodeReferenceMedia{EpisodeID: registration.episodeID, Artifact: audio, Name: registration.name, Description: registration.description, Revision: 1, CreatedAt: at, UpdatedAt: at}, at); err != nil {
			t.Fatal(err)
		}
	}
	at = clock.Now()
	otherAudio := audio
	otherAudio.ID = project.ArtifactID("art_" + strings.Repeat("V", 26))
	otherAudio.RelativePath, otherAudio.OriginalFilename = "media/audio/other-episode.mp3", "other-episode.mp3"
	otherAudio.SHA256, otherAudio.CreatedAt = strings.Repeat("f", 64), at
	if err := store.PutEpisodeReferenceMedia(ctx, project.EpisodeReferenceMedia{EpisodeID: otherEpisode.ID, Artifact: otherAudio, Name: "仅其他集的音频", Description: "不得出现在当前集的音频列表中", Revision: 1, CreatedAt: at, UpdatedAt: at}, at); err != nil {
		t.Fatal(err)
	}
	otherBefore, err := store.EpisodeReferenceMediaList(ctx, otherEpisode.ID, false)
	if err != nil || len(otherBefore) != 2 {
		t.Fatalf("other Episode reference library = %+v, %v; want two references", otherBefore, err)
	}

	registry := projectapp.NewProjectStoreRegistry()
	if _, _, err := registry.Register(projectapp.ProjectHandle{ProjectID: projectValue.ID, RootPath: root, Store: store}); err != nil {
		t.Fatal(err)
	}
	queries, err := projectapp.NewService(projectapp.Dependencies{IDs: projectapp.NewSecureIDGenerator(), Clock: clock, Workspace: platformpaths.ApplicationProjectWorkspace{}, Stores: projectstore.ApplicationStoreFactory{}, Library: unusedAudioIntegrationLibrary{}, Importer: scriptimport.FileImporter{}, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	assertOtherEpisodeUnchanged := func() {
		t.Helper()
		current, err := queries.ListEpisodeReferenceMedia(ctx, projectapp.ListEpisodeReferenceMediaInput{ProjectID: projectValue.ID, EpisodeID: otherEpisode.ID})
		if err != nil || !reflect.DeepEqual(current, otherBefore) {
			t.Fatalf("audio adaptation changed the other Episode library = %+v, %v", current, err)
		}
	}
	resolver, err := projectstore.NewApplicationChangeSetResolver(registry)
	if err != nil {
		t.Fatal(err)
	}
	changeSets, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, resolver)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{Queries: queries, Drafts: changeSets, DraftCleanup: changeSets, Random: bytes.NewReader(bytes.Repeat([]byte{0x53}, 512))})
	if err != nil {
		t.Fatal(err)
	}
	server.listener = staticListener{address: staticAddress("127.0.0.1:43123")}
	endpoint, err := server.RegisterBinding(ctx, BindingScope{RouteKey: testRoute, SessionID: testSessionID, ProjectID: projectValue.ID, EpisodeID: episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ActivateTurn(testSessionID, ActiveTurn{AgentTurnID: testTurnID, CodexThreadID: "thr_audio_integration", CodexTurnID: "turn_audio_integration", RuntimeKind: runtimeKind, Focus: agent.FocusSnapshot{ClipID: clip.ID}}); err != nil {
		t.Fatal(err)
	}
	token := server.Credential().Value
	declareProjectChange(t, server, endpoint, token, "prompts")
	listed := callTool(t, server, endpoint, token, "list_reference_media", map[string]any{"kind": "audio", "limit": 100})
	if listed.IsError {
		t.Fatalf("list_reference_media(kind=audio, limit=100) = %+v", listed)
	}
	var listedPayload struct {
		Items      []audioIntegrationListItem `json:"items"`
		NextCursor string                     `json:"next_cursor"`
		HasMore    bool                       `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(listed.Text), &listedPayload); err != nil {
		t.Fatal(err)
	}
	wantItems := []audioIntegrationListItem{{ArtifactID: audio.ID, Kind: project.MediaKindAudio, Name: "布布音频", Description: "布布对白音色参考", DurationMilliseconds: 15_000, MIMEType: "audio/mpeg", UsageCount: 0, Revision: 1}}
	if !reflect.DeepEqual(listedPayload.Items, wantItems) || listedPayload.HasMore || listedPayload.NextCursor != "" {
		t.Fatalf("list_reference_media leaked another Episode or incorrect metadata = %+v; want %+v", listedPayload, wantItems)
	}
	contextResult := callTool(t, server, endpoint, token, "get_video_prompt_adaptation_context", map[string]any{"clip_id": clip.ID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "MiniMax-H3-Turbo-Ref2VA"})
	if contextResult.IsError {
		t.Fatalf("get_video_prompt_adaptation_context = %+v", contextResult)
	}
	var adaptation struct {
		Source struct {
			Mode   string `json:"generation_mode"`
			Family string `json:"prompt_family"`
			Hash   string `json:"prompt_hash"`
		} `json:"source"`
		Hash       string `json:"reference_set_hash"`
		References []struct {
			Key   string `json:"reference_key"`
			Ready bool   `json:"media_ready"`
		} `json:"references"`
	}
	if err := json.Unmarshal([]byte(contextResult.Text), &adaptation); err != nil {
		t.Fatal(err)
	}
	if len(adaptation.References) != 4 {
		t.Fatalf("adaptation references = %+v; want four existing visual Assets", adaptation.References)
	}
	references := []map[string]any{}
	for _, reference := range adaptation.References {
		if !reference.Ready || !strings.HasPrefix(reference.Key, "asset:") {
			t.Fatalf("visual reference is not ready = %+v", reference)
		}
		references = append(references, map[string]any{"key": reference.Key, "purposes": []string{"identity"}})
	}
	references = append(references, map[string]any{"key": "upload:" + audio.ID.String(), "purposes": []string{"voice"}, "note": "Use as the character dialogue voice"})
	begin := callTool(t, server, endpoint, token, "begin_change_set", map[string]any{"title": "关联布布音频", "operation_key": "audio-integration-begin"})
	changeSetID, revision := changeSetIdentity(t, begin)
	prompt := "subject_definitions:\n<Subject 1>: " + strings.Join(assetNames, " ") + "\nsummary:\n人物说话。\nretention_analysis:\n保留视觉身份，@布布音频 仅参考音色。\ndetailed_description:\n[Shot 1] 人物使用 @布布音频 的音色说话。\noverall_soundscape:\n安静的室内环境。\nnon_diegetic_music:\nNone"
	appended := callTool(t, server, endpoint, token, "append_video_prompt_adaptation", map[string]any{
		"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "audio-integration-append",
		"clip_id": clip.ID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "MiniMax-H3-Turbo-Ref2VA",
		"prompt_family": "h3", "prompt_profile": "h3_ref2va@1", "source_generation_mode": adaptation.Source.Mode, "source_family": adaptation.Source.Family,
		"source_prompt_hash": adaptation.Source.Hash, "reference_set_hash": adaptation.Hash, "prompt": prompt,
		"reference_artifact_ids": []string{audio.ID.String()}, "references": references,
	})
	_, revision = changeSetIdentity(t, appended)
	finalized := callTool(t, server, endpoint, token, "finalize_change_set", map[string]any{"change_set_id": changeSetID.String(), "expected_change_set_revision": revision, "operation_key": "audio-integration-finalize"})
	if finalized.IsError || !strings.Contains(finalized.Text, `"status":"applied"`) {
		t.Fatalf("finalize_change_set = %+v", finalized)
	}
	readback := callTool(t, server, endpoint, token, "get_clip", map[string]any{"clip_id": clip.ID.String(), "prompt_fields": []string{"reference_video_prompt"}})
	if readback.IsError || !strings.Contains(readback.Text, "布布音频") {
		t.Fatalf("get_clip after apply = %+v", readback)
	}
	assertAudioIntegrationClip(t, store, before, audio.ID, prompt)
	assertOtherEpisodeUnchanged()
	undo, err := changeSets.Undo(ctx, changesetapp.UndoInput{ProjectID: projectValue.ID, ChangeSetID: changeSetID, OperationKey: "audio-integration-undo"})
	if err != nil || undo.ChangeSet.Status != changeset.StatusApplied || undo.ChangeSet.RevertsChangeSetID != changeSetID {
		t.Fatalf("Undo audio adaptation = %+v, %v", undo, err)
	}
	assertAudioIntegrationClip(t, store, before, "", "")
	assertOtherEpisodeUnchanged()
	if err := server.EndTurn(ctx, testSessionID, testTurnID); err != nil {
		t.Fatal(err)
	}
}

// These read-only ProjectQueries never synchronize or mutate a studio library.
// Importing its adapter would form a studio -> agentsession -> agentmcp cycle.
type unusedAudioIntegrationLibrary struct{ projectapp.ProjectLibrary }

type audioIntegrationListItem struct {
	ArtifactID           project.ArtifactID `json:"artifact_id"`
	Kind                 project.MediaKind  `json:"kind"`
	Name                 string             `json:"name"`
	Description          string             `json:"description"`
	DurationMilliseconds int64              `json:"duration_ms"`
	MIMEType             string             `json:"mime_type"`
	UsageCount           int                `json:"usage_count"`
	Revision             int64              `json:"revision"`
}

func assertAudioIntegrationClip(t *testing.T, store projectapp.ProjectStore, before project.ClipDetails, audioID project.ArtifactID, prompt string) {
	t.Helper()
	ctx := context.Background()
	current, err := store.Clip(ctx, before.Clip.ID)
	if err != nil || !reflect.DeepEqual(current.AssetReferences, before.AssetReferences) || current.Clip.FirstImagePrompt != before.Clip.FirstImagePrompt || current.Clip.LastImagePrompt != before.Clip.LastImagePrompt || current.Clip.VideoPrompts.FirstImage != before.Clip.VideoPrompts.FirstImage || current.Clip.VideoPrompts.FirstLastImage != before.Clip.VideoPrompts.FirstLastImage {
		t.Fatalf("visual references or other mode Prompts changed = %+v, %v", current, err)
	}
	if audioID == "" {
		if current.Clip.VideoPrompts.Reference != before.Clip.VideoPrompts.Reference {
			t.Fatalf("Undo did not restore reference Prompt = %q", current.Clip.VideoPrompts.Reference)
		}
	} else {
		stored := parseVideoPromptSet(current.Clip.VideoPrompts.Reference)
		original := parseVideoPromptSet(before.Clip.VideoPrompts.Reference)
		if stored.Families["h3"].Prompt != prompt || stored.Families["h3"].Profile != "h3_ref2va@1" || len(stored.Families["h3"].References) != 5 || !reflect.DeepEqual(stored.Families["wan3"], original.Families["wan3"]) || stored.LegacyPrompt != original.LegacyPrompt {
			t.Fatalf("stored video Prompt families = %+v", stored)
		}
		for index, asset := range before.AssetReferences {
			reference := stored.Families["h3"].References[index]
			if reference.Key != "asset:"+asset.AssetID.String() || !reflect.DeepEqual(reference.Purposes, []string{"identity"}) {
				t.Fatalf("stored visual reference %d = %+v", index, reference)
			}
		}
		voice := stored.Families["h3"].References[4]
		if voice.Key != "upload:"+audioID.String() || !reflect.DeepEqual(voice.Purposes, []string{"voice"}) || voice.Note != "Use as the character dialogue voice" {
			t.Fatalf("stored uploaded audio purpose = %+v", voice)
		}
	}
	uploads, err := store.ClipReferenceArtifacts(ctx, before.Clip.ID)
	if err != nil || audioID == "" && len(uploads) != 0 || audioID != "" && (len(uploads) != 1 || uploads[0].ID != audioID) {
		t.Fatalf("uploaded audio bindings = %+v, %v; want %q", uploads, err, audioID)
	}
}
