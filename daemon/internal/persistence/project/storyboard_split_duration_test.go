package projectstore

import (
	"context"
	"errors"
	"testing"
	"time"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestReplaceClipsAllowsSplitDurationToIncreaseOrDecrease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		durations []int64
		total     int64
	}{{"increase", []int64{4, 4, 4}, 24}, {"decrease", []int64{4, 4}, 20}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, projectValue, episode := initializedStoryboardStore(t)
			defer store.Close()
			at := episode.UpdatedAt.Add(time.Second)
			scene := storyboardScene(t, projectValue.ID, episode.ID, "A", 1, "拆分场景", at)
			if err := store.CreateScene(ctx, scene, 1, at); err != nil {
				t.Fatal(err)
			}
			var sources []project.Clip
			for index, seconds := range []int64{4, 11, 8} {
				at = at.Add(time.Second)
				clip := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, string(rune('A'+index)), index+1, seconds, at)
				if err := store.CreateClip(ctx, clip, int64(index+2), at); err != nil {
					t.Fatal(err)
				}
				sources = append(sources, clip)
			}
			at = at.Add(time.Second)
			var replacements []project.ClipReplacement
			for index, seconds := range test.durations {
				clip := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, string(rune('D'+index)), index+2, seconds, at)
				replacements = append(replacements, project.ClipReplacement{Clip: clip, SourceClipIDs: []project.ClipID{sources[1].ID}})
			}
			if err := store.ReplaceClips(ctx, []project.Clip{sources[1]}, replacements, 5, at); err != nil {
				t.Fatal(err)
			}
			view, err := store.Storyboard(ctx, episode.ID)
			if err != nil || view.Revision != 6 || view.PlannedDurationSeconds != test.total || len(view.Scenes[0].Clips) != len(test.durations)+2 {
				t.Fatalf("split Storyboard = %+v, %v", view, err)
			}
			for index, replacement := range replacements {
				clip := view.Scenes[0].Clips[index+1]
				if clip.Clip.ID != replacement.Clip.ID || clip.Clip.PlannedDurationSeconds != test.durations[index] ||
					len(clip.DerivedFromClipIDs) != 1 || clip.DerivedFromClipIDs[0] != sources[1].ID {
					t.Fatalf("split Clip = %+v", clip)
				}
			}
			if view.Scenes[0].Clips[0].Clip.ID != sources[0].ID || view.Scenes[0].Clips[len(view.Scenes[0].Clips)-1].Clip.ID != sources[2].ID {
				t.Fatalf("split reordered other Clips = %+v", view)
			}
		})
	}
}

func TestClipReplacementStillRequiresMergeDurationConservation(t *testing.T) {
	t.Parallel()
	projectValue, episode := testModels(t)
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, projectValue.ID, episode.ID, "S", 1, "合并场景", at)
	first := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, "A", 1, 4, at)
	second := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, "B", 2, 11, at)
	merged := storyboardClip(t, projectValue.ID, episode.ID, scene.ID, "C", 1, 14, at)
	err := validateClipReplacements([]project.Clip{first, second}, []project.ClipReplacement{{Clip: merged, SourceClipIDs: []project.ClipID{first.ID, second.ID}}}, 1, at)
	if !errors.Is(err, ErrClipStructureConflict) {
		t.Fatalf("merge duration mismatch error = %v", err)
	}
}
