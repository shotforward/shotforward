package shotforwardcloud

import (
	"context"
	"errors"
	"strings"
	"time"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ProviderService exposes account-managed cloud providers beside editable BYOK
// providers. Cloud profiles are read-only and derive their models from the
// authenticated server catalog.
type ProviderService struct {
	Base interface {
		Profiles(context.Context) ([]providerconfig.Profile, error)
		Profile(context.Context, domain.GeneratorID) (providerconfig.Profile, error)
		Configure(context.Context, providerconfig.ConfigureInput) (providerconfig.Profile, error)
		ValidateAndConfigure(context.Context, providerconfig.ConfigureInput) (providerconfig.Profile, error)
		Check(context.Context, domain.GeneratorID) (providerconfig.Profile, error)
		Delete(context.Context, providerconfig.DeleteInput) error
	}
	Manager *Manager
}

func (s ProviderService) Profiles(ctx context.Context) ([]providerconfig.Profile, error) {
	profiles, err := s.Base.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	return append(s.cloudProfiles(ctx), profiles...), nil
}
func (s ProviderService) Profile(ctx context.Context, id domain.GeneratorID) (providerconfig.Profile, error) {
	if !cloudGenerator(id) {
		return s.Base.Profile(ctx, id)
	}
	for _, profile := range s.cloudProfiles(ctx) {
		if profile.GeneratorID == id {
			return profile, nil
		}
	}
	return providerconfig.Profile{}, providerconfig.ErrNotFound
}
func (s ProviderService) Check(ctx context.Context, id domain.GeneratorID) (providerconfig.Profile, error) {
	if !cloudGenerator(id) {
		return s.Base.Check(ctx, id)
	}
	profile, err := s.Profile(ctx, id)
	if err != nil {
		return providerconfig.Profile{}, err
	}
	if profile.Health != providerconfig.HealthReady {
		return profile, providerconfig.ErrProviderUnavailable
	}
	return profile, nil
}
func (s ProviderService) Configure(ctx context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	if cloudGenerator(input.GeneratorID) {
		return providerconfig.Profile{}, providerconfig.ErrInvalidRequest
	}
	return s.Base.Configure(ctx, input)
}
func (s ProviderService) ValidateAndConfigure(ctx context.Context, input providerconfig.ConfigureInput) (providerconfig.Profile, error) {
	if cloudGenerator(input.GeneratorID) {
		return providerconfig.Profile{}, providerconfig.ErrInvalidRequest
	}
	return s.Base.ValidateAndConfigure(ctx, input)
}
func (s ProviderService) Delete(ctx context.Context, input providerconfig.DeleteInput) error {
	if cloudGenerator(input.GeneratorID) {
		return providerconfig.ErrInvalidRequest
	}
	return s.Base.Delete(ctx, input)
}

func (s ProviderService) cloudProfiles(ctx context.Context) []providerconfig.Profile {
	now := time.Now().UTC()
	image := providerconfig.Profile{GeneratorID: domain.GeneratorImageShotForward, EndpointPreset: "shotforward_cloud", Enabled: true, ConcurrencyLimit: 5, Health: providerconfig.HealthUnavailable, Revision: 1, CreatedAt: now, UpdatedAt: now}
	video := providerconfig.Profile{GeneratorID: domain.GeneratorVideoH3ShotForward, EndpointPreset: "shotforward_cloud", Enabled: true, ConcurrencyLimit: 5, Health: providerconfig.HealthUnavailable, Revision: 1, CreatedAt: now, UpdatedAt: now}
	models, err := s.Manager.Models(ctx)
	if err != nil {
		if !errors.Is(err, ErrLoginRequired) {
			image.FailureCode = "cloud_unavailable"
			video.FailureCode = "cloud_unavailable"
		}
		return []providerconfig.Profile{image, video}
	}
	sortCloudModels(models)
	for _, model := range models {
		target := &image
		capabilityVersion := cloudImageCapabilityVersion
		if model.ProductKind == "video" {
			if _, capabilityErr := videoModelCapability(model); capabilityErr != nil {
				continue
			}
			target = &video
			capabilityVersion = model.CapabilityVersion
			if capabilityVersion == "" {
				capabilityVersion = cloudH3CapabilityVersion
			}
		} else if model.ProductKind != "image" {
			continue
		}
		if strings.TrimSpace(model.ID) == "" || !modelAvailable(model) {
			continue
		}
		profile := cloudModelProfile(model)
		profile.CapabilityVersion = capabilityVersion
		target.Models = append(target.Models, profile)
	}
	for _, target := range []*providerconfig.Profile{&image, &video} {
		if len(target.Models) > 0 {
			target.Health = providerconfig.HealthReady
			target.ModelID = target.Models[0].ModelID
			target.CapabilityVersion = target.Models[0].CapabilityVersion
		}
	}
	return []providerconfig.Profile{image, video}
}

func cloudGenerator(id domain.GeneratorID) bool {
	return id == domain.GeneratorImageShotForward || id == domain.GeneratorVideoH3ShotForward
}
