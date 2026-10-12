package agentmcp

import (
	"fmt"
	"testing"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestVideoPromptSetPreservesLegacyAndFamilies(t *testing.T) {
	set := parseVideoPromptSet("旧提示词")
	target, err := deriveVideoPromptTarget("video.wan-3.0.official", "wan3.0-video-prime", project.ClipGenerationReference)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := set.withVariant(target, "video.wan-3.0.official", "wan3.0-video-prime", "Wan 提示词", []videoPromptReference{{Key: "asset:ast_1", Purposes: []string{"identity"}}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseVideoPromptSet(encoded)
	if parsed.LegacyPrompt != "旧提示词" || parsed.Families["wan3"].Prompt != "Wan 提示词" || parsed.Families["wan3"].References[0].Key != "asset:ast_1" {
		t.Fatalf("unexpected prompt set: %+v", parsed)
	}
}

func TestH3WorkerProfileFollowsMode(t *testing.T) {
	target, err := deriveVideoPromptTarget("video.h3.shotforward", "MiniMax-H3-Turbo-Ref2VA", project.ClipGenerationReference)
	if err != nil || target.Profile != "h3_ref2va@1" {
		t.Fatalf("target = %+v, err = %v", target, err)
	}
	target, err = deriveVideoPromptTarget("video.h3.shotforward", "MiniMax-H3-Turbo-FL2VA", project.ClipGenerationFirstLastImage)
	if err != nil || target.Profile != "h3_base@1" {
		t.Fatalf("target = %+v, err = %v", target, err)
	}
}

func TestAdaptedVideoPromptValidationLeavesContentReviewToUser(t *testing.T) {
	h3 := videoPromptTarget{Family: "h3", Profile: "h3_ref2va@1"}
	valid := "subject_definitions:\n<Subject 1>: person\nsummary:\nA shot\nretention_analysis:\n<Picture 1>: fully_preserved\ndetailed_description:\nAction\noverall_soundscape:\nRoom tone\nnon_diegetic_music:\nNone"
	if _, err := validateAdaptedVideoPrompt(valid, h3, project.ClipGenerationReference, nil); err != nil {
		t.Fatalf("valid H3 Ref2VA Prompt failed: %v", err)
	}
	if _, err := validateAdaptedVideoPrompt("summary:\nmissing fields", h3, project.ClipGenerationReference, nil); err != nil {
		t.Fatalf("incomplete H3 Ref2VA Prompt should remain user-reviewable: %v", err)
	}
	if _, err := validateAdaptedVideoPrompt(valid+"\n@person", h3, project.ClipGenerationReference, []string{"person"}); err != nil {
		t.Fatalf("H3 reference syntax should remain user-reviewable: %v", err)
	}
	wan := videoPromptTarget{Family: "wan3", Profile: "wan3@1"}
	references := []string{"林黛玉", "走路参考"}
	if _, err := validateAdaptedVideoPrompt("镜头1：@林黛玉参考人物，按照@走路参考完成动作。", wan, project.ClipGenerationReference, references); err != nil {
		t.Fatalf("valid Wan Prompt failed: %v", err)
	}
	if _, err := validateAdaptedVideoPrompt("镜头1：@林黛玉走入庭院。", wan, project.ClipGenerationReference, references); err != nil {
		t.Fatalf("missing reference mention should remain user-reviewable: %v", err)
	}
	if _, err := validateAdaptedVideoPrompt("  ", wan, project.ClipGenerationReference, references); err == nil {
		t.Fatal("empty adapted Prompt must still fail")
	}
}

func TestAdaptationModelLabelsFollowRegionalProviderSyntax(t *testing.T) {
	global := videoPromptTarget{Family: "seedance", GeneratorID: string(project.GeneratorVideoSeedanceGlobal)}
	china := videoPromptTarget{Family: "seedance", GeneratorID: string(project.GeneratorVideoSeedanceChina)}
	wan := videoPromptTarget{Family: "wan3"}
	if got := adaptationModelLabel(global, string(project.MediaKindImage), 2); got != "@Image 2" {
		t.Fatalf("global Seedance label = %q", got)
	}
	if got := adaptationModelLabel(china, string(project.MediaKindVideo), 3); got != "@视频3" {
		t.Fatalf("China Seedance label = %q", got)
	}
	if got := adaptationModelLabel(wan, string(project.MediaKindAudio), 1); got != "音频1 / Audio 1" {
		t.Fatalf("Wan label = %q", got)
	}
	if !adaptationTargetSupportsMedia(wan, project.MediaKindAudio) {
		t.Fatal("Wan audio reference should be supported")
	}
}

func TestAdaptationReferencesRequireEveryReadyAssetAndUploadWithPurpose(t *testing.T) {
	target := videoPromptTarget{Family: "h3", Profile: "h3_ref2va@1"}
	available := []adaptationReference{
		{Key: "asset:ast_1", MediaType: string(project.MediaKindImage), MediaReady: true, TargetSupported: true},
		{Key: "upload:art_1", MediaType: string(project.MediaKindVideo), MediaReady: true, TargetSupported: true},
	}
	prompt := "<Picture 1> identity and <Video 1> motion"
	result, err := validateVideoPromptAdaptationReferences([]videoPromptReference{
		{Key: "asset:ast_1", Purposes: []string{"identity"}},
		{Key: "upload:art_1", Purposes: []string{"motion"}, Note: "Do not copy appearance"},
	}, available, target, project.ClipGenerationReference, prompt)
	if err != nil || len(result) != 2 || result[1].Note != "Do not copy appearance" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if _, err := validateVideoPromptAdaptationReferences(result[:1], available, target, project.ClipGenerationReference, prompt); err == nil {
		t.Fatal("expected incomplete uploaded-media manifest to fail")
	}
}

func TestAdaptationReferencesEnforceProviderMediaTypeLimits(t *testing.T) {
	target := videoPromptTarget{Family: "wan3", Profile: "wan3@1"}
	available := make([]adaptationReference, 11)
	for index := range available {
		available[index] = adaptationReference{Key: fmt.Sprintf("asset:ast_%d", index), MediaType: string(project.MediaKindImage), MediaReady: true, TargetSupported: true}
	}
	if _, err := validateVideoPromptAdaptationReferences(nil, available, target, project.ClipGenerationReference, "prompt"); err == nil {
		t.Fatal("expected Wan image reference limit to be enforced")
	}
}
