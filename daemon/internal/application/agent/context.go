package agentapp

import (
	"context"
	"errors"
)

var (
	ErrContextCompactionUnavailable = errors.New("Agent Runtime does not support context compaction")
	ErrContextCompactionBusy        = errors.New("Agent context cannot be compacted while work is active")
)

// ContextCompactingRuntime is optional so older Runtime adapters remain usable.
// Success means native compaction finished, not just that it was acknowledged.
// The Runtime owns its history; application chat records stay put.
type ContextCompactingRuntime interface {
	CompactThread(context.Context, string) error
}

// ContextCompactionStatusRuntime keeps admission fenced if an HTTP caller stops
// waiting before the native operation reaches a terminal notification.
type ContextCompactionStatusRuntime interface {
	ContextCompactionInProgress(string) bool
}
