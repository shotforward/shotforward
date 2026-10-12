package studio

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
)

func TestStudioV2MigratesToProviderProfilesV3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "studio.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Studio, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:2]); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	version, err := sharedsqlite.SchemaVersion(ctx, store.database)
	if err != nil || version != len(migrations) {
		t.Fatalf("SchemaVersion() = %d, %v", version, err)
	}
	profiles, err := store.ProviderProfiles(ctx)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("ProviderProfiles() = %#v, %v", profiles, err)
	}
}

func TestStudioV7MigratesProviderConcurrencyToTen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "studio.db")
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{Path: path, Kind: sharedsqlite.Studio, Mode: sharedsqlite.CreateNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedsqlite.Migrate(ctx, database, migrations[:7]); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO generation_provider_profiles (
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
) VALUES ('video.h3.official', 'minimax_global', 'MiniMax-H3', NULL, NULL,
          'credential-h3', 1, 5, NULL, 'unchecked', NULL, NULL, 1, 1, 1);
INSERT INTO generation_provider_models (
    generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
) VALUES ('video.h3.official', 'MiniMax-H3', 1, NULL, 'unchecked', NULL, NULL);`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, sharedsqlite.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profile, err := store.ProviderProfile(ctx, domain.GeneratorVideoH3Official)
	if err != nil || profile.ConcurrencyLimit != 5 || len(profile.Models) != 1 {
		t.Fatalf("migrated ProviderProfile() = %#v, %v", profile, err)
	}
	profile, err = store.UpdateProviderProfile(ctx, domain.GeneratorVideoH3Official, ProviderProfileUpdate{
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", CredentialRef: "credential-h3",
		Enabled: true, ConcurrencyLimit: 10, ExpectedRevision: 1, UpdatedAt: time.UnixMilli(2).UTC(),
		Models: []ProviderModel{{ModelID: "MiniMax-H3", IsDefault: true, Health: ProviderHealthUnchecked}},
	})
	if err != nil || profile.ConcurrencyLimit != 10 || profile.Revision != 2 {
		t.Fatalf("UpdateProviderProfile(10) = %#v, %v", profile, err)
	}
	rows, err := store.database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
}

func TestProviderProfilesPersistOnlyCredentialReferences(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)
	profiles := []ProviderProfile{
		{GeneratorID: domain.GeneratorVideoSeedanceChina, EndpointPreset: EndpointVolcengineChina, ModelID: "doubao-seedance-2-5-260628", CredentialRef: "credential-cn", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoSeedanceGlobal, EndpointPreset: EndpointBytePlusGlobal, ModelID: "dreamina-seedance-2-5-260628", CredentialRef: "credential-global", Enabled: true, ConcurrencyLimit: 2, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoH3China, EndpointPreset: EndpointMiniMaxChina, ModelID: "MiniMax-H3-Max", CredentialRef: "credential-h3-cn", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoH3Official, EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", CredentialRef: "credential-h3", Enabled: false, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: EndpointAlibabaSingapore, ModelID: "wan3.0-video", WorkspaceID: "workspace-global", CredentialRef: "credential-wan", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
	}
	for _, profile := range profiles {
		if err := store.CreateProviderProfile(context.Background(), profile); err != nil {
			t.Fatalf("CreateProviderProfile(%s) error = %v", profile.GeneratorID, err)
		}
	}
	listed, err := store.ProviderProfiles(context.Background())
	if err != nil || len(listed) != len(profiles) {
		t.Fatalf("ProviderProfiles() = %#v, %v", listed, err)
	}
	wan, err := store.ProviderProfile(context.Background(), domain.GeneratorVideoWanOfficial)
	if err != nil || wan.WorkspaceID != "workspace-global" || wan.CredentialRef != "credential-wan" {
		t.Fatalf("ProviderProfile(Wan) = %#v, %v", wan, err)
	}
	checkedAt := at.Add(time.Second)
	wan, err = store.RecordProviderProfileCheck(context.Background(), domain.GeneratorVideoWanOfficial, 1, "wan3-v1", ProviderHealthReady, "", checkedAt)
	if err != nil || wan.Revision != 2 || wan.Health != ProviderHealthReady || wan.CapabilityVersion != "wan3-v1" || wan.LastCheckedAt == nil || !wan.LastCheckedAt.Equal(checkedAt) {
		t.Fatalf("RecordProviderProfileCheck() = %#v, %v", wan, err)
	}
	updatedAt := at.Add(2 * time.Second)
	wan, err = store.UpdateProviderProfile(context.Background(), domain.GeneratorVideoWanOfficial, ProviderProfileUpdate{
		EndpointPreset: EndpointAlibabaTokyo, ModelID: "wan3.0-video-prime", WorkspaceID: "workspace-japan",
		CredentialRef: "credential-wan-next", Enabled: true, ConcurrencyLimit: 2, ExpectedRevision: 2, UpdatedAt: updatedAt,
		Models: []ProviderModel{{ModelID: "wan3.0-video", Health: ProviderHealthUnchecked}, {ModelID: "wan3.0-video-prime", IsDefault: true, Health: ProviderHealthUnchecked}},
	})
	if err != nil || wan.Revision != 3 || wan.EndpointPreset != EndpointAlibabaTokyo || wan.ModelID != "wan3.0-video-prime" || len(wan.Models) != 2 || !wan.Models[0].IsDefault || wan.Health != ProviderHealthUnchecked || wan.CapabilityVersion != "" || wan.LastCheckedAt != nil || !wan.CreatedAt.Equal(at) || !wan.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdateProviderProfile() = %#v, %v", wan, err)
	}
	if _, err := store.UpdateProviderProfile(context.Background(), domain.GeneratorVideoWanOfficial, ProviderProfileUpdate{
		EndpointPreset: EndpointAlibabaTokyo, ModelID: "wan3.0-video-prime", WorkspaceID: "workspace-japan",
		CredentialRef: "credential-wan-next", Enabled: true, ConcurrencyLimit: 2, ExpectedRevision: 2, UpdatedAt: updatedAt,
	}); !errors.Is(err, ErrProviderProfileRevisionConflict) {
		t.Fatalf("stale UpdateProviderProfile() error = %v", err)
	}
	credentialRef, err := store.DeleteProviderProfile(context.Background(), domain.GeneratorVideoH3Official, 1)
	if err != nil || credentialRef != "credential-h3" {
		t.Fatalf("DeleteProviderProfile() = %q, %v", credentialRef, err)
	}

	rows, err := store.database.QueryContext(context.Background(), "PRAGMA table_info(generation_provider_profiles)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "api_key") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
			t.Fatalf("Provider profile schema contains a secret-bearing column %q", name)
		}
	}
}

func TestProviderProfilesRejectRouteConfusionAndCodexCredentials(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)
	tests := []ProviderProfile{
		{GeneratorID: domain.GeneratorImageCodex, EndpointPreset: "openai", ModelID: "gpt-image", CredentialRef: "must-not-exist", Enabled: true, ConcurrencyLimit: 5, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoSeedanceChina, EndpointPreset: EndpointBytePlusGlobal, ModelID: "doubao-seedance-2-5-260628", CredentialRef: "wrong-region", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoH3China, EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3-Max", CredentialRef: "wrong-region", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
		{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: EndpointAlibabaChina, ModelID: "wan3.0-video", CredentialRef: "missing-workspace", Enabled: true, ConcurrencyLimit: 1, Health: ProviderHealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at},
	}
	for _, profile := range tests {
		if err := store.CreateProviderProfile(context.Background(), profile); err == nil {
			t.Fatalf("CreateProviderProfile(%s, %s) succeeded", profile.GeneratorID, profile.EndpointPreset)
		}
	}
}

func TestPersistedProviderModelsAcceptAllChinaSeedanceVariants(t *testing.T) {
	t.Parallel()
	for _, model := range []string{
		"doubao-seedance-2-5-260628",
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
		"doubao-seedance-2-0-mini-260615",
	} {
		if !validPersistedProviderModel(domain.GeneratorVideoSeedanceChina, model) {
			t.Fatalf("China Seedance model %q is not persistable", model)
		}
	}
}
