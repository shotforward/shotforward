package agent

import (
	"errors"
	"testing"
)

func TestAgentTypedIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parse func(string) error
		value string
		want  error
	}{
		{name: "session", value: "ags_" + testSuffix, parse: func(value string) error { _, err := ParseSessionID(value); return err }},
		{name: "turn", value: "agt_" + testSuffix, parse: func(value string) error { _, err := ParseTurnID(value); return err }},
		{name: "binding", value: "amb_" + testSuffix, parse: func(value string) error { _, err := ParseMCPBindingID(value); return err }},
		{name: "wrong prefix", value: "agt_" + testSuffix, parse: func(value string) error { _, err := ParseSessionID(value); return err }, want: ErrInvalidSessionID},
		{name: "lowercase", value: "ags_abcdefghijklmnopqrstuvwxyz", parse: func(value string) error { _, err := ParseSessionID(value); return err }, want: ErrInvalidSessionID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse(test.value); !errors.Is(err, test.want) {
				t.Fatalf("parse(%q) error = %v, want %v", test.value, err, test.want)
			}
		})
	}
}
