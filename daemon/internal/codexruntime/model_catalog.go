package codexruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	maximumBundledModelCatalogBytes = 4 * 1024 * 1024
	maximumCatalogDiagnosticBytes   = 8 * 1024
)

// The bundled Codex catalog can lag behind the model IDs published by the
// ShotForward Responses endpoint. These aliases retain Codex's model-specific
// capability metadata while allowing the cloud catalog to remain the
// authoritative availability allowlist. Once Codex bundles a target slug, the
// existing entry wins and no alias is added.
var directToolModelAliases = []struct {
	sourceSlug, targetSlug, displayName string
}{
	{sourceSlug: "gpt-5.6-luna", targetSlug: "gpt-6-luna", displayName: "GPT-6-Luna"},
	{sourceSlug: "gpt-5.6-sol", targetSlug: "gpt-6-sol", displayName: "GPT-6-Sol"},
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = b.buffer.Write(value[:remaining])
	}
	if remaining < len(value) {
		b.overflow = true
	}
	return written, nil
}

func prepareDirectToolModelCatalog(ctx context.Context, executable, targetPath, workingDirectory string, environment []string) error {
	if ctx == nil || executable == "" || targetPath == "" || !filepath.IsAbs(targetPath) || workingDirectory == "" {
		return errors.New("prepare Codex direct-tool model catalog: invalid input")
	}
	command := exec.CommandContext(ctx, executable, "debug", "models", "--bundled")
	command.Dir = workingDirectory
	command.Env = environment
	stdout := &limitedBuffer{limit: maximumBundledModelCatalogBytes}
	stderr := &limitedBuffer{limit: maximumCatalogDiagnosticBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		diagnostic := safeSummary(errors.New(stderr.buffer.String()))
		if diagnostic != "" {
			return fmt.Errorf("read bundled Codex model catalog: %w: %s", err, diagnostic)
		}
		return fmt.Errorf("read bundled Codex model catalog: %w", err)
	}
	if stdout.overflow {
		return errors.New("bundled Codex model catalog exceeds size limit")
	}
	normalized, err := directToolModelCatalog(stdout.buffer.Bytes())
	if err != nil {
		return err
	}
	return writePrivateModelCatalog(targetPath, normalized)
}

func directToolModelCatalog(data []byte) ([]byte, error) {
	var catalog map[string]any
	if len(data) == 0 || len(data) > maximumBundledModelCatalogBytes || json.Unmarshal(data, &catalog) != nil {
		return nil, errors.New("bundled Codex model catalog is invalid")
	}
	models, ok := catalog["models"].([]any)
	if !ok || len(models) == 0 {
		return nil, errors.New("bundled Codex model catalog is invalid")
	}
	for _, value := range models {
		model, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("bundled Codex model catalog contains an invalid model")
		}
		slug, _ := model["slug"].(string)
		if slug == "" {
			return nil, errors.New("bundled Codex model catalog contains an invalid model")
		}
		model["tool_mode"] = "direct"
		model["use_responses_lite"] = false
	}
	models = appendDirectToolModelAliases(models)
	catalog["models"] = models
	result, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, errors.New("encode Codex direct-tool model catalog")
	}
	return append(result, '\n'), nil
}

func appendDirectToolModelAliases(models []any) []any {
	bySlug := make(map[string]map[string]any, len(models))
	for _, value := range models {
		if model, ok := value.(map[string]any); ok {
			if slug, ok := model["slug"].(string); ok {
				bySlug[slug] = model
			}
		}
	}
	for _, alias := range directToolModelAliases {
		if _, exists := bySlug[alias.targetSlug]; exists {
			continue
		}
		source, exists := bySlug[alias.sourceSlug]
		if !exists {
			continue
		}
		cloned := make(map[string]any, len(source))
		for key, value := range source {
			cloned[key] = value
		}
		cloned["slug"] = alias.targetSlug
		cloned["display_name"] = alias.displayName
		models = append(models, cloned)
		bySlug[alias.targetSlug] = cloned
	}
	return models
}

func writePrivateModelCatalog(path string, data []byte) error {
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return errors.New("Codex model catalog directory is unavailable")
	}
	if existing, statErr := os.Lstat(path); statErr == nil {
		if !existing.Mode().IsRegular() {
			return errors.New("Codex model catalog path is not a regular file")
		}
		if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, data) {
			return os.Chmod(path, 0o600)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	temporary, err := os.CreateTemp(directory, ".model-catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Codex model catalog: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install Codex direct-tool model catalog: %w", err)
	}
	return nil
}
