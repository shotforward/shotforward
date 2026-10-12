package agentmcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type paginationToolCase struct {
	name    string
	maximum int
	idField string
}

func paginationToolCases() []paginationToolCase {
	return []paginationToolCase{
		{name: "list_assets", maximum: MaxAssetPageSize, idField: "asset_id"},
		{name: "list_deleted_assets", maximum: MaxAssetPageSize, idField: "asset_id"},
		{name: "list_prior_asset_candidates", maximum: MaxPriorAssetPageSize, idField: "asset_id"},
		{name: "list_scenes", maximum: MaxScenePageSize, idField: "scene_id"},
		{name: "get_clips", maximum: MaxClipPageSize, idField: "clip_id"},
		{name: "list_reference_media", maximum: MaxReferenceMediaPageSize, idField: "artifact_id"},
	}
}

func TestPaginatedToolsNormalizePositiveLimitsAndPreservePagination(t *testing.T) {
	t.Parallel()
	for _, tool := range paginationToolCases() {
		t.Run(tool.name, func(t *testing.T) {
			t.Parallel()
			for _, limit := range []struct {
				name  string
				value any
			}{
				{name: "omitted"},
				{name: "zero", value: 0},
				{name: "maximum", value: tool.maximum},
				{name: "just_above_maximum", value: tool.maximum + 1},
				{name: "one_hundred", value: 100},
				{name: "largest_integer", value: int(^uint(0) >> 1)},
			} {
				t.Run(limit.name, func(t *testing.T) {
					t.Parallel()
					server, endpoint, credential, queries, drafts := newMCPFixture(t)
					expected := populatePaginationQueries(tool.name, queries, 2*tool.maximum+3)
					activateTestTurn(t, server, testTurnID, "turn-pagination-limits")
					if tool.name == "list_deleted_assets" {
						declareProjectChange(t, server, endpoint, credential.Value, "assets")
					}
					arguments := paginationToolArguments(tool.name)
					if limit.value != nil {
						arguments["limit"] = limit.value
					}
					assertPaginationIDs(t, server, endpoint, credential.Value, tool, arguments, expected)
					if drafts.appendCount() != 0 || drafts.discardCount() != 0 {
						t.Fatal("read-only pagination wrote or discarded a draft")
					}
				})
			}
		})
	}
}

func TestPaginatedToolsPreserveRequestedSmallLimits(t *testing.T) {
	t.Parallel()
	for _, tool := range paginationToolCases() {
		t.Run(tool.name, func(t *testing.T) {
			t.Parallel()
			for _, limit := range []int{1, 2} {
				t.Run(fmt.Sprint(limit), func(t *testing.T) {
					t.Parallel()
					server, endpoint, credential, queries, _ := newMCPFixture(t)
					expected := populatePaginationQueries(tool.name, queries, 5)
					activateTestTurn(t, server, testTurnID, "turn-pagination-small")
					if tool.name == "list_deleted_assets" {
						declareProjectChange(t, server, endpoint, credential.Value, "assets")
					}
					arguments := paginationToolArguments(tool.name)
					arguments["limit"] = limit
					smallPage := tool
					smallPage.maximum = limit
					assertPaginationIDs(t, server, endpoint, credential.Value, smallPage, arguments, expected)
				})
			}
		})
	}
}

func TestPaginatedToolsKeepStrictArgumentValidation(t *testing.T) {
	t.Parallel()
	for _, tool := range paginationToolCases() {
		t.Run(tool.name, func(t *testing.T) {
			t.Parallel()
			for _, invalid := range []struct {
				name   string
				fields map[string]any
			}{
				{name: "negative", fields: map[string]any{"limit": -1}},
				{name: "string", fields: map[string]any{"limit": "100"}},
				{name: "fractional", fields: map[string]any{"limit": 1.5}},
				{name: "boolean", fields: map[string]any{"limit": true}},
				{name: "unknown_field", fields: map[string]any{"limit": 100, "page": 1}},
				{name: "episode_scope_field", fields: map[string]any{"limit": 100, "episode_id": testEpisodeID.String()}},
				{name: "project_scope_field", fields: map[string]any{"limit": 100, "project_id": testProjectID.String()}},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					t.Parallel()
					server, endpoint, credential, _, _ := newMCPFixture(t)
					activateTestTurn(t, server, testTurnID, "turn-pagination-invalid")
					if tool.name == "list_deleted_assets" {
						declareProjectChange(t, server, endpoint, credential.Value, "assets")
					}
					arguments := paginationToolArguments(tool.name)
					for key, value := range invalid.fields {
						arguments[key] = value
					}
					result := callTool(t, server, endpoint, credential.Value, tool.name, arguments)
					if !result.IsError || toolFailureCode(t, result.Text) != "invalid_arguments" {
						t.Fatalf("%s(%v) = %+v; want invalid_arguments", tool.name, arguments, result)
					}
				})
			}
		})
	}
}

func TestReferenceMediaOversizedLimitPreservesKindFiltering(t *testing.T) {
	t.Parallel()
	for _, kind := range []project.MediaKind{"", project.MediaKindImage, project.MediaKindVideo, project.MediaKindAudio} {
		name := string(kind)
		if name == "" {
			name = "all_kinds"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, endpoint, credential, queries, _ := newMCPFixture(t)
			count := 2*MaxReferenceMediaPageSize + 3
			expected := make([]string, 0, count)
			for index := range count {
				for kindIndex, mediaKind := range []project.MediaKind{project.MediaKindImage, project.MediaKindVideo, project.MediaKindAudio} {
					value := paginationReferenceMedia(index*3+kindIndex, mediaKind, queries.episode.CreatedAt)
					queries.episodeReferenceMedia = append(queries.episodeReferenceMedia, value)
					if kind == "" || mediaKind == kind {
						expected = append(expected, value.Artifact.ID.String())
					}
				}
			}
			activateTestTurn(t, server, testTurnID, "turn-pagination-kind")
			tool := paginationToolCase{name: "list_reference_media", maximum: MaxReferenceMediaPageSize, idField: "artifact_id"}
			arguments := map[string]any{"limit": 100}
			if kind != "" {
				arguments["kind"] = string(kind)
			}
			assertPaginationIDs(t, server, endpoint, credential.Value, tool, arguments, expected)
		})
	}
}

func TestGetClipsOversizedLimitPreservesSceneValidation(t *testing.T) {
	t.Parallel()
	for _, invalid := range []struct {
		name    string
		sceneID string
		code    string
	}{
		{name: "empty", code: "invalid_arguments"},
		{name: "malformed", sceneID: "not-a-scene", code: "invalid_arguments"},
		{name: "outside_scope", sceneID: paginationID("scn_", 1), code: "object_outside_scope"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			t.Parallel()
			server, endpoint, credential, queries, _ := newMCPFixture(t)
			populatePaginationQueries("get_clips", queries, 1)
			activateTestTurn(t, server, testTurnID, "turn-pagination-invalid-scene")
			result := callTool(t, server, endpoint, credential.Value, "get_clips", map[string]any{"scene_id": invalid.sceneID, "limit": 100})
			if !result.IsError || toolFailureCode(t, result.Text) != invalid.code {
				t.Fatalf("get_clips(scene_id=%q, limit=100) = %+v; want %s", invalid.sceneID, result, invalid.code)
			}
		})
	}
}

func TestPaginatedToolsRejectInvalidKind(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"list_reference_media", "list_prior_asset_candidates"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			server, endpoint, credential, _, _ := newMCPFixture(t)
			activateTestTurn(t, server, testTurnID, "turn-pagination-invalid-kind")
			result := callTool(t, server, endpoint, credential.Value, tool, map[string]any{"kind": "unsupported", "limit": 100})
			if !result.IsError || toolFailureCode(t, result.Text) != "invalid_arguments" {
				t.Fatalf("%s(invalid kind, limit=100) = %+v; want invalid_arguments", tool, result)
			}
		})
	}
}

func TestPaginatedToolSchemasDescribeResponseCapsWithoutRejectingLargerRequests(t *testing.T) {
	t.Parallel()
	definitions := make(map[string]toolDefinition)
	for _, definition := range toolDefinitions() {
		definitions[definition.Name] = definition
	}
	for _, tool := range paginationToolCases() {
		t.Run(tool.name, func(t *testing.T) {
			definition, ok := definitions[tool.name]
			if !ok {
				t.Fatalf("missing tool definition %q", tool.name)
			}
			properties := definition.InputSchema["properties"].(map[string]any)
			limit := properties["limit"].(map[string]any)
			if limit["type"] != "integer" || limit["minimum"] != 0 || limit["default"] != tool.maximum {
				t.Fatalf("limit schema = %v; want integer, minimum 0, default %d", limit, tool.maximum)
			}
			if _, exists := limit["maximum"]; exists {
				t.Fatalf("limit schema still rejects larger requests: %v", limit)
			}
			description, _ := limit["description"].(string)
			if !strings.Contains(strings.ToLower(description), "default") || !strings.Contains(strings.ToLower(description), "capped") || !strings.Contains(description, fmt.Sprint(tool.maximum)) {
				t.Fatalf("limit description does not explain default and response cap %d: %q", tool.maximum, description)
			}
			if definition.InputSchema["additionalProperties"] != false {
				t.Fatalf("tool schema allows unknown fields: %v", definition.InputSchema)
			}
		})
	}
}

func paginationToolArguments(name string) map[string]any {
	arguments := make(map[string]any)
	switch name {
	case "get_clips":
		arguments["scene_id"] = paginationID("scn_", 0)
	case "list_reference_media":
		arguments["kind"] = "audio"
	case "list_prior_asset_candidates":
		arguments["kind"] = "character"
	}
	return arguments
}

func populatePaginationQueries(name string, queries *fakeProjectQueries, count int) []string {
	now := queries.episode.CreatedAt
	expected := make([]string, 0, count)
	queries.assets = make(map[project.AssetID]project.EpisodeAsset)
	for index := range count {
		switch name {
		case "list_assets", "list_deleted_assets", "list_prior_asset_candidates":
			value := paginationAsset(index, now)
			if name == "list_prior_asset_candidates" {
				value.Asset.EpisodeID = project.EpisodeID(paginationID("ep_", 0))
				queries.prior[value.Asset.ID] = projectapp.PriorAssetCandidate{Asset: value, EpisodeSequence: 1}
			} else {
				if name == "list_deleted_assets" {
					value.Asset.DeletedAt = &now
				}
				queries.assets[value.Asset.ID] = value
			}
			expected = append(expected, value.Asset.ID.String())
		case "list_scenes":
			scene := paginationScene(index, now)
			queries.storyboard.Scenes = append(queries.storyboard.Scenes, project.StoryboardScene{Scene: scene})
			expected = append(expected, scene.ID.String())
		case "get_clips":
			if index == 0 {
				queries.storyboard.Scenes = []project.StoryboardScene{{Scene: paginationScene(0, now)}}
			}
			clip := project.ClipDetails{Clip: project.Clip{
				ID: project.ClipID(paginationID("clp_", index)), ProjectID: testProjectID, EpisodeID: testEpisodeID,
				SceneID: queries.storyboard.Scenes[0].Scene.ID, Position: index + 1, Title: fmt.Sprintf("Clip %03d", index),
				PlannedDurationSeconds: 8, GenerationMode: project.ClipGenerationReference, Revision: 1, CreatedAt: now, UpdatedAt: now,
			}}
			queries.storyboard.Scenes[0].Clips = append(queries.storyboard.Scenes[0].Clips, clip)
			queries.storyboard.PlannedDurationSeconds += clip.Clip.PlannedDurationSeconds
			expected = append(expected, clip.Clip.ID.String())
		case "list_reference_media":
			value := paginationReferenceMedia(index, project.MediaKindAudio, now)
			queries.episodeReferenceMedia = append(queries.episodeReferenceMedia, value)
			expected = append(expected, value.Artifact.ID.String())
		}
	}
	if name == "list_deleted_assets" {
		active := paginationAsset(count, now)
		queries.assets[active.Asset.ID] = active
	}
	return expected
}

func paginationID(prefix string, index int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	suffix := []byte(strings.Repeat("A", 26))
	for position := len(suffix) - 1; index > 0; position-- {
		suffix[position] = alphabet[index%len(alphabet)]
		index /= len(alphabet)
	}
	return prefix + string(suffix)
}

func paginationAsset(index int, now time.Time) project.EpisodeAsset {
	identityID := project.AssetIdentityID(paginationID("asi_", index))
	return project.EpisodeAsset{
		Identity: project.AssetIdentity{ID: identityID, ProjectID: testProjectID, Kind: project.AssetKindCharacter, Name: fmt.Sprintf("Asset %03d", index), Revision: 1, CreatedAt: now, UpdatedAt: now},
		Asset: project.Asset{ID: project.AssetID(paginationID("ast_", index)), ProjectID: testProjectID, EpisodeID: testEpisodeID,
			IdentityID: identityID, Prompt: "reference prompt", Revision: 1, CreatedAt: now, UpdatedAt: now},
	}
}

func paginationScene(index int, now time.Time) project.Scene {
	return project.Scene{ID: project.SceneID(paginationID("scn_", index)), ProjectID: testProjectID, EpisodeID: testEpisodeID,
		Position: index + 1, Title: fmt.Sprintf("Scene %03d", index), Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func paginationReferenceMedia(index int, kind project.MediaKind, now time.Time) project.EpisodeReferenceMedia {
	artifact := project.Artifact{
		ID: project.ArtifactID(paginationID("art_", index)), ProjectID: testProjectID, Kind: kind,
		RelativePath: fmt.Sprintf("media/reference/%d.bin", index), SHA256: strings.Repeat("a", 64), ByteSize: 128,
		MIMEType: "application/octet-stream", OriginalFilename: fmt.Sprintf("reference-%d.bin", index), CreatedAt: now,
	}
	switch kind {
	case project.MediaKindImage:
		artifact.Width, artifact.Height = 720, 1280
	case project.MediaKindVideo:
		artifact.Width, artifact.Height = 720, 1280
		artifact.DurationMilliseconds, artifact.FPSNumerator, artifact.FPSDenominator = 8_000, 24, 1
	case project.MediaKindAudio:
		artifact.DurationMilliseconds, artifact.AudioSampleRate, artifact.AudioChannels = 8_000, 44_100, 1
	}
	return project.EpisodeReferenceMedia{EpisodeID: testEpisodeID, Artifact: artifact, Name: fmt.Sprintf("Reference %03d", index),
		Description: "uploaded media reference", Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func assertPaginationIDs(t *testing.T, server *Server, endpoint, token string, tool paginationToolCase, arguments map[string]any, expected []string) {
	t.Helper()
	actual := make([]string, 0, len(expected))
	for pageIndex := 0; ; pageIndex++ {
		if pageIndex > len(expected) {
			t.Fatal("pagination did not finish")
		}
		result := callTool(t, server, endpoint, token, tool.name, arguments)
		if result.IsError {
			t.Fatalf("%s(%v), page %d = %+v", tool.name, arguments, pageIndex+1, result)
		}
		var page struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
			HasMore    bool             `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(result.Text), &page); err != nil {
			t.Fatal(err)
		}
		wantCount := min(tool.maximum, len(expected)-len(actual))
		if len(page.Items) != wantCount {
			t.Fatalf("page %d has %d items; want %d (response cap %d)", pageIndex+1, len(page.Items), wantCount, tool.maximum)
		}
		for _, item := range page.Items {
			id, ok := item[tool.idField].(string)
			if !ok {
				t.Fatalf("page item has no %s: %v", tool.idField, item)
			}
			actual = append(actual, id)
			if tool.name == "list_reference_media" && arguments["kind"] != nil && item["kind"] != arguments["kind"] {
				t.Fatalf("filtered media kind = %v; want %v", item["kind"], arguments["kind"])
			}
		}
		wantMore := len(actual) < len(expected)
		if page.HasMore != wantMore || (page.NextCursor != "") != wantMore {
			t.Fatalf("page %d cursor/has_more = %q/%t; want has_more %t", pageIndex+1, page.NextCursor, page.HasMore, wantMore)
		}
		if !page.HasMore {
			break
		}
		arguments["cursor"] = page.NextCursor
	}
	if !reflect.DeepEqual(actual, expected) {
		for index := range expected {
			if actual[index] != expected[index] {
				t.Fatalf("pagination omitted, duplicated, or reordered item %d: got %q; want %q", index, actual[index], expected[index])
			}
		}
	}
}
