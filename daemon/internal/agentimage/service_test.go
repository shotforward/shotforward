package agentimage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestGenerateRoutesByRuntimeAndImportsConversationImage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		runtime   agentapp.RuntimeKind
		generator project.GeneratorID
		model     string
	}{
		{name: "local Codex subscription", runtime: agentapp.RuntimeUserCodex, generator: project.GeneratorImageCodex},
		{name: "ShotForward cloud", runtime: agentapp.RuntimeShotForward, generator: project.GeneratorImageShotForward, model: "shotforward-image"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Clean(t.TempDir())
			output := filepath.Join(root, "generated.png")
			artifact := validReferenceArtifact()
			media := &fakeProjectMedia{context: validGenerationContext(root, artifact), imported: artifact}
			generator := &fakeGenerator{id: test.generator, output: output}
			capability := projectapp.GenerationCapability{GeneratorID: test.generator, MediaKind: project.MediaKindImage, CapabilityVersion: "test-v1",
				ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: project.GenerationReferenceImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: []string{"image/png"}}}}
			if test.model != "" {
				capability.ProviderModelID, capability.ProviderConnectionRevision = test.model, 1
			}
			service, err := NewService(Options{Projects: media, Generators: fakeRegistry{generator}, Catalog: fakeCatalog{capability}, IDs: fakeIDs{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Generate(context.Background(), Input{ProjectID: testProjectID, EpisodeID: testEpisodeID,
				SessionID: testSessionID, TurnID: testTurnID, RuntimeKind: test.runtime, Prompt: "Use the attached character as the reference.", ArtifactIDs: []project.ArtifactID{artifact.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if result.GeneratorID != test.generator || result.ReferenceCount != 1 || result.ArtifactID != artifact.ID || media.importInput.SourcePath != output || len(media.prepareInput.ArtifactIDs) != 1 || media.prepareInput.ArtifactIDs[0] != artifact.ID {
				t.Fatalf("result=%#v import=%#v", result, media.importInput)
			}
			if generator.request.Snapshot.GeneratorID != test.generator || generator.request.Snapshot.ProviderModelID != test.model || len(generator.request.References) != 1 || generator.request.References[0].Artifact.ID != artifact.ID {
				t.Fatalf("request=%#v", generator.request)
			}
		})
	}
}

func TestGeneratePreservesSafeProviderStartFailure(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(t.TempDir())
	artifact := validReferenceArtifact()
	providerErr := generation.NewProviderHTTPError(
		"ShotForward",
		400,
		nil,
		strings.NewReader(`{"error":{"code":"invalid_media","message":"uploaded reference media is invalid"}}`),
	)
	generator := &fakeGenerator{id: project.GeneratorImageShotForward, startErr: providerErr}
	capability := projectapp.GenerationCapability{
		GeneratorID: project.GeneratorImageShotForward, ProviderModelID: "shotforward-image", ProviderConnectionRevision: 1,
		MediaKind: project.MediaKindImage, CapabilityVersion: "test-v1",
		ReferenceCapabilities: []projectapp.GenerationReferenceCapability{{Role: project.GenerationReferenceImage, MaximumCount: 1, MaximumBytes: 20 << 20, AcceptedMIMETypes: []string{"image/png"}}},
	}
	service, err := NewService(Options{Projects: &fakeProjectMedia{context: validGenerationContext(root, artifact)}, Generators: fakeRegistry{generator}, Catalog: fakeCatalog{capability}, IDs: fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Generate(context.Background(), Input{ProjectID: testProjectID, EpisodeID: testEpisodeID,
		SessionID: testSessionID, TurnID: testTurnID, RuntimeKind: agentapp.RuntimeShotForward, Prompt: "Use the reference", ArtifactIDs: []project.ArtifactID{artifact.ID}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != generation.ProviderResponseFailureCode || !strings.Contains(failure.Message, "invalid_media") {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
}

var (
	testProjectID = project.ProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testEpisodeID = project.EpisodeID("ep_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testSessionID = agent.SessionID("ags_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	testTurnID    = agent.TurnID("agt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
)

type fakeProjectMedia struct {
	context      projectapp.AgentImageGenerationContext
	imported     project.Artifact
	importInput  projectapp.ImportAgentGeneratedMediaInput
	prepareInput projectapp.PrepareAgentImageGenerationInput
}

func (f *fakeProjectMedia) PrepareAgentImageGeneration(_ context.Context, input projectapp.PrepareAgentImageGenerationInput) (projectapp.AgentImageGenerationContext, error) {
	f.prepareInput = input
	return f.context, nil
}
func (f *fakeProjectMedia) ImportAgentGeneratedMedia(_ context.Context, input projectapp.ImportAgentGeneratedMediaInput) (project.AgentConversationMedia, error) {
	f.importInput = input
	return project.AgentConversationMedia{Artifact: f.imported}, nil
}

type fakeIDs struct{}

func (fakeIDs) NewGenerationJobID() (project.GenerationJobID, error) {
	return project.GenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), nil
}

type fakeCatalog struct {
	capability projectapp.GenerationCapability
}

func (f fakeCatalog) Generator(context.Context, project.GeneratorID) (projectapp.GenerationCapability, error) {
	return f.capability, nil
}

type fakeRegistry struct{ generator generation.Generator }

func (f fakeRegistry) Generator(id project.GeneratorID) (generation.Generator, bool) {
	return f.generator, f.generator.ID() == id
}

type fakeGenerator struct {
	id       project.GeneratorID
	output   string
	request  generation.GeneratorRequest
	startErr error
}

func (f *fakeGenerator) ID() project.GeneratorID { return f.id }
func (f *fakeGenerator) ConcurrencyLimit() int   { return 1 }
func (f *fakeGenerator) SupportsRecovery(project.GenerationInputSnapshot) bool {
	return false
}
func (f *fakeGenerator) PollInterval() time.Duration { return time.Millisecond }
func (f *fakeGenerator) Start(_ context.Context, request generation.GeneratorRequest) (generation.GeneratorOperation, error) {
	if err := request.Validate(); err != nil {
		return generation.GeneratorOperation{}, err
	}
	f.request = request
	if f.startErr != nil {
		return generation.GeneratorOperation{}, f.startErr
	}
	return generation.GeneratorOperation{ID: "operation"}, nil
}
func (f *fakeGenerator) Poll(context.Context, generation.GeneratorOperation) (generation.GeneratorStatus, error) {
	return generation.GeneratorStatus{State: generation.GeneratorSucceeded, Stage: "complete", Message: "done", Outputs: []generation.GeneratorOutput{{SourcePath: f.output}}}, nil
}
func (f *fakeGenerator) Stop(context.Context, generation.GeneratorOperation) error { return nil }

func validReferenceArtifact() project.Artifact {
	return project.Artifact{ID: project.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), ProjectID: testProjectID, Kind: project.MediaKindImage,
		RelativePath: "media/images/reference.png", SHA256: strings.Repeat("a", 64), ByteSize: 1024, MIMEType: "image/png",
		OriginalFilename: "reference.png", Width: 256, Height: 256, CreatedAt: time.Date(2026, 9, 21, 1, 2, 3, 0, time.UTC)}
}

func validGenerationContext(root string, artifact project.Artifact) projectapp.AgentImageGenerationContext {
	output, _ := project.NewOutputSpec(720, 1280)
	return projectapp.AgentImageGenerationContext{ProjectRoot: root, Project: project.Project{ID: testProjectID, Output: output},
		References: []projectapp.AgentInputMedia{{Media: project.AgentConversationMedia{Artifact: artifact}, AbsolutePath: filepath.Join(root, "reference.png")}}}
}
