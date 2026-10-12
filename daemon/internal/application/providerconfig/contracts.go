// Package providerconfig coordinates non-secret Generation Provider settings
// with credentials stored in the operating system vault.
package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	MaximumConcurrency = 10

	EndpointVolcengineChina  = "volcengine_cn_beijing"
	EndpointBytePlusGlobal   = "byteplus_ap_southeast_1"
	EndpointMiniMaxChina     = "minimax_cn"
	EndpointMiniMaxGlobal    = "minimax_global"
	EndpointAlibabaChina     = "alibaba_cn_beijing"
	EndpointAlibabaSingapore = "alibaba_ap_southeast_1"
	EndpointAlibabaTokyo     = "alibaba_ap_northeast_1"
	EndpointAlibabaFrankfurt = "alibaba_eu_central_1"
	EndpointAlibabaVirginia  = "alibaba_us_east_1"
	EndpointAlibabaHongKong  = "alibaba_cn_hongkong"

	HealthUnchecked   = "unchecked"
	HealthReady       = "ready"
	HealthUnavailable = "unavailable"
)

var (
	ErrInvalidRequest        = errors.New("invalid Provider configuration request")
	ErrNotFound              = errors.New("Provider configuration not found")
	ErrRevisionConflict      = errors.New("Provider configuration revision conflict")
	ErrCredentialUnavailable = errors.New("Provider credential store unavailable")
	ErrProviderValidation    = errors.New("Provider configuration validation failed")
	ErrProviderUnavailable   = errors.New("Provider configuration is unavailable")
)

type Profile struct {
	GeneratorID          domain.GeneratorID
	EndpointPreset       string
	ModelID              string
	ResourceID           string
	WorkspaceID          string
	Enabled              bool
	ConcurrencyLimit     int
	CredentialConfigured bool
	CapabilityVersion    string
	Health               string
	FailureCode          string
	LastCheckedAt        *time.Time
	Revision             int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Models               []ModelProfile
}

type ModelProfile struct {
	ModelID            string
	DisplayName        string
	Availability       string
	Family             string
	Variant            string
	SortOrder          int
	Deprecated         bool
	ReplacementModelID string
	IsDefault          bool
	CapabilityVersion  string
	Capabilities       *ModelCapabilities
	Health             string
	FailureCode        string
	LastCheckedAt      *time.Time
}

// ModelCapabilities is the normalized, non-secret model catalog contract
// projected to the desktop. Cloud-managed Providers populate it dynamically;
// configured BYOK Providers may omit it and retain their local adapter rules.
type ModelCapabilities struct {
	Workflow           string                 `json:"workflow"`
	GenerationModes    []string               `json:"generation_modes"`
	Duration           ModelDuration          `json:"duration"`
	Output             ModelOutput            `json:"output"`
	Inputs             []ModelInputCapability `json:"inputs"`
	MinimumTotalInputs int                    `json:"minimum_total_inputs"`
	MaximumTotalInputs int                    `json:"maximum_total_inputs"`
	SupportsStop       bool                   `json:"supports_stop"`
	StopPolicy         string                 `json:"stop_policy"`
	SupportsRecovery   bool                   `json:"supports_recovery"`
	RecoveryPolicy     string                 `json:"recovery_policy"`
}

// MarshalJSON keeps required catalog collections as arrays even when an older
// server omitted them or sent null. Do not change the source catalog in place:
// it may be shared by concurrent readers, and nil inputs still distinguish an
// incomplete catalog from an explicitly empty input list during normalization.
func (c ModelCapabilities) MarshalJSON() ([]byte, error) {
	type wireCapabilities ModelCapabilities
	wire := wireCapabilities(c)
	if wire.GenerationModes == nil {
		wire.GenerationModes = []string{}
	}
	if wire.Output.Sizes == nil {
		wire.Output.Sizes = []string{}
	}
	wire.Inputs = make([]ModelInputCapability, len(c.Inputs))
	copy(wire.Inputs, c.Inputs)
	for index := range wire.Inputs {
		if wire.Inputs[index].GenerationModes == nil {
			wire.Inputs[index].GenerationModes = []string{}
		}
		if wire.Inputs[index].AcceptedMIMETypes == nil {
			wire.Inputs[index].AcceptedMIMETypes = []string{}
		}
	}
	return json.Marshal(wire)
}

type ModelDuration struct {
	MinimumSeconds int64 `json:"minimum_seconds"`
	MaximumSeconds int64 `json:"maximum_seconds"`
	StepSeconds    int64 `json:"step_seconds"`
	DefaultSeconds int64 `json:"default_seconds"`
}

type ModelOutput struct {
	DefaultSize string   `json:"default_size"`
	Sizes       []string `json:"sizes"`
}

type ModelInputCapability struct {
	Role                string   `json:"role"`
	GenerationModes     []string `json:"generation_modes"`
	Required            bool     `json:"required"`
	MinimumCount        int      `json:"minimum_count"`
	MaximumCount        int      `json:"maximum_count"`
	MaximumBytes        int64    `json:"maximum_bytes"`
	ByteLimitSource     string   `json:"byte_limit_source"`
	MinimumSeconds      int64    `json:"minimum_seconds,omitempty"`
	MaximumSeconds      int64    `json:"maximum_seconds,omitempty"`
	MaximumTotalSeconds int64    `json:"maximum_total_seconds,omitempty"`
	AcceptedMIMETypes   []string `json:"accepted_mime_types"`
	RequiresAnyOf       []string `json:"requires_any_of,omitempty"`
}

// Record includes the opaque credential reference needed by adapters. It must
// never be serialized into an HTTP or GenerationJob projection.
type Record struct {
	Profile
	CredentialRef string
}

type Store interface {
	Create(context.Context, Record) error
	Get(context.Context, domain.GeneratorID) (Record, error)
	List(context.Context) ([]Record, error)
	Update(context.Context, domain.GeneratorID, UpdateRecord) (Record, error)
	RecordCheck(context.Context, domain.GeneratorID, int64, CheckRecord) (Record, error)
	Delete(context.Context, domain.GeneratorID, int64) (string, error)
}

type UpdateRecord struct {
	EndpointPreset, ModelID, ResourceID, WorkspaceID, CredentialRef string
	Models                                                          []ModelRecord
	Enabled                                                         bool
	ConcurrencyLimit                                                int
	ExpectedRevision                                                int64
	UpdatedAt                                                       time.Time
}

type CheckRecord struct {
	CapabilityVersion, Health, FailureCode string
	CheckedAt                              time.Time
	Models                                 []ModelCheckRecord
}

type ModelRecord struct {
	ModelID   string
	IsDefault bool
}

type ModelCheckRecord struct {
	ModelID, CapabilityVersion, Health, FailureCode string
}

type CredentialStore interface {
	Put(context.Context, string, string) error
	Get(context.Context, string) (string, bool, error)
	Delete(context.Context, string) (bool, error)
}

type IDGenerator interface{ NewCredentialReference() (string, error) }
type Clock interface{ Now() time.Time }

// Checker performs only non-generating authentication and capability probes.
// Implementations must never create a media task or consume generation quota.
type Checker interface {
	Check(context.Context, Record, string) (CheckResult, error)
}

type CheckerRegistry interface {
	Checker(domain.GeneratorID) (Checker, bool)
}

type CheckResult struct{ CapabilityVersion, FailureCode string }

// RuntimeConfiguration is consumed only by installed server-side Provider
// adapters. HTTP projections intentionally do not expose it.
type RuntimeConfiguration interface {
	RuntimeProvider(context.Context, domain.GeneratorID) (Record, string, error)
}

type ModelRuntimeConfiguration interface {
	RuntimeProviderModel(context.Context, domain.GeneratorID, string) (Record, string, error)
}

type ConfigureInput struct {
	GeneratorID                                              domain.GeneratorID
	EndpointPreset, ModelID, ResourceID, WorkspaceID, APIKey string
	Models                                                   []ModelRecord
	Enabled                                                  bool
	ConcurrencyLimit                                         int
	ExpectedRevision                                         int64
}

type DeleteInput struct {
	GeneratorID      domain.GeneratorID
	ExpectedRevision int64
}

type Dependencies struct {
	Store       Store
	Credentials CredentialStore
	IDs         IDGenerator
	Clock       Clock
	Checkers    CheckerRegistry
}
