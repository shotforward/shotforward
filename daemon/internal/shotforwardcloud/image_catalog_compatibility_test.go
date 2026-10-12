package shotforwardcloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	providerconfig "github.com/shotforward/shotforward/daemon/internal/application/providerconfig"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestImageCatalogCompletesOnlyMissingOrNullReferenceInputs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		inputsJSON string
		wantCount  int
		wantLimit  int
	}{
		{"missing", "", 1, 5},
		{"null", `,"inputs":null`, 1, 5},
		{"explicit empty", `,"inputs":[]`, 0, 0},
		{"published limit", `,"inputs":[{"role":"reference_image","maximum_count":2,"maximum_bytes":1024,"generation_modes":[],"accepted_mime_types":["image/png"]}]`, 1, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Raw JSON is intentional: missing, null and explicit [] must remain
			// distinguishable until the adapter compatibility rule runs.
			data := `{"schema_version":1,"data":[{"id":"image-model","product_kind":"image","capabilities":{"workflow":"image","output":{"default_size":"1024x1024","sizes":["1024x1024"]}` + test.inputsJSON + `}}]}`
			var catalog cloudModelCatalog
			if err := json.Unmarshal([]byte(data), &catalog); err != nil {
				t.Fatal(err)
			}
			source := catalog.Data[0].Capabilities
			models, err := catalog.models()
			if err != nil || len(models) != 1 || models[0].Capabilities == nil {
				t.Fatalf("models = %+v, err = %v", models, err)
			}
			capabilities := models[0].Capabilities
			if len(capabilities.Inputs) != test.wantCount || test.wantCount > 0 && capabilities.Inputs[0].MaximumCount != test.wantLimit {
				t.Fatalf("reference capabilities = %+v", capabilities)
			}
			if test.name == "missing" || test.name == "null" {
				if source.Inputs != nil || source == capabilities || capabilities.MaximumTotalInputs != 5 || capabilities.Inputs[0].MaximumBytes != 256<<20 {
					t.Fatalf("compatibility projection mutated source or lost adapter limits: source=%+v, projected=%+v", source, capabilities)
				}
			}
		})
	}
}

func TestImageCatalogCompatibilityAppliesToBothPublicModelProjections(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/auth/desktop-code/exchanges" {
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "test", RefreshToken: "refresh-test", ExpiresIn: 900}), nil
		}
		if request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected endpoint: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"schema_version":1,"data":[{"id":"image-model","product_kind":"image","capabilities":{"workflow":"image","generation_modes":null,"output":{"sizes":null},"inputs":null}}]}`,
		))}, nil
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.ExchangeDesktopCode(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	profiles := (ProviderService{Manager: manager}).cloudProfiles(ctx)
	imageModel := profiles[0].Models[0]
	if imageModel.Capabilities == nil || len(imageModel.Capabilities.Inputs) != 1 || imageModel.Capabilities.Inputs[0].MaximumCount != 5 {
		t.Fatalf("Provider projection lost reference support: %+v", imageModel)
	}
	adapter, err := (Catalog{Manager: manager}).GeneratorModel(ctx, domain.GeneratorImageShotForward, imageModel.ModelID)
	if err != nil || len(adapter.ReferenceCapabilities) != 1 || adapter.ReferenceCapabilities[0].MaximumCount != imageModel.Capabilities.Inputs[0].MaximumCount || adapter.ReferenceCapabilities[0].MaximumBytes != imageModel.Capabilities.Inputs[0].MaximumBytes {
		t.Fatalf("projection disagrees with installed image adapter: %+v, %v", adapter, err)
	}
	handler, err := NewHandler(manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/cloud/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"maximum_count":5`) {
		t.Fatalf("cloud models response = %d, %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{`"generation_modes":null`, `"inputs":null`, `"sizes":null`, `"accepted_mime_types":null`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("cloud models response leaked nil collection: %s", response.Body.String())
		}
	}
}

func TestImageCatalogCompatibilityDoesNotAlterVideoOrLegacyCapabilities(t *testing.T) {
	t.Parallel()
	video := &providerconfig.ModelCapabilities{Workflow: "ref2va", GenerationModes: []string{"reference"}, MaximumTotalInputs: 12}
	for _, model := range []Model{
		{ProductKind: "video", Capabilities: video},
		{ProductKind: "image"},
		{ProductKind: "image", Capabilities: &providerconfig.ModelCapabilities{Inputs: []providerconfig.ModelInputCapability{}}},
	} {
		if projected := completeImageModelCapabilities(model); projected != model.Capabilities || !reflect.DeepEqual(projected, model.Capabilities) {
			t.Fatalf("compatibility rule changed an authoritative or legacy catalog: %+v", model)
		}
	}
	if video.Inputs != nil || video.MaximumTotalInputs != 12 {
		t.Fatalf("video capability changed: %+v", video)
	}
}

func TestEmptyCloudCatalogModelsSerializeAsArray(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{}`, `{"data":null}`, `{"data":[]}`} {
		var catalog cloudModelCatalog
		if err := json.Unmarshal([]byte(data), &catalog); err != nil {
			t.Fatal(err)
		}
		models, err := catalog.models()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(models)
		if err != nil || string(encoded) != "[]" {
			t.Fatalf("empty model catalog = %s, err = %v", encoded, err)
		}
	}
}
