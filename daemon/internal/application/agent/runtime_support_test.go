package agentapp

import (
	"errors"
	"testing"
)

func TestSecureIDGeneratorCreatesAgentIDs(t *testing.T) {
	t.Parallel()
	generator := NewSecureIDGenerator()
	sessionID, err := generator.NewSessionID()
	if err != nil || sessionID.Validate() != nil {
		t.Fatalf("NewSessionID() = %q, %v", sessionID, err)
	}
	turnID, err := generator.NewTurnID()
	if err != nil || turnID.Validate() != nil {
		t.Fatalf("NewTurnID() = %q, %v", turnID, err)
	}
	bindingID, err := generator.NewMCPBindingID()
	if err != nil || bindingID.Validate() != nil {
		t.Fatalf("NewMCPBindingID() = %q, %v", bindingID, err)
	}
	if sessionID.String() == turnID.String() || turnID.String() == bindingID.String() {
		t.Fatal("typed Agent IDs unexpectedly match")
	}
}

func TestSecureIDGeneratorRequiresRandomSource(t *testing.T) {
	t.Parallel()
	if _, err := (SecureIDGenerator{}).NewSessionID(); err == nil {
		t.Fatal("NewSessionID() error = nil")
	}
	failing := SecureIDGenerator{reader: errorReader{}}
	if _, err := failing.NewTurnID(); err == nil {
		t.Fatal("NewTurnID() error = nil")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random failed") }
