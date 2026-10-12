package providerconfig

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestModelCapabilitiesJSONUsesArraysForRequiredCollections(t *testing.T) {
	t.Parallel()
	for _, catalog := range []string{
		`{}`,
		`{"generation_modes":null,"output":{"sizes":null},"inputs":null}`,
		`{"generation_modes":[],"output":{"sizes":[]},"inputs":[]}`,
	} {
		t.Run(catalog, func(t *testing.T) {
			var capabilities ModelCapabilities
			if err := json.Unmarshal([]byte(catalog), &capabilities); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(capabilities)
			if err != nil {
				t.Fatal(err)
			}
			var projected struct {
				GenerationModes json.RawMessage `json:"generation_modes"`
				Output          struct {
					Sizes json.RawMessage `json:"sizes"`
				} `json:"output"`
				Inputs json.RawMessage `json:"inputs"`
			}
			if err := json.Unmarshal(encoded, &projected); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]json.RawMessage{
				"generation_modes": projected.GenerationModes,
				"output.sizes":     projected.Output.Sizes,
				"inputs":           projected.Inputs,
			} {
				if string(value) != "[]" {
					t.Errorf("%s = %s, want []", name, value)
				}
			}
		})
	}
}

func TestModelCapabilitiesJSONNormalizesNestedCollectionsWithoutMutatingCatalog(t *testing.T) {
	t.Parallel()
	capabilities := ModelCapabilities{
		Workflow: "image",
		Inputs:   []ModelInputCapability{{Role: "reference_image", MaximumCount: 5}},
	}
	encoded, err := json.Marshal(&capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var projected struct {
		Inputs []struct {
			GenerationModes   json.RawMessage `json:"generation_modes"`
			AcceptedMIMETypes json.RawMessage `json:"accepted_mime_types"`
			RequiresAnyOf     json.RawMessage `json:"requires_any_of"`
			MaximumCount      int             `json:"maximum_count"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected.Inputs) != 1 || string(projected.Inputs[0].GenerationModes) != "[]" || string(projected.Inputs[0].AcceptedMIMETypes) != "[]" || len(projected.Inputs[0].RequiresAnyOf) != 0 || projected.Inputs[0].MaximumCount != 5 {
		t.Fatalf("nested projection = %s", encoded)
	}
	if capabilities.GenerationModes != nil || capabilities.Output.Sizes != nil || capabilities.Inputs[0].GenerationModes != nil || capabilities.Inputs[0].AcceptedMIMETypes != nil {
		t.Fatalf("marshaling mutated the source catalog: %+v", capabilities)
	}
}

func TestModelCapabilitiesJSONPreservesPublishedCapabilities(t *testing.T) {
	t.Parallel()
	capabilities := ModelCapabilities{
		Workflow: "ref2va", GenerationModes: []string{"reference"},
		Output: ModelOutput{DefaultSize: "720x1280", Sizes: []string{"720x1280"}},
		Inputs: []ModelInputCapability{{
			Role: "reference_audio", GenerationModes: []string{"reference"}, MaximumCount: 3,
			AcceptedMIMETypes: []string{"audio/wav"}, RequiresAnyOf: []string{"reference_image", "reference_video"},
		}},
		MaximumTotalInputs: 12,
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ModelCapabilities
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, capabilities) {
		t.Fatalf("catalog changed after projection: got %+v, want %+v", decoded, capabilities)
	}
}
