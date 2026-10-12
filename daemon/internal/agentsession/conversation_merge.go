package agentsession

import (
	"sort"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

func localInputMessage(sessionID agent.SessionID, input agent.InputOperation) agentapp.ConversationMessage {
	return agentapp.ConversationMessage{
		ID: publicMessageID(sessionID, "input:"+input.ClientOperationID), ClientOperationID: input.ClientOperationID,
		Role: agentapp.ConversationUser, Text: input.InputText,
	}
}

// Each persisted operation may replace at most one Runtime user item. Equal
// text is not a message identity: sending the same words twice is two inputs.
// The store returns inputs in submission order, which also supplies anchors for
// missing steering items without moving those inputs ahead of earlier replies.
func mergeConversationMessages(sessionID agent.SessionID, runtimeMessages []agentapp.RuntimeConversationMessage, inputs []agent.InputOperation) []agentapp.ConversationMessage {
	inputs = append([]agent.InputOperation(nil), inputs...)
	sort.SliceStable(inputs, func(i, j int) bool {
		// The queued input necessarily precedes steering even when both were
		// persisted in the same millisecond and operation IDs sort differently.
		if inputs[i].Action != inputs[j].Action {
			return inputs[i].Action == agent.InputQueueTurn
		}
		if inputs[i].CreatedAt.Equal(inputs[j].CreatedAt) {
			return inputs[i].ClientOperationID < inputs[j].ClientOperationID
		}
		return inputs[i].CreatedAt.Before(inputs[j].CreatedAt)
	})
	messages := make([]agentapp.ConversationMessage, len(runtimeMessages))
	matched := make([]bool, len(inputs))
	anchors := make([]int, len(runtimeMessages))
	for index, message := range runtimeMessages {
		anchors[index] = -1
		messages[index] = agentapp.ConversationMessage{
			ID: publicMessageID(sessionID, message.ItemID), Role: message.Role, Phase: message.Phase, Text: message.Text,
		}
		if message.Role != agentapp.ConversationUser {
			continue
		}
		text := conversationUserInputText(message.Text, inputs)
		messages[index].Text = text
		for inputIndex, input := range inputs {
			if matched[inputIndex] || input.InputText == "" || input.InputText != text {
				continue
			}
			matched[inputIndex], anchors[index] = true, inputIndex
			messages[index] = localInputMessage(sessionID, input)
			break
		}
	}
	// Buckets are insertion positions before a Runtime item (or after the final
	// item). Missing queued input starts its Turn; a steer belongs after existing
	// replies unless a subsequent persisted user item provides a closer anchor.
	before := make([][]agentapp.ConversationMessage, len(messages)+1)
	for inputIndex, input := range inputs {
		if matched[inputIndex] || input.InputText == "" {
			continue
		}
		position := len(messages)
		if input.Action == agent.InputQueueTurn {
			position = 0
		} else {
			for messageIndex, anchor := range anchors {
				if anchor > inputIndex {
					position = messageIndex
					break
				}
			}
		}
		before[position] = append(before[position], localInputMessage(sessionID, input))
	}
	result := make([]agentapp.ConversationMessage, 0, len(messages)+len(inputs))
	for index, message := range messages {
		result = append(result, before[index]...)
		result = append(result, message)
	}
	return append(result, before[len(messages)]...)
}

// Runtime pagination remains authoritative for history. Only the newest page
// gets missing local Turns in its window; older cursor pages must not repeat
// current inputs. With no Runtime anchor yet, cap the fallback to the latest
// requested number of locally submitted Turns rather than loading all history.
func missingFirstPageTurns(turns []agent.AgentTurn, page []agentapp.ConversationTurn, present map[agent.TurnID]bool, inputs map[agent.TurnID][]agent.InputOperation, sessionID agent.SessionID, limit int) []agent.AgentTurn {
	ordered := append([]agent.AgentTurn(nil), turns...)
	sort.SliceStable(ordered, func(i, j int) bool { return conversationTurnBefore(ordered[i], ordered[j]) })
	var oldest *agent.AgentTurn
	for _, value := range page {
		if oldest == nil || conversationTurnBefore(value.Turn, *oldest) {
			turn := value.Turn
			oldest = &turn
		}
	}
	missing := make([]agent.AgentTurn, 0)
	for _, turn := range ordered {
		if turn.AgentSessionID != sessionID || present[turn.ID] || !hasConversationInput(inputs[turn.ID]) {
			continue
		}
		// IDs stabilize display order, but random IDs cannot prove that a
		// same-millisecond Turn predates the Runtime page boundary.
		if oldest != nil && turn.Status.IsTerminal() && turn.CreatedAt.Before(oldest.CreatedAt) {
			continue
		}
		missing = append(missing, turn)
	}
	if oldest == nil && len(missing) > limit {
		missing = missing[len(missing)-limit:]
	}
	return missing
}

func hasConversationInput(inputs []agent.InputOperation) bool {
	for _, input := range inputs {
		if input.InputText != "" {
			return true
		}
	}
	return false
}

func conversationTurnBefore(left, right agent.AgentTurn) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID.String() < right.ID.String()
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func sortConversationTurns(turns []agentapp.ConversationTurn) {
	sort.SliceStable(turns, func(i, j int) bool { return conversationTurnBefore(turns[i].Turn, turns[j].Turn) })
}
