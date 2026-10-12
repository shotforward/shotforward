package agentapp

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"

	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

const randomIDBytes = 16

var unpaddedBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type IDGenerator interface {
	NewSessionID() (agent.SessionID, error)
	NewTurnID() (agent.TurnID, error)
	NewMCPBindingID() (agent.MCPBindingID, error)
}

type SecureIDGenerator struct {
	reader io.Reader
}

func NewSecureIDGenerator() SecureIDGenerator {
	return SecureIDGenerator{reader: rand.Reader}
}

func (g SecureIDGenerator) NewSessionID() (agent.SessionID, error) {
	value, err := g.generate("ags_")
	if err != nil {
		return "", fmt.Errorf("generate Agent Session ID: %w", err)
	}
	return agent.ParseSessionID(value)
}

func (g SecureIDGenerator) NewTurnID() (agent.TurnID, error) {
	value, err := g.generate("agt_")
	if err != nil {
		return "", fmt.Errorf("generate Agent Turn ID: %w", err)
	}
	return agent.ParseTurnID(value)
}

func (g SecureIDGenerator) NewMCPBindingID() (agent.MCPBindingID, error) {
	value, err := g.generate("amb_")
	if err != nil {
		return "", fmt.Errorf("generate Agent MCP Binding ID: %w", err)
	}
	return agent.ParseMCPBindingID(value)
}

func (g SecureIDGenerator) generate(prefix string) (string, error) {
	if g.reader == nil {
		return "", errors.New("random source is required")
	}
	buffer := make([]byte, randomIDBytes)
	if _, err := io.ReadFull(g.reader, buffer); err != nil {
		return "", fmt.Errorf("read secure randomness: %w", err)
	}
	return prefix + unpaddedBase32.EncodeToString(buffer), nil
}
