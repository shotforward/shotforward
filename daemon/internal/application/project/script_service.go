package projectapp

import (
	"context"
	"fmt"
	"strings"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

// ImportScript reads an external source and explicitly replaces the current
// Episode script under the same revision guard used by direct editor saves.
func (s *Service) ImportScript(ctx context.Context, input ImportScriptInput) (ImportScriptResult, error) {
	if err := validateContext(ctx); err != nil {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w", err)
	}
	if strings.TrimSpace(input.SourcePath) == "" {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w: source path is required", ErrInvalidRequest)
	}
	if input.ExpectedScriptRevision < 1 {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w: expected script revision is required", ErrInvalidRequest)
	}
	current, err := s.GetEpisode(ctx, GetEpisodeInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID})
	if err != nil {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w", err)
	}
	if current.Script.Revision != input.ExpectedScriptRevision {
		return ImportScriptResult{}, fmt.Errorf(
			"import Episode script: %w: expected %d, current %d",
			ErrRevisionConflict,
			input.ExpectedScriptRevision,
			current.Script.Revision,
		)
	}

	imported, err := s.importer.Import(ctx, input.SourcePath)
	if err != nil {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w", err)
	}
	markdown := domain.NormalizeMarkdown(imported.Markdown)
	characterCount := domain.ScriptCharacterCount(markdown)
	if characterCount > domain.MaxScriptCharacters {
		return ImportScriptResult{}, fmt.Errorf(
			"import Episode script: %w: got %d characters, maximum is %d",
			ErrScriptTooLong,
			characterCount,
			domain.MaxScriptCharacters,
		)
	}
	if err := domain.ValidateScriptMarkdown(markdown); err != nil {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w: %w", ErrScriptImportInvalid, err)
	}
	if !validScriptImportFormat(imported.Format) || imported.CharacterCount != characterCount {
		return ImportScriptResult{}, fmt.Errorf("import Episode script: %w: importer returned inconsistent metadata", ErrScriptImportInvalid)
	}

	episodeResult, saveErr := s.SaveScript(ctx, SaveScriptInput{
		ProjectID:              input.ProjectID,
		EpisodeID:              input.EpisodeID,
		Markdown:               markdown,
		ExpectedScriptRevision: input.ExpectedScriptRevision,
	})
	result := ImportScriptResult{
		EpisodeResult:  episodeResult,
		Format:         imported.Format,
		CharacterCount: characterCount,
		Diagnostics:    domain.DiagnoseScriptFormat(markdown),
	}
	if saveErr != nil {
		return result, fmt.Errorf("import Episode script: %w", saveErr)
	}
	return result, nil
}

// ValidateScript returns hard content errors separately from non-blocking
// editor-format diagnostics and never changes Project state.
func (s *Service) ValidateScript(ctx context.Context, input ValidateScriptInput) (ValidateScriptResult, error) {
	if err := validateContext(ctx); err != nil {
		return ValidateScriptResult{}, fmt.Errorf("validate script: %w", err)
	}
	markdown := domain.NormalizeMarkdown(input.Markdown)
	characterCount := domain.ScriptCharacterCount(markdown)
	if characterCount > domain.MaxScriptCharacters {
		return ValidateScriptResult{CharacterCount: characterCount, Diagnostics: []domain.ScriptDiagnostic{}}, fmt.Errorf(
			"validate script: %w: got %d characters, maximum is %d",
			ErrScriptTooLong,
			characterCount,
			domain.MaxScriptCharacters,
		)
	}
	if err := domain.ValidateScriptMarkdown(markdown); err != nil {
		return ValidateScriptResult{CharacterCount: characterCount, Diagnostics: []domain.ScriptDiagnostic{}}, fmt.Errorf(
			"validate script: %w: %w",
			ErrInvalidRequest,
			err,
		)
	}
	return ValidateScriptResult{
		CharacterCount: characterCount,
		Diagnostics:    domain.DiagnoseScriptFormat(markdown),
	}, nil
}

func validScriptImportFormat(format ScriptImportFormat) bool {
	switch format {
	case ScriptImportText, ScriptImportMarkdown, ScriptImportDOCX:
		return true
	default:
		return false
	}
}
