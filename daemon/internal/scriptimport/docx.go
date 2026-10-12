package scriptimport

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

const maxDocumentXMLBytes = 16 << 20

func importDOCX(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: open DOCX package: %v", projectapp.ErrScriptImportUnreadable, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: inspect DOCX package: %v", projectapp.ErrScriptImportUnreadable, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: DOCX source is not a regular file", projectapp.ErrScriptImportUnreadable)
	}
	if info.Size() > maxDOCXFileBytes {
		return "", fmt.Errorf("%w: DOCX package exceeds the safe read limit", projectapp.ErrScriptImportInvalid)
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return "", fmt.Errorf("%w: open DOCX package: %v", projectapp.ErrScriptImportInvalid, err)
	}
	var document *zip.File
	for _, entry := range archive.File {
		if entry.Name == "word/document.xml" {
			document = entry
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("%w: DOCX has no word/document.xml", projectapp.ErrScriptImportInvalid)
	}
	if document.UncompressedSize64 > maxDocumentXMLBytes {
		return "", fmt.Errorf("%w: DOCX document XML exceeds the safe read limit", projectapp.ErrScriptImportInvalid)
	}
	reader, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("%w: open DOCX document XML: %v", projectapp.ErrScriptImportInvalid, err)
	}
	defer reader.Close()
	limited := &io.LimitedReader{R: reader, N: maxDocumentXMLBytes + 1}
	markdown, err := parseWordDocument(ctx, limited)
	if err != nil {
		return "", err
	}
	if limited.N <= 0 {
		return "", fmt.Errorf("%w: DOCX document XML exceeds the safe read limit", projectapp.ErrScriptImportInvalid)
	}
	return markdown, nil
}

func parseWordDocument(ctx context.Context, reader io.Reader) (string, error) {
	decoder := xml.NewDecoder(reader)
	paragraphs := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: parse DOCX document XML: %v", projectapp.ErrScriptImportInvalid, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "p" {
			continue
		}
		paragraph, style, err := readWordParagraph(decoder, start)
		if err != nil {
			return "", fmt.Errorf("%w: parse DOCX paragraph: %v", projectapp.ErrScriptImportInvalid, err)
		}
		paragraphs = append(paragraphs, formatWordParagraph(paragraph, style))
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n\n")), nil
}

func readWordParagraph(decoder *xml.Decoder, paragraphStart xml.StartElement) (string, string, error) {
	var content strings.Builder
	var style string
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", "", err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "pStyle":
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "val" {
						style = attribute.Value
						break
					}
				}
			case "t":
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return "", "", err
				}
				content.WriteString(text)
			case "tab":
				content.WriteByte('\t')
			case "br", "cr":
				content.WriteByte('\n')
			}
		case xml.EndElement:
			if value.Name == paragraphStart.Name {
				return content.String(), style, nil
			}
		}
	}
}

func formatWordParagraph(content, style string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	level := wordHeadingLevel(style)
	if level == 0 {
		return content
	}
	return strings.Repeat("#", level) + " " + content
}

func wordHeadingLevel(style string) int {
	normalized := strings.ToLower(style)
	normalized = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(normalized)
	if normalized == "title" {
		return 1
	}
	for level := 1; level <= 6; level++ {
		if normalized == fmt.Sprintf("heading%d", level) {
			return level
		}
	}
	return 0
}
