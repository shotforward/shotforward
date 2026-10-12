package studio

import (
	"context"
	"errors"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ApplicationProviderProfileStore translates Studio persistence categories at
// the Provider configuration application boundary.
type ApplicationProviderProfileStore struct{ store *Store }

func NewApplicationProviderProfileStore(store *Store) (*ApplicationProviderProfileStore, error) {
	if store == nil {
		return nil, errors.New("create Application Provider profile Store: Studio Store is required")
	}
	return &ApplicationProviderProfileStore{store: store}, nil
}

func (s *ApplicationProviderProfileStore) Create(ctx context.Context, record providerconfig.Record) error {
	return mapProviderApplicationError(s.store.CreateProviderProfile(ctx, fromApplicationProviderRecord(record)))
}

func (s *ApplicationProviderProfileStore) Get(ctx context.Context, id domain.GeneratorID) (providerconfig.Record, error) {
	value, err := s.store.ProviderProfile(ctx, id)
	if err != nil {
		return providerconfig.Record{}, mapProviderApplicationError(err)
	}
	return toApplicationProviderRecord(value), nil
}

func (s *ApplicationProviderProfileStore) List(ctx context.Context) ([]providerconfig.Record, error) {
	values, err := s.store.ProviderProfiles(ctx)
	if err != nil {
		return nil, mapProviderApplicationError(err)
	}
	result := make([]providerconfig.Record, 0, len(values))
	for _, value := range values {
		result = append(result, toApplicationProviderRecord(value))
	}
	return result, nil
}

func (s *ApplicationProviderProfileStore) Update(ctx context.Context, id domain.GeneratorID, update providerconfig.UpdateRecord) (providerconfig.Record, error) {
	value, err := s.store.UpdateProviderProfile(ctx, id, ProviderProfileUpdate{EndpointPreset: update.EndpointPreset, ModelID: update.ModelID,
		Models:     fromApplicationProviderModels(update.Models),
		ResourceID: update.ResourceID, WorkspaceID: update.WorkspaceID, CredentialRef: update.CredentialRef,
		Enabled: update.Enabled, ConcurrencyLimit: update.ConcurrencyLimit, ExpectedRevision: update.ExpectedRevision, UpdatedAt: update.UpdatedAt})
	if err != nil {
		return providerconfig.Record{}, mapProviderApplicationError(err)
	}
	return toApplicationProviderRecord(value), nil
}

func (s *ApplicationProviderProfileStore) RecordCheck(ctx context.Context, id domain.GeneratorID, revision int64, check providerconfig.CheckRecord) (providerconfig.Record, error) {
	models := make([]ProviderModel, 0, len(check.Models))
	for _, model := range check.Models {
		models = append(models, ProviderModel{ModelID: model.ModelID, CapabilityVersion: model.CapabilityVersion, Health: model.Health, FailureCode: model.FailureCode})
	}
	value, err := s.store.RecordProviderProfileModelChecks(ctx, id, revision, check.CapabilityVersion, check.Health, check.FailureCode, check.CheckedAt, models)
	if err != nil {
		return providerconfig.Record{}, mapProviderApplicationError(err)
	}
	return toApplicationProviderRecord(value), nil
}

func (s *ApplicationProviderProfileStore) Delete(ctx context.Context, id domain.GeneratorID, revision int64) (string, error) {
	value, err := s.store.DeleteProviderProfile(ctx, id, revision)
	return value, mapProviderApplicationError(err)
}

func toApplicationProviderRecord(value ProviderProfile) providerconfig.Record {
	return providerconfig.Record{Profile: providerconfig.Profile{GeneratorID: value.GeneratorID, EndpointPreset: value.EndpointPreset,
		ModelID: value.ModelID, ResourceID: value.ResourceID, WorkspaceID: value.WorkspaceID, Enabled: value.Enabled,
		ConcurrencyLimit: value.ConcurrencyLimit, CapabilityVersion: value.CapabilityVersion, Health: value.Health,
		FailureCode: value.FailureCode, LastCheckedAt: value.LastCheckedAt, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Models: toApplicationProfileModels(value.Models)}, CredentialRef: value.CredentialRef}
}

func fromApplicationProviderRecord(value providerconfig.Record) ProviderProfile {
	return ProviderProfile{GeneratorID: value.GeneratorID, EndpointPreset: value.EndpointPreset, ModelID: value.ModelID,
		ResourceID: value.ResourceID, WorkspaceID: value.WorkspaceID, CredentialRef: value.CredentialRef, Enabled: value.Enabled,
		ConcurrencyLimit: value.ConcurrencyLimit, CapabilityVersion: value.CapabilityVersion, Health: value.Health,
		FailureCode: value.FailureCode, LastCheckedAt: value.LastCheckedAt, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Models: fromApplicationProfileModels(value.Models)}
}

func fromApplicationProviderModels(values []providerconfig.ModelRecord) []ProviderModel {
	result := make([]ProviderModel, 0, len(values))
	for _, value := range values {
		result = append(result, ProviderModel{ModelID: value.ModelID, IsDefault: value.IsDefault, Health: ProviderHealthUnchecked})
	}
	return result
}

func fromApplicationProfileModels(values []providerconfig.ModelProfile) []ProviderModel {
	result := make([]ProviderModel, 0, len(values))
	for _, value := range values {
		result = append(result, ProviderModel{ModelID: value.ModelID, IsDefault: value.IsDefault, CapabilityVersion: value.CapabilityVersion, Health: value.Health, FailureCode: value.FailureCode, LastCheckedAt: value.LastCheckedAt})
	}
	return result
}

func toApplicationProfileModels(values []ProviderModel) []providerconfig.ModelProfile {
	result := make([]providerconfig.ModelProfile, 0, len(values))
	for _, value := range values {
		result = append(result, providerconfig.ModelProfile{ModelID: value.ModelID, IsDefault: value.IsDefault, CapabilityVersion: value.CapabilityVersion, Health: value.Health, FailureCode: value.FailureCode, LastCheckedAt: value.LastCheckedAt})
	}
	return result
}

func mapProviderApplicationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrProviderProfileNotFound):
		return errors.Join(providerconfig.ErrNotFound, err)
	case errors.Is(err, ErrProviderProfileConflict), errors.Is(err, ErrProviderProfileRevisionConflict):
		return errors.Join(providerconfig.ErrRevisionConflict, err)
	default:
		return err
	}
}
