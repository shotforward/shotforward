package mediafiles

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestPrepareResizesMatchingFirstImageAndInstallsAtomically(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "portrait.png")
	writePNG(t, source, 36, 64)

	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage, RequiredWidth: 72, RequiredHeight: 128})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Width != 72 || prepared.Height != 128 || prepared.MIMEType != "image/png" || len(prepared.SHA256) != 64 || prepared.ByteSize < 1 {
		t.Fatalf("PreparedMedia = %#v", prepared)
	}
	id, _ := domain.ParseArtifactID("art_" + strings.Repeat("A", 26))
	installed, err := processor.Install(context.Background(), root, prepared, id)
	if err != nil {
		t.Fatal(err)
	}
	if installed.RelativePath != "media/images/"+id.String()+".png" {
		t.Fatalf("RelativePath = %q", installed.RelativePath)
	}
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(installed.RelativePath))); err != nil || info.Size() != prepared.ByteSize {
		t.Fatalf("installed file = %v, %v", info, err)
	}
	if err := processor.Remove(root, installed); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsFirstImageAspectMismatchButAllowsUnrestrictedImage(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "landscape.png")
	writePNG(t, source, 64, 36)

	_, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage, RequiredWidth: 72, RequiredHeight: 128})
	if !errors.Is(err, projectapp.ErrMediaAspectRatioMismatch) {
		t.Fatalf("aspect mismatch error = %v", err)
	}
	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Width != 64 || prepared.Height != 36 {
		t.Fatalf("unrestricted dimensions = %dx%d", prepared.Width, prepared.Height)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCanCropGeneratedImageToRequiredAspect(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "square.png")
	writePNG(t, source, 64, 64)

	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
		ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage,
		RequiredWidth: 128, RequiredHeight: 72, CropToFill: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Width != 128 || prepared.Height != 72 || prepared.MIMEType != "image/png" {
		t.Fatalf("cropped media = %#v", prepared)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareVideoFrameExtractsAProjectVideoAtRequestedTime(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	videoDirectory := filepath.Join(root, "media", "videos")
	if err := os.MkdirAll(videoDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(videoDirectory, "source.mp4")
	command := exec.Command(processor.ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=64x36:rate=10:duration=1", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create video fixture: %v: %s", err, output)
	}
	prepared, err := processor.PrepareVideoFrame(context.Background(), projectapp.PrepareVideoFrameRequest{
		ProjectRoot: root, SourceRelativePath: "media/videos/source.mp4", AtMilliseconds: 500,
		OriginalFilename: "continuity-frame.png", RequiredWidth: 128, RequiredHeight: 72,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Kind != domain.MediaKindImage || prepared.Width != 128 || prepared.Height != 72 || prepared.MIMEType != "image/png" || prepared.OriginalFilename != "continuity-frame.png" {
		t.Fatalf("prepared frame = %#v", prepared)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCentersGeneratedImageCrop(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "bands.png")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	value := image.NewRGBA(image.Rect(0, 0, 6, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 6; x++ {
			pixel := color.RGBA{R: 255, A: 255}
			if x >= 2 && x < 4 {
				pixel = color.RGBA{G: 255, A: 255}
			} else if x >= 4 {
				pixel = color.RGBA{B: 255, A: 255}
			}
			value.SetRGBA(x, y, pixel)
		}
	}
	if err := png.Encode(file, value); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
		ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage,
		RequiredWidth: 2, RequiredHeight: 2, CropToFill: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := os.Open(prepared.AbsolutePath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(result)
	closeErr := result.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	red, green, blue, _ := decoded.At(0, 0).RGBA()
	if green <= red || green <= blue {
		t.Fatalf("center crop selected the wrong band: r=%d g=%d b=%d", red, green, blue)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsUnsupportedExtensionAndSymlink(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "image.gif")
	writePNG(t, source, 10, 10)
	if _, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage}); !errors.Is(err, projectapp.ErrMediaImportUnsupported) {
		t.Fatalf("unsupported error = %v", err)
	}
	link := filepath.Join(t.TempDir(), "image.png")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: link, Kind: domain.MediaKindImage}); !errors.Is(err, projectapp.ErrMediaImportUnreadable) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestPrepareRejectsContentThatDoesNotMatchExtension(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	source := filepath.Join(t.TempDir(), "disguised.jpg")
	writePNG(t, source, 10, 10)
	_, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: t.TempDir(), SourcePath: source, Kind: domain.MediaKindImage})
	if !errors.Is(err, projectapp.ErrMediaImportInvalid) {
		t.Fatalf("mismatched content error = %v", err)
	}
}

func TestPrepareExtractsVideoAndAudioMetadata(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	sources := t.TempDir()
	video := filepath.Join(sources, "clip.mp4")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "color=c=black:s=64x36:r=30", "-t", "0.2", "-c:v", "mpeg4", video)
	audio := filepath.Join(sources, "reference.wav")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "0.1", audio)

	videoMedia, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: t.TempDir(), SourcePath: video, Kind: domain.MediaKindVideo})
	if err != nil {
		t.Fatal(err)
	}
	if videoMedia.Width != 64 || videoMedia.Height != 36 || videoMedia.DurationMilliseconds < 100 || videoMedia.FPSNumerator < 1 || videoMedia.FPSDenominator < 1 {
		t.Fatalf("video metadata = %#v", videoMedia)
	}
	audioRoot := t.TempDir()
	audioMedia, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: audioRoot, SourcePath: audio, Kind: domain.MediaKindAudio})
	if err != nil {
		t.Fatal(err)
	}
	if audioMedia.DurationMilliseconds < 50 || audioMedia.AudioSampleRate != 48000 || audioMedia.AudioChannels != 1 {
		t.Fatalf("audio metadata = %#v", audioMedia)
	}
	if err := processor.Discard(audioRoot, audioMedia); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareMaterializesRequestedVideoTrim(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	sources := t.TempDir()
	root := t.TempDir()
	video := filepath.Join(sources, "clip.mp4")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "testsrc=size=64x36:rate=24", "-t", "2", "-c:v", "mpeg4", video)

	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
		ProjectRoot: root, SourcePath: video, Kind: domain.MediaKindVideo,
		TrimStartMilliseconds: 500, TrimEndMilliseconds: 1_500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.MIMEType != "video/mp4" || prepared.DurationMilliseconds < 850 || prepared.DurationMilliseconds > 1_150 {
		t.Fatalf("trimmed video metadata = %#v", prepared)
	}
	if info, err := os.Stat(prepared.AbsolutePath); err != nil || info.Size() != prepared.ByteSize {
		t.Fatalf("trimmed video file = %v, %v", info, err)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func testProcessor(t *testing.T) Processor {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	processor, err := NewProcessor(ffprobe, ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func writePNG(t *testing.T, path string, width, height int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	image := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			image.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	if err := png.Encode(file, image); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func runFFmpeg(t *testing.T, binary string, arguments ...string) {
	t.Helper()
	arguments = append([]string{"-nostdin", "-v", "error", "-y"}, arguments...)
	if output, err := exec.Command(binary, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v: %s", err, boundedMessage(output))
	}
}
