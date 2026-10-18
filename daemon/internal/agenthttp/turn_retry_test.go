package agenthttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/agentsse"
	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func TestRetryNotificationCrossesHTTPAsNonTerminalScopedEvent(t *testing.T) {
	t.Parallel()
	sequence := []agentapp.StudioEvent{
		{Type: agentapp.EventTurnStarted, SessionID: testSession, TurnID: testTurn},
		{Type: agentapp.EventTurnRetrying, SessionID: testSession, TurnID: testTurn, FailureCode: "runtime_retrying"},
		{Type: agentapp.EventAssistantDelta, SessionID: testSession, TurnID: testTurn, TextDelta: "Resumed output"},
		{Type: agentapp.EventTurnCompleted, SessionID: testSession, TurnID: testTurn, TurnStatus: agent.TurnCompleted},
	}
	values := make([]agentsse.Event, 0, len(sequence))
	for position, event := range sequence {
		event.OccurredAt = testTime.Add(time.Duration(position) * time.Millisecond)
		if err := event.Validate(); err != nil {
			t.Fatalf("event %s fails public validation: %v", event.Type, err)
		}
		values = append(values, agentsse.Event{ID: fmt.Sprintf("EPOCH:%d", 8+position), Value: event})
	}
	handler, _ := testHandler(t, fakeEvents{values: values})
	request := authorizedRequest(http.MethodGet, "/api/v1/agent-sessions/"+testSession.String()+"/events?after=EPOCH%3A7", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("retry stream = %d %s", response.Code, response.Body.String())
	}
	var packets []map[string]any
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var packet map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &packet); err != nil {
			t.Fatal(err)
		}
		packets = append(packets, packet)
	}
	if len(packets) != len(sequence) {
		t.Fatalf("retry lost subsequent output or completion: %s", response.Body.String())
	}
	for position, packet := range packets {
		if packet["type"] != string(sequence[position].Type) || packet["session_id"] != testSession.String() || packet["turn_id"] != testTurn.String() || packet["occurred_at"] == nil {
			t.Fatalf("incorrectly scoped event %d: %#v", position, packet)
		}
	}
	retry := packets[1]
	if retry["failure_code"] != "runtime_retrying" || retry["turn_status"] != nil || retry["summary"] != nil || retry["text_delta"] != nil {
		t.Fatalf("retry was exposed as a terminal failure or message: %#v", retry)
	}
	if packets[3]["turn_status"] != "completed" || strings.Contains(strings.ToLower(response.Body.String()), "codex") {
		t.Fatalf("completion status or private protocol boundary changed: %s", response.Body.String())
	}
}
