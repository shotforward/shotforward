// Package agentimage implements conversation-scoped image generation for the
// ShotForward MCP server. It deliberately does not write generated media into
// Assets or video units.
package agentimage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

var (
	ErrInvalidRequest = errors.New("invalid Agent image generation request")
	ErrUnavailable    = errors.New("Agent image generation is unavailable")
)

type ProjectMedia interface {
	PrepareAgentImageGeneration(context.Context, projectapp.PrepareAgentImageGenerationInput) (projectapp.AgentImageGenerationContext, error)
	ImportAgentGeneratedMedia(context.Context, projectapp.ImportAgentGeneratedMediaInput) (project.AgentConversationMedia, error)
}

type IDGenerator interface {
	NewGenerationJobID() (project.GenerationJobID, error)
}

type Options struct {
	Projects   ProjectMedia
	Generators generation.GeneratorRegistry
	Catalog    projectapp.GenerationCapabilityCatalog
	IDs        IDGenerator
}

type Service struct {
	projects   ProjectMedia
	generators generation.GeneratorRegistry
	catalog    projectapp.GenerationCapabilityCatalog
	ids        IDGenerator
}

type Input struct {
	ProjectID   project.ProjectID
	EpisodeID   project.EpisodeID
	SessionID   agent.SessionID
	TurnID      agent.TurnID
	RuntimeKind agentapp.RuntimeKind
	Prompt      string
	ArtifactIDs []project.ArtifactID
}

type Result struct {
	ArtifactID     project.ArtifactID
	GeneratorID    project.GeneratorID
	ReferenceCount int
}

type Failure struct {
	Code    string
	Message string
}

func (f *Failure) Error() string { return f.Message }

func NewService(options Options) (*Service, error) {
	if options.Projects == nil || options.Generators == nil || options.Catalog == nil || options.IDs == nil {
		return nil, ErrUnavailable
	}
	return &Service{projects: options.Projects, generators: options.Generators, catalog: options.Catalog, ids: options.IDs}, nil
}

func (s *Service) Generate(ctx context.Context, input Input) (Result, error) {
	if ctx == nil || input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil ||
		input.SessionID.Validate() != nil || input.TurnID.Validate() != nil ||
		!utf8.ValidString(input.Prompt) || strings.TrimSpace(input.Prompt) == "" ||
		utf8.RuneCountInString(input.Prompt) > project.MaxGenerationPromptCharacters || strings.ContainsRune(input.Prompt, 0) {
		return Result{}, ErrInvalidRequest
	}
	generatorID := project.GeneratorImageCodex
	switch input.RuntimeKind {
	case "", agentapp.RuntimeUserCodex:
	case agentapp.RuntimeShotForward:
		generatorID = project.GeneratorImageShotForward
	default:
		return Result{}, ErrInvalidRequest
	}
	generator, ok := s.generators.Generator(generatorID)
	if !ok {
		return Result{}, ErrUnavailable
	}
	capability, err := s.catalog.Generator(ctx, generatorID)
	if err != nil || capability.GeneratorID != generatorID || capability.MediaKind != project.MediaKindImage {
		return Result{}, ErrUnavailable
	}
	prepared, err := s.projects.PrepareAgentImageGeneration(ctx, projectapp.PrepareAgentImageGenerationInput{
		ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, SessionID: input.SessionID.String(), TurnID: input.TurnID.String(), ArtifactIDs: input.ArtifactIDs,
	})
	if err != nil {
		return Result{}, err
	}
	limit, referencesSupported := imageReferenceCapability(capability)
	if len(prepared.References) > 0 && (!referencesSupported || len(prepared.References) > limit.MaximumCount) {
		return Result{}, &Failure{Code: "reference_limit_exceeded", Message: fmt.Sprintf("This image generator accepts at most %d reference image(s)", limit.MaximumCount)}
	}
	referenceInputs := make([]project.GenerationArtifactReference, 0, len(prepared.References))
	references := make([]generation.GeneratorArtifactReference, 0, len(prepared.References))
	for _, item := range prepared.References {
		artifact := item.Media.Artifact
		if limit.MaximumBytes > 0 && artifact.ByteSize > limit.MaximumBytes {
			return Result{}, &Failure{Code: "reference_too_large", Message: "A reference image exceeds this generator's size limit"}
		}
		if len(limit.AcceptedMIMETypes) > 0 && !slices.Contains(limit.AcceptedMIMETypes, artifact.MIMEType) {
			return Result{}, &Failure{Code: "reference_format_unsupported", Message: "A reference image format is not supported by this generator"}
		}
		reference := project.GenerationArtifactReference{Role: project.GenerationReferenceImage, ArtifactID: artifact.ID}
		referenceInputs = append(referenceInputs, reference)
		references = append(references, generation.GeneratorArtifactReference{Input: reference, Artifact: artifact, AbsolutePath: item.AbsolutePath})
	}
	jobID, err := s.ids.NewGenerationJobID()
	if err != nil {
		return Result{}, ErrUnavailable
	}
	snapshot := project.GenerationInputSnapshot{
		SchemaVersion: project.GenerationSnapshotVersion, TargetMediaKind: project.MediaKindImage,
		Prompt: input.Prompt, GeneratorID: generatorID, ProviderModelID: capability.ProviderModelID,
		ProviderConnectionRevision: capability.ProviderConnectionRevision, CapabilityVersion: capability.CapabilityVersion,
		OutputWidth: prepared.Project.Output.Width(), OutputHeight: prepared.Project.Output.Height(),
		References: referenceInputs, ParametersJSON: "{}", TargetSlotRevision: 1,
	}
	request := generation.GeneratorRequest{JobID: jobID, Snapshot: snapshot, ProjectRoot: prepared.ProjectRoot, References: references}
	operation, err := generator.Start(ctx, request)
	if err != nil {
		code, message := generation.PublicGeneratorError("generation_start_failed", err)
		return Result{}, &Failure{Code: code, Message: message}
	}
	interval := 500 * time.Millisecond
	if configured, ok := generator.(generation.PollIntervalGenerator); ok && configured.PollInterval() > 0 {
		interval = configured.PollInterval()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = generator.Stop(context.WithoutCancel(ctx), operation)
			return Result{}, ctx.Err()
		case <-ticker.C:
			status, pollErr := generator.Poll(ctx, operation)
			if pollErr != nil {
				if generation.IsRetryablePollError(pollErr) {
					continue
				}
				code, message := generation.PublicGeneratorError("generation_poll_failed", pollErr)
				return Result{}, &Failure{Code: code, Message: message}
			}
			if validateErr := status.Validate(); validateErr != nil {
				return Result{}, &Failure{Code: "invalid_generation_result", Message: "Image generation returned an invalid result"}
			}
			switch status.State {
			case generation.GeneratorRunning:
				continue
			case generation.GeneratorFailed:
				return Result{}, &Failure{Code: status.FailureCode, Message: status.FailureMessage}
			case generation.GeneratorStopped:
				return Result{}, &Failure{Code: "generation_stopped", Message: "Image generation was stopped"}
			case generation.GeneratorSucceeded:
				media, importErr := s.projects.ImportAgentGeneratedMedia(ctx, projectapp.ImportAgentGeneratedMediaInput{
					ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, SessionID: input.SessionID.String(), TurnID: input.TurnID.String(),
					SourcePath: status.Outputs[0].SourcePath, OriginalFilename: filepath.Base(status.Outputs[0].SourcePath),
				})
				if importErr != nil {
					return Result{}, &Failure{Code: "generated_media_import_failed", Message: "The generated image could not be added to the conversation"}
				}
				return Result{ArtifactID: media.Artifact.ID, GeneratorID: generatorID, ReferenceCount: len(references)}, nil
			}
		}
	}
}

func imageReferenceCapability(capability projectapp.GenerationCapability) (projectapp.GenerationReferenceCapability, bool) {
	for _, value := range capability.ReferenceCapabilities {
		if value.Role == project.GenerationReferenceImage {
			return value, true
		}
	}
	return projectapp.GenerationReferenceCapability{}, false
}
