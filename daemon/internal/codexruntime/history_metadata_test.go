package codexruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func TestReadThreadLoadsRecoveryMetadataWithoutHydratingImages(t *testing.T) {
	client := &fakeProtocolClient{}
	pages := 0
	client.handler = func(method string, params map[string]any) (any, error) {
		switch method {
		case "thread/read":
			if params["includeTurns"] != false || params["threadId"] != "thread" {
				t.Fatalf("history was fully hydrated: %#v", params)
			}
			return map[string]any{"thread": map[string]any{"id": "thread", "sessionId": "session", "name": "Images"}}, nil
		case "thread/turns/list":
			if params["itemsView"] != "notLoaded" || params["sortDirection"] != "asc" || params["limit"] != 100 || params["threadId"] != "thread" {
				t.Fatalf("unexpected metadata view: %#v", params)
			}
			pages++
			if pages == 1 {
				return map[string]any{"data": []map[string]any{{"id": "old", "status": "completed"}}, "nextCursor": "next"}, nil
			}
			if params["cursor"] != "next" {
				t.Fatalf("lost pagination: %#v", params)
			}
			return map[string]any{"data": []map[string]any{{"id": "current", "status": "inProgress"}}, "nextCursor": nil}, nil
		default:
			return nil, errors.New("unexpected RPC: " + method)
		}
	}
	supervisor := &Supervisor{client: client, snapshot: agentapp.RuntimeSnapshot{Health: agentapp.RuntimeReady}}
	thread, err := supervisor.ReadThread(context.Background(), "thread")
	if err != nil || pages != 2 || len(thread.Turns) != 2 || thread.Turns[0].Status != agent.TurnCompleted || thread.Turns[1].Status != agent.TurnRunning {
		t.Fatalf("metadata recovery: %#v, pages=%d, err=%v", thread, pages, err)
	}
}

func TestRecoveryMetadataRejectsInvalidPaginationAndRPCFailures(t *testing.T) {
	for _, cursor := range []string{"repeat", "", "bad\x00cursor", strings.Repeat("x", maximumConversationCursorBytes+1)} {
		t.Run(cursor[:min(len(cursor), 12)], func(t *testing.T) {
			calls := 0
			client := &fakeProtocolClient{handler: func(string, map[string]any) (any, error) {
				calls++
				return map[string]any{"data": []any{}, "nextCursor": cursor}, nil
			}}
			if _, err := readTurnStatuses(context.Background(), client, "thread"); err == nil || calls > 2 {
				t.Fatalf("invalid cursor accepted: calls=%d, err=%v", calls, err)
			}
		})
	}
	want := errors.New("disconnected")
	client := &fakeProtocolClient{handler: func(string, map[string]any) (any, error) { return nil, want }}
	if _, err := readTurnStatuses(context.Background(), client, "thread"); !errors.Is(err, want) {
		t.Fatalf("RPC failure was hidden: %v", err)
	}
}
