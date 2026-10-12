package projectapp

import (
	"context"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type generationSubmissionIdentity struct {
	projectID    domain.ProjectID
	operationKey string
}

type generationSubmissionGate struct {
	token chan struct{}
	users int
}

// Serialize only replays of one operation. A waiting replay rechecks the
// durable batch before translation, so it cannot spend Agent quota twice.
func (s *Service) acquireGenerationSubmission(ctx context.Context, key generationSubmissionIdentity) (func(), error) {
	s.generationSubmissionMu.Lock()
	if s.generationSubmissions == nil {
		s.generationSubmissions = make(map[generationSubmissionIdentity]*generationSubmissionGate)
	}
	gate := s.generationSubmissions[key]
	if gate == nil {
		gate = &generationSubmissionGate{token: make(chan struct{}, 1)}
		s.generationSubmissions[key] = gate
	}
	gate.users++
	s.generationSubmissionMu.Unlock()
	retire := func() {
		s.generationSubmissionMu.Lock()
		defer s.generationSubmissionMu.Unlock()
		gate.users--
		if gate.users == 0 {
			delete(s.generationSubmissions, key)
		}
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; retire() }, nil
	case <-ctx.Done():
		retire()
		return nil, ctx.Err()
	}
}
