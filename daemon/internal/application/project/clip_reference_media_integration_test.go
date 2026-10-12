package projectapp_test

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/mediafiles"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

func TestClipReferenceMediaAcceptsRealImageVideoAndAudio(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	processor, err := mediafiles.NewProcessor(ffprobe, ffmpeg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	service := newRealService(
		t,
		library,
		registry,
		projectapp.NewSecureIDGenerator(),
		fixedClock{at: testNow()},
		processor,
	)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: filepath.Join(t.TempDir(), "reference-media-project"),
		Name:     "参考素材测试", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	episodeID := created.Episodes[0].ID
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID,
		Title: "真实媒体", ExpectedStoryboardRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SceneID: scene.ID,
		ClipFields: projectapp.ClipFields{
			Title: "三类参考", PlannedDurationSeconds: 5,
			GenerationMode: domain.ClipGenerationReference,
		},
		ExpectedStoryboardRevision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	fixtures := t.TempDir()
	imagePath := filepath.Join(fixtures, "landscape.png")
	writeReferencePNG(t, imagePath)
	videoPath := filepath.Join(fixtures, "reference.mp4")
	runReferenceFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", "color=c=black:s=64x36:r=30", "-t", "0.2", "-c:v", "mpeg4", videoPath)
	audioPath := filepath.Join(fixtures, "reference.wav")
	runReferenceFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "0.1", audioPath)

	want := []struct {
		path     string
		filename string
		kind     domain.MediaKind
	}{
		{imagePath, "参考图片.png", domain.MediaKindImage},
		{videoPath, "参考视频.mp4", domain.MediaKindVideo},
		{audioPath, "参考音频.wav", domain.MediaKindAudio},
	}
	for _, item := range want {
		artifact, uploadErr := service.UploadClipReferenceMedia(ctx, projectapp.UploadClipReferenceMediaInput{
			ProjectID: created.Project.ID, EpisodeID: episodeID, ClipID: clip.Clip.ID,
			SourcePath: item.path, OriginalFilename: item.filename, Kind: item.kind,
		})
		if uploadErr != nil {
			t.Fatalf("upload %s: %v", item.kind, uploadErr)
		}
		if artifact.Kind != item.kind || artifact.OriginalFilename != item.filename {
			t.Fatalf("uploaded %s Artifact = %#v", item.kind, artifact)
		}
	}

	artifacts, err := service.ListClipReferenceMedia(ctx, projectapp.ListClipReferenceMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, ClipID: clip.Clip.ID,
	})
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("reference media = %#v, %v", artifacts, err)
	}
	for _, artifact := range artifacts {
		if artifact.Kind == domain.MediaKindImage && (artifact.Width != 64 || artifact.Height != 36) {
			t.Fatalf("reference image was forced to Episode aspect: %#v", artifact)
		}
		if artifact.Kind != domain.MediaKindImage && artifact.DurationMilliseconds < 1 {
			t.Fatalf("timed reference metadata is missing: %#v", artifact)
		}
	}

	handle, _ := registry.ByProjectID(created.Project.ID)
	for _, kind := range []domain.MediaSlotKind{
		domain.MediaSlotClipFirstImage,
		domain.MediaSlotClipLastImage,
		domain.MediaSlotClipVideo,
		domain.MediaSlotAudioPrimary,
	} {
		slotID, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), kind)
		details, readErr := handle.Store.MediaSlot(ctx, slotID, true)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if details.Current != nil || len(details.Versions) != 0 || details.Slot.Revision != 1 {
			t.Fatalf("reference upload changed %s output Slot: %#v", kind, details)
		}
	}
}

func writeReferencePNG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	value := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			value.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	if err := png.Encode(file, value); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func runReferenceFFmpeg(t *testing.T, binary string, args ...string) {
	t.Helper()
	args = append([]string{"-nostdin", "-v", "error", "-y"}, args...)
	if output, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v: %s", err, output)
	}
}
