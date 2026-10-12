package codexruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestDirectToolModelCatalogEnablesClientToolsAndFullResponses(t *testing.T) {
	input := []byte(`{"catalog_version":"kept","models":[{"slug":"gpt-test","display_name":"GPT Test","tool_mode":"code_mode_only","use_responses_lite":true,"custom":"kept"}]}`)
	output, err := directToolModelCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Version string           `json:"catalog_version"`
		Models  []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(output, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Version != "kept" || len(catalog.Models) != 1 || catalog.Models[0]["tool_mode"] != "direct" || catalog.Models[0]["use_responses_lite"] != false || catalog.Models[0]["custom"] != "kept" {
		t.Fatalf("normalized catalog = %#v", catalog.Models)
	}
}

func TestDirectToolModelCatalogAddsCurrentCloudAliases(t *testing.T) {
	input := []byte(`{"models":[
		{"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"}]},
		{"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"ultra"}]}
	]}`)
	output, err := directToolModelCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(output, &catalog); err != nil {
		t.Fatal(err)
	}
	models := make(map[string]map[string]any, len(catalog.Models))
	for _, model := range catalog.Models {
		models[model["slug"].(string)] = model
	}
	for _, slug := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-6-luna", "gpt-6-sol"} {
		if models[slug] == nil {
			t.Fatalf("model %q missing from %#v", slug, models)
		}
		if models[slug]["tool_mode"] != "direct" || models[slug]["use_responses_lite"] != false {
			t.Fatalf("model %q does not use direct/full Responses: %#v", slug, models[slug])
		}
	}
	if models["gpt-6-luna"]["display_name"] != "GPT-6-Luna" || models["gpt-6-luna"]["default_reasoning_level"] != "medium" {
		t.Fatalf("Luna alias = %#v", models["gpt-6-luna"])
	}
	if models["gpt-6-sol"]["display_name"] != "GPT-6-Sol" || models["gpt-6-sol"]["default_reasoning_level"] != "low" {
		t.Fatalf("Sol alias = %#v", models["gpt-6-sol"])
	}
}

func TestDirectToolModelCatalogKeepsBundledCloudModel(t *testing.T) {
	input := []byte(`{"models":[
		{"slug":"gpt-5.6-luna","display_name":"Old Luna"},
		{"slug":"gpt-6-luna","display_name":"Bundled Luna","custom":"keep"}
	]}`)
	output, err := directToolModelCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(output, &catalog); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, model := range catalog.Models {
		if model["slug"] == "gpt-6-luna" {
			count++
			if model["display_name"] != "Bundled Luna" || model["custom"] != "keep" {
				t.Fatalf("bundled Luna was replaced: %#v", model)
			}
		}
	}
	if count != 1 {
		t.Fatalf("gpt-6-luna count = %d", count)
	}
}

func TestDirectToolModelCatalogRejectsInvalidCatalogs(t *testing.T) {
	for _, input := range [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(`{"models":[]}`),
		[]byte(`{"models":[{"display_name":"Missing slug"}]}`),
	} {
		if _, err := directToolModelCatalog(input); err == nil {
			t.Fatalf("invalid catalog accepted: %s", input)
		}
	}
}

func TestWritePrivateModelCatalogReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-catalog.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateModelCatalog(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("catalog data=%q mode=%#o", data, info.Mode().Perm())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode=%#o", info.Mode().Perm())
	}
}

func TestWritePrivateModelCatalogRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "model-catalog.json")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writePrivateModelCatalog(link, []byte("replace")); err == nil {
		t.Fatal("symlink model catalog path was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
}

func TestSupervisorPreparesAndLoadsDirectToolModelCatalog(t *testing.T) {
	probe := standardProbe()
	sandboxProbe := standardProbe()
	formal := &fakeProtocolClient{handler: runtimeHandler(false)}
	clients := []*fakeProtocolClient{probe, sandboxProbe, formal}
	deps := fakeDependencies(&clients)
	prepared := false
	var starts [][]string
	deps.prepareModelCatalog = func(_ context.Context, executable, target, workingDirectory string, environment []string) error {
		prepared = executable == "/bin/codex" && filepath.Base(target) == "model-catalog.json" && workingDirectory != "" && len(environment) > 0
		return nil
	}
	baseNewClient := deps.newClient
	deps.newClient = func(executable string, args []string, cwd string, environment []string, timeout time.Duration, notify func(rpcNotification), exit func(error)) (protocolClient, error) {
		starts = append(starts, append([]string(nil), args...))
		return baseNewClient(executable, args, cwd, environment, timeout, notify, exit)
	}
	root := filepath.Clean(t.TempDir())
	catalogPath := filepath.Join(root, "model-catalog.json")
	supervisor, err := newSupervisor(Options{
		WorkingDirectory: root, DirectToolModelCatalogPath: catalogPath,
		Environment: []string{"SHOTFORWARD_MCP_TOKEN=test-token"},
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !prepared || len(starts) != 3 {
		t.Fatalf("prepared=%v starts=%d", prepared, len(starts))
	}
	expected := `model_catalog_json=` + strconv.Quote(catalogPath)
	for _, args := range starts {
		if !containsArgument(args, expected) {
			t.Fatalf("model catalog config missing from args: %v", args)
		}
	}
}

func TestInstalledCodexDirectToolModelCatalog(t *testing.T) {
	executable := os.Getenv("SHOTFORWARD_TEST_DIRECT_TOOL_CODEX")
	if executable == "" {
		t.Skip("set SHOTFORWARD_TEST_DIRECT_TOOL_CODEX to probe an installed Codex runtime")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "model-catalog.json")
	if err := prepareDirectToolModelCatalog(context.Background(), executable, path, directory, os.Environ()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) == 0 {
		t.Fatal("prepared catalog has no models")
	}
	for _, model := range catalog.Models {
		if model["tool_mode"] != "direct" || model["use_responses_lite"] != false {
			t.Fatalf("model %v is not direct/full: %#v", model["slug"], model)
		}
	}
}
