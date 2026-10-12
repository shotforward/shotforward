package projectstore

import (
	"errors"
	"fmt"
	"testing"

	changesetapp "github.com/shotforward/shotforward/daemon/internal/application/changeset"
)

func TestChangeSetApplicationAdapterPreservesPreconditionFailure(t *testing.T) {
	t.Parallel()
	storageError := fmt.Errorf("undo_target_changed: %w", ErrChangeSetPreconditionFailed)
	mapped := mapChangeSetApplicationError(storageError)
	for _, expected := range []error{changesetapp.ErrChangeSetPreconditionFailed, ErrChangeSetPreconditionFailed, storageError} {
		if !errors.Is(mapped, expected) {
			t.Fatalf("mapped precondition failure lost %v: %v", expected, mapped)
		}
	}
}

func TestChangeSetApplicationAdapterLeavesUnrelatedErrorsUnchanged(t *testing.T) {
	t.Parallel()
	if mapped := mapChangeSetApplicationError(nil); mapped != nil {
		t.Fatalf("mapped nil error = %v", mapped)
	}
	unrelated := errors.New("unrelated persistence failure")
	if mapped := mapChangeSetApplicationError(unrelated); mapped != unrelated {
		t.Fatalf("mapped unrelated error = %v", mapped)
	}
}
