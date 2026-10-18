package codexruntime

import (
	"context"
	"errors"
	"strings"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

// Recovery needs Turn IDs and statuses, never image payloads or tool output.
// Use the native metadata-only view instead of hydrating an entire rollout into
// one RPC message. This does not prune or alter the Runtime's model context.
func readTurnStatuses(ctx context.Context, client protocolClient, threadID string) ([]agentapp.RuntimeTurnSnapshot, error) {
	turns := make([]agentapp.RuntimeTurnSnapshot, 0)
	cursor := ""
	seen := make(map[string]bool)
	for page := 0; page < 1000; page++ {
		params := map[string]any{"threadId": threadID, "limit": 100, "sortDirection": "asc", "itemsView": "notLoaded"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := client.Request(ctx, "thread/turns/list", params, &response); err != nil {
			return nil, err
		}
		for _, turn := range response.Data {
			if turn.ID != "" {
				turns = append(turns, agentapp.RuntimeTurnSnapshot{CodexTurnID: turn.ID, Status: mapPersistedTurnStatus(turn.Status)})
			}
		}
		if response.NextCursor == nil {
			return turns, nil
		}
		cursor = *response.NextCursor
		if cursor == "" || len(cursor) > maximumConversationCursorBytes || strings.ContainsRune(cursor, 0) || seen[cursor] {
			return nil, errors.New("thread/turns/list returned an invalid recovery cursor")
		}
		seen[cursor] = true
	}
	return nil, errors.New("thread/turns/list exceeded the recovery page limit")
}

// Both "summary" and "full" inline historical image data in Codex 0.154.
// Fetch one native Turn per RPC, then aggregate the display page after decoding
// away non-display fields. A long image chat must not become one huge JSONL
// response that disconnects every Thread sharing this Runtime.
func readConversationTurns(ctx context.Context, client protocolClient, threadID, cursor string, limit int) ([]wireConversationTurn, string, error) {
	turns := make([]wireConversationTurn, 0, limit)
	seen := map[string]bool{cursor: true}
	for len(turns) < limit {
		params := map[string]any{"threadId": threadID, "limit": 1, "sortDirection": "desc", "itemsView": "full"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []wireConversationTurn `json:"data"`
			NextCursor *string                `json:"nextCursor"`
		}
		if err := client.Request(ctx, "thread/turns/list", params, &response); err != nil {
			return nil, "", err
		}
		if len(response.Data) > 1 || (len(response.Data) == 0 && response.NextCursor != nil) {
			return nil, "", errors.New("thread/turns/list returned an invalid display page")
		}
		turns = append(turns, response.Data...)
		if response.NextCursor == nil {
			return turns, "", nil
		}
		cursor = *response.NextCursor
		if cursor == "" || len(cursor) > maximumConversationCursorBytes || strings.ContainsRune(cursor, 0) || seen[cursor] {
			return nil, "", errors.New("thread/turns/list returned an invalid display cursor")
		}
		seen[cursor] = true
	}
	return turns, cursor, nil
}
