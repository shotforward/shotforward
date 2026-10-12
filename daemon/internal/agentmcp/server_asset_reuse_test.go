package agentmcp

import (
	"encoding/json"
	"strings"
	"testing"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestAssetReuseRecoversAfterHistoricalReadUsesWrongTool(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts, priorID := newAssetReuseFixture(t)
	listed := callTool(t, server, endpoint, credential.Value, "list_prior_asset_candidates", map[string]any{"kind": "character"})
	if listed.IsError || !strings.Contains(listed.Text, priorID.String()) || strings.Contains(listed.Text, "第一集黑色风衣") {
		t.Fatalf("prior candidates = %+v", listed)
	}
	arguments := assetReuseArguments(priorID, "第二集婚礼服装")
	withoutDetail := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	assertPriorAssetRecovery(t, withoutDetail, "source_asset_not_observed", priorID)
	if drafts.appendCount() != 0 {
		t.Fatal("listing alone allowed an unobserved source into a ChangeSet")
	}

	wrongRead := callTool(t, server, endpoint, credential.Value, "get_asset", map[string]any{"asset_id": priorID.String()})
	assertPriorAssetRecovery(t, wrongRead, "object_outside_scope", priorID)
	afterWrongRead := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	assertPriorAssetRecovery(t, afterWrongRead, "source_asset_not_observed", priorID)
	if drafts.appendCount() != 0 {
		t.Fatal("wrong-scope read observed the source or modified the ChangeSet")
	}

	detail := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()})
	if detail.IsError || !strings.Contains(detail.Text, "第一集黑色风衣") {
		t.Fatalf("read historical source = %+v", detail)
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("reuse after correct detail read = %+v, appends=%d", appended, drafts.appendCount())
	}
	creation := drafts.lastAppend().Operations[0].AssetCreation
	if creation == nil || creation.SourceAssetID != priorID || creation.IdentityID != queries.prior[priorID].Asset.Identity.ID || creation.Name != "布布" || creation.Prompt != "第二集婚礼服装" || creation.SourceAssetRevision != 1 {
		t.Fatalf("derived episode asset = %+v", creation)
	}
	if queries.prior[priorID].Asset.Asset.Prompt != "第一集黑色风衣" {
		t.Fatal("drafting a new episode version changed its historical source")
	}
}

func TestAssetCreationWithHistoricalNameReturnsActionableReuseRecovery(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts, priorID := newAssetReuseFixture(t)
	arguments := assetReuseArguments(priorID, "第二集婚礼服装")
	arguments["operations"] = []map[string]any{{"kind": "character", "name": "布布", "prompt": "第二集婚礼服装"}}
	duplicateIdentity := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	assertPriorAssetRecovery(t, duplicateIdentity, "prior_asset_reuse_required", priorID)
	if !strings.Contains(duplicateIdentity.Text, "source_asset_id") || !strings.Contains(duplicateIdentity.Text, "append_asset_operations") || drafts.appendCount() != 0 {
		t.Fatalf("same-name recovery is not actionable: %+v, appends=%d", duplicateIdentity, drafts.appendCount())
	}
	if detail := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()}); detail.IsError {
		t.Fatalf("read historical detail = %+v", detail)
	}
	arguments["operations"] = []map[string]any{{"source_asset_id": priorID.String(), "prompt": "第二集婚礼服装"}}
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("retry by source = %+v, appends=%d", appended, drafts.appendCount())
	}
	creation := drafts.lastAppend().Operations[0].AssetCreation
	if creation == nil || creation.IdentityID != queries.prior[priorID].Asset.Identity.ID || creation.SourceAssetID != priorID || creation.Prompt != "第二集婚礼服装" {
		t.Fatalf("recovered asset creation = %+v", creation)
	}
}

func TestAssetReuseRereadsSourceAfterItsRevisionReallyChanges(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts, priorID := newAssetReuseFixture(t)
	if detail := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()}); detail.IsError {
		t.Fatalf("read historical detail = %+v", detail)
	}
	queries.mutex.Lock()
	changed := queries.prior[priorID]
	changed.Asset.Asset.Prompt = "第一集重新确认的外观"
	changed.Asset.Asset.Revision++
	queries.prior[priorID] = changed
	queries.mutex.Unlock()
	arguments := assetReuseArguments(priorID, "")
	arguments["operations"] = []map[string]any{{"source_asset_id": priorID.String()}}
	stale := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	assertPriorAssetRecovery(t, stale, "observation_stale", priorID)
	if drafts.appendCount() != 0 {
		t.Fatal("stale historical source was accepted")
	}
	refreshed := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()})
	if refreshed.IsError || !strings.Contains(refreshed.Text, `"revision":2`) || !strings.Contains(refreshed.Text, "第一集重新确认的外观") {
		t.Fatalf("refresh source = %+v", refreshed)
	}
	appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	if appended.IsError || drafts.appendCount() != 1 {
		t.Fatalf("retry refreshed source = %+v, appends=%d", appended, drafts.appendCount())
	}
	creation := drafts.lastAppend().Operations[0].AssetCreation
	if creation == nil || creation.SourceAssetRevision != 2 || creation.Prompt != "第一集重新确认的外观" {
		t.Fatalf("refreshed asset creation = %+v", creation)
	}
}

func TestAssetReuseDoesNotDuplicateIdentityAlreadyPresentInCurrentEpisode(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts, priorID := newAssetReuseFixture(t)
	if detail := callTool(t, server, endpoint, credential.Value, "get_prior_asset_candidate", map[string]any{"asset_id": priorID.String()}); detail.IsError {
		t.Fatalf("read historical detail = %+v", detail)
	}
	queries.mutex.Lock()
	current := queries.prior[priorID].Asset
	current.Asset.ID = project.AssetID("ast_CCCCCCCCCCCCCCCCCCCCCCCCCC")
	current.Asset.EpisodeID = testEpisodeID
	current.Asset.Prompt = "第二集已经创建的婚礼服装"
	queries.assets[current.Asset.ID] = current
	queries.mutex.Unlock()
	if result := callTool(t, server, endpoint, credential.Value, "list_assets", map[string]any{}); result.IsError {
		t.Fatalf("refresh current assets = %+v", result)
	}
	for _, operation := range []map[string]any{
		{"source_asset_id": priorID.String()},
		{"kind": "character", "name": "布布", "prompt": "再次创建"},
	} {
		arguments := assetReuseArguments(priorID, "")
		arguments["operations"] = []map[string]any{operation}
		repeated := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
		if !repeated.IsError || toolFailureCode(t, repeated.Text) != "asset_already_present" || drafts.appendCount() != 0 {
			t.Fatalf("repeated creation = %+v, appends=%d", repeated, drafts.appendCount())
		}
	}
}

func TestHistoricalAssetReuseRequiresExactKindAndName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		kind string
	}{
		{name: "布布妹妹", kind: "character"},
		{name: "布布", kind: "prop"},
	} {
		t.Run(test.kind+"_"+test.name, func(t *testing.T) {
			t.Parallel()
			server, endpoint, credential, _, drafts, priorID := newAssetReuseFixture(t)
			arguments := assetReuseArguments(priorID, "")
			arguments["operations"] = []map[string]any{{"kind": test.kind, "name": test.name, "prompt": "新资产外观"}}
			appended := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
			if appended.IsError || drafts.appendCount() != 1 {
				t.Fatalf("distinct identity creation = %+v, appends=%d", appended, drafts.appendCount())
			}
			creation := drafts.lastAppend().Operations[0].AssetCreation
			if creation == nil || creation.IdentityID != "" || creation.SourceAssetID != "" || creation.Name != test.name || string(creation.Kind) != test.kind {
				t.Fatalf("distinct identity was silently reused = %+v", creation)
			}
		})
	}
}

func TestHistoricalAssetNameRecoverySelectsLatestEarlierEpisodeVersion(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, queries, drafts, priorID := newAssetReuseFixture(t)
	latestID := project.AssetID("ast_LLLLLLLLLLLLLLLLLLLLLLLLLL")
	latestEpisodeID := project.EpisodeID("ep_LLLLLLLLLLLLLLLLLLLLLLLLLL")
	queries.addPriorAsset(latestID, latestEpisodeID, "布布", "第二集外卖制服", queries.episode.CreatedAt)
	queries.mutex.Lock()
	queries.episode.SequenceNumber = 3
	latest := queries.prior[latestID]
	latest.Asset.Identity = queries.prior[priorID].Asset.Identity
	latest.Asset.Asset.IdentityID = latest.Asset.Identity.ID
	latest.EpisodeSequence = 2
	queries.prior[latestID] = latest
	latestEpisode := queries.episodes[latestEpisodeID]
	latestEpisode.SequenceNumber = 2
	queries.episodes[latestEpisodeID] = latestEpisode
	queries.mutex.Unlock()
	arguments := assetReuseArguments(priorID, "")
	arguments["operations"] = []map[string]any{{"kind": "character", "name": "布布", "prompt": "第三集婚礼服装"}}
	recovery := callTool(t, server, endpoint, credential.Value, "append_asset_operations", arguments)
	assertPriorAssetRecovery(t, recovery, "prior_asset_reuse_required", latestID)
	if drafts.appendCount() != 0 {
		t.Fatal("same-name creation bypassed the latest historical version")
	}
}

func newAssetReuseFixture(t *testing.T) (*Server, string, Credential, *fakeProjectQueries, *fakeDraftCommands, project.AssetID) {
	t.Helper()
	server, endpoint, credential, queries, drafts := newMCPFixture(t)
	queries.episode.SequenceNumber = 2
	priorID := project.AssetID("ast_PPPPPPPPPPPPPPPPPPPPPPPPPP")
	queries.addPriorAsset(priorID, project.EpisodeID("ep_PPPPPPPPPPPPPPPPPPPPPPPPPP"), "布布", "第一集黑色风衣", queries.episode.CreatedAt)
	activateTestTurn(t, server, testTurnID, "turn_asset_reuse")
	declareProjectChange(t, server, endpoint, credential.Value, "assets")
	for _, tool := range []string{"read_script", "list_assets"} {
		if result := callTool(t, server, endpoint, credential.Value, tool, map[string]any{}); result.IsError {
			t.Fatalf("%s = %+v", tool, result)
		}
	}
	begin := callTool(t, server, endpoint, credential.Value, "begin_change_set", map[string]any{"title": "复用历史角色", "operation_key": "reuse-begin"})
	if begin.IsError {
		t.Fatalf("begin = %+v", begin)
	}
	return server, endpoint, credential, queries, drafts, priorID
}

func assetReuseArguments(priorID project.AssetID, prompt string) map[string]any {
	return map[string]any{
		"change_set_id": "cst_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "expected_change_set_revision": 1, "operation_key": "reuse-append",
		"operations": []map[string]any{{"source_asset_id": priorID.String(), "prompt": prompt}},
	}
}

func assertPriorAssetRecovery(t *testing.T, result toolResultView, code string, priorID project.AssetID) {
	t.Helper()
	var failure struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		SourceAssetID string `json:"source_asset_id"`
		NextTool      string `json:"next_tool"`
	}
	if !result.IsError || json.Unmarshal([]byte(result.Text), &failure) != nil || failure.Code != code || failure.SourceAssetID != priorID.String() || failure.NextTool != "get_prior_asset_candidate" || !strings.Contains(failure.Message, "get_prior_asset_candidate") {
		t.Fatalf("historical recovery expected %q for %s: %+v", code, priorID, result)
	}
}
