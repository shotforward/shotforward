package agentmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestMCPReferenceMediaCropUpdatesAllClipContextsAndRejectsStaleAdaptation(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	media := paginationReferenceMedia(0, project.MediaKindAudio, queries.episode.CreatedAt)
	media.Name, media.Description = "声音参考", "仅参考角色音色，不复制台词。"
	media.Artifact.DurationMilliseconds = 15_000
	media.TrimStartMilliseconds, media.TrimEndMilliseconds = 3_000, 11_000
	extra := paginationReferenceMedia(1, project.MediaKindAudio, queries.episode.CreatedAt)
	queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{media, extra}
	clip := queries.clips[testClipID]
	clip.Clip.GenerationMode, clip.Clip.VideoPrompts.Reference = project.ClipGenerationReference, "角色说话，保留声音意图。"
	queries.clips[testClipID] = clip
	secondID := project.ClipID("clp_" + strings.Repeat("B", 26))
	second := clip
	second.Clip.ID = secondID
	queries.clips[secondID] = second
	queries.clipReferenceMedia = map[project.ClipID][]project.Artifact{testClipID: {media.Artifact}, secondID: {media.Artifact}}
	activateTestTurn(t, server, testTurnID, "turn-trim-context")
	declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	currentTurn := testTurnID
	switchTurn := func(letter string) {
		t.Helper()
		if err := server.EndTurn(context.Background(), testSessionID, currentTurn); err != nil {
			t.Fatal(err)
		}
		currentTurn = agent.TurnID("agt_" + strings.Repeat(letter, 26))
		activateTestTurn(t, server, currentTurn, "turn-trim-"+letter)
		declareProjectChange(t, server, endpoint, credential.Value, "prompts")
	}

	listed := callTool(t, server, endpoint, credential.Value, "list_reference_media", map[string]any{"kind": "audio", "limit": 1})
	var page struct {
		Items      []trimReferenceMetadata `json:"items"`
		NextCursor string                  `json:"next_cursor"`
	}
	if listed.IsError || json.Unmarshal([]byte(listed.Text), &page) != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("list trimmed media = %+v", listed)
	}
	assertTrimReferenceMetadata(t, page.Items[0], 15_000, 3_000, 11_000, 8_000)
	firstContext := readTrimAdaptationContext(t, server, endpoint, credential.Value, testClipID)
	// The adaptation contract correctly fixes one target per Turn. Read the
	// second Clip in another Turn, as the UI does when switching targets.
	switchTurn("B")
	secondContext := readTrimAdaptationContext(t, server, endpoint, credential.Value, secondID)
	for _, value := range []trimAdaptationContext{firstContext, secondContext} {
		if len(value.References) != 1 {
			t.Fatalf("clip references = %+v", value.References)
		}
		assertTrimReferenceMetadata(t, value.References[0], 15_000, 3_000, 11_000, 8_000)
	}
	if firstContext.Hash != secondContext.Hash {
		t.Fatal("the same Episode selection produced different Clip reference hashes")
	}
	switchTurn("C")
	firstContext = readTrimAdaptationContext(t, server, endpoint, credential.Value, testClipID)
	// Page cursors belong to a Turn too; obtain a current-turn page before
	// verifying that a library crop change invalidates that cursor.
	listed = callTool(t, server, endpoint, credential.Value, "list_reference_media", map[string]any{"kind": "audio", "limit": 1})
	if listed.IsError || json.Unmarshal([]byte(listed.Text), &page) != nil {
		t.Fatalf("current-turn list = %+v", listed)
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "适配裁剪素材", "operation_key": "trim-context-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	queries.mutex.Lock()
	queries.episodeReferenceMedia[0].TrimStartMilliseconds = 5_000
	queries.episodeReferenceMedia[0].TrimEndMilliseconds = 9_000
	queries.episodeReferenceMedia[0].Revision++
	queries.mutex.Unlock()

	stalePage := callTool(t, server, endpoint, credential.Value, "list_reference_media", map[string]any{"kind": "audio", "limit": 1, "cursor": page.NextCursor})
	if !stalePage.IsError || toolFailureCode(t, stalePage.Text) != "cursor_stale" {
		t.Fatalf("crop change did not invalidate the old library cursor = %+v", stalePage)
	}
	appendArguments := func(value trimAdaptationContext) map[string]any {
		return map[string]any{
			"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "trim-context-append",
			"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va",
			"prompt_family": "h3", "prompt_profile": "h3_ref2va@1", "source_generation_mode": value.Source.Mode, "source_family": value.Source.Family,
			"source_prompt_hash": value.Source.Hash, "reference_set_hash": value.Hash,
			"prompt":     "subject_definitions:\n<Subject 1>: 角色\nsummary:\n角色说话。\nretention_analysis:\n@声音参考 仅参考音色。\ndetailed_description:\n人物使用 @声音参考 的音色说话。\noverall_soundscape:\n安静的室内环境。\nnon_diegetic_music:\nNone",
			"references": []map[string]any{{"key": "upload:" + media.Artifact.ID.String(), "purposes": []string{"voice"}, "note": "参考音色，不复制台词。"}},
		}
	}
	staleWrite := callTool(t, server, endpoint, credential.Value, "append_video_prompt_adaptation", appendArguments(firstContext))
	if !staleWrite.IsError || toolFailureCode(t, staleWrite.Text) != "observation_stale" || drafts.appendCount() != 0 {
		t.Fatalf("old crop context appended a ChangeSet = %+v", staleWrite)
	}
	refreshed := readTrimAdaptationContext(t, server, endpoint, credential.Value, testClipID)
	if refreshed.Hash == firstContext.Hash || refreshed.Source.Hash != firstContext.Source.Hash {
		t.Fatalf("crop change should update reference hash without changing prompt = %+v", refreshed)
	}
	assertTrimReferenceMetadata(t, refreshed.References[0], 15_000, 5_000, 9_000, 4_000)
	appended := callTool(t, server, endpoint, credential.Value, "append_video_prompt_adaptation", appendArguments(refreshed))
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("refreshed crop context could not append = %+v", appended)
	}
	switchTurn("D")
	secondRefreshed := readTrimAdaptationContext(t, server, endpoint, credential.Value, secondID)
	assertTrimReferenceMetadata(t, secondRefreshed.References[0], 15_000, 5_000, 9_000, 4_000)
}

func TestReferenceMediaHashesIncludeRangeEvenWhenRevisionIsUnchanged(t *testing.T) {
	t.Parallel()
	_, _, _, queries, _ := newMCPFixture(t)
	media := paginationReferenceMedia(0, project.MediaKindVideo, queries.episode.CreatedAt)
	before, err := referenceMediaListSnapshot([]project.EpisodeReferenceMedia{media})
	if err != nil {
		t.Fatal(err)
	}
	media.TrimStartMilliseconds, media.TrimEndMilliseconds = 1_000, 6_000
	after, err := referenceMediaListSnapshot([]project.EpisodeReferenceMedia{media})
	if err != nil || before == after {
		t.Fatalf("range did not affect list snapshot = %q, %q, %v", before, after, err)
	}
	reference := adaptationReference{Key: "upload:" + media.Artifact.ID.String(), Source: "upload", LibraryRevision: media.Revision, OriginalDurationMilliseconds: 8_000, DurationMilliseconds: 8_000, TrimEndMilliseconds: 8_000}
	oldHash := adaptationReferenceSetHash([]adaptationReference{reference})
	reference.TrimStartMilliseconds, reference.TrimEndMilliseconds, reference.DurationMilliseconds = 1_000, 6_000, 5_000
	if oldHash == adaptationReferenceSetHash([]adaptationReference{reference}) {
		t.Fatal("crop range did not affect adaptation reference hash")
	}
}

func TestMCPAdaptationNeverFallsBackToFullSourceForOrphanedLibraryUploads(t *testing.T) {
	t.Parallel()
	for _, kind := range []project.MediaKind{project.MediaKindAudio, project.MediaKindVideo, project.MediaKindImage} {
		for _, missing := range []string{"absent", "deleted", "other_episode"} {
			t.Run(string(kind)+"/"+missing, func(t *testing.T) {
				t.Parallel()
				server, endpoint, credential, queries, drafts := newMCPFixture(t)
				media := paginationReferenceMedia(0, kind, queries.episode.CreatedAt)
				clip := queries.clips[testClipID]
				clip.Clip.GenerationMode, clip.Clip.VideoPrompts.Reference = project.ClipGenerationReference, "保留现有素材参考。"
				queries.clips[testClipID] = clip
				queries.clipReferenceMedia = map[project.ClipID][]project.Artifact{testClipID: {media.Artifact}}
				switch missing {
				case "deleted":
					deletedAt := queries.episode.CreatedAt
					media.DeletedAt = &deletedAt
					queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{media}
				case "other_episode":
					media.EpisodeID = project.EpisodeID("ep_" + strings.Repeat("O", 26))
					queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{media}
				}
				activateTestTurn(t, server, testTurnID, "turn-orphaned-upload")
				declareProjectChange(t, server, endpoint, credential.Value, "prompts")
				result := callTool(t, server, endpoint, credential.Value, "get_video_prompt_adaptation_context", map[string]any{"clip_id": testClipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va"})
				if !result.IsError || toolFailureCode(t, result.Text) != "observation_stale" || drafts.appendCount() != 0 {
					t.Fatalf("orphaned library upload fell back to source metadata = %+v", result)
				}
				// Repairing the current-Episode binding permits a refreshed read;
				// the source identity is unchanged and no prompt edit was written.
				media.EpisodeID, media.DeletedAt = testEpisodeID, nil
				queries.episodeReferenceMedia = []project.EpisodeReferenceMedia{media}
				refreshed := readTrimAdaptationContext(t, server, endpoint, credential.Value, testClipID)
				if len(refreshed.References) != 1 || drafts.appendCount() != 0 {
					t.Fatalf("refreshed active-library context = %+v", refreshed)
				}
			})
		}
	}
}

type trimReferenceMetadata struct {
	OriginalDuration int64 `json:"original_duration_ms"`
	Duration         int64 `json:"duration_ms"`
	Start            int64 `json:"trim_start_ms"`
	End              int64 `json:"trim_end_ms"`
}

type trimAdaptationContext struct {
	Source struct {
		Mode   string `json:"generation_mode"`
		Family string `json:"prompt_family"`
		Hash   string `json:"prompt_hash"`
	} `json:"source"`
	Hash       string                  `json:"reference_set_hash"`
	References []trimReferenceMetadata `json:"references"`
}

func readTrimAdaptationContext(t *testing.T, server *Server, endpoint, token string, clipID project.ClipID) trimAdaptationContext {
	t.Helper()
	result := callTool(t, server, endpoint, token, "get_video_prompt_adaptation_context", map[string]any{"clip_id": clipID.String(), "generation_mode": "reference", "generator_id": "video.h3.shotforward", "provider_model_id": "h3-ref2va"})
	var value trimAdaptationContext
	if result.IsError || json.Unmarshal([]byte(result.Text), &value) != nil {
		t.Fatalf("trim adaptation context = %+v", result)
	}
	return value
}

func assertTrimReferenceMetadata(t *testing.T, value trimReferenceMetadata, original, start, end, duration int64) {
	t.Helper()
	if value.OriginalDuration != original || value.Start != start || value.End != end || value.Duration != duration {
		t.Fatalf("trim metadata = %+v; want original=%d range=%d..%d duration=%d", value, original, start, end, duration)
	}
}
