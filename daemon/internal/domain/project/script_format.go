package project

import "strings"

type ScriptDiagnosticSeverity string

const (
	ScriptDiagnosticWarning ScriptDiagnosticSeverity = "warning"
)

type ScriptDiagnosticCode string

const (
	ScriptDiagnosticUnsupportedHeading ScriptDiagnosticCode = "unsupported_heading"
	ScriptDiagnosticUnsupportedBlock   ScriptDiagnosticCode = "unsupported_block"
)

// ScriptDiagnostic describes a non-blocking editor-format issue. Line numbers
// are one-based so transports can pass them directly to the editor.
type ScriptDiagnostic struct {
	Severity ScriptDiagnosticSeverity
	Code     ScriptDiagnosticCode
	Line     int
	Message  string
}

// DiagnoseScriptFormat checks only the deliberately small editor surface:
// level-two/three ATX headings and body paragraphs. It does not judge content.
func DiagnoseScriptFormat(markdown string) []ScriptDiagnostic {
	markdown = NormalizeMarkdown(markdown)
	diagnostics := make([]ScriptDiagnostic, 0)
	for index, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if level, ok := markdownHeadingLevel(trimmed); ok {
			if level != 2 && level != 3 {
				diagnostics = append(diagnostics, ScriptDiagnostic{
					Severity: ScriptDiagnosticWarning,
					Code:     ScriptDiagnosticUnsupportedHeading,
					Line:     index + 1,
					Message:  "剧本编辑器只支持二级和三级标题，该行将按普通兼容内容显示",
				})
			}
			continue
		}
		if isUnsupportedMarkdownBlock(trimmed) {
			diagnostics = append(diagnostics, ScriptDiagnostic{
				Severity: ScriptDiagnosticWarning,
				Code:     ScriptDiagnosticUnsupportedBlock,
				Line:     index + 1,
				Message:  "该 Markdown 块格式不在轻量剧本编辑器的正式支持范围内",
			})
		}
	}
	return diagnostics
}

func markdownHeadingLevel(line string) (int, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || (line[level] != ' ' && line[level] != '\t') {
		return 0, false
	}
	return level, true
}

func isUnsupportedMarkdownBlock(line string) bool {
	if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") ||
		strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") ||
		strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "<") {
		return true
	}
	dot := strings.IndexByte(line, '.')
	if dot < 1 || dot+1 >= len(line) || line[dot+1] != ' ' {
		return false
	}
	for _, character := range line[:dot] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
