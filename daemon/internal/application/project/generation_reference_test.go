package projectapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestNormalizeGenerationReferencesUsesExplicitRangesAndPreparation(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("A", 26))
	videoID := domain.ArtifactID("art_" + strings.Repeat("V", 26))
	audioID := domain.ArtifactID("art_" + strings.Repeat("U", 26))
	secondAudioID := domain.ArtifactID("art_" + strings.Repeat("S", 26))
	store := &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{
		videoID:       timedGenerationArtifact(videoID, projectID, domain.MediaKindVideo, "video/mp4", 150<<20, 20_000),
		audioID:       timedGenerationArtifact(audioID, projectID, domain.MediaKindAudio, "audio/mp4", 20<<20, 30_000),
		secondAudioID: timedGenerationArtifact(secondAudioID, projectID, domain.MediaKindAudio, "audio/mpeg", 10<<20, 14_994),
	}}
	capability := GenerationCapability{
		GeneratorID:         domain.GeneratorVideoWanOfficial,
		CapabilityVersion:   "wan-2026-09",
		MediaKind:           domain.MediaKindVideo,
		SupportedVideoModes: []domain.ClipGenerationMode{domain.ClipGenerationReference},
		SupportedDurations:  []int64{5, 10},
		ReferenceCapabilities: []GenerationReferenceCapability{
			{Role: domain.GenerationReferenceVideo, MaximumCount: 5, MaximumBytes: 100 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"video/mp4"}, SupportsAutomaticPreparation: true},
			{Role: domain.GenerationReferenceAudio, MaximumCount: 5, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 1_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/mpeg"}, SupportsAutomaticPreparation: true},
		},
		MaximumTotalTimedReferenceDurationMilliseconds: 20_000,
	}

	references, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{
		{Role: domain.GenerationReferenceVideo, ArtifactID: videoID, TrimStartMilliseconds: 2_000, TrimEndMilliseconds: 12_000},
		{Role: domain.GenerationReferenceAudio, ArtifactID: audioID, TrimStartMilliseconds: 4_000, TrimEndMilliseconds: 14_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range references {
		if !reference.PreparationRequired {
			t.Fatalf("reference should require a derived upload copy: %#v", reference)
		}
	}

	withoutRange := []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceVideo, ArtifactID: videoID}}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, withoutRange); err == nil {
		t.Fatal("full 20-second reference unexpectedly passed a 15-second limit")
	}

	tooMuchCombined := []domain.GenerationArtifactReference{
		{Role: domain.GenerationReferenceVideo, ArtifactID: videoID, TrimEndMilliseconds: 11_000},
		{Role: domain.GenerationReferenceAudio, ArtifactID: audioID, TrimEndMilliseconds: 10_000},
	}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, tooMuchCombined); err == nil {
		t.Fatal("combined timed references unexpectedly exceeded the route limit")
	}

	audioTotal := []domain.GenerationArtifactReference{
		{Role: domain.GenerationReferenceAudio, ArtifactID: audioID, TrimStartMilliseconds: 4_000, TrimEndMilliseconds: 14_000},
		{Role: domain.GenerationReferenceAudio, ArtifactID: secondAudioID, TrimEndMilliseconds: 14_994},
	}
	_, err = normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, audioTotal)
	var issue *GenerationCapabilityIssue
	if !errors.As(err, &issue) || issue.Constraint != "reference_total_duration" || issue.Role != domain.GenerationReferenceAudio || issue.Actual != 24_994 || issue.Maximum != 15_000 {
		t.Fatalf("audio total-duration issue = %#v, %v", issue, err)
	}
}

func TestNormalizeGenerationReferencesNeverHidesRequiredTrimming(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("B", 26))
	videoID := domain.ArtifactID("art_" + strings.Repeat("W", 26))
	store := &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{
		videoID: timedGenerationArtifact(videoID, projectID, domain.MediaKindVideo, "video/mp4", 101<<20, 10_000),
	}}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceVideo, MaximumCount: 1, MaximumBytes: 100 << 20}}}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceVideo, ArtifactID: videoID}}); err == nil {
		t.Fatal("oversized reference passed without automatic preparation support")
	}
}

func TestNormalizeGenerationReferencesResizesOnlyMatchingFrameAspectRatio(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("C", 26))
	frameID := domain.ArtifactID("art_" + strings.Repeat("F", 26))
	frame := domain.Artifact{ID: frameID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/images/frame.png", SHA256: strings.Repeat("f", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "frame.png", Width: 360, Height: 640, CreatedAt: time.Unix(1_800_000_000, 0).UTC()}
	store := &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{frameID: frame}}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceFirstImage, MaximumCount: 1}}}
	references, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationFirstImage, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: frameID}})
	if err != nil || !references[0].PreparationRequired {
		t.Fatalf("matching lower-resolution frame = %#v, %v", references, err)
	}
	frame.Width = 640
	frame.Height = 360
	store.artifacts[frameID] = frame
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationFirstImage, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: frameID}}); !errors.Is(err, ErrMediaAspectRatioMismatch) {
		t.Fatalf("wrong-aspect frame error = %v", err)
	}
}

func TestNormalizeGenerationReferencesAllowsProjectLibraryImagesAcrossEpisodes(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("P", 26))
	imageID := domain.ArtifactID("art_" + strings.Repeat("I", 26))
	image := domain.Artifact{ID: imageID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/images/project-reference.png", SHA256: strings.Repeat("1", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "project-reference.png", Width: 720, Height: 1280, CreatedAt: time.Unix(1_800_000_000, 0).UTC()}
	selected := false
	store := &fakeStore{
		artifacts:        map[domain.ArtifactID]domain.Artifact{imageID: image},
		artifactSelected: &selected,
	}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceImage, MaximumCount: 1, AcceptedMIMETypes: []string{"image/png"}}}}

	references, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceImage, ArtifactID: imageID}})
	if err != nil || len(references) != 1 || references[0].ArtifactID != imageID {
		t.Fatalf("project image reference = %#v, %v", references, err)
	}

	capability.ReferenceCapabilities[0].Role = domain.GenerationReferenceFirstImage
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationFirstImage, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceFirstImage, ArtifactID: imageID}}); err == nil {
		t.Fatal("cross-Episode first frame unexpectedly passed")
	}
}

func TestNormalizeGenerationReferencesEnforcesPublishedDependencies(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("D", 26))
	imageID := domain.ArtifactID("art_" + strings.Repeat("J", 26))
	audioID := domain.ArtifactID("art_" + strings.Repeat("A", 26))
	store := &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{
		imageID: {ID: imageID, ProjectID: projectID, Kind: domain.MediaKindImage, RelativePath: "media/images/reference.png", SHA256: strings.Repeat("2", 64), ByteSize: 10, MIMEType: "image/png", OriginalFilename: "reference.png", Width: 720, Height: 1280, CreatedAt: time.Unix(1_800_000_000, 0).UTC()},
		audioID: timedGenerationArtifact(audioID, projectID, domain.MediaKindAudio, "audio/mpeg", 10, 5_000),
	}}
	capability := GenerationCapability{MinimumTotalReferences: 1, MaximumTotalReferences: 12, ReferenceCapabilities: []GenerationReferenceCapability{
		{Role: domain.GenerationReferenceImage, GenerationModes: []domain.ClipGenerationMode{domain.ClipGenerationReference}, MaximumCount: 9, AcceptedMIMETypes: []string{"image/png"}},
		{Role: domain.GenerationReferenceAudio, GenerationModes: []domain.ClipGenerationMode{domain.ClipGenerationReference}, MaximumCount: 3, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/mpeg"}, RequiresAnyOf: []domain.GenerationArtifactRole{domain.GenerationReferenceImage, domain.GenerationReferenceVideo}},
	}}
	audio := domain.GenerationArtifactReference{Role: domain.GenerationReferenceAudio, ArtifactID: audioID, TrimEndMilliseconds: 5_000}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{audio}); err == nil {
		t.Fatal("audio-only references unexpectedly passed the published dependency")
	}
	image := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: imageID}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{image, audio}); err != nil {
		t.Fatalf("image plus audio references failed: %v", err)
	}
}

func generationTestEpisodeID() domain.EpisodeID {
	return domain.EpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func TestGenerationCapabilityCanLimitInputVideoPlusOutput(t *testing.T) {
	t.Parallel()
	snapshot := domain.GenerationInputSnapshot{
		SchemaVersion:            domain.GenerationSnapshotVersion,
		TargetMediaKind:          domain.MediaKindVideo,
		Prompt:                   "延续动作",
		GenerationMode:           domain.ClipGenerationReference,
		GeneratorID:              domain.GeneratorVideoWanOfficial,
		CapabilityVersion:        "wan-2026-09",
		OutputWidth:              720,
		OutputHeight:             1280,
		RequestedDurationSeconds: 20,
		References: []domain.GenerationArtifactReference{{
			Role: domain.GenerationReferenceVideo, ArtifactID: domain.ArtifactID("art_" + strings.Repeat("R", 26)), TrimEndMilliseconds: 15_000,
		}},
		ParametersJSON:     "{}",
		TargetSlotRevision: 1,
	}
	capability := GenerationCapability{
		GeneratorID:            domain.GeneratorVideoWanOfficial,
		CapabilityVersion:      "wan-2026-09",
		MediaKind:              domain.MediaKindVideo,
		SupportedVideoModes:    []domain.ClipGenerationMode{domain.ClipGenerationReference},
		MinimumDurationSeconds: 2,
		MaximumDurationSeconds: 30,
		MaximumInputVideoPlusOutputDurationMilliseconds: 30_000,
	}
	if err := validateGenerationCapability(capability, snapshot); err == nil {
		t.Fatal("15 seconds of input plus 20 seconds of output unexpectedly passed a 30-second limit")
	}
	snapshot.RequestedDurationSeconds = 15
	if err := validateGenerationCapability(capability, snapshot); err != nil {
		t.Fatalf("15 seconds of input plus 15 seconds of output: %v", err)
	}
}

func timedGenerationArtifact(id domain.ArtifactID, projectID domain.ProjectID, kind domain.MediaKind, mime string, bytes, duration int64) domain.Artifact {
	artifact := domain.Artifact{ID: id, ProjectID: projectID, Kind: kind, RelativePath: "media/reference/" + id.String(), SHA256: strings.Repeat("a", 64), ByteSize: bytes, MIMEType: mime, OriginalFilename: id.String(), DurationMilliseconds: duration, CreatedAt: time.Unix(1_800_000_000, 0).UTC()}
	if kind == domain.MediaKindVideo {
		artifact.Width, artifact.Height = 1920, 1080
		artifact.FPSNumerator, artifact.FPSDenominator = 24, 1
	} else {
		artifact.AudioSampleRate, artifact.AudioChannels = 48_000, 2
	}
	return artifact
}
