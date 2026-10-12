package generation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratorStatusRequiresExactlyOneAbsoluteSuccessfulOutput(t *testing.T) {
	t.Parallel()
	outputRoot := t.TempDir()
	valid := GeneratorStatus{State: GeneratorSucceeded, Stage: "complete", Outputs: []GeneratorOutput{{SourcePath: filepath.Join(outputRoot, "output.mp4")}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid GeneratorStatus error = %v", err)
	}
	for _, outputs := range [][]GeneratorOutput{
		nil,
		{{SourcePath: "relative.mp4"}},
		{{SourcePath: filepath.Join(outputRoot, "one.mp4")}, {SourcePath: filepath.Join(outputRoot, "two.mp4")}},
	} {
		value := valid
		value.Outputs = outputs
		if err := value.Validate(); !errors.Is(err, ErrInvalidGeneratorResult) {
			t.Errorf("outputs %#v error = %v", outputs, err)
		}
	}
}

func TestGeneratorErrorProducesValidBoundedJobFailure(t *testing.T) {
	t.Parallel()
	code, message := generatorError("generator_failed", errors.New(string([]byte{0xff, 0})+strings.Repeat("界", 2_100)))
	if code != "generator_failed" || strings.ContainsRune(message, 0) || len([]rune(message)) != 2_000 {
		t.Fatalf("generatorError() = %q, %d runes", code, len([]rune(message)))
	}
	if code, message = generatorError("generator_failed", errors.New("   ")); code != "generator_failed" || message != code {
		t.Fatalf("blank generatorError() = %q, %q", code, message)
	}
}

func TestGeneratorErrorPreservesSafeFailureCategory(t *testing.T) {
	t.Parallel()
	err := NewGeneratorFailure(ProviderUnreachableFailureCode, errors.New("start Provider: upstream unavailable"))
	code, message := generatorError("generator_start_failed", err)
	if code != ProviderUnreachableFailureCode || !strings.Contains(message, "upstream unavailable") {
		t.Fatalf("generatorError() = %q, %q", code, message)
	}
}

func TestGeneratorStatusRejectsExecutionDataInWrongState(t *testing.T) {
	t.Parallel()
	if err := (GeneratorStatus{State: GeneratorRunning, Stage: "running", Outputs: []GeneratorOutput{{SourcePath: "/unexpected"}}}).Validate(); !errors.Is(err, ErrInvalidGeneratorResult) {
		t.Fatalf("running output error = %v", err)
	}
	if err := (GeneratorStatus{State: GeneratorFailed, Stage: "failed", FailureCode: "generator_failed", FailureMessage: "明确失败"}).Validate(); err != nil {
		t.Fatalf("failed status error = %v", err)
	}
}
