package agent

import (
	"errors"
	"testing"
)

func TestSchedulingAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		snapshot SchedulingSnapshot
		want     AdmissionDecision
		wantErr  error
	}{
		{name: "queue", snapshot: SchedulingSnapshot{GlobalActive: 5, GlobalQueued: 2}, want: AdmissionQueueTurn},
		{name: "steer active", snapshot: SchedulingSnapshot{GlobalActive: 1, SessionActive: 1}, want: AdmissionSteerTurn},
		{name: "session queue full", snapshot: SchedulingSnapshot{GlobalQueued: 1, SessionQueued: 1}, wantErr: ErrSessionQueueFull},
		{name: "global queue full", snapshot: SchedulingSnapshot{GlobalQueued: 50}, wantErr: ErrGlobalQueueFull},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.snapshot.AdmitInput()
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("AdmitInput() = %q, %v, want %q, %v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestSchedulingDispatchCapacity(t *testing.T) {
	t.Parallel()
	if err := (SchedulingSnapshot{GlobalActive: 4, GlobalQueued: 1, SessionQueued: 1}).CanDispatch(); err != nil {
		t.Fatalf("CanDispatch() error = %v", err)
	}
	if err := (SchedulingSnapshot{GlobalActive: 5, GlobalQueued: 1, SessionQueued: 1}).CanDispatch(); !errors.Is(err, ErrDispatchCapacityReached) {
		t.Fatalf("full CanDispatch() error = %v", err)
	}
	if err := (SchedulingSnapshot{SessionActive: 1}).Validate(); !errors.Is(err, ErrInvalidSchedulingSnapshot) {
		t.Fatalf("invalid snapshot error = %v", err)
	}
}
