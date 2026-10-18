package codexruntime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

func TestConversationAggregatesImageHistoryWithoutOneOversizedRPC(t *testing.T) {
	// Twenty image Turns exceed even the new 64 MiB JSONL bound if requested
	// together. Each response stays small, and unused image data is discarded
	// at the wire boundary, not retained in the aggregated application page.
	imageURL := "data:image/png;base64," + strings.Repeat("A", 4<<20)
	calls := 0
	client := &fakeProtocolClient{handler: func(method string, params map[string]any) (any, error) {
		if method != "thread/turns/list" || params["limit"] != 1 || params["itemsView"] != "full" {
			t.Fatalf("unbounded display history request: %s %#v", method, params)
		}
		wantCursor := ""
		if calls > 0 {
			wantCursor = strconv.Itoa(calls)
		}
		if cursor, _ := params["cursor"].(string); cursor != wantCursor {
			t.Fatalf("cursor=%q want=%q", cursor, wantCursor)
		}
		calls++
		return map[string]any{"data": []map[string]any{{
			"id": fmt.Sprintf("turn-%02d", calls), "status": "completed",
			"items": []map[string]any{
				{"id": "image", "type": "userMessage", "content": []map[string]any{
					{"type": "image", "url": imageURL}, {"type": "text", "text": "question"},
				}},
				{"id": "answer", "type": "agentMessage", "text": "answer", "phase": "final_answer"},
			},
		}}, "nextCursor": strconv.Itoa(calls)}, nil
	}}
	supervisor := &Supervisor{client: client, snapshot: agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}}
	page, err := supervisor.ListConversation(context.Background(), "thread", "", 20)
	if err != nil || calls != 20 || len(page.Turns) != 20 || page.NextCursor != "20" {
		t.Fatalf("page turns=%d next=%q calls=%d err=%v", len(page.Turns), page.NextCursor, calls, err)
	}
	if page.Turns[0].CodexTurnID != "turn-20" || page.Turns[19].CodexTurnID != "turn-01" {
		t.Fatal("display page is not chronological")
	}
	for _, turn := range page.Turns {
		if len(turn.Messages) != 2 || turn.Messages[0].Text != "question" || turn.Messages[1].Text != "answer" {
			t.Fatalf("text lost or image leaked into display: %#v", turn)
		}
	}
}

func TestConversationPagingStopsAtEndAndRejectsInvalidResponses(t *testing.T) {
	client := &fakeProtocolClient{handler: func(string, map[string]any) (any, error) {
		return map[string]any{"data": []any{}, "nextCursor": nil}, nil
	}}
	turns, next, err := readConversationTurns(context.Background(), client, "thread", "", 20)
	if err != nil || len(turns) != 0 || next != "" {
		t.Fatalf("empty history: %#v %q %v", turns, next, err)
	}
	for name, response := range map[string]map[string]any{
		"cycle":         {"data": []map[string]any{{"id": "turn"}}, "nextCursor": "current"},
		"empty cursor":  {"data": []map[string]any{{"id": "turn"}}, "nextCursor": ""},
		"empty page":    {"data": []any{}, "nextCursor": "next"},
		"too many":      {"data": []map[string]any{{"id": "a"}, {"id": "b"}}, "nextCursor": nil},
		"large cursor":  {"data": []map[string]any{{"id": "turn"}}, "nextCursor": strings.Repeat("a", maximumConversationCursorBytes+1)},
		"unsafe cursor": {"data": []map[string]any{{"id": "turn"}}, "nextCursor": "bad\x00cursor"},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeProtocolClient{handler: func(string, map[string]any) (any, error) { return response, nil }}
			if _, _, err := readConversationTurns(context.Background(), client, "thread", "current", 2); err == nil {
				t.Fatal("invalid display pagination accepted")
			}
		})
	}
}
