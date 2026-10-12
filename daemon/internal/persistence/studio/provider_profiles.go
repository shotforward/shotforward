package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrProviderProfileNotFound         = errors.New("Generation Provider profile not found")
	ErrProviderProfileConflict         = errors.New("Generation Provider profile conflicts with existing data")
	ErrProviderProfileRevisionConflict = errors.New("Generation Provider profile revision conflict")
)

const (
	EndpointVolcengineChina   = "volcengine_cn_beijing"
	EndpointBytePlusGlobal    = "byteplus_ap_southeast_1"
	EndpointMiniMaxChina      = "minimax_cn"
	EndpointMiniMaxGlobal     = "minimax_global"
	EndpointAlibabaChina      = "alibaba_cn_beijing"
	EndpointAlibabaSingapore  = "alibaba_ap_southeast_1"
	EndpointAlibabaTokyo      = "alibaba_ap_northeast_1"
	EndpointAlibabaFrankfurt  = "alibaba_eu_central_1"
	EndpointAlibabaVirginia   = "alibaba_us_east_1"
	EndpointAlibabaHongKong   = "alibaba_cn_hongkong"
	ProviderHealthUnchecked   = "unchecked"
	ProviderHealthReady       = "ready"
	ProviderHealthUnavailable = "unavailable"
)

type ProviderProfile struct {
	GeneratorID       domain.GeneratorID
	EndpointPreset    string
	ModelID           string
	ResourceID        string
	WorkspaceID       string
	CredentialRef     string
	Enabled           bool
	ConcurrencyLimit  int
	CapabilityVersion string
	Health            string
	FailureCode       string
	LastCheckedAt     *time.Time
	Revision          int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Models            []ProviderModel
}

type ProviderModel struct {
	ModelID           string
	IsDefault         bool
	CapabilityVersion string
	Health            string
	FailureCode       string
	LastCheckedAt     *time.Time
}

type ProviderProfileUpdate struct {
	EndpointPreset   string
	ModelID          string
	ResourceID       string
	WorkspaceID      string
	CredentialRef    string
	Enabled          bool
	ConcurrencyLimit int
	ExpectedRevision int64
	UpdatedAt        time.Time
	Models           []ProviderModel
}

func (s *Store) CreateProviderProfile(ctx context.Context, profile ProviderProfile) error {
	database, err := s.requireDatabase()
	if err != nil {
		return fmt.Errorf("create Generation Provider profile: %w", err)
	}
	if validateContext(ctx) != nil || validateProviderProfile(profile) != nil || profile.Revision != 1 || !profile.CreatedAt.Equal(profile.UpdatedAt) {
		return errors.New("create Generation Provider profile: invalid profile")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create Generation Provider profile: begin: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
INSERT INTO generation_provider_profiles (
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`,
		string(profile.GeneratorID), profile.EndpointPreset, profile.ModelID,
		nullableString(profile.ResourceID), nullableString(profile.WorkspaceID), profile.CredentialRef,
		boolInteger(profile.Enabled), profile.ConcurrencyLimit, nullableString(profile.CapabilityVersion), profile.Health,
		nullableString(profile.FailureCode), nullableTimeMilliseconds(profile.LastCheckedAt), profile.Revision,
		toMilliseconds(profile.CreatedAt), toMilliseconds(profile.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create Generation Provider profile: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create Generation Provider profile: affected rows: %w", err)
	}
	if rows != 1 {
		return ErrProviderProfileConflict
	}
	if err := replaceProviderModels(ctx, tx, profile.GeneratorID, normalizedProviderModels(profile)); err != nil {
		return fmt.Errorf("create Generation Provider models: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create Generation Provider profile: commit: %w", err)
	}
	return nil
}

func (s *Store) ProviderProfile(ctx context.Context, generatorID domain.GeneratorID) (ProviderProfile, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("read Generation Provider profile: %w", err)
	}
	if validateContext(ctx) != nil || !configurableGenerator(generatorID) {
		return ProviderProfile{}, errors.New("read Generation Provider profile: invalid Generator ID")
	}
	profile, err := scanProviderProfile(database.QueryRowContext(ctx, selectProviderProfileSQL+" WHERE generator_id = ?", string(generatorID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderProfile{}, ErrProviderProfileNotFound
	}
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("read Generation Provider profile: %w", err)
	}
	profile.Models, err = readProviderModels(ctx, database, generatorID)
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("read Generation Provider models: %w", err)
	}
	return profile, nil
}

func (s *Store) ProviderProfiles(ctx context.Context) ([]ProviderProfile, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, fmt.Errorf("list Generation Provider profiles: %w", err)
	}
	if err := validateContext(ctx); err != nil {
		return nil, fmt.Errorf("list Generation Provider profiles: %w", err)
	}
	rows, err := database.QueryContext(ctx, selectProviderProfileSQL+" ORDER BY generator_id ASC")
	if err != nil {
		return nil, fmt.Errorf("list Generation Provider profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]ProviderProfile, 0)
	for rows.Next() {
		profile, err := scanProviderProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("list Generation Provider profiles: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Generation Provider profiles: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list Generation Provider profiles: close: %w", err)
	}
	for index := range profiles {
		profiles[index].Models, err = readProviderModels(ctx, database, profiles[index].GeneratorID)
		if err != nil {
			return nil, fmt.Errorf("list Generation Provider models: %w", err)
		}
	}
	return profiles, nil
}

func (s *Store) UpdateProviderProfile(ctx context.Context, generatorID domain.GeneratorID, update ProviderProfileUpdate) (ProviderProfile, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider profile: %w", err)
	}
	candidate := ProviderProfile{
		GeneratorID: generatorID, EndpointPreset: update.EndpointPreset, ModelID: update.ModelID,
		ResourceID: update.ResourceID, WorkspaceID: update.WorkspaceID, CredentialRef: update.CredentialRef,
		Enabled: update.Enabled, ConcurrencyLimit: update.ConcurrencyLimit, Revision: update.ExpectedRevision + 1,
		Health: ProviderHealthUnchecked, CreatedAt: update.UpdatedAt, UpdatedAt: update.UpdatedAt,
	}
	if validateContext(ctx) != nil || update.ExpectedRevision < 1 || validateProviderProfile(candidate) != nil {
		return ProviderProfile{}, errors.New("update Generation Provider profile: invalid profile")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider profile: begin: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE generation_provider_profiles
SET endpoint_preset = ?, model_id = ?, resource_id = ?, workspace_id = ?, credential_ref = ?,
    enabled = ?, concurrency_limit = ?, capability_version = NULL, health = 'unchecked',
    failure_code = NULL, last_checked_at_ms = NULL, revision = revision + 1, updated_at_ms = ?
WHERE generator_id = ? AND revision = ?`,
		update.EndpointPreset, update.ModelID, nullableString(update.ResourceID), nullableString(update.WorkspaceID), update.CredentialRef,
		boolInteger(update.Enabled), update.ConcurrencyLimit, toMilliseconds(update.UpdatedAt), string(generatorID), update.ExpectedRevision)
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider profile: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider profile: affected rows: %w", err)
	}
	if rows != 1 {
		return ProviderProfile{}, ErrProviderProfileRevisionConflict
	}
	models := update.Models
	if len(models) == 0 {
		models = []ProviderModel{{ModelID: update.ModelID, IsDefault: true, Health: ProviderHealthUnchecked}}
	}
	if err := replaceProviderModels(ctx, tx, generatorID, models); err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider models: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ProviderProfile{}, fmt.Errorf("update Generation Provider profile: commit: %w", err)
	}
	return s.ProviderProfile(ctx, generatorID)
}

func (s *Store) RecordProviderProfileCheck(ctx context.Context, generatorID domain.GeneratorID, expectedRevision int64, capabilityVersion, health, failureCode string, at time.Time) (ProviderProfile, error) {
	return s.RecordProviderProfileModelChecks(ctx, generatorID, expectedRevision, capabilityVersion, health, failureCode, at, nil)
}

func (s *Store) RecordProviderProfileModelChecks(ctx context.Context, generatorID domain.GeneratorID, expectedRevision int64, capabilityVersion, health, failureCode string, at time.Time, models []ProviderModel) (ProviderProfile, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("record Generation Provider profile check: %w", err)
	}
	if validateContext(ctx) != nil || !configurableGenerator(generatorID) || expectedRevision < 1 || validateTimestamp("checked at", at) != nil ||
		!validProviderValue(capabilityVersion, 200, false) || !validProviderValue(failureCode, 100, false) ||
		health == ProviderHealthReady && (capabilityVersion == "" || failureCode != "") || health == ProviderHealthUnavailable && (capabilityVersion != "" || failureCode == "") ||
		health != ProviderHealthReady && health != ProviderHealthUnavailable {
		return ProviderProfile{}, errors.New("record Generation Provider profile check: invalid result")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("record Generation Provider profile check: begin: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE generation_provider_profiles
SET capability_version = ?, health = ?, failure_code = ?, last_checked_at_ms = ?,
    revision = revision + 1, updated_at_ms = ?
WHERE generator_id = ? AND revision = ?`, nullableString(capabilityVersion), health, nullableString(failureCode),
		toMilliseconds(at), toMilliseconds(at), string(generatorID), expectedRevision)
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("record Generation Provider profile check: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ProviderProfile{}, fmt.Errorf("record Generation Provider profile check: affected rows: %w", err)
	}
	if rows != 1 {
		return ProviderProfile{}, ErrProviderProfileRevisionConflict
	}
	if len(models) > 0 {
		for _, model := range models {
			if _, err := tx.ExecContext(ctx, `UPDATE generation_provider_models SET capability_version = ?, health = ?, failure_code = ?, last_checked_at_ms = ? WHERE generator_id = ? AND model_id = ?`, nullableString(model.CapabilityVersion), model.Health, nullableString(model.FailureCode), toMilliseconds(at), string(generatorID), model.ModelID); err != nil {
				return ProviderProfile{}, fmt.Errorf("record Generation Provider model check: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return ProviderProfile{}, fmt.Errorf("record Generation Provider profile check: commit: %w", err)
	}
	return s.ProviderProfile(ctx, generatorID)
}

func (s *Store) DeleteProviderProfile(ctx context.Context, generatorID domain.GeneratorID, expectedRevision int64) (string, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return "", fmt.Errorf("delete Generation Provider profile: %w", err)
	}
	if validateContext(ctx) != nil || !configurableGenerator(generatorID) || expectedRevision < 1 {
		return "", errors.New("delete Generation Provider profile: invalid request")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("delete Generation Provider profile: begin transaction: %w", err)
	}
	defer tx.Rollback()
	var credentialRef string
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT credential_ref, revision FROM generation_provider_profiles WHERE generator_id = ?", string(generatorID)).Scan(&credentialRef, &revision); errors.Is(err, sql.ErrNoRows) {
		return "", ErrProviderProfileNotFound
	} else if err != nil {
		return "", fmt.Errorf("delete Generation Provider profile: read: %w", err)
	}
	if revision != expectedRevision {
		return "", ErrProviderProfileRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM generation_provider_profiles WHERE generator_id = ? AND revision = ?", string(generatorID), expectedRevision); err != nil {
		return "", fmt.Errorf("delete Generation Provider profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("delete Generation Provider profile: commit: %w", err)
	}
	return credentialRef, nil
}

const selectProviderProfileSQL = `
SELECT generator_id, endpoint_preset, model_id, resource_id, workspace_id,
       credential_ref, enabled, concurrency_limit, capability_version, health,
       failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
FROM generation_provider_profiles`

func scanProviderProfile(row rowScanner) (ProviderProfile, error) {
	var profile ProviderProfile
	var generatorID string
	var resourceID, workspaceID sql.NullString
	var capabilityVersion, failureCode sql.NullString
	var lastCheckedAtMS sql.NullInt64
	var enabled int
	var createdAtMS, updatedAtMS int64
	if err := row.Scan(&generatorID, &profile.EndpointPreset, &profile.ModelID, &resourceID, &workspaceID,
		&profile.CredentialRef, &enabled, &profile.ConcurrencyLimit, &capabilityVersion, &profile.Health,
		&failureCode, &lastCheckedAtMS, &profile.Revision, &createdAtMS, &updatedAtMS); err != nil {
		return ProviderProfile{}, err
	}
	profile.GeneratorID = domain.GeneratorID(generatorID)
	if resourceID.Valid {
		profile.ResourceID = resourceID.String
	}
	if workspaceID.Valid {
		profile.WorkspaceID = workspaceID.String
	}
	if capabilityVersion.Valid {
		profile.CapabilityVersion = capabilityVersion.String
	}
	if failureCode.Valid {
		profile.FailureCode = failureCode.String
	}
	if lastCheckedAtMS.Valid {
		value := fromMilliseconds(lastCheckedAtMS.Int64)
		profile.LastCheckedAt = &value
	}
	profile.Enabled = enabled == 1
	profile.CreatedAt = fromMilliseconds(createdAtMS)
	profile.UpdatedAt = fromMilliseconds(updatedAtMS)
	if err := validateProviderProfile(profile); err != nil {
		return ProviderProfile{}, fmt.Errorf("invalid persisted Generation Provider profile: %w", err)
	}
	return profile, nil
}

func validateProviderProfile(profile ProviderProfile) error {
	if !configurableGenerator(profile.GeneratorID) || !validProviderValue(profile.EndpointPreset, 100, true) || !validProviderValue(profile.ModelID, 200, true) ||
		!validProviderValue(profile.ResourceID, 500, false) || !validProviderValue(profile.WorkspaceID, 500, false) || !validProviderValue(profile.CredentialRef, 500, true) ||
		!validProviderValue(profile.CapabilityVersion, 200, false) || !validProviderValue(profile.FailureCode, 100, false) ||
		profile.ConcurrencyLimit < 1 || profile.ConcurrencyLimit > 10 || profile.Revision < 1 || validateTimestamp("created at", profile.CreatedAt) != nil || validateTimestamp("updated at", profile.UpdatedAt) != nil || profile.UpdatedAt.Before(profile.CreatedAt) {
		return errors.New("invalid fields")
	}
	switch profile.Health {
	case ProviderHealthUnchecked:
		if profile.CapabilityVersion != "" || profile.FailureCode != "" || profile.LastCheckedAt != nil {
			return errors.New("invalid unchecked Provider health")
		}
	case ProviderHealthReady:
		if profile.CapabilityVersion == "" || profile.FailureCode != "" || profile.LastCheckedAt == nil || validateTimestamp("last checked at", *profile.LastCheckedAt) != nil {
			return errors.New("invalid ready Provider health")
		}
	case ProviderHealthUnavailable:
		if profile.CapabilityVersion != "" || profile.FailureCode == "" || profile.LastCheckedAt == nil || validateTimestamp("last checked at", *profile.LastCheckedAt) != nil {
			return errors.New("invalid unavailable Provider health")
		}
	default:
		return errors.New("invalid Provider health")
	}
	switch profile.GeneratorID {
	case domain.GeneratorVideoSeedanceChina:
		if profile.EndpointPreset != EndpointVolcengineChina || !validPersistedProviderModel(profile.GeneratorID, profile.ModelID) || profile.ResourceID != "" || profile.WorkspaceID != "" {
			return errors.New("invalid Volcengine Seedance profile")
		}
	case domain.GeneratorVideoSeedanceGlobal:
		if profile.EndpointPreset != EndpointBytePlusGlobal || !validPersistedProviderModel(profile.GeneratorID, profile.ModelID) || profile.ResourceID != "" || profile.WorkspaceID != "" {
			return errors.New("invalid BytePlus Seedance profile")
		}
	case domain.GeneratorVideoH3China:
		if profile.EndpointPreset != EndpointMiniMaxChina || !validPersistedProviderModel(profile.GeneratorID, profile.ModelID) || profile.ResourceID != "" || profile.WorkspaceID != "" {
			return errors.New("invalid MiniMax China H3 profile")
		}
	case domain.GeneratorVideoH3Official:
		if profile.EndpointPreset != EndpointMiniMaxGlobal || !validPersistedProviderModel(profile.GeneratorID, profile.ModelID) || profile.ResourceID != "" || profile.WorkspaceID != "" {
			return errors.New("invalid MiniMax H3 profile")
		}
	case domain.GeneratorVideoWanOfficial:
		if !alibabaEndpoint(profile.EndpointPreset) || profile.ModelID != "wan3.0-video" && profile.ModelID != "wan3.0-video-prime" || profile.ResourceID != "" || profile.WorkspaceID == "" {
			return errors.New("invalid Wan 3.0 profile")
		}
	}
	return nil
}

func normalizedProviderModels(profile ProviderProfile) []ProviderModel {
	if len(profile.Models) > 0 {
		return profile.Models
	}
	return []ProviderModel{{ModelID: profile.ModelID, IsDefault: true, CapabilityVersion: profile.CapabilityVersion, Health: profile.Health, FailureCode: profile.FailureCode, LastCheckedAt: profile.LastCheckedAt}}
}

func replaceProviderModels(ctx context.Context, tx *sql.Tx, generatorID domain.GeneratorID, models []ProviderModel) error {
	if len(models) == 0 {
		return errors.New("at least one model is required")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM generation_provider_models WHERE generator_id = ?", string(generatorID)); err != nil {
		return err
	}
	seen, defaults := map[string]bool{}, 0
	for _, model := range models {
		if seen[model.ModelID] || !validPersistedProviderModel(generatorID, model.ModelID) {
			return errors.New("invalid Provider model")
		}
		seen[model.ModelID] = true
		if model.IsDefault {
			defaults++
		}
		if model.Health == "" {
			model.Health = ProviderHealthUnchecked
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_provider_models (generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`, string(generatorID), model.ModelID, boolInteger(model.IsDefault), nullableString(model.CapabilityVersion), model.Health, nullableString(model.FailureCode), nullableTimeMilliseconds(model.LastCheckedAt)); err != nil {
			return err
		}
	}
	if defaults != 1 {
		return errors.New("exactly one default model is required")
	}
	return nil
}

type providerModelQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readProviderModels(ctx context.Context, database providerModelQuerier, generatorID domain.GeneratorID) ([]ProviderModel, error) {
	rows, err := database.QueryContext(ctx, `SELECT model_id, is_default, capability_version, health, failure_code, last_checked_at_ms FROM generation_provider_models WHERE generator_id = ? ORDER BY is_default DESC, model_id ASC`, string(generatorID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProviderModel{}
	for rows.Next() {
		var model ProviderModel
		var isDefault int
		var capability, failure sql.NullString
		var checked sql.NullInt64
		if err := rows.Scan(&model.ModelID, &isDefault, &capability, &model.Health, &failure, &checked); err != nil {
			return nil, err
		}
		model.IsDefault = isDefault == 1
		if capability.Valid {
			model.CapabilityVersion = capability.String
		}
		if failure.Valid {
			model.FailureCode = failure.String
		}
		if checked.Valid {
			at := fromMilliseconds(checked.Int64)
			model.LastCheckedAt = &at
		}
		result = append(result, model)
	}
	return result, rows.Err()
}

func validPersistedProviderModel(id domain.GeneratorID, model string) bool {
	switch id {
	case domain.GeneratorVideoSeedanceChina:
		return model == "doubao-seedance-2-5-260628" || model == "doubao-seedance-2-0-260128" || model == "doubao-seedance-2-0-fast-260128" || model == "doubao-seedance-2-0-mini-260615"
	case domain.GeneratorVideoSeedanceGlobal:
		return model == "dreamina-seedance-2-5-260628" || model == "dreamina-seedance-2-0-260128" || model == "dreamina-seedance-2-0-fast-260128" || model == "dreamina-seedance-2-0-mini-260615"
	case domain.GeneratorVideoH3China, domain.GeneratorVideoH3Official:
		return model == "MiniMax-H3" || model == "MiniMax-H3-Max"
	case domain.GeneratorVideoWanOfficial:
		return model == "wan3.0-video" || model == "wan3.0-video-prime"
	}
	return false
}

func configurableGenerator(id domain.GeneratorID) bool {
	return id == domain.GeneratorVideoSeedanceChina || id == domain.GeneratorVideoSeedanceGlobal || id == domain.GeneratorVideoH3China || id == domain.GeneratorVideoH3Official || id == domain.GeneratorVideoWanOfficial
}

func alibabaEndpoint(value string) bool {
	switch value {
	case EndpointAlibabaChina, EndpointAlibabaSingapore, EndpointAlibabaTokyo, EndpointAlibabaFrankfurt, EndpointAlibabaVirginia:
		return true
	default:
		return false
	}
}

func validProviderValue(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00") && utf8.RuneCountInString(value) <= maximum && value == strings.TrimSpace(value) && (!required || value != "")
}
