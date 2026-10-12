package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shotforward/shotforward/daemon/internal/config"
)

func TestRunPrintsVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"--version"},
		&stdout,
		&stderr,
		func(string) (string, bool) { return "", false },
	)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "shotforwardd ") {
		t.Fatalf("stdout = %q, want version information", stdout.String())
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := run(
		nil,
		&bytes.Buffer{},
		&stderr,
		func(key string) (string, bool) {
			if key == "SHOTFORWARD_LOG_LEVEL" {
				return "verbose", true
			}
			return "", false
		},
	)

	if exitCode != 1 {
		t.Fatalf("run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "SHOTFORWARD_LOG_LEVEL") {
		t.Fatalf("stderr = %q, want configuration error", stderr.String())
	}
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := run(
		[]string{"serve"},
		&bytes.Buffer{},
		&stderr,
		config.LookupEnv(func(string) (string, bool) { return "", false }),
	)

	if exitCode != 2 {
		t.Fatalf("run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "unexpected arguments") {
		t.Fatalf("stderr = %q, want unexpected argument error", stderr.String())
	}
}
