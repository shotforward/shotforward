package project

import (
	"fmt"
	"regexp"
	"strings"
)

const MaxModelClipDurationSeconds int64 = 30

var clipVideoPromptStructuralPrefix = regexp.MustCompile(`^\s*(?:\[?\d{1,2}:\d{2}(?::\d{2})?\s*[-–—~至]\s*\d{1,2}:\d{2}(?::\d{2})?\]?|\d+(?:\.\d+)?\s*(?:秒(?:钟)?|s|seconds?)(?:单镜头|镜头)?)\s*[，,：:\-—\s]*`)

// ValidateClipVideoPromptForWrite rejects structural information that belongs
// in Clip fields, as well as cross-Clip dependencies that make an independent
// generation request ambiguous. Empty Prompts remain valid while a manually
// created Clip is being prepared.
func ValidateClipVideoPromptForWrite(value string) error {
	value = strings.TrimSpace(NormalizeStoryboardText(value))
	if value == "" {
		return nil
	}
	if clipVideoPromptStructuralPrefix.MatchString(value) {
		return fmt.Errorf("%w: video prompt must not start with duration or a first-shot timestamp", ErrInvalidClip)
	}
	if strings.HasPrefix(value, "参考资产：") || strings.HasPrefix(value, "参考素材：") {
		return fmt.Errorf("%w: video prompt must use Assets in semantic context instead of a generic reference list", ErrInvalidClip)
	}
	lower := strings.ToLower(value)
	for _, dependency := range []string{"上一镜", "上一片段", "前一镜", "previous clip", "previous shot"} {
		if strings.Contains(lower, dependency) {
			return fmt.Errorf("%w: video prompt must be self-contained and cannot depend on another Clip", ErrInvalidClip)
		}
	}
	return nil
}

func ValidateClipPlanningDuration(seconds int64, portable bool) error {
	maximum := MaxModelClipDurationSeconds
	if portable {
		maximum = MaxPortableClipDurationSeconds
	}
	if seconds < MinPortableClipDurationSeconds || seconds > maximum {
		return fmt.Errorf("%w: planned duration must be between %d and %d seconds", ErrInvalidClip, MinPortableClipDurationSeconds, maximum)
	}
	return nil
}
