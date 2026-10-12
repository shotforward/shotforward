package agenthttp_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

type preparationHistoryRuntime struct {
	*scriptedRuntime
	historyReads atomic.Int32
}

func (r *preparationHistoryRuntime) ListConversation(ctx context.Context, id, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	r.historyReads.Add(1)
	return r.scriptedRuntime.ListConversation(ctx, id, cursor, limit)
}

// Exercises HTTP -> Session service -> real SQLite/MCP without sending a paid
// model request or touching the user's workspace or credentials.
func TestNewSessionEmptyHistoryAvoidsRuntimeRoundTripEndToEnd(t *testing.T) {
	if os.Getenv("SHOTFORWARD_TEST_AGENT_E2E") != "1" {
		t.Skip("set SHOTFORWARD_TEST_AGENT_E2E=1 to run the loopback Agent stack")
	}
	var runtime *preparationHistoryRuntime
	fixture := newE2EFixture(t, func(token agentmcp.Credential, _ string) agentapp.Runtime {
		runtime = &preparationHistoryRuntime{scriptedRuntime: newScriptedRuntime(token.Value)}
		return runtime
	})
	ctx := context.Background()
	sessionID := fixture.createSession(t, ctx)
	path := "/api/v1/agent-sessions/" + sessionID.String() + "/messages?limit=20"
	for i := 0; i < 3; i++ {
		response := fixture.request(t, ctx, http.MethodGet, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"turns":[]`) {
			t.Fatalf("empty conversation status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if runtime.historyReads.Load() != 0 {
		t.Fatalf("new Session performed %d unnecessary history reads", runtime.historyReads.Load())
	}
	fixture.submitAndProcess(t, ctx, sessionID, "preparation-first-input", "synthetic prompt after preparation")
	response := fixture.request(t, ctx, http.MethodGet, path, "")
	if response.Code != http.StatusOK || runtime.historyReads.Load() != 1 {
		t.Fatalf("first Turn history was skipped: status=%d reads=%d body=%s", response.Code, runtime.historyReads.Load(), response.Body.String())
	}
}
