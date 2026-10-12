package projectapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestEpisodeReferenceMediaDefaultNameIsSafeAndUnique(t *testing.T) {
	t.Parallel()
	episodeID, _ := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifactID, _ := domain.ParseArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	store := &fakeStore{
		episodeAssets: []domain.EpisodeAsset{{
			Identity: domain.AssetIdentity{Name: "镜头 参考"},
			Asset:    domain.Asset{EpisodeID: episodeID},
		}},
	}
	service := &Service{}
	name, err := service.uniqueEpisodeReferenceMediaName(
		context.Background(),
		store,
		episodeID,
		"  镜头@\n参考.png  ",
		artifactID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if name != "镜头 参考-2" {
		t.Fatalf("default name = %q, want %q", name, "镜头 参考-2")
	}
	if strings.ContainsAny(name, "@\r\n\x00") || len([]rune(name)) > 40 {
		t.Fatalf("unsafe default name %q", name)
	}
}

func TestEpisodeReferenceMediaRenameRejectsGeneratedAndUploadedNameConflicts(t *testing.T) {
	t.Parallel()
	episodeID, _ := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifactID, _ := domain.ParseArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	otherArtifactID, _ := domain.ParseArtifactID("art_BCDEFGHIJKLMNOPQRSTUVWXYZA")
	store := &fakeStore{
		episodeAssets: []domain.EpisodeAsset{{
			Identity: domain.AssetIdentity{Name: "林黛玉"},
			Asset:    domain.Asset{EpisodeID: episodeID},
		}},
		episodeReferenceMedia: []domain.EpisodeReferenceMedia{{
			Artifact: domain.Artifact{ID: otherArtifactID},
			Name:     "动作参考",
		}},
	}
	for _, name := range []string{"林黛玉", "动作参考"} {
		err := episodeReferenceMediaNameAvailable(context.Background(), store, episodeID, artifactID, name)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("name %q error = %v, want ErrInvalidRequest", name, err)
		}
	}
}
