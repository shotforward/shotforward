package exportjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestFFmpegProcessorProducesPlayableNormalizedMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	root := t.TempDir()
	for _, directory := range []string{filepath.Join(root, "media", "videos"), filepath.Join(root, "exports"), filepath.Join(root, ".shotforward", "staging")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "media", "videos", "source.mp4")
	command := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=320x180:d=1:r=24", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-an", source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create synthetic source: %v: %s", err, output)
	}
	request := exportRequest(t, root, source)
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	request.Clips[0].Artifact.ByteSize = info.Size()
	request.Clips[0].Artifact.Width = 320
	request.Clips[0].Artifact.Height = 180
	processor, err := NewFFmpegProcessor(ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := processor.Export(context.Background(), request, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", filepath.Join(root, filepath.FromSlash(relative)))
	output, err := probe.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "720x1280" {
		t.Fatalf("probe output = %q, %v", output, err)
	}
	profileProbe := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,pix_fmt,r_frame_rate,sample_rate,channels", "-of", "default=noprint_wrappers=1", filepath.Join(root, filepath.FromSlash(relative)))
	profile, err := profileProbe.CombinedOutput()
	profileText := string(profile)
	for _, expected := range []string{"codec_name=h264", "pix_fmt=yuv420p", "r_frame_rate=30/1", "codec_name=aac", "sample_rate=48000", "channels=2"} {
		if err != nil || !strings.Contains(profileText, expected) {
			t.Fatalf("fixed profile missing %q: %v: %s", expected, err, profileText)
		}
	}
}

func TestFFmpegProcessorNormalizesAndConcatenatesWithoutShell(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, directory := range []string{filepath.Join(root, "media", "videos"), filepath.Join(root, "exports"), filepath.Join(root, ".shotforward", "staging")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "media", "videos", "source.mp4")
	if err := os.WriteFile(source, []byte("source-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := exportRequest(t, root, source)
	runner := &recordingRunner{}
	processor := &FFmpegProcessor{executable: "/usr/bin/ffmpeg", runner: runner}
	progressCalls := 0
	relative, err := processor.Export(context.Background(), request, func(stage, message string) error {
		progressCalls++
		if stage == "" || message == "" {
			t.Fatal("empty progress")
		}
		return nil
	})
	if err != nil || relative != "exports/"+request.JobID.String()+".mp4" || progressCalls != 2 || len(runner.calls) != 2 {
		t.Fatalf("Export() = %q, %v, progress=%d calls=%d", relative, err, progressCalls, len(runner.calls))
	}
	first := strings.Join(runner.calls[0], " ")
	if !strings.Contains(first, "anullsrc=r=48000:cl=stereo") || !strings.Contains(first, "trim=start=0.100:end=0.900") || !strings.Contains(first, "scale=720:1280") {
		t.Fatalf("segment args = %q", first)
	}
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative))); err != nil || string(data) != "encoded" {
		t.Fatalf("output = %q, %v", data, err)
	}
	if err := processor.RemoveOutput(root, relative); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("source-vide0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Export(context.Background(), request, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("tampered source error = %v", err)
	}
}

type recordingRunner struct{ calls [][]string }

func (r *recordingRunner) Run(_ context.Context, _ string, arguments []string) error {
	r.calls = append(r.calls, append([]string(nil), arguments...))
	return os.WriteFile(arguments[len(arguments)-1], []byte("encoded"), 0o600)
}

func exportRequest(t *testing.T, root, source string) Request {
	t.Helper()
	clipID := domain.ClipID("clp_" + strings.Repeat("C", 26))
	slotID, _ := domain.FixedMediaSlotID(clipID.String(), domain.MediaSlotClipVideo)
	at := time.Unix(1_800_000_000, 0).UTC()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	artifact := domain.Artifact{ID: domain.ArtifactID("art_" + strings.Repeat("A", 26)), ProjectID: domain.ProjectID("prj_" + strings.Repeat("P", 26)), Kind: domain.MediaKindVideo, RelativePath: "media/videos/source.mp4", SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(data)), MIMEType: "video/mp4", OriginalFilename: "source.mp4", Width: 720, Height: 1280, DurationMilliseconds: 1000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: at}
	snapshot := domain.ExportClipSnapshot{SceneID: domain.SceneID("scn_" + strings.Repeat("S", 26)), ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 2, MediaVersionID: domain.MediaVersionID("mvr_" + strings.Repeat("V", 26)), MediaVersionRevision: 1, ArtifactID: artifact.ID, TrimInMilliseconds: 100, TrimOutMilliseconds: 900}
	return Request{JobID: domain.ExportJobID("exj_" + strings.Repeat("E", 26)), ProjectRoot: root, OutputWidth: 720, OutputHeight: 1280, Clips: []ClipSource{{Snapshot: snapshot, Artifact: artifact, AbsolutePath: source}}}
}
