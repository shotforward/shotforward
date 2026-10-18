package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

const contextCompactionTimeout = 3 * time.Minute

type pendingTurnStart struct {
	completedTurnID string // guarded by Supervisor.mu
}

// All fields are guarded by Supervisor.mu. A cancelled HTTP wait does not
// release this fence: the native task can still be running after its RPC ACK.
type contextCompaction struct {
	done            chan struct{}
	turnID          string
	cancelRequested bool
	finished        bool
	err             error
}

var _ agentapp.ContextCompactingRuntime = (*Supervisor)(nil)
var _ agentapp.ContextCompactionStatusRuntime = (*Supervisor)(nil)

func (s *Supervisor) ContextCompactionInProgress(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactions[threadID] != nil
}

// CompactThread delegates history management to Codex. It neither rewrites the
// rollout nor creates an application user Turn. Token-based automatic
// compaction remains governed by the Runtime's own model defaults.
func (s *Supervisor) CompactThread(ctx context.Context, threadID string) error {
	client, err := s.readyClient(ctx)
	if err != nil {
		return err
	}
	if threadID == "" {
		return errors.New("compact Codex context: Thread ID is required")
	}
	s.mu.Lock()
	if s.client != client || s.closed || s.stopping {
		s.mu.Unlock()
		return errSupervisorStopping
	}
	workspace := s.threadWorkspaces[threadID]
	if workspace == "" || !validSessionWorkspace(s.options.SessionWorkspaceRoot, workspace) {
		s.mu.Unlock()
		return errors.New("compact Codex context: Thread workspace is unavailable")
	}
	if s.compactions[threadID] != nil || s.activeTurns[threadID] != "" || s.pendingTurns[threadID] != nil {
		s.mu.Unlock()
		return agentapp.ErrContextCompactionBusy
	}
	flight := &contextCompaction{done: make(chan struct{})}
	if s.compactions == nil {
		s.compactions = make(map[string]*contextCompaction)
	}
	s.compactions[threadID] = flight
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, contextCompactionTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		s.mu.Lock()
		s.completeCompactionLocked(threadID, flight, err)
		s.mu.Unlock()
		return err
	}
	// An empty success response only acknowledges scheduling. Wait for the
	// native terminal turn notification below before declaring success.
	if err := client.Request(ctx, "thread/compact/start", map[string]any{"threadId": threadID}, nil); err != nil {
		var rejected *rpcError
		if errors.As(err, &rejected) {
			if rejected.Code == -32601 {
				err = agentapp.ErrContextCompactionUnavailable
			}
			s.mu.Lock()
			s.completeCompactionLocked(threadID, flight, err)
			s.mu.Unlock()
		} else {
			// A lost ACK is ambiguous, not proof that Codex did not start.
			s.cancelCompaction(client, threadID, flight)
		}
		return err
	}
	select {
	case <-flight.done:
		s.mu.Lock()
		err := flight.err
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		s.cancelCompaction(client, threadID, flight)
		return ctx.Err()
	}
}

func (s *Supervisor) cancelCompaction(client protocolClient, threadID string, flight *contextCompaction) {
	s.mu.Lock()
	if s.compactions[threadID] != flight {
		s.mu.Unlock()
		return
	}
	flight.cancelRequested = true
	turnID := flight.turnID
	s.mu.Unlock()
	if turnID != "" {
		s.interruptCompaction(client, threadID, turnID)
	}
}

func (s *Supervisor) interruptCompaction(client protocolClient, threadID, turnID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
		defer cancel()
		_ = client.Request(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
	}()
}

// Consume maintenance events before normal user-Turn event normalization. In
// particular turn/started must not claim an MCP binding or create chat output.
func (s *Supervisor) onCompactionNotification(notification rpcNotification) bool {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" {
		return false
	}
	turnID := params.TurnID
	if params.Turn.ID != "" {
		turnID = params.Turn.ID
	}
	s.mu.Lock()
	// Native terminal/item notifications may be repeated after the next user
	// Turn has started. They are still maintenance, not that user's Turn.
	if turnID != "" && s.compactedTurns[params.ThreadID] == turnID {
		s.mu.Unlock()
		return true
	}
	flight := s.compactions[params.ThreadID]
	if flight == nil {
		s.mu.Unlock()
		return false
	}
	// Only turn/started can identify this maintenance operation. A delayed
	// terminal/item notification from an earlier user Turn is not its ACK.
	if flight.turnID == "" {
		if notification.Method != "turn/started" || turnID == "" {
			s.mu.Unlock()
			return false
		}
		flight.turnID = turnID
	} else if turnID != "" && flight.turnID != turnID {
		s.mu.Unlock()
		return false
	}
	interrupt := notification.Method == "turn/started" && flight.cancelRequested
	client := s.client
	if notification.Method == "turn/completed" && turnID == flight.turnID {
		var err error
		if params.Turn.Status != "completed" {
			err = errors.New("Codex context compaction did not complete successfully")
		}
		s.completeCompactionLocked(params.ThreadID, flight, err)
	}
	s.mu.Unlock()
	if interrupt && client != nil {
		s.interruptCompaction(client, params.ThreadID, turnID)
	}
	return true
}

func (s *Supervisor) completeCompactionLocked(threadID string, flight *contextCompaction, err error) {
	if s.compactions[threadID] != flight {
		return
	}
	delete(s.compactions, threadID)
	if flight.turnID != "" {
		if s.compactedTurns == nil {
			s.compactedTurns = make(map[string]string)
		}
		s.compactedTurns[threadID] = flight.turnID
	}
	if !flight.finished {
		flight.finished, flight.err = true, err
		close(flight.done)
	}
}

func (s *Supervisor) finishCompactionsLocked(err error) {
	for threadID, flight := range s.compactions {
		s.completeCompactionLocked(threadID, flight, err)
	}
}
