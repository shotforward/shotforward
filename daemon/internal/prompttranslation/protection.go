package prompttranslation

import (
	"regexp"
	"slices"
	"strings"
)

const markerPrefix = "__SF_PT_"

const headerNames = `subject_definitions|summary|retention_analysis|detailed_description|integrated_multimodal_description|overall_soundscape|non_diegetic_music`

var (
	literalTagPattern = regexp.MustCompile(`</?(?:d|lyrics|on_screen_text|literal)>`)
	tagPattern        = regexp.MustCompile(`</?[A-Za-z][^<>\r\n]*>`)
	headerPattern     = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]+)?(?:` + headerNames + `)(?:[ \t]*:|[ \t]*$)`)
	jsonHeaderPattern = regexp.MustCompile(`"(?:` + headerNames + `)"[ \t]*:`)
	shotPattern       = regexp.MustCompile(`\[Shot[ \t]+[0-9]+\]`)
	timestampPattern  = regexp.MustCompile(`\b[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?:\.[0-9]{1,3})?\b`)
	speakerPattern    = regexp.MustCompile(`\(S[0-9]+\)`)
	newlinePattern    = regexp.MustCompile(`\n`)
	// Fixed relationship values from MiniMax's official full-reference guide,
	// section 4. Only relation-entry positions are protected: ordinary English
	// prose containing the word "reference" must remain translatable.
	// https://huggingface.co/MiniMaxAI/MiniMax-H3/raw/main/docs/VIDEO_PROMPT_WRITING_GUIDE_ref_en.md
	retentionValuePattern = regexp.MustCompile(`<(?:Subject|Picture|Video|Audio)[ \t]+[0-9]+>[^\r\n]*?:[ \t]*(fully_preserved|partially_preserved|attribute_transfer|weak_reference|fully_copy|partially_copy|reference)\b`)
)

type span struct {
	start, end int
	boundary   bool
}

type protectedToken struct {
	marker, value string
	boundary      bool
	block         int
}

type protectedPrompt struct {
	text   string
	tokens []protectedToken
}

func protect(prompt string) (protectedPrompt, error) {
	if strings.Contains(prompt, markerPrefix) {
		return protectedPrompt{}, validationError("input uses the reserved marker namespace")
	}
	spans, err := literalSpans(prompt)
	if err != nil {
		return protectedPrompt{}, err
	}
	patterns := []struct {
		pattern  *regexp.Regexp
		boundary bool
	}{
		{headerPattern, true}, {jsonHeaderPattern, true}, {shotPattern, true},
		{newlinePattern, true}, {tagPattern, false}, {timestampPattern, false}, {speakerPattern, false},
	}
	for _, location := range retentionValuePattern.FindAllStringSubmatchIndex(prompt, -1) {
		spans = append(spans, span{start: location[2], end: location[3]})
	}
	for _, item := range patterns {
		for _, location := range item.pattern.FindAllStringIndex(prompt, -1) {
			start, end := location[0], location[1]
			if item.pattern == jsonHeaderPattern {
				// Keep the quotes and colon so the protected input stays valid JSON.
				end = start + strings.LastIndex(prompt[start:end], `"`)
				start++
			}
			spans = append(spans, span{start: start, end: end, boundary: item.boundary})
		}
	}
	slices.SortFunc(spans, func(a, b span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end // Whole literal blocks take precedence over their tags.
	})
	var text strings.Builder
	result := protectedPrompt{}
	end, block := 0, 0
	for _, item := range spans {
		if item.start < end {
			continue
		}
		text.WriteString(prompt[end:item.start])
		token := protectedToken{marker: marker(len(result.tokens)), value: prompt[item.start:item.end], boundary: item.boundary, block: block}
		text.WriteString(token.marker)
		result.tokens = append(result.tokens, token)
		if item.boundary {
			block++
		}
		end = item.end
	}
	text.WriteString(prompt[end:])
	result.text = text.String()
	return result, nil
}

// Only explicit tagged literals are opaque. Quotation marks can enclose JSON
// values or ordinary descriptive prose and are therefore not protected.
func literalSpans(prompt string) ([]span, error) {
	type openTag struct {
		name  string
		start int
	}
	var stack []openTag
	var result []span
	for _, location := range literalTagPattern.FindAllStringIndex(prompt, -1) {
		tag := prompt[location[0]:location[1]]
		if strings.HasPrefix(tag, "</") {
			name := tag[2 : len(tag)-1]
			if len(stack) == 0 || stack[len(stack)-1].name != name {
				return nil, validationError("literal tags are not balanced")
			}
			opening := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				result = append(result, span{start: opening.start, end: location[1]})
			}
		} else {
			stack = append(stack, openTag{name: tag[1 : len(tag)-1], start: location[0]})
		}
	}
	if len(stack) != 0 {
		return nil, validationError("literal tags are not balanced")
	}
	return result, nil
}

func (p protectedPrompt) restore(output string) (string, error) {
	if strings.TrimSpace(output) == "" || invalidText(output) {
		return "", validationError("invalid translated text")
	}
	if strings.Count(output, "```") > strings.Count(p.text, "```") {
		return "", validationError("translation added a Markdown fence")
	}
	type occurrence struct {
		position int
		token    protectedToken
	}
	ordered := make([]occurrence, 0, len(p.tokens))
	replacements := make([]string, 0, len(p.tokens)*2)
	var boundaries []string
	for _, token := range p.tokens {
		if strings.Count(output, token.marker) != 1 {
			return "", validationError("protected fragment is missing or duplicated")
		}
		ordered = append(ordered, occurrence{position: strings.Index(output, token.marker), token: token})
		replacements = append(replacements, token.marker, "")
		if token.boundary {
			boundaries = append(boundaries, token.marker)
		}
	}
	plainOutput := strings.NewReplacer(replacements...).Replace(output)
	if strings.Contains(plainOutput, markerPrefix) {
		return "", validationError("translation added an unknown protected fragment")
	}
	for _, pattern := range []*regexp.Regexp{tagPattern, headerPattern, jsonHeaderPattern, shotPattern, timestampPattern, speakerPattern, newlinePattern} {
		if pattern.MatchString(output) {
			return "", validationError("translation added structural markup")
		}
	}
	plainInput := strings.NewReplacer(replacements...).Replace(p.text)
	if strings.TrimSpace(plainInput) != "" && strings.TrimSpace(plainOutput) == "" {
		return "", validationError("translation removed the descriptive content")
	}
	slices.SortFunc(ordered, func(a, b occurrence) int { return a.position - b.position })
	block := 0
	for _, item := range ordered {
		if item.token.boundary {
			if block >= len(boundaries) || item.token.marker != boundaries[block] {
				return "", validationError("translation changed section, line, or shot order")
			}
			block++
		} else if item.token.block != block {
			return "", validationError("protected content moved across a structural boundary")
		}
	}
	for index, token := range p.tokens {
		replacements[index*2+1] = token.value
	}
	return strings.NewReplacer(replacements...).Replace(output), nil
}
