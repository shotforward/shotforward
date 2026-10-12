package projectstore

import (
	"context"
	"errors"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

func TestApplicationMutationErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input error
		want  error
	}{
		{name: "Project conflict", input: ErrProjectRevisionConflict, want: projectapp.ErrRevisionConflict},
		{name: "missing Episode", input: ErrEpisodeNotFound, want: projectapp.ErrEpisodeNotFound},
		{name: "Episode conflict", input: ErrEpisodeRevisionConflict, want: projectapp.ErrRevisionConflict},
		{name: "Script conflict", input: ErrScriptRevisionConflict, want: projectapp.ErrRevisionConflict},
		{name: "sequence conflict", input: ErrEpisodeSequenceConflict, want: projectapp.ErrRevisionConflict},
		{name: "invalid data", input: ErrInvalidStoredProjectData, want: projectapp.ErrProjectDataInvalid},
		{name: "write failure", input: errors.New("disk failure"), want: projectapp.ErrProjectWriteFailed},
		{name: "cancelled", input: context.Canceled, want: context.Canceled},
		{name: "deadline", input: context.DeadlineExceeded, want: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := mapApplicationMutationError("test", test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("mapped error = %v, want %v", err, test.want)
			}
		})
	}
	if err := mapApplicationMutationError("test", nil); err != nil {
		t.Fatalf("nil mapped error = %v", err)
	}
}
