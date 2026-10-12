package agentmcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

func TestInitializeIncludesHistoricalAssetReuseInstructions(t *testing.T) {
	t.Parallel()
	server, endpoint, credential, _, _ := newMCPFixture(t)
	status, body := postRPC(t, server, endpoint, credential.Value, "initialize", map[string]any{"protocolVersion": mcpProtocolVersion}, "")
	var response struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &response) != nil {
		t.Fatalf("initialize = %d %s", status, body)
	}
	if !strings.Contains(response.Result.Instructions, agentapp.AssetReuseInstructions) {
		t.Fatal("MCP initialization omitted the historical Asset reuse workflow")
	}
	if !strings.Contains(response.Result.Instructions, agentapp.VideoPromptInstructions) {
		t.Fatal("MCP initialization omitted the all-path model-aware video Prompt workflow")
	}
}
