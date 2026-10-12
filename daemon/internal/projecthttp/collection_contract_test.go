package projecthttp

import (
	"encoding/json"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// Keep the media, generation and storyboard projections safe to iterate in the
// desktop even when persistence returns nil for an empty collection.
func TestEmptyMediaCollectionsUseJSONArrayContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value any
		paths [][]string
	}{
		{"media slot", mapMediaSlotDetails(project.MediaSlotDetails{}), [][]string{{"versions"}, {"artifacts"}}},
		{"generation snapshot", mapGenerationSnapshot(project.GenerationInputSnapshot{ParametersJSON: "{}"}), [][]string{{"references"}}},
		{"generation batch", mapGenerationBatch(project.GenerationBatchDetails{}), [][]string{{"jobs"}}},
		{"clip", mapClipDetails(project.ClipDetails{}), [][]string{{"asset_references"}, {"derived_from_clip_ids"}}},
		{"scene", mapStoryboardScene(project.StoryboardScene{}), [][]string{{"clips"}}},
		{"storyboard", mapStoryboardResult(projectapp.StoryboardResult{}), [][]string{{"storyboard", "scenes"}}},
		{"split clips", mapReplaceClipsResult(projectapp.ReplaceClipsResult{}), [][]string{{"created_clips"}, {"replaced_clips"}, {"storyboard", "scenes"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range test.paths {
				current := json.RawMessage(encoded)
				for _, field := range path {
					var object map[string]json.RawMessage
					if err := json.Unmarshal(current, &object); err != nil {
						t.Fatal(err)
					}
					current = object[field]
				}
				if string(current) != "[]" {
					t.Errorf("%v = %s, want []", path, current)
				}
			}
		})
	}
}
