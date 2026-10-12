package changeset

import (
	"errors"
	"strings"
	"testing"
	"time"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestClipSplitAllowsIndependentPlannedDurations(t *testing.T) {
	t.Parallel()
	for _, durations := range [][]int64{{4, 4, 4}, {4, 4}} {
		value := clipSplitForDurationTest(t, durations)
		if err := value.Validate(); err != nil {
			t.Fatalf("11-second source split into %v: %v", durations, err)
		}
	}
}

func TestClipSplitStillRejectsInvalidIndividualDuration(t *testing.T) {
	t.Parallel()
	for _, seconds := range []int64{0, 3, project.MaxPortableClipDurationSeconds + 1} {
		value := clipSplitForDurationTest(t, []int64{4, seconds})
		if err := value.Validate(); !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("replacement duration %d: error = %v", seconds, err)
		}
	}
}

func clipSplitForDurationTest(t *testing.T, durations []int64) ClipSplit {
	t.Helper()
	at := time.UnixMilli(1_000).UTC()
	source := project.Clip{
		ID:        project.ClipID("clp_" + strings.Repeat("S", 26)),
		ProjectID: project.ProjectID("prj_" + suffix), EpisodeID: project.EpisodeID("ep_" + suffix),
		SceneID: project.SceneID("scn_" + suffix), Position: project.StoryboardOrderStep,
		Title: "源视频单元", PlannedDurationSeconds: 11, GenerationMode: project.ClipGenerationFirstImage,
		ContinueFromPrevious: true, Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	value := ClipSplit{Source: source}
	for index, seconds := range durations {
		clip := source
		clip.ID = project.ClipID("clp_" + strings.Repeat(string(rune('A'+index)), 26))
		clip.Position += index
		clip.PlannedDurationSeconds = seconds
		clip.ContinueFromPrevious = index == 0
		value.Replacements = append(value.Replacements, ClipSplitReplacement{Clip: clip})
	}
	for _, kind := range []project.MediaSlotKind{project.MediaSlotClipFirstImage, project.MediaSlotClipLastImage} {
		id, err := project.FixedMediaSlotID(source.ID.String(), kind)
		if err != nil {
			t.Fatal(err)
		}
		frame := ClipMergeFrame{Kind: kind, SourceSlotID: id, ExpectedSourceSlotRevision: 1}
		if kind == project.MediaSlotClipFirstImage {
			value.FirstFrame = frame
		} else {
			value.LastFrame = frame
		}
	}
	return value
}
