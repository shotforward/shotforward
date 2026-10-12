package project

import (
	"errors"
	"testing"
	"time"
)

func TestStoryboardAllowsOptionalImagesAndPromptsAndUsesWholeSeconds(t *testing.T) {
	t.Parallel()
	scene, clip := validStoryboardModels(t)
	if clip.FirstImagePrompt != "" || clip.LastImagePrompt != "" || clip.VideoPrompts != (ClipVideoPrompts{}) {
		t.Fatal("test Clip prompts should be optional")
	}
	view := Storyboard{EpisodeID: scene.EpisodeID, Revision: 1, PlannedDurationSeconds: 5, Scenes: []StoryboardScene{{Scene: scene, Clips: []ClipDetails{{Clip: clip, AssetReferences: []ClipAssetReference{}}}}}}
	if err := view.Validate(); err != nil {
		t.Fatalf("Storyboard.Validate() error = %v", err)
	}
}

func TestClipValidationRejectsFractionalRepresentationAndInvalidMode(t *testing.T) {
	t.Parallel()
	_, clip := validStoryboardModels(t)
	clip.PlannedDurationSeconds = 0
	if err := clip.Validate(); !errors.Is(err, ErrInvalidClip) {
		t.Fatalf("zero duration error = %v", err)
	}
	_, clip = validStoryboardModels(t)
	clip.GenerationMode = "unknown"
	if err := clip.Validate(); !errors.Is(err, ErrInvalidClipGenerationMode) {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestSceneScriptSourceRequiresExactByteLength(t *testing.T) {
	t.Parallel()
	scene, _ := validStoryboardModels(t)
	scene.Source = &SceneScriptSource{ScriptRevision: 1, StartByte: 0, EndByte: 1, Excerpt: "雨"}
	if err := scene.Validate(); !errors.Is(err, ErrInvalidScene) {
		t.Fatalf("mismatched source byte length error = %v", err)
	}
}

func validStoryboardModels(t *testing.T) (Scene, Clip) {
	t.Helper()
	projectID, _ := ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	episodeID, _ := ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	sceneID, _ := ParseSceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	clipID, _ := ParseClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	now := time.Date(2026, time.September, 3, 12, 0, 0, 123_000_000, time.UTC)
	scene := Scene{ID: sceneID, ProjectID: projectID, EpisodeID: episodeID, Position: 1, Title: "雨夜街道", Revision: 1, CreatedAt: now, UpdatedAt: now}
	clip := Clip{ID: clipID, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Position: 1, Title: "相遇", PlannedDurationSeconds: 5, GenerationMode: ClipGenerationFirstImage, Revision: 1, CreatedAt: now, UpdatedAt: now}
	return scene, clip
}
