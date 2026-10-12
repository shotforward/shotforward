package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestStoryboardOrderingMovementAndRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	store := newTestStore(t, path)
	project, episode := testModels(t)
	episode.Script.Markdown = "## 雨夜\n林晚走进街道。"
	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}

	at := episode.UpdatedAt.Add(time.Second)
	sceneA := storyboardScene(t, project.ID, episode.ID, "A", domain.StoryboardOrderStep, "街道", at)
	sceneA.Source = &domain.SceneScriptSource{ScriptRevision: 1, StartByte: 0, EndByte: len(episode.Script.Markdown), Excerpt: episode.Script.Markdown}
	if err := store.CreateScene(ctx, sceneA, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	sceneB := storyboardScene(t, project.ID, episode.ID, "B", 2*domain.StoryboardOrderStep, "室内", at)
	if err := store.CreateScene(ctx, sceneB, 2, at); err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Second)
	clipA := storyboardClip(t, project.ID, episode.ID, sceneA.ID, "A", domain.StoryboardOrderStep, 5, at)
	if err := store.CreateClip(ctx, clipA, 3, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clipB := storyboardClip(t, project.ID, episode.ID, sceneA.ID, "B", 2*domain.StoryboardOrderStep, 7, at)
	clipB.GenerationMode = domain.ClipGenerationFirstLastImage
	if err := store.CreateClip(ctx, clipB, 4, at); err != nil {
		t.Fatal(err)
	}

	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 5 || view.PlannedDurationSeconds != 12 || len(view.Scenes) != 2 || view.Scenes[0].Scene.ID != sceneA.ID || view.Scenes[0].Clips[0].Clip.ID != clipA.ID {
		t.Fatalf("Storyboard after append = %#v", view)
	}

	at = at.Add(time.Second)
	clipB.SceneID = sceneB.ID
	clipB.Position = domain.StoryboardOrderStep
	clipB.Revision++
	clipB.UpdatedAt = at
	if err := store.MoveClip(ctx, clipB, "", "", 1, 5, at); err != nil {
		t.Fatal(err)
	}
	view, err = store.Storyboard(ctx, episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 6 || len(view.Scenes[0].Clips) != 1 || view.Scenes[1].Clips[0].Clip.ID != clipB.ID || view.Scenes[0].Clips[0].Clip.Position != domain.StoryboardOrderStep {
		t.Fatalf("Storyboard after move = %#v", view)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.Storyboard(ctx, episode.ID)
	if err != nil || restored.Revision != 6 || restored.PlannedDurationSeconds != 12 {
		t.Fatalf("reopened Storyboard = %#v, %v", restored, err)
	}
}

func TestSceneCascadeRestorePreservesIndependentlyDeletedClip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "C", 1, "车站", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	first := storyboardClip(t, project.ID, episode.ID, scene.ID, "C", 1, 4, at)
	if err := store.CreateClip(ctx, first, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	second := storyboardClip(t, project.ID, episode.ID, scene.ID, "D", 2, 6, at)
	if err := store.CreateClip(ctx, second, 3, at); err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Second)
	first.Revision++
	first.UpdatedAt = at
	first.DeletedAt = &at
	if err := store.DeleteClip(ctx, first, 1, 4, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	scene.Revision++
	scene.UpdatedAt = at
	scene.DeletedAt = &at
	if err := store.DeleteScene(ctx, scene, 1, 5, false, at); !errors.Is(err, ErrSceneNotEmpty) {
		t.Fatalf("unconfirmed Scene deletion error = %v", err)
	}
	if err := store.DeleteScene(ctx, scene, 1, 5, true, at); err != nil {
		t.Fatal(err)
	}
	cascade, err := store.Clip(ctx, second.ID)
	if err != nil || cascade.Clip.DeletedWithSceneID != scene.ID {
		t.Fatalf("cascade Clip = %#v, %v", cascade, err)
	}

	at = at.Add(time.Second)
	scene.Revision++
	scene.UpdatedAt = at
	scene.DeletedAt = nil
	if err := store.RestoreScene(ctx, scene, 2, 6, at); err != nil {
		t.Fatal(err)
	}
	firstAfter, _ := store.Clip(ctx, first.ID)
	secondAfter, _ := store.Clip(ctx, second.ID)
	if !firstAfter.Clip.IsDeleted() || secondAfter.Clip.IsDeleted() || secondAfter.Clip.DeletedWithSceneID != "" {
		t.Fatalf("restored Clips: independent=%#v cascade=%#v", firstAfter.Clip, secondAfter.Clip)
	}
}

func TestDeletedStoryboardItemsKeepsCascadeClipsInsideTheirScene(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, value, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, value.ID, episode.ID, "T", 1, "Deleted scene", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	independent := storyboardClip(t, value.ID, episode.ID, scene.ID, "T", 1, 4, at)
	if err := store.CreateClip(ctx, independent, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	cascade := storyboardClip(t, value.ID, episode.ID, scene.ID, "U", 2, 5, at)
	if err := store.CreateClip(ctx, cascade, 3, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	independent.Revision++
	independent.UpdatedAt = at
	independent.DeletedAt = &at
	if err := store.DeleteClip(ctx, independent, 1, 4, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	scene.Revision++
	scene.UpdatedAt = at
	scene.DeletedAt = &at
	if err := store.DeleteScene(ctx, scene, 1, 5, true, at); err != nil {
		t.Fatal(err)
	}

	scenes, clips, err := store.DeletedStoryboardItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) != 1 || scenes[0].Scene.ID != scene.ID || len(scenes[0].Clips) != 1 || scenes[0].Clips[0].Clip.ID != cascade.ID {
		t.Fatalf("deleted Scene groups = %#v", scenes)
	}
	if len(clips) != 1 || clips[0].Clip.ID != independent.ID {
		t.Fatalf("independently deleted Clips = %#v", clips)
	}
}

func TestReorderScenesRequiresExactActiveSetAndIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	first := storyboardScene(t, project.ID, episode.ID, "F", 1, "第一场", at)
	if err := store.CreateScene(ctx, first, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	second := storyboardScene(t, project.ID, episode.ID, "G", 2, "第二场", at)
	if err := store.CreateScene(ctx, second, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	if err := store.ReorderScenes(ctx, episode.ID, []domain.SceneID{second.ID, first.ID}, 3, at); err != nil {
		t.Fatal(err)
	}
	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil || view.Revision != 4 || view.Scenes[0].Scene.ID != second.ID || view.Scenes[0].Scene.Revision != 1 || view.Scenes[1].Scene.ID != first.ID {
		t.Fatalf("reordered Storyboard = %#v, %v", view, err)
	}
	if err := store.ReorderScenes(ctx, episode.ID, []domain.SceneID{first.ID}, 4, at.Add(time.Second)); !errors.Is(err, ErrSceneOrderMismatch) {
		t.Fatalf("incomplete reorder error = %v", err)
	}
	unchanged, err := store.Storyboard(ctx, episode.ID)
	if err != nil || unchanged.Revision != 4 || unchanged.Scenes[0].Scene.ID != second.ID {
		t.Fatalf("Storyboard changed after rejected reorder = %#v, %v", unchanged, err)
	}
}

func TestClipAssetReferenceReplaceAndRemovalKeepContiguousOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	assets := make([]domain.Asset, 0, 4)
	for index, suffix := range []string{"H", "I", "J", "K"} {
		identity, asset := assetModels(t, project.ID, episode.ID, suffix, suffix, domain.AssetKindProp, "道具"+suffix, "", at.Add(time.Duration(index)*time.Second))
		if err := store.CreateAsset(ctx, &identity, asset, asset.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset)
	}
	at = at.Add(5 * time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "H", 1, "室内", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "H", 1, 5, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		at = at.Add(time.Second)
		if err := store.BindClipAsset(ctx, clip.ID, assets[index].ID, int64(index+1), at); err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(time.Second)
	if err := store.ReplaceClipAsset(ctx, clip.ID, assets[1].ID, assets[3].ID, 4, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	if err := store.UnbindClipAsset(ctx, clip.ID, assets[0].ID, 5, at); err != nil {
		t.Fatal(err)
	}
	details, err := store.Clip(ctx, clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Clip.Revision != 6 || len(details.AssetReferences) != 2 || details.AssetReferences[0].AssetID != assets[3].ID || details.AssetReferences[0].Position != 1 || details.AssetReferences[1].AssetID != assets[2].ID || details.AssetReferences[1].Position != 2 {
		t.Fatalf("Clip references = %#v", details)
	}
}

func TestMoveDeleteAndRestoreClipMaintainPositions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "L", 1, "街道", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	clips := make([]domain.Clip, 0, 3)
	for index, suffix := range []string{"L", "M", "N"} {
		at = at.Add(time.Second)
		clip := storyboardClip(t, project.ID, episode.ID, scene.ID, suffix, index+1, 3, at)
		if err := store.CreateClip(ctx, clip, int64(index+2), at); err != nil {
			t.Fatal(err)
		}
		clips = append(clips, clip)
	}
	at = at.Add(time.Second)
	clips[2].Revision++
	clips[2].UpdatedAt = at
	if err := store.MoveClip(ctx, clips[2], clips[0].ID, "", 1, 5, at); err != nil {
		t.Fatal(err)
	}
	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil || view.Scenes[0].Clips[0].Clip.ID != clips[2].ID || view.Scenes[0].Clips[1].Clip.ID != clips[0].ID || view.Scenes[0].Clips[2].Clip.ID != clips[1].ID {
		t.Fatalf("after forward move = %#v, %v", view, err)
	}

	at = at.Add(time.Second)
	clips[2].Revision++
	clips[2].UpdatedAt = at
	if err := store.MoveClip(ctx, clips[2], "", "", 2, 6, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	middle, err := store.Clip(ctx, clips[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted := middle.Clip
	deleted.Revision++
	deleted.UpdatedAt = at
	deleted.DeletedAt = &at
	if err := store.DeleteClip(ctx, deleted, middle.Clip.Revision, 7, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	restored := deleted
	restored.Revision++
	restored.UpdatedAt = at
	restored.DeletedAt = nil
	if err := store.RestoreClip(ctx, restored, deleted.Revision, 8, at); err != nil {
		t.Fatal(err)
	}
	view, err = store.Storyboard(ctx, episode.ID)
	if err != nil || view.Revision != 9 || len(view.Scenes[0].Clips) != 3 {
		t.Fatalf("restored Storyboard = %#v, %v", view, err)
	}
	for index, details := range view.Scenes[0].Clips {
		if details.Clip.Position != (index+1)*domain.StoryboardOrderStep {
			t.Fatalf("Clip order keys are not normalized: %#v", view.Scenes[0].Clips)
		}
	}
}

func TestClipAssetBindingUsageAndConfirmedAssetDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	identity, asset := assetModels(t, project.ID, episode.ID, "R", "R", domain.AssetKindCharacter, "林晚", "", at)
	if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "E", 1, "街道", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "E", 1, 5, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	if err := store.BindClipAsset(ctx, clip.ID, asset.ID, 1, at); err != nil {
		t.Fatal(err)
	}
	if err := store.BindClipAsset(ctx, clip.ID, asset.ID, 2, at.Add(time.Second)); !errors.Is(err, ErrClipAssetAlreadyBound) {
		t.Fatalf("duplicate bind error = %v", err)
	}
	usage, err := store.AssetUsage(ctx, asset.ID)
	if err != nil || len(usage) != 1 || usage[0].ID != clip.ID {
		t.Fatalf("AssetUsage() = %#v, %v", usage, err)
	}

	at = at.Add(2 * time.Second)
	asset.Revision++
	asset.UpdatedAt = at
	asset.DeletedAt = &at
	if err := store.DeleteAsset(ctx, asset, 1, false, at); !errors.Is(err, ErrAssetInUse) {
		t.Fatalf("unconfirmed deletion error = %v", err)
	}
	unchanged, _ := store.Asset(ctx, asset.ID)
	if unchanged.Asset.IsDeleted() || unchanged.Asset.Revision != 1 {
		t.Fatalf("Asset changed after rejected deletion: %#v", unchanged.Asset)
	}
	if err := store.DeleteAsset(ctx, asset, 1, true, at); err != nil {
		t.Fatal(err)
	}
	details, err := store.Clip(ctx, clip.ID)
	if err != nil || len(details.AssetReferences) != 1 || details.AssetReferences[0].AssetID != asset.ID || !details.AssetReferences[0].AssetDeleted || details.AssetReferences[0].Name != identity.Name {
		t.Fatalf("references after Asset deletion = %#v, %v", details, err)
	}
}

func initializedStoryboardStore(t *testing.T) (*Store, domain.Project, domain.Episode) {
	t.Helper()
	store := newTestStore(t, filepath.Join(t.TempDir(), "project.db"))
	project, episode := testModels(t)
	if err := store.Initialize(context.Background(), project, episode); err != nil {
		t.Fatal(err)
	}
	return store, project, episode
}

func TestReplaceClipsSplitsAndMergesAtomicallyWithDerivationAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "P", 1, "站台", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	sources := make([]domain.Clip, 0, 3)
	for index, item := range []struct {
		suffix  string
		seconds int64
	}{{"Q", 4}, {"R", 6}, {"S", 8}} {
		at = at.Add(time.Second)
		clip := storyboardClip(t, project.ID, episode.ID, scene.ID, item.suffix, index+1, item.seconds, at)
		if err := store.CreateClip(ctx, clip, int64(index+2), at); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, clip)
	}

	at = at.Add(time.Second)
	first := storyboardClip(t, project.ID, episode.ID, scene.ID, "T", 2, 2, at)
	second := storyboardClip(t, project.ID, episode.ID, scene.ID, "U", 3, 4, at)
	bad := second
	bad.PlannedDurationSeconds = 0
	if err := store.ReplaceClips(ctx, []domain.Clip{sources[1]}, []domain.ClipReplacement{
		{Clip: first, SourceClipIDs: []domain.ClipID{sources[1].ID}},
		{Clip: bad, SourceClipIDs: []domain.ClipID{sources[1].ID}},
	}, 5, at); !errors.Is(err, ErrClipStructureConflict) {
		t.Fatalf("ReplaceClips(invalid duration) error = %v", err)
	}
	unchanged, err := store.Storyboard(ctx, episode.ID)
	if err != nil || unchanged.Revision != 5 || unchanged.Scenes[0].Clips[1].Clip.ID != sources[1].ID {
		t.Fatalf("failed Split changed Storyboard = %#v, %v", unchanged, err)
	}

	if err := store.ReplaceClips(ctx, []domain.Clip{sources[1]}, []domain.ClipReplacement{
		{Clip: first, SourceClipIDs: []domain.ClipID{sources[1].ID}},
		{Clip: second, SourceClipIDs: []domain.ClipID{sources[1].ID}},
	}, 5, at); err != nil {
		t.Fatal(err)
	}
	split, err := store.Storyboard(ctx, episode.ID)
	if err != nil || split.Revision != 6 || len(split.Scenes[0].Clips) != 4 || split.PlannedDurationSeconds != 18 {
		t.Fatalf("split Storyboard = %#v, %v", split, err)
	}
	wantSplitOrder := []domain.ClipID{sources[0].ID, first.ID, second.ID, sources[2].ID}
	for index, id := range wantSplitOrder {
		if split.Scenes[0].Clips[index].Clip.ID != id || split.Scenes[0].Clips[index].Clip.Position != (index+1)*domain.StoryboardOrderStep {
			t.Fatalf("split order = %#v", split.Scenes[0].Clips)
		}
	}
	if split.Scenes[0].Clips[1].DerivedFromClipIDs[0] != sources[1].ID || split.Scenes[0].Clips[2].DerivedFromClipIDs[0] != sources[1].ID {
		t.Fatalf("split derivations = %#v", split.Scenes[0].Clips)
	}
	deletedSource, err := store.Clip(ctx, sources[1].ID)
	if err != nil || !deletedSource.Clip.IsDeleted() || deletedSource.Clip.Revision != 2 {
		t.Fatalf("split source = %#v, %v", deletedSource, err)
	}

	at = at.Add(time.Second)
	merged := storyboardClip(t, project.ID, episode.ID, scene.ID, "V", 2, 6, at)
	if err := store.ReplaceClips(ctx, []domain.Clip{first, second}, []domain.ClipReplacement{
		{Clip: merged, SourceClipIDs: []domain.ClipID{first.ID, second.ID}},
	}, 6, at); err != nil {
		t.Fatal(err)
	}
	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil || view.Revision != 7 || len(view.Scenes[0].Clips) != 3 || view.PlannedDurationSeconds != 18 {
		t.Fatalf("merged Storyboard = %#v, %v", view, err)
	}
	wantMergeOrder := []domain.ClipID{sources[0].ID, merged.ID, sources[2].ID}
	for index, id := range wantMergeOrder {
		if view.Scenes[0].Clips[index].Clip.ID != id || view.Scenes[0].Clips[index].Clip.Position != (index+1)*domain.StoryboardOrderStep {
			t.Fatalf("merged order = %#v", view.Scenes[0].Clips)
		}
	}
	if got := view.Scenes[0].Clips[1].DerivedFromClipIDs; len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("merge derivations = %#v", got)
	}
}

func TestReplaceClipsRejectsActiveGenerationJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, artifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	videoSlotID, err := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	if err != nil {
		t.Fatal(err)
	}
	at := artifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, artifact.ID, "Z", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	first := storyboardClip(t, project.ID, episode.ID, clip.SceneID, "X", clip.Position, 2, at)
	second := storyboardClip(t, project.ID, episode.ID, clip.SceneID, "Y", clip.Position+1, clip.PlannedDurationSeconds-2, at)
	err = store.ReplaceClips(ctx, []domain.Clip{clip}, []domain.ClipReplacement{
		{Clip: first, SourceClipIDs: []domain.ClipID{clip.ID}},
		{Clip: second, SourceClipIDs: []domain.ClipID{clip.ID}},
	}, 3, at)
	if !errors.Is(err, ErrClipHasActiveGeneration) {
		t.Fatalf("ReplaceClips() error = %v, want ErrClipHasActiveGeneration", err)
	}
	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil || view.Revision != 3 || len(view.Scenes[0].Clips) != 1 || view.Scenes[0].Clips[0].Clip.ID != clip.ID {
		t.Fatalf("rejected Split changed Storyboard = %#v, %v", view, err)
	}
}

func TestResetStoryboardCancelsQueuedJobsAndSoftDeletesAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, artifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := artifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, artifact.ID, "R", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	changed, reset, err := store.ResetStoryboard(ctx, episode.ID, 3, false, at.Add(time.Second))
	if err != nil || !reset || len(changed) != 1 || changed[0].State != domain.GenerationJobCancelled {
		t.Fatalf("ResetStoryboard() = %#v, reset=%v, err=%v", changed, reset, err)
	}
	view, err := store.Storyboard(ctx, episode.ID)
	if err != nil || view.Revision != 4 || len(view.Scenes) != 0 {
		t.Fatalf("reset Storyboard = %#v, %v", view, err)
	}
	deleted, err := store.Clip(ctx, clip.ID)
	if err != nil || !deleted.Clip.IsDeleted() || deleted.Clip.DeletedWithSceneID != clip.SceneID {
		t.Fatalf("soft-deleted Clip = %#v, %v", deleted, err)
	}
}

func TestResetStoryboardWaitsForRequestedStops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, artifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := artifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, artifact.ID, "S", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	running, err := store.ClaimGenerationJob(ctx, job.ID, job.Revision, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	changed, reset, err := store.ResetStoryboard(ctx, episode.ID, 3, true, at.Add(2*time.Second))
	if err != nil || reset || len(changed) != 1 || changed[0].State != domain.GenerationJobStopRequested {
		t.Fatalf("first ResetStoryboard() = %#v, reset=%v, err=%v", changed, reset, err)
	}
	view, _ := store.Storyboard(ctx, episode.ID)
	if view.Revision != 3 || len(view.Scenes) != 1 {
		t.Fatalf("Storyboard changed before stop completed: %#v", view)
	}
	stopped, err := store.FinishGenerationJob(ctx, running.ID, changed[0].Revision, domain.GenerationJobStopped, "", "", at.Add(3*time.Second))
	if err != nil || stopped.State != domain.GenerationJobStopped {
		t.Fatalf("FinishGenerationJob() = %#v, %v", stopped, err)
	}
	changed, reset, err = store.ResetStoryboard(ctx, episode.ID, 3, true, at.Add(4*time.Second))
	if err != nil || !reset || len(changed) != 0 {
		t.Fatalf("second ResetStoryboard() = %#v, reset=%v, err=%v", changed, reset, err)
	}
	view, _ = store.Storyboard(ctx, episode.ID)
	if view.Revision != 4 || len(view.Scenes) != 0 {
		t.Fatalf("Storyboard after stopped Job = %#v", view)
	}
}

func TestResetStoryboardRevisionConflictDoesNotCancelQueuedJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, artifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := artifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, artifact.ID, "T", 1, at)
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResetStoryboard(ctx, episode.ID, 2, false, at.Add(time.Second)); !errors.Is(err, ErrStoryboardRevisionConflict) {
		t.Fatalf("ResetStoryboard() error = %v", err)
	}
	unchanged, err := store.GenerationJob(ctx, job.ID)
	if err != nil || unchanged.State != domain.GenerationJobQueued || unchanged.Revision != 1 {
		t.Fatalf("queued Job changed after conflict = %#v, %v", unchanged, err)
	}
}

func TestResetStoryboardUnsupportedStopRollsBackEntirePreparation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, clip, artifact := initializedGenerationStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	videoSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	at := artifact.CreatedAt.Add(time.Second)
	batch, job := generationBatchAndJob(t, project, episode, videoSlotID, artifact.ID, "U", 1, at)
	job.StopSupported = false
	if err := store.CreateGenerationBatch(ctx, batch, []domain.GenerationJob{job}, at); err != nil {
		t.Fatal(err)
	}
	running, err := store.ClaimGenerationJob(ctx, job.ID, 1, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResetStoryboard(ctx, episode.ID, 3, true, at.Add(2*time.Second)); !errors.Is(err, ErrGenerationStopUnsupported) {
		t.Fatalf("ResetStoryboard() error = %v", err)
	}
	unchanged, err := store.GenerationJob(ctx, job.ID)
	if err != nil || unchanged.State != domain.GenerationJobRunning || unchanged.Revision != running.Revision {
		t.Fatalf("unsupported running Job changed = %#v, %v", unchanged, err)
	}
	view, _ := store.Storyboard(ctx, episode.ID)
	if view.Revision != 3 || len(view.Scenes) != 1 {
		t.Fatalf("Storyboard changed after unsupported stop = %#v", view)
	}
}

func storyboardScene(t *testing.T, projectID domain.ProjectID, episodeID domain.EpisodeID, suffix string, position int, title string, at time.Time) domain.Scene {
	t.Helper()
	id, err := domain.ParseSceneID("scn_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return domain.Scene{ID: id, ProjectID: projectID, EpisodeID: episodeID, Position: position, Title: title, Revision: 1, CreatedAt: at, UpdatedAt: at}
}
func storyboardClip(t *testing.T, projectID domain.ProjectID, episodeID domain.EpisodeID, sceneID domain.SceneID, suffix string, position int, seconds int64, at time.Time) domain.Clip {
	t.Helper()
	id, err := domain.ParseClipID("clp_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return domain.Clip{ID: id, ProjectID: projectID, EpisodeID: episodeID, SceneID: sceneID, Position: position, Title: "镜头 " + suffix, PlannedDurationSeconds: seconds, GenerationMode: domain.ClipGenerationFirstImage, Revision: 1, CreatedAt: at, UpdatedAt: at}
}
