package projectstore

import (
	"context"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestEpisodeReferenceMediaIsReusableAcrossClips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()

	at := episode.UpdatedAt.Add(time.Second)
	scene := storyboardScene(t, project.ID, episode.ID, "R", 1, "上传素材复用", at)
	if err := store.CreateScene(ctx, scene, 1, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	first := storyboardClip(t, project.ID, episode.ID, scene.ID, "R", 1, 8, at)
	first.GenerationMode = domain.ClipGenerationReference
	first.VideoPrompts.Reference = "沿用@动作参考完成回头。"
	if err := store.CreateClip(ctx, first, 2, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	second := storyboardClip(t, project.ID, episode.ID, scene.ID, "S", 2, 8, at)
	second.GenerationMode = domain.ClipGenerationReference
	second.VideoPrompts.Reference = "@动作参考中的人物停下。"
	if err := store.CreateClip(ctx, second, 3, at); err != nil {
		t.Fatal(err)
	}

	artifactID, _ := domain.ParseArtifactID("art_" + strings.Repeat("R", 26))
	artifact := domain.Artifact{
		ID: artifactID, ProjectID: project.ID, Kind: domain.MediaKindVideo,
		RelativePath: "media/videos/action-reference.mp4", SHA256: strings.Repeat("c", 64), ByteSize: 4096,
		MIMEType: "video/mp4", OriginalFilename: "动作参考.mp4", Width: 1280, Height: 720,
		DurationMilliseconds: 8000, FPSNumerator: 24, FPSDenominator: 1, CreatedAt: at.Add(time.Second),
	}
	libraryItem := domain.EpisodeReferenceMedia{
		EpisodeID: episode.ID, Artifact: artifact, Name: "动作参考", Description: "供多个视频单元复用",
		Revision: 1, CreatedAt: artifact.CreatedAt, UpdatedAt: artifact.CreatedAt,
	}
	if err := store.PutEpisodeReferenceMedia(ctx, libraryItem, artifact.CreatedAt); err != nil {
		t.Fatal(err)
	}
	selected, err := store.ArtifactSelectedInEpisode(ctx, artifact.ID, episode.ID)
	if err != nil || !selected {
		t.Fatalf("library ArtifactSelectedInEpisode() = %v, %v", selected, err)
	}

	at = artifact.CreatedAt.Add(time.Second)
	if err := store.AddClipReferenceArtifact(ctx, first.ID, artifact, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddClipReferenceArtifact(ctx, second.ID, artifact, at); err != nil {
		t.Fatal(err)
	}
	value, err := store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
	if err != nil || value.UsageCount != 2 {
		t.Fatalf("EpisodeReferenceMedia() usage = %#v, %v", value, err)
	}

	at = at.Add(time.Second)
	value.Name = "回头动作参考"
	value.Description = "已补充动作说明"
	value.UpdatedAt = at
	if err := store.UpdateEpisodeReferenceMedia(ctx, value, 1, at); err != nil {
		t.Fatal(err)
	}
	value, err = store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
	if err != nil || value.Name != "回头动作参考" || value.Revision != 2 {
		t.Fatalf("updated EpisodeReferenceMedia() = %#v, %v", value, err)
	}
	firstDetails, err := store.Clip(ctx, first.ID)
	if err != nil || firstDetails.Clip.VideoPrompts.Reference != "沿用@回头动作参考完成回头。" || firstDetails.Clip.Revision != 2 {
		t.Fatalf("Clip() after library rename = %#v, %v", firstDetails, err)
	}

	at = at.Add(time.Second)
	if err := store.DeleteEpisodeReferenceMedia(ctx, value, 2, at); err != nil {
		t.Fatal(err)
	}
	for _, clipID := range []domain.ClipID{first.ID, second.ID} {
		values, readErr := store.ClipReferenceArtifacts(ctx, clipID)
		if readErr != nil || len(values) != 0 {
			t.Fatalf("ClipReferenceArtifacts(%s) after library delete = %#v, %v", clipID, values, readErr)
		}
		details, readErr := store.Clip(ctx, clipID)
		if readErr != nil || strings.Contains(details.Clip.VideoPrompts.Reference, "@回头动作参考") {
			t.Fatalf("Clip(%s) prompt after library delete = %q, %v", clipID, details.Clip.VideoPrompts.Reference, readErr)
		}
	}
	selected, err = store.ArtifactSelectedInEpisode(ctx, artifact.ID, episode.ID)
	if err != nil || selected {
		t.Fatalf("ArtifactSelectedInEpisode() after library delete = %v, %v", selected, err)
	}
}
