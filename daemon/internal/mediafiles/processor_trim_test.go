package mediafiles

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// Use distinct content before, inside and after the selection, not only a
// duration assertion: accidentally submitting the original or its head fails.
func TestPrepareTimedSelectionPreservesOriginalAndSelectedContent(t *testing.T) {
	for _, kind := range []domain.MediaKind{domain.MediaKindAudio, domain.MediaKindVideo} {
		t.Run(string(kind), func(t *testing.T) {
			processor := testProcessor(t)
			root := t.TempDir()
			extension := ".wav"
			if kind == domain.MediaKindVideo {
				extension = ".mp4"
			}
			source := filepath.Join(t.TempDir(), "original"+extension)
			if kind == domain.MediaKindAudio {
				runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "aevalsrc=if(between(t\\,0.5\\,1.5)\\,0.5\\,0):s=8000:d=2", "-c:a", "pcm_s16le", source)
			} else {
				runFFmpeg(t, processor.ffmpeg,
					"-f", "lavfi", "-i", "color=c=red:s=64x36:r=20:d=0.5",
					"-f", "lavfi", "-i", "color=c=blue:s=64x36:r=20:d=1",
					"-f", "lavfi", "-i", "color=c=green:s=64x36:r=20:d=0.5",
					"-filter_complex", "[0:v][1:v][2:v]concat=n=3:v=1:a=0[v]", "-map", "[v]", "-c:v", "mpeg4", source)
			}
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
				ProjectRoot: root, SourcePath: source, Kind: kind,
				TrimStartMilliseconds: 750, TrimEndMilliseconds: 1250,
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.AbsolutePath == source || prepared.DurationMilliseconds < 475 || prepared.DurationMilliseconds > 550 {
				t.Fatalf("selection metadata = %#v", prepared)
			}
			if kind == domain.MediaKindAudio {
				decoded, err := exec.Command(processor.ffmpeg, "-v", "error", "-i", prepared.AbsolutePath, "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1").Output()
				if err != nil || len(decoded) < 2 {
					t.Fatalf("decode selected audio: %v", err)
				}
				for index := 0; index+1 < len(decoded); index += 2 {
					sample := int16(binary.LittleEndian.Uint16(decoded[index : index+2]))
					if sample < 16000 || sample > 17000 {
						t.Fatalf("selection contains content outside the requested audio interval: sample %d = %d", index/2, sample)
					}
				}
			} else {
				decoded, err := exec.Command(processor.ffmpeg, "-v", "error", "-i", prepared.AbsolutePath, "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1").Output()
				if err != nil || len(decoded) < 3 {
					t.Fatalf("decode selected video: %v", err)
				}
				for index := 0; index+2 < len(decoded); index += 3 {
					if decoded[index] > 40 || decoded[index+1] > 40 || decoded[index+2] < 200 {
						t.Fatalf("selection contains a non-blue frame at %d: %v", index/3, decoded[index:index+3])
					}
				}
			}
			after, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("source was changed: %v", err)
			}
			if err := processor.Discard(root, prepared); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(prepared.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary selection was not cleaned: %v", err)
			}
		})
	}
}

func TestPrepareAudioOpenEndedTrimDoesNotReturnFullSource(t *testing.T) {
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "original.wav")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000:duration=2", source)
	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
		ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindAudio, TrimStartMilliseconds: 1250,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.DurationMilliseconds < 725 || prepared.DurationMilliseconds > 775 {
		t.Fatalf("open-ended trim returned the wrong duration: %d", prepared.DurationMilliseconds)
	}
	if err := processor.Discard(root, prepared); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareInvalidTrimFailsWithoutLeavingCopies(t *testing.T) {
	processor := testProcessor(t)
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "original.wav")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "sine=duration=1", source)
	for _, bounds := range [][2]int64{{-1, 100}, {500, 500}, {1000, 1500}, {1001, 0}, {0, 1500}} {
		_, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
			ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindAudio,
			TrimStartMilliseconds: bounds[0], TrimEndMilliseconds: bounds[1],
		})
		if !errors.Is(err, projectapp.ErrMediaImportInvalid) {
			t.Fatalf("range %v: %v", bounds, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(stagingDirectory), "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid trims left staging files: %v, %v", files, err)
	}
}
