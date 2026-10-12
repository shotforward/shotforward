package projectapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// Embed the existing store fake so library lookup is scoped to the exact
// Episode, while all other reference validation uses the production path.
type generationLibraryTrimStore struct {
	*fakeStore
	library map[domain.EpisodeID]map[domain.ArtifactID]domain.EpisodeReferenceMedia
	readErr error
}

func (s *generationLibraryTrimStore) EpisodeReferenceMedia(_ context.Context, episodeID domain.EpisodeID, artifactID domain.ArtifactID, includeDeleted bool) (domain.EpisodeReferenceMedia, error) {
	if s.readErr != nil {
		return domain.EpisodeReferenceMedia{}, s.readErr
	}
	if item, ok := s.library[episodeID][artifactID]; ok {
		if item.DeletedAt != nil && !includeDeleted {
			return domain.EpisodeReferenceMedia{}, ErrArtifactNotFound
		}
		return item, nil
	}
	return domain.EpisodeReferenceMedia{}, ErrArtifactNotFound
}

func TestNormalizeGenerationReferencesDoesNotReviveDeletedLibraryMediaThroughAnotherSlot(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("L", 26))
	episodeID := generationTestEpisodeID()
	artifactID := domain.ArtifactID("art_" + strings.Repeat("T", 26))
	artifact := timedGenerationArtifact(artifactID, projectID, domain.MediaKindAudio, "audio/mpeg", 128, 15_000)
	deletedAt := time.Unix(1_800_000_000, 0).UTC()
	media := domain.EpisodeReferenceMedia{EpisodeID: episodeID, Artifact: artifact, TrimStartMilliseconds: 3_000, TrimEndMilliseconds: 11_000, DeletedAt: &deletedAt}
	selected := true // The same deduplicated source is selected by an active Slot.
	store := &generationLibraryTrimStore{fakeStore: &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{artifactID: artifact}, artifactSelected: &selected}, library: map[domain.EpisodeID]map[domain.ArtifactID]domain.EpisodeReferenceMedia{episodeID: {artifactID: media}}}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceAudio, MaximumCount: 1}}}
	for _, latest := range []bool{true, false} {
		_, err := normalizeGenerationReferencesWithSelection(context.Background(), store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceAudio, ArtifactID: artifactID, TrimStartMilliseconds: 3_000, TrimEndMilliseconds: 11_000}}, latest)
		if err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("latest=%v revived deleted upload through an active Slot: %v", latest, err)
		}
	}
	delete(store.library[episodeID], artifactID)
	continuity, err := normalizeGenerationReferences(context.Background(), store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceAudio, ArtifactID: artifactID, TrimStartMilliseconds: 12_000, TrimEndMilliseconds: 15_000}})
	if err != nil || continuity[0].TrimStartMilliseconds != 12_000 || continuity[0].TrimEndMilliseconds != 15_000 {
		t.Fatalf("genuinely non-library continuity range = %+v, %v", continuity, err)
	}
}

func TestNormalizeGenerationReferencesUsesLatestEpisodeLibrarySelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID := domain.ProjectID("prj_" + strings.Repeat("L", 26))
	episodeID := generationTestEpisodeID()
	otherEpisodeID := domain.EpisodeID("ep_" + strings.Repeat("O", 26))
	for _, kind := range []domain.MediaKind{domain.MediaKindAudio, domain.MediaKindVideo} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			artifactID := domain.ArtifactID("art_" + strings.Repeat("T", 26))
			artifact := timedGenerationArtifact(artifactID, projectID, kind, "application/octet-stream", 128, 30_000)
			role := domain.GenerationReferenceAudio
			if kind == domain.MediaKindVideo {
				role = domain.GenerationReferenceVideo
			}
			current := domain.EpisodeReferenceMedia{EpisodeID: episodeID, Artifact: artifact, TrimStartMilliseconds: 4_000, TrimEndMilliseconds: 12_000}
			other := current
			other.EpisodeID, other.TrimStartMilliseconds, other.TrimEndMilliseconds = otherEpisodeID, 20_000, 29_000
			store := &generationLibraryTrimStore{fakeStore: &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{artifactID: artifact}}, library: map[domain.EpisodeID]map[domain.ArtifactID]domain.EpisodeReferenceMedia{
				episodeID: {artifactID: current}, otherEpisodeID: {artifactID: other},
			}}
			capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: role, MaximumCount: 3, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 20_000}}, MaximumTotalTimedReferenceDurationMilliseconds: 20_000}
			input := []domain.GenerationArtifactReference{{Role: role, ArtifactID: artifactID, TrimEndMilliseconds: 30_000}}
			before := append([]domain.GenerationArtifactReference(nil), input...)
			first, err := normalizeGenerationReferences(ctx, store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, input)
			if err != nil || first[0].TrimStartMilliseconds != 4_000 || first[0].TrimEndMilliseconds != 12_000 || !first[0].PreparationRequired {
				t.Fatalf("stale full-file request did not use the library crop: %+v, %v", first, err)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("normalization mutated the caller's request slice")
			}
			// Any later Clip submission sees the latest Episode selection, not
			// an old per-Clip range. The already frozen snapshot remains unchanged.
			current.TrimStartMilliseconds, current.TrimEndMilliseconds = 6_000, 11_000
			store.library[episodeID][artifactID] = current
			second, err := normalizeGenerationReferences(ctx, store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: role, ArtifactID: artifactID, TrimStartMilliseconds: 4_000, TrimEndMilliseconds: 12_000}})
			if err != nil || second[0].TrimStartMilliseconds != 6_000 || second[0].TrimEndMilliseconds != 11_000 {
				t.Fatalf("next submission ignored the latest crop: %+v, %v", second, err)
			}
			if first[0].TrimStartMilliseconds != 4_000 || first[0].TrimEndMilliseconds != 12_000 {
				t.Fatal("changing the library changed the first frozen selection")
			}
			otherResult, err := normalizeGenerationReferences(ctx, store, projectID, otherEpisodeID, 720, 1280, domain.ClipGenerationReference, capability, input)
			if err != nil || otherResult[0].TrimStartMilliseconds != 20_000 || otherResult[0].TrimEndMilliseconds != 29_000 {
				t.Fatalf("another Episode did not keep its independent crop: %+v, %v", otherResult, err)
			}
			// Restore full source (legacy 0/0); an old request crop cannot hide
			// the now-over-limit source. A larger-capacity model sees full range.
			current.TrimStartMilliseconds, current.TrimEndMilliseconds = 0, 0
			store.library[episodeID][artifactID] = current
			_, err = normalizeGenerationReferences(ctx, store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, second)
			var issue *GenerationCapabilityIssue
			if !errors.As(err, &issue) || issue.Constraint != "reference_duration" || issue.Actual != 30_000 {
				t.Fatalf("restore full source did not validate effective duration: %+v, %v", issue, err)
			}
			capability.ReferenceCapabilities[0].MaximumDurationMilliseconds = 40_000
			capability.ReferenceCapabilities[0].MaximumTotalDurationMilliseconds = 40_000
			capability.MaximumTotalTimedReferenceDurationMilliseconds = 40_000
			full, err := normalizeGenerationReferences(ctx, store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, second)
			if err != nil || full[0].TrimStartMilliseconds != 0 || full[0].TrimEndMilliseconds != 30_000 || full[0].PreparationRequired {
				t.Fatalf("restore source range = %+v, %v", full, err)
			}
		})
	}
}

func TestNormalizeGenerationReferencesValidatesEffectiveLibraryTotals(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("L", 26))
	episodeID := generationTestEpisodeID()
	store := &generationLibraryTrimStore{fakeStore: &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{}}, library: map[domain.EpisodeID]map[domain.ArtifactID]domain.EpisodeReferenceMedia{episodeID: {}}}
	references := []domain.GenerationArtifactReference{}
	for _, letter := range []string{"A", "B"} {
		id := domain.ArtifactID("art_" + strings.Repeat(letter, 26))
		artifact := timedGenerationArtifact(id, projectID, domain.MediaKindAudio, "audio/mpeg", 128, 30_000)
		store.artifacts[id] = artifact
		store.library[episodeID][id] = domain.EpisodeReferenceMedia{EpisodeID: episodeID, Artifact: artifact, TrimStartMilliseconds: 2_000, TrimEndMilliseconds: 10_000}
		references = append(references, domain.GenerationArtifactReference{Role: domain.GenerationReferenceAudio, ArtifactID: id})
	}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceAudio, MaximumCount: 2, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000}}}
	_, err := normalizeGenerationReferences(context.Background(), store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, references)
	var issue *GenerationCapabilityIssue
	if !errors.As(err, &issue) || issue.Constraint != "reference_total_duration" || issue.Actual != 16_000 {
		t.Fatalf("effective audio total issue = %+v, %v", issue, err)
	}
	capability.ReferenceCapabilities[0].MaximumTotalDurationMilliseconds = 20_000
	capability.MaximumTotalTimedReferenceDurationMilliseconds = 15_000
	_, err = normalizeGenerationReferences(context.Background(), store, projectID, episodeID, 720, 1280, domain.ClipGenerationReference, capability, references)
	if !errors.As(err, &issue) || issue.Constraint != "total_timed_reference_duration" || issue.Actual != 16_000 {
		t.Fatalf("combined effective duration issue = %+v, %v", issue, err)
	}
}

func TestNormalizeGenerationReferencesDoesNotIgnoreLibraryReadFailures(t *testing.T) {
	t.Parallel()
	projectID := domain.ProjectID("prj_" + strings.Repeat("L", 26))
	artifactID := domain.ArtifactID("art_" + strings.Repeat("T", 26))
	artifact := timedGenerationArtifact(artifactID, projectID, domain.MediaKindAudio, "audio/mpeg", 128, 15_000)
	readErr := errors.New("library read failed")
	store := &generationLibraryTrimStore{fakeStore: &fakeStore{artifacts: map[domain.ArtifactID]domain.Artifact{artifactID: artifact}}, readErr: readErr}
	capability := GenerationCapability{ReferenceCapabilities: []GenerationReferenceCapability{{Role: domain.GenerationReferenceAudio, MaximumCount: 1}}}
	if _, err := normalizeGenerationReferences(context.Background(), store, projectID, generationTestEpisodeID(), 720, 1280, domain.ClipGenerationReference, capability, []domain.GenerationArtifactReference{{Role: domain.GenerationReferenceAudio, ArtifactID: artifactID}}); !errors.Is(err, readErr) {
		t.Fatalf("failed library lookup silently used the original file: %v", err)
	}
}
