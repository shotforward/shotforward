package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const maxAPIKeyBytes = 16 * 1024

type Service struct {
	store       Store
	credentials CredentialStore
	ids         IDGenerator
	clock       Clock
	checkers    CheckerRegistry
	codexMutex  sync.Mutex
	codex       Profile
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Store == nil || dependencies.Credentials == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, errors.New("create Provider configuration Service: invalid dependencies")
	}
	if dependencies.Checkers == nil {
		dependencies.Checkers = EmptyCheckerRegistry{}
	}
	at := dependencies.Clock.Now()
	if at.IsZero() {
		return nil, errors.New("create Provider configuration Service: invalid clock")
	}
	return &Service{store: dependencies.Store, credentials: dependencies.Credentials, ids: dependencies.IDs, clock: dependencies.Clock, checkers: dependencies.Checkers,
		codex: Profile{GeneratorID: domain.GeneratorImageCodex, EndpointPreset: "codex_subscription", Enabled: true,
			ConcurrencyLimit: 5, Health: HealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at}}, nil
}

func (s *Service) Profiles(ctx context.Context) ([]Profile, error) {
	if err := validContext(ctx); err != nil {
		return nil, err
	}
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Provider configurations: %w", mapStoreError(err))
	}
	profiles := make([]Profile, 0, len(records)+1)
	profiles = append(profiles, s.codexProfile())
	for _, record := range records {
		profiles = append(profiles, publicProfile(record))
	}
	return profiles, nil
}

func (s *Service) Profile(ctx context.Context, generatorID domain.GeneratorID) (Profile, error) {
	if err := validContext(ctx); err != nil {
		return Profile{}, err
	}
	if generatorID == domain.GeneratorImageCodex {
		return s.codexProfile(), nil
	}
	if err := validGeneratorRequest(ctx, generatorID); err != nil {
		return Profile{}, err
	}
	record, err := s.store.Get(ctx, generatorID)
	if err != nil {
		return Profile{}, fmt.Errorf("read Provider configuration: %w", mapStoreError(err))
	}
	return publicProfile(record), nil
}

func (s *Service) RuntimeProvider(ctx context.Context, generatorID domain.GeneratorID) (Record, string, error) {
	return s.RuntimeProviderModel(ctx, generatorID, "")
}

func (s *Service) RuntimeProviderModel(ctx context.Context, generatorID domain.GeneratorID, modelID string) (Record, string, error) {
	if err := validGeneratorRequest(ctx, generatorID); err != nil {
		return Record{}, "", err
	}
	record, err := s.store.Get(ctx, generatorID)
	if err != nil {
		return Record{}, "", fmt.Errorf("read Provider runtime configuration: %w", mapStoreError(err))
	}
	if !record.Enabled || record.Health != HealthReady {
		return Record{}, "", ErrProviderUnavailable
	}
	if modelID != "" {
		model, ok := enabledModel(record.Models, modelID)
		if !ok || model.Health != HealthReady {
			return Record{}, "", ErrProviderUnavailable
		}
		record.ModelID = modelID
		record.CapabilityVersion = model.CapabilityVersion
	}
	secret, found, err := s.credentials.Get(ctx, record.CredentialRef)
	if err != nil || !found {
		return Record{}, "", ErrCredentialUnavailable
	}
	return record, secret, nil
}

func (s *Service) Configure(ctx context.Context, input ConfigureInput) (Profile, error) {
	input = normalizeConfigureModels(input)
	if err := validateConfigure(ctx, input); err != nil {
		return Profile{}, err
	}
	at := s.clock.Now()
	if at.IsZero() {
		return Profile{}, errors.New("configure Provider: invalid clock")
	}
	if input.ExpectedRevision == 0 {
		if input.APIKey == "" {
			return Profile{}, ErrInvalidRequest
		}
		reference, err := s.ids.NewCredentialReference()
		if err != nil || !validCredentialReference(reference) {
			return Profile{}, errors.New("configure Provider: create credential reference")
		}
		if err := s.credentials.Put(ctx, reference, input.APIKey); err != nil {
			return Profile{}, ErrCredentialUnavailable
		}
		record := Record{Profile: Profile{GeneratorID: input.GeneratorID, EndpointPreset: input.EndpointPreset, ModelID: input.ModelID,
			ResourceID: input.ResourceID, WorkspaceID: input.WorkspaceID, Enabled: input.Enabled, ConcurrencyLimit: input.ConcurrencyLimit,
			Health: HealthUnchecked, Revision: 1, CreatedAt: at, UpdatedAt: at, Models: uncheckedModels(input.Models)}, CredentialRef: reference}
		if err := s.store.Create(ctx, record); err != nil {
			_, _ = s.credentials.Delete(context.Background(), reference)
			return Profile{}, fmt.Errorf("configure Provider: %w", mapStoreError(err))
		}
		return publicProfile(record), nil
	}

	existing, err := s.store.Get(ctx, input.GeneratorID)
	if err != nil {
		return Profile{}, fmt.Errorf("configure Provider: %w", mapStoreError(err))
	}
	if existing.Revision != input.ExpectedRevision {
		return Profile{}, ErrRevisionConflict
	}
	if !validCredentialReference(existing.CredentialRef) {
		return Profile{}, errors.New("configure Provider: invalid persisted credential reference")
	}
	var priorSecret string
	secretChanged := input.APIKey != ""
	if secretChanged {
		var found bool
		priorSecret, found, err = s.credentials.Get(ctx, existing.CredentialRef)
		if err != nil || !found {
			return Profile{}, ErrCredentialUnavailable
		}
		if err := s.credentials.Put(ctx, existing.CredentialRef, input.APIKey); err != nil {
			return Profile{}, ErrCredentialUnavailable
		}
	}
	updated, err := s.store.Update(ctx, input.GeneratorID, UpdateRecord{EndpointPreset: input.EndpointPreset, ModelID: input.ModelID,
		Models:     input.Models,
		ResourceID: input.ResourceID, WorkspaceID: input.WorkspaceID, CredentialRef: existing.CredentialRef,
		Enabled: input.Enabled, ConcurrencyLimit: input.ConcurrencyLimit, ExpectedRevision: input.ExpectedRevision, UpdatedAt: at})
	if err != nil {
		if secretChanged {
			if restoreErr := s.credentials.Put(context.Background(), existing.CredentialRef, priorSecret); restoreErr != nil {
				return Profile{}, ErrCredentialUnavailable
			}
		}
		return Profile{}, fmt.Errorf("configure Provider: %w", mapStoreError(err))
	}
	return publicProfile(updated), nil
}

// ValidateAndConfigure checks every selected model with the proposed settings
// before persisting either the profile or its credential. A failed capability
// probe therefore leaves the last known-good configuration untouched.
func (s *Service) ValidateAndConfigure(ctx context.Context, input ConfigureInput) (Profile, error) {
	input = normalizeConfigureModels(input)
	if err := validateConfigure(ctx, input); err != nil {
		return Profile{}, err
	}

	candidate, secret, err := s.validationCandidate(ctx, input)
	if err != nil {
		return Profile{}, err
	}
	checker, ok := s.checkers.Checker(input.GeneratorID)
	if !ok || checker == nil {
		return Profile{}, ErrProviderValidation
	}

	modelChecks := make([]ModelCheckRecord, 0, len(input.Models))
	defaultResult := CheckResult{}
	for _, model := range input.Models {
		modelCandidate := candidate
		modelCandidate.ModelID = model.ModelID
		result, checkErr := checker.Check(ctx, modelCandidate, secret)
		if checkErr != nil || !validCheckResult(result) || result.FailureCode != "" {
			return Profile{}, ErrProviderValidation
		}
		modelChecks = append(modelChecks, ModelCheckRecord{
			ModelID: model.ModelID, CapabilityVersion: result.CapabilityVersion, Health: HealthReady,
		})
		if model.IsDefault {
			defaultResult = result
		}
	}
	if defaultResult.CapabilityVersion == "" {
		return Profile{}, ErrProviderValidation
	}

	configured, err := s.Configure(ctx, input)
	if err != nil {
		return Profile{}, err
	}
	checkedAt := s.clock.Now()
	if checkedAt.IsZero() {
		return Profile{}, errors.New("validate and configure Provider: invalid clock")
	}
	updated, err := s.store.RecordCheck(ctx, input.GeneratorID, configured.Revision, CheckRecord{
		CapabilityVersion: defaultResult.CapabilityVersion,
		Health:            HealthReady,
		CheckedAt:         checkedAt,
		Models:            modelChecks,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("validate and configure Provider: %w", mapStoreError(err))
	}
	return publicProfile(updated), nil
}

func (s *Service) validationCandidate(ctx context.Context, input ConfigureInput) (Record, string, error) {
	candidate := Record{Profile: Profile{
		GeneratorID: input.GeneratorID, EndpointPreset: input.EndpointPreset, ModelID: input.ModelID,
		ResourceID: input.ResourceID, WorkspaceID: input.WorkspaceID, Enabled: input.Enabled,
		ConcurrencyLimit: input.ConcurrencyLimit, Models: uncheckedModels(input.Models),
	}}
	if input.ExpectedRevision == 0 {
		if input.APIKey == "" {
			return Record{}, "", ErrInvalidRequest
		}
		return candidate, input.APIKey, nil
	}

	existing, err := s.store.Get(ctx, input.GeneratorID)
	if err != nil {
		return Record{}, "", fmt.Errorf("validate Provider configuration: %w", mapStoreError(err))
	}
	if existing.Revision != input.ExpectedRevision {
		return Record{}, "", ErrRevisionConflict
	}
	candidate.CredentialRef = existing.CredentialRef
	secret := input.APIKey
	if secret == "" {
		var found bool
		secret, found, err = s.credentials.Get(ctx, existing.CredentialRef)
		if err != nil || !found {
			return Record{}, "", ErrCredentialUnavailable
		}
	}
	return candidate, secret, nil
}

func (s *Service) Check(ctx context.Context, generatorID domain.GeneratorID) (Profile, error) {
	if err := validContext(ctx); err != nil {
		return Profile{}, err
	}
	if generatorID == domain.GeneratorImageCodex {
		return s.checkCodex(ctx)
	}
	if err := validGeneratorRequest(ctx, generatorID); err != nil {
		return Profile{}, err
	}
	record, err := s.store.Get(ctx, generatorID)
	if err != nil {
		return Profile{}, fmt.Errorf("check Provider: %w", mapStoreError(err))
	}
	result := CheckResult{}
	secret, found, credentialErr := s.credentials.Get(ctx, record.CredentialRef)
	if credentialErr != nil || !found {
		result.FailureCode = "credential_unavailable"
	} else if checker, ok := s.checkers.Checker(generatorID); !ok || checker == nil {
		result.FailureCode = "provider_adapter_unavailable"
	} else {
		modelChecks := make([]ModelCheckRecord, 0, len(record.Models))
		for _, model := range record.Models {
			candidate := record
			candidate.ModelID = model.ModelID
			checked, checkErr := checker.Check(ctx, candidate, secret)
			if checkErr != nil || !validCheckResult(checked) {
				checked = CheckResult{FailureCode: "provider_check_failed"}
			}
			health := HealthReady
			if checked.FailureCode != "" {
				health = HealthUnavailable
			}
			modelChecks = append(modelChecks, ModelCheckRecord{ModelID: model.ModelID, CapabilityVersion: checked.CapabilityVersion, Health: health, FailureCode: checked.FailureCode})
			if model.IsDefault {
				result = checked
			}
		}
		if len(modelChecks) == 0 {
			result = CheckResult{FailureCode: "provider_check_failed"}
		}
		record.Models = modelProfilesFromChecks(modelChecks, record.Models)
	}
	if !validCheckResult(result) {
		result = CheckResult{FailureCode: "provider_check_failed"}
	}
	health := HealthReady
	if result.FailureCode != "" {
		health = HealthUnavailable
	}
	checkedAt := s.clock.Now()
	if checkedAt.IsZero() {
		return Profile{}, errors.New("check Provider: invalid clock")
	}
	updated, err := s.store.RecordCheck(ctx, generatorID, record.Revision, CheckRecord{CapabilityVersion: result.CapabilityVersion,
		Health: health, FailureCode: result.FailureCode, CheckedAt: checkedAt, Models: modelCheckRecords(record, result)})
	if err != nil {
		return Profile{}, fmt.Errorf("check Provider: %w", mapStoreError(err))
	}
	return publicProfile(updated), nil
}

func (s *Service) codexProfile() Profile {
	s.codexMutex.Lock()
	defer s.codexMutex.Unlock()
	return s.codex
}

func (s *Service) checkCodex(ctx context.Context) (Profile, error) {
	result := CheckResult{}
	checker, ok := s.checkers.Checker(domain.GeneratorImageCodex)
	if !ok || checker == nil {
		result.FailureCode = "provider_adapter_unavailable"
	} else {
		var err error
		result, err = checker.Check(ctx, Record{Profile: s.codexProfile()}, "")
		if err != nil || !validCheckResult(result) {
			result = CheckResult{FailureCode: "provider_check_failed"}
		}
	}
	at := s.clock.Now()
	if at.IsZero() {
		return Profile{}, errors.New("check Codex Provider: invalid clock")
	}
	s.codexMutex.Lock()
	s.codex.Health, s.codex.CapabilityVersion, s.codex.FailureCode = HealthReady, result.CapabilityVersion, result.FailureCode
	if result.FailureCode != "" {
		s.codex.Health = HealthUnavailable
	}
	s.codex.LastCheckedAt, s.codex.UpdatedAt, s.codex.Revision = &at, at, s.codex.Revision+1
	profile := s.codex
	s.codexMutex.Unlock()
	return profile, nil
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) error {
	if err := validGeneratorRequest(ctx, input.GeneratorID); err != nil || input.ExpectedRevision < 1 {
		return ErrInvalidRequest
	}
	record, err := s.store.Get(ctx, input.GeneratorID)
	if err != nil {
		return fmt.Errorf("delete Provider configuration: %w", mapStoreError(err))
	}
	if record.Revision != input.ExpectedRevision {
		return ErrRevisionConflict
	}
	secret, found, getErr := s.credentials.Get(ctx, record.CredentialRef)
	if getErr != nil {
		return ErrCredentialUnavailable
	}
	removed, err := s.credentials.Delete(ctx, record.CredentialRef)
	if err != nil || found != removed {
		return ErrCredentialUnavailable
	}
	deletedReference, err := s.store.Delete(ctx, input.GeneratorID, input.ExpectedRevision)
	if err != nil {
		if found {
			if restoreErr := s.credentials.Put(context.Background(), record.CredentialRef, secret); restoreErr != nil {
				return ErrCredentialUnavailable
			}
		}
		return fmt.Errorf("delete Provider configuration: %w", mapStoreError(err))
	}
	if deletedReference != record.CredentialRef {
		return errors.New("delete Provider configuration: credential reference mismatch")
	}
	return nil
}

func publicProfile(record Record) Profile {
	profile := record.Profile
	profile.CredentialConfigured = record.CredentialRef != ""
	return profile
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	case errors.Is(err, ErrRevisionConflict):
		return ErrRevisionConflict
	default:
		return err
	}
}

func validateConfigure(ctx context.Context, input ConfigureInput) error {
	if validGeneratorRequest(ctx, input.GeneratorID) != nil || input.ExpectedRevision < 0 || input.ConcurrencyLimit < 1 || input.ConcurrencyLimit > MaximumConcurrency ||
		!validText(input.EndpointPreset, 100, true) || !validText(input.ModelID, 200, true) || !validText(input.ResourceID, 500, false) || !validText(input.WorkspaceID, 500, false) ||
		input.APIKey != "" && (!utf8.ValidString(input.APIKey) || len(input.APIKey) > maxAPIKeyBytes || strings.ContainsAny(input.APIKey, "\r\n\x00") || input.APIKey != strings.TrimSpace(input.APIKey)) {
		return ErrInvalidRequest
	}
	if len(input.Models) == 0 || input.ModelID == "" {
		return ErrInvalidRequest
	}
	seen, defaults := map[string]bool{}, 0
	for _, model := range input.Models {
		if !validText(model.ModelID, 200, true) || seen[model.ModelID] || !supportedModel(input.GeneratorID, model.ModelID) {
			return ErrInvalidRequest
		}
		seen[model.ModelID] = true
		if model.IsDefault {
			defaults++
			if model.ModelID != input.ModelID {
				return ErrInvalidRequest
			}
		}
	}
	if defaults != 1 {
		return ErrInvalidRequest
	}
	switch input.GeneratorID {
	case domain.GeneratorVideoSeedanceChina:
		if input.EndpointPreset != EndpointVolcengineChina || !supportedModel(input.GeneratorID, input.ModelID) || input.ResourceID != "" || input.WorkspaceID != "" {
			return ErrInvalidRequest
		}
	case domain.GeneratorVideoSeedanceGlobal:
		if input.EndpointPreset != EndpointBytePlusGlobal || input.ResourceID != "" || input.WorkspaceID != "" {
			return ErrInvalidRequest
		}
	case domain.GeneratorVideoH3China:
		if input.EndpointPreset != EndpointMiniMaxChina || input.ResourceID != "" || input.WorkspaceID != "" {
			return ErrInvalidRequest
		}
	case domain.GeneratorVideoH3Official:
		if input.EndpointPreset != EndpointMiniMaxGlobal || input.ResourceID != "" || input.WorkspaceID != "" {
			return ErrInvalidRequest
		}
	case domain.GeneratorVideoWanOfficial:
		if !alibabaEndpoint(input.EndpointPreset) || input.ModelID != "wan3.0-video" && input.ModelID != "wan3.0-video-prime" || input.ResourceID != "" || input.WorkspaceID == "" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func supportedModel(id domain.GeneratorID, model string) bool {
	switch id {
	case domain.GeneratorVideoSeedanceChina:
		switch model {
		case "doubao-seedance-2-5-260628", "doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615":
			return true
		}
	case domain.GeneratorVideoSeedanceGlobal:
		switch model {
		case "dreamina-seedance-2-5-260628", "dreamina-seedance-2-0-260128", "dreamina-seedance-2-0-fast-260128", "dreamina-seedance-2-0-mini-260615":
			return true
		}
	case domain.GeneratorVideoH3China, domain.GeneratorVideoH3Official:
		return model == "MiniMax-H3" || model == "MiniMax-H3-Max"
	case domain.GeneratorVideoWanOfficial:
		return model == "wan3.0-video" || model == "wan3.0-video-prime"
	}
	return false
}

func normalizeConfigureModels(input ConfigureInput) ConfigureInput {
	if len(input.Models) == 0 && input.ModelID != "" {
		input.Models = []ModelRecord{{ModelID: input.ModelID, IsDefault: true}}
	}
	if input.ModelID == "" {
		for _, model := range input.Models {
			if model.IsDefault {
				input.ModelID = model.ModelID
				break
			}
		}
	}
	return input
}

func uncheckedModels(models []ModelRecord) []ModelProfile {
	result := make([]ModelProfile, 0, len(models))
	for _, model := range models {
		result = append(result, ModelProfile{ModelID: model.ModelID, IsDefault: model.IsDefault, Health: HealthUnchecked})
	}
	return result
}

func enabledModel(models []ModelProfile, id string) (ModelProfile, bool) {
	for _, model := range models {
		if model.ModelID == id {
			return model, true
		}
	}
	return ModelProfile{}, false
}

func modelProfilesFromChecks(checks []ModelCheckRecord, existing []ModelProfile) []ModelProfile {
	result := make([]ModelProfile, 0, len(checks))
	for _, check := range checks {
		isDefault := false
		for _, model := range existing {
			if model.ModelID == check.ModelID {
				isDefault = model.IsDefault
				break
			}
		}
		result = append(result, ModelProfile{ModelID: check.ModelID, IsDefault: isDefault, CapabilityVersion: check.CapabilityVersion, Health: check.Health, FailureCode: check.FailureCode})
	}
	return result
}

func modelCheckRecords(record Record, fallback CheckResult) []ModelCheckRecord {
	fallbackHealth := HealthReady
	if fallback.FailureCode != "" {
		fallbackHealth = HealthUnavailable
	}
	if len(record.Models) == 0 {
		return []ModelCheckRecord{{ModelID: record.ModelID, CapabilityVersion: fallback.CapabilityVersion, Health: fallbackHealth, FailureCode: fallback.FailureCode}}
	}
	result := make([]ModelCheckRecord, 0, len(record.Models))
	for _, model := range record.Models {
		health, capability, failure := model.Health, model.CapabilityVersion, model.FailureCode
		if health == HealthUnchecked {
			health, capability, failure = fallbackHealth, fallback.CapabilityVersion, fallback.FailureCode
		}
		result = append(result, ModelCheckRecord{ModelID: model.ModelID, CapabilityVersion: capability, Health: health, FailureCode: failure})
	}
	return result
}

func validGeneratorRequest(ctx context.Context, id domain.GeneratorID) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	switch id {
	case domain.GeneratorVideoSeedanceChina, domain.GeneratorVideoSeedanceGlobal, domain.GeneratorVideoH3China, domain.GeneratorVideoH3Official, domain.GeneratorVideoWanOfficial:
		return nil
	default:
		return ErrInvalidRequest
	}
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00") && len([]rune(value)) <= maximum && (!required || value != "")
}

func validCredentialReference(value string) bool { return validText(value, 500, true) }

func alibabaEndpoint(value string) bool {
	switch value {
	case EndpointAlibabaChina, EndpointAlibabaSingapore, EndpointAlibabaTokyo, EndpointAlibabaFrankfurt, EndpointAlibabaVirginia:
		return true
	default:
		return false
	}
}

func validCheckResult(result CheckResult) bool {
	if !validText(result.CapabilityVersion, 200, false) || !validText(result.FailureCode, 100, false) {
		return false
	}
	return result.CapabilityVersion != "" && result.FailureCode == "" || result.CapabilityVersion == "" && result.FailureCode != ""
}
