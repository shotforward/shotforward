package projectstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestClipPromptChangeSetScopesSharedUploadedMediaToEpisode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		kind           project.MediaKind
		currentPresent bool
		currentDeleted bool
		otherDeleted   bool
		wantRejected   bool
	}{
		{name: "same audio is active in both Episodes", currentPresent: true},
		{name: "audio is deleted in the other Episode", currentPresent: true, otherDeleted: true},
		{name: "audio is absent from the current Episode", wantRejected: true},
		{name: "audio is deleted in the current Episode", currentPresent: true, currentDeleted: true, wantRejected: true},
		{name: "same image is active in both Episodes", kind: project.MediaKindImage, currentPresent: true},
		{name: "same video is active in both Episodes", kind: project.MediaKindVideo, currentPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newChangeSetFixture(t)
			defer fixture.store.Close()
			other := testSecondEpisode(t, fixture.project, fixture.episode)
			if err := fixture.store.CreateEpisode(ctx, other, fixture.nextTime()); err != nil {
				t.Fatal(err)
			}

			assetIDs := []project.AssetID{fixture.asset.ID}
			for _, suffix := range []string{"H", "I", "J"} {
				at := fixture.nextTime()
				identity, asset := assetModels(t, fixture.project.ID, fixture.episode.ID, suffix, suffix, project.AssetKindProp, "道具"+suffix, "visual reference", at)
				if err := fixture.store.CreateAsset(ctx, &identity, asset, at); err != nil {
					t.Fatal(err)
				}
				assetIDs = append(assetIDs, asset.ID)
			}
			for index, assetID := range assetIDs {
				if err := fixture.store.BindClipAsset(ctx, fixture.clip.ID, assetID, int64(index+1), fixture.nextTime()); err != nil {
					t.Fatal(err)
				}
			}
			before, err := fixture.store.Clip(ctx, fixture.clip.ID)
			if err != nil {
				t.Fatal(err)
			}

			at := fixture.nextTime()
			media := project.Artifact{
				ID: project.ArtifactID("art_" + strings.Repeat("U", 26)), ProjectID: fixture.project.ID, Kind: project.MediaKindAudio,
				RelativePath: "media/audio/bubu.mp3", SHA256: strings.Repeat("a", 64), ByteSize: 128,
				MIMEType: "audio/mpeg", OriginalFilename: "bubu.mp3", DurationMilliseconds: 15_000,
				AudioSampleRate: 44_100, AudioChannels: 1, CreatedAt: at,
			}
			if test.kind == project.MediaKindImage || test.kind == project.MediaKindVideo {
				media.Kind, media.Width, media.Height = test.kind, 720, 1280
				media.AudioSampleRate, media.AudioChannels = 0, 0
				if test.kind == project.MediaKindImage {
					media.RelativePath, media.MIMEType, media.OriginalFilename, media.DurationMilliseconds = "media/images/reference.png", "image/png", "reference.png", 0
				} else {
					media.RelativePath, media.MIMEType, media.OriginalFilename = "media/videos/reference.mp4", "video/mp4", "reference.mp4"
					media.FPSNumerator, media.FPSDenominator = 24, 1
				}
			}
			// The same immutable Artifact is registered in another Episode first,
			// as happens when a later upload reuses a Project-wide SHA256 match.
			putReference := func(episodeID project.EpisodeID) {
				t.Helper()
				at := fixture.nextTime()
				if err := fixture.store.PutEpisodeReferenceMedia(ctx, project.EpisodeReferenceMedia{
					EpisodeID: episodeID, Artifact: media, Name: "上传参考", Description: "上传素材参考。",
					Revision: 1, CreatedAt: at, UpdatedAt: at,
				}, at); err != nil {
					t.Fatal(err)
				}
			}
			deleteReference := func(episodeID project.EpisodeID) {
				t.Helper()
				value, err := fixture.store.EpisodeReferenceMedia(ctx, episodeID, media.ID, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.DeleteEpisodeReferenceMedia(ctx, value, value.Revision, fixture.nextTime()); err != nil {
					t.Fatal(err)
				}
			}
			putReference(other.ID)
			if test.currentPresent {
				putReference(fixture.episode.ID)
			}
			if test.otherDeleted {
				deleteReference(other.ID)
			}
			if test.currentDeleted {
				deleteReference(fixture.episode.ID)
			}

			value := fixture.begin(t, "begin-shared-audio", "1")
			operationID := changeset.OperationID("cop_" + strings.Repeat("U", 26))
			prompt := "@角色 说话，使用 @上传参考 作为参考素材。"
			operation := changeset.Operation{
				ID: operationID, ChangeSetID: value.ID, Position: 1, Type: changeset.UpdateClipPrompts,
				Precondition: changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("U", 26)), OperationID: operationID, TargetKind: changeset.TargetClip, TargetID: before.Clip.ID.String(), ExpectedRevision: before.Clip.Revision},
				ClipPrompts: &changeset.ClipPromptsChange{
					ClipID: before.Clip.ID, VideoMode: project.ClipGenerationReference,
					FirstBefore: before.Clip.FirstImagePrompt, FirstAfter: before.Clip.FirstImagePrompt,
					LastBefore: before.Clip.LastImagePrompt, LastAfter: before.Clip.LastImagePrompt,
					VideoBefore: before.Clip.VideoPrompts.Reference, VideoAfter: prompt,
					AssetIDsBefore: assetIDs, AssetIDsAfter: assetIDs, AssetBindingsRecorded: true,
					ReferenceArtifactIDsAfter: []project.ArtifactID{media.ID}, ReferenceBindingsRecorded: true,
				},
			}
			appendAt := fixture.nextTime()
			appended, replay, err := fixture.store.AppendChangeSetOperations(ctx, value.ID, value.Revision, []changeset.Operation{operation}, appendAt, fixture.receipt(value.ID, changeset.CommandAppend, "append-shared-audio", "2", appendAt))
			if test.wantRejected {
				if !errors.Is(err, ErrChangeSetPreconditionFailed) || !strings.Contains(err.Error(), "reference_media_invalid") || replay {
					t.Fatalf("AppendChangeSetOperations() = %+v, replay=%v, err=%v; want reference_media_invalid", appended, replay, err)
				}
				unchanged, err := fixture.store.Clip(ctx, fixture.clip.ID)
				if err != nil || !reflect.DeepEqual(unchanged, before) {
					t.Fatalf("rejected binding changed Clip = %+v, %v", unchanged, err)
				}
				assertChangeSetUploadedMediaReferences(t, fixture, before.AssetReferences, before.Clip.VideoPrompts.Reference, "")
				stored, err := fixture.store.ChangeSet(ctx, value.ID)
				if err != nil || stored.Status != changeset.StatusDraft || stored.OperationCount != 0 || stored.Revision != value.Revision {
					t.Fatalf("rejected append changed draft = %+v, %v", stored, err)
				}
				return
			}
			if err != nil || replay {
				t.Fatalf("AppendChangeSetOperations() = %+v, replay=%v, err=%v", appended, replay, err)
			}
			value = fixture.finalize(t, appended, "finalize-shared-audio", "3")
			applyAt := fixture.nextTime()
			applied, replay, err := fixture.store.ApplyChangeSet(ctx, value.ID, value.Revision, changeset.ConflictID("ccf_"+strings.Repeat("U", 26)), applyAt, fixture.receipt(value.ID, changeset.CommandApply, "apply-shared-audio", "4", applyAt))
			if err != nil || replay || applied.Status != changeset.StatusApplied {
				t.Fatalf("ApplyChangeSet() = %+v, replay=%v, err=%v", applied, replay, err)
			}
			assertChangeSetUploadedMediaReferences(t, fixture, before.AssetReferences, prompt, media.ID)

			undoAt := fixture.nextTime()
			undo := changeset.ChangeSet{
				ID: changeset.ChangeSetID("cst_" + strings.Repeat("V", 26)), ProjectID: fixture.project.ID, EpisodeID: fixture.episode.ID,
				Source: applied.Source, Title: "Undo shared audio binding", Status: changeset.StatusDraft, Revision: 1, CreatedAt: undoAt, UpdatedAt: undoAt,
			}
			undo, replay, err = fixture.store.CreateChangeSet(ctx, undo, fixture.receipt(undo.ID, changeset.CommandBegin, "begin-shared-audio-undo", "5", undoAt))
			if err != nil || replay {
				t.Fatalf("Create undo ChangeSet() = %+v, replay=%v, err=%v", undo, replay, err)
			}
			revertID := changeset.OperationID("cop_" + strings.Repeat("V", 26))
			undo = fixture.append(t, undo, []changeset.Operation{{
				ID: revertID, ChangeSetID: undo.ID, Position: 1, Type: changeset.RevertChangeSet,
				Precondition:    changeset.Precondition{ID: changeset.PreconditionID("cpc_" + strings.Repeat("V", 26)), OperationID: revertID, TargetKind: changeset.TargetChangeSet, TargetID: applied.ID.String(), ExpectedRevision: applied.Revision},
				ChangeSetRevert: &changeset.ChangeSetRevert{OriginalChangeSetID: applied.ID},
			}}, "append-shared-audio-undo", "6")
			undo = fixture.finalize(t, undo, "finalize-shared-audio-undo", "7")
			undoAt = fixture.nextTime()
			undo, replay, err = fixture.store.ApplyChangeSet(ctx, undo.ID, undo.Revision, changeset.ConflictID("ccf_"+strings.Repeat("V", 26)), undoAt, fixture.receipt(undo.ID, changeset.CommandApply, "apply-shared-audio-undo", "8", undoAt))
			if err != nil || replay || undo.Status != changeset.StatusApplied {
				t.Fatalf("Apply undo ChangeSet() = %+v, replay=%v, err=%v", undo, replay, err)
			}
			assertChangeSetUploadedMediaReferences(t, fixture, before.AssetReferences, before.Clip.VideoPrompts.Reference, "")
		})
	}
}

func assertChangeSetUploadedMediaReferences(t *testing.T, fixture *changeSetFixture, assets []project.ClipAssetReference, prompt string, artifactID project.ArtifactID) {
	t.Helper()
	ctx := context.Background()
	clip, err := fixture.store.Clip(ctx, fixture.clip.ID)
	if err != nil || !reflect.DeepEqual(clip.AssetReferences, assets) || clip.Clip.VideoPrompts.Reference != prompt || clip.Clip.FirstImagePrompt != fixture.clip.FirstImagePrompt || clip.Clip.LastImagePrompt != fixture.clip.LastImagePrompt || clip.Clip.VideoPrompts.FirstImage != fixture.clip.VideoPrompts.FirstImage || clip.Clip.VideoPrompts.FirstLastImage != fixture.clip.VideoPrompts.FirstLastImage {
		t.Fatalf("Clip prompts or visual references changed unexpectedly = %+v, %v", clip, err)
	}
	references, err := fixture.store.ClipReferenceArtifacts(ctx, fixture.clip.ID)
	if err != nil || artifactID == "" && len(references) != 0 || artifactID != "" && (len(references) != 1 || references[0].ID != artifactID) {
		t.Fatalf("Clip uploaded-media references = %+v, %v; want %q", references, err, artifactID)
	}
}
