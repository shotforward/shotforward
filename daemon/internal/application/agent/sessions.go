package agentapp

import (
	"strings"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const maxSessionTitleRunes = 20

// SessionTitleFromText creates a stable, local-only title from the first
// non-empty line of a user message. It intentionally avoids a model call.
func SessionTitleFromText(value string) string {
	for _, rawLine := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		original := line
		line = strings.TrimSpace(strings.TrimLeft(line, "#>*+-"))
		line = strings.Trim(strings.TrimSpace(line), "`_*")
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			line = original
		}
		runes := []rune(line)
		if len(runes) > maxSessionTitleRunes {
			return string(runes[:maxSessionTitleRunes-1]) + "…"
		}
		return line
	}
	return ""
}

// IsDefaultSessionTitle identifies the empty/runtime placeholders that should
// be replaced once a conversation has meaningful user input.
func IsDefaultSessionTitle(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "new session", "new conversation", "new thread", "untitled", "新会话", "新对话":
		return true
	default:
		return false
	}
}

type CreateSessionInput struct {
	ProjectID         project.ProjectID
	EpisodeID         project.EpisodeID
	ModelID           string
	WebSearchDisabled bool
	RuntimeKind       RuntimeKind
}

type SessionSnapshot struct {
	Session    agent.AgentSession
	Turns      []agent.AgentTurn
	ChangeSets []changeset.ChangeSet
}

type ConversationMessage struct {
	ID                string
	ClientOperationID string
	Role              ConversationRole
	Phase             ConversationPhase
	Text              string
}

type ConversationTurn struct {
	Turn     agent.AgentTurn
	Messages []ConversationMessage
	Media    []project.AgentConversationMedia
}

type ConversationPage struct {
	Turns      []ConversationTurn
	NextCursor string
}
