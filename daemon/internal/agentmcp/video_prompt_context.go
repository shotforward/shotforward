package agentmcp

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// VideoPromptContext is the UI-selected video model context captured when a
// Turn is submitted. It is not supplied by model tool arguments.
type VideoPromptContext struct {
	GeneratorID             string                  `json:"generator_id"`
	ProviderModelID         string                  `json:"provider_model_id"`
	KeyframeProviderModelID string                  `json:"keyframe_provider_model_id,omitempty"`
	Focus                   *VideoPromptFocusTarget `json:"focus,omitempty"`
}

type VideoPromptFocusTarget struct {
	ClipID          project.ClipID             `json:"clip_id"`
	GenerationMode  project.ClipGenerationMode `json:"generation_mode"`
	GeneratorID     string                     `json:"generator_id"`
	ProviderModelID string                     `json:"provider_model_id"`
}

func (c *VideoPromptContext) Validate() error {
	if c == nil {
		return nil
	}
	if !validVideoPromptContextGenerator(c.GeneratorID) {
		return errors.New("invalid video Prompt generator")
	}
	if _, err := deriveVideoPromptContextTarget(c.GeneratorID, c.ProviderModelID, project.ClipGenerationReference); err != nil {
		return err
	}
	for _, value := range []string{c.GeneratorID, c.ProviderModelID, c.KeyframeProviderModelID} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid video Prompt model context")
		}
	}
	if c.KeyframeProviderModelID != "" {
		if _, err := deriveVideoPromptContextTarget(c.GeneratorID, c.KeyframeProviderModelID, project.ClipGenerationFirstLastImage); err != nil {
			return err
		}
	}
	if c.Focus != nil {
		if c.Focus.ClipID.Validate() != nil || !validVideoPromptContextGenerator(c.Focus.GeneratorID) {
			return errors.New("invalid focused video Prompt Clip")
		}
		if _, err := deriveVideoPromptContextTarget(c.Focus.GeneratorID, c.Focus.ProviderModelID, c.Focus.GenerationMode); err != nil {
			return err
		}
		if !utf8.ValidString(c.Focus.ProviderModelID) || strings.TrimSpace(c.Focus.GeneratorID) != c.Focus.GeneratorID || strings.TrimSpace(c.Focus.ProviderModelID) != c.Focus.ProviderModelID || strings.ContainsAny(c.Focus.GeneratorID+c.Focus.ProviderModelID, "\x00\r\n") {
			return errors.New("invalid focused video Prompt target")
		}
	}
	return nil
}

func (c *VideoPromptContext) selected(clipID project.ClipID, mode project.ClipGenerationMode) (videoPromptTarget, error) {
	if c == nil {
		return videoPromptTarget{}, errors.New("video Prompt model context is missing")
	}
	if c.Focus != nil && c.Focus.ClipID == clipID && c.Focus.GenerationMode == mode {
		return deriveVideoPromptContextTarget(c.Focus.GeneratorID, c.Focus.ProviderModelID, mode)
	}
	modelID := c.ProviderModelID
	if mode != project.ClipGenerationReference {
		modelID = c.KeyframeProviderModelID
		if modelID == "" && !strings.HasPrefix(c.GeneratorID, "video.h3.") {
			modelID = c.ProviderModelID
		}
	}
	return deriveVideoPromptContextTarget(c.GeneratorID, modelID, mode)
}

func validVideoPromptContextGenerator(value string) bool {
	if project.GeneratorID(value).Validate() != nil || project.GeneratorID(value).MediaKind() != project.MediaKindVideo {
		return false
	}
	switch project.GeneratorID(value) {
	case project.GeneratorVideoH3China, project.GeneratorVideoH3Official, project.GeneratorVideoH3ShotForward,
		project.GeneratorVideoSeedanceChina, project.GeneratorVideoSeedanceGlobal, project.GeneratorVideoWanOfficial:
		return true
	default:
		return false
	}
}

func deriveVideoPromptContextTarget(generatorID, modelID string, mode project.ClipGenerationMode) (videoPromptTarget, error) {
	target, err := deriveVideoPromptTarget(generatorID, modelID, mode)
	if err != nil {
		return videoPromptTarget{}, err
	}
	model := strings.ToLower(modelID)
	if target.Family == "h3" && (strings.Contains(model, "ref2va") && mode != project.ClipGenerationReference || strings.Contains(model, "fl2va") && mode == project.ClipGenerationReference) {
		return videoPromptTarget{}, errors.New("the H3 model is incompatible with this generation mode")
	}
	return target, nil
}

// VideoPromptContextInstructionPreamble identifies the private model context
// added by ShotForward. Conversation display must only unwrap it when the
// persisted original user input also matches, never from this marker alone.
const VideoPromptContextInstructionPreamble = "ShotForward-selected video Prompt context: apply these model rules to every video Prompt creation, rewrite, split, and merge. They do not apply to Asset or first/last still-image Prompts. Preserve dialogue in its original language; keep exact @reference names for deterministic provider-label compilation."

// InstructionText exposes the same selected targets and editable Prompt rules
// through the Runtime request and the MCP read context.
func (c *VideoPromptContext) InstructionText() string {
	if c == nil {
		return ""
	}
	var lines []string
	lines = append(lines, VideoPromptContextInstructionPreamble)
	for _, mode := range []project.ClipGenerationMode{project.ClipGenerationReference, project.ClipGenerationFirstImage, project.ClipGenerationFirstLastImage} {
		target, err := c.selected("", mode)
		if err == nil {
			lines = append(lines, fmt.Sprintf("Default %s: generator_id=%s; provider_model_id=%s; prompt_profile=%s. %s", mode, target.GeneratorID, target.ProviderModelID, target.Profile, videoPromptFormatGuidance(target)))
		}
	}
	if c.Focus != nil {
		target, err := c.selected(c.Focus.ClipID, c.Focus.GenerationMode)
		if err == nil {
			lines = append(lines, fmt.Sprintf("Focused Clip %s, %s overrides the default: generator_id=%s; provider_model_id=%s; prompt_profile=%s. %s", c.Focus.ClipID, c.Focus.GenerationMode, target.GeneratorID, target.ProviderModelID, target.Profile, videoPromptFormatGuidance(target)))
		}
	}
	return strings.Join(lines, "\n")
}

func videoPromptFormatGuidance(target videoPromptTarget) string {
	switch target.Profile {
	case "h3_ref2va@1":
		return "Use six sections in order: subject_definitions, summary, retention_analysis, detailed_description, overall_soundscape, non_diegetic_music. Write section bodies in the user's requested language, otherwise keep the current creative language. Keep field names and fixed format values unchanged, retain <Subject N>, and use exact @media names instead of inventing <Picture>/<Video>/<Audio> labels. Do not timestamp Shot 1; use Clip-local timestamps only for later shots. Preserve original-language dialogue inside <d>[Language] ...</d>, lyrics inside <lyrics>...</lyrics>, and visible text inside <on_screen_text>...</on_screen_text>. Submission prepares an English copy without overwriting the editable Prompt."
	case "h3_base@1":
		return "Use integrated_multimodal_description, overall_soundscape, non_diegetic_music in order. Write bodies in the user's requested language, otherwise keep the current creative language; keep field names and fixed format values unchanged. Do not timestamp Shot 1; later shots may use [Shot N] At 00:SS.mmm. Preserve original-language dialogue inside <d>[Language] ...</d>, lyrics inside <lyrics>...</lyrics>, and visible text inside <on_screen_text>...</on_screen_text>. Submission prepares an English copy without overwriting the editable Prompt."
	case "seedance@1":
		return "Use numbered Shot N sections with coherent action, camera, sound, and optional Clip-local time ranges. Preserve every exact @reference name at its semantic position; provider labels are compiled at submission."
	case "wan3@1":
		return "Organize the Prompt into overall description, references and purposes, continuous shot timeline, dialogue, sound/music, visual style, and necessary negative requirements. Keep exact @reference names; provider labels are compiled at submission."
	default:
		return "Write a coherent, model-aware, directly editable video Prompt."
	}
}
