package projectapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestImportScriptReplacesEpisodeAndReturnsDiagnostics(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	importer := dependencies.Importer.(*fakeScriptImporter)
	markdown := "# 单集标题\r\n\r\n## 场景一\r\n\r\n雨夜。"
	importer.result = ImportedScript{
		Markdown:       markdown,
		Format:         ScriptImportMarkdown,
		CharacterCount: domain.ScriptCharacterCount(domain.NormalizeMarkdown(markdown)),
	}
	result, err := service.ImportScript(context.Background(), ImportScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, SourcePath: "/scripts/episode.md",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if err != nil {
		t.Fatalf("ImportScript() error = %v", err)
	}
	if importer.calls != 1 || importer.path != "/scripts/episode.md" || store.saveScriptCalls != 1 {
		t.Fatalf("calls = importer %d path %q, Store %d", importer.calls, importer.path, store.saveScriptCalls)
	}
	if result.Episode.Script.Markdown != domain.NormalizeMarkdown(markdown) || result.Format != ScriptImportMarkdown {
		t.Fatalf("ImportScript() = %#v", result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != domain.ScriptDiagnosticUnsupportedHeading {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestImportScriptRejectsStaleRevisionBeforeReadingFile(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	importer := dependencies.Importer.(*fakeScriptImporter)
	_, err := service.ImportScript(context.Background(), ImportScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, SourcePath: "/scripts/episode.md",
		ExpectedScriptRevision: episode.Script.Revision + 1,
	})
	if !errors.Is(err, ErrRevisionConflict) || importer.calls != 0 || store.saveScriptCalls != 0 {
		t.Fatalf("stale import = %v, importer %d, Store %d", err, importer.calls, store.saveScriptCalls)
	}
}

func TestImportScriptFailureLeavesEpisodeUnchanged(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	importer := dependencies.Importer.(*fakeScriptImporter)
	importer.err = ErrScriptImportEncoding
	_, err := service.ImportScript(context.Background(), ImportScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, SourcePath: "/scripts/episode.txt",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrScriptImportEncoding) || store.saveScriptCalls != 0 || store.episode.Script != episode.Script {
		t.Fatalf("failed import = %v, Store calls %d, Script %#v", err, store.saveScriptCalls, store.episode.Script)
	}
}

func TestImportScriptRejectsInconsistentImporterResult(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	importer := dependencies.Importer.(*fakeScriptImporter)
	importer.result = ImportedScript{Markdown: "正文", Format: ScriptImportMarkdown, CharacterCount: 999}
	_, err := service.ImportScript(context.Background(), ImportScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, SourcePath: "/scripts/episode.md",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrScriptImportInvalid) || store.saveScriptCalls != 0 {
		t.Fatalf("inconsistent import = %v, Store calls %d", err, store.saveScriptCalls)
	}
}

func TestValidateScriptReportsWarningsWithoutRejectingContent(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, _ := openedEpisodeService(t)
	result, err := service.ValidateScript(context.Background(), ValidateScriptInput{
		Markdown: "## 场景一\r\n\r\n正文\r\n\r\n- 临时列表",
	})
	if err != nil {
		t.Fatalf("ValidateScript() error = %v", err)
	}
	if result.CharacterCount != domain.ScriptCharacterCount("## 场景一\n\n正文\n\n- 临时列表") || len(result.Diagnostics) != 1 {
		t.Fatalf("ValidateScript() = %#v", result)
	}
}

func TestScriptCharacterLimitRejectsWholeContent(t *testing.T) {
	t.Parallel()

	service, dependencies, store, _, project, episode := openedEpisodeService(t)
	tooLong := strings.Repeat("字", domain.MaxScriptCharacters+1)
	_, err := service.SaveScript(context.Background(), SaveScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, Markdown: tooLong,
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrScriptTooLong) || store.saveScriptCalls != 0 || store.episode.Script != episode.Script {
		t.Fatalf("over-limit save = %v, Store calls %d", err, store.saveScriptCalls)
	}

	importer := dependencies.Importer.(*fakeScriptImporter)
	importer.result = ImportedScript{
		Markdown: tooLong, Format: ScriptImportText, CharacterCount: domain.MaxScriptCharacters + 1,
	}
	_, err = service.ImportScript(context.Background(), ImportScriptInput{
		ProjectID: project.ID, EpisodeID: episode.ID, SourcePath: "/scripts/episode.txt",
		ExpectedScriptRevision: episode.Script.Revision,
	})
	if !errors.Is(err, ErrScriptTooLong) || store.saveScriptCalls != 0 || store.episode.Script != episode.Script {
		t.Fatalf("over-limit import = %v, Store calls %d", err, store.saveScriptCalls)
	}

	validation, err := service.ValidateScript(context.Background(), ValidateScriptInput{Markdown: tooLong})
	if !errors.Is(err, ErrScriptTooLong) || validation.CharacterCount != domain.MaxScriptCharacters+1 {
		t.Fatalf("over-limit validation = %#v, %v", validation, err)
	}
}
