package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEpisodeReferenceMediaTrimValidationAndEffectiveRange(t *testing.T) {
	t.Parallel()
	for _, kind := range []MediaKind{MediaKindAudio, MediaKindVideo, MediaKindImage} {
		t.Run(string(kind), func(t *testing.T) {
			at := time.Unix(1, 0).UTC()
			artifact := Artifact{ID: ArtifactID("art_" + strings.Repeat("A", 26)), ProjectID: ProjectID("prj_" + strings.Repeat("A", 26)), Kind: kind, RelativePath: "media/source", SHA256: strings.Repeat("a", 64), ByteSize: 1024, MIMEType: "application/octet-stream", OriginalFilename: "source", CreatedAt: at}
			if kind != MediaKindAudio {
				artifact.Width, artifact.Height = 720, 1280
			}
			if kind != MediaKindImage {
				artifact.DurationMilliseconds = 15000
			}
			artifact.FPSNumerator, artifact.FPSDenominator = 24, 1
			artifact.AudioSampleRate, artifact.AudioChannels = 48000, 2
			base := EpisodeReferenceMedia{EpisodeID: EpisodeID("ep_" + strings.Repeat("A", 26)), Artifact: artifact, Name: "参考", Revision: 1, CreatedAt: at, UpdatedAt: at}
			for _, bounds := range [][2]int64{{0, 0}, {0, 15000}, {3000, 11000}, {0, 1}, {14999, 15000}, {-1, 11000}, {3000, 0}, {3000, 3000}, {4000, 3000}, {0, 15001}, {15000, 15001}, {0, -1}} {
				value := base
				value.TrimStartMilliseconds, value.TrimEndMilliseconds = bounds[0], bounds[1]
				valid := bounds == [2]int64{} || (kind != MediaKindImage && bounds[0] >= 0 && bounds[1] > bounds[0] && bounds[1] <= artifact.DurationMilliseconds)
				err := value.Validate()
				if valid && err != nil || !valid && !errors.Is(err, ErrInvalidEpisodeReferenceMedia) {
					t.Fatalf("%s %v validation = %v, valid=%v", kind, bounds, err, valid)
				}
				if !valid {
					continue
				}
				wantStart, wantEnd := bounds[0], bounds[1]
				if bounds == [2]int64{} {
					wantEnd = artifact.DurationMilliseconds
				}
				start, end := value.EffectiveTrimRange()
				if start != wantStart || end != wantEnd || value.EffectiveDurationMilliseconds() != wantEnd-wantStart {
					t.Fatalf("effective range %v = (%d,%d), duration=%d", bounds, start, end, value.EffectiveDurationMilliseconds())
				}
				if value.Artifact != artifact {
					t.Fatal("resolving trim mutated the original Artifact")
				}
			}
		})
	}
}
