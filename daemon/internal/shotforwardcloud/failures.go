package shotforwardcloud

import (
	"encoding/json"
	"net/http"
)

func cloudFailureCode(code string) string {
	switch code {
	case "negative_balance", "insufficient_points", "wallet_not_found":
		return "shotforward_" + code
	default:
		return ""
	}
}

func cloudErrorCode(status int, body []byte) string {
	if status != http.StatusPaymentRequired {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return cloudFailureCode(envelope.Error.Code)
}

type agentFailure struct{ code string }

// BeginAgentTurn retains only a recognized business code for an active thread.
// Pointer identity keeps an old subscription's cleanup from clearing a new turn.
func (m *Manager) BeginAgentTurn(threadID string) func() string {
	entry := &agentFailure{}
	m.failureMu.Lock()
	if m.agentFailures == nil {
		m.agentFailures = make(map[string]*agentFailure)
	}
	m.agentFailures[threadID] = entry
	m.failureMu.Unlock()
	return func() string {
		m.failureMu.Lock()
		defer m.failureMu.Unlock()
		if m.agentFailures[threadID] == entry {
			delete(m.agentFailures, threadID)
		}
		return entry.code
	}
}

func (m *Manager) recordAgentFailure(threadID, code string) {
	if code == "" {
		return
	}
	m.failureMu.Lock()
	defer m.failureMu.Unlock()
	if entry := m.agentFailures[threadID]; entry != nil {
		entry.code = code
	}
}

func (m *Manager) EndAgentTurn(threadID string) string {
	m.failureMu.Lock()
	defer m.failureMu.Unlock()
	if entry := m.agentFailures[threadID]; entry != nil {
		delete(m.agentFailures, threadID)
		return entry.code
	}
	return ""
}
