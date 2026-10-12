package generation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/mediafiles"
)

func TestGeneratorRequestMaterializesFrozenTimedReferenceRanges(t *testing.T) {
	ctx := context.Background()
	processor := schedulerTrimProcessor(t)
	fixture := newSchedulerFixture(t, 1)
	job := timedReferenceJob(fixture)
	originals := make(map[domain.ArtifactID]domain.Artifact)
	contents := make(map[domain.ArtifactID][]byte)
	for index, kind := range []domain.MediaKind{domain.MediaKindVideo, domain.MediaKindAudio} {
		artifact := schedulerTrimArtifact(t, fixture, processor, kind, string(rune('V'+index)))
		originals[artifact.ID] = artifact
		content, err := os.ReadFile(filepath.Join(fixture.root, artifact.RelativePath))
		if err != nil {
			t.Fatal(err)
		}
		contents[artifact.ID] = content
		role := domain.GenerationReferenceVideo
		if kind == domain.MediaKindAudio {
			role = domain.GenerationReferenceAudio
		}
		job.Snapshot.References = append(job.Snapshot.References, domain.GenerationArtifactReference{
			Role: role, ArtifactID: artifact.ID, TrimStartMilliseconds: 500, TrimEndMilliseconds: 1500, PreparationRequired: true,
		})
		// The library can change after a task's snapshot was taken. Execution
		// must not reinterpret that snapshot using the latest selection.
		entry, err := fixture.store.EpisodeReferenceMedia(ctx, fixture.episode.ID, artifact.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		entry.TrimStartMilliseconds, entry.TrimEndMilliseconds = 1500, 1750
		if err := fixture.store.UpdateEpisodeReferenceMedia(ctx, entry, entry.Revision, entry.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	frozen := append([]domain.GenerationArtifactReference(nil), job.Snapshot.References...)
	scheduler := &Scheduler{mediaFiles: processor}
	request, cleanup, err := scheduler.generatorRequest(ctx, projectapp.ProjectHandle{ProjectID: fixture.project.ID, RootPath: fixture.root, Store: fixture.store}, job)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !reflect.DeepEqual(job.Snapshot.References, frozen) {
		t.Fatal("execution mutated the task's saved selection")
	}
	for _, reference := range request.References {
		original := originals[reference.Artifact.ID]
		if reference.AbsolutePath == filepath.Join(fixture.root, original.RelativePath) || reference.Artifact.SHA256 == original.SHA256 {
			t.Fatal("generator received the original instead of a physical selection")
		}
		if reference.Artifact.RemoteReferenceURL != "" || reference.Artifact.RemoteReferenceExpiresAt != nil {
			t.Fatal("generator could reuse the original remote URL")
		}
		if reference.Artifact.DurationMilliseconds < 950 || reference.Artifact.DurationMilliseconds > 1100 || reference.Input.TrimStartMilliseconds != 0 || reference.Input.TrimEndMilliseconds != reference.Artifact.DurationMilliseconds || reference.Input.PreparationRequired {
			t.Fatalf("generator reference does not describe the prepared file: %#v", reference)
		}
		if info, err := os.Stat(reference.AbsolutePath); err != nil || info.Size() != reference.Artifact.ByteSize {
			t.Fatalf("generator upload file/metadata mismatch: %v, %v", info, err)
		}
		persisted, err := fixture.store.Artifact(ctx, original.ID)
		if err != nil || !reflect.DeepEqual(persisted, original) {
			t.Fatalf("original Artifact changed: %#v, %v", persisted, err)
		}
		content, err := os.ReadFile(filepath.Join(fixture.root, original.RelativePath))
		if err != nil || !bytes.Equal(content, contents[original.ID]) {
			t.Fatalf("original file changed: %v", err)
		}
	}
	cleanup()
	for _, reference := range request.References {
		if _, err := os.Stat(reference.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary upload not discarded: %v", err)
		}
	}
}

func TestGeneratorRequestTrimFailureNeverFallsBackAndCleansEarlierSelections(t *testing.T) {
	processor := schedulerTrimProcessor(t)
	fixture := newSchedulerFixture(t, 1)
	job := timedReferenceJob(fixture)
	for index, kind := range []domain.MediaKind{domain.MediaKindVideo, domain.MediaKindAudio} {
		artifact := schedulerTrimArtifact(t, fixture, processor, kind, string(rune('X'+index)))
		role := domain.GenerationReferenceVideo
		if kind == domain.MediaKindAudio {
			role = domain.GenerationReferenceAudio
			// Exact test-owned fixture only. The original's remote URL still
			// exists, but it must never be used as a fallback for failed trim.
			if err := os.Remove(filepath.Join(fixture.root, artifact.RelativePath)); err != nil {
				t.Fatal(err)
			}
		}
		job.Snapshot.References = append(job.Snapshot.References, domain.GenerationArtifactReference{
			Role: role, ArtifactID: artifact.ID, TrimStartMilliseconds: 500, TrimEndMilliseconds: 1500, PreparationRequired: true,
		})
	}
	scheduler := &Scheduler{mediaFiles: processor}
	request, cleanup, err := scheduler.generatorRequest(context.Background(), projectapp.ProjectHandle{ProjectID: fixture.project.ID, RootPath: fixture.root, Store: fixture.store}, job)
	cleanup()
	if err == nil || len(request.References) != 0 {
		t.Fatalf("failed trim returned a generator request: %#v, %v", request, err)
	}
	files, err := filepath.Glob(filepath.Join(fixture.root, "media", ".staging", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed preparation left earlier selections: %v, %v", files, err)
	}
}

func timedReferenceJob(fixture *schedulerFixture) domain.GenerationJob {
	job := fixture.jobs[0]
	job.Snapshot = domain.GenerationInputSnapshot{
		SchemaVersion: domain.GenerationSnapshotVersion, TargetMediaKind: domain.MediaKindVideo,
		Prompt: "参考素材", GenerationMode: domain.ClipGenerationReference, GeneratorID: domain.GeneratorVideoH3ShotForward,
		CapabilityVersion: "1", OutputWidth: 720, OutputHeight: 1280, RequestedDurationSeconds: 4, ParametersJSON: "{}", TargetSlotRevision: 1,
	}
	return job
}

func schedulerTrimProcessor(t *testing.T) mediafiles.Processor {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	processor, err := mediafiles.NewProcessor(ffprobe, ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func schedulerTrimArtifact(t *testing.T, fixture *schedulerFixture, processor mediafiles.Processor, kind domain.MediaKind, suffix string) domain.Artifact {
	t.Helper()
	ffmpeg, _ := exec.LookPath("ffmpeg")
	name := "original-" + suffix + ".wav"
	arguments := []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000:duration=2"}
	if kind == domain.MediaKindVideo {
		name = "original-" + suffix + ".mp4"
		arguments = []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=64x36:rate=20:duration=2", "-c:v", "mpeg4"}
	}
	path := filepath.Join(fixture.root, name)
	if output, err := exec.Command(ffmpeg, append(arguments, path)...).CombinedOutput(); err != nil {
		t.Fatalf("create source: %v: %s", err, output)
	}
	prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: fixture.root, SourcePath: path, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.Discard(fixture.root, prepared); err != nil {
		t.Fatal(err)
	}
	expires := fixture.project.CreatedAt.Add(24 * time.Hour)
	artifact := domain.Artifact{
		ID: domain.ArtifactID("art_" + strings.Repeat(suffix, 26)), ProjectID: fixture.project.ID, Kind: kind,
		RelativePath: name, SHA256: prepared.SHA256, ByteSize: prepared.ByteSize, MIMEType: prepared.MIMEType, OriginalFilename: name,
		Width: prepared.Width, Height: prepared.Height, DurationMilliseconds: prepared.DurationMilliseconds,
		FPSNumerator: prepared.FPSNumerator, FPSDenominator: prepared.FPSDenominator, AudioSampleRate: prepared.AudioSampleRate, AudioChannels: prepared.AudioChannels,
		RemoteReferenceURL: "https://example.invalid/original-" + suffix, RemoteReferenceExpiresAt: &expires, CreatedAt: fixture.project.CreatedAt,
	}
	entry := domain.EpisodeReferenceMedia{EpisodeID: fixture.episode.ID, Artifact: artifact, Name: "Reference " + suffix,
		TrimStartMilliseconds: 500, TrimEndMilliseconds: 1500, Revision: 1, CreatedAt: artifact.CreatedAt, UpdatedAt: artifact.CreatedAt}
	if err := fixture.store.PutEpisodeReferenceMedia(context.Background(), entry, entry.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return artifact
}
