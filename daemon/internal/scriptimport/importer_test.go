package scriptimport

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func TestImportUTF8MarkdownWithoutRetainingSource(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "episode.md")
	content := "## 场景一\r\n\r\n雨夜。"
	if err := os.WriteFile(path, append(append([]byte{}, utf8BOM...), []byte(content)...), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (FileImporter{}).Import(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != projectapp.ScriptImportMarkdown || result.Markdown != "## 场景一\n\n雨夜。" {
		t.Fatalf("Import() = %#v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source file was modified or removed: %v", err)
	}
}

func TestImportSupportedTextEncodings(t *testing.T) {
	t.Parallel()

	want := "第二集：雨夜来信"
	tests := []struct {
		name string
		data func(*testing.T) []byte
	}{
		{name: "UTF-8", data: func(*testing.T) []byte { return []byte(want) }},
		{name: "UTF-16LE", data: func(t *testing.T) []byte {
			return append([]byte{0xff, 0xfe}, encodeText(t, want, textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM).NewEncoder())...)
		}},
		{name: "UTF-16BE", data: func(t *testing.T) []byte {
			return append([]byte{0xfe, 0xff}, encodeText(t, want, textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM).NewEncoder())...)
		}},
		{name: "GB18030", data: func(t *testing.T) []byte {
			return encodeText(t, want, simplifiedchinese.GB18030.NewEncoder())
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "episode.txt")
			if err := os.WriteFile(path, test.data(t), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := (FileImporter{}).Import(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if result.Markdown != want || result.CharacterCount != domain.ScriptCharacterCount(want) {
				t.Fatalf("Import() = %#v", result)
			}
		})
	}
}

func TestImportDOCXParagraphsAndHeadingStyles(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "episode.docx")
	writeDOCX(t, path, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>场景一</w:t></w:r></w:p>
<w:p><w:r><w:t>雨夜。</w:t></w:r><w:r><w:tab/><w:t>林晚出现。</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading3"/></w:pPr><w:r><w:t>对白</w:t></w:r></w:p>
</w:body></w:document>`)
	result, err := (FileImporter{}).Import(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := "## 场景一\n\n雨夜。\t林晚出现。\n\n### 对白"
	if result.Format != projectapp.ScriptImportDOCX || result.Markdown != want {
		t.Fatalf("Import(DOCX) = %#v, want %q", result, want)
	}
}

func TestImportRejectsWholeOverLimitScript(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "episode.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("字", domain.MaxScriptCharacters+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileImporter{}).Import(context.Background(), path); !errors.Is(err, projectapp.ErrScriptTooLong) {
		t.Fatalf("Import(over limit) error = %v, want ErrScriptTooLong", err)
	}
}

func TestImportRejectsUnsupportedUnreadableAndInvalidFiles(t *testing.T) {
	t.Parallel()

	importer := FileImporter{}
	if _, err := importer.Import(context.Background(), filepath.Join(t.TempDir(), "episode.pdf")); !errors.Is(err, projectapp.ErrScriptImportUnsupported) {
		t.Fatalf("unsupported error = %v", err)
	}
	if _, err := importer.Import(context.Background(), filepath.Join(t.TempDir(), "missing.md")); !errors.Is(err, projectapp.ErrScriptImportUnreadable) {
		t.Fatalf("missing error = %v", err)
	}
	invalidText := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(invalidText, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), invalidText); !errors.Is(err, projectapp.ErrScriptImportEncoding) {
		t.Fatalf("invalid encoding error = %v", err)
	}
	invalidUTF16 := filepath.Join(t.TempDir(), "invalid-utf16.txt")
	if err := os.WriteFile(invalidUTF16, []byte{0xff, 0xfe, 0x00, 0xd8}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), invalidUTF16); !errors.Is(err, projectapp.ErrScriptImportEncoding) {
		t.Fatalf("invalid UTF-16 error = %v", err)
	}
	invalidDOCX := filepath.Join(t.TempDir(), "invalid.docx")
	if err := os.WriteFile(invalidDOCX, []byte("not a ZIP package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), invalidDOCX); !errors.Is(err, projectapp.ErrScriptImportInvalid) {
		t.Fatalf("invalid DOCX error = %v", err)
	}
}

func TestImportHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FileImporter{}).Import(ctx, filepath.Join(t.TempDir(), "episode.md")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Import(cancelled) error = %v", err)
	}
}

func encodeText(t *testing.T, value string, transformer transform.Transformer) []byte {
	t.Helper()
	encoded, _, err := transform.String(transformer, value)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(encoded)
}

func writeDOCX(t *testing.T, path, documentXML string) {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(documentXML)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
