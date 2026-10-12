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

func TestMediaVersionLifecycleAndRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "project.db")
	store := newTestStore(t, databasePath)
	project, episode := testModels(t)
	if err := store.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "M", 1, "场景", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "M", 1, 5, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
	details, err := store.MediaSlot(ctx, slotID, true)
	if err != nil || details.Slot.Revision != 1 || len(details.Versions) != 0 {
		t.Fatalf("initial MediaSlot = %#v, %v", details, err)
	}

	at = at.Add(time.Second)
	artifact := testVideoArtifact(t, project.ID, "M", strings.Repeat("a", 64), at)
	version := testMediaVersion(t, slotID, artifact.ID, "M", 1, artifact.DurationMilliseconds, at)
	if err := store.AddMediaVersion(ctx, artifact, version, 1, true, at); err != nil {
		t.Fatal(err)
	}
	details, err = store.MediaSlot(ctx, slotID, true)
	if err != nil || details.Slot.Revision != 2 || details.Current == nil || details.Current.ID != version.ID {
		t.Fatalf("MediaSlot after add = %#v, %v", details, err)
	}

	at = at.Add(time.Second)
	version.TrimInMilliseconds = 100
	version.TrimOutMilliseconds = 900
	version.Revision = 2
	version.UpdatedAt = at
	if err := store.UpdateVideoTrim(ctx, version, 1, at); err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Second)
	version.Revision = 3
	version.UpdatedAt = at
	version.DeletedAt = &at
	if err := store.DeleteMediaVersion(ctx, version, 2, 2, at); err != nil {
		t.Fatal(err)
	}
	details, err = store.MediaSlot(ctx, slotID, true)
	if err != nil || details.Slot.Revision != 3 || details.Current != nil || len(details.Versions) != 1 || !details.Versions[0].IsDeleted() {
		t.Fatalf("MediaSlot after delete = %#v, %v", details, err)
	}

	at = at.Add(time.Second)
	version.Revision = 4
	version.UpdatedAt = at
	version.DeletedAt = nil
	if err := store.RestoreMediaVersion(ctx, version, 3, 3, at); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectMediaVersion(ctx, slotID, version.ID, 4, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	details, err = reopened.MediaSlot(ctx, slotID, true)
	if err != nil || details.Slot.Revision != 5 || details.Current == nil || details.Current.TrimInMilliseconds != 100 || details.Current.TrimOutMilliseconds != 900 {
		t.Fatalf("reopened MediaSlot = %#v, %v", details, err)
	}
}

func TestMediaVersionRejectsWrongKindAndDuplicateAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	identity, asset := assetModels(t, project.ID, episode.ID, "N", "N", domain.AssetKindProp, "道具", "", at)
	if err := store.CreateAsset(ctx, &identity, asset, at); err != nil {
		t.Fatal(err)
	}
	slotID, _ := domain.FixedMediaSlotID(asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	video := testVideoArtifact(t, project.ID, "N", strings.Repeat("b", 64), at.Add(time.Second))
	version := testMediaVersion(t, slotID, video.ID, "N", 1, video.DurationMilliseconds, at.Add(time.Second))
	if err := store.AddMediaVersion(ctx, video, version, 1, true, at.Add(time.Second)); !errors.Is(err, ErrMediaKindMismatch) {
		t.Fatalf("wrong kind error = %v", err)
	}
	image := video
	image.Kind = domain.MediaKindImage
	image.RelativePath = "media/images/n.png"
	image.MIMEType = "image/png"
	image.DurationMilliseconds = 0
	image.FPSNumerator = 0
	image.FPSDenominator = 0
	version.TrimOutMilliseconds = 0
	if err := store.AddMediaVersion(ctx, image, version, 1, true, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	duplicate := version
	duplicate.ID, _ = domain.ParseMediaVersionID("mvr_" + strings.Repeat("O", 26))
	duplicate.SequenceNumber = 2
	duplicate.CreatedAt = at.Add(2 * time.Second)
	duplicate.UpdatedAt = duplicate.CreatedAt
	if err := store.AddMediaVersion(ctx, image, duplicate, 2, true, at.Add(2*time.Second)); !errors.Is(err, ErrMediaVersionAlreadyExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	details, err := store.MediaSlot(ctx, slotID, true)
	if err != nil || details.Slot.Revision != 2 || len(details.Versions) != 1 {
		t.Fatalf("state after duplicate = %#v, %v", details, err)
	}
}

func TestClipReferenceMediaIsIndependentFromOutputSlots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "Y", 1, "参考素材", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "Y", 1, 8, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	artifactID, _ := domain.ParseArtifactID("art_" + strings.Repeat("Y", 26))
	artifact := domain.Artifact{
		ID: artifactID, ProjectID: project.ID, Kind: domain.MediaKindImage,
		RelativePath: "media/images/reference.png", SHA256: strings.Repeat("f", 64), ByteSize: 100,
		MIMEType: "image/png", OriginalFilename: "构图参考.png", Width: 1280, Height: 720,
		CreatedAt: at.Add(time.Second),
	}
	if err := store.AddClipReferenceArtifact(ctx, clip.ID, artifact, artifact.CreatedAt); err != nil {
		t.Fatal(err)
	}
	values, err := store.ClipReferenceArtifacts(ctx, clip.ID)
	if err != nil || len(values) != 1 || values[0] != artifact {
		t.Fatalf("ClipReferenceArtifacts() = %#v, %v", values, err)
	}
	selected, err := store.ArtifactSelectedInEpisode(ctx, artifact.ID, episode.ID)
	if err != nil || !selected {
		t.Fatalf("ArtifactSelectedInEpisode() = %v, %v", selected, err)
	}
	for _, kind := range []domain.MediaSlotKind{domain.MediaSlotClipFirstImage, domain.MediaSlotClipLastImage, domain.MediaSlotClipVideo, domain.MediaSlotAudioPrimary} {
		slotID, _ := domain.FixedMediaSlotID(clip.ID.String(), kind)
		details, readErr := store.MediaSlot(ctx, slotID, true)
		if readErr != nil || details.Slot.Revision != 1 || len(details.Versions) != 0 {
			t.Fatalf("output slot %s changed = %#v, %v", kind, details, readErr)
		}
	}
	removedAt := artifact.CreatedAt.Add(time.Second)
	if err := store.DeleteClipReferenceArtifact(ctx, clip.ID, artifact.ID, removedAt); err != nil {
		t.Fatal(err)
	}
	values, err = store.ClipReferenceArtifacts(ctx, clip.ID)
	if err != nil || len(values) != 0 {
		t.Fatalf("ClipReferenceArtifacts() after delete = %#v, %v", values, err)
	}
	selected, err = store.ArtifactSelectedInEpisode(ctx, artifact.ID, episode.ID)
	if err != nil || selected {
		t.Fatalf("ArtifactSelectedInEpisode() after delete = %v, %v", selected, err)
	}
}

func TestAudioReferenceIsEpisodeScopedAndClipHasAtMostOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "P", 1, "场景", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	clip := storyboardClip(t, project.ID, episode.ID, scene.ID, "P", 1, 5, at)
	if err := store.CreateClip(ctx, clip, 2, at); err != nil {
		t.Fatal(err)
	}
	firstSlotID, _ := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipFirstImage)
	wrongRatioID, _ := domain.ParseArtifactID("art_" + strings.Repeat("T", 26))
	wrongRatio := domain.Artifact{ID: wrongRatioID, ProjectID: project.ID, Kind: domain.MediaKindImage, RelativePath: "media/images/wrong.png", SHA256: strings.Repeat("d", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "wrong.png", Width: 1280, Height: 720, CreatedAt: at.Add(time.Second)}
	wrongRatioVersion := testMediaVersion(t, firstSlotID, wrongRatio.ID, "T", 1, 0, wrongRatio.CreatedAt)
	if err := store.AddMediaVersion(ctx, wrongRatio, wrongRatioVersion, 1, true, wrongRatio.CreatedAt); !errors.Is(err, ErrMediaAspectRatioMismatch) {
		t.Fatalf("first image aspect error = %v", err)
	}
	wrongSizeID, _ := domain.ParseArtifactID("art_" + strings.Repeat("U", 26))
	wrongSize := wrongRatio
	wrongSize.ID = wrongSizeID
	wrongSize.RelativePath = "media/images/wrong-size.png"
	wrongSize.SHA256 = strings.Repeat("e", 64)
	wrongSize.Width = 360
	wrongSize.Height = 640
	wrongSizeVersion := testMediaVersion(t, firstSlotID, wrongSize.ID, "U", 1, 0, wrongSize.CreatedAt)
	if err := store.AddMediaVersion(ctx, wrongSize, wrongSizeVersion, 1, true, wrongSize.CreatedAt); !errors.Is(err, ErrMediaOutputSpecMismatch) {
		t.Fatalf("first image dimensions error = %v", err)
	}
	first := testAudioReference(t, project.ID, episode.ID, "P", "参考一", at.Add(time.Second))
	second := testAudioReference(t, project.ID, episode.ID, "Q", "参考二", at.Add(2*time.Second))
	if err := store.CreateAudioReferenceAsset(ctx, first, first.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAudioReferenceAsset(ctx, second, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	audioSlotID, _ := domain.FixedMediaSlotID(first.ID.String(), domain.MediaSlotAudioPrimary)
	if slot, err := store.MediaSlot(ctx, audioSlotID, true); err != nil || slot.Slot.OwnerID != first.ID.String() || slot.Slot.Kind != domain.MediaSlotAudioPrimary {
		t.Fatalf("AudioReference MediaSlot = %#v, %v", slot, err)
	}
	if err := store.SetClipAudioReference(ctx, clip.ID, first.ID, 1, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClipAudioReference(ctx, clip.ID, second.ID, 2, at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	selected, err := store.ClipAudioReference(ctx, clip.ID)
	if err != nil || selected != second.ID {
		t.Fatalf("ClipAudioReference() = %q, %v", selected, err)
	}
	second.Revision = 2
	second.UpdatedAt = at.Add(5 * time.Second)
	second.DeletedAt = &second.UpdatedAt
	if err := store.DeleteAudioReferenceAsset(ctx, second, 1, second.UpdatedAt); !errors.Is(err, ErrAudioReferenceInUse) {
		t.Fatalf("delete bound audio error = %v", err)
	}
	if err := store.ClearClipAudioReference(ctx, clip.ID, 3, at.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAudioReferenceAsset(ctx, second, 1, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedAssetReusesOnlySourceCurrentArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, first, second := initializedAssetStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	at := second.UpdatedAt.Add(time.Second)
	identity, source := assetModels(t, project.ID, first.ID, "R", "R", domain.AssetKindCharacter, "角色", "提示", at)
	if err := store.CreateAsset(ctx, &identity, source, at); err != nil {
		t.Fatal(err)
	}
	sourceSlotID, _ := domain.FixedMediaSlotID(source.ID.String(), domain.MediaSlotAssetPrimaryImage)
	artifactID, _ := domain.ParseArtifactID("art_" + strings.Repeat("R", 26))
	artifact := domain.Artifact{ID: artifactID, ProjectID: project.ID, Kind: domain.MediaKindImage, RelativePath: "media/images/r.png", SHA256: strings.Repeat("c", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "r.png", Width: 600, Height: 600, CreatedAt: at.Add(time.Second)}
	sourceVersion := testMediaVersion(t, sourceSlotID, artifact.ID, "R", 1, 0, artifact.CreatedAt)
	if err := store.AddMediaVersion(ctx, artifact, sourceVersion, 1, true, artifact.CreatedAt); err != nil {
		t.Fatal(err)
	}
	derivedAt := at.Add(2 * time.Second)
	derived := source
	derived.ID = mustAssetID(t, "S")
	derived.EpisodeID = second.ID
	derived.DerivedFromAssetID = source.ID
	derived.CreatedAt = derivedAt
	derived.UpdatedAt = derivedAt
	inheritedVersionID, _ := domain.ParseMediaVersionID("mvr_" + strings.Repeat("S", 26))
	if err := store.CreateDerivedAsset(ctx, derived, inheritedVersionID, derivedAt); err != nil {
		t.Fatal(err)
	}
	targetSlotID, _ := domain.FixedMediaSlotID(derived.ID.String(), domain.MediaSlotAssetPrimaryImage)
	details, err := store.MediaSlot(ctx, targetSlotID, true)
	if err != nil || details.Slot.Revision != 2 || details.Current == nil || details.Current.ID != inheritedVersionID || details.Current.ArtifactID != artifact.ID || len(details.Versions) != 1 {
		t.Fatalf("derived MediaSlot = %#v, %v", details, err)
	}
}

func testVideoArtifact(t *testing.T, projectID domain.ProjectID, suffix, sha string, at time.Time) domain.Artifact {
	t.Helper()
	id, err := domain.ParseArtifactID("art_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return domain.Artifact{ID: id, ProjectID: projectID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/" + strings.ToLower(suffix) + ".mp4", SHA256: sha, ByteSize: 100, MIMEType: "video/mp4", OriginalFilename: "source.mp4", Width: 1920, Height: 1080, DurationMilliseconds: 1000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: at}
}

func testMediaVersion(t *testing.T, slotID domain.MediaSlotID, artifactID domain.ArtifactID, suffix string, sequence int, trimOut int64, at time.Time) domain.MediaVersion {
	t.Helper()
	id, err := domain.ParseMediaVersionID("mvr_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return domain.MediaVersion{ID: id, SlotID: slotID, ArtifactID: artifactID, SequenceNumber: sequence, TrimOutMilliseconds: trimOut, Revision: 1, CreatedAt: at, UpdatedAt: at}
}

func testAudioReference(t *testing.T, projectID domain.ProjectID, episodeID domain.EpisodeID, suffix, name string, at time.Time) domain.AudioReferenceAsset {
	t.Helper()
	id, err := domain.ParseAudioReferenceAssetID("aud_" + strings.Repeat(suffix, 26))
	if err != nil {
		t.Fatal(err)
	}
	return domain.AudioReferenceAsset{ID: id, ProjectID: projectID, EpisodeID: episodeID, Name: name, Revision: 1, CreatedAt: at, UpdatedAt: at}
}
