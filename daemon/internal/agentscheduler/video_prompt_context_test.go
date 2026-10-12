package agentscheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shotforward/shotforward/daemon/internal/agentmcp"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestSchedulerFreezesVideoPromptTargetAndInjectsModelRules(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	input := fixture.input(0, "video-prompt-context", "Split into three four-second units")
	input.Focus.ClipID = project.ClipID("clp_" + strings.Repeat("F", 26))
	input.VideoPromptTarget = &agentmcp.VideoPromptContext{
		GeneratorID: string(project.GeneratorVideoH3China), ProviderModelID: "h3-ref2va",
		KeyframeProviderModelID: "h3-fl2va",
		Focus: &agentmcp.VideoPromptFocusTarget{
			ClipID: input.Focus.ClipID, GenerationMode: project.ClipGenerationReference,
			GeneratorID: string(project.GeneratorVideoH3China), ProviderModelID: "h3-ref2va",
		},
	}
	admitted, err := fixture.scheduler.SubmitInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	// A UI selection changing after admission must not mutate this queued Turn.
	input.VideoPromptTarget.ProviderModelID = "changed-after-submit"
	input.VideoPromptTarget.Focus.ProviderModelID = "changed-focus-after-submit"
	done := make(chan error, 1)
	go func() {
		_, processErr := fixture.scheduler.ProcessNext(context.Background())
		done <- processErr
	}()
	started := fixture.runtime.waitStarted(t)
	for _, expected := range []string{"h3_ref2va@1", "subject_definitions", "retention_analysis", "detailed_description", "Split into three four-second units"} {
		if !strings.Contains(started.request.Text, expected) {
			t.Errorf("Runtime input is missing %q", expected)
		}
	}
	if strings.Contains(started.request.Text, "changed-after-submit") || strings.Contains(started.request.Text, "changed-focus-after-submit") {
		t.Error("Runtime target was changed after admission")
	}
	fixture.runtime.complete(started.threadID, started.turnID, agent.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	active := fixture.mcp.lastActivatedTurn()
	if active.AgentTurnID != admitted.Turn.ID || active.VideoPromptTarget == nil || active.VideoPromptTarget.ProviderModelID != "h3-ref2va" || active.VideoPromptTarget.Focus.ProviderModelID != "h3-ref2va" {
		t.Fatalf("MCP video Prompt target = %#v", active.VideoPromptTarget)
	}
}

func TestVideoPromptTargetParticipatesInInputIdempotency(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	input := fixture.input(0, "same-video-operation", "Rewrite video prompt")
	input.VideoPromptTarget = &agentmcp.VideoPromptContext{GeneratorID: string(project.GeneratorVideoH3China), ProviderModelID: "h3-ref2va"}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if replay, err := fixture.scheduler.SubmitInput(context.Background(), input); err != nil || !replay.Replay {
		t.Fatalf("same context replay = %#v, %v", replay, err)
	}
	changed := input
	changed.VideoPromptTarget = &agentmcp.VideoPromptContext{GeneratorID: string(project.GeneratorVideoWanOfficial), ProviderModelID: "wan3.0-video"}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), changed); !errors.Is(err, ErrInputIdempotencyConflict) {
		t.Fatalf("changed context error = %v", err)
	}
}

func TestSchedulerRejectsVideoPromptFocusMismatch(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t, 1)
	input := fixture.input(0, "mismatched-video-focus", "Rewrite video prompt")
	input.VideoPromptTarget = &agentmcp.VideoPromptContext{
		GeneratorID: string(project.GeneratorVideoH3China), ProviderModelID: "h3-ref2va",
		Focus: &agentmcp.VideoPromptFocusTarget{ClipID: project.ClipID("clp_" + strings.Repeat("F", 26)), GenerationMode: project.ClipGenerationReference, GeneratorID: string(project.GeneratorVideoH3China), ProviderModelID: "h3-ref2va"},
	}
	if _, err := fixture.scheduler.SubmitInput(context.Background(), input); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("focus mismatch error = %v", err)
	}
}
