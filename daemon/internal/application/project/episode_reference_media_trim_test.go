package projectapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestUpdateEpisodeReferenceMediaTrimPreservesOmittedAndResetsExplicitZero(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, store, library := episodeTrimServiceFixture(t)
	base := UpdateEpisodeReferenceMediaInput{ProjectID: store.project.ID, EpisodeID: store.episode.ID, ArtifactID: store.current.Artifact.ID, Name: store.current.Name, Description: "修改说明", ExpectedRevision: 1}
	value, err := service.UpdateEpisodeReferenceMedia(ctx, base)
	if err != nil || value.TrimStartMilliseconds != 3000 || value.TrimEndMilliseconds != 11000 || value.Revision != 2 {
		t.Fatalf("omitted bounds must preserve trim: %#v, %v", value, err)
	}
	zero := int64(0)
	base.ExpectedRevision = 2
	base.TrimStartMilliseconds, base.TrimEndMilliseconds = &zero, &zero
	value, err = service.UpdateEpisodeReferenceMedia(ctx, base)
	if err != nil || value.TrimStartMilliseconds != 0 || value.TrimEndMilliseconds != 0 || value.EffectiveDurationMilliseconds() != 15000 || value.Revision != 3 {
		t.Fatalf("explicit 0/0 must reset full source: %#v, %v", value, err)
	}
	start, end := int64(1250), int64(11250)
	base.ExpectedRevision = 3
	base.TrimStartMilliseconds, base.TrimEndMilliseconds = &start, &end
	value, err = service.UpdateEpisodeReferenceMedia(ctx, base)
	if err != nil || value.TrimStartMilliseconds != start || value.TrimEndMilliseconds != end || value.EffectiveDurationMilliseconds() != 10000 || value.Revision != 4 {
		t.Fatalf("explicit bounds not saved: %#v, %v", value, err)
	}
	if store.updateCalls != 3 || len(library.records) != 3 || value.Artifact != store.original {
		t.Fatalf("writes=%d syncs=%d original changed=%v", store.updateCalls, len(library.records), value.Artifact != store.original)
	}
	if _, err := service.UpdateEpisodeReferenceMedia(ctx, base); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update must conflict: %v", err)
	}
	if store.current.Revision != 4 || len(library.records) != 3 {
		t.Fatal("stale update changed data or synced the library")
	}
}

func TestUpdateEpisodeReferenceMediaTrimRejectsInvalidAndUnpairedBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		start, end *int64
		image      bool
	}{
		{name: "missing end", start: trimMilliseconds(3000)},
		{name: "missing start", end: trimMilliseconds(11000)},
		{name: "negative", start: trimMilliseconds(-1), end: trimMilliseconds(11000)},
		{name: "empty", start: trimMilliseconds(3000), end: trimMilliseconds(3000)},
		{name: "reversed", start: trimMilliseconds(11000), end: trimMilliseconds(3000)},
		{name: "outside original", start: trimMilliseconds(3000), end: trimMilliseconds(15001)},
		{name: "image", start: trimMilliseconds(0), end: trimMilliseconds(1), image: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, library := episodeTrimServiceFixture(t)
			if test.image {
				store.current.Artifact.Kind = domain.MediaKindImage
				store.current.Artifact.DurationMilliseconds = 0
				store.current.TrimStartMilliseconds, store.current.TrimEndMilliseconds = 0, 0
			}
			before := store.current
			_, err := service.UpdateEpisodeReferenceMedia(context.Background(), UpdateEpisodeReferenceMediaInput{ProjectID: store.project.ID, EpisodeID: store.episode.ID, ArtifactID: store.current.Artifact.ID, Name: store.current.Name, TrimStartMilliseconds: test.start, TrimEndMilliseconds: test.end, ExpectedRevision: 1})
			if !errors.Is(err, ErrInvalidRequest) || store.updateCalls != 0 || store.current != before || len(library.records) != 0 {
				t.Fatalf("invalid trim wrote data: err=%v writes=%d value=%#v", err, store.updateCalls, store.current)
			}
		})
	}
}

type episodeTrimStore struct {
	*fakeStore
	current     domain.EpisodeReferenceMedia
	original    domain.Artifact
	updateCalls int
}

func (s *episodeTrimStore) EpisodeReferenceMedia(_ context.Context, episodeID domain.EpisodeID, artifactID domain.ArtifactID, _ bool) (domain.EpisodeReferenceMedia, error) {
	if s.current.EpisodeID != episodeID || s.current.Artifact.ID != artifactID {
		return domain.EpisodeReferenceMedia{}, ErrArtifactNotFound
	}
	return s.current, nil
}

func (s *episodeTrimStore) UpdateEpisodeReferenceMedia(_ context.Context, value domain.EpisodeReferenceMedia, expected int64, at time.Time) error {
	if s.current.Revision != expected {
		return ErrRevisionConflict
	}
	s.updateCalls++
	value.Revision = expected + 1
	value.UpdatedAt = at
	s.current = value
	return nil
}

func episodeTrimServiceFixture(t *testing.T) (*Service, *episodeTrimStore, *fakeLibrary) {
	t.Helper()
	project, episode := testProjectResult(t)
	artifact := domain.Artifact{ID: domain.ArtifactID("art_" + strings.Repeat("T", 26)), ProjectID: project.ID, Kind: domain.MediaKindAudio, RelativePath: "media/source.wav", SHA256: strings.Repeat("a", 64), ByteSize: 4096, MIMEType: "audio/wav", OriginalFilename: "source.wav", Width: 720, Height: 1280, DurationMilliseconds: 15000, AudioSampleRate: 48000, AudioChannels: 2, CreatedAt: project.CreatedAt}
	store := &episodeTrimStore{fakeStore: &fakeStore{project: project, episode: episode, episodes: []domain.EpisodeSummary{episode.Summary()}}, original: artifact, current: domain.EpisodeReferenceMedia{EpisodeID: episode.ID, Artifact: artifact, Name: "参考音频", TrimStartMilliseconds: 3000, TrimEndMilliseconds: 11000, Revision: 1, CreatedAt: project.CreatedAt, UpdatedAt: project.CreatedAt}}
	registry := NewProjectStoreRegistry()
	if _, _, err := registry.Register(ProjectHandle{ProjectID: project.ID, RootPath: t.TempDir(), Store: store}); err != nil {
		t.Fatal(err)
	}
	library := &fakeLibrary{}
	return &Service{registry: registry, clock: fakeClock{value: project.CreatedAt.Add(time.Second)}, library: library}, store, library
}

func trimMilliseconds(value int64) *int64 { return &value }
