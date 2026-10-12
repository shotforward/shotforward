package host

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	"github.com/shotforward/shotforward/daemon/internal/prompttranslation"
)

type translationSelection struct{ kind agentapp.RuntimeKind }

func (s *translationSelection) ActiveRuntime(context.Context) (agentapp.RuntimeKind, error) {
	return s.kind, nil
}
func (s *translationSelection) SelectRuntime(_ context.Context, kind agentapp.RuntimeKind) error {
	s.kind = kind
	return nil
}

type translationModels struct {
	values []agentapp.Model
	err    error
}

func (m translationModels) ModelsFor(context.Context, agentapp.RuntimeKind) ([]agentapp.Model, error) {
	return m.values, m.err
}

type translationWorker struct {
	calls         int
	model, prompt string
	err           error
	after         func()
}

func (w *translationWorker) Translate(_ context.Context, model, prompt string) (string, error) {
	w.calls++
	w.model, w.prompt = model, prompt
	if w.after != nil {
		w.after()
	}
	return "Translated prompt", w.err
}

func TestPromptTranslationRoutesOnlyToSelectedServiceAndModel(t *testing.T) {
	for _, kind := range []agentapp.RuntimeKind{agentapp.RuntimeUserCodex, agentapp.RuntimeShotForward} {
		t.Run(string(kind), func(t *testing.T) {
			selected, other := &translationWorker{}, &translationWorker{}
			selection := &translationSelection{kind: kind}
			translator := &generationPromptTranslator{selection: selection, models: translationModels{values: []agentapp.Model{{ID: "selected-model", RuntimeKind: kind}}}, workers: map[agentapp.RuntimeKind]textPromptTranslator{agentapp.RuntimeUserCodex: other, agentapp.RuntimeShotForward: other}}
			translator.workers[kind] = selected
			result, err := translator.Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{RuntimeKind: string(kind), ModelID: "selected-model"}, "原文")
			if err != nil || result != "Translated prompt" || selected.calls != 1 || selected.model != "selected-model" || selected.prompt != "原文" || other.calls != 0 {
				t.Fatalf("routing failed: %q, %v, calls=%d/%d", result, err, selected.calls, other.calls)
			}
		})
	}
}

func TestPromptTranslationRejectsChangedUnavailableAndHiddenSelection(t *testing.T) {
	for _, test := range []struct {
		name   string
		active agentapp.RuntimeKind
		models []agentapp.Model
		want   error
	}{
		{"changed-service", agentapp.RuntimeShotForward, []agentapp.Model{{ID: "m"}}, projectapp.ErrPromptTranslationSelectionChanged},
		{"missing-model", agentapp.RuntimeUserCodex, nil, projectapp.ErrPromptTranslationSelectionChanged},
		{"hidden", agentapp.RuntimeUserCodex, []agentapp.Model{{ID: "m", Hidden: true}}, projectapp.ErrPromptTranslationSelectionChanged},
		{"foreign-model", agentapp.RuntimeUserCodex, []agentapp.Model{{ID: "m", RuntimeKind: agentapp.RuntimeShotForward}}, projectapp.ErrPromptTranslationSelectionChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := &translationWorker{}
			translator := &generationPromptTranslator{selection: &translationSelection{kind: test.active}, models: translationModels{values: test.models}, workers: map[agentapp.RuntimeKind]textPromptTranslator{agentapp.RuntimeUserCodex: worker}}
			if _, err := translator.Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{RuntimeKind: "user_codex", ModelID: "m"}, "原文"); !errors.Is(err, test.want) || worker.calls != 0 {
				t.Fatalf("selection error=%v, calls=%d", err, worker.calls)
			}
		})
	}
	if _, err := (&generationPromptTranslator{}).Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{}, "原文"); !errors.Is(err, projectapp.ErrPromptTranslationUnavailable) {
		t.Fatalf("uninitialized=%v", err)
	}
}

func TestPromptTranslationSafeErrorsAndServiceChangeDuringCompletion(t *testing.T) {
	for _, test := range []struct {
		name      string
		workerErr error
		want      error
	}{
		{"preservation", &prompttranslation.ValidationError{Reason: "invalid output"}, projectapp.ErrPromptTranslationInvalid},
		{"worker", &prompttranslation.WorkerError{Err: errors.New("upstream private content")}, projectapp.ErrPromptTranslationFailed},
		{"timeout", &prompttranslation.WorkerError{Err: context.DeadlineExceeded}, projectapp.ErrPromptTranslationTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			translator := &generationPromptTranslator{selection: &translationSelection{kind: agentapp.RuntimeUserCodex}, models: translationModels{values: []agentapp.Model{{ID: "m"}}}, workers: map[agentapp.RuntimeKind]textPromptTranslator{agentapp.RuntimeUserCodex: &translationWorker{err: test.workerErr}}}
			if _, err := translator.Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{RuntimeKind: "user_codex", ModelID: "m"}, "原文"); !errors.Is(err, test.want) || err.Error() != test.want.Error() {
				t.Fatalf("safe error=%v", err)
			}
		})
	}
	selection := &translationSelection{kind: agentapp.RuntimeUserCodex}
	worker := &translationWorker{after: func() { selection.kind = agentapp.RuntimeShotForward }}
	translator := &generationPromptTranslator{selection: selection, models: translationModels{values: []agentapp.Model{{ID: "m"}}}, workers: map[agentapp.RuntimeKind]textPromptTranslator{agentapp.RuntimeUserCodex: worker}}
	if _, err := translator.Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{RuntimeKind: "user_codex", ModelID: "m"}, "原文"); !errors.Is(err, projectapp.ErrPromptTranslationSelectionChanged) {
		t.Fatalf("midflight change=%v", err)
	}
}

func TestPromptTranslationPreservesRecognizedCloudBusinessFailures(t *testing.T) {
	for _, code := range []string{"shotforward_negative_balance", "shotforward_insufficient_points", "shotforward_wallet_not_found", "untrusted-upstream-code"} {
		worker := &translationWorker{err: &prompttranslation.WorkerError{Err: &codexruntime.TextWorkerFailure{Code: code}}}
		translator := &generationPromptTranslator{selection: &translationSelection{kind: agentapp.RuntimeShotForward}, models: translationModels{values: []agentapp.Model{{ID: "m"}}}, workers: map[agentapp.RuntimeKind]textPromptTranslator{agentapp.RuntimeShotForward: worker}}
		_, err := translator.Translate(context.Background(), projectapp.GenerationPromptTranslationOptions{RuntimeKind: "shotforward", ModelID: "m"}, "原文")
		var failure *projectapp.GenerationPromptTranslationFailure
		if code == "untrusted-upstream-code" {
			if errors.As(err, &failure) || err != projectapp.ErrPromptTranslationFailed {
				t.Fatalf("unrecognized business code escaped: %v", err)
			}
		} else if !errors.As(err, &failure) || failure.Code != code || !errors.Is(err, projectapp.ErrPromptTranslationFailed) {
			t.Fatalf("business error lost: %v", err)
		}
	}
}
