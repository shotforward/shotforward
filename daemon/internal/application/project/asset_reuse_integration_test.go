package projectapp_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

func TestPriorAssetReuseSelectsLatestEpisodeAndKeepsAppearanceIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	service := newRealService(t, library, registry, projectapp.NewSecureIDGenerator(), fixedClock{at: testNow()})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: filepath.Join(t.TempDir(), "asset-continuity"), Name: "角色连续性", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	second, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	third, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{
		ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID,
		Kind: domain.AssetKindCharacter, Name: "布布", Prompt: "青年，蓝色外套",
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := registry.ByProjectID(created.Project.ID)
	addImage := func(asset domain.Asset, suffix string) domain.MediaSlotDetails {
		t.Helper()
		slotID, err := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
		if err != nil {
			t.Fatal(err)
		}
		slot, err := handle.Store.MediaSlot(ctx, slotID, true)
		if err != nil {
			t.Fatal(err)
		}
		artifact := domain.Artifact{
			ID: domain.ArtifactID("art_" + strings.Repeat(suffix, 26)), ProjectID: created.Project.ID,
			Kind: domain.MediaKindImage, RelativePath: "media/images/" + suffix + ".png",
			SHA256: strings.Repeat(strings.ToLower(suffix), 64), ByteSize: 10, MIMEType: "image/png",
			OriginalFilename: suffix + ".png", Width: 720, Height: 1280, CreatedAt: testNow(),
		}
		version := domain.MediaVersion{
			ID: domain.MediaVersionID("mvr_" + strings.Repeat(suffix, 26)), SlotID: slotID,
			ArtifactID: artifact.ID, SequenceNumber: len(slot.Versions) + 1,
			Revision: 1, CreatedAt: testNow(), UpdatedAt: testNow(),
		}
		if err := handle.Store.AddMediaVersion(ctx, artifact, version, slot.Slot.Revision, true, testNow()); err != nil {
			t.Fatal(err)
		}
		slot, err = handle.Store.MediaSlot(ctx, slotID, true)
		if err != nil {
			t.Fatal(err)
		}
		return slot
	}
	addImage(first.Asset.Asset, "A")
	firstMedia := addImage(first.Asset.Asset, "B")
	secondPrompt := "中年，灰色大衣"
	previous, err := service.DeriveAsset(ctx, projectapp.DeriveAssetInput{
		ProjectID: created.Project.ID, EpisodeID: second.Episode.ID, SourceAssetID: first.Asset.Asset.ID,
		Prompt: &secondPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousSlotID, _ := domain.FixedMediaSlotID(previous.Asset.Asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	previousMedia, err := handle.Store.MediaSlot(ctx, previousSlotID, true)
	if err != nil || previousMedia.Current == nil || len(previousMedia.Versions) != 1 ||
		previousMedia.Current.ArtifactID != firstMedia.Current.ArtifactID || previousMedia.Current.ID == firstMedia.Current.ID {
		t.Fatalf("only the selected prior image should be inherited in a new version: %+v, %v", previousMedia, err)
	}
	previousMedia = addImage(previous.Asset.Asset, "C")
	candidates, err := service.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{
		ProjectID: created.Project.ID, EpisodeID: third.Episode.ID, Kind: domain.AssetKindCharacter,
	})
	if err != nil || len(candidates) != 1 || candidates[0].EpisodeSequence != 2 ||
		candidates[0].Asset.Asset.ID != previous.Asset.Asset.ID || candidates[0].Asset.Asset.Prompt != secondPrompt {
		t.Fatalf("latest active episode appearance should win: %+v, %v", candidates, err)
	}
	weddingPrompt := "中年，白色婚礼西装"
	current, err := service.DeriveAsset(ctx, projectapp.DeriveAssetInput{
		ProjectID: created.Project.ID, EpisodeID: third.Episode.ID,
		SourceAssetID: candidates[0].Asset.Asset.ID, Prompt: &weddingPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if current.Asset.Asset.ID == previous.Asset.Asset.ID || current.Asset.Identity.ID != previous.Asset.Identity.ID ||
		current.Asset.Asset.DerivedFromAssetID != previous.Asset.Asset.ID || current.Asset.Asset.Prompt != weddingPrompt {
		t.Fatalf("reuse should retain identity but create an independent appearance: %+v", current.Asset)
	}
	currentSlotID, _ := domain.FixedMediaSlotID(current.Asset.Asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	currentMedia, err := handle.Store.MediaSlot(ctx, currentSlotID, true)
	if err != nil || currentMedia.Current == nil || currentSlotID == previousSlotID || len(currentMedia.Versions) != 1 ||
		currentMedia.Current.ID == previousMedia.Current.ID || currentMedia.Current.ArtifactID != previousMedia.Current.ArtifactID {
		t.Fatalf("image slot and version should be independent: %+v, %v", currentMedia, err)
	}
	if _, err := service.DeriveAsset(ctx, projectapp.DeriveAssetInput{
		ProjectID: created.Project.ID, EpisodeID: third.Episode.ID, SourceAssetID: previous.Asset.Asset.ID,
	}); !errors.Is(err, projectstore.ErrEpisodeAssetIdentityConflict) {
		t.Fatalf("repeat reuse must not create a second appearance of the same identity: %v", err)
	}
	candidates, err = service.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{
		ProjectID: created.Project.ID, EpisodeID: third.Episode.ID,
	})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("already present identities must disappear from reuse candidates: %+v, %v", candidates, err)
	}
	current, err = service.UpdateAssetPrompt(ctx, projectapp.UpdateAssetPromptInput{
		ProjectID: created.Project.ID, AssetID: current.Asset.Asset.ID,
		ExpectedRevision: current.Asset.Asset.Revision, Prompt: "老年，红色礼服",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentMedia = addImage(current.Asset.Asset, "D")
	unchanged, err := service.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: created.Project.ID, AssetID: previous.Asset.Asset.ID})
	unchangedMedia, mediaErr := handle.Store.MediaSlot(ctx, previousSlotID, true)
	if err != nil || mediaErr != nil || unchanged.Asset.Prompt != secondPrompt ||
		unchangedMedia.Current.ID != previousMedia.Current.ID || len(unchangedMedia.Versions) != 2 {
		t.Fatalf("target edits must not change the prior episode: %+v, %+v, %v, %v", unchanged, unchangedMedia, err, mediaErr)
	}
	if _, err := service.UpdateAssetPrompt(ctx, projectapp.UpdateAssetPromptInput{
		ProjectID: created.Project.ID, AssetID: previous.Asset.Asset.ID,
		ExpectedRevision: previous.Asset.Asset.Revision, Prompt: "青年，绿色便装",
	}); err != nil {
		t.Fatal(err)
	}
	addImage(previous.Asset.Asset, "E")
	unchanged, err = service.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: created.Project.ID, AssetID: current.Asset.Asset.ID})
	unchangedMedia, mediaErr = handle.Store.MediaSlot(ctx, currentSlotID, true)
	if err != nil || mediaErr != nil || unchanged.Asset.Prompt != current.Asset.Asset.Prompt ||
		unchangedMedia.Current.ID != currentMedia.Current.ID || len(unchangedMedia.Versions) != 2 {
		t.Fatalf("prior edits must not synchronize into the target: %+v, %+v, %v, %v", unchanged, unchangedMedia, err, mediaErr)
	}
	firstAgain, err := service.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: created.Project.ID, AssetID: first.Asset.Asset.ID})
	if err != nil || firstAgain.Asset.Prompt != first.Asset.Asset.Prompt || firstAgain.Asset.Revision != first.Asset.Asset.Revision {
		t.Fatalf("original episode must retain its own appearance: %+v, %v", firstAgain, err)
	}
}
