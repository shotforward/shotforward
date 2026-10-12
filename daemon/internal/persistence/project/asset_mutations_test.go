package projectstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestAssetLifecyclePersistsAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	store, project, first, second := initializedAssetStore(t, path)
	identity, source := assetModels(t, project.ID, first.ID, "A", "A", domain.AssetKindCharacter, "林晚", "黑色风衣", first.UpdatedAt.Add(time.Second))
	if err := store.CreateAsset(ctx, &identity, source, source.UpdatedAt); err != nil {
		t.Fatalf("CreateAsset(new identity) error = %v", err)
	}

	derivedID := mustAssetID(t, "B")
	derivedAt := source.UpdatedAt.Add(time.Second)
	derived := domain.Asset{
		ID: derivedID, ProjectID: project.ID, EpisodeID: second.ID, IdentityID: identity.ID,
		DerivedFromAssetID: source.ID, Prompt: source.Prompt, Revision: 1,
		CreatedAt: derivedAt, UpdatedAt: derivedAt,
	}
	if err := store.CreateAsset(ctx, nil, derived, derivedAt); err != nil {
		t.Fatalf("CreateAsset(derived) error = %v", err)
	}

	updateAt := derivedAt.Add(time.Second)
	derived.Prompt = "灰色大衣"
	derived.Revision++
	derived.UpdatedAt = updateAt
	if err := store.UpdateAsset(ctx, derived, 1, updateAt); err != nil {
		t.Fatalf("UpdateAsset(prompt) error = %v", err)
	}
	stale := derived
	stale.Prompt = "不应写入"
	stale.UpdatedAt = updateAt.Add(500 * time.Millisecond)
	if err := store.UpdateAsset(ctx, stale, 1, stale.UpdatedAt); !errors.Is(err, ErrAssetRevisionConflict) {
		t.Fatalf("UpdateAsset(stale) error = %v, want ErrAssetRevisionConflict", err)
	}
	unchanged, err := store.Asset(ctx, derived.ID)
	if err != nil || unchanged.Asset.Prompt != derived.Prompt || unchanged.Asset.Revision != derived.Revision {
		t.Fatalf("Asset after stale update = %#v, %v", unchanged, err)
	}

	renameAt := updateAt.Add(time.Second)
	identity.Name = "林晚（统一身份）"
	identity.Revision++
	identity.UpdatedAt = renameAt
	if err := store.UpdateAssetIdentity(ctx, identity, 1, renameAt); err != nil {
		t.Fatalf("UpdateAssetIdentity() error = %v", err)
	}

	deleteSourceAt := renameAt.Add(time.Second)
	deletedSource := source
	deletedSource.Revision++
	deletedSource.UpdatedAt = deleteSourceAt
	deletedSource.DeletedAt = &deleteSourceAt
	if err := store.UpdateAsset(ctx, deletedSource, 1, deleteSourceAt); err != nil {
		t.Fatalf("UpdateAsset(delete source) error = %v", err)
	}

	// Historical provenance remains valid after its source is deleted.
	afterSourceDelete := deleteSourceAt.Add(time.Second)
	derived.Prompt = "白色大衣"
	derived.Revision++
	derived.UpdatedAt = afterSourceDelete
	if err := store.UpdateAsset(ctx, derived, 2, afterSourceDelete); err != nil {
		t.Fatalf("UpdateAsset(after source deletion) error = %v", err)
	}

	deleteAt := afterSourceDelete.Add(time.Second)
	derived.Revision++
	derived.UpdatedAt = deleteAt
	derived.DeletedAt = &deleteAt
	if err := store.UpdateAsset(ctx, derived, 3, deleteAt); err != nil {
		t.Fatalf("UpdateAsset(delete) error = %v", err)
	}
	active, err := store.EpisodeAssets(ctx, second.ID, false)
	if err != nil || len(active) != 0 {
		t.Fatalf("active EpisodeAssets() = %#v, %v", active, err)
	}
	all, err := store.EpisodeAssets(ctx, second.ID, true)
	if err != nil || len(all) != 1 || !all[0].Asset.IsDeleted() {
		t.Fatalf("all EpisodeAssets() = %#v, %v", all, err)
	}

	restoreAt := deleteAt.Add(time.Second)
	derived.Revision++
	derived.UpdatedAt = restoreAt
	derived.DeletedAt = nil
	if err := store.UpdateAsset(ctx, derived, 4, restoreAt); err != nil {
		t.Fatalf("UpdateAsset(restore) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Asset(ctx, derived.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity.Name != identity.Name || got.Asset.Prompt != derived.Prompt || got.Asset.IsDeleted() || got.Asset.Revision != 5 {
		t.Fatalf("reopened Asset = %#v", got)
	}
}

func TestAssetConstraintsRejectDuplicatesAndInvalidProvenance(t *testing.T) {
	t.Parallel()

	store, project, first, second := initializedAssetStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	identity, source := assetModels(t, project.ID, first.ID, "C", "C", domain.AssetKindLocation, "雨夜街道", "湿润路面", first.UpdatedAt.Add(time.Second))
	if err := store.CreateAsset(context.Background(), &identity, source, source.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	duplicateIdentity, duplicateAsset := assetModels(t, project.ID, second.ID, "D", "D", domain.AssetKindLocation, "雨夜街道", "另一个", source.UpdatedAt.Add(time.Second))
	if err := store.CreateAsset(context.Background(), &duplicateIdentity, duplicateAsset, duplicateAsset.UpdatedAt); !errors.Is(err, ErrAssetIdentityNameConflict) {
		t.Fatalf("duplicate Identity error = %v, want ErrAssetIdentityNameConflict", err)
	}

	derivedAt := source.UpdatedAt.Add(2 * time.Second)
	derived := domain.Asset{
		ID: mustAssetID(t, "E"), ProjectID: project.ID, EpisodeID: second.ID,
		IdentityID: identity.ID, DerivedFromAssetID: source.ID, Prompt: source.Prompt,
		Revision: 1, CreatedAt: derivedAt, UpdatedAt: derivedAt,
	}
	if err := store.CreateAsset(context.Background(), nil, derived, derivedAt); err != nil {
		t.Fatal(err)
	}
	duplicate := derived
	duplicate.ID = mustAssetID(t, "F")
	duplicate.CreatedAt = derivedAt.Add(time.Second)
	duplicate.UpdatedAt = duplicate.CreatedAt
	if err := store.CreateAsset(context.Background(), nil, duplicate, duplicate.UpdatedAt); !errors.Is(err, ErrEpisodeAssetIdentityConflict) {
		t.Fatalf("duplicate Episode identity error = %v, want ErrEpisodeAssetIdentityConflict", err)
	}

	backward := domain.Asset{
		ID: mustAssetID(t, "G"), ProjectID: project.ID, EpisodeID: first.ID,
		IdentityID: identity.ID, DerivedFromAssetID: derived.ID, Prompt: "",
		Revision: 1, CreatedAt: duplicate.UpdatedAt.Add(time.Second), UpdatedAt: duplicate.UpdatedAt.Add(time.Second),
	}
	if err := store.CreateAsset(context.Background(), nil, backward, backward.UpdatedAt); !errors.Is(err, ErrInvalidAssetProvenance) {
		t.Fatalf("backward provenance error = %v, want ErrInvalidAssetProvenance", err)
	}
}

func TestDeletedAssetDoesNotReserveItsIdentityName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, project, episode, _ := initializedAssetStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()

	identity, original := assetModels(t, project.ID, episode.ID, "M", "M", domain.AssetKindCharacter, "主角", "旧提示词", episode.UpdatedAt.Add(time.Second))
	if err := store.CreateAsset(ctx, &identity, original, original.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	deletedAt := original.UpdatedAt.Add(time.Second)
	deleted := original
	deleted.Revision++
	deleted.UpdatedAt = deletedAt
	deleted.DeletedAt = &deletedAt
	if err := store.DeleteAsset(ctx, deleted, original.Revision, false, deletedAt); err != nil {
		t.Fatal(err)
	}

	replacementIdentity, replacement := assetModels(t, project.ID, episode.ID, "N", "N", domain.AssetKindCharacter, "主角", "新提示词", deletedAt.Add(time.Second))
	if err := store.CreateAsset(ctx, &replacementIdentity, replacement, replacement.UpdatedAt); err != nil {
		t.Fatalf("CreateAsset() after same-name deletion = %v", err)
	}
	created, err := store.Asset(ctx, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Identity.ID != identity.ID || created.Asset.IdentityID != identity.ID || created.Asset.Prompt != "新提示词" {
		t.Fatalf("replacement Asset = %#v, want dormant Identity %q", created, identity.ID)
	}
	active, err := store.EpisodeAssets(ctx, episode.ID, false)
	if err != nil || len(active) != 1 || active[0].Asset.ID != replacement.ID {
		t.Fatalf("active EpisodeAssets() = %#v, %v", active, err)
	}
	all, err := store.EpisodeAssets(ctx, episode.ID, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all EpisodeAssets() = %#v, %v", all, err)
	}
}

func TestRelinkDetachAndRestoreConflictAreAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, project, first, second := initializedAssetStore(t, filepath.Join(t.TempDir(), "project.db"))
	defer store.Close()
	firstIdentity, firstAsset := assetModels(t, project.ID, first.ID, "H", "H", domain.AssetKindCharacter, "甲", "甲", first.UpdatedAt.Add(time.Second))
	secondIdentity, secondAsset := assetModels(t, project.ID, first.ID, "I", "I", domain.AssetKindCharacter, "乙", "乙", firstAsset.UpdatedAt.Add(time.Second))
	if err := store.CreateAsset(ctx, &firstIdentity, firstAsset, firstAsset.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAsset(ctx, &secondIdentity, secondAsset, secondAsset.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	derivedAt := secondAsset.UpdatedAt.Add(time.Second)
	asset := domain.Asset{
		ID: mustAssetID(t, "J"), ProjectID: project.ID, EpisodeID: second.ID,
		IdentityID: firstIdentity.ID, DerivedFromAssetID: firstAsset.ID, Prompt: "本集甲",
		Revision: 1, CreatedAt: derivedAt, UpdatedAt: derivedAt,
	}
	if err := store.CreateAsset(ctx, nil, asset, derivedAt); err != nil {
		t.Fatal(err)
	}
	relinkAt := derivedAt.Add(time.Second)
	asset.IdentityID = secondIdentity.ID
	asset.Revision++
	asset.UpdatedAt = relinkAt
	if err := store.UpdateAsset(ctx, asset, 1, relinkAt); err != nil {
		t.Fatalf("UpdateAsset(relink) error = %v", err)
	}
	if asset.DerivedFromAssetID != firstAsset.ID {
		t.Fatal("test lost historical provenance")
	}

	detachedIdentityID := mustIdentityID(t, "K")
	detachAt := relinkAt.Add(time.Second)
	detachedIdentity := domain.AssetIdentity{
		ID: detachedIdentityID, ProjectID: project.ID, Kind: domain.AssetKindCharacter, Name: "丙",
		Revision: 1, CreatedAt: detachAt, UpdatedAt: detachAt,
	}
	asset.IdentityID = detachedIdentityID
	asset.Revision++
	asset.UpdatedAt = detachAt
	if err := store.DetachAssetIdentity(ctx, detachedIdentity, asset, 2, detachAt); err != nil {
		t.Fatalf("DetachAssetIdentity() error = %v", err)
	}
	got, err := store.Asset(ctx, asset.ID)
	if err != nil || got.Identity.Name != "丙" || got.Asset.DerivedFromAssetID != firstAsset.ID {
		t.Fatalf("detached Asset = %#v, %v", got, err)
	}

	deleteAt := detachAt.Add(time.Second)
	asset.Revision++
	asset.UpdatedAt = deleteAt
	asset.DeletedAt = &deleteAt
	if err := store.UpdateAsset(ctx, asset, 3, deleteAt); err != nil {
		t.Fatal(err)
	}
	replacement := domain.Asset{
		ID: mustAssetID(t, "L"), ProjectID: project.ID, EpisodeID: second.ID,
		IdentityID: detachedIdentity.ID, Prompt: "replacement", Revision: 1,
		CreatedAt: deleteAt.Add(time.Second), UpdatedAt: deleteAt.Add(time.Second),
	}
	if err := store.CreateAsset(ctx, nil, replacement, replacement.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	restoreAt := replacement.UpdatedAt.Add(time.Second)
	asset.Revision++
	asset.UpdatedAt = restoreAt
	asset.DeletedAt = nil
	if err := store.UpdateAsset(ctx, asset, 4, restoreAt); !errors.Is(err, ErrEpisodeAssetIdentityConflict) {
		t.Fatalf("restore conflict error = %v, want ErrEpisodeAssetIdentityConflict", err)
	}
	unchanged, _ := store.Asset(ctx, asset.ID)
	if !unchanged.Asset.IsDeleted() || unchanged.Asset.Revision != 4 {
		t.Fatalf("conflicting restore changed Asset = %#v", unchanged)
	}
}

func initializedAssetStore(t *testing.T, path string) (*Store, domain.Project, domain.Episode, domain.Episode) {
	t.Helper()
	store := newTestStore(t, path)
	project, first := testModels(t)
	if err := store.Initialize(context.Background(), project, first); err != nil {
		t.Fatal(err)
	}
	second := testSecondEpisode(t, project, first)
	if err := store.CreateEpisode(context.Background(), second, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return store, project, first, second
}

func assetModels(
	t *testing.T,
	projectID domain.ProjectID,
	episodeID domain.EpisodeID,
	identityFill string,
	assetFill string,
	kind domain.AssetKind,
	name string,
	prompt string,
	now time.Time,
) (domain.AssetIdentity, domain.Asset) {
	t.Helper()
	identityID := mustIdentityID(t, identityFill)
	assetID := mustAssetID(t, assetFill)
	identity := domain.AssetIdentity{
		ID: identityID, ProjectID: projectID, Kind: kind, Name: name,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	asset := domain.Asset{
		ID: assetID, ProjectID: projectID, EpisodeID: episodeID, IdentityID: identityID,
		Prompt: prompt, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	return identity, asset
}

func mustAssetID(t *testing.T, fill string) domain.AssetID {
	t.Helper()
	id, err := domain.ParseAssetID("ast_" + repeatedID(fill))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustIdentityID(t *testing.T, fill string) domain.AssetIdentityID {
	t.Helper()
	id, err := domain.ParseAssetIdentityID("asi_" + repeatedID(fill))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func repeatedID(fill string) string {
	result := ""
	for len(result) < 26 {
		result += fill
	}
	return result[:26]
}
