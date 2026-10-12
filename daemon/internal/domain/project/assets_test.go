package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEpisodeAssetValidationAndPromptNormalization(t *testing.T) {
	t.Parallel()

	identity, asset := validAssetModels(t)
	asset.Prompt = NormalizeAssetPrompt("雨夜\r\n霓虹\r特写")
	if asset.Prompt != "雨夜\n霓虹\n特写" {
		t.Fatalf("NormalizeAssetPrompt() = %q", asset.Prompt)
	}
	view := EpisodeAsset{Identity: identity, Asset: asset}
	if err := view.Validate(); err != nil {
		t.Fatalf("EpisodeAsset.Validate() error = %v", err)
	}
	if asset.IsDeleted() {
		t.Fatal("new Asset reports deleted")
	}
}

func TestNormalizeAssetPromptTruncatesByUnicodeCharacter(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("镜", MaxEditableAssetPromptCharacters-1) + "🎬尾部"
	normalized := NormalizeAssetPrompt(value)
	if got := []rune(normalized); len(got) != MaxEditableAssetPromptCharacters || got[len(got)-1] != '🎬' {
		t.Fatalf("NormalizeAssetPrompt() rune count/ending = %d/%q", len(got), got[len(got)-1])
	}
}

func TestAssetValidationRejectsInvalidKindNamePromptAndOwnership(t *testing.T) {
	t.Parallel()

	identity, asset := validAssetModels(t)
	identity.Kind = "unknown"
	if err := identity.Validate(); !errors.Is(err, ErrInvalidAssetKind) {
		t.Fatalf("invalid kind error = %v, want ErrInvalidAssetKind", err)
	}
	identity, asset = validAssetModels(t)
	identity.Name = strings.Repeat("角", MaxAssetIdentityNameCharacters+1)
	if err := identity.Validate(); !errors.Is(err, ErrInvalidAssetIdentity) {
		t.Fatalf("long name error = %v, want ErrInvalidAssetIdentity", err)
	}
	asset.Prompt = strings.Repeat("画", MaxAssetPromptCharacters+1)
	if err := asset.Validate(); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("long prompt error = %v, want ErrInvalidAsset", err)
	}
	identity, asset = validAssetModels(t)
	asset.IdentityID = "asi_77777777777777777777777777"
	if err := (EpisodeAsset{Identity: identity, Asset: asset}).Validate(); !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("mismatched identity error = %v, want ErrInvalidAsset", err)
	}
}

func TestAssetValidationAcceptsDeletedAndHistoricalProvenance(t *testing.T) {
	t.Parallel()

	_, asset := validAssetModels(t)
	source, err := ParseAssetID("ast_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	asset.DerivedFromAssetID = source
	deletedAt := asset.CreatedAt.Add(time.Second)
	asset.DeletedAt = &deletedAt
	asset.UpdatedAt = deletedAt
	asset.Revision = 2
	if err := asset.Validate(); err != nil {
		t.Fatalf("deleted derived Asset validation error = %v", err)
	}
	if !asset.IsDeleted() {
		t.Fatal("deleted Asset reports active")
	}
}

func validAssetModels(t *testing.T) (AssetIdentity, Asset) {
	t.Helper()
	projectID, err := ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	identityID, err := ParseAssetIdentityID("asi_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	assetID, err := ParseAssetID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 3, 10, 0, 0, 123_000_000, time.UTC)
	identity := AssetIdentity{
		ID: identityID, ProjectID: projectID, Kind: AssetKindCharacter, Name: "林晚",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	asset := Asset{
		ID: assetID, ProjectID: projectID, EpisodeID: episodeID, IdentityID: identityID,
		Prompt: "黑色风衣", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	return identity, asset
}
