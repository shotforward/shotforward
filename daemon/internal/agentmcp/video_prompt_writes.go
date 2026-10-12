package agentmcp

import project "github.com/shotforward/shotforward/daemon/internal/domain/project"

func resolveVideoPromptWriteTarget(state bindingSnapshot, clipID project.ClipID, mode project.ClipGenerationMode, generatorID, providerModelID, current string) (videoPromptTarget, error) {
	if !mode.IsValid() || (generatorID == "") != (providerModelID == "") {
		return videoPromptTarget{}, toolError("invalid_arguments", "video Prompt mode and model target are invalid")
	}
	if state.turn.VideoPromptTarget != nil {
		selected, err := state.turn.VideoPromptTarget.selected(clipID, mode)
		if err != nil {
			return videoPromptTarget{}, toolError("video_prompt_target_missing", "the selected video model has no compatible target for this mode; select a mode-compatible model and submit again")
		}
		if generatorID != "" && (selected.GeneratorID != generatorID || selected.ProviderModelID != providerModelID) {
			return videoPromptTarget{}, toolError("video_prompt_target_conflict", "use the video model selected for this Turn and mode, as returned by get_episode_context or get_clip")
		}
		return selected, nil
	}
	if generatorID != "" {
		target, err := deriveVideoPromptContextTarget(generatorID, providerModelID, mode)
		if err != nil {
			return videoPromptTarget{}, toolError("invalid_arguments", "video Prompt model target is unsupported or incompatible")
		}
		return target, nil
	}
	set := parseVideoPromptSet(current)
	family, _ := set.sourcePrompt()
	if variant, ok := set.Families[family]; ok {
		if target, err := deriveVideoPromptContextTarget(variant.GeneratorID, variant.ProviderModelID, mode); err == nil {
			return target, nil
		}
	}
	return videoPromptTarget{}, toolError("video_prompt_target_missing", "video Prompt model context is missing; read the selected model target with get_episode_context or get_clip, or provide its exact generator_id and mode-compatible provider_model_id")
}

// storeModelVideoPrompt is the common write path for ordinary Agent creation,
// rewriting, splitting, and merging. It never overwrites another family or
// mode, and retains the reference metadata for unchanged bindings.
func storeModelVideoPrompt(current, prompt string, target videoPromptTarget, mode project.ClipGenerationMode, assetIDs []project.AssetID, uploadIDs []project.ArtifactID) (string, error) {
	set := parseVideoPromptSet(current)
	set.PrimaryFamily = target.Family
	references := []videoPromptReference{}
	if mode == project.ClipGenerationReference {
		oldByKey := make(map[string]videoPromptReference)
		for _, reference := range set.Families[target.Family].References {
			oldByKey[reference.Key] = reference
		}
		keys := make([]string, 0, len(assetIDs)+len(uploadIDs))
		for _, id := range assetIDs {
			keys = append(keys, "asset:"+id.String())
		}
		for _, id := range uploadIDs {
			keys = append(keys, "upload:"+id.String())
		}
		for _, key := range keys {
			reference, ok := oldByKey[key]
			if !ok || len(reference.Purposes) == 0 {
				reference = videoPromptReference{Key: key, Purposes: []string{"other"}}
			}
			references = append(references, reference)
		}
	}
	stored, err := set.withVariant(target, target.GeneratorID, target.ProviderModelID, prompt, references)
	if err != nil {
		return "", toolError("invalid_arguments", err.Error())
	}
	return stored, nil
}

func videoPromptTargetContext(state bindingSnapshot, clipID project.ClipID) map[string]any {
	result := make(map[string]any)
	if state.turn.VideoPromptTarget == nil {
		return result
	}
	for _, mode := range []project.ClipGenerationMode{project.ClipGenerationReference, project.ClipGenerationFirstImage, project.ClipGenerationFirstLastImage} {
		target, err := state.turn.VideoPromptTarget.selected(clipID, mode)
		if err != nil {
			continue
		}
		result[string(mode)] = map[string]any{
			"generator_id": target.GeneratorID, "provider_model_id": target.ProviderModelID,
			"prompt_family": target.Family, "prompt_profile": target.Profile,
			"instructions": videoPromptFormatGuidance(target),
		}
	}
	return result
}

func videoPromptTargetContextForClip(state bindingSnapshot, clip project.Clip) map[string]any {
	result := videoPromptTargetContext(state, clip.ID)
	if state.turn.VideoPromptTarget != nil {
		return result
	}
	for _, mode := range []project.ClipGenerationMode{project.ClipGenerationReference, project.ClipGenerationFirstImage, project.ClipGenerationFirstLastImage} {
		target, err := resolveVideoPromptWriteTarget(state, clip.ID, mode, "", "", clip.VideoPrompts.ForMode(mode))
		if err == nil {
			result[string(mode)] = map[string]any{
				"generator_id": target.GeneratorID, "provider_model_id": target.ProviderModelID,
				"prompt_family": target.Family, "prompt_profile": target.Profile,
				"instructions": videoPromptFormatGuidance(target),
			}
		}
	}
	return result
}

func cloneVideoPromptContext(value *VideoPromptContext) *VideoPromptContext {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Focus != nil {
		focus := *value.Focus
		copy.Focus = &focus
	}
	return &copy
}

func validateVideoPromptModelFields(generatorID, providerModelID string, hasVideo bool) bool {
	return hasVideo || generatorID == "" && providerModelID == ""
}
