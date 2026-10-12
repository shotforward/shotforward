// Package agent owns local AgentSession, AgentTurn, scheduling, and MCP
// binding invariants. It contains no Codex app-server or transport types.
package agent

import (
	"errors"
	"fmt"
	"strings"
)

const minimumRandomIDSuffixLength = 26

var (
	ErrInvalidSessionID    = errors.New("invalid Agent Session ID")
	ErrInvalidTurnID       = errors.New("invalid Agent Turn ID")
	ErrInvalidMCPBindingID = errors.New("invalid Agent MCP Binding ID")
)

type SessionID string

func ParseSessionID(value string) (SessionID, error) {
	if err := validateTypedID(value, "ags_", ErrInvalidSessionID); err != nil {
		return "", err
	}
	return SessionID(value), nil
}

func (id SessionID) String() string { return string(id) }
func (id SessionID) Validate() error {
	return validateTypedID(string(id), "ags_", ErrInvalidSessionID)
}

type TurnID string

func ParseTurnID(value string) (TurnID, error) {
	if err := validateTypedID(value, "agt_", ErrInvalidTurnID); err != nil {
		return "", err
	}
	return TurnID(value), nil
}

func (id TurnID) String() string { return string(id) }
func (id TurnID) Validate() error {
	return validateTypedID(string(id), "agt_", ErrInvalidTurnID)
}

type MCPBindingID string

func ParseMCPBindingID(value string) (MCPBindingID, error) {
	if err := validateTypedID(value, "amb_", ErrInvalidMCPBindingID); err != nil {
		return "", err
	}
	return MCPBindingID(value), nil
}

func (id MCPBindingID) String() string { return string(id) }
func (id MCPBindingID) Validate() error {
	return validateTypedID(string(id), "amb_", ErrInvalidMCPBindingID)
}

func validateTypedID(value, prefix string, category error) error {
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%w: expected prefix %q", category, prefix)
	}
	suffix := strings.TrimPrefix(value, prefix)
	if len(suffix) < minimumRandomIDSuffixLength {
		return fmt.Errorf("%w: random suffix is too short", category)
	}
	for _, character := range suffix {
		if (character >= 'A' && character <= 'Z') || (character >= '2' && character <= '7') {
			continue
		}
		return fmt.Errorf("%w: random suffix must use RFC 4648 base32", category)
	}
	return nil
}
