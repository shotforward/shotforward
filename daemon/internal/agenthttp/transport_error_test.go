package agenthttp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
)

func TestChangeSetPreconditionFailureMapsToSafeConflict(t *testing.T) {
	t.Parallel()
	mapped := mapError(errors.Join(changesetapp.ErrChangeSetPreconditionFailed, errors.New("private storage detail")))
	if mapped.status != http.StatusConflict || mapped.Code != "change_set_precondition_failed" || mapped.Retryable {
		t.Fatalf("mapped precondition failure = %#v", mapped)
	}
	if !strings.Contains(mapped.SuggestedAction, "Refresh the current project state") || strings.Contains(mapped.SuggestedAction, "recorded conflicts") {
		t.Fatalf("precondition failure recovery guidance = %q", mapped.SuggestedAction)
	}
	if strings.Contains(mapped.Message+mapped.SuggestedAction, "private storage detail") || len(mapped.Details) != 0 {
		t.Fatalf("precondition failure leaked internal detail: %#v", mapped)
	}
}
