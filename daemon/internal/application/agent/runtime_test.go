package agentapp

import (
	"errors"
	"testing"
)

func TestRuntimeSnapshotFailureContract(t *testing.T) {
	t.Parallel()
	if err := (RuntimeSnapshot{Health: RuntimeReady, Version: "0.153.0"}).Validate(); err != nil {
		t.Fatalf("ready snapshot error = %v", err)
	}
	if err := (RuntimeSnapshot{Health: RuntimeDisconnected}).Validate(); !errors.Is(err, ErrInvalidRuntimeValue) {
		t.Fatalf("disconnected snapshot error = %v", err)
	}
	if err := (RuntimeSnapshot{Health: RuntimeDisconnected, FailureCode: "runtime_disconnected", FailureSummary: "Runtime disconnected"}).Validate(); err != nil {
		t.Fatalf("valid disconnected snapshot error = %v", err)
	}
}

func TestRuntimeSnapshotNotInstalledRequiresSafeFailure(t *testing.T) {
	t.Parallel()
	if err := (RuntimeSnapshot{Health: RuntimeNotInstalled}).Validate(); !errors.Is(err, ErrInvalidRuntimeValue) {
		t.Fatalf("not-installed snapshot error = %v", err)
	}
	value := RuntimeSnapshot{Health: RuntimeNotInstalled, FailureCode: "codex_not_installed", FailureSummary: "Codex executable was not found"}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid not-installed snapshot error = %v", err)
	}
}

func TestRuntimeSnapshotUpgradeRequirementIncludesMinimumVersion(t *testing.T) {
	t.Parallel()
	value := RuntimeSnapshot{
		Health: RuntimeIncompatible, Version: "0.155.0", MinimumVersion: "0.155.1",
		FailureCode: "codex_upgrade_required", FailureSummary: "Update local Codex",
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid upgrade snapshot error = %v", err)
	}
	value.MinimumVersion = ""
	if err := value.Validate(); !errors.Is(err, ErrInvalidRuntimeValue) {
		t.Fatalf("missing minimum version error = %v", err)
	}
}

func TestRuntimeSnapshotSafeExecutionContract(t *testing.T) {
	t.Parallel()
	ready := RuntimeSnapshot{
		Health: RuntimeReady, SafeExecutionAvailable: true,
		SafeExecutionBackend: "codex_linux_sandbox",
	}
	if err := ready.Validate(); err != nil {
		t.Fatalf("ready safe-execution snapshot error = %v", err)
	}
	ready.SafeExecutionFailureCode = "sandbox_failed"
	ready.SafeExecutionFailureSummary = "Sandbox failed"
	if err := ready.Validate(); !errors.Is(err, ErrInvalidRuntimeValue) {
		t.Fatalf("available snapshot with failure error = %v", err)
	}
	unavailable := RuntimeSnapshot{
		Health: RuntimeReady, SafeExecutionFailureCode: "sandbox_unavailable",
		SafeExecutionFailureSummary: "Safe execution is unavailable",
	}
	if err := unavailable.Validate(); err != nil {
		t.Fatalf("unavailable safe-execution snapshot error = %v", err)
	}
}
