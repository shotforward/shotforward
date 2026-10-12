// Package scriptimport reads supported local script files into normalized
// Markdown without retaining or modifying the source file.
package scriptimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	maxEncodedTextBytes = domain.MaxScriptCharacters*4 + 4
	maxDOCXFileBytes    = 32 << 20
)

type FileImporter struct{}

func (FileImporter) Import(ctx context.Context, sourcePath string) (projectapp.ImportedScript, error) {
	if ctx == nil {
		return projectapp.ImportedScript{}, errors.New("import script: context is required")
	}
	if err := ctx.Err(); err != nil {
		return projectapp.ImportedScript{}, err
	}
	path, format, err := validateSourcePath(sourcePath)
	if err != nil {
		return projectapp.ImportedScript{}, err
	}

	var markdown string
	switch format {
	case projectapp.ScriptImportText, projectapp.ScriptImportMarkdown:
		markdown, err = importTextFile(ctx, path)
	case projectapp.ScriptImportDOCX:
		markdown, err = importDOCX(ctx, path)
	default:
		err = projectapp.ErrScriptImportUnsupported
	}
	if err != nil {
		return projectapp.ImportedScript{}, err
	}
	markdown = domain.NormalizeMarkdown(markdown)
	characterCount := domain.ScriptCharacterCount(markdown)
	if characterCount > domain.MaxScriptCharacters {
		return projectapp.ImportedScript{}, fmt.Errorf(
			"%w: got %d characters, maximum is %d",
			projectapp.ErrScriptTooLong,
			characterCount,
			domain.MaxScriptCharacters,
		)
	}
	if err := domain.ValidateScriptMarkdown(markdown); err != nil {
		return projectapp.ImportedScript{}, fmt.Errorf("%w: %w", projectapp.ErrScriptImportInvalid, err)
	}
	return projectapp.ImportedScript{
		Markdown:       markdown,
		Format:         format,
		CharacterCount: characterCount,
	}, nil
}

func validateSourcePath(sourcePath string) (string, projectapp.ScriptImportFormat, error) {
	if strings.TrimSpace(sourcePath) == "" || !filepath.IsAbs(sourcePath) {
		return "", "", fmt.Errorf("%w: source path must be absolute", projectapp.ErrInvalidRequest)
	}
	path := filepath.Clean(sourcePath)
	var format projectapp.ScriptImportFormat
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt":
		format = projectapp.ScriptImportText
	case ".md":
		format = projectapp.ScriptImportMarkdown
	case ".docx":
		format = projectapp.ScriptImportDOCX
	default:
		return "", "", projectapp.ErrScriptImportUnsupported
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", projectapp.ErrScriptImportUnreadable, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: source is not a regular file", projectapp.ErrScriptImportUnreadable)
	}
	if format == projectapp.ScriptImportDOCX && info.Size() > maxDOCXFileBytes {
		return "", "", fmt.Errorf("%w: DOCX package exceeds the safe read limit", projectapp.ErrScriptImportInvalid)
	}
	return path, format, nil
}

func importTextFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", projectapp.ErrScriptImportUnreadable, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEncodedTextBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %v", projectapp.ErrScriptImportUnreadable, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > maxEncodedTextBytes {
		return "", fmt.Errorf("%w: encoded source exceeds the maximum possible size", projectapp.ErrScriptTooLong)
	}
	decoded, err := decodeText(data)
	if err != nil {
		return "", err
	}
	return decoded, nil
}
