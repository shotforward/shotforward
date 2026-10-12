package agentmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shotforward/shotforward/daemon/internal/agentimage"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

const minimumGeneratedSceneAverageClipDurationSeconds int64 = 6

func objectSchema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func pageLimitSchema(maximum int) map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 0, "default": maximum,
		"description": fmt.Sprintf("Optional requested page size. Omit or use 0 for the default of %d. Larger values are capped at %d; follow next_cursor while has_more is true. Use a smaller limit if the response is too large.", maximum, maximum),
	}
}

// Callers reject negative values before normalizing. Cap before offset arithmetic
// so an oversized request cannot overflow or bypass the bounded response size.
func cappedPageSize(requested, maximum int) int {
	if requested == 0 || requested > maximum {
		return maximum
	}
	return requested
}

func toolDefinitions() []toolDefinition {
	id := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	stringValue := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false}
	draftOnly := map[string]any{"readOnlyHint": false, "destructiveHint": false}
	return []toolDefinition{
		{Name: "declare_turn_intent", Description: "Declare whether this Turn is discussion, clarification, or a persistent ShotForward project change. Call this before other project tools.", InputSchema: objectSchema(map[string]any{"intent": map[string]any{"type": "string", "enum": []string{"discussion", "clarification", "project_change"}}, "target": map[string]any{"type": "string", "enum": []string{"none", "script", "assets", "storyboard", "prompts"}}}, "intent", "target"), Annotations: readOnly},
		{Name: "get_session_context", Description: "Return the immutable Session, Project, and Episode scope for this MCP route.", InputSchema: objectSchema(map[string]any{}), Annotations: readOnly},
		{Name: "get_episode_context", Description: "Return current Episode metadata and script revision, without the full script.", InputSchema: objectSchema(map[string]any{}), Annotations: readOnly},
		{Name: "read_script", Description: "Read the complete current Episode script in bounded pages. Follow next_cursor until has_more is false before proposing extracted Assets.", InputSchema: objectSchema(map[string]any{"cursor": stringValue("Opaque cursor returned by the previous page.")}), Annotations: readOnly},
		{Name: "list_assets", Description: "List active Episode Assets without Prompt text. Use get_asset before proposing a Prompt change.", InputSchema: objectSchema(map[string]any{"cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxAssetPageSize)}), Annotations: readOnly},
		{Name: "list_reference_media", Description: "List active uploaded Episode reference media, including image, video, and audio Artifact IDs, names, descriptions, durations, and current Clip usage. For timed media, duration_ms is the selected range's effective duration; original_duration_ms and trim_start_ms/trim_end_ms describe the original source and the shared Episode selection used by all Clip previews and new generations. Read every page before changing a Clip's uploaded-media bindings.", InputSchema: objectSchema(map[string]any{"cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxReferenceMediaPageSize), "kind": map[string]any{"type": "string", "enum": []string{"image", "video", "audio"}}}), Annotations: readOnly},
		{Name: "get_asset", Description: "Read one active Asset including its current Prompt. The Asset must belong to this Session's Episode. For an earlier-Episode Asset returned by list_prior_asset_candidates, use get_prior_asset_candidate instead.", InputSchema: objectSchema(map[string]any{"asset_id": id("Asset ID.")}, "asset_id"), Annotations: readOnly},
		{Name: "list_deleted_assets", Description: "List soft-deleted Assets only when the user explicitly asks to inspect or restore recycle-bin content. Never use deleted Assets for duplicate detection before creation.", InputSchema: objectSchema(map[string]any{"cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxAssetPageSize)}), Annotations: readOnly},
		{Name: "get_deleted_asset", Description: "Read one soft-deleted Asset only for an explicit recycle-bin inspection or restoration request.", InputSchema: objectSchema(map[string]any{"asset_id": id("Deleted Asset ID.")}, "asset_id"), Annotations: readOnly},
		{Name: "list_prior_asset_candidates", Description: "List the newest active earlier-Episode Asset for each identity not already present in this Episode. Before creating extracted Assets, check for matching historical identities. This returns summaries only: call get_prior_asset_candidate for each match in this Turn, then append_asset_operations with source_asset_id to create an independent Episode copy. Do not recreate a matching identity by name.", InputSchema: objectSchema(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"character", "location", "prop", "style"}}, "name_query": stringValue("Optional case-insensitive identity name filter."), "cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxPriorAssetPageSize)}), Annotations: readOnly},
		{Name: "get_prior_asset_candidate", Description: "Read an active earlier-Episode Asset's full Prompt and record the source read for this Turn. Required before append_asset_operations with source_asset_id; list_prior_asset_candidates or get_asset does not satisfy this requirement. Reuse creates a separate Episode Asset with the same identity, inherited current image, and an optional Episode-specific Prompt; it never modifies the source.", InputSchema: objectSchema(map[string]any{"asset_id": id("Earlier-Episode Asset ID returned by list_prior_asset_candidates.")}, "asset_id"), Annotations: readOnly},
		{Name: "list_scenes", Description: "List active Scenes in Storyboard order. Follow next_cursor until has_more is false before appending the next Scene.", InputSchema: objectSchema(map[string]any{"cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxScenePageSize)}), Annotations: readOnly},
		{Name: "get_scene", Description: "Read one active Scene and its source-script provenance.", InputSchema: objectSchema(map[string]any{"scene_id": id("Scene ID.")}, "scene_id"), Annotations: readOnly},
		{Name: "get_clips", Description: "List active Clips in one Scene in Storyboard order. Follow next_cursor until has_more is false.", InputSchema: objectSchema(map[string]any{"scene_id": id("Scene ID."), "cursor": stringValue("Opaque cursor returned by the previous page."), "limit": pageLimitSchema(MaxClipPageSize)}, "scene_id"), Annotations: readOnly},
		{Name: "get_clip", Description: "Read one active Clip and selected Prompt fields. Each video generation mode owns an independent video Prompt. prompt_fields defaults to all Prompt fields and may be narrowed if the result is too large.", InputSchema: objectSchema(map[string]any{"clip_id": id("Clip ID."), "prompt_fields": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{string(clipFirstPrompt), string(clipLastPrompt), string(clipFirstVideoPrompt), string(clipFirstLastVideoPrompt), string(clipReferenceVideoPrompt)}}, "uniqueItems": true, "minItems": 1, "maxItems": 5}}, "clip_id"), Annotations: readOnly},
		{Name: "get_video_prompt_adaptation_context", Description: "Read and freeze the focused Clip context for one explicit video-model prompt adaptation, including bound Assets and uploaded image/video/audio media with stable reference_key, name, description, Prompt token, model label, and clarification candidates. The daemon derives prompt_family and prompt_profile from generator_id, provider_model_id, and generation_mode; never infer or substitute another target. Ask the user in chat and stop without a ChangeSet if a reference purpose cannot be inferred safely. Call this before append_video_prompt_adaptation.", InputSchema: videoPromptAdaptationContextSchema(), Annotations: readOnly},
		{Name: "generate_conversation_image", Description: "Generate exactly one image and add it to the active Agent conversation. Images attached to the current user message are used automatically as ordered references. Use only when the user explicitly requests image generation. This does not create or update an Asset or video-unit media slot.", InputSchema: objectSchema(map[string]any{"prompt": stringValue("Complete image-generation prompt preserving the user's requested subject, style, composition, and reference-image constraints.")}, "prompt"), Annotations: draftOnly},
		{Name: "begin_change_set", Description: "Begin a draft ChangeSet for this active Turn. This does not modify the Project.", InputSchema: objectSchema(map[string]any{"title": stringValue("Short review title."), "summary": stringValue("Optional review summary."), "operation_key": stringValue("Unique idempotency key for this command.")}, "title", "operation_key"), Annotations: draftOnly},
		{Name: "append_prompt_operations", Description: "Append exactly one Asset or Clip Prompt proposal using values observed in this Turn. Use a separate ChangeSet for every other Asset or Clip. A Clip owns three independent video Prompts; video_prompt_mode is required and only that mode changes. When changing a reference-mode video Prompt, include its complete ordered asset_ids and reference_artifact_ids dependency sets after reading every newly added Asset and uploaded medium. Prompt text plus Asset/image/video/audio bindings apply and undo atomically. Keep exact @reference names in the editable Prompt; generation deterministically compiles them to the selected model's labels. Prompt wording and format remain user-reviewable and are not blocked by stylistic validation. For every video Prompt rewrite, follow get_clip video_prompt_targets and its profile instructions; supply generator_id/provider_model_id when no selected model context exists. The daemon stores a model-family variant and preserves all other families and modes. Do not supply old values or target revisions.", InputSchema: appendOperationsSchema(), Annotations: draftOnly},
		{Name: "append_video_prompt_adaptation", Description: "Append exactly one model-family video Prompt adaptation after get_video_prompt_adaptation_context. The daemon re-derives and verifies the family/profile, checks the source and current reference-set hashes, preserves every other family and generation mode, and records complete Asset/upload reference metadata with Clip-specific purposes inside the existing mode Prompt JSON. Omit reference_artifact_ids to preserve uploaded-media bindings. Only when the user explicitly adds, removes, or replaces uploaded media, call list_reference_media and provide the complete desired ordered reference_artifact_ids set; Prompt and bindings then apply and undo atomically. Exact @reference first-mention order in the Prompt controls provider labels at submission. Do not use append_prompt_operations for a model adaptation.", InputSchema: appendVideoPromptAdaptationSchema(), Annotations: draftOnly},
		{Name: "append_asset_operations", Description: "Append exactly one extracted Asset proposal after reading the complete script and current Asset list. If the identity is already in this Episode, use that Asset without creating another. Check list_prior_asset_candidates before creating a new identity; for a matching earlier identity call get_prior_asset_candidate in this Turn, then supply source_asset_id instead of kind/name. This creates an independent Episode copy, inheriting the current image and optionally adapting the Prompt for clothing, age, or other Episode-specific appearance without changing the source. During broad extraction, create characters, locations, and props only; fold visual style into each of their Prompts instead of creating a style Asset. Create a style Asset only when the user explicitly requested a standalone style Asset in the current message. Use a separate ChangeSet per Asset.", InputSchema: appendAssetOperationsSchema(), Annotations: draftOnly},
		{Name: "restore_asset", Description: "When the user explicitly requests restoration, append exactly one restoration of a deleted Asset previously read with get_deleted_asset. The existing Asset identity, Prompt, and media are preserved. Use a dedicated ChangeSet, then finalize it for safe auto-apply and deterministic Undo.", InputSchema: restoreAssetSchema(), Annotations: draftOnly},
		{Name: "append_storyboard_operations", Description: "Append exactly one next Scene with its Clips, or add one or more Clips to exactly one existing Scene. For a new Scene, create_clip uses scene_ref. For an existing Scene it uses scene_id and may specify exactly one placement anchor: before_clip_id or after_clip_id; omit both to append at the end. Requires the complete script, complete current Scene list, every existing Clip in the target Scene, and every referenced Asset to have been read in this Turn. Plan complete beats, default to 8 seconds, and normally use 6-10 seconds; 4-5 seconds is only for a brief insert, reaction, empty shot, or transition. Do not split merely because the dialogue speaker changes. Merge adjacent micro-beats to preserve the Episode duration budget: the proposal must average at least 6 seconds per Clip and may not push the Storyboard beyond the Episode target. Default each Clip to reference mode and initialize only the selected mode's independent video Prompt. The system-injected request supplies the current generator_id, mode-compatible provider_model_id, and Prompt rules; copy both IDs into every create_clip and use exact @Asset names at semantic reference points. Bind every depended-on Asset in intended reference order. Finalize one Scene-targeted ChangeSet at a time and re-read the Storyboard before continuing.", InputSchema: appendStoryboardOperationsSchema(), Annotations: draftOnly},
		{Name: "append_clip_merge_operation", Description: "Append one atomic merge of existing adjacent Clips after the user explicitly requests it. Read every Clip in full, every active Asset bound by a source Clip, and the complete target Scene Clip list first. Source IDs must be ordered and adjacent in one Scene. The daemon preserves total duration, continue-from-previous, and the ordered union of active source Asset bindings; it soft-deletes sources for deterministic Undo, inherits only the first source's current first frame and the last source's current last frame, and never inherits video or other media. Supply complete merged content and a model-aware video Prompt using the current selected video_prompt_targets; optionally copy its generator_id/provider_model_id. Only source-bound Assets carry into the merged Clip; uploaded-media bindings remain on the source Clips for Undo. This tool is the only supported merge entry point; never emulate merge with create/delete operations.", InputSchema: appendClipMergeOperationSchema(), Annotations: draftOnly},
		{Name: "append_clip_split_operation", Description: "Append one atomic split of an existing Clip into 2-10 new Clips after the user explicitly requests it. Read the source Clip, every active bound Asset, and its complete Scene first. Preserve narrative order; choose each result's duration from its content, even when the combined duration changes. Each replacement must be a complete independent Clip and may use only source-bound Assets. The source remains stored for deterministic Undo; only its current first frame may pass to the first result and its current last frame to the last result. Video and other media never carry over; uploaded-media bindings remain on the source for Undo, so new Prompts must not mention unbound uploads. Follow the selected video_prompt_targets and profile instructions for every replacement; optionally copy its exact generator_id/provider_model_id. Never emulate a split with separate create and delete operations.", InputSchema: appendClipSplitOperationSchema(), Annotations: draftOnly},
		{Name: "append_script_operation", Description: "Append a complete Episode script replacement after reading the complete current script. The daemon records before/after text and revision; do not put a long script only in chat.", InputSchema: objectSchema(map[string]any{"change_set_id": id("Draft ChangeSet ID."), "expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1}, "operation_key": stringValue("Unique idempotency key for this command."), "markdown": stringValue("Complete replacement script Markdown.")}, "change_set_id", "expected_change_set_revision", "operation_key", "markdown"), Annotations: draftOnly},
		{Name: "finalize_change_set", Description: "Validate, freeze, and auto-apply a safe local ChangeSet. The UI records it and offers deterministic Undo.", InputSchema: objectSchema(map[string]any{"change_set_id": id("Draft ChangeSet ID."), "expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1}, "operation_key": stringValue("Unique idempotency key for this command.")}, "change_set_id", "expected_change_set_revision", "operation_key"), Annotations: draftOnly},
	}
}

func appendStoryboardOperationsSchema() map[string]any {
	source := objectSchema(map[string]any{"start_byte": map[string]any{"type": "integer", "minimum": 0}, "end_byte": map[string]any{"type": "integer", "minimum": 1}}, "start_byte", "end_byte")
	scene := objectSchema(map[string]any{"type": map[string]any{"const": string(changeset.CreateScene)}, "ref": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "source": source}, "type", "ref", "title")
	clip := objectSchema(map[string]any{
		"type":                     map[string]any{"const": string(changeset.CreateClip)},
		"ref":                      map[string]any{"type": "string"},
		"scene_ref":                map[string]any{"type": "string", "description": "Temporary Scene ref when this ChangeSet creates the Scene. Mutually exclusive with scene_id."},
		"scene_id":                 map[string]any{"type": "string", "description": "Existing target Scene ID. Mutually exclusive with scene_ref."},
		"before_clip_id":           map[string]any{"type": "string", "description": "Optional active Clip anchor in scene_id. Insert this Clip immediately before it. Mutually exclusive with after_clip_id."},
		"after_clip_id":            map[string]any{"type": "string", "description": "Optional active Clip anchor in scene_id. Insert this Clip immediately after it. Mutually exclusive with before_clip_id."},
		"title":                    map[string]any{"type": "string"},
		"description":              map[string]any{"type": "string"},
		"dialogue":                 map[string]any{"type": "string"},
		"creative_intent":          map[string]any{"type": "string"},
		"planned_duration_seconds": map[string]any{"type": "integer", "minimum": project.MinPortableClipDurationSeconds, "maximum": project.MaxPortableClipDurationSeconds, "description": "Portable Clip duration. Default to 8 seconds and normally use 6-10. Use 4-5 only for a brief insert, reaction, empty shot, or transition; merge adjacent micro-beats instead of making many short Clips."},
		"first_image_prompt":       map[string]any{"type": "string", "description": "Still-image prompt for the opening frame. Describe composition and appearance, not motion timing."},
		"last_image_prompt":        map[string]any{"type": "string", "description": "Optional still-image prompt for an explicitly constrained ending frame."},
		"video_prompt":             map[string]any{"type": "string", "description": "Model-aware editable video Prompt for the selected generation_mode only; the other two mode Prompts start empty. Keep exact @Asset names at semantic reference points and follow the system-injected current-model rules."},
		"generation_mode":          map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}, "description": "Use reference by default. Use first_image only for strict opening-frame control, or first_last_image for a deliberate interpolation between prepared endpoints."},
		"generator_id":             map[string]any{"type": "string", "description": "Current video generator supplied by the system-injected storyboard request."},
		"provider_model_id":        map[string]any{"type": "string", "description": "Mode-compatible provider model supplied by the system-injected storyboard request."},
	}, "type", "ref", "title", "planned_duration_seconds", "generation_mode", "generator_id", "provider_model_id")
	assetRef := objectSchema(map[string]any{"type": map[string]any{"const": string(changeset.AddClipAssetRef)}, "clip_ref": map[string]any{"type": "string"}, "asset_id": map[string]any{"type": "string"}}, "type", "clip_ref", "asset_id")
	return objectSchema(map[string]any{"change_set_id": map[string]any{"type": "string"}, "expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1}, "operation_key": map[string]any{"type": "string"}, "operations": map[string]any{"type": "array", "minItems": 1, "maxItems": changeset.MaxAppendBatch, "items": map[string]any{"oneOf": []any{scene, clip, assetRef}}}}, "change_set_id", "expected_change_set_revision", "operation_key", "operations")
}

func appendAssetOperationsSchema() map[string]any {
	newAsset := objectSchema(map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"character", "location", "prop", "style"}, "description": "Use style only when the user explicitly requested a standalone style Asset; otherwise distribute style direction across character, location, and prop Prompts."},
		"name": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"},
	}, "kind", "name", "prompt")
	derived := objectSchema(map[string]any{
		"source_asset_id": map[string]any{"type": "string", "description": "Active earlier-Episode Asset ID read with get_prior_asset_candidate in this Turn, not get_asset. Creates a separate Asset sharing only the narrative identity and initial current image; source edits never synchronize to the new Asset."},
		"prompt":          map[string]any{"type": "string", "description": "Optional complete Prompt for this Episode's independent appearance (for example clothing or age). Omit to inherit the source Prompt. This never changes the source Prompt."},
	}, "source_asset_id")
	return objectSchema(map[string]any{
		"change_set_id": map[string]any{"type": "string"}, "expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1},
		"operation_key": map[string]any{"type": "string"},
		"operations":    map[string]any{"type": "array", "minItems": 1, "maxItems": MaxAssetOperationsPerChangeSet, "items": map[string]any{"oneOf": []any{newAsset, derived}}},
	}, "change_set_id", "expected_change_set_revision", "operation_key", "operations")
}

func restoreAssetSchema() map[string]any {
	return objectSchema(map[string]any{
		"change_set_id":                map[string]any{"type": "string"},
		"expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1},
		"operation_key":                map[string]any{"type": "string"},
		"asset_id":                     map[string]any{"type": "string"},
	}, "change_set_id", "expected_change_set_revision", "operation_key", "asset_id")
}

func appendClipMergeOperationSchema() map[string]any {
	return objectSchema(map[string]any{
		"change_set_id":                map[string]any{"type": "string"},
		"expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1},
		"operation_key":                map[string]any{"type": "string"},
		"source_clip_ids": map[string]any{"type": "array", "minItems": 2, "maxItems": 20, "uniqueItems": true,
			"items": map[string]any{"type": "string"}},
		"title":              map[string]any{"type": "string"},
		"description":        map[string]any{"type": "string"},
		"dialogue":           map[string]any{"type": "string"},
		"creative_intent":    map[string]any{"type": "string"},
		"first_image_prompt": map[string]any{"type": "string"},
		"last_image_prompt":  map[string]any{"type": "string"},
		"video_prompt":       map[string]any{"type": "string"},
		"generation_mode":    map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}},
		"generator_id":       map[string]any{"type": "string", "description": "Exact generator from this Turn's selected video_prompt_targets; required if no selected or source model context exists."},
		"provider_model_id":  map[string]any{"type": "string", "description": "Mode-compatible model from this Turn's selected video_prompt_targets."},
	}, "change_set_id", "expected_change_set_revision", "operation_key", "source_clip_ids", "title", "description", "dialogue", "creative_intent", "first_image_prompt", "last_image_prompt", "video_prompt", "generation_mode")
}

func appendClipSplitOperationSchema() map[string]any {
	replacement := objectSchema(map[string]any{
		"title":                    map[string]any{"type": "string"},
		"description":              map[string]any{"type": "string"},
		"dialogue":                 map[string]any{"type": "string"},
		"creative_intent":          map[string]any{"type": "string"},
		"planned_duration_seconds": map[string]any{"type": "integer", "minimum": project.MinPortableClipDurationSeconds, "maximum": project.MaxPortableClipDurationSeconds},
		"first_image_prompt":       map[string]any{"type": "string"},
		"last_image_prompt":        map[string]any{"type": "string"},
		"video_prompt":             map[string]any{"type": "string"},
		"generation_mode":          map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}},
		"asset_ids":                map[string]any{"type": "array", "uniqueItems": true, "items": map[string]any{"type": "string"}},
		"generator_id":             map[string]any{"type": "string", "description": "Exact generator from this Turn's selected video_prompt_targets."},
		"provider_model_id":        map[string]any{"type": "string", "description": "Mode-compatible model from this Turn's selected video_prompt_targets."},
	}, "title", "description", "dialogue", "creative_intent", "planned_duration_seconds", "first_image_prompt", "last_image_prompt", "video_prompt", "generation_mode", "asset_ids")
	return objectSchema(map[string]any{
		"change_set_id":                map[string]any{"type": "string"},
		"expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1},
		"operation_key":                map[string]any{"type": "string"},
		"source_clip_id":               map[string]any{"type": "string"},
		"replacements":                 map[string]any{"type": "array", "minItems": 2, "maxItems": 10, "items": replacement},
	}, "change_set_id", "expected_change_set_revision", "operation_key", "source_clip_id", "replacements")
}

func appendOperationsSchema() map[string]any {
	asset := objectSchema(map[string]any{
		"type":       map[string]any{"const": string(changeset.UpdateAssetPrompt)},
		"asset_id":   map[string]any{"type": "string"},
		"new_prompt": map[string]any{"type": "string"},
	}, "type", "asset_id", "new_prompt")
	clip := objectSchema(map[string]any{
		"type":                   map[string]any{"const": string(changeset.UpdateClipPrompts)},
		"clip_id":                map[string]any{"type": "string"},
		"first_image_prompt":     map[string]any{"type": "string"},
		"last_image_prompt":      map[string]any{"type": "string"},
		"video_prompt":           map[string]any{"type": "string"},
		"video_prompt_mode":      map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}, "description": "Required with video_prompt; updates only this mode's independent video Prompt."},
		"generator_id":           map[string]any{"type": "string", "description": "Exact selected video generator. Omit only when this Turn or the existing mode Prompt contains its model target."},
		"provider_model_id":      map[string]any{"type": "string", "description": "Mode-compatible selected video model; paired with generator_id."},
		"asset_ids":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true, "maxItems": 30, "description": "Complete ordered Asset dependency set for this Clip Prompt."},
		"reference_artifact_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true, "maxItems": 30, "description": "Complete ordered uploaded image/video/audio Artifact dependency set for this Clip Prompt. Use IDs returned by list_reference_media."},
	}, "type", "clip_id")
	return objectSchema(map[string]any{
		"change_set_id":                map[string]any{"type": "string"},
		"expected_change_set_revision": map[string]any{"type": "integer", "minimum": 1},
		"operation_key":                map[string]any{"type": "string"},
		"operations": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 1,
			"items": map[string]any{"oneOf": []any{asset, clip}},
		},
	}, "change_set_id", "expected_change_set_revision", "operation_key", "operations")
}

func videoPromptAdaptationTargetProperties() map[string]any {
	return map[string]any{
		"clip_id":           map[string]any{"type": "string"},
		"generation_mode":   map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}},
		"generator_id":      map[string]any{"type": "string"},
		"provider_model_id": map[string]any{"type": "string"},
		"prompt_family":     map[string]any{"type": "string", "enum": []string{"h3", "seedance", "wan3"}},
		"prompt_profile":    map[string]any{"type": "string"},
	}
}

func videoPromptAdaptationContextSchema() map[string]any {
	properties := videoPromptAdaptationTargetProperties()
	delete(properties, "prompt_family")
	delete(properties, "prompt_profile")
	return objectSchema(properties, "clip_id", "generation_mode", "generator_id", "provider_model_id")
}

func appendVideoPromptAdaptationSchema() map[string]any {
	properties := videoPromptAdaptationTargetProperties()
	properties["change_set_id"] = map[string]any{"type": "string"}
	properties["expected_change_set_revision"] = map[string]any{"type": "integer", "minimum": 1}
	properties["operation_key"] = map[string]any{"type": "string"}
	properties["source_family"] = map[string]any{"type": "string", "enum": []string{"legacy", "h3", "seedance", "wan3"}}
	properties["source_generation_mode"] = map[string]any{"type": "string", "enum": []string{"first_image", "first_last_image", "reference"}, "description": "Generation mode returned in source by get_video_prompt_adaptation_context; it may differ from the target mode after a mode switch."}
	properties["source_prompt_hash"] = map[string]any{"type": "string"}
	properties["reference_set_hash"] = map[string]any{"type": "string"}
	properties["prompt"] = map[string]any{"type": "string"}
	properties["reference_artifact_ids"] = map[string]any{
		"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true, "maxItems": 30,
		"description": "Optional complete desired ordered uploaded image/video/audio Artifact binding set. Omit to preserve current bindings. Supply only for the user's explicit add, remove, or replace request after list_reference_media.",
	}
	reference := objectSchema(map[string]any{
		"key":      map[string]any{"type": "string", "description": "Stable reference_key returned by get_video_prompt_adaptation_context, such as asset:<asset_id> or upload:<artifact_id>."},
		"purposes": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []string{"identity", "appearance", "scene", "object", "style", "composition", "motion", "camera", "voice", "sound", "music", "other"}}},
		"note":     map[string]any{"type": "string", "maxLength": 500, "description": "Optional Clip-specific clarification, including what must not be copied from this reference."},
	}, "key", "purposes")
	properties["references"] = map[string]any{"type": "array", "maxItems": 30, "items": reference, "description": "Complete desired reference metadata. Include every ready bound Asset plus every uploaded medium in the desired reference_artifact_ids set, or the preserved current set when that field is omitted; use an empty array in keyframe modes. Provider label order is derived from exact @reference first mentions in the Prompt."}
	return objectSchema(properties, "change_set_id", "expected_change_set_revision", "operation_key", "clip_id", "generation_mode", "generator_id", "provider_model_id", "prompt_family", "prompt_profile", "source_generation_mode", "source_family", "source_prompt_hash", "reference_set_hash", "prompt", "references")
}

type emptyArguments struct{}

type declareTurnIntentArguments struct {
	Intent string `json:"intent"`
	Target string `json:"target"`
}

type listAssetsArguments struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

type listReferenceMediaArguments struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
	Kind   string `json:"kind"`
}

type readScriptArguments struct {
	Cursor string `json:"cursor"`
}

type listPriorAssetsArguments struct {
	Kind      string `json:"kind"`
	NameQuery string `json:"name_query"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
}

type getAssetArguments struct {
	AssetID string `json:"asset_id"`
}

type appendAssetOperationsArguments struct {
	ChangeSetID               string                   `json:"change_set_id"`
	ExpectedChangeSetRevision int64                    `json:"expected_change_set_revision"`
	OperationKey              string                   `json:"operation_key"`
	Operations                []assetOperationArgument `json:"operations"`
}

type restoreAssetArguments struct {
	ChangeSetID               string `json:"change_set_id"`
	ExpectedChangeSetRevision int64  `json:"expected_change_set_revision"`
	OperationKey              string `json:"operation_key"`
	AssetID                   string `json:"asset_id"`
}

type assetOperationArgument struct {
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	Prompt        *string `json:"prompt"`
	SourceAssetID string  `json:"source_asset_id"`
}

type getClipArguments struct {
	ClipID       string   `json:"clip_id"`
	PromptFields []string `json:"prompt_fields"`
}

type listScenesArguments struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

type getSceneArguments struct {
	SceneID string `json:"scene_id"`
}

type getClipsArguments struct {
	SceneID string `json:"scene_id"`
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
}

type storyboardSourceArgument struct {
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
}

type storyboardOperationArgument struct {
	Type                   string                     `json:"type"`
	Ref                    string                     `json:"ref"`
	SceneRef               string                     `json:"scene_ref"`
	SceneID                string                     `json:"scene_id"`
	BeforeClipID           string                     `json:"before_clip_id"`
	AfterClipID            string                     `json:"after_clip_id"`
	ClipRef                string                     `json:"clip_ref"`
	AssetID                string                     `json:"asset_id"`
	Position               int                        `json:"position"`
	Title                  string                     `json:"title"`
	Description            string                     `json:"description"`
	Dialogue               string                     `json:"dialogue"`
	CreativeIntent         string                     `json:"creative_intent"`
	PlannedDurationSeconds int64                      `json:"planned_duration_seconds"`
	FirstImagePrompt       string                     `json:"first_image_prompt"`
	LastImagePrompt        string                     `json:"last_image_prompt"`
	VideoPrompt            string                     `json:"video_prompt"`
	GenerationMode         project.ClipGenerationMode `json:"generation_mode"`
	GeneratorID            string                     `json:"generator_id"`
	ProviderModelID        string                     `json:"provider_model_id"`
	Source                 *storyboardSourceArgument  `json:"source"`
}

type appendStoryboardOperationsArguments struct {
	ChangeSetID               string                        `json:"change_set_id"`
	ExpectedChangeSetRevision int64                         `json:"expected_change_set_revision"`
	OperationKey              string                        `json:"operation_key"`
	Operations                []storyboardOperationArgument `json:"operations"`
}

type appendClipMergeOperationArguments struct {
	ChangeSetID               string                     `json:"change_set_id"`
	ExpectedChangeSetRevision int64                      `json:"expected_change_set_revision"`
	OperationKey              string                     `json:"operation_key"`
	SourceClipIDs             []string                   `json:"source_clip_ids"`
	Title                     string                     `json:"title"`
	Description               string                     `json:"description"`
	Dialogue                  string                     `json:"dialogue"`
	CreativeIntent            string                     `json:"creative_intent"`
	FirstImagePrompt          string                     `json:"first_image_prompt"`
	LastImagePrompt           string                     `json:"last_image_prompt"`
	VideoPrompt               string                     `json:"video_prompt"`
	GenerationMode            project.ClipGenerationMode `json:"generation_mode"`
	GeneratorID               string                     `json:"generator_id"`
	ProviderModelID           string                     `json:"provider_model_id"`
}

type clipSplitReplacementArgument struct {
	Title                  string                     `json:"title"`
	Description            string                     `json:"description"`
	Dialogue               string                     `json:"dialogue"`
	CreativeIntent         string                     `json:"creative_intent"`
	PlannedDurationSeconds int64                      `json:"planned_duration_seconds"`
	FirstImagePrompt       string                     `json:"first_image_prompt"`
	LastImagePrompt        string                     `json:"last_image_prompt"`
	VideoPrompt            string                     `json:"video_prompt"`
	GenerationMode         project.ClipGenerationMode `json:"generation_mode"`
	AssetIDs               []string                   `json:"asset_ids"`
	GeneratorID            string                     `json:"generator_id"`
	ProviderModelID        string                     `json:"provider_model_id"`
}

type appendClipSplitOperationArguments struct {
	ChangeSetID               string                         `json:"change_set_id"`
	ExpectedChangeSetRevision int64                          `json:"expected_change_set_revision"`
	OperationKey              string                         `json:"operation_key"`
	SourceClipID              string                         `json:"source_clip_id"`
	Replacements              []clipSplitReplacementArgument `json:"replacements"`
}

type beginChangeSetArguments struct {
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	OperationKey string `json:"operation_key"`
}

type appendPromptOperationsArguments struct {
	ChangeSetID               string                    `json:"change_set_id"`
	ExpectedChangeSetRevision int64                     `json:"expected_change_set_revision"`
	OperationKey              string                    `json:"operation_key"`
	Operations                []promptOperationArgument `json:"operations"`
}

type videoPromptAdaptationTargetArguments struct {
	ClipID          string `json:"clip_id"`
	GenerationMode  string `json:"generation_mode"`
	GeneratorID     string `json:"generator_id"`
	ProviderModelID string `json:"provider_model_id"`
	PromptFamily    string `json:"prompt_family"`
	PromptProfile   string `json:"prompt_profile"`
}

type appendVideoPromptAdaptationArguments struct {
	videoPromptAdaptationTargetArguments
	ChangeSetID               string                 `json:"change_set_id"`
	ExpectedChangeSetRevision int64                  `json:"expected_change_set_revision"`
	OperationKey              string                 `json:"operation_key"`
	SourceGenerationMode      string                 `json:"source_generation_mode"`
	SourceFamily              string                 `json:"source_family"`
	SourcePromptHash          string                 `json:"source_prompt_hash"`
	ReferenceSetHash          string                 `json:"reference_set_hash"`
	Prompt                    string                 `json:"prompt"`
	References                []videoPromptReference `json:"references"`
	ReferenceArtifactIDs      *[]string              `json:"reference_artifact_ids"`
}

type promptOperationArgument struct {
	Type                 string    `json:"type"`
	AssetID              string    `json:"asset_id"`
	ClipID               string    `json:"clip_id"`
	NewPrompt            *string   `json:"new_prompt"`
	FirstImagePrompt     *string   `json:"first_image_prompt"`
	LastImagePrompt      *string   `json:"last_image_prompt"`
	VideoPrompt          *string   `json:"video_prompt"`
	VideoPromptMode      string    `json:"video_prompt_mode"`
	AssetIDs             *[]string `json:"asset_ids"`
	ReferenceArtifactIDs *[]string `json:"reference_artifact_ids"`
	GeneratorID          string    `json:"generator_id"`
	ProviderModelID      string    `json:"provider_model_id"`
}

type finalizeChangeSetArguments struct {
	ChangeSetID               string `json:"change_set_id"`
	ExpectedChangeSetRevision int64  `json:"expected_change_set_revision"`
	OperationKey              string `json:"operation_key"`
}

type appendScriptOperationArguments struct {
	ChangeSetID               string `json:"change_set_id"`
	ExpectedChangeSetRevision int64  `json:"expected_change_set_revision"`
	OperationKey              string `json:"operation_key"`
	Markdown                  string `json:"markdown"`
}

type generateConversationImageArguments struct {
	Prompt string `json:"prompt"`
}

type bindingSnapshot struct {
	scope   BindingScope
	turn    ActiveTurn
	readSet *TurnReadSet
}

func (s *Server) callTool(ctx context.Context, route, name string, arguments json.RawMessage) ([]byte, error) {
	if !enabledTool(name) {
		return nil, toolError("unknown_tool", "tool is not available")
	}
	if name != "get_session_context" {
		if err := s.awaitPreparedTurn(ctx, route); err != nil {
			return nil, err
		}
	}
	release, err := s.acquireBindingGate(route)
	if err != nil {
		return nil, err
	}
	defer release()
	switch name {
	case "declare_turn_intent":
		return s.declareTurnIntent(route, arguments)
	case "get_session_context":
		var input emptyArguments
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, toolError("invalid_arguments", "get_session_context does not accept arguments")
		}
		state, err := s.snapshotBinding(route, false)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{"session_id": state.scope.SessionID.String(), "project_id": state.scope.ProjectID.String(), "episode_id": state.scope.EpisodeID.String()}
		if state.turn.AgentTurnID != "" {
			payload["focus"] = map[string]any{"scene_id": state.turn.Focus.SceneID.String(), "clip_id": state.turn.Focus.ClipID.String(), "asset_id": state.turn.Focus.AssetID.String()}
		}
		return marshalToolPayload(payload)
	case "get_episode_context":
		return s.getEpisodeContext(ctx, route, arguments)
	case "read_script":
		return s.readScript(ctx, route, arguments)
	case "list_assets":
		return s.listAssets(ctx, route, arguments)
	case "list_reference_media":
		return s.listReferenceMedia(ctx, route, arguments)
	case "get_asset":
		return s.getAsset(ctx, route, arguments)
	case "list_deleted_assets":
		return s.listDeletedAssets(ctx, route, arguments)
	case "get_deleted_asset":
		return s.getDeletedAsset(ctx, route, arguments)
	case "list_prior_asset_candidates":
		return s.listPriorAssetCandidates(ctx, route, arguments)
	case "get_prior_asset_candidate":
		return s.getPriorAssetCandidate(ctx, route, arguments)
	case "list_scenes":
		return s.listScenes(ctx, route, arguments)
	case "get_scene":
		return s.getScene(ctx, route, arguments)
	case "get_clips":
		return s.getClips(ctx, route, arguments)
	case "get_clip":
		return s.getClip(ctx, route, arguments)
	case "get_video_prompt_adaptation_context":
		return s.getVideoPromptAdaptationContext(ctx, route, arguments)
	case "generate_conversation_image":
		return s.generateConversationImage(ctx, route, arguments)
	case "begin_change_set":
		return s.beginChangeSet(ctx, route, arguments)
	case "append_prompt_operations":
		return s.appendPromptOperations(ctx, route, arguments)
	case "append_video_prompt_adaptation":
		return s.appendVideoPromptAdaptation(ctx, route, arguments)
	case "append_asset_operations":
		return s.appendAssetOperations(ctx, route, arguments)
	case "restore_asset":
		return s.restoreAsset(ctx, route, arguments)
	case "append_storyboard_operations":
		return s.appendStoryboardOperations(ctx, route, arguments)
	case "append_clip_merge_operation":
		return s.appendClipMergeOperation(ctx, route, arguments)
	case "append_clip_split_operation":
		return s.appendClipSplitOperation(ctx, route, arguments)
	case "append_script_operation":
		return s.appendScriptOperation(ctx, route, arguments)
	case "finalize_change_set":
		return s.finalizeChangeSet(ctx, route, arguments)
	default:
		return nil, toolError("unknown_tool", "tool is not available")
	}
}

func (s *Server) generateConversationImage(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input generateConversationImageArguments
	if err := decodeStrict(arguments, &input); err != nil || strings.TrimSpace(input.Prompt) == "" || utf8.RuneCountInString(input.Prompt) > project.MaxGenerationPromptCharacters {
		return nil, toolError("invalid_arguments", "generate_conversation_image arguments are invalid")
	}
	if s.images == nil {
		return nil, toolError("image_generation_unavailable", "Image generation is unavailable")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	result, err := s.images.Generate(ctx, agentimage.Input{
		ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, SessionID: state.scope.SessionID,
		TurnID: state.turn.AgentTurnID, RuntimeKind: state.turn.RuntimeKind, Prompt: input.Prompt, ArtifactIDs: append([]project.ArtifactID(nil), state.turn.ArtifactIDs...),
	})
	if err != nil {
		var failure *agentimage.Failure
		if errors.As(err, &failure) {
			if generation.IsCloudBusinessFailure(failure.Code) {
				s.events.Publish(agentapp.StudioEvent{
					Type: agentapp.EventToolCompleted, SessionID: state.scope.SessionID,
					TurnID: state.turn.AgentTurnID, ToolName: "generate_conversation_image",
					FailureCode: failure.Code, Summary: "ShotForward account cannot accept this request", OccurredAt: time.Now().UTC().Truncate(time.Millisecond),
				})
			}
			return nil, toolError(failure.Code, failure.Message)
		}
		if errors.Is(err, agentimage.ErrInvalidRequest) {
			return nil, toolError("invalid_arguments", "Image generation request is invalid")
		}
		return nil, toolError("image_generation_failed", "Image generation failed")
	}
	return marshalToolPayload(map[string]any{
		"status": "completed", "artifact_id": result.ArtifactID.String(), "generator_id": result.GeneratorID,
		"reference_count": result.ReferenceCount, "message": "The generated image was added to this conversation.",
	})
}

func enabledTool(name string) bool {
	for _, allowed := range EnabledToolNames() {
		if name == allowed {
			return true
		}
	}
	return false
}

func (s *Server) declareTurnIntent(route string, arguments json.RawMessage) ([]byte, error) {
	var input declareTurnIntentArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "declare_turn_intent arguments are invalid")
	}
	validIntent := input.Intent == "discussion" || input.Intent == "clarification" || input.Intent == "project_change"
	validTarget := input.Target == "none" || input.Target == "script" || input.Target == "assets" || input.Target == "storyboard" || input.Target == "prompts"
	if !validIntent || !validTarget || input.Intent == "project_change" && input.Target == "none" || input.Intent != "project_change" && input.Target != "none" {
		return nil, toolError("invalid_arguments", "intent and target are inconsistent")
	}
	s.mutex.Lock()
	state := s.bindings[route]
	if state == nil || state.turn == nil {
		s.mutex.Unlock()
		return nil, toolError("turn_not_active", "no active Agent Turn is bound to this Session")
	}
	state.intent, state.target = input.Intent, input.Target
	s.mutex.Unlock()
	return marshalToolPayload(map[string]any{"intent": input.Intent, "target": input.Target})
}

func (s *Server) listScenes(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input listScenesArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 {
		return nil, toolError("invalid_arguments", "list_scenes arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxScenePageSize)
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	value, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	snapshot, err := storyboardSnapshot(value.Storyboard)
	if err != nil {
		return nil, toolError("invalid_project_data", "Storyboard could not be validated")
	}
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(value.Storyboard.Scenes) {
		return nil, toolError("cursor_stale", "Storyboard cursor is stale")
	}
	end := min(offset+input.Limit, len(value.Storyboard.Scenes))
	items := make([]map[string]any, 0, end-offset)
	for index, scene := range value.Storyboard.Scenes[offset:end] {
		items = append(items, map[string]any{"scene_id": scene.Scene.ID.String(), "position": offset + index + 1, "title": scene.Scene.Title, "revision": scene.Scene.Revision, "clip_count": len(scene.Clips)})
	}
	next := ""
	if end < len(value.Storyboard.Scenes) {
		next, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	payload, err := marshalToolPayload(map[string]any{"items": items, "storyboard_revision": value.Storyboard.Revision, "planned_duration_ms": value.Storyboard.PlannedDurationSeconds * 1000, "has_more": end < len(value.Storyboard.Scenes), "next_cursor": next})
	if err == nil {
		state.readSet.observeStoryboardPage(value.Storyboard.Revision, snapshot, offset, end, end == len(value.Storyboard.Scenes), len(value.Storyboard.Scenes) == 0)
	}
	return payload, err
}

func (s *Server) getScene(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getSceneArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_scene arguments are invalid")
	}
	id, err := project.ParseSceneID(input.SceneID)
	if err != nil {
		return nil, toolError("invalid_arguments", "scene_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	storyboard, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	for index, value := range storyboard.Storyboard.Scenes {
		if value.Scene.ID == id {
			payload := map[string]any{"scene_id": id.String(), "position": index + 1, "title": value.Scene.Title, "description": value.Scene.Description, "revision": value.Scene.Revision, "clip_count": len(value.Clips), "storyboard_revision": storyboard.Storyboard.Revision}
			if value.Scene.Source != nil {
				payload["source"] = value.Scene.Source
			}
			return marshalToolPayload(payload)
		}
	}
	return nil, toolError("object_outside_scope", "Scene is not active in this Session's Episode")
}

func (s *Server) getClips(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getClipsArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 {
		return nil, toolError("invalid_arguments", "get_clips arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxClipPageSize)
	sceneID, err := project.ParseSceneID(input.SceneID)
	if err != nil {
		return nil, toolError("invalid_arguments", "scene_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	storyboard, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	var clips []project.ClipDetails
	found := false
	for _, scene := range storyboard.Storyboard.Scenes {
		if scene.Scene.ID == sceneID {
			clips = scene.Clips
			found = true
			break
		}
	}
	if !found {
		return nil, toolError("object_outside_scope", "Scene is not active in this Session's Episode")
	}
	base, err := storyboardSnapshot(storyboard.Storyboard)
	if err != nil {
		return nil, toolError("invalid_project_data", "Storyboard could not be validated")
	}
	snapshot := base + ":" + sceneID.String()
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(clips) {
		return nil, toolError("cursor_stale", "Clip cursor is stale")
	}
	end := min(offset+input.Limit, len(clips))
	items := make([]map[string]any, 0, end-offset)
	for index, value := range clips[offset:end] {
		assetIDs := make([]string, 0, len(value.AssetReferences))
		for _, reference := range value.AssetReferences {
			assetIDs = append(assetIDs, reference.AssetID.String())
		}
		items = append(items, map[string]any{"clip_id": value.Clip.ID.String(), "position": offset + index + 1, "title": value.Clip.Title, "planned_duration_ms": value.Clip.PlannedDurationSeconds * 1000, "generation_mode": value.Clip.GenerationMode, "asset_ids": assetIDs, "revision": value.Clip.Revision})
	}
	next := ""
	if end < len(clips) {
		next, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	payload, err := marshalToolPayload(map[string]any{"items": items, "scene_id": sceneID.String(), "storyboard_revision": storyboard.Storyboard.Revision, "has_more": end < len(clips), "next_cursor": next})
	if err == nil {
		state.readSet.observeSceneClipsPage(sceneID, storyboard.Storyboard.Revision, snapshot, offset, end, end == len(clips))
	}
	return payload, err
}

func (s *Server) snapshotBinding(route string, requireTurn bool) (bindingSnapshot, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	state := s.bindings[route]
	if state == nil {
		return bindingSnapshot{}, toolError("binding_unavailable", "Session binding is unavailable")
	}
	result := bindingSnapshot{scope: state.scope}
	if state.turn != nil {
		result.turn = *state.turn
		result.readSet = state.readSet
	}
	if requireTurn && (state.turn == nil || state.readSet == nil) {
		return bindingSnapshot{}, toolError("turn_not_active", "no active Agent Turn is bound to this Session")
	}
	return result, nil
}

func (s *Server) getEpisodeContext(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input emptyArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_episode_context does not accept arguments")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	return marshalToolPayload(map[string]any{
		"episode_id": episode.ID.String(), "sequence_number": episode.SequenceNumber,
		"title": episode.Title, "target_duration_ms": episode.TargetDuration.Milliseconds(),
		"revision": episode.Revision, "script_revision": episode.Script.Revision,
		"video_prompt_targets": videoPromptTargetContext(state, ""),
	})
}

func (s *Server) readScript(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input readScriptArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "read_script arguments are invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	snapshot := scriptSnapshot(episode.Script)
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	chunk, end, err := boundedScriptPage(episode.Script.Markdown, offset)
	if err != nil {
		return nil, err
	}
	hasMore := end < len(episode.Script.Markdown)
	nextCursor := ""
	if hasMore {
		nextCursor, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	payload, err := marshalToolPayload(map[string]any{"markdown": chunk, "script_revision": episode.Script.Revision, "has_more": hasMore, "next_cursor": nextCursor})
	if err != nil {
		return nil, err
	}
	state.readSet.observeScriptPage(episode.Script.Revision, snapshot, offset, end, !hasMore)
	return payload, nil
}

func (s *Server) listAssets(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input listAssetsArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 {
		return nil, toolError("invalid_arguments", "list_assets arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxAssetPageSize)
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	assets, err := s.queries.ListEpisodeAssets(ctx, projectapp.ListEpisodeAssetsInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, IncludeDeleted: false})
	if err != nil {
		return nil, mapQueryError(err)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset.ID.String() < assets[j].Asset.ID.String() })
	snapshot, err := assetListSnapshot(assets)
	if err != nil {
		return nil, toolError("invalid_project_data", "Asset list could not be validated")
	}
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(assets) {
		return nil, toolError("cursor_stale", "Asset list cursor is stale")
	}
	end := min(offset+input.Limit, len(assets))
	items := make([]map[string]any, 0, end-offset)
	for _, value := range assets[offset:end] {
		items = append(items, map[string]any{
			"asset_id": value.Asset.ID.String(), "identity_id": value.Identity.ID.String(),
			"kind": string(value.Identity.Kind), "name": value.Identity.Name,
			"revision": value.Asset.Revision,
		})
	}
	nextCursor := ""
	if end < len(assets) {
		nextCursor, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	payload, err := marshalToolPayload(map[string]any{"items": items, "next_cursor": nextCursor, "has_more": end < len(assets)})
	if err == nil {
		state.readSet.observeAssetListPage(snapshot, offset, end, end == len(assets))
	}
	return payload, err
}

func (s *Server) listReferenceMedia(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input listReferenceMediaArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 || (input.Kind != "" && input.Kind != "image" && input.Kind != "video" && input.Kind != "audio") {
		return nil, toolError("invalid_arguments", "list_reference_media arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxReferenceMediaPageSize)
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	all, err := s.queries.ListEpisodeReferenceMedia(ctx, projectapp.ListEpisodeReferenceMediaInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	values := make([]project.EpisodeReferenceMedia, 0, len(all))
	for _, value := range all {
		if input.Kind == "" || string(value.Artifact.Kind) == input.Kind {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Artifact.ID.String() < values[j].Artifact.ID.String() })
	snapshot, err := referenceMediaListSnapshot(values)
	if err != nil {
		return nil, toolError("invalid_project_data", "uploaded reference media could not be validated")
	}
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(values) {
		return nil, toolError("cursor_stale", "uploaded reference media cursor is stale")
	}
	end := min(offset+input.Limit, len(values))
	items := make([]map[string]any, 0, end-offset)
	for _, value := range values[offset:end] {
		trimStart, trimEnd := value.EffectiveTrimRange()
		items = append(items, map[string]any{
			"artifact_id": value.Artifact.ID.String(), "kind": string(value.Artifact.Kind), "name": value.Name,
			"description": value.Description, "duration_ms": value.EffectiveDurationMilliseconds(),
			"original_duration_ms": value.Artifact.DurationMilliseconds, "trim_start_ms": trimStart, "trim_end_ms": trimEnd,
			"mime_type": value.Artifact.MIMEType, "usage_count": value.UsageCount, "revision": value.Revision,
		})
	}
	nextCursor := ""
	if end < len(values) {
		nextCursor, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	return marshalToolPayload(map[string]any{"items": items, "next_cursor": nextCursor, "has_more": end < len(values)})
}

func (s *Server) listDeletedAssets(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input listAssetsArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 {
		return nil, toolError("invalid_arguments", "list_deleted_assets arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxAssetPageSize)
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	all, err := s.queries.ListEpisodeAssets(ctx, projectapp.ListEpisodeAssetsInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, IncludeDeleted: true})
	if err != nil {
		return nil, mapQueryError(err)
	}
	assets := make([]project.EpisodeAsset, 0, len(all))
	for _, value := range all {
		if value.Asset.IsDeleted() {
			assets = append(assets, value)
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset.ID.String() < assets[j].Asset.ID.String() })
	snapshot, err := deletedAssetListSnapshot(assets)
	if err != nil {
		return nil, toolError("invalid_project_data", "deleted Asset list could not be validated")
	}
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(assets) {
		return nil, toolError("cursor_stale", "deleted Asset list cursor is stale")
	}
	end := min(offset+input.Limit, len(assets))
	items := make([]map[string]any, 0, end-offset)
	for _, value := range assets[offset:end] {
		items = append(items, map[string]any{
			"asset_id": value.Asset.ID.String(), "identity_id": value.Identity.ID.String(),
			"kind": string(value.Identity.Kind), "name": value.Identity.Name,
			"revision": value.Asset.Revision, "deleted_at": value.Asset.DeletedAt,
		})
	}
	nextCursor := ""
	if end < len(assets) {
		nextCursor, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	return marshalToolPayload(map[string]any{"items": items, "next_cursor": nextCursor, "has_more": end < len(assets)})
}

func (s *Server) listPriorAssetCandidates(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input listPriorAssetsArguments
	if err := decodeStrict(arguments, &input); err != nil || input.Limit < 0 ||
		(input.Kind != "" && !project.AssetKind(input.Kind).IsValid()) ||
		(input.NameQuery != "" && project.ValidateAssetIdentityName(input.NameQuery) != nil) {
		return nil, toolError("invalid_arguments", "list_prior_asset_candidates arguments are invalid")
	}
	input.Limit = cappedPageSize(input.Limit, MaxPriorAssetPageSize)
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	values, err := s.queries.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, Kind: project.AssetKind(input.Kind), NameQuery: input.NameQuery})
	if err != nil {
		return nil, mapQueryError(err)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].EpisodeSequence != values[j].EpisodeSequence {
			return values[i].EpisodeSequence > values[j].EpisodeSequence
		}
		if values[i].Asset.Identity.Kind != values[j].Asset.Identity.Kind {
			return values[i].Asset.Identity.Kind < values[j].Asset.Identity.Kind
		}
		if values[i].Asset.Identity.Name != values[j].Asset.Identity.Name {
			return values[i].Asset.Identity.Name < values[j].Asset.Identity.Name
		}
		return values[i].Asset.Asset.ID.String() < values[j].Asset.Asset.ID.String()
	})
	snapshot, err := priorAssetListSnapshot(values)
	if err != nil {
		return nil, toolError("invalid_project_data", "prior Asset candidates could not be validated")
	}
	offset, err := s.consumeCursor(route, input.Cursor, snapshot)
	if err != nil {
		return nil, err
	}
	if offset > len(values) {
		return nil, toolError("cursor_stale", "prior Asset candidate cursor is stale")
	}
	end := min(offset+input.Limit, len(values))
	items := make([]map[string]any, 0, end-offset)
	for _, value := range values[offset:end] {
		items = append(items, map[string]any{"asset_id": value.Asset.Asset.ID.String(), "identity_id": value.Asset.Identity.ID.String(), "kind": string(value.Asset.Identity.Kind), "name": value.Asset.Identity.Name, "episode_sequence": value.EpisodeSequence, "revision": value.Asset.Asset.Revision})
	}
	nextCursor := ""
	if end < len(values) {
		nextCursor, err = s.createCursor(route, snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	return marshalToolPayload(map[string]any{"items": items, "next_cursor": nextCursor, "has_more": end < len(values)})
}

func (s *Server) getPriorAssetCandidate(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getAssetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_prior_asset_candidate arguments are invalid")
	}
	id, err := project.ParseAssetID(input.AssetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "asset_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	value, sequence, err := s.priorAssetCandidate(ctx, state, id)
	if err != nil {
		return nil, err
	}
	payload, err := marshalToolPayload(map[string]any{"asset_id": value.Asset.ID.String(), "identity_id": value.Identity.ID.String(), "kind": string(value.Identity.Kind), "name": value.Identity.Name, "prompt": value.Asset.Prompt, "revision": value.Asset.Revision, "episode_sequence": sequence})
	if err != nil {
		return nil, err
	}
	state.readSet.observePriorAsset(value, sequence)
	return payload, nil
}

func (s *Server) getAsset(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getAssetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_asset arguments are invalid")
	}
	id, err := project.ParseAssetID(input.AssetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "asset_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	value, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: id})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Asset.EpisodeID != state.scope.EpisodeID || value.Asset.IsDeleted() {
		if value.Asset.EpisodeID != state.scope.EpisodeID && !value.Asset.IsDeleted() {
			return nil, sourceAssetToolError("object_outside_scope", "get_asset only reads this Session's Episode. For an active earlier-Episode Asset, call get_prior_asset_candidate with this asset_id, then use source_asset_id in append_asset_operations to create an independent copy.", id)
		}
		return nil, toolError("object_outside_scope", "Asset is not active in this Session's Episode")
	}
	payload, err := marshalToolPayload(map[string]any{
		"asset_id": value.Asset.ID.String(), "identity_id": value.Identity.ID.String(),
		"kind": string(value.Identity.Kind), "name": value.Identity.Name,
		"prompt": value.Asset.Prompt, "revision": value.Asset.Revision,
	})
	if err != nil {
		return nil, err
	}
	state.readSet.observeAsset(value.Asset)
	return payload, nil
}

func (s *Server) getDeletedAsset(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getAssetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_deleted_asset arguments are invalid")
	}
	id, err := project.ParseAssetID(input.AssetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "asset_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	value, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: id})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Asset.EpisodeID != state.scope.EpisodeID || !value.Asset.IsDeleted() {
		return nil, toolError("object_outside_scope", "Asset is not deleted in this Session's Episode")
	}
	payload, err := marshalToolPayload(map[string]any{
		"asset_id": value.Asset.ID.String(), "identity_id": value.Identity.ID.String(),
		"kind": string(value.Identity.Kind), "name": value.Identity.Name,
		"prompt": value.Asset.Prompt, "revision": value.Asset.Revision, "deleted_at": value.Asset.DeletedAt,
	})
	if err != nil {
		return nil, err
	}
	state.readSet.observeDeletedAsset(value)
	return payload, nil
}

func (s *Server) getClip(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input getClipArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "get_clip arguments are invalid")
	}
	id, err := project.ParseClipID(input.ClipID)
	if err != nil {
		return nil, toolError("invalid_arguments", "clip_id is invalid")
	}
	fields, err := parseClipPromptFields(input.PromptFields)
	if err != nil {
		return nil, err
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	value, err := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: id})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Clip.EpisodeID != state.scope.EpisodeID || value.Clip.IsDeleted() {
		return nil, toolError("object_outside_scope", "Clip is not active in this Session's Episode")
	}
	result := map[string]any{
		"clip_id": value.Clip.ID.String(), "scene_id": value.Clip.SceneID.String(),
		"title": value.Clip.Title, "revision": value.Clip.Revision,
		"video_prompt_targets": videoPromptTargetContextForClip(state, value.Clip),
	}
	if fields[clipFirstPrompt] {
		result[string(clipFirstPrompt)] = value.Clip.FirstImagePrompt
	}
	if fields[clipLastPrompt] {
		result[string(clipLastPrompt)] = value.Clip.LastImagePrompt
	}
	if fields[clipFirstVideoPrompt] {
		result[string(clipFirstVideoPrompt)] = value.Clip.VideoPrompts.FirstImage
	}
	if fields[clipFirstLastVideoPrompt] {
		result[string(clipFirstLastVideoPrompt)] = value.Clip.VideoPrompts.FirstLastImage
	}
	if fields[clipReferenceVideoPrompt] {
		result[string(clipReferenceVideoPrompt)] = value.Clip.VideoPrompts.Reference
	}
	payload, err := marshalToolPayload(result)
	if err != nil {
		return nil, err
	}
	state.readSet.observeClip(value.Clip, fields)
	return payload, nil
}

type adaptationReference struct {
	Key                          string `json:"key"`
	Source                       string `json:"source"`
	Name                         string `json:"name"`
	Description                  string `json:"description"`
	MediaType                    string `json:"media_type"`
	ArtifactID                   string `json:"artifact_id"`
	BindingPosition              int    `json:"binding_position"`
	MediaReady                   bool   `json:"media_ready"`
	TargetSupported              bool   `json:"target_supported"`
	LibraryRevision              int64  `json:"library_revision,omitempty"`
	OriginalDurationMilliseconds int64  `json:"original_duration_ms,omitempty"`
	DurationMilliseconds         int64  `json:"duration_ms,omitempty"`
	TrimStartMilliseconds        int64  `json:"trim_start_ms,omitempty"`
	TrimEndMilliseconds          int64  `json:"trim_end_ms,omitempty"`
}

func (s *Server) loadVideoPromptAdaptationReferences(ctx context.Context, state bindingSnapshot, value project.ClipDetails, mode project.ClipGenerationMode, target videoPromptTarget) ([]adaptationReference, error) {
	if mode != project.ClipGenerationReference {
		return []adaptationReference{}, nil
	}
	result := make([]adaptationReference, 0, len(value.AssetReferences))
	for _, reference := range value.AssetReferences {
		if reference.AssetDeleted {
			continue
		}
		asset, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: reference.AssetID})
		if err != nil || asset.Asset.IsDeleted() {
			return nil, toolError("project_read_failed", "a bound Asset could not be read for video Prompt adaptation")
		}
		entry := adaptationReference{
			Key: "asset:" + reference.AssetID.String(), Source: "asset", Name: reference.Name,
			Description: asset.Asset.Prompt, MediaType: string(project.MediaKindImage), BindingPosition: reference.Position, TargetSupported: true,
		}
		if slotID, slotErr := project.FixedMediaSlotID(reference.AssetID.String(), project.MediaSlotAssetPrimaryImage); slotErr == nil {
			if media, mediaErr := s.queries.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: state.scope.ProjectID, MediaSlotID: slotID}); mediaErr == nil && media.Current != nil {
				entry.MediaReady = true
				entry.ArtifactID = media.Current.ArtifactID.String()
			}
		}
		result = append(result, entry)
	}
	uploads, err := s.queries.ListClipReferenceMedia(ctx, projectapp.ListClipReferenceMediaInput{
		ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, ClipID: value.Clip.ID,
	})
	if err != nil {
		return nil, toolError("project_read_failed", "uploaded media attached to the video unit could not be read")
	}
	library, err := s.queries.ListEpisodeReferenceMedia(ctx, projectapp.ListEpisodeReferenceMediaInput{
		ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID,
	})
	if err != nil {
		return nil, toolError("project_read_failed", "uploaded media descriptions could not be read")
	}
	libraryByArtifact := make(map[project.ArtifactID]project.EpisodeReferenceMedia, len(library))
	for _, item := range library {
		libraryByArtifact[item.Artifact.ID] = item
	}
	for position, artifact := range uploads {
		item, ok := libraryByArtifact[artifact.ID]
		if !ok || item.EpisodeID != state.scope.EpisodeID || item.DeletedAt != nil || item.Artifact.ProjectID != state.scope.ProjectID || item.Artifact.Kind != artifact.Kind {
			// Bound uploads must resolve to the active Episode library entry.
			// Falling back to immutable-source metadata could silently discard
			// a crop after a concurrent deletion or an orphaned binding.
			return nil, toolError("observation_stale", "an uploaded reference is no longer active in this Episode's media library; refresh the Clip bindings and adaptation context")
		}
		trimStart, trimEnd := item.EffectiveTrimRange()
		result = append(result, adaptationReference{
			Key: "upload:" + artifact.ID.String(), Source: "upload", Name: item.Name, Description: item.Description,
			MediaType: string(artifact.Kind), ArtifactID: artifact.ID.String(),
			BindingPosition: len(value.AssetReferences) + position + 1, MediaReady: true,
			TargetSupported: adaptationTargetSupportsMedia(target, artifact.Kind), LibraryRevision: item.Revision,
			OriginalDurationMilliseconds: artifact.DurationMilliseconds, DurationMilliseconds: item.EffectiveDurationMilliseconds(),
			TrimStartMilliseconds: trimStart, TrimEndMilliseconds: trimEnd,
		})
	}
	return result, nil
}

func adaptationTargetSupportsMedia(target videoPromptTarget, kind project.MediaKind) bool {
	return kind == project.MediaKindImage || kind == project.MediaKindVideo || kind == project.MediaKindAudio
}

func adaptationReferenceSetHash(references []adaptationReference) string {
	encoded, _ := json.Marshal(references)
	return videoPromptHash(string(encoded))
}

func adaptationReferenceContext(references []adaptationReference, target videoPromptTarget, saved []videoPromptReference) []map[string]any {
	savedByKey := make(map[string]videoPromptReference, len(saved))
	for _, reference := range saved {
		savedByKey[reference.Key] = reference
	}
	indexes := map[string]int{}
	result := make([]map[string]any, 0, len(references))
	for _, reference := range references {
		label := ""
		if reference.MediaReady && reference.TargetSupported {
			indexes[reference.MediaType]++
			label = adaptationModelLabel(target, reference.MediaType, indexes[reference.MediaType])
		}
		entry := map[string]any{
			"reference_key": reference.Key, "source": reference.Source, "media_type": reference.MediaType,
			"name": reference.Name, "description": reference.Description, "prompt_token": "@" + reference.Name,
			"model_label": label, "media_ready": reference.MediaReady, "target_supported": reference.TargetSupported, "binding_position": reference.BindingPosition,
		}
		if reference.ArtifactID != "" {
			entry["artifact_id"] = reference.ArtifactID
		}
		if reference.Source == "upload" {
			entry["original_duration_ms"] = reference.OriginalDurationMilliseconds
			entry["duration_ms"] = reference.DurationMilliseconds
			entry["trim_start_ms"] = reference.TrimStartMilliseconds
			entry["trim_end_ms"] = reference.TrimEndMilliseconds
			entry["library_revision"] = reference.LibraryRevision
		}
		if previous, ok := savedByKey[reference.Key]; ok {
			entry["saved_purposes"] = previous.Purposes
			entry["saved_note"] = previous.Note
		}
		result = append(result, entry)
	}
	return result
}

func adaptationClarificationCandidates(references []adaptationReference) []map[string]any {
	result := []map[string]any{}
	for _, reference := range references {
		if !reference.TargetSupported {
			result = append(result, map[string]any{
				"reference_key": reference.Key, "name": reference.Name,
				"reason": "unsupported_media_type", "message": "The selected target model cannot use this attached media type. Ask the user to remove it from the video unit or choose a compatible model; do not create a ChangeSet.",
			})
		} else if reference.Source == "upload" && strings.TrimSpace(reference.Description) == "" {
			result = append(result, map[string]any{
				"reference_key": reference.Key, "name": reference.Name,
				"reason": "missing_description", "message": "The uploaded medium has no description. Ask what it should control only when its purpose cannot be inferred safely from its name and the source Prompt.",
			})
		}
	}
	return result
}

func adaptationModelLabel(target videoPromptTarget, mediaType string, index int) string {
	kind := map[string]string{
		string(project.MediaKindImage): "Image", string(project.MediaKindVideo): "Video", string(project.MediaKindAudio): "Audio",
	}[mediaType]
	if target.Family == "h3" {
		if mediaType == string(project.MediaKindImage) {
			kind = "Picture"
		}
		return fmt.Sprintf("<%s %d>", kind, index)
	}
	if target.Family == "seedance" {
		if target.GeneratorID == string(project.GeneratorVideoSeedanceGlobal) {
			kind = map[string]string{string(project.MediaKindImage): "Image", string(project.MediaKindVideo): "Video", string(project.MediaKindAudio): "Audio"}[mediaType]
			return fmt.Sprintf("@%s %d", kind, index)
		}
		kind = map[string]string{string(project.MediaKindImage): "图片", string(project.MediaKindVideo): "视频", string(project.MediaKindAudio): "音频"}[mediaType]
		return fmt.Sprintf("@%s%d", kind, index)
	}
	kind = map[string]string{string(project.MediaKindImage): "图", string(project.MediaKindVideo): "视频", string(project.MediaKindAudio): "音频"}[mediaType]
	return fmt.Sprintf("%s%d / %s %d", kind, index, map[string]string{string(project.MediaKindImage): "Image", string(project.MediaKindVideo): "Video", string(project.MediaKindAudio): "Audio"}[mediaType], index)
}

func (s *Server) getVideoPromptAdaptationContext(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input videoPromptAdaptationTargetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "video Prompt adaptation target is invalid")
	}
	clipID, err := project.ParseClipID(input.ClipID)
	mode := project.ClipGenerationMode(input.GenerationMode)
	target, targetErr := deriveVideoPromptTarget(input.GeneratorID, input.ProviderModelID, mode)
	if err != nil || targetErr != nil {
		return nil, toolError("invalid_arguments", "video Prompt adaptation target is unsupported or incompatible")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if state.turn.Focus.ClipID != "" && state.turn.Focus.ClipID != clipID {
		return nil, toolError("object_outside_scope", "adaptation Clip does not match the video unit focused when the user sent this Turn")
	}
	input.PromptFamily = target.Family
	input.PromptProfile = target.Profile
	value, err := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: clipID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Clip.EpisodeID != state.scope.EpisodeID || value.Clip.IsDeleted() {
		return nil, toolError("object_outside_scope", "Clip is not active in this Session's Episode")
	}
	if state.turn.VideoPromptTarget != nil {
		if _, err := resolveVideoPromptWriteTarget(state, clipID, mode, input.GeneratorID, input.ProviderModelID, value.Clip.VideoPrompts.ForMode(mode)); err != nil {
			return nil, err
		}
	}
	if err := s.freezeVideoPromptAdaptationTarget(route, input); err != nil {
		return nil, err
	}
	field, ok := clipVideoPromptField(mode)
	if !ok {
		return nil, toolError("invalid_arguments", "generation_mode is invalid")
	}
	fields := map[clipPromptField]bool{field: true, clipFirstPrompt: true, clipLastPrompt: true}
	set := parseVideoPromptSet(value.Clip.VideoPrompts.ForMode(mode))
	sourceFamily, sourcePrompt := set.sourcePrompt()
	sourceMode := mode
	if strings.TrimSpace(sourcePrompt) == "" {
		for _, candidateMode := range []project.ClipGenerationMode{project.ClipGenerationReference, project.ClipGenerationFirstImage, project.ClipGenerationFirstLastImage} {
			if candidateMode == mode {
				continue
			}
			candidateSet := parseVideoPromptSet(value.Clip.VideoPrompts.ForMode(candidateMode))
			candidateFamily, candidatePrompt := candidateSet.sourcePrompt()
			if strings.TrimSpace(candidatePrompt) == "" {
				continue
			}
			sourceMode, sourceFamily, sourcePrompt = candidateMode, candidateFamily, candidatePrompt
			if sourceField, exists := clipVideoPromptField(candidateMode); exists {
				fields[sourceField] = true
			}
			break
		}
	}
	state.readSet.observeClip(value.Clip, fields)
	references, err := s.loadVideoPromptAdaptationReferences(ctx, state, value, mode, target)
	if err != nil {
		return nil, err
	}
	currentTarget := set.Families[target.Family]
	referenceSetHash := adaptationReferenceSetHash(references)
	return marshalToolPayload(map[string]any{
		"clip": map[string]any{
			"clip_id": value.Clip.ID.String(), "scene_id": value.Clip.SceneID.String(), "revision": value.Clip.Revision,
			"title": value.Clip.Title, "description": value.Clip.Description, "dialogue": value.Clip.Dialogue,
			"creative_intent": value.Clip.CreativeIntent, "planned_duration_seconds": value.Clip.PlannedDurationSeconds,
			"first_image_prompt": value.Clip.FirstImagePrompt, "last_image_prompt": value.Clip.LastImagePrompt,
		},
		"target": map[string]any{
			"generation_mode": string(mode), "generator_id": input.GeneratorID, "provider_model_id": input.ProviderModelID,
			"prompt_family": target.Family, "prompt_profile": target.Profile,
		},
		"source": map[string]any{
			"generation_mode": string(sourceMode), "prompt_family": sourceFamily, "prompt": sourcePrompt, "prompt_hash": videoPromptHash(sourcePrompt),
		},
		"current_target_prompt":    currentTarget.Prompt,
		"prompt_instructions":      videoPromptFormatGuidance(target),
		"references":               adaptationReferenceContext(references, target, currentTarget.References),
		"reference_set_hash":       referenceSetHash,
		"clarification_candidates": adaptationClarificationCandidates(references),
	})
}

func (s *Server) beginChangeSet(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input beginChangeSetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "begin_change_set arguments are invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	s.mutex.RLock()
	binding := s.bindings[route]
	declaredProjectChange := binding != nil && binding.intent == "project_change" && binding.target != "" && binding.target != "none"
	s.mutex.RUnlock()
	if !declaredProjectChange {
		return nil, toolError("intent_required", "declare a project_change intent and target before beginning a ChangeSet")
	}
	result, err := s.drafts.BeginDraft(ctx, changesetapp.BeginDraftInput{
		ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID,
		AgentSessionID: state.scope.SessionID, AgentTurnID: state.turn.AgentTurnID,
		CodexThreadID: state.turn.CodexThreadID, CodexTurnID: state.turn.CodexTurnID,
		Title: input.Title, Summary: input.Summary, OperationKey: input.OperationKey,
	})
	if err != nil {
		return nil, mapDraftError(err)
	}
	if err := s.recordDraft(route, state.turn.AgentTurnID, result.ChangeSet.ID, result.ChangeSet.Status != changeset.StatusDraft); err != nil {
		if result.ChangeSet.Status == changeset.StatusDraft {
			_ = s.cleanup.DiscardDraft(ctx, state.scope.ProjectID, result.ChangeSet.ID)
		}
		return nil, err
	}
	return marshalChangeSetResult(result)
}

func (s *Server) appendPromptOperations(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendPromptOperationsArguments
	if err := decodeStrict(arguments, &input); err != nil || len(input.Operations) != 1 {
		return nil, toolError("invalid_arguments", "append_prompt_operations arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "ChangeSet identity or revision is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	drafts := make([]changesetapp.OperationDraft, 0, len(input.Operations))
	for _, operation := range input.Operations {
		draft, err := s.buildPromptDraft(ctx, state, operation)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{
		ProjectID: state.scope.ProjectID, ChangeSetID: id,
		ExpectedRevision: input.ExpectedChangeSetRevision, Operations: drafts,
		OperationKey: input.OperationKey,
	})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) appendVideoPromptAdaptation(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendVideoPromptAdaptationArguments
	if err := decodeStrict(arguments, &input); err != nil || input.ExpectedChangeSetRevision < 1 || strings.TrimSpace(input.OperationKey) == "" {
		return nil, toolError("invalid_arguments", "append_video_prompt_adaptation arguments are invalid")
	}
	changeSetID, err := changeset.ParseChangeSetID(input.ChangeSetID)
	clipID, clipErr := project.ParseClipID(input.ClipID)
	mode := project.ClipGenerationMode(input.GenerationMode)
	target, targetErr := deriveVideoPromptTarget(input.GeneratorID, input.ProviderModelID, mode)
	if err != nil || clipErr != nil || targetErr != nil || input.PromptFamily != target.Family || input.PromptProfile != target.Profile {
		return nil, toolError("invalid_arguments", "video Prompt adaptation identity, family, profile, or target is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if state.turn.Focus.ClipID != "" && state.turn.Focus.ClipID != clipID {
		return nil, toolError("object_outside_scope", "adaptation Clip does not match the video unit focused when the user sent this Turn")
	}
	if err := s.requireFrozenVideoPromptAdaptationTarget(route, input.videoPromptAdaptationTargetArguments); err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, changeSetID); err != nil {
		return nil, err
	}
	value, err := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: clipID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Clip.EpisodeID != state.scope.EpisodeID || value.Clip.IsDeleted() {
		return nil, toolError("object_outside_scope", "Clip is not active in this Session's Episode")
	}
	set := parseVideoPromptSet(value.Clip.VideoPrompts.ForMode(mode))
	sourceMode := project.ClipGenerationMode(input.SourceGenerationMode)
	if !sourceMode.IsValid() {
		return nil, toolError("invalid_arguments", "source_generation_mode is invalid")
	}
	if input.SourceFamily != "legacy" && input.SourceFamily != "h3" && input.SourceFamily != "seedance" && input.SourceFamily != "wan3" {
		return nil, toolError("invalid_arguments", "source_family is invalid")
	}
	sourceSet := parseVideoPromptSet(value.Clip.VideoPrompts.ForMode(sourceMode))
	if videoPromptHash(sourceSet.promptForSource(input.SourceFamily)) != input.SourcePromptHash {
		return nil, toolError("observation_stale", "the source Prompt changed after the adaptation context was read")
	}
	currentReferences, err := s.loadVideoPromptAdaptationReferences(ctx, state, value, mode, target)
	if err != nil {
		return nil, err
	}
	if adaptationReferenceSetHash(currentReferences) != input.ReferenceSetHash {
		return nil, toolError("observation_stale", "the video unit reference media changed after the adaptation context was read")
	}
	beforeAssetIDs := make([]project.AssetID, 0, len(value.AssetReferences))
	for _, reference := range value.AssetReferences {
		if !reference.AssetDeleted {
			beforeAssetIDs = append(beforeAssetIDs, reference.AssetID)
		}
	}
	beforeReferenceIDs := make([]project.ArtifactID, 0, len(currentReferences))
	for _, reference := range currentReferences {
		if reference.Source != "upload" {
			continue
		}
		artifactID, parseErr := project.ParseArtifactID(reference.ArtifactID)
		if parseErr != nil {
			return nil, toolError("invalid_project_data", "an uploaded reference Artifact ID is invalid")
		}
		beforeReferenceIDs = append(beforeReferenceIDs, artifactID)
	}
	desiredReferences := currentReferences
	afterReferenceIDs := append([]project.ArtifactID(nil), beforeReferenceIDs...)
	if input.ReferenceArtifactIDs != nil {
		if mode != project.ClipGenerationReference {
			return nil, toolError("invalid_arguments", "uploaded-media bindings can change only for a reference-mode adaptation")
		}
		library, queryErr := s.queries.ListEpisodeReferenceMedia(ctx, projectapp.ListEpisodeReferenceMediaInput{
			ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID,
		})
		if queryErr != nil {
			return nil, mapQueryError(queryErr)
		}
		libraryByID := make(map[project.ArtifactID]project.EpisodeReferenceMedia, len(library))
		for _, item := range library {
			if item.DeletedAt == nil {
				libraryByID[item.Artifact.ID] = item
			}
		}
		desiredReferences = make([]adaptationReference, 0, len(beforeAssetIDs)+len(*input.ReferenceArtifactIDs))
		for _, reference := range currentReferences {
			if reference.Source == "asset" {
				desiredReferences = append(desiredReferences, reference)
			}
		}
		afterReferenceIDs = make([]project.ArtifactID, 0, len(*input.ReferenceArtifactIDs))
		seen := make(map[project.ArtifactID]struct{}, len(*input.ReferenceArtifactIDs))
		for position, raw := range *input.ReferenceArtifactIDs {
			artifactID, parseErr := project.ParseArtifactID(raw)
			if parseErr != nil {
				return nil, toolError("invalid_arguments", "reference_artifact_ids contains an invalid Artifact ID")
			}
			if _, duplicate := seen[artifactID]; duplicate {
				return nil, toolError("invalid_arguments", "reference_artifact_ids contains a duplicate Artifact")
			}
			item, exists := libraryByID[artifactID]
			if !exists {
				return nil, toolError("object_outside_scope", "uploaded reference media is not active in this Session's Episode")
			}
			seen[artifactID] = struct{}{}
			afterReferenceIDs = append(afterReferenceIDs, artifactID)
			trimStart, trimEnd := item.EffectiveTrimRange()
			desiredReferences = append(desiredReferences, adaptationReference{
				Key: "upload:" + artifactID.String(), Source: "upload", Name: item.Name, Description: item.Description,
				MediaType: string(item.Artifact.Kind), ArtifactID: artifactID.String(),
				BindingPosition: len(beforeAssetIDs) + position + 1, MediaReady: true,
				TargetSupported: adaptationTargetSupportsMedia(target, item.Artifact.Kind), LibraryRevision: item.Revision,
				OriginalDurationMilliseconds: item.Artifact.DurationMilliseconds, DurationMilliseconds: item.EffectiveDurationMilliseconds(),
				TrimStartMilliseconds: trimStart, TrimEndMilliseconds: trimEnd,
			})
		}
	}
	orderedReferences, err := validateVideoPromptAdaptationReferences(input.References, desiredReferences, target, mode, input.Prompt)
	if err != nil {
		return nil, err
	}
	promptTokens := make([]string, 0, len(desiredReferences))
	for _, reference := range desiredReferences {
		promptTokens = append(promptTokens, reference.Name)
	}
	prompt, err := validateAdaptedVideoPrompt(input.Prompt, target, mode, promptTokens)
	if err != nil {
		return nil, err
	}
	stored, err := set.withVariant(target, input.GeneratorID, input.ProviderModelID, prompt, orderedReferences)
	if err != nil {
		return nil, toolError("invalid_arguments", err.Error())
	}
	draft, err := state.readSet.clipDraft(value.Clip, beforeAssetIDs, beforeAssetIDs, beforeReferenceIDs, afterReferenceIDs, nil, nil, mode, &stored)
	if err != nil {
		return nil, mapReadSetError(err)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{
		ProjectID: state.scope.ProjectID, ChangeSetID: changeSetID, ExpectedRevision: input.ExpectedChangeSetRevision,
		Operations: []changesetapp.OperationDraft{draft}, OperationKey: input.OperationKey,
	})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) freezeVideoPromptAdaptationTarget(route string, target videoPromptAdaptationTargetArguments) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	state := s.bindings[route]
	if state == nil || state.turn == nil {
		return ErrTurnNotActive
	}
	if state.videoPromptTarget == nil {
		copy := target
		state.videoPromptTarget = &copy
		return nil
	}
	if *state.videoPromptTarget != target {
		return toolError("adaptation_target_conflict", "this Turn already confirmed a different video Prompt adaptation target")
	}
	return nil
}

func (s *Server) requireFrozenVideoPromptAdaptationTarget(route string, target videoPromptAdaptationTargetArguments) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	state := s.bindings[route]
	if state == nil || state.turn == nil {
		return ErrTurnNotActive
	}
	if state.videoPromptTarget == nil {
		return toolError("object_not_observed", "call get_video_prompt_adaptation_context before appending the adaptation")
	}
	if *state.videoPromptTarget != target {
		return toolError("adaptation_target_conflict", "Clip, generation mode, generator, provider model, family, and profile must match the confirmed adaptation context")
	}
	return nil
}

func validateVideoPromptAdaptationReferences(raw []videoPromptReference, available []adaptationReference, target videoPromptTarget, mode project.ClipGenerationMode, _ string) ([]videoPromptReference, error) {
	if mode != project.ClipGenerationReference {
		if len(raw) != 0 {
			return nil, toolError("invalid_arguments", "keyframe-mode adaptations must not include reference media")
		}
		return []videoPromptReference{}, nil
	}
	expected := make(map[string]adaptationReference, len(available))
	for _, reference := range available {
		if reference.MediaReady && !reference.TargetSupported {
			return nil, toolError("invalid_arguments", "the selected model does not support one or more attached reference media types; remove them or choose another model")
		}
		if reference.MediaReady {
			expected[reference.Key] = reference
		}
	}
	limit := map[string]int{"h3": 9, "seedance": 50, "wan3": 20}[target.Family]
	if target.Family == "seedance" && target.ProviderModelID != "" && !strings.Contains(target.ProviderModelID, "2-5") {
		limit = 15
	}
	if len(expected) > limit {
		return nil, toolError("invalid_arguments", "the video unit has more ready reference media than the selected model supports")
	}
	typeLimits := map[string]int{}
	if target.Family == "seedance" {
		typeLimits = map[string]int{string(project.MediaKindImage): 30, string(project.MediaKindVideo): 10, string(project.MediaKindAudio): 10}
		if target.ProviderModelID != "" && !strings.Contains(target.ProviderModelID, "2-5") {
			typeLimits = map[string]int{string(project.MediaKindImage): 9, string(project.MediaKindVideo): 3, string(project.MediaKindAudio): 3}
		}
	} else if target.Family == "wan3" {
		typeLimits = map[string]int{string(project.MediaKindImage): 10, string(project.MediaKindVideo): 5, string(project.MediaKindAudio): 5}
	}
	typeCounts := map[string]int{}
	for _, reference := range expected {
		typeCounts[reference.MediaType]++
		if maximum := typeLimits[reference.MediaType]; maximum > 0 && typeCounts[reference.MediaType] > maximum {
			return nil, toolError("invalid_arguments", "the video unit exceeds the selected model's limit for one reference media type")
		}
	}
	if len(raw) != len(expected) {
		return nil, toolError("invalid_arguments", "references must include every ready bound Asset and uploaded medium attached to the video unit")
	}
	validPurpose := map[string]bool{
		"identity": true, "appearance": true, "scene": true, "object": true, "style": true,
		"composition": true, "motion": true, "camera": true, "voice": true, "sound": true,
		"music": true, "other": true,
	}
	seen := make(map[string]struct{}, len(raw))
	result := make([]videoPromptReference, 0, len(raw))
	for _, reference := range raw {
		_, ok := expected[reference.Key]
		_, duplicate := seen[reference.Key]
		if !ok || duplicate || len(reference.Purposes) == 0 || len(reference.Purposes) > 4 {
			return nil, toolError("invalid_arguments", "references contain an unavailable, duplicate, or purposeless reference_key")
		}
		purposeSeen := map[string]struct{}{}
		for _, purpose := range reference.Purposes {
			_, repeated := purposeSeen[purpose]
			if !validPurpose[purpose] || repeated {
				return nil, toolError("invalid_arguments", "reference purposes are invalid or duplicated")
			}
			purposeSeen[purpose] = struct{}{}
		}
		note := strings.TrimSpace(project.NormalizeStoryboardText(reference.Note))
		if utf8.RuneCountInString(note) > 500 || strings.ContainsRune(note, '\x00') {
			return nil, toolError("invalid_arguments", "a reference note is invalid")
		}
		seen[reference.Key] = struct{}{}
		result = append(result, videoPromptReference{Key: reference.Key, Purposes: append([]string(nil), reference.Purposes...), Note: note})
	}
	return result, nil
}

func validateAdaptedVideoPrompt(value string, _ videoPromptTarget, _ project.ClipGenerationMode, _ []string) (string, error) {
	prompt := strings.TrimSpace(project.NormalizeStoryboardText(value))
	if prompt == "" {
		return "", toolError("invalid_arguments", "adapted video Prompt must not be empty")
	}
	return prompt, nil
}

func (s *Server) appendAssetOperations(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendAssetOperationsArguments
	if err := decodeStrict(arguments, &input); err != nil || len(input.Operations) == 0 || len(input.Operations) > MaxAssetOperationsPerChangeSet {
		return nil, toolError("invalid_arguments", "append_asset_operations arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "ChangeSet identity or revision is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	assets, err := s.queries.ListEpisodeAssets(ctx, projectapp.ListEpisodeAssetsInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, IncludeDeleted: false})
	if err != nil {
		return nil, mapQueryError(err)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset.ID.String() < assets[j].Asset.ID.String() })
	listSnapshot, err := assetListSnapshot(assets)
	if err != nil {
		return nil, toolError("invalid_project_data", "Asset list could not be validated")
	}
	scriptState := scriptSnapshot(episode.Script)
	drafts := make([]changesetapp.OperationDraft, 0, len(input.Operations))
	seen := make(map[string]struct{}, len(input.Operations))
	for _, requested := range input.Operations {
		var source *project.EpisodeAsset
		var sourceSequence int
		var kind project.AssetKind
		var name, prompt string
		if requested.SourceAssetID == "" {
			kind = project.AssetKind(requested.Kind)
			if requested.Prompt != nil {
				prompt = project.NormalizeAssetPrompt(*requested.Prompt)
			}
			if !kind.IsValid() || requested.Prompt == nil || project.ValidateAssetIdentityName(requested.Name) != nil || project.ValidateAssetPrompt(prompt) != nil {
				return nil, toolError("invalid_arguments", "new Asset fields are invalid")
			}
			name = requested.Name
		} else {
			if requested.Kind != "" || requested.Name != "" {
				return nil, toolError("invalid_arguments", "derived Asset fields are invalid")
			}
			sourceID, parseErr := project.ParseAssetID(requested.SourceAssetID)
			if parseErr != nil {
				return nil, toolError("invalid_arguments", "source_asset_id is invalid")
			}
			value, sequence, readErr := s.priorAssetCandidate(ctx, state, sourceID)
			if readErr != nil {
				return nil, readErr
			}
			source, sourceSequence = &value, sequence
			kind, name, prompt = value.Identity.Kind, value.Identity.Name, value.Asset.Prompt
			if requested.Prompt != nil {
				prompt = project.NormalizeAssetPrompt(*requested.Prompt)
			}
			if project.ValidateAssetPrompt(prompt) != nil {
				return nil, toolError("invalid_arguments", "derived Asset prompt is invalid")
			}
		}
		draft, draftErr := state.readSet.assetCreationDraft(episode, scriptState, listSnapshot, assets, kind, name, prompt, source, sourceSequence)
		if draftErr != nil {
			if source != nil && errors.Is(draftErr, ErrSourceAssetNotObserved) {
				return nil, sourceAssetToolError("source_asset_not_observed", "Read the source with get_prior_asset_candidate in this Turn before retrying append_asset_operations with source_asset_id. Listing prior candidates or reading current Episode Assets does not read the historical source.", source.Asset.ID)
			}
			if source != nil && errors.Is(draftErr, ErrObservationStale) {
				return nil, sourceAssetToolError("observation_stale", "The historical source changed after it was read. Call get_prior_asset_candidate again in this Turn, then retry append_asset_operations with source_asset_id.", source.Asset.ID)
			}
			return nil, mapReadSetError(draftErr)
		}
		if source == nil {
			if err := s.requireNewAssetIdentity(ctx, state, episode, kind, name); err != nil {
				return nil, err
			}
		}
		key := draft.AssetCreation.IdentityID.String() + ":" + string(kind) + ":" + name
		if _, duplicate := seen[key]; duplicate {
			return nil, toolError("invalid_arguments", "Asset operations contain a duplicate identity")
		}
		seen[key] = struct{}{}
		drafts = append(drafts, draft)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: state.scope.ProjectID, ChangeSetID: id, ExpectedRevision: input.ExpectedChangeSetRevision, Operations: drafts, OperationKey: input.OperationKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

// requireNewAssetIdentity directs a mistaken same-name creation to the explicit
// read-before-derive workflow instead of leaving the model with a generic conflict.
func (s *Server) requireNewAssetIdentity(ctx context.Context, state bindingSnapshot, episode project.Episode, kind project.AssetKind, name string) error {
	values, err := s.queries.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{
		ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, Kind: kind, NameQuery: name,
	})
	if err != nil {
		return mapQueryError(err)
	}
	var match *projectapp.PriorAssetCandidate
	for i := range values {
		candidate := &values[i]
		if candidate.Asset.Identity.Kind != kind || candidate.Asset.Identity.Name != name ||
			candidate.Asset.Asset.IsDeleted() || candidate.EpisodeSequence >= episode.SequenceNumber {
			continue
		}
		if match == nil || candidate.EpisodeSequence > match.EpisodeSequence ||
			(candidate.EpisodeSequence == match.EpisodeSequence && candidate.Asset.Asset.ID.String() < match.Asset.Asset.ID.String()) {
			match = candidate
		}
	}
	if match == nil {
		return nil
	}
	return sourceAssetToolError("prior_asset_reuse_required", fmt.Sprintf("An earlier-Episode %s named %q already exists. Call get_prior_asset_candidate with asset_id=%q, then retry append_asset_operations with source_asset_id=%q instead of kind/name. You may supply this Episode's prompt. This creates an independent Episode Asset without changing the source; do not skip the Asset or rename it to bypass the existing identity.", kind, name, match.Asset.Asset.ID, match.Asset.Asset.ID), match.Asset.Asset.ID)
}

func sourceAssetToolError(code, message string, id project.AssetID) error {
	return &codedToolError{code: code, message: message, sourceAssetID: id.String(), nextTool: "get_prior_asset_candidate"}
}

func (s *Server) restoreAsset(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input restoreAssetArguments
	if err := decodeStrict(arguments, &input); err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "restore_asset arguments are invalid")
	}
	changeSetID, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "ChangeSet identity is invalid")
	}
	assetID, err := project.ParseAssetID(input.AssetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "asset_id is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, changeSetID); err != nil {
		return nil, err
	}
	value, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: assetID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if value.Asset.EpisodeID != state.scope.EpisodeID || !value.Asset.IsDeleted() {
		return nil, toolError("object_outside_scope", "Asset is not deleted in this Session's Episode")
	}
	draft, err := state.readSet.assetRestorationDraft(value)
	if err != nil {
		return nil, mapReadSetError(err)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{
		ProjectID: state.scope.ProjectID, ChangeSetID: changeSetID, ExpectedRevision: input.ExpectedChangeSetRevision,
		Operations: []changesetapp.OperationDraft{draft}, OperationKey: input.OperationKey,
	})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) appendStoryboardOperations(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendStoryboardOperationsArguments
	if err := decodeStrict(arguments, &input); err != nil || len(input.Operations) == 0 || len(input.Operations) > changeset.MaxAppendBatch {
		return nil, toolError("invalid_arguments", "append_storyboard_operations arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "ChangeSet identity or revision is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	storyboard, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	storyboardState, err := storyboardSnapshot(storyboard.Storyboard)
	if err != nil {
		return nil, toolError("invalid_project_data", "Storyboard could not be validated")
	}
	createdSceneRefs := make(map[string]struct{})
	existingSceneIDs := make(map[project.SceneID]struct{})
	clipCount := 0
	for _, operation := range input.Operations {
		switch changeset.OperationType(operation.Type) {
		case changeset.CreateScene:
			createdSceneRefs[operation.Ref] = struct{}{}
		case changeset.CreateClip:
			clipCount++
			if (operation.SceneRef == "") == (operation.SceneID == "") {
				return nil, toolError("invalid_arguments", "create_clip requires exactly one of scene_ref or scene_id")
			}
			if operation.SceneID != "" {
				sceneID, parseErr := project.ParseSceneID(operation.SceneID)
				if parseErr != nil {
					return nil, toolError("invalid_arguments", "create_clip scene_id is invalid")
				}
				existingSceneIDs[sceneID] = struct{}{}
			}
		}
	}
	if clipCount == 0 || len(createdSceneRefs) > 1 || len(existingSceneIDs) > 1 || (len(createdSceneRefs) == 0) == (len(existingSceneIDs) == 0) {
		return nil, toolError("invalid_arguments", "Storyboard operations must target exactly one new or existing Scene and create at least one Clip")
	}
	var existingScene *project.StoryboardScene
	if len(existingSceneIDs) == 1 {
		var targetID project.SceneID
		for id := range existingSceneIDs {
			targetID = id
		}
		for index := range storyboard.Storyboard.Scenes {
			if storyboard.Storyboard.Scenes[index].Scene.ID == targetID {
				existingScene = &storyboard.Storyboard.Scenes[index]
				break
			}
		}
		if existingScene == nil {
			return nil, toolError("object_outside_scope", "target Scene is not active in this Session's Episode")
		}
		if err := state.readSet.existingSceneClipsReady(targetID, storyboard.Storyboard.Revision, storyboardState+":"+targetID.String()); err != nil {
			return nil, mapReadSetError(err)
		}
	}
	assetsByID := make(map[project.AssetID]project.EpisodeAsset)
	for _, operation := range input.Operations {
		if changeset.OperationType(operation.Type) != changeset.AddClipAssetRef {
			continue
		}
		assetID, parseErr := project.ParseAssetID(operation.AssetID)
		if parseErr != nil {
			return nil, toolError("invalid_arguments", "Storyboard Asset reference is invalid")
		}
		if _, exists := assetsByID[assetID]; exists {
			continue
		}
		value, readErr := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: assetID})
		if readErr != nil {
			return nil, mapQueryError(readErr)
		}
		if value.Asset.EpisodeID != state.scope.EpisodeID || value.Asset.IsDeleted() {
			return nil, toolError("object_outside_scope", "Storyboard Asset is not active in this Session's Episode")
		}
		assetsByID[assetID] = value
	}
	assets := make([]project.Asset, 0, len(assetsByID))
	for _, asset := range assetsByID {
		assets = append(assets, asset.Asset)
	}
	if err := state.readSet.storyboardCreationReady(episode, scriptSnapshot(episode.Script), storyboardState, storyboard.Storyboard, assets); err != nil {
		return nil, mapReadSetError(err)
	}
	if err := validateAgentStoryboardDurationPlan(storyboard.Storyboard.PlannedDurationSeconds, episode.TargetDuration.Milliseconds(), input.Operations); err != nil {
		return nil, err
	}
	sceneOrderKey := project.StoryboardOrderStep
	if existingScene == nil && len(storyboard.Storyboard.Scenes) > 0 {
		last := storyboard.Storyboard.Scenes[len(storyboard.Storyboard.Scenes)-1].Scene.Position
		if last > math.MaxInt-project.StoryboardOrderStep {
			return nil, toolError("invalid_project_data", "Storyboard Scene order is exhausted")
		}
		sceneOrderKey = last + project.StoryboardOrderStep
	}
	assetNamesByClip := make(map[string][]string)
	assetIDsByClip := make(map[string][]project.AssetID)
	for _, operation := range input.Operations {
		if changeset.OperationType(operation.Type) != changeset.AddClipAssetRef {
			continue
		}
		assetID, _ := project.ParseAssetID(operation.AssetID)
		assetNamesByClip[operation.ClipRef] = append(assetNamesByClip[operation.ClipRef], assetsByID[assetID].Identity.Name)
		assetIDsByClip[operation.ClipRef] = append(assetIDsByClip[operation.ClipRef], assetID)
	}
	drafts := make([]changesetapp.OperationDraft, 0, len(input.Operations))
	clipOrderKeys := make(map[string]int)
	plannedExistingClipPositions := make(map[int]int)
	if existingScene != nil {
		plannedExistingClipPositions, err = planExistingSceneClipPositions(*existingScene, input.Operations)
		if err != nil {
			return nil, err
		}
	}
	assetReferencePositions := make(map[string]int)
	for operationIndex, requested := range input.Operations {
		draft := changesetapp.OperationDraft{Type: changeset.OperationType(requested.Type), ExpectedRevision: storyboard.Storyboard.Revision, ExpectedScriptRevision: episode.Script.Revision}
		switch draft.Type {
		case changeset.CreateScene:
			if requested.Ref == "" || requested.SceneRef != "" || requested.SceneID != "" || requested.BeforeClipID != "" || requested.AfterClipID != "" || requested.ClipRef != "" || requested.AssetID != "" || requested.PlannedDurationSeconds != 0 || requested.GenerationMode != "" || requested.GeneratorID != "" || requested.ProviderModelID != "" {
				return nil, toolError("invalid_arguments", "create_scene fields are invalid")
			}
			var source *project.SceneScriptSource
			if requested.Source != nil {
				if requested.Source.StartByte < 0 || requested.Source.EndByte <= requested.Source.StartByte || requested.Source.EndByte > len(episode.Script.Markdown) {
					return nil, toolError("invalid_arguments", "Scene source range is invalid")
				}
				source = &project.SceneScriptSource{ScriptRevision: episode.Script.Revision, StartByte: requested.Source.StartByte, EndByte: requested.Source.EndByte, Excerpt: episode.Script.Markdown[requested.Source.StartByte:requested.Source.EndByte]}
			}
			draft.SceneCreation = &changesetapp.SceneCreationDraft{Ref: requested.Ref, Position: sceneOrderKey, Title: requested.Title, Description: requested.Description, Source: source}
		case changeset.CreateClip:
			if requested.Ref == "" || requested.ClipRef != "" || requested.AssetID != "" || requested.Source != nil || (requested.SceneRef == "") == (requested.SceneID == "") || (requested.BeforeClipID != "" && requested.AfterClipID != "") {
				return nil, toolError("invalid_arguments", "create_clip fields are invalid")
			}
			var existingSceneID project.SceneID
			orderKey := requested.SceneRef
			if requested.SceneID != "" {
				existingSceneID, _ = project.ParseSceneID(requested.SceneID)
				orderKey = existingSceneID.String()
			} else if _, exists := createdSceneRefs[requested.SceneRef]; !exists {
				return nil, toolError("invalid_arguments", "create_clip scene_ref does not identify the proposed Scene")
			} else if requested.BeforeClipID != "" || requested.AfterClipID != "" {
				return nil, toolError("invalid_arguments", "create_clip placement anchors require an existing scene_id")
			}
			if requested.PlannedDurationSeconds < project.MinPortableClipDurationSeconds || requested.PlannedDurationSeconds > project.MaxPortableClipDurationSeconds {
				return nil, toolError("invalid_arguments", "portable Clip duration must be between 4 and 15 seconds")
			}
			videoPrompt, promptErr := validateStoryboardVideoPrompt(requested.VideoPrompt, assetNamesByClip[requested.Ref], requested.GenerationMode == project.ClipGenerationReference)
			if promptErr != nil {
				return nil, promptErr
			}
			target, targetErr := resolveVideoPromptWriteTarget(state, "", requested.GenerationMode, requested.GeneratorID, requested.ProviderModelID, "")
			if targetErr != nil {
				return nil, targetErr
			}
			videoPrompt, promptErr = storeModelVideoPrompt("", videoPrompt, target, requested.GenerationMode, assetIDsByClip[requested.Ref], nil)
			if promptErr != nil {
				return nil, toolError("invalid_arguments", promptErr.Error())
			}
			position, positioned := plannedExistingClipPositions[operationIndex]
			if !positioned {
				if clipOrderKeys[orderKey] > math.MaxInt-project.StoryboardOrderStep {
					return nil, toolError("invalid_project_data", "Storyboard Clip order is exhausted")
				}
				clipOrderKeys[orderKey] += project.StoryboardOrderStep
				position = clipOrderKeys[orderKey]
			}
			draft.ClipCreation = &changesetapp.ClipCreationDraft{Ref: requested.Ref, SceneRef: requested.SceneRef, ExistingSceneID: existingSceneID, Position: position, Title: requested.Title,
				Description: requested.Description, Dialogue: requested.Dialogue, CreativeIntent: requested.CreativeIntent, PlannedDurationSeconds: requested.PlannedDurationSeconds,
				FirstImagePrompt: requested.FirstImagePrompt, LastImagePrompt: requested.LastImagePrompt, VideoPrompt: videoPrompt, GenerationMode: requested.GenerationMode}
		case changeset.AddClipAssetRef:
			if requested.ClipRef == "" || requested.AssetID == "" || requested.Ref != "" || requested.SceneRef != "" || requested.SceneID != "" || requested.BeforeClipID != "" || requested.AfterClipID != "" || requested.Title != "" || requested.Source != nil || requested.GeneratorID != "" || requested.ProviderModelID != "" {
				return nil, toolError("invalid_arguments", "add_clip_asset_reference fields are invalid")
			}
			assetID, _ := project.ParseAssetID(requested.AssetID)
			assetReferencePositions[requested.ClipRef]++
			draft.ClipAssetRef = &changesetapp.ClipAssetReferenceDraft{ClipRef: requested.ClipRef, AssetID: assetID, Position: assetReferencePositions[requested.ClipRef]}
		default:
			return nil, toolError("invalid_arguments", "Storyboard operation type is not available")
		}
		drafts = append(drafts, draft)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: state.scope.ProjectID, ChangeSetID: id, ExpectedRevision: input.ExpectedChangeSetRevision, Operations: drafts, OperationKey: input.OperationKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) appendClipMergeOperation(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendClipMergeOperationArguments
	if err := decodeStrict(arguments, &input); err != nil || len(input.SourceClipIDs) < 2 || len(input.SourceClipIDs) > 20 {
		return nil, toolError("invalid_arguments", "append_clip_merge_operation arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil || input.ExpectedChangeSetRevision < 1 || input.OperationKey == "" {
		return nil, toolError("invalid_arguments", "ChangeSet identity, revision, or operation key is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	storyboard, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	storyboardState, err := storyboardSnapshot(storyboard.Storyboard)
	if err != nil {
		return nil, toolError("invalid_project_data", "Storyboard could not be validated")
	}
	sourceIDs := make([]project.ClipID, 0, len(input.SourceClipIDs))
	seenSources := make(map[project.ClipID]struct{}, len(input.SourceClipIDs))
	for _, raw := range input.SourceClipIDs {
		clipID, parseErr := project.ParseClipID(raw)
		if parseErr != nil {
			return nil, toolError("invalid_arguments", "source_clip_ids contains an invalid Clip ID")
		}
		if _, duplicate := seenSources[clipID]; duplicate {
			return nil, toolError("invalid_arguments", "source_clip_ids contains a duplicate")
		}
		seenSources[clipID] = struct{}{}
		sourceIDs = append(sourceIDs, clipID)
	}
	var scene *project.StoryboardScene
	start := -1
	for sceneIndex := range storyboard.Storyboard.Scenes {
		candidate := &storyboard.Storyboard.Scenes[sceneIndex]
		for clipIndex := range candidate.Clips {
			if candidate.Clips[clipIndex].Clip.ID == sourceIDs[0] {
				scene, start = candidate, clipIndex
				break
			}
		}
		if scene != nil {
			break
		}
	}
	if scene == nil || start+len(sourceIDs) > len(scene.Clips) {
		return nil, toolError("object_outside_scope", "source Clips are not active in one Scene")
	}
	for index, sourceID := range sourceIDs {
		if scene.Clips[start+index].Clip.ID != sourceID {
			return nil, toolError("invalid_arguments", "source Clips must be adjacent and ordered within one Scene")
		}
	}
	if err := state.readSet.existingSceneClipsReady(scene.Scene.ID, storyboard.Storyboard.Revision, storyboardState+":"+scene.Scene.ID.String()); err != nil {
		return nil, mapReadSetError(err)
	}
	sources := make([]project.Clip, 0, len(sourceIDs))
	var sourceAssetIDs []project.AssetID
	seenSourceAssets := make(map[project.AssetID]struct{})
	var duration int64
	for _, sourceID := range sourceIDs {
		details, readErr := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: sourceID})
		if readErr != nil {
			return nil, mapQueryError(readErr)
		}
		if details.Clip.EpisodeID != state.scope.EpisodeID || details.Clip.SceneID != scene.Scene.ID || details.Clip.IsDeleted() {
			return nil, toolError("object_outside_scope", "a source Clip is not active in the target Scene")
		}
		sources = append(sources, details.Clip)
		duration += details.Clip.PlannedDurationSeconds
		for _, reference := range details.AssetReferences {
			if reference.AssetDeleted {
				continue
			}
			if _, duplicate := seenSourceAssets[reference.AssetID]; duplicate {
				continue
			}
			seenSourceAssets[reference.AssetID] = struct{}{}
			sourceAssetIDs = append(sourceAssetIDs, reference.AssetID)
		}
	}
	if duration < project.MinPortableClipDurationSeconds || duration > project.MaxModelClipDurationSeconds {
		return nil, toolError("invalid_arguments", "merged Clip duration must remain within the supported 4-30 second range")
	}
	assetIDs := make([]project.AssetID, 0, len(sourceAssetIDs))
	assets := make([]project.Asset, 0, len(sourceAssetIDs))
	assetNames := make([]string, 0, len(sourceAssetIDs))
	for _, assetID := range sourceAssetIDs {
		value, readErr := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: assetID})
		if readErr != nil {
			return nil, mapQueryError(readErr)
		}
		if value.Asset.EpisodeID != state.scope.EpisodeID || value.Asset.IsDeleted() {
			return nil, toolError("object_outside_scope", "a merged Clip Asset is not active in this Episode")
		}
		assetIDs = append(assetIDs, assetID)
		assets = append(assets, value.Asset)
		assetNames = append(assetNames, value.Identity.Name)
	}
	if err := state.readSet.clipMergeReady(sources, assets); err != nil {
		return nil, mapReadSetError(err)
	}
	videoPrompt, err := validateStoryboardVideoPrompt(input.VideoPrompt, assetNames, input.GenerationMode == project.ClipGenerationReference)
	if err != nil {
		return nil, err
	}
	targetSource := sources[0]
	for _, source := range sources {
		if source.ID == state.turn.Focus.ClipID {
			targetSource = source
			break
		}
	}
	target, err := resolveVideoPromptWriteTarget(state, targetSource.ID, input.GenerationMode, input.GeneratorID, input.ProviderModelID, targetSource.VideoPrompts.ForMode(input.GenerationMode))
	if err != nil {
		return nil, err
	}
	videoPrompt, err = storeModelVideoPrompt("", videoPrompt, target, input.GenerationMode, assetIDs, nil)
	if err != nil {
		return nil, err
	}
	firstSlotID, err := project.FixedMediaSlotID(sources[0].ID.String(), project.MediaSlotClipFirstImage)
	if err != nil {
		return nil, toolError("invalid_project_data", "source first-frame slot is invalid")
	}
	firstSlot, err := s.queries.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: state.scope.ProjectID, MediaSlotID: firstSlotID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	lastSource := sources[len(sources)-1]
	lastSlotID, err := project.FixedMediaSlotID(lastSource.ID.String(), project.MediaSlotClipLastImage)
	if err != nil {
		return nil, toolError("invalid_project_data", "source last-frame slot is invalid")
	}
	lastSlot, err := s.queries.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: state.scope.ProjectID, MediaSlotID: lastSlotID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	draft := changesetapp.OperationDraft{Type: changeset.MergeClips, ExpectedRevision: storyboard.Storyboard.Revision,
		ClipMerge: &changesetapp.ClipMergeDraft{Sources: sources, Title: strings.TrimSpace(input.Title),
			Description: project.NormalizeStoryboardText(input.Description), Dialogue: project.NormalizeStoryboardText(input.Dialogue), CreativeIntent: project.NormalizeStoryboardText(input.CreativeIntent),
			FirstImagePrompt: project.NormalizeStoryboardText(input.FirstImagePrompt), LastImagePrompt: project.NormalizeStoryboardText(input.LastImagePrompt), VideoPrompt: videoPrompt,
			GenerationMode: input.GenerationMode, AssetIDs: assetIDs,
			FirstFrame: changesetapp.ClipMergeFrameDraft{Slot: firstSlot.Slot, Current: firstSlot.Current},
			LastFrame:  changesetapp.ClipMergeFrameDraft{Slot: lastSlot.Slot, Current: lastSlot.Current}}}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: state.scope.ProjectID, ChangeSetID: id,
		ExpectedRevision: input.ExpectedChangeSetRevision, Operations: []changesetapp.OperationDraft{draft}, OperationKey: input.OperationKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) appendClipSplitOperation(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendClipSplitOperationArguments
	if err := decodeStrict(arguments, &input); err != nil || len(input.Replacements) < 2 || len(input.Replacements) > 10 {
		return nil, toolError("invalid_arguments", "append_clip_split_operation arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	sourceID, sourceErr := project.ParseClipID(input.SourceClipID)
	if err != nil || sourceErr != nil || input.ExpectedChangeSetRevision < 1 || input.OperationKey == "" {
		return nil, toolError("invalid_arguments", "ChangeSet identity, source Clip, revision, or operation key is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	storyboard, err := s.queries.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	storyboardState, err := storyboardSnapshot(storyboard.Storyboard)
	if err != nil {
		return nil, toolError("invalid_project_data", "Storyboard could not be validated")
	}
	var scene *project.StoryboardScene
	for sceneIndex := range storyboard.Storyboard.Scenes {
		candidate := &storyboard.Storyboard.Scenes[sceneIndex]
		for _, details := range candidate.Clips {
			if details.Clip.ID == sourceID {
				scene = candidate
				break
			}
		}
		if scene != nil {
			break
		}
	}
	if scene == nil {
		return nil, toolError("object_outside_scope", "source Clip is not active in this Episode")
	}
	if err := state.readSet.existingSceneClipsReady(scene.Scene.ID, storyboard.Storyboard.Revision, storyboardState+":"+scene.Scene.ID.String()); err != nil {
		return nil, mapReadSetError(err)
	}
	details, err := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: sourceID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	if details.Clip.EpisodeID != state.scope.EpisodeID || details.Clip.SceneID != scene.Scene.ID || details.Clip.IsDeleted() {
		return nil, toolError("object_outside_scope", "source Clip is not active in the target Scene")
	}
	assetValues := make(map[project.AssetID]project.Asset)
	assetNames := make(map[project.AssetID]string)
	assets := make([]project.Asset, 0, len(details.AssetReferences))
	for _, reference := range details.AssetReferences {
		if reference.AssetDeleted {
			continue
		}
		value, readErr := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: reference.AssetID})
		if readErr != nil {
			return nil, mapQueryError(readErr)
		}
		if value.Asset.EpisodeID != state.scope.EpisodeID || value.Asset.IsDeleted() {
			return nil, toolError("object_outside_scope", "a source Clip Asset is not active in this Episode")
		}
		assetValues[reference.AssetID] = value.Asset
		assetNames[reference.AssetID] = value.Identity.Name
		assets = append(assets, value.Asset)
	}
	if err := state.readSet.clipMergeReady([]project.Clip{details.Clip}, assets); err != nil {
		return nil, mapReadSetError(err)
	}
	replacements := make([]changesetapp.ClipSplitReplacementDraft, 0, len(input.Replacements))
	for _, candidate := range input.Replacements {
		if candidate.PlannedDurationSeconds < project.MinPortableClipDurationSeconds || candidate.PlannedDurationSeconds > project.MaxPortableClipDurationSeconds {
			return nil, toolError("invalid_arguments", "each split replacement duration must be between 4 and 15 seconds")
		}
		ids := make([]project.AssetID, 0, len(candidate.AssetIDs))
		names := make([]string, 0, len(candidate.AssetIDs))
		seen := make(map[project.AssetID]struct{}, len(candidate.AssetIDs))
		for _, raw := range candidate.AssetIDs {
			assetID, parseErr := project.ParseAssetID(raw)
			if parseErr != nil || assetValues[assetID].ID == "" {
				return nil, toolError("invalid_arguments", "split replacements may use only active Assets already bound to the source Clip")
			}
			if _, duplicate := seen[assetID]; duplicate {
				return nil, toolError("invalid_arguments", "a split replacement contains a duplicate Asset")
			}
			seen[assetID] = struct{}{}
			ids = append(ids, assetID)
			names = append(names, assetNames[assetID])
		}
		videoPrompt, promptErr := validateStoryboardVideoPrompt(candidate.VideoPrompt, names, candidate.GenerationMode == project.ClipGenerationReference)
		if promptErr != nil {
			return nil, promptErr
		}
		target, targetErr := resolveVideoPromptWriteTarget(state, sourceID, candidate.GenerationMode, candidate.GeneratorID, candidate.ProviderModelID, details.Clip.VideoPrompts.ForMode(candidate.GenerationMode))
		if targetErr != nil {
			return nil, targetErr
		}
		videoPrompt, promptErr = storeModelVideoPrompt("", videoPrompt, target, candidate.GenerationMode, ids, nil)
		if promptErr != nil {
			return nil, promptErr
		}
		replacements = append(replacements, changesetapp.ClipSplitReplacementDraft{
			Title: strings.TrimSpace(candidate.Title), Description: project.NormalizeStoryboardText(candidate.Description),
			Dialogue: project.NormalizeStoryboardText(candidate.Dialogue), CreativeIntent: project.NormalizeStoryboardText(candidate.CreativeIntent),
			PlannedDurationSeconds: candidate.PlannedDurationSeconds, FirstImagePrompt: project.NormalizeStoryboardText(candidate.FirstImagePrompt),
			LastImagePrompt: project.NormalizeStoryboardText(candidate.LastImagePrompt), VideoPrompt: videoPrompt,
			GenerationMode: candidate.GenerationMode, AssetIDs: ids,
		})
	}
	firstSlotID, _ := project.FixedMediaSlotID(sourceID.String(), project.MediaSlotClipFirstImage)
	lastSlotID, _ := project.FixedMediaSlotID(sourceID.String(), project.MediaSlotClipLastImage)
	firstSlot, err := s.queries.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: state.scope.ProjectID, MediaSlotID: firstSlotID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	lastSlot, err := s.queries.GetMediaSlot(ctx, projectapp.GetMediaSlotInput{ProjectID: state.scope.ProjectID, MediaSlotID: lastSlotID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	draft := changesetapp.OperationDraft{Type: changeset.SplitClip, ExpectedRevision: storyboard.Storyboard.Revision,
		ClipSplit: &changesetapp.ClipSplitDraft{Source: details.Clip, Replacements: replacements,
			FirstFrame: changesetapp.ClipMergeFrameDraft{Slot: firstSlot.Slot, Current: firstSlot.Current},
			LastFrame:  changesetapp.ClipMergeFrameDraft{Slot: lastSlot.Slot, Current: lastSlot.Current}}}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{ProjectID: state.scope.ProjectID, ChangeSetID: id,
		ExpectedRevision: input.ExpectedChangeSetRevision, Operations: []changesetapp.OperationDraft{draft}, OperationKey: input.OperationKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

type clipInsertionInterval struct {
	lower int
	upper int
}

func planExistingSceneClipPositions(scene project.StoryboardScene, operations []storyboardOperationArgument) (map[int]int, error) {
	clipIndexes := make(map[project.ClipID]int, len(scene.Clips))
	for index, details := range scene.Clips {
		clipIndexes[details.Clip.ID] = index
	}
	intervals := make(map[clipInsertionInterval][]int)
	lastPosition := 0
	if len(scene.Clips) > 0 {
		lastPosition = scene.Clips[len(scene.Clips)-1].Clip.Position
	}
	for operationIndex, requested := range operations {
		if changeset.OperationType(requested.Type) != changeset.CreateClip || requested.SceneID == "" {
			continue
		}
		if requested.BeforeClipID != "" && requested.AfterClipID != "" {
			return nil, toolError("invalid_arguments", "create_clip accepts only one placement anchor")
		}
		interval := clipInsertionInterval{lower: lastPosition}
		anchorValue := requested.BeforeClipID
		insertAfter := false
		if anchorValue == "" {
			anchorValue = requested.AfterClipID
			insertAfter = anchorValue != ""
		}
		if anchorValue != "" {
			anchorID, err := project.ParseClipID(anchorValue)
			if err != nil {
				return nil, toolError("invalid_arguments", "create_clip placement anchor is invalid")
			}
			anchorIndex, exists := clipIndexes[anchorID]
			if !exists {
				return nil, toolError("object_outside_scope", "create_clip placement anchor is not active in the target Scene")
			}
			if insertAfter {
				interval.lower = scene.Clips[anchorIndex].Clip.Position
				if anchorIndex+1 < len(scene.Clips) {
					interval.upper = scene.Clips[anchorIndex+1].Clip.Position
				}
			} else {
				interval.upper = scene.Clips[anchorIndex].Clip.Position
				interval.lower = 0
				if anchorIndex > 0 {
					interval.lower = scene.Clips[anchorIndex-1].Clip.Position
				}
			}
		}
		intervals[interval] = append(intervals[interval], operationIndex)
	}

	positions := make(map[int]int)
	for interval, operationIndexes := range intervals {
		if interval.upper == 0 {
			if interval.lower > math.MaxInt-len(operationIndexes)*project.StoryboardOrderStep {
				return nil, toolError("invalid_project_data", "Storyboard Clip order is exhausted")
			}
			for offset, operationIndex := range operationIndexes {
				positions[operationIndex] = interval.lower + (offset+1)*project.StoryboardOrderStep
			}
			continue
		}
		stride := (interval.upper - interval.lower) / (len(operationIndexes) + 1)
		if stride < 1 {
			return nil, toolError("invalid_project_data", "Storyboard Clip insertion space is exhausted")
		}
		for offset, operationIndex := range operationIndexes {
			positions[operationIndex] = interval.lower + (offset+1)*stride
		}
	}
	return positions, nil
}

func validateAgentStoryboardDurationPlan(currentSeconds, targetMilliseconds int64, operations []storyboardOperationArgument) error {
	var proposedSeconds int64
	clipCount := 0
	for _, operation := range operations {
		if changeset.OperationType(operation.Type) != changeset.CreateClip {
			continue
		}
		if operation.PlannedDurationSeconds > math.MaxInt64-proposedSeconds {
			return toolError("invalid_arguments", "Storyboard duration overflows")
		}
		proposedSeconds += operation.PlannedDurationSeconds
		clipCount++
	}
	if clipCount == 0 {
		return nil
	}
	if proposedSeconds < int64(clipCount)*minimumGeneratedSceneAverageClipDurationSeconds {
		return toolError("clip_plan_too_fragmented", "the Scene must average at least 6 seconds per Clip; merge adjacent micro-beats instead of padding them")
	}
	if currentSeconds > math.MaxInt64-proposedSeconds {
		return toolError("invalid_arguments", "Storyboard duration overflows")
	}
	totalSeconds := currentSeconds + proposedSeconds
	if totalSeconds > math.MaxInt64/1000 || totalSeconds*1000 > targetMilliseconds {
		return toolError("storyboard_duration_exceeded", "the Scene would exceed the Episode target duration; merge adjacent micro-beats")
	}
	return nil
}

func validateStoryboardVideoPrompt(value string, _ []string, _ bool) (string, error) {
	prompt := strings.TrimSpace(project.NormalizeStoryboardText(value))
	if prompt == "" {
		return "", toolError("invalid_arguments", "every Clip requires a self-contained video Prompt")
	}
	return prompt, nil
}

func hasUnboundStoryboardAssetMention(prompt string, boundNames []string) bool {
	for offset := 0; offset < len(prompt); {
		index := strings.IndexByte(prompt[offset:], '@')
		if index < 0 {
			return false
		}
		rest := prompt[offset+index+1:]
		matched := false
		for _, name := range boundNames {
			if strings.HasPrefix(rest, name) {
				matched = true
				break
			}
		}
		if !matched {
			return true
		}
		offset += index + 1
	}
	return false
}

func (s *Server) buildPromptDraft(ctx context.Context, state bindingSnapshot, input promptOperationArgument) (changesetapp.OperationDraft, error) {
	switch changeset.OperationType(input.Type) {
	case changeset.UpdateAssetPrompt:
		if input.NewPrompt == nil || input.ClipID != "" || input.FirstImagePrompt != nil || input.LastImagePrompt != nil || input.VideoPrompt != nil || input.VideoPromptMode != "" || input.ReferenceArtifactIDs != nil || input.GeneratorID != "" || input.ProviderModelID != "" {
			return changesetapp.OperationDraft{}, toolError("invalid_arguments", "Asset Prompt operation fields are invalid")
		}
		id, err := project.ParseAssetID(input.AssetID)
		if err != nil {
			return changesetapp.OperationDraft{}, toolError("invalid_arguments", "asset_id is invalid")
		}
		value, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: id})
		if err != nil {
			return changesetapp.OperationDraft{}, mapQueryError(err)
		}
		if value.Asset.EpisodeID != state.scope.EpisodeID || value.Asset.IsDeleted() {
			return changesetapp.OperationDraft{}, toolError("object_outside_scope", "Asset is not active in this Session's Episode")
		}
		draft, err := state.readSet.assetDraft(value.Asset, *input.NewPrompt)
		return draft, mapReadSetError(err)
	case changeset.UpdateClipPrompts:
		if input.AssetID != "" || input.NewPrompt != nil || (input.FirstImagePrompt == nil && input.LastImagePrompt == nil && input.VideoPrompt == nil) || (input.VideoPrompt == nil) != (input.VideoPromptMode == "") || !validateVideoPromptModelFields(input.GeneratorID, input.ProviderModelID, input.VideoPrompt != nil) {
			return changesetapp.OperationDraft{}, toolError("invalid_arguments", "Clip Prompt operation fields are invalid")
		}
		id, err := project.ParseClipID(input.ClipID)
		if err != nil {
			return changesetapp.OperationDraft{}, toolError("invalid_arguments", "clip_id is invalid")
		}
		value, err := s.queries.GetClip(ctx, projectapp.GetClipInput{ProjectID: state.scope.ProjectID, ClipID: id})
		if err != nil {
			return changesetapp.OperationDraft{}, mapQueryError(err)
		}
		if value.Clip.EpisodeID != state.scope.EpisodeID || value.Clip.IsDeleted() {
			return changesetapp.OperationDraft{}, toolError("object_outside_scope", "Clip is not active in this Session's Episode")
		}
		beforeAssetIDs := make([]project.AssetID, 0, len(value.AssetReferences))
		assetsByID := make(map[project.AssetID]project.EpisodeAsset, len(value.AssetReferences))
		for _, reference := range value.AssetReferences {
			beforeAssetIDs = append(beforeAssetIDs, reference.AssetID)
		}
		currentReferenceMedia, queryErr := s.queries.ListClipReferenceMedia(ctx, projectapp.ListClipReferenceMediaInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID, ClipID: id})
		if queryErr != nil {
			return changesetapp.OperationDraft{}, mapQueryError(queryErr)
		}
		beforeReferenceIDs := make([]project.ArtifactID, 0, len(currentReferenceMedia))
		for _, artifact := range currentReferenceMedia {
			beforeReferenceIDs = append(beforeReferenceIDs, artifact.ID)
		}
		afterReferenceIDs := append([]project.ArtifactID(nil), beforeReferenceIDs...)
		library, queryErr := s.queries.ListEpisodeReferenceMedia(ctx, projectapp.ListEpisodeReferenceMediaInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
		if queryErr != nil {
			return changesetapp.OperationDraft{}, mapQueryError(queryErr)
		}
		libraryByID := make(map[project.ArtifactID]project.EpisodeReferenceMedia, len(library))
		for _, item := range library {
			if item.DeletedAt == nil {
				libraryByID[item.Artifact.ID] = item
			}
		}
		if input.ReferenceArtifactIDs != nil {
			afterReferenceIDs = make([]project.ArtifactID, 0, len(*input.ReferenceArtifactIDs))
			seen := make(map[project.ArtifactID]struct{}, len(*input.ReferenceArtifactIDs))
			for _, raw := range *input.ReferenceArtifactIDs {
				artifactID, parseErr := project.ParseArtifactID(raw)
				if parseErr != nil {
					return changesetapp.OperationDraft{}, toolError("invalid_arguments", "reference_artifact_ids contains an invalid Artifact ID")
				}
				if _, duplicate := seen[artifactID]; duplicate {
					return changesetapp.OperationDraft{}, toolError("invalid_arguments", "reference_artifact_ids contains a duplicate Artifact")
				}
				if _, exists := libraryByID[artifactID]; !exists {
					return changesetapp.OperationDraft{}, toolError("object_outside_scope", "uploaded reference media is not active in this Session's Episode")
				}
				seen[artifactID] = struct{}{}
				afterReferenceIDs = append(afterReferenceIDs, artifactID)
			}
		}
		afterAssetIDs := append([]project.AssetID(nil), beforeAssetIDs...)
		if input.AssetIDs != nil {
			afterAssetIDs = make([]project.AssetID, 0, len(*input.AssetIDs))
			seen := make(map[project.AssetID]struct{}, len(*input.AssetIDs))
			for _, raw := range *input.AssetIDs {
				assetID, parseErr := project.ParseAssetID(raw)
				if parseErr != nil {
					return changesetapp.OperationDraft{}, toolError("invalid_arguments", "asset_ids contains an invalid Asset ID")
				}
				if _, duplicate := seen[assetID]; duplicate {
					return changesetapp.OperationDraft{}, toolError("invalid_arguments", "asset_ids contains a duplicate Asset")
				}
				asset, queryErr := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: assetID})
				if queryErr != nil {
					return changesetapp.OperationDraft{}, mapQueryError(queryErr)
				}
				if asset.Asset.EpisodeID != state.scope.EpisodeID || asset.Asset.IsDeleted() {
					return changesetapp.OperationDraft{}, toolError("object_outside_scope", "Clip Asset is not active in this Session's Episode")
				}
				if !state.readSet.assetIsCurrent(asset.Asset) {
					return changesetapp.OperationDraft{}, toolError("object_not_observed", "read every proposed Clip Asset before changing its binding")
				}
				seen[assetID] = struct{}{}
				afterAssetIDs = append(afterAssetIDs, assetID)
				assetsByID[assetID] = asset
			}
		}
		for _, assetID := range afterAssetIDs {
			if _, exists := assetsByID[assetID]; exists {
				continue
			}
			asset, queryErr := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: assetID})
			if queryErr != nil {
				return changesetapp.OperationDraft{}, mapQueryError(queryErr)
			}
			assetsByID[assetID] = asset
		}
		names := make([]string, 0, len(afterAssetIDs))
		for _, assetID := range afterAssetIDs {
			names = append(names, assetsByID[assetID].Identity.Name)
		}
		for _, artifactID := range afterReferenceIDs {
			if item, exists := libraryByID[artifactID]; exists {
				names = append(names, item.Name)
			}
		}
		videoMode := value.Clip.GenerationMode
		if input.VideoPrompt != nil {
			videoMode = project.ClipGenerationMode(input.VideoPromptMode)
			if !videoMode.IsValid() {
				return changesetapp.OperationDraft{}, toolError("invalid_arguments", "video_prompt_mode is invalid")
			}
			normalized, promptErr := validateStoryboardVideoPrompt(*input.VideoPrompt, names, videoMode == project.ClipGenerationReference)
			if promptErr != nil {
				return changesetapp.OperationDraft{}, promptErr
			}
			target, targetErr := resolveVideoPromptWriteTarget(state, id, videoMode, input.GeneratorID, input.ProviderModelID, value.Clip.VideoPrompts.ForMode(videoMode))
			if targetErr != nil {
				return changesetapp.OperationDraft{}, targetErr
			}
			stored, storeErr := storeModelVideoPrompt(value.Clip.VideoPrompts.ForMode(videoMode), normalized, target, videoMode, afterAssetIDs, afterReferenceIDs)
			if storeErr != nil {
				return changesetapp.OperationDraft{}, storeErr
			}
			input.VideoPrompt = &stored
		}
		draft, err := state.readSet.clipDraft(value.Clip, beforeAssetIDs, afterAssetIDs, beforeReferenceIDs, afterReferenceIDs, input.FirstImagePrompt, input.LastImagePrompt, videoMode, input.VideoPrompt)
		return draft, mapReadSetError(err)
	default:
		return changesetapp.OperationDraft{}, toolError("invalid_arguments", "operation type is not available")
	}
}

func (s *Server) appendScriptOperation(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input appendScriptOperationArguments
	if err := decodeStrict(arguments, &input); err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "append_script_operation arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil {
		return nil, toolError("invalid_arguments", "ChangeSet identity is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	episode, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return nil, mapQueryError(err)
	}
	draft, err := state.readSet.scriptDraft(episode, input.Markdown)
	if err != nil {
		if errors.Is(err, ErrInvalidArguments) {
			return nil, toolError("invalid_arguments", "replacement script is invalid or unchanged")
		}
		return nil, mapReadSetError(err)
	}
	result, err := s.drafts.AppendOperations(ctx, changesetapp.AppendOperationsInput{
		ProjectID: state.scope.ProjectID, ChangeSetID: id, ExpectedRevision: input.ExpectedChangeSetRevision,
		Operations: []changesetapp.OperationDraft{draft}, OperationKey: input.OperationKey,
	})
	if err != nil {
		return nil, mapDraftError(err)
	}
	return marshalChangeSetResult(result)
}

func (s *Server) finalizeChangeSet(ctx context.Context, route string, arguments json.RawMessage) ([]byte, error) {
	var input finalizeChangeSetArguments
	if err := decodeStrict(arguments, &input); err != nil {
		return nil, toolError("invalid_arguments", "finalize_change_set arguments are invalid")
	}
	id, err := changeset.ParseChangeSetID(input.ChangeSetID)
	if err != nil || input.ExpectedChangeSetRevision < 1 {
		return nil, toolError("invalid_arguments", "ChangeSet identity or revision is invalid")
	}
	state, err := s.snapshotBinding(route, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDraft(route, state.turn.AgentTurnID, id); err != nil {
		return nil, err
	}
	result, err := s.drafts.Finalize(ctx, changesetapp.FinalizeInput{ProjectID: state.scope.ProjectID, ChangeSetID: id, ExpectedRevision: input.ExpectedChangeSetRevision, OperationKey: input.OperationKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	s.releaseDraft(route, state.turn.AgentTurnID, id)
	if result.ChangeSet.Status == changeset.StatusApplied {
		return marshalChangeSetResult(result)
	}
	applyKeyHash := sha256.Sum256([]byte(input.OperationKey))
	applyKey := "auto-apply:" + hex.EncodeToString(applyKeyHash[:])
	applied, err := s.drafts.Apply(ctx, changesetapp.ApplyInput{ProjectID: state.scope.ProjectID, ChangeSetID: id, ExpectedRevision: result.ChangeSet.Revision, OperationKey: applyKey})
	if err != nil {
		return nil, mapDraftError(err)
	}
	if !applied.Replayed {
		s.events.Publish(agentapp.StudioEvent{
			Type: agentapp.EventChangeSetApplied, SessionID: state.scope.SessionID,
			TurnID: state.turn.AgentTurnID, ChangeSetID: applied.ChangeSet.ID,
			Summary: applied.ChangeSet.Title, OccurredAt: applied.ChangeSet.UpdatedAt,
		})
	}
	return marshalChangeSetResult(applied)
}

func (s *Server) recordDraft(route string, turnID agent.TurnID, id changeset.ChangeSetID, finalized bool) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	state := s.bindings[route]
	if state == nil || state.turn == nil || state.turn.AgentTurnID != turnID || state.drafts == nil {
		return toolError("turn_not_active", "the Agent Turn ended before the draft was registered")
	}
	state.drafts[id] = state.drafts[id] || finalized
	return nil
}

func (s *Server) requireOwnedDraft(route string, turnID agent.TurnID, id changeset.ChangeSetID) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	state := s.bindings[route]
	if state == nil || state.turn == nil || state.turn.AgentTurnID != turnID {
		return toolError("turn_not_active", "no active Agent Turn is bound to this Session")
	}
	_, ok := state.drafts[id]
	if !ok {
		return toolError("change_set_outside_turn", "ChangeSet was not created by this active Turn")
	}
	return nil
}

func (s *Server) releaseDraft(route string, turnID agent.TurnID, id changeset.ChangeSetID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	state := s.bindings[route]
	if state != nil && state.turn != nil && state.turn.AgentTurnID == turnID {
		state.drafts[id] = true
	}
}

func (s *Server) consumeCursor(route, token, snapshot string) (int, error) {
	if token == "" {
		return 0, nil
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	state := s.bindings[route]
	if state == nil {
		return 0, toolError("binding_unavailable", "Session binding is unavailable")
	}
	cursor, ok := state.cursors[token]
	delete(state.cursors, token)
	if !ok {
		return 0, toolError("cursor_invalid", "Asset list cursor is invalid or already consumed")
	}
	if cursor.snapshot != snapshot {
		return 0, toolError("cursor_stale", "Asset list changed; restart pagination")
	}
	return cursor.offset, nil
}

func (s *Server) createCursor(route, snapshot string, offset int) (string, error) {
	token, err := s.randomSecret(24)
	if err != nil {
		return "", toolError("cursor_unavailable", "could not create pagination cursor")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	state := s.bindings[route]
	if state == nil {
		return "", toolError("binding_unavailable", "Session binding is unavailable")
	}
	if len(state.cursors) >= MaxCursorsPerBinding {
		for existing := range state.cursors {
			delete(state.cursors, existing)
			break
		}
	}
	state.cursors[token] = cursorState{snapshot: snapshot, offset: offset}
	return token, nil
}

func (s *Server) priorAssetCandidate(ctx context.Context, state bindingSnapshot, id project.AssetID) (project.EpisodeAsset, int, error) {
	value, err := s.queries.GetAsset(ctx, projectapp.GetAssetInput{ProjectID: state.scope.ProjectID, AssetID: id})
	if err != nil {
		return project.EpisodeAsset{}, 0, mapQueryError(err)
	}
	if value.Asset.IsDeleted() || value.Asset.EpisodeID == state.scope.EpisodeID {
		return project.EpisodeAsset{}, 0, toolError("object_outside_scope", "Asset is not an active earlier-Episode candidate")
	}
	target, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: state.scope.EpisodeID})
	if err != nil {
		return project.EpisodeAsset{}, 0, mapQueryError(err)
	}
	source, err := s.queries.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: state.scope.ProjectID, EpisodeID: value.Asset.EpisodeID})
	if err != nil {
		return project.EpisodeAsset{}, 0, mapQueryError(err)
	}
	if source.SequenceNumber >= target.SequenceNumber {
		return project.EpisodeAsset{}, 0, toolError("object_outside_scope", "Asset is not from an earlier Episode")
	}
	return value, source.SequenceNumber, nil
}

func scriptSnapshot(value project.Script) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(fmt.Sprintf("%d\x00", value.Revision)))
	_, _ = hash.Write([]byte(value.Markdown))
	return hex.EncodeToString(hash.Sum(nil))
}

func boundedScriptPage(markdown string, offset int) (string, int, error) {
	if offset < 0 || offset > len(markdown) || !utf8.ValidString(markdown[offset:]) {
		return "", 0, toolError("cursor_stale", "script cursor is stale")
	}
	end, count := offset, 0
	for end < len(markdown) {
		relative := strings.IndexByte(markdown[end:], '\n')
		lineEnd := len(markdown)
		if relative >= 0 {
			lineEnd = end + relative + 1
		}
		lineCharacters := utf8.RuneCountInString(markdown[end:lineEnd])
		if lineCharacters > MaxScriptPageCharacters {
			return "", 0, toolError("script_line_too_large", "a script line exceeds the page limit")
		}
		if count > 0 && count+lineCharacters > MaxScriptPageCharacters {
			break
		}
		count += lineCharacters
		end = lineEnd
	}
	return markdown[offset:end], end, nil
}

func priorAssetListSnapshot(values []projectapp.PriorAssetCandidate) (string, error) {
	type item struct {
		AssetID, IdentityID, EpisodeID, Kind, Name string
		AssetRevision, IdentityRevision            int64
		EpisodeSequence                            int
	}
	items := make([]item, 0, len(values))
	for _, value := range values {
		if value.Asset.Validate() != nil || value.Asset.Asset.IsDeleted() || value.EpisodeSequence < 1 {
			return "", errors.New("invalid prior Asset candidate")
		}
		items = append(items, item{value.Asset.Asset.ID.String(), value.Asset.Identity.ID.String(), value.Asset.Asset.EpisodeID.String(), string(value.Asset.Identity.Kind), value.Asset.Identity.Name, value.Asset.Asset.Revision, value.Asset.Identity.Revision, value.EpisodeSequence})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func assetListSnapshot(assets []project.EpisodeAsset) (string, error) {
	type item struct {
		AssetID, IdentityID, Kind, Name string
		AssetRevision, IdentityRevision int64
	}
	values := make([]item, 0, len(assets))
	for _, value := range assets {
		if value.Validate() != nil || value.Asset.IsDeleted() {
			return "", errors.New("invalid Asset list")
		}
		values = append(values, item{value.Asset.ID.String(), value.Identity.ID.String(), string(value.Identity.Kind), value.Identity.Name, value.Asset.Revision, value.Identity.Revision})
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func referenceMediaListSnapshot(values []project.EpisodeReferenceMedia) (string, error) {
	type item struct {
		ArtifactID, Kind, Name, Description        string
		Revision                                   int64
		TrimStartMilliseconds, TrimEndMilliseconds int64
	}
	items := make([]item, 0, len(values))
	for _, value := range values {
		if value.Validate() != nil || value.DeletedAt != nil {
			return "", errors.New("invalid uploaded reference media list")
		}
		items = append(items, item{value.Artifact.ID.String(), string(value.Artifact.Kind), value.Name, value.Description, value.Revision, value.TrimStartMilliseconds, value.TrimEndMilliseconds})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func deletedAssetListSnapshot(assets []project.EpisodeAsset) (string, error) {
	type item struct {
		AssetID, IdentityID, Kind, Name, Prompt string
		AssetRevision, IdentityRevision         int64
		DeletedAt                               int64
	}
	values := make([]item, 0, len(assets))
	for _, value := range assets {
		if value.Validate() != nil || !value.Asset.IsDeleted() {
			return "", errors.New("invalid deleted Asset list")
		}
		values = append(values, item{value.Asset.ID.String(), value.Identity.ID.String(), string(value.Identity.Kind), value.Identity.Name, value.Asset.Prompt, value.Asset.Revision, value.Identity.Revision, value.Asset.DeletedAt.UnixMilli()})
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func storyboardSnapshot(value project.Storyboard) (string, error) {
	if value.Validate() != nil {
		return "", errors.New("invalid Storyboard")
	}
	type clipItem struct {
		ID       string
		Revision int64
	}
	type sceneItem struct {
		ID       string
		Revision int64
		Clips    []clipItem
	}
	payload := struct {
		Revision int64
		Scenes   []sceneItem
	}{Revision: value.Revision, Scenes: make([]sceneItem, 0, len(value.Scenes))}
	for _, scene := range value.Scenes {
		item := sceneItem{ID: scene.Scene.ID.String(), Revision: scene.Scene.Revision, Clips: make([]clipItem, 0, len(scene.Clips))}
		for _, clip := range scene.Clips {
			item.Clips = append(item.Clips, clipItem{ID: clip.Clip.ID.String(), Revision: clip.Clip.Revision})
		}
		payload.Scenes = append(payload.Scenes, item)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func parseClipPromptFields(values []string) (map[clipPromptField]bool, error) {
	if len(values) == 0 {
		return map[clipPromptField]bool{clipFirstPrompt: true, clipLastPrompt: true, clipFirstVideoPrompt: true, clipFirstLastVideoPrompt: true, clipReferenceVideoPrompt: true}, nil
	}
	if len(values) > 5 {
		return nil, toolError("invalid_arguments", "prompt_fields has too many values")
	}
	result := make(map[clipPromptField]bool, len(values))
	for _, value := range values {
		field := clipPromptField(value)
		if field != clipFirstPrompt && field != clipLastPrompt && field != clipFirstVideoPrompt && field != clipFirstLastVideoPrompt && field != clipReferenceVideoPrompt || result[field] {
			return nil, toolError("invalid_arguments", "prompt_fields contains an invalid or duplicate value")
		}
		result[field] = true
	}
	return result, nil
}

func marshalToolPayload(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, toolError("serialization_failed", "tool result could not be serialized")
	}
	if len(encoded) > MaxToolResultBytes {
		return nil, toolError("result_too_large", "tool result exceeds 64 KiB; request less data")
	}
	return encoded, nil
}

func marshalChangeSetResult(result changesetapp.CommandResult) ([]byte, error) {
	return marshalToolPayload(map[string]any{
		"change_set_id": result.ChangeSet.ID.String(), "status": string(result.ChangeSet.Status),
		"operation_count": result.ChangeSet.OperationCount, "revision": result.ChangeSet.Revision,
		"replayed": result.Replayed,
	})
}

func mapReadSetError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrObjectNotObserved):
		return toolError("object_not_observed", "read the target object in this Turn before proposing a change")
	case errors.Is(err, ErrFieldNotObserved):
		return toolError("field_not_observed", "read every Prompt field being changed in this Turn")
	case errors.Is(err, ErrObservationStale):
		return toolError("observation_stale", "the target changed after it was read; read it again")
	case errors.Is(err, ErrScriptNotObserved):
		return toolError("script_not_observed", "read every script page in this Turn before proposing extracted Assets")
	case errors.Is(err, ErrAssetListNotObserved):
		return toolError("asset_list_not_observed", "read every current Asset page in this Turn before proposing extracted Assets")
	case errors.Is(err, ErrSourceAssetNotObserved):
		return toolError("source_asset_not_observed", "call get_prior_asset_candidate in this Turn before proposing a source_asset_id")
	case errors.Is(err, ErrAssetAlreadyPresent):
		return toolError("asset_already_present", "the proposed identity is already present in this Episode")
	default:
		return toolError("invalid_operation", "Prompt operation is invalid")
	}
}

func mapQueryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, projectapp.ErrEpisodeNotFound) || errors.Is(err, projectapp.ErrAssetNotFound) || errors.Is(err, projectapp.ErrClipNotFound) {
		return toolError("object_not_found", "requested project object was not found")
	}
	return toolError("project_read_failed", "project data could not be read")
}

func mapDraftError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, changesetapp.ErrInvalidRequest) {
		return toolError("invalid_arguments", "ChangeSet command is invalid")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "revision") || strings.Contains(message, "state") || strings.Contains(message, "precondition") || strings.Contains(message, "conflict") {
		return toolError("change_set_conflict", "ChangeSet changed or a precondition failed")
	}
	return toolError("change_set_failed", "ChangeSet command failed")
}

func EnabledToolNames() []string {
	return agentapp.ShotForwardMCPToolNames()
}
