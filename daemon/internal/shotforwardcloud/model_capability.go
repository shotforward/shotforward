package shotforwardcloud

import (
	"errors"
	"slices"
	"sort"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func modelAvailable(model Model) bool {
	return !model.Deprecated && (model.Availability == "" || model.Availability == "available")
}

func sortCloudModels(models []Model) {
	sort.SliceStable(models, func(left, right int) bool {
		if models[left].SortOrder != models[right].SortOrder {
			return models[left].SortOrder < models[right].SortOrder
		}
		return models[left].ID < models[right].ID
	})
}

// Older image catalogs include output metadata but omit reference inputs. The
// installed cloud image adapter still accepts five optional references in that
// case. Fill only missing/null inputs; an explicitly empty array is an
// authoritative declaration that references are unsupported by the model.
func completeImageModelCapabilities(model Model) *providerconfig.ModelCapabilities {
	if model.ProductKind != "image" || model.Capabilities == nil || model.Capabilities.Inputs != nil {
		return model.Capabilities
	}
	capability := *model.Capabilities
	capability.Inputs = []providerconfig.ModelInputCapability{{
		Role: "reference_image", MaximumCount: 5, MaximumBytes: 256 << 20,
		AcceptedMIMETypes: []string{"image/png", "image/jpeg", "image/webp"},
		ByteLimitSource:   "local_adapter",
	}}
	if capability.MaximumTotalInputs == 0 {
		capability.MaximumTotalInputs = 5
	}
	return &capability
}

func videoModelCapability(model Model) (projectapp.GenerationCapability, error) {
	if strings.TrimSpace(model.ID) == "" || model.ProductKind != "video" || !modelAvailable(model) {
		return projectapp.GenerationCapability{}, errors.New("cloud video model is unavailable")
	}
	if model.Capabilities == nil {
		return legacyVideoModelCapability(model)
	}
	catalog := model.Capabilities
	if strings.TrimSpace(model.CapabilityVersion) == "" || catalog.Duration.MinimumSeconds < 1 || catalog.Duration.MaximumSeconds < catalog.Duration.MinimumSeconds || catalog.Duration.StepSeconds < 1 || len(catalog.GenerationModes) == 0 || len(catalog.Output.Sizes) == 0 {
		return projectapp.GenerationCapability{}, errors.New("cloud video model capabilities are invalid")
	}
	capability := projectapp.GenerationCapability{
		GeneratorID: domain.GeneratorVideoH3ShotForward, ProviderModelID: model.ID,
		ProviderConnectionRevision: 1, CapabilityVersion: model.CapabilityVersion, MediaKind: domain.MediaKindVideo,
		MinimumDurationSeconds: catalog.Duration.MinimumSeconds, MaximumDurationSeconds: catalog.Duration.MaximumSeconds,
		DurationStepSeconds: catalog.Duration.StepSeconds, SupportedOutputSizes: slices.Clone(catalog.Output.Sizes),
		MinimumTotalReferences: catalog.MinimumTotalInputs, MaximumTotalReferences: catalog.MaximumTotalInputs,
		SupportsStop: catalog.SupportsStop, SupportsRecovery: catalog.SupportsRecovery,
	}
	for _, value := range catalog.GenerationModes {
		mode := domain.ClipGenerationMode(value)
		if !mode.IsValid() || slices.Contains(capability.SupportedVideoModes, mode) {
			return projectapp.GenerationCapability{}, errors.New("cloud video model generation modes are invalid")
		}
		capability.SupportedVideoModes = append(capability.SupportedVideoModes, mode)
	}
	for _, input := range catalog.Inputs {
		role, ok := cloudInputRole(input.Role)
		if !ok || input.MinimumCount < 0 || input.MaximumCount < 1 || input.MinimumCount > input.MaximumCount || input.MaximumBytes < 0 || input.MinimumSeconds < 0 || input.MaximumSeconds < input.MinimumSeconds || input.MaximumTotalSeconds < 0 {
			return projectapp.GenerationCapability{}, errors.New("cloud video model input capability is invalid")
		}
		limit := projectapp.GenerationReferenceCapability{
			Role: role, MinimumCount: input.MinimumCount, MaximumCount: input.MaximumCount, MaximumBytes: input.MaximumBytes,
			MinimumDurationMilliseconds: input.MinimumSeconds * 1_000, MaximumDurationMilliseconds: input.MaximumSeconds * 1_000,
			MaximumTotalDurationMilliseconds: input.MaximumTotalSeconds * 1_000, AcceptedMIMETypes: slices.Clone(input.AcceptedMIMETypes),
		}
		for _, value := range input.GenerationModes {
			mode := domain.ClipGenerationMode(value)
			if !mode.IsValid() {
				return projectapp.GenerationCapability{}, errors.New("cloud video model input mode is invalid")
			}
			limit.GenerationModes = append(limit.GenerationModes, mode)
		}
		for _, value := range input.RequiresAnyOf {
			required, found := cloudInputRole(value)
			if !found {
				return projectapp.GenerationCapability{}, errors.New("cloud video model input dependency is invalid")
			}
			limit.RequiresAnyOf = append(limit.RequiresAnyOf, required)
		}
		capability.ReferenceCapabilities = append(capability.ReferenceCapabilities, limit)
		if role.MediaKind() != domain.MediaKindImage {
			capability.MaximumTotalTimedReferenceDurationMilliseconds += limit.MaximumTotalDurationMilliseconds
		}
	}
	return capability, nil
}

func legacyVideoModelCapability(model Model) (projectapp.GenerationCapability, error) {
	var modes []domain.ClipGenerationMode
	var references []projectapp.GenerationReferenceCapability
	images := []string{"image/jpeg", "image/png", "image/webp", "image/heic", "image/heif"}
	switch model.ID {
	case "h3-fl2va":
		modes = []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage}
		references = []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceFirstImage, GenerationModes: modes, MinimumCount: 1, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceLastImage, GenerationModes: []domain.ClipGenerationMode{domain.ClipGenerationFirstLastImage}, MinimumCount: 1, MaximumCount: 1, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
		}
	case "h3-ref2va":
		modes = []domain.ClipGenerationMode{domain.ClipGenerationReference}
		references = []projectapp.GenerationReferenceCapability{
			{Role: domain.GenerationReferenceImage, GenerationModes: modes, MaximumCount: 9, MaximumBytes: 30 << 20, AcceptedMIMETypes: images},
			{Role: domain.GenerationReferenceVideo, GenerationModes: modes, MaximumCount: 3, MaximumBytes: 50 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}},
			{Role: domain.GenerationReferenceAudio, GenerationModes: modes, MaximumCount: 3, MaximumBytes: 15 << 20, MinimumDurationMilliseconds: 2_000, MaximumDurationMilliseconds: 15_000, MaximumTotalDurationMilliseconds: 15_000, AcceptedMIMETypes: []string{"audio/wav", "audio/x-wav", "audio/mpeg"}},
		}
	default:
		return projectapp.GenerationCapability{}, errors.New("cloud video model capabilities are unavailable")
	}
	return projectapp.GenerationCapability{
		GeneratorID: domain.GeneratorVideoH3ShotForward, ProviderModelID: model.ID, ProviderConnectionRevision: 1,
		CapabilityVersion: cloudH3CapabilityVersion, MediaKind: domain.MediaKindVideo, SupportedVideoModes: modes,
		MinimumDurationSeconds: 4, MaximumDurationSeconds: 15, DurationStepSeconds: 1,
		SupportedOutputSizes: []string{"720x1280", "1280x720", "1024x1024"}, ReferenceCapabilities: references,
		MinimumTotalReferences: 1, MaximumTotalReferences: 12, MaximumTotalTimedReferenceDurationMilliseconds: 30_000,
		SupportsStop: true, SupportsRecovery: true,
	}, nil
}

func cloudInputRole(value string) (domain.GenerationArtifactRole, bool) {
	switch value {
	case "first_image":
		return domain.GenerationReferenceFirstImage, true
	case "last_image":
		return domain.GenerationReferenceLastImage, true
	case "reference_image":
		return domain.GenerationReferenceImage, true
	case "reference_video":
		return domain.GenerationReferenceVideo, true
	case "reference_audio":
		return domain.GenerationReferenceAudio, true
	default:
		return "", false
	}
}

func cloudModelProfile(model Model) providerconfig.ModelProfile {
	health, failureCode := providerconfig.HealthReady, ""
	if !modelAvailable(model) {
		health, failureCode = providerconfig.HealthUnavailable, "model_unavailable"
	}
	return providerconfig.ModelProfile{
		ModelID: model.ID, DisplayName: model.DisplayName, Availability: model.Availability, Family: model.Family,
		Variant: model.Variant, SortOrder: model.SortOrder, Deprecated: model.Deprecated, ReplacementModelID: model.ReplacementModelID,
		IsDefault: model.IsDefault, CapabilityVersion: model.CapabilityVersion, Capabilities: model.Capabilities,
		Health: health, FailureCode: failureCode,
	}
}
