package providerconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestServiceCoordinatesProfileCredentialAndCheck(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	credentials := newMemoryCredentials()
	clock := &fixedClock{at: time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)}
	service, err := NewService(Dependencies{Store: store, Credentials: credentials, IDs: fixedIDs{"pcr_H3"}, Clock: clock,
		Checkers: checkerRegistry{domain.GeneratorVideoH3Official: checkerFunc(func(context.Context, Record, string) (CheckResult, error) {
			return CheckResult{CapabilityVersion: "minimax-h3-v1"}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.Configure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "minimax-private-key", Enabled: true, ConcurrencyLimit: 1})
	if err != nil || !profile.CredentialConfigured || profile.Revision != 1 || profile.Health != HealthUnchecked {
		t.Fatalf("Configure() = %#v, %v", profile, err)
	}
	if credentials.values["pcr_H3"] != "minimax-private-key" || store.records[domain.GeneratorVideoH3Official].CredentialRef != "pcr_H3" {
		t.Fatal("credential was not separated from profile")
	}

	clock.at = clock.at.Add(time.Second)
	profile, err = service.Check(context.Background(), domain.GeneratorVideoH3Official)
	if err != nil || profile.Health != HealthReady || profile.CapabilityVersion != "minimax-h3-v1" || profile.Revision != 2 {
		t.Fatalf("Check() = %#v, %v", profile, err)
	}

	clock.at = clock.at.Add(time.Second)
	profile, err = service.Configure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", Enabled: false, ConcurrencyLimit: 1, ExpectedRevision: 2})
	if err != nil || profile.Health != HealthUnchecked || profile.Revision != 3 || credentials.values["pcr_H3"] != "minimax-private-key" {
		t.Fatalf("update Configure() = %#v, %v", profile, err)
	}
	if err := service.Delete(context.Background(), DeleteInput{GeneratorID: domain.GeneratorVideoH3Official, ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	if len(credentials.values) != 0 || len(store.records) != 0 {
		t.Fatal("Delete() left profile or credential")
	}
}

func TestServiceConfiguresAndChecksMultipleModels(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	clock := &fixedClock{at: time.Date(2026, 9, 4, 5, 6, 7, 0, time.UTC)}
	service, err := NewService(Dependencies{Store: store, Credentials: newMemoryCredentials(), IDs: fixedIDs{"pcr_wan"}, Clock: clock,
		Checkers: checkerRegistry{domain.GeneratorVideoWanOfficial: checkerFunc(func(_ context.Context, record Record, _ string) (CheckResult, error) {
			return CheckResult{CapabilityVersion: "checked-" + record.ModelID}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := service.Configure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: EndpointAlibabaChina, WorkspaceID: "workspace", APIKey: "secret", Enabled: true, ConcurrencyLimit: 2, Models: []ModelRecord{{ModelID: "wan3.0-video", IsDefault: true}, {ModelID: "wan3.0-video-prime"}}})
	if err != nil || configured.ModelID != "wan3.0-video" || len(configured.Models) != 2 {
		t.Fatalf("Configure() = %#v, %v", configured, err)
	}
	clock.at = clock.at.Add(time.Second)
	checked, err := service.Check(context.Background(), domain.GeneratorVideoWanOfficial)
	if err != nil || len(checked.Models) != 2 || checked.Models[0].Health != HealthReady || checked.Models[1].Health != HealthReady {
		t.Fatalf("Check() = %#v, %v", checked, err)
	}
	record, _, err := service.RuntimeProviderModel(context.Background(), domain.GeneratorVideoWanOfficial, "wan3.0-video-prime")
	if err != nil || record.ModelID != "wan3.0-video-prime" || record.CapabilityVersion != "checked-wan3.0-video-prime" {
		t.Fatalf("RuntimeProviderModel() = %#v, %v", record, err)
	}
}

func TestServiceSupportsRegionalMiniMaxModels(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		id       domain.GeneratorID
		endpoint string
	}{
		{id: domain.GeneratorVideoH3China, endpoint: EndpointMiniMaxChina},
		{id: domain.GeneratorVideoH3Official, endpoint: EndpointMiniMaxGlobal},
	} {
		if err := validateConfigure(context.Background(), ConfigureInput{
			GeneratorID: test.id, EndpointPreset: test.endpoint, ModelID: "MiniMax-H3", APIKey: "regional-key", Enabled: true, ConcurrencyLimit: 1,
			Models: []ModelRecord{{ModelID: "MiniMax-H3", IsDefault: true}, {ModelID: "MiniMax-H3-Max"}},
		}); err != nil {
			t.Fatalf("validateConfigure(%s) error = %v", test.id, err)
		}
	}
	if err := validateConfigure(context.Background(), ConfigureInput{
		GeneratorID: domain.GeneratorVideoH3China, EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "wrong-region", Enabled: true, ConcurrencyLimit: 1,
		Models: []ModelRecord{{ModelID: "MiniMax-H3", IsDefault: true}},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cross-region MiniMax configuration error = %v", err)
	}
}

func TestServiceValidatesEveryModelBeforeSaving(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	credentials := newMemoryCredentials()
	service, err := NewService(Dependencies{Store: store, Credentials: credentials, IDs: fixedIDs{"pcr_wan"}, Clock: &fixedClock{at: time.Date(2026, 9, 4, 6, 7, 8, 0, time.UTC)},
		Checkers: checkerRegistry{domain.GeneratorVideoWanOfficial: checkerFunc(func(_ context.Context, record Record, secret string) (CheckResult, error) {
			if secret != "valid-secret" || record.WorkspaceID != "workspace" {
				return CheckResult{FailureCode: "authentication_failed"}, nil
			}
			return CheckResult{CapabilityVersion: "checked-" + record.ModelID}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	input := ConfigureInput{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: EndpointAlibabaChina,
		WorkspaceID: "workspace", APIKey: "valid-secret", Enabled: true, ConcurrencyLimit: MaximumConcurrency,
		Models: []ModelRecord{{ModelID: "wan3.0-video", IsDefault: true}, {ModelID: "wan3.0-video-prime"}}}
	profile, err := service.ValidateAndConfigure(context.Background(), input)
	if err != nil || profile.Health != HealthReady || profile.Revision != 2 || profile.ConcurrencyLimit != MaximumConcurrency || len(profile.Models) != 2 {
		t.Fatalf("ValidateAndConfigure() = %#v, %v", profile, err)
	}
	for _, model := range profile.Models {
		if model.Health != HealthReady || model.CapabilityVersion != "checked-"+model.ModelID {
			t.Fatalf("validated model = %#v", model)
		}
	}
	if credentials.values["pcr_wan"] != "valid-secret" || len(store.records) != 1 {
		t.Fatal("validated configuration was not persisted")
	}
}

func TestServiceEnforcesProviderConcurrencyRange(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	credentials := newMemoryCredentials()
	service, err := NewService(Dependencies{
		Store: store, Credentials: credentials, IDs: fixedIDs{"pcr_H3"}, Clock: &fixedClock{at: time.Now().UTC()},
		Checkers: EmptyCheckerRegistry{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, concurrency := range []int{0, MaximumConcurrency + 1} {
		_, err := service.Configure(context.Background(), ConfigureInput{
			GeneratorID: domain.GeneratorVideoH3Official, EndpointPreset: EndpointMiniMaxGlobal,
			ModelID: "MiniMax-H3", APIKey: "must-not-write", Enabled: true, ConcurrencyLimit: concurrency,
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Configure(concurrency %d) error = %v", concurrency, err)
		}
	}
	if len(store.records) != 0 || len(credentials.values) != 0 {
		t.Fatal("invalid concurrency persisted configuration or credential")
	}

	profile, err := service.Configure(context.Background(), ConfigureInput{
		GeneratorID: domain.GeneratorVideoH3Official, EndpointPreset: EndpointMiniMaxGlobal,
		ModelID: "MiniMax-H3", APIKey: "valid-key", Enabled: true, ConcurrencyLimit: MaximumConcurrency,
	})
	if err != nil || profile.ConcurrencyLimit != MaximumConcurrency {
		t.Fatalf("Configure(max concurrency) = %#v, %v", profile, err)
	}
}

func TestServiceDoesNotSaveFailedValidation(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	credentials := newMemoryCredentials()
	service, err := NewService(Dependencies{Store: store, Credentials: credentials, IDs: fixedIDs{"must-not-be-used"}, Clock: &fixedClock{at: time.Now().UTC()},
		Checkers: checkerRegistry{domain.GeneratorVideoH3Official: checkerFunc(func(context.Context, Record, string) (CheckResult, error) {
			return CheckResult{FailureCode: "authentication_failed"}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ValidateAndConfigure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "invalid-secret", Enabled: true, ConcurrencyLimit: 1})
	if !errors.Is(err, ErrProviderValidation) {
		t.Fatalf("ValidateAndConfigure() error = %v", err)
	}
	if len(store.records) != 0 || len(credentials.values) != 0 {
		t.Fatal("failed validation persisted configuration or credential")
	}

	saved, err := service.Configure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "last-known-good", Enabled: true, ConcurrencyLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ValidateAndConfigure(context.Background(), ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official,
		EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "invalid-replacement", Enabled: false,
		ConcurrencyLimit: 2, ExpectedRevision: saved.Revision})
	if !errors.Is(err, ErrProviderValidation) {
		t.Fatalf("ValidateAndConfigure(update) error = %v", err)
	}
	unchanged := store.records[domain.GeneratorVideoH3Official]
	if !unchanged.Enabled || unchanged.ConcurrencyLimit != 1 || unchanged.Revision != saved.Revision || credentials.values[unchanged.CredentialRef] != "last-known-good" {
		t.Fatalf("failed validation changed saved configuration: %#v", unchanged)
	}
}

func TestServiceRollsBackCredentialSaga(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newMemoryStore()
	credentials := newMemoryCredentials()
	service, _ := NewService(Dependencies{Store: store, Credentials: credentials, IDs: fixedIDs{"pcr_H3"}, Clock: &fixedClock{at: time.Now().UTC()}, Checkers: EmptyCheckerRegistry{}})
	input := ConfigureInput{GeneratorID: domain.GeneratorVideoH3Official, EndpointPreset: EndpointMiniMaxGlobal, ModelID: "MiniMax-H3", APIKey: "old-key", Enabled: true, ConcurrencyLimit: 1}
	if _, err := service.Configure(ctx, input); err != nil {
		t.Fatal(err)
	}
	store.updateErr = ErrRevisionConflict
	input.APIKey, input.ExpectedRevision = "replacement-key", 1
	if _, err := service.Configure(ctx, input); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Configure(update) error = %v", err)
	}
	if credentials.values["pcr_H3"] != "old-key" {
		t.Fatal("failed update did not restore prior credential")
	}
	store.updateErr = nil
	store.deleteErr = ErrRevisionConflict
	if err := service.Delete(ctx, DeleteInput{GeneratorID: domain.GeneratorVideoH3Official, ExpectedRevision: 1}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Delete() error = %v", err)
	}
	if credentials.values["pcr_H3"] != "old-key" {
		t.Fatal("failed delete did not restore credential")
	}
}

func TestServiceRejectsCodexAndRouteConfusionBeforeWritingSecret(t *testing.T) {
	t.Parallel()
	credentials := newMemoryCredentials()
	service, _ := NewService(Dependencies{Store: newMemoryStore(), Credentials: credentials, IDs: fixedIDs{"pcr_BAD"}, Clock: &fixedClock{at: time.Now().UTC()}})
	for _, input := range []ConfigureInput{
		{GeneratorID: domain.GeneratorImageCodex, EndpointPreset: "openai", ModelID: "gpt-image", APIKey: "must-not-write", ConcurrencyLimit: 5},
		{GeneratorID: domain.GeneratorVideoSeedanceChina, EndpointPreset: EndpointBytePlusGlobal, ModelID: "doubao-seedance-2-5-260628", APIKey: "must-not-write", ConcurrencyLimit: 1},
		{GeneratorID: domain.GeneratorVideoSeedanceGlobal, EndpointPreset: EndpointBytePlusGlobal, ModelID: "dreamina-seedance-2-5-260628", ResourceID: "unused-endpoint", APIKey: "must-not-write", ConcurrencyLimit: 1},
		{GeneratorID: domain.GeneratorVideoWanOfficial, EndpointPreset: EndpointAlibabaHongKong, ModelID: "wan3.0-video", WorkspaceID: "llm-workspace", APIKey: "must-not-write", ConcurrencyLimit: 1},
	} {
		if _, err := service.Configure(context.Background(), input); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Configure(%s) error = %v", input.GeneratorID, err)
		}
	}
	if len(credentials.values) != 0 {
		t.Fatal("invalid input wrote a credential")
	}
}

func TestSupportedSeedanceModelsRemainRegionSpecific(t *testing.T) {
	t.Parallel()
	for _, model := range []string{
		"doubao-seedance-2-5-260628",
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
		"doubao-seedance-2-0-mini-260615",
	} {
		if !supportedModel(domain.GeneratorVideoSeedanceChina, model) {
			t.Fatalf("China Seedance model %q is not supported", model)
		}
		if supportedModel(domain.GeneratorVideoSeedanceGlobal, model) {
			t.Fatalf("China Seedance model %q leaked into the Global route", model)
		}
	}
}

func TestServicePublishesAndChecksTransientCodexSubscription(t *testing.T) {
	t.Parallel()
	clock := &fixedClock{at: time.Date(2026, 9, 4, 4, 5, 6, 0, time.UTC)}
	service, err := NewService(Dependencies{Store: newMemoryStore(), Credentials: newMemoryCredentials(), IDs: fixedIDs{"unused"}, Clock: clock,
		Checkers: checkerRegistry{domain.GeneratorImageCodex: checkerFunc(func(_ context.Context, record Record, secret string) (CheckResult, error) {
			if record.GeneratorID != domain.GeneratorImageCodex || secret != "" {
				t.Fatal("Codex check received credential material")
			}
			return CheckResult{CapabilityVersion: "codex-subscription-image-v1@0.153.0"}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := service.Profiles(context.Background())
	if err != nil || len(profiles) != 1 || profiles[0].GeneratorID != domain.GeneratorImageCodex || profiles[0].ConcurrencyLimit != 5 || profiles[0].CredentialConfigured {
		t.Fatalf("Profiles() = %#v, %v", profiles, err)
	}
	clock.at = clock.at.Add(time.Second)
	profile, err := service.Check(context.Background(), domain.GeneratorImageCodex)
	if err != nil || profile.Health != HealthReady || profile.CapabilityVersion != "codex-subscription-image-v1@0.153.0" || profile.Revision != 2 {
		t.Fatalf("Check(Codex) = %#v, %v", profile, err)
	}
	read, err := service.Profile(context.Background(), domain.GeneratorImageCodex)
	if err != nil || read.Health != HealthReady || read.Revision != 2 {
		t.Fatalf("Profile(Codex) = %#v, %v", read, err)
	}
}

type fixedIDs []string

func (f fixedIDs) NewCredentialReference() (string, error) { return f[0], nil }

type fixedClock struct{ at time.Time }

func (c *fixedClock) Now() time.Time { return c.at }

type checkerFunc func(context.Context, Record, string) (CheckResult, error)

func (f checkerFunc) Check(ctx context.Context, record Record, secret string) (CheckResult, error) {
	return f(ctx, record, secret)
}

type checkerRegistry map[domain.GeneratorID]Checker

func (r checkerRegistry) Checker(id domain.GeneratorID) (Checker, bool) {
	value, ok := r[id]
	return value, ok
}

type memoryCredentials struct{ values map[string]string }

func newMemoryCredentials() *memoryCredentials {
	return &memoryCredentials{values: map[string]string{}}
}
func (m *memoryCredentials) Put(_ context.Context, reference, secret string) error {
	m.values[reference] = secret
	return nil
}
func (m *memoryCredentials) Get(_ context.Context, reference string) (string, bool, error) {
	value, ok := m.values[reference]
	return value, ok, nil
}
func (m *memoryCredentials) Delete(_ context.Context, reference string) (bool, error) {
	_, ok := m.values[reference]
	delete(m.values, reference)
	return ok, nil
}

type memoryStore struct {
	records              map[domain.GeneratorID]Record
	updateErr, deleteErr error
}

func newMemoryStore() *memoryStore { return &memoryStore{records: map[domain.GeneratorID]Record{}} }
func (m *memoryStore) Create(_ context.Context, record Record) error {
	if _, ok := m.records[record.GeneratorID]; ok {
		return ErrRevisionConflict
	}
	m.records[record.GeneratorID] = record
	return nil
}
func (m *memoryStore) Get(_ context.Context, id domain.GeneratorID) (Record, error) {
	value, ok := m.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return value, nil
}
func (m *memoryStore) List(context.Context) ([]Record, error) {
	result := make([]Record, 0, len(m.records))
	for _, value := range m.records {
		result = append(result, value)
	}
	return result, nil
}
func (m *memoryStore) Update(_ context.Context, id domain.GeneratorID, update UpdateRecord) (Record, error) {
	if m.updateErr != nil {
		return Record{}, m.updateErr
	}
	value, ok := m.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if value.Revision != update.ExpectedRevision {
		return Record{}, ErrRevisionConflict
	}
	value.EndpointPreset, value.ModelID, value.ResourceID, value.WorkspaceID, value.CredentialRef = update.EndpointPreset, update.ModelID, update.ResourceID, update.WorkspaceID, update.CredentialRef
	value.Models = uncheckedModels(update.Models)
	value.Enabled, value.ConcurrencyLimit, value.Health, value.CapabilityVersion, value.FailureCode, value.LastCheckedAt = update.Enabled, update.ConcurrencyLimit, HealthUnchecked, "", "", nil
	value.Revision, value.UpdatedAt = value.Revision+1, update.UpdatedAt
	m.records[id] = value
	return value, nil
}
func (m *memoryStore) RecordCheck(_ context.Context, id domain.GeneratorID, revision int64, check CheckRecord) (Record, error) {
	value, ok := m.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if value.Revision != revision {
		return Record{}, ErrRevisionConflict
	}
	value.Health, value.CapabilityVersion, value.FailureCode = check.Health, check.CapabilityVersion, check.FailureCode
	value.LastCheckedAt = &check.CheckedAt
	for index := range value.Models {
		for _, model := range check.Models {
			if value.Models[index].ModelID == model.ModelID {
				value.Models[index].Health, value.Models[index].CapabilityVersion, value.Models[index].FailureCode, value.Models[index].LastCheckedAt = model.Health, model.CapabilityVersion, model.FailureCode, &check.CheckedAt
			}
		}
	}
	value.Revision, value.UpdatedAt = value.Revision+1, check.CheckedAt
	m.records[id] = value
	return value, nil
}
func (m *memoryStore) Delete(_ context.Context, id domain.GeneratorID, revision int64) (string, error) {
	if m.deleteErr != nil {
		return "", m.deleteErr
	}
	value, ok := m.records[id]
	if !ok {
		return "", ErrNotFound
	}
	if value.Revision != revision {
		return "", ErrRevisionConflict
	}
	delete(m.records, id)
	return value.CredentialRef, nil
}
