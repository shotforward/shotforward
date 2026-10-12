package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerationJobTransitions(t *testing.T) {
	t.Parallel()
	allowed := [][2]GenerationJobState{
		{GenerationJobQueued, GenerationJobRunning},
		{GenerationJobQueued, GenerationJobCancelled},
		{GenerationJobRunning, GenerationJobSucceeded},
		{GenerationJobRunning, GenerationJobFailed},
		{GenerationJobRunning, GenerationJobStopRequested},
		{GenerationJobStopRequested, GenerationJobSucceeded},
		{GenerationJobStopRequested, GenerationJobFailed},
		{GenerationJobStopRequested, GenerationJobStopped},
	}
	for _, transition := range allowed {
		if !CanTransitionGenerationJob(transition[0], transition[1]) {
			t.Errorf("CanTransitionGenerationJob(%q, %q) = false", transition[0], transition[1])
		}
	}
	for _, transition := range [][2]GenerationJobState{
		{GenerationJobQueued, GenerationJobSucceeded},
		{GenerationJobRunning, GenerationJobCancelled},
		{GenerationJobSucceeded, GenerationJobRunning},
		{GenerationJobStopped, GenerationJobQueued},
	} {
		if CanTransitionGenerationJob(transition[0], transition[1]) {
			t.Errorf("CanTransitionGenerationJob(%q, %q) = true", transition[0], transition[1])
		}
	}
}

func TestGenerationSnapshotValidatesModeReferences(t *testing.T) {
	t.Parallel()
	artifactID := ArtifactID("art_" + strings.Repeat("A", 26))
	lastArtifactID := ArtifactID("art_" + strings.Repeat("C", 26))
	base := GenerationInputSnapshot{
		SchemaVersion:            GenerationSnapshotVersion,
		TargetMediaKind:          MediaKindVideo,
		Prompt:                   "镜头向前推进",
		GenerationMode:           ClipGenerationFirstLastImage,
		GeneratorID:              GeneratorVideoWanOfficial,
		CapabilityVersion:        "2026-09",
		OutputWidth:              720,
		OutputHeight:             1280,
		RequestedDurationSeconds: 5,
		References: []GenerationArtifactReference{
			{Role: GenerationReferenceFirstImage, ArtifactID: artifactID},
			{Role: GenerationReferenceLastImage, ArtifactID: lastArtifactID},
		},
		ParametersJSON:     "{}",
		TargetSlotRevision: 1,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid first/last snapshot error = %v", err)
	}
	audioID := ArtifactID("art_" + strings.Repeat("B", 26))
	withAudio := base
	withAudio.GenerationMode = ClipGenerationReference
	withAudio.References = []GenerationArtifactReference{{Role: GenerationReferenceAudio, ArtifactID: audioID, TrimEndMilliseconds: 5_000}}
	if err := withAudio.Validate(); err != nil {
		t.Fatalf("optional audio reference error = %v", err)
	}

	missingLast := base
	missingLast.References = missingLast.References[:1]
	if err := missingLast.Validate(); !errors.Is(err, ErrInvalidGenerationSnapshot) {
		t.Fatalf("missing last image error = %v", err)
	}

	image := base
	image.TargetMediaKind = MediaKindImage
	image.GeneratorID = GeneratorImageShotForward
	image.GenerationMode = ""
	image.RequestedDurationSeconds = 0
	image.References = nil
	if err := image.Validate(); err != nil {
		t.Fatalf("valid image snapshot error = %v", err)
	}
	for _, size := range [][2]int{{864, 1536}, {1536, 864}, {1536, 1536}} {
		assetImage := image
		assetImage.OutputWidth, assetImage.OutputHeight = size[0], size[1]
		if err := assetImage.Validate(); err != nil {
			t.Errorf("valid 1.5K image snapshot %dx%d error = %v", size[0], size[1], err)
		}
	}
	invalidImageSize := image
	invalidImageSize.OutputWidth, invalidImageSize.OutputHeight = 1000, 1500
	if err := invalidImageSize.Validate(); !errors.Is(err, ErrInvalidGenerationSnapshot) {
		t.Errorf("unsupported image snapshot size error = %v", err)
	}
	invalidVideoSize := base
	invalidVideoSize.OutputWidth, invalidVideoSize.OutputHeight = 1536, 864
	if err := invalidVideoSize.Validate(); !errors.Is(err, ErrInvalidGenerationSnapshot) {
		t.Errorf("1.5K video snapshot size error = %v", err)
	}
	image.ParametersJSON = "[]"
	if err := image.Validate(); !errors.Is(err, ErrInvalidGenerationSnapshot) {
		t.Fatalf("non-object parameters error = %v", err)
	}
}

func TestGenerationJobValidatesStateShape(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	job := GenerationJob{
		ID:           GenerationJobID("gjb_" + strings.Repeat("A", 26)),
		BatchID:      GenerationBatchID("gbt_" + strings.Repeat("A", 26)),
		ProjectID:    ProjectID("prj_" + strings.Repeat("A", 26)),
		EpisodeID:    EpisodeID("ep_" + strings.Repeat("A", 26)),
		TargetSlotID: MediaSlotID("msl_A" + strings.Repeat("A", 26)),
		State:        GenerationJobQueued,
		Snapshot: GenerationInputSnapshot{
			SchemaVersion:      GenerationSnapshotVersion,
			TargetMediaKind:    MediaKindImage,
			Prompt:             "生成图片",
			GeneratorID:        GeneratorImageShotForward,
			OutputWidth:        720,
			OutputHeight:       1280,
			ParametersJSON:     "{}",
			TargetSlotRevision: 1,
		},
		ProgressStage: "queued",
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := job.Validate(); err != nil {
		t.Fatalf("queued Job validation error = %v", err)
	}
	started := now.Add(time.Second)
	job.State = GenerationJobRunning
	job.StartedAt = &started
	job.UpdatedAt = started
	if err := job.Validate(); err != nil {
		t.Fatalf("running Job validation error = %v", err)
	}
	job.State = GenerationJobStopRequested
	if err := job.Validate(); !errors.Is(err, ErrInvalidGenerationJob) {
		t.Fatalf("unsupported stop request error = %v", err)
	}
}

func TestGenerationBatchStateIsDerivedFromJobs(t *testing.T) {
	t.Parallel()
	states := func(values ...GenerationJobState) GenerationBatchState {
		jobs := make([]GenerationJob, len(values))
		for index, value := range values {
			jobs[index].State = value
		}
		return (GenerationBatchDetails{Jobs: jobs}).DerivedState()
	}
	for name, test := range map[string]struct {
		jobs []GenerationJobState
		want GenerationBatchState
	}{
		"queued":    {[]GenerationJobState{GenerationJobQueued, GenerationJobQueued}, GenerationBatchQueued},
		"running":   {[]GenerationJobState{GenerationJobSucceeded, GenerationJobRunning}, GenerationBatchRunning},
		"succeeded": {[]GenerationJobState{GenerationJobSucceeded}, GenerationBatchSucceeded},
		"failed":    {[]GenerationJobState{GenerationJobFailed, GenerationJobFailed}, GenerationBatchFailed},
		"cancelled": {[]GenerationJobState{GenerationJobCancelled}, GenerationBatchCancelled},
		"stopped":   {[]GenerationJobState{GenerationJobStopped}, GenerationBatchStopped},
		"partial":   {[]GenerationJobState{GenerationJobSucceeded, GenerationJobFailed}, GenerationBatchPartial},
	} {
		if got := states(test.jobs...); got != test.want {
			t.Errorf("%s DerivedState() = %q, want %q", name, got, test.want)
		}
	}
}
