package changesetapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
)

func TestSplitClipReplansDurationAndUndoRestoresOriginal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		durations []int64
		total     int64
	}{{"increase_11_to_12", []int64{4, 4, 4}, 61}, {"decrease_11_to_8", []int64{4, 4}, 57}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newSplitApplicationFixture(t)
			defer fixture.store.Close()
			before, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
			if err != nil || before.PlannedDurationSeconds != 60 {
				t.Fatalf("original Storyboard = %+v, %v", before, err)
			}
			begin := fixture.begin(t)
			appended, err := fixture.service.AppendOperations(ctx, changesetapp.AppendOperationsInput{
				ProjectID: fixture.project.ID, ChangeSetID: begin.ID, ExpectedRevision: begin.Revision,
				OperationKey: "append-split", Operations: []changesetapp.OperationDraft{fixture.draft(before.Revision, test.durations)},
			})
			if err != nil {
				t.Fatal(err)
			}
			operations, err := fixture.service.Operations(ctx, fixture.project.ID, begin.ID, 0, 100)
			if err != nil || len(operations) != 1 || operations[0].ClipSplit == nil {
				t.Fatalf("frozen operations = %+v, %v", operations, err)
			}
			split := operations[0].ClipSplit
			finalized, err := fixture.service.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: fixture.project.ID,
				ChangeSetID: begin.ID, ExpectedRevision: appended.ChangeSet.Revision, OperationKey: "finalize-split"})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
			if err != nil || pending.Revision != before.Revision || pending.PlannedDurationSeconds != 60 || len(pending.Scenes[0].Clips) != 3 {
				t.Fatalf("pending split mutated Storyboard = %+v, %v", pending, err)
			}
			applied, err := fixture.service.Apply(ctx, changesetapp.ApplyInput{ProjectID: fixture.project.ID,
				ChangeSetID: begin.ID, ExpectedRevision: finalized.ChangeSet.Revision, OperationKey: "apply-split"})
			if err != nil || applied.ChangeSet.Status != changeset.StatusApplied {
				t.Fatalf("Apply() = %+v, %v", applied, err)
			}
			view, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
			if err != nil || view.Revision != before.Revision+1 || view.PlannedDurationSeconds != test.total || len(view.Scenes[0].Clips) != len(test.durations)+2 {
				t.Fatalf("applied split = %+v, %v", view, err)
			}
			// The increasing case intentionally exceeds the Episode's 60-second
			// creation budget. An explicit split also permits a 4-second average.
			wantOrder := []project.ClipID{fixture.clips[0].ID}
			for index, replacement := range split.Replacements {
				wantOrder = append(wantOrder, replacement.Clip.ID)
				clip := view.Scenes[0].Clips[index+1]
				if clip.Clip.ID != replacement.Clip.ID || clip.Clip.PlannedDurationSeconds != test.durations[index] ||
					clip.Clip.ContinueFromPrevious != (index == 0) || len(clip.DerivedFromClipIDs) != 1 || clip.DerivedFromClipIDs[0] != fixture.clips[1].ID ||
					len(clip.AssetReferences) != 1 || clip.AssetReferences[0].AssetID != fixture.asset.ID {
					t.Fatalf("replacement %d = %+v", index, clip)
				}
			}
			wantOrder = append(wantOrder, fixture.clips[2].ID)
			assertSplitClipOrder(t, view, wantOrder)
			fixture.assertNeighborsUnchanged(t)
			for index, replacement := range split.Replacements {
				for _, kind := range []project.MediaSlotKind{project.MediaSlotClipFirstImage, project.MediaSlotClipLastImage} {
					if index == 0 && kind == project.MediaSlotClipFirstImage || index == len(split.Replacements)-1 && kind == project.MediaSlotClipLastImage {
						continue
					}
					id, _ := project.FixedMediaSlotID(replacement.Clip.ID.String(), kind)
					media, err := fixture.store.MediaSlot(ctx, id, true)
					if err != nil || media.Current != nil || len(media.Versions) != 0 {
						t.Fatalf("inner boundary inherited unexpected media = %+v, %v", media, err)
					}
				}
			}
			for _, frame := range []struct {
				clip  project.ClipID
				kind  project.MediaSlotKind
				value changesetapp.ClipMergeFrameDraft
			}{{split.Replacements[0].Clip.ID, project.MediaSlotClipFirstImage, fixture.firstFrame},
				{split.Replacements[len(split.Replacements)-1].Clip.ID, project.MediaSlotClipLastImage, fixture.lastFrame}} {
				id, _ := project.FixedMediaSlotID(frame.clip.String(), frame.kind)
				media, err := fixture.store.MediaSlot(ctx, id, true)
				if err != nil || media.Current == nil || media.Current.ArtifactID != frame.value.Current.ArtifactID || media.Current.ID == frame.value.Current.ID {
					t.Fatalf("inherited boundary frame = %+v, %v", media, err)
				}
			}
			deleted, err := fixture.store.Clip(ctx, fixture.clips[1].ID)
			if err != nil || !deleted.Clip.IsDeleted() || deleted.Clip.PlannedDurationSeconds != 11 {
				t.Fatalf("original source after split = %+v, %v", deleted, err)
			}
			undone, err := fixture.service.Undo(ctx, changesetapp.UndoInput{ProjectID: fixture.project.ID,
				ChangeSetID: applied.ChangeSet.ID, OperationKey: "undo-split"})
			if err != nil || undone.ChangeSet.Status != changeset.StatusApplied {
				t.Fatalf("Undo() = %+v, %v", undone, err)
			}
			restored, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
			if err != nil || restored.Revision != before.Revision+2 || restored.PlannedDurationSeconds != 60 || len(restored.Scenes[0].Clips) != 3 || restored.Scenes[0].Clips[1].Clip.PlannedDurationSeconds != 11 {
				t.Fatalf("Storyboard after Undo = %+v, %v", restored, err)
			}
			assertSplitClipOrder(t, restored, []project.ClipID{fixture.clips[0].ID, fixture.clips[1].ID, fixture.clips[2].ID})
			fixture.assertNeighborsUnchanged(t)
			for _, frame := range []changesetapp.ClipMergeFrameDraft{fixture.firstFrame, fixture.lastFrame} {
				media, err := fixture.store.MediaSlot(ctx, frame.Slot.ID, true)
				if err != nil || media.Current == nil || media.Current.ID != frame.Current.ID || len(media.Versions) != 1 {
					t.Fatalf("source boundary frame changed after Undo = %+v, %v", media, err)
				}
			}
			for _, replacement := range split.Replacements {
				clip, err := fixture.store.Clip(ctx, replacement.Clip.ID)
				if err != nil || !clip.Clip.IsDeleted() {
					t.Fatalf("replacement survived Undo = %+v, %v", clip, err)
				}
			}
		})
	}
}

func TestStoryboardCreationStillEnforcesEpisodeDurationBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newSplitApplicationFixture(t)
	defer fixture.store.Close()
	before, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	begin := fixture.begin(t)
	appended, err := fixture.service.AppendOperations(ctx, changesetapp.AppendOperationsInput{
		ProjectID: fixture.project.ID, ChangeSetID: begin.ID, ExpectedRevision: begin.Revision, OperationKey: "append-over-budget",
		Operations: []changesetapp.OperationDraft{{Type: changeset.CreateClip, ExpectedRevision: before.Revision,
			ExpectedScriptRevision: fixture.episode.Script.Revision, ClipCreation: &changesetapp.ClipCreationDraft{
				Ref: "extra_clip", ExistingSceneID: fixture.clips[0].SceneID, Position: 4 * project.StoryboardOrderStep,
				Title: "新增视频单元", PlannedDurationSeconds: 6, GenerationMode: project.ClipGenerationFirstImage,
			}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: fixture.project.ID,
		ChangeSetID: begin.ID, ExpectedRevision: appended.ChangeSet.Revision, OperationKey: "finalize-over-budget"})
	if !errors.Is(err, projectstore.ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "storyboard_duration_exceeded") {
		t.Fatalf("over-budget creation Finalize() error = %v", err)
	}
	after, err := fixture.store.Storyboard(ctx, fixture.episode.ID)
	if err != nil || after.Revision != before.Revision || after.PlannedDurationSeconds != 60 || len(after.Scenes[0].Clips) != 3 {
		t.Fatalf("over-budget creation changed Storyboard = %+v, %v", after, err)
	}
}

func TestSplitClipRejectsInvalidDurationWithoutPersistingOperations(t *testing.T) {
	t.Parallel()
	fixture := newSplitApplicationFixture(t)
	defer fixture.store.Close()
	begin := fixture.begin(t)
	before, err := fixture.store.Storyboard(context.Background(), fixture.episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int64{0, 3, project.MaxPortableClipDurationSeconds + 1} {
		_, err := fixture.service.AppendOperations(context.Background(), changesetapp.AppendOperationsInput{
			ProjectID: fixture.project.ID, ChangeSetID: begin.ID, ExpectedRevision: begin.Revision,
			OperationKey: fmt.Sprintf("invalid-split-%d", seconds), Operations: []changesetapp.OperationDraft{fixture.draft(before.Revision, []int64{4, seconds})},
		})
		if !errors.Is(err, changesetapp.ErrInvalidRequest) {
			t.Fatalf("duration %d: AppendOperations() error = %v", seconds, err)
		}
	}
	operations, err := fixture.service.Operations(context.Background(), fixture.project.ID, begin.ID, 0, 100)
	if err != nil || len(operations) != 0 {
		t.Fatalf("invalid splits persisted operations = %+v, %v", operations, err)
	}
	after, err := fixture.store.Storyboard(context.Background(), fixture.episode.ID)
	if err != nil || after.Revision != before.Revision || after.PlannedDurationSeconds != 60 || len(after.Scenes[0].Clips) != 3 {
		t.Fatalf("invalid splits changed Storyboard = %+v, %v", after, err)
	}
}

type splitApplicationFixture struct {
	store                 *projectstore.Store
	service               *changesetapp.Service
	project               project.Project
	episode               project.Episode
	asset                 project.Asset
	clips                 []project.Clip
	firstFrame, lastFrame changesetapp.ClipMergeFrameDraft
}

func newSplitApplicationFixture(t *testing.T) splitApplicationFixture {
	t.Helper()
	ctx := context.Background()
	store, projectValue, episode, asset := applicationFixture(t)
	clock := &stepClock{now: asset.UpdatedAt}
	service, err := changesetapp.NewService(changesetapp.NewSecureIDGenerator(), clock, staticResolver{store: store})
	if err != nil {
		t.Fatal(err)
	}
	fixture := splitApplicationFixture{store: store, service: service, project: projectValue, episode: episode, asset: asset}
	at := clock.Now()
	scene := project.Scene{ID: project.SceneID("scn_" + strings.Repeat("S", 26)), ProjectID: projectValue.ID,
		EpisodeID: episode.ID, Position: project.StoryboardOrderStep, Title: "婚礼", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	for index, seconds := range []int64{24, 11, 25} {
		at = clock.Now()
		clip := project.Clip{ID: project.ClipID("clp_" + strings.Repeat(string(rune('A'+index)), 26)), ProjectID: projectValue.ID,
			EpisodeID: episode.ID, SceneID: scene.ID, Position: (index + 1) * project.StoryboardOrderStep,
			Title: fmt.Sprintf("视频单元 %d", index), PlannedDurationSeconds: seconds, GenerationMode: project.ClipGenerationFirstImage,
			ContinueFromPrevious: index == 1, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if err := store.CreateClip(ctx, clip, int64(index+2), at); err != nil {
			t.Fatal(err)
		}
		fixture.clips = append(fixture.clips, clip)
	}
	if err := store.BindClipAsset(ctx, fixture.clips[1].ID, asset.ID, fixture.clips[1].Revision, clock.Now()); err != nil {
		t.Fatal(err)
	}
	source, err := store.Clip(ctx, fixture.clips[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clips[1] = source.Clip
	for index, kind := range []project.MediaSlotKind{project.MediaSlotClipFirstImage, project.MediaSlotClipLastImage} {
		at = clock.Now()
		suffix := string(rune('F' + index))
		id, _ := project.FixedMediaSlotID(source.Clip.ID.String(), kind)
		artifact := project.Artifact{ID: project.ArtifactID("art_" + strings.Repeat(suffix, 26)), ProjectID: projectValue.ID,
			Kind: project.MediaKindImage, RelativePath: "media/images/" + suffix + ".png", SHA256: strings.Repeat(string(rune('d'+index)), 64),
			ByteSize: 10, MIMEType: "image/png", OriginalFilename: suffix + ".png", Width: 720, Height: 1280, CreatedAt: at}
		version := project.MediaVersion{ID: project.MediaVersionID("mvr_" + strings.Repeat(suffix, 26)), SlotID: id,
			ArtifactID: artifact.ID, SequenceNumber: 1, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if err := store.AddMediaVersion(ctx, artifact, version, 1, true, at); err != nil {
			t.Fatal(err)
		}
		media, err := store.MediaSlot(ctx, id, true)
		if err != nil {
			t.Fatal(err)
		}
		frame := changesetapp.ClipMergeFrameDraft{Slot: media.Slot, Current: media.Current}
		if index == 0 {
			fixture.firstFrame = frame
		} else {
			fixture.lastFrame = frame
		}
	}
	return fixture
}

func (f splitApplicationFixture) begin(t *testing.T) changeset.ChangeSet {
	t.Helper()
	result, err := f.service.BeginDraft(context.Background(), changesetapp.BeginDraftInput{ProjectID: f.project.ID, EpisodeID: f.episode.ID,
		AgentSessionID: agent.SessionID("ags_" + strings.Repeat("S", 26)), AgentTurnID: agent.TurnID("agt_" + strings.Repeat("T", 26)),
		CodexThreadID: "thread-split", CodexTurnID: "turn-split", Title: "拆分视频单元", OperationKey: "begin-split"})
	if err != nil {
		t.Fatal(err)
	}
	return result.ChangeSet
}

func (f splitApplicationFixture) draft(revision int64, durations []int64) changesetapp.OperationDraft {
	split := &changesetapp.ClipSplitDraft{Source: f.clips[1], FirstFrame: f.firstFrame, LastFrame: f.lastFrame}
	for index, seconds := range durations {
		split.Replacements = append(split.Replacements, changesetapp.ClipSplitReplacementDraft{Title: fmt.Sprintf("拆分单元 %d", index),
			PlannedDurationSeconds: seconds, GenerationMode: project.ClipGenerationFirstImage, AssetIDs: []project.AssetID{f.asset.ID}})
	}
	return changesetapp.OperationDraft{Type: changeset.SplitClip, ExpectedRevision: revision, ClipSplit: split}
}

func (f splitApplicationFixture) assertNeighborsUnchanged(t *testing.T) {
	t.Helper()
	for _, index := range []int{0, 2} {
		current, err := f.store.Clip(context.Background(), f.clips[index].ID)
		// Sparse positions may be normalized, but neighboring content and
		// revision must not be rewritten by splitting or undoing another Clip.
		current.Clip.Position = f.clips[index].Position
		if err != nil || current.Clip != f.clips[index] {
			t.Fatalf("neighbor changed: got %+v, want %+v, error %v", current.Clip, f.clips[index], err)
		}
	}
}

func assertSplitClipOrder(t *testing.T, view project.Storyboard, want []project.ClipID) {
	t.Helper()
	for index, id := range want {
		clip := view.Scenes[0].Clips[index].Clip
		if clip.ID != id || clip.Position != (index+1)*project.StoryboardOrderStep {
			t.Fatalf("clip order %d: got %+v, want %s", index, clip, id)
		}
	}
}
