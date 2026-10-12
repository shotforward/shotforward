package shotforwardcloud

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestRealH3CatalogMatchesWorkerDurationAndWorkflow(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/auth/desktop-code/exchanges" {
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "test", RefreshToken: "refresh-test", ExpiresIn: 900}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"data": []Model{
			{ID: "h3-video", ProductKind: "video"},
			cloudVideoModel("MiniMax-H3-Ref2VA", "ref2va", []string{"reference"}, 20),
			cloudVideoModel("MiniMax-H3-FL2VA", "fl2va", []string{"first_image", "first_last_image"}, 10),
		}}), nil
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.ExchangeDesktopCode(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		model string
		modes []domain.ClipGenerationMode
		roles []domain.GenerationArtifactRole
	}{
		{"MiniMax-H3-Ref2VA", []domain.ClipGenerationMode{domain.ClipGenerationReference}, []domain.GenerationArtifactRole{domain.GenerationReferenceImage, domain.GenerationReferenceVideo, domain.GenerationReferenceAudio}},
		{"MiniMax-H3-FL2VA", []domain.ClipGenerationMode{domain.ClipGenerationFirstImage, domain.ClipGenerationFirstLastImage}, []domain.GenerationArtifactRole{domain.GenerationReferenceFirstImage, domain.GenerationReferenceLastImage}},
	} {
		cap, err := (Catalog{Manager: manager}).GeneratorModel(ctx, domain.GeneratorVideoH3ShotForward, test.model)
		if err != nil {
			t.Fatal(err)
		}
		if len(cap.SupportedDurations) != 0 || cap.MinimumDurationSeconds != 4 || cap.MaximumDurationSeconds != 15 || cap.DurationStepSeconds != 1 || !reflect.DeepEqual(cap.SupportedVideoModes, test.modes) {
			t.Fatalf("unexpected capability for %s: %+v", test.model, cap)
		}
		var roles []domain.GenerationArtifactRole
		for _, ref := range cap.ReferenceCapabilities {
			roles = append(roles, ref.Role)
		}
		if !reflect.DeepEqual(roles, test.roles) {
			t.Fatalf("wrong reference roles for %s: %v", test.model, roles)
		}
		if cap.CapabilityVersion != "catalog-v1" || !reflect.DeepEqual(cap.SupportedOutputSizes, []string{"720x1280", "1280x720", "1024x1024"}) {
			t.Fatalf("catalog metadata for %s = %+v", test.model, cap)
		}
	}
	profiles := (ProviderService{Manager: manager}).cloudProfiles(ctx)
	var videoModels []providerconfig.ModelProfile
	for _, profile := range profiles {
		if profile.GeneratorID == domain.GeneratorVideoH3ShotForward {
			videoModels = profile.Models
		}
		for _, model := range profile.Models {
			if model.ModelID == "h3-video" {
				t.Fatal("retired mock exposed")
			}
		}
	}
	if len(videoModels) != 2 || videoModels[0].ModelID != "MiniMax-H3-FL2VA" || videoModels[1].ModelID != "MiniMax-H3-Ref2VA" || videoModels[0].Capabilities == nil || videoModels[0].DisplayName == "" {
		t.Fatalf("dynamic video profile models=%#v", videoModels)
	}
}

func cloudVideoModel(id, workflow string, modes []string, sortOrder int) Model {
	inputs := []providerconfig.ModelInputCapability{
		{Role: "first_image", GenerationModes: []string{"first_image", "first_last_image"}, Required: true, MinimumCount: 1, MaximumCount: 1, MaximumBytes: 256 << 20, AcceptedMIMETypes: []string{"image/jpeg", "image/png", "image/webp"}},
		{Role: "last_image", GenerationModes: []string{"first_last_image"}, Required: true, MinimumCount: 1, MaximumCount: 1, MaximumBytes: 256 << 20, AcceptedMIMETypes: []string{"image/jpeg", "image/png", "image/webp"}},
	}
	minimumTotal, maximumTotal := 1, 2
	if workflow == "ref2va" {
		inputs = []providerconfig.ModelInputCapability{
			{Role: "reference_image", GenerationModes: []string{"reference"}, MaximumCount: 9, MaximumBytes: 256 << 20, AcceptedMIMETypes: []string{"image/jpeg", "image/png", "image/webp"}},
			{Role: "reference_video", GenerationModes: []string{"reference"}, MaximumCount: 3, MaximumBytes: 512 << 20, MinimumSeconds: 2, MaximumSeconds: 15, MaximumTotalSeconds: 15, AcceptedMIMETypes: []string{"video/mp4", "video/quicktime"}},
			{Role: "reference_audio", GenerationModes: []string{"reference"}, MaximumCount: 3, MaximumBytes: 256 << 20, MinimumSeconds: 2, MaximumSeconds: 15, MaximumTotalSeconds: 15, AcceptedMIMETypes: []string{"audio/wav", "audio/mpeg"}, RequiresAnyOf: []string{"reference_image", "reference_video"}},
		}
		maximumTotal = 12
	}
	return Model{ID: id, DisplayName: id, ProductKind: "video", Availability: "available", CapabilityVersion: "catalog-v1", SortOrder: sortOrder, Capabilities: &providerconfig.ModelCapabilities{
		Workflow: workflow, GenerationModes: modes, Duration: providerconfig.ModelDuration{MinimumSeconds: 4, MaximumSeconds: 15, StepSeconds: 1, DefaultSeconds: 5},
		Output: providerconfig.ModelOutput{DefaultSize: "720x1280", Sizes: []string{"720x1280", "1280x720", "1024x1024"}}, Inputs: inputs,
		MinimumTotalInputs: minimumTotal, MaximumTotalInputs: maximumTotal, SupportsStop: true, SupportsRecovery: true,
	}}
}
