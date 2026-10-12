package project

import (
	"strings"
	"testing"
	"time"
)

func TestExportJobValidatesFrozenClipOrderAndLifecycle(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_800_000_000, 0).UTC()
	clipID := ClipID("clp_" + strings.Repeat("C", 26))
	slotID, _ := FixedMediaSlotID(clipID.String(), MediaSlotClipVideo)
	job := ExportJob{
		ID: ExportJobID("exj_" + strings.Repeat("E", 26)), ProjectID: ProjectID("prj_" + strings.Repeat("P", 26)), EpisodeID: EpisodeID("ep_" + strings.Repeat("D", 26)),
		OperationKey: "export-one", State: ExportJobQueued, ProgressStage: "queued", Revision: 1, CreatedAt: at, UpdatedAt: at,
		Snapshot: ExportSnapshot{SchemaVersion: ExportSnapshotVersion, StoryboardRevision: 3, OutputWidth: 720, OutputHeight: 1280, Clips: []ExportClipSnapshot{{
			SceneID: SceneID("scn_" + strings.Repeat("S", 26)), ClipID: clipID, Position: 1, MediaSlotID: slotID, MediaSlotRevision: 2,
			MediaVersionID: MediaVersionID("mvr_" + strings.Repeat("V", 26)), MediaVersionRevision: 4, ArtifactID: ArtifactID("art_" + strings.Repeat("A", 26)), TrimInMilliseconds: 100, TrimOutMilliseconds: 900,
		}}},
	}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.State = ExportJobSucceeded
	job.StartedAt = ptrTime(at.Add(time.Second))
	job.FinishedAt = ptrTime(at.Add(2 * time.Second))
	job.UpdatedAt = *job.FinishedAt
	job.OutputRelativePath = "exports/" + job.ID.String() + ".mp4"
	job.Revision = 3
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.Snapshot.Clips[0].Position = 2
	if err := job.Validate(); err == nil {
		t.Fatal("non-contiguous export positions accepted")
	}
}

func TestExportSnapshotOutputEqualityTracksRenderedInputs(t *testing.T) {
	clipID := ClipID("clp_" + strings.Repeat("C", 26))
	slotID, _ := FixedMediaSlotID(clipID.String(), MediaSlotClipVideo)
	base := ExportSnapshot{
		SchemaVersion: ExportSnapshotVersion, StoryboardRevision: 3, OutputWidth: 720, OutputHeight: 1280,
		Clips: []ExportClipSnapshot{{
			SceneID: SceneID("scn_" + strings.Repeat("S", 26)), ClipID: clipID, Position: 1,
			MediaSlotID: slotID, MediaSlotRevision: 2, MediaVersionID: MediaVersionID("mvr_" + strings.Repeat("V", 26)),
			MediaVersionRevision: 4, ArtifactID: ArtifactID("art_" + strings.Repeat("A", 26)), TrimInMilliseconds: 100, TrimOutMilliseconds: 900,
		}},
	}
	coordinationOnly := base
	coordinationOnly.StoryboardRevision++
	coordinationOnly.Clips = append([]ExportClipSnapshot(nil), base.Clips...)
	coordinationOnly.Clips[0].MediaSlotRevision++
	if !base.HasSameOutput(coordinationOnly) {
		t.Fatal("coordination-only revisions invalidated identical output")
	}
	changedTrim := coordinationOnly
	changedTrim.Clips = append([]ExportClipSnapshot(nil), coordinationOnly.Clips...)
	changedTrim.Clips[0].TrimOutMilliseconds--
	if base.HasSameOutput(changedTrim) {
		t.Fatal("changed trim reused stale output")
	}
	changedDimensions := base
	changedDimensions.OutputWidth = 1080
	if base.HasSameOutput(changedDimensions) {
		t.Fatal("changed output dimensions reused stale output")
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
