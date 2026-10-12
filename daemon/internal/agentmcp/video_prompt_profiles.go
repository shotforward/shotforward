package agentmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const videoPromptSetVersion = 1

type videoPromptVariant struct {
	Prompt          string                 `json:"prompt"`
	Profile         string                 `json:"profile"`
	GeneratorID     string                 `json:"generator_id"`
	ProviderModelID string                 `json:"provider_model_id"`
	AssetIDs        []string               `json:"asset_ids,omitempty"`
	References      []videoPromptReference `json:"references,omitempty"`
}

type videoPromptReference struct {
	Key      string   `json:"key"`
	Purposes []string `json:"purposes"`
	Note     string   `json:"note,omitempty"`
}

type videoPromptSet struct {
	Version       int                           `json:"v"`
	PrimaryFamily string                        `json:"primary_family,omitempty"`
	LegacyPrompt  string                        `json:"legacy_prompt,omitempty"`
	Families      map[string]videoPromptVariant `json:"families"`
}

type videoPromptTarget struct {
	Family          string
	Profile         string
	GeneratorID     string
	ProviderModelID string
}

func deriveVideoPromptTarget(generatorID, providerModelID string, mode project.ClipGenerationMode) (videoPromptTarget, error) {
	if !mode.IsValid() || strings.TrimSpace(providerModelID) == "" || len(providerModelID) > 200 {
		return videoPromptTarget{}, errors.New("invalid video prompt target")
	}
	var family string
	switch {
	case strings.HasPrefix(generatorID, "video.h3."):
		family = "h3"
	case strings.Contains(generatorID, "seedance"):
		family = "seedance"
	case strings.Contains(generatorID, "wan-3.0"):
		family = "wan3"
	default:
		return videoPromptTarget{}, errors.New("unsupported video prompt generator")
	}
	profile := family + "@1"
	if family == "h3" {
		profile = "h3_base@1"
		if mode == project.ClipGenerationReference {
			profile = "h3_ref2va@1"
		}
	}
	return videoPromptTarget{Family: family, Profile: profile, GeneratorID: generatorID, ProviderModelID: providerModelID}, nil
}

func parseVideoPromptSet(value string) videoPromptSet {
	result := videoPromptSet{Version: videoPromptSetVersion, Families: map[string]videoPromptVariant{}}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return result
	}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil || result.Version != videoPromptSetVersion || result.Families == nil {
		return videoPromptSet{Version: videoPromptSetVersion, LegacyPrompt: value, Families: map[string]videoPromptVariant{}}
	}
	for family := range result.Families {
		if family != "h3" && family != "seedance" && family != "wan3" {
			delete(result.Families, family)
			continue
		}
		variant := result.Families[family]
		if len(variant.References) == 0 && len(variant.AssetIDs) > 0 {
			variant.References = make([]videoPromptReference, 0, len(variant.AssetIDs))
			for _, assetID := range variant.AssetIDs {
				variant.References = append(variant.References, videoPromptReference{Key: "asset:" + assetID})
			}
			result.Families[family] = variant
		}
	}
	return result
}

func (set videoPromptSet) sourcePrompt() (string, string) {
	if variant, ok := set.Families[set.PrimaryFamily]; ok && strings.TrimSpace(variant.Prompt) != "" {
		return set.PrimaryFamily, variant.Prompt
	}
	if strings.TrimSpace(set.LegacyPrompt) != "" {
		return "legacy", set.LegacyPrompt
	}
	for _, family := range []string{"h3", "seedance", "wan3"} {
		if variant, ok := set.Families[family]; ok && strings.TrimSpace(variant.Prompt) != "" {
			return family, variant.Prompt
		}
	}
	return "legacy", ""
}

func (set videoPromptSet) promptForSource(family string) string {
	if family == "legacy" {
		return set.LegacyPrompt
	}
	return set.Families[family].Prompt
}

func (set videoPromptSet) withVariant(target videoPromptTarget, generatorID, providerModelID, prompt string, references []videoPromptReference) (string, error) {
	if set.Families == nil {
		set.Families = map[string]videoPromptVariant{}
	}
	if set.PrimaryFamily == "" {
		set.PrimaryFamily = target.Family
	}
	set.Version = videoPromptSetVersion
	assetIDs := make([]string, 0, len(references))
	for _, reference := range references {
		if strings.HasPrefix(reference.Key, "asset:") {
			assetIDs = append(assetIDs, strings.TrimPrefix(reference.Key, "asset:"))
		}
	}
	set.Families[target.Family] = videoPromptVariant{
		Prompt: strings.TrimSpace(project.NormalizeStoryboardText(prompt)), Profile: target.Profile,
		GeneratorID: generatorID, ProviderModelID: providerModelID, AssetIDs: assetIDs,
		References: append([]videoPromptReference(nil), references...),
	}
	encoded, err := json.Marshal(set)
	if err != nil || utf8.RuneCount(encoded) > project.MaxStoryboardTextCharacters {
		return "", errors.New("video prompt variants exceed the Clip storage limit")
	}
	return string(encoded), nil
}

func videoPromptHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
