package codexruntime

import (
	"strings"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

func TestSupervisorIncludesHistoricalAssetReuseInstructions(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "Custom host guidance."} {
		t.Run(base, func(t *testing.T) {
			var clients []*fakeProtocolClient
			supervisor, err := newSupervisor(Options{WorkingDirectory: t.TempDir(), BaseInstructions: base}, fakeDependencies(&clients))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(supervisor.options.BaseInstructions, agentapp.AssetReuseInstructions) {
				t.Fatal("Runtime instructions omitted the historical Asset reuse workflow")
			}
			if !strings.Contains(supervisor.options.BaseInstructions, agentapp.VideoPromptInstructions) {
				t.Fatal("Runtime instructions omitted the all-path model-aware video Prompt workflow")
			}
			if base != "" && !strings.HasPrefix(supervisor.options.BaseInstructions, base) {
				t.Fatal("Runtime instructions replaced custom host guidance")
			}
		})
	}
}
