package agentmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	"github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestAssetExtractionToolDefaultsStyleIntoEntityPrompts(t *testing.T) {
	t.Parallel()
	for _, definition := range toolDefinitions() {
		if definition.Name != "append_asset_operations" {
			continue
		}
		if !strings.Contains(definition.Description, "Create a style Asset only when the user explicitly requested") {
			t.Fatalf("append_asset_operations description does not protect the default extraction policy: %q", definition.Description)
		}
		schema, err := json.Marshal(definition.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(schema), "otherwise distribute style direction across character, location, and prop Prompts") {
			t.Fatalf("append_asset_operations schema does not explain where style belongs: %s", schema)
		}
		return
	}
	t.Fatal("append_asset_operations tool definition not found")
}

func TestPlanExistingSceneClipPositions(t *testing.T) {
	t.Parallel()
	firstID := project.ClipID("clp_" + strings.Repeat("A", 26))
	secondID := project.ClipID("clp_" + strings.Repeat("B", 26))
	sceneID := project.SceneID("scn_" + strings.Repeat("S", 26))
	scene := project.StoryboardScene{
		Scene: project.Scene{ID: sceneID},
		Clips: []project.ClipDetails{
			{Clip: project.Clip{ID: firstID, Position: project.StoryboardOrderStep}},
			{Clip: project.Clip{ID: secondID, Position: 2 * project.StoryboardOrderStep}},
		},
	}
	operations := []storyboardOperationArgument{
		{Type: string(changeset.CreateClip), SceneID: sceneID.String(), BeforeClipID: firstID.String()},
		{Type: string(changeset.CreateClip), SceneID: sceneID.String(), AfterClipID: firstID.String()},
		{Type: string(changeset.CreateClip), SceneID: sceneID.String()},
	}

	positions, err := planExistingSceneClipPositions(scene, operations)
	if err != nil {
		t.Fatal(err)
	}
	if positions[0] <= 0 || positions[0] >= project.StoryboardOrderStep {
		t.Fatalf("position before first Clip = %d", positions[0])
	}
	if positions[1] <= project.StoryboardOrderStep || positions[1] >= 2*project.StoryboardOrderStep {
		t.Fatalf("position after selected Clip = %d", positions[1])
	}
	if positions[2] != 3*project.StoryboardOrderStep {
		t.Fatalf("appended position = %d", positions[2])
	}
}

func TestValidateStoryboardVideoPromptAllowsUserReviewedFormatting(t *testing.T) {
	t.Parallel()
	for _, prompt := range []string{
		"5秒单镜头：@林黛玉缓慢走入正房。",
		"[00:00-00:05] @林黛玉缓慢走入正房。",
	} {
		if got, err := validateStoryboardVideoPrompt(prompt, []string{"林黛玉"}, true); err != nil || got == "" {
			t.Fatalf("user-reviewed Prompt %q was rejected: %v", prompt, err)
		}
	}
}

func TestValidateStoryboardVideoPromptDoesNotEnforceAssetSyntax(t *testing.T) {
	t.Parallel()
	if got, err := validateStoryboardVideoPrompt("@林黛玉缓慢走入正房。", []string{"林黛玉", "贾母正房"}, true); err != nil || got == "" {
		t.Fatalf("Prompt missing a bound Asset mention was rejected: %v", err)
	}
	if got, err := validateStoryboardVideoPrompt("参考资产：@林黛玉、@贾母正房。\n人物走入房间。", []string{"林黛玉", "贾母正房"}, true); err != nil || got == "" {
		t.Fatalf("generic Asset list was rejected: %v", err)
	}
	if got, err := validateStoryboardVideoPrompt("以@林黛玉为主体，她缓慢走入@贾母正房。", []string{"林黛玉", "贾母正房"}, true); err != nil || got == "" {
		t.Fatalf("semantic Asset mentions were rejected: %q, %v", got, err)
	}
	if got, err := validateStoryboardVideoPrompt("人物缓慢走入房间。", []string{"林黛玉", "贾母正房"}, false); err != nil || got == "" {
		t.Fatalf("keyframe-mode Prompt without Asset mentions was rejected: %q, %v", got, err)
	}
}

func TestStoryboardAssetMentionsMustBeBound(t *testing.T) {
	t.Parallel()
	if hasUnboundStoryboardAssetMention("@林黛玉走入@贾母正房。", []string{"林黛玉", "贾母正房"}) {
		t.Fatal("bound mentions were reported as unbound")
	}
	if !hasUnboundStoryboardAssetMention("@林黛玉走入@荣国府。", []string{"林黛玉"}) {
		t.Fatal("unbound mention was accepted")
	}
}

func TestValidateAgentStoryboardDurationPlan(t *testing.T) {
	t.Parallel()
	operations := func(durations ...int64) []storyboardOperationArgument {
		result := make([]storyboardOperationArgument, 0, len(durations))
		for _, duration := range durations {
			result = append(result, storyboardOperationArgument{
				Type:                   string(changeset.CreateClip),
				PlannedDurationSeconds: duration,
			})
		}
		return result
	}

	if err := validateAgentStoryboardDurationPlan(0, 60_000, operations(4, 4)); err == nil {
		t.Fatal("fragmented four-second Clip plan was accepted")
	}
	if err := validateAgentStoryboardDurationPlan(0, 60_000, operations(4, 8)); err != nil {
		t.Fatalf("balanced Clip plan was rejected: %v", err)
	}
	if err := validateAgentStoryboardDurationPlan(52, 60_000, operations(8)); err != nil {
		t.Fatalf("plan reaching the Episode target was rejected: %v", err)
	}
	if err := validateAgentStoryboardDurationPlan(54, 60_000, operations(8)); err == nil {
		t.Fatal("plan exceeding the Episode target was accepted")
	}
}
