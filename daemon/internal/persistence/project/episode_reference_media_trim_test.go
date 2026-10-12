package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestEpisodeReferenceMediaTrimPersistsAndIsEpisodeScoped(t *testing.T) {
	t.Parallel()
	for _, kind := range []domain.MediaKind{domain.MediaKindAudio, domain.MediaKindVideo} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "project.db")
			store := newTestStore(t, path)
			defer func() { _ = store.Close() }()
			project, episode := testModels(t)
			if err := store.Initialize(ctx, project, episode); err != nil {
				t.Fatal(err)
			}
			other := testSecondEpisode(t, project, episode)
			if err := store.CreateEpisode(ctx, other, other.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			at := other.UpdatedAt.Add(time.Second)
			artifact := episodeTrimTestArtifact(project.ID, kind, at)
			for _, ep := range []domain.EpisodeID{episode.ID, other.ID} {
				if err := store.PutEpisodeReferenceMedia(ctx, domain.EpisodeReferenceMedia{EpisodeID: ep, Artifact: artifact, Name: "原始素材", Revision: 1, CreatedAt: at, UpdatedAt: at}, at); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
			if err != nil || current.EffectiveDurationMilliseconds() != 15000 || current.TrimEndMilliseconds != 0 {
				t.Fatalf("full-source item = %#v, %v", current, err)
			}
			current.TrimStartMilliseconds, current.TrimEndMilliseconds = 3000, 11000
			current.UpdatedAt = at.Add(time.Second)
			if err := store.UpdateEpisodeReferenceMedia(ctx, current, 1, current.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateEpisodeReferenceMedia(ctx, current, 1, current.UpdatedAt); !errors.Is(err, ErrEpisodeReferenceMediaRevisionConflict) {
				t.Fatalf("stale trim update = %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, path, sharedsqlite.OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			current, err = store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
			if err != nil || current.TrimStartMilliseconds != 3000 || current.TrimEndMilliseconds != 11000 || current.EffectiveDurationMilliseconds() != 8000 || current.Revision != 2 {
				t.Fatalf("reopened item = %#v, %v", current, err)
			}
			unchanged, err := store.EpisodeReferenceMedia(ctx, other.ID, artifact.ID, false)
			if err != nil || unchanged.Revision != 1 || unchanged.TrimStartMilliseconds != 0 || unchanged.TrimEndMilliseconds != 0 || unchanged.EffectiveDurationMilliseconds() != 15000 {
				t.Fatalf("other Episode changed = %#v, %v", unchanged, err)
			}
			original, err := store.Artifact(ctx, artifact.ID)
			if err != nil || original != artifact {
				t.Fatalf("original Artifact changed = %#v, %v", original, err)
			}
			for _, bounds := range [][2]int64{{-1, 3000}, {11000, 3000}, {3000, 3000}, {3000, 0}, {3000, 15001}} {
				invalid := current
				invalid.TrimStartMilliseconds, invalid.TrimEndMilliseconds = bounds[0], bounds[1]
				if err := store.UpdateEpisodeReferenceMedia(ctx, invalid, current.Revision, current.UpdatedAt); !errors.Is(err, domain.ErrInvalidEpisodeReferenceMedia) {
					t.Fatalf("invalid trim %v accepted: %v", bounds, err)
				}
			}
			forged := current
			forged.Artifact.DurationMilliseconds = 30000
			forged.TrimEndMilliseconds = 20000
			if err := store.UpdateEpisodeReferenceMedia(ctx, forged, current.Revision, current.UpdatedAt); !errors.Is(err, domain.ErrInvalidEpisodeReferenceMedia) {
				t.Fatalf("caller-supplied duration bypassed original bounds: %v", err)
			}
			current.TrimStartMilliseconds, current.TrimEndMilliseconds = 0, 0
			current.UpdatedAt = current.UpdatedAt.Add(time.Second)
			if err := store.UpdateEpisodeReferenceMedia(ctx, current, 2, current.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			reset, err := store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
			if err != nil || reset.Revision != 3 || reset.EffectiveDurationMilliseconds() != 15000 || reset.Artifact != artifact {
				t.Fatalf("reset item = %#v, %v", reset, err)
			}
		})
	}
}

func TestEpisodeReferenceMediaTrimExplicitInsertAndImageRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode := initializedStoryboardStore(t)
	defer store.Close()
	at := episode.UpdatedAt.Add(time.Second)
	artifact := episodeTrimTestArtifact(project.ID, domain.MediaKindVideo, at)
	value := domain.EpisodeReferenceMedia{EpisodeID: episode.ID, Artifact: artifact, Name: "已有选区", TrimStartMilliseconds: 1250, TrimEndMilliseconds: 11250, Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := store.PutEpisodeReferenceMedia(ctx, value, at); err != nil {
		t.Fatal(err)
	}
	saved, err := store.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
	if err != nil || saved.TrimStartMilliseconds != 1250 || saved.TrimEndMilliseconds != 11250 || saved.EffectiveDurationMilliseconds() != 10000 {
		t.Fatalf("explicit insert lost range: %#v, %v", saved, err)
	}
	image := artifact
	image.ID = domain.ArtifactID("art_" + strings.Repeat("U", 26))
	image.RelativePath = "media/image.png"
	image.Kind, image.DurationMilliseconds, image.SHA256 = domain.MediaKindImage, 0, strings.Repeat("b", 64)
	value.Artifact, value.Name = image, "图片"
	if err := store.PutEpisodeReferenceMedia(ctx, value, at); !errors.Is(err, domain.ErrInvalidEpisodeReferenceMedia) {
		t.Fatalf("image accepted range: %v", err)
	}
	value.TrimStartMilliseconds, value.TrimEndMilliseconds = 0, 0
	if err := store.PutEpisodeReferenceMedia(ctx, value, at); err != nil {
		t.Fatal(err)
	}
	value.TrimEndMilliseconds = 1
	if err := store.UpdateEpisodeReferenceMedia(ctx, value, 1, at); !errors.Is(err, domain.ErrInvalidEpisodeReferenceMedia) {
		t.Fatalf("image update accepted range: %v", err)
	}
}

func TestEpisodeReferenceMediaTrimMigrationPreservesLegacyFullSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:25]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{database: database}
	project, episode := testModels(t)
	if err := legacy.Initialize(ctx, project, episode); err != nil {
		t.Fatal(err)
	}
	artifact := episodeTrimTestArtifact(project.ID, domain.MediaKindAudio, episode.UpdatedAt.Add(time.Second))
	if err := legacy.withTransaction(ctx, "legacy fixture", func(tx *sql.Tx) error {
		if err := insertArtifact(ctx, tx, artifact); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO episode_reference_media (episode_id,artifact_id,project_id,name,description,revision,created_at_ms,updated_at_ms) VALUES (?,?,?,'旧音频','保留描述',4,?,?)`, episode.ID.String(), artifact.ID.String(), project.ID.String(), artifact.CreatedAt.UnixMilli(), artifact.CreatedAt.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	value, err := upgraded.EpisodeReferenceMedia(ctx, episode.ID, artifact.ID, false)
	if err != nil || value.TrimStartMilliseconds != 0 || value.TrimEndMilliseconds != 0 || value.EffectiveDurationMilliseconds() != 15000 || value.Name != "旧音频" || value.Description != "保留描述" || value.Revision != 4 || value.Artifact != artifact {
		t.Fatalf("legacy migration changed data: %#v, %v", value, err)
	}
}

func episodeTrimTestArtifact(projectID domain.ProjectID, kind domain.MediaKind, at time.Time) domain.Artifact {
	return domain.Artifact{ID: domain.ArtifactID("art_" + strings.Repeat("T", 26)), ProjectID: projectID, Kind: kind, RelativePath: "media/source.dat", SHA256: strings.Repeat("a", 64), ByteSize: 4096, MIMEType: string(kind) + "/fixture", OriginalFilename: "source.dat", Width: 720, Height: 1280, DurationMilliseconds: 15000, FPSNumerator: 24, FPSDenominator: 1, AudioSampleRate: 48000, AudioChannels: 2, CreatedAt: at}
}
