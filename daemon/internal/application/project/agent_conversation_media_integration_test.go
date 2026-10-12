package projectapp_test

import (
	"context"
	"path/filepath"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
)

func TestAgentConversationMediaPersistsAcrossUploadGenerationReuseAndReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "agent-media-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	processor := &recordingMediaProcessor{}
	service := newRealService(t, library, registry, projectapp.NewSecureIDGenerator(), fixedClock{at: testNow()}, processor)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "Agent media", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", root, err)
	}
	created = addFirstEpisode(t, service, created)
	episodeID := created.Episodes[0].ID
	const sessionID = "ags_AgentMediaSession"

	uploaded, err := service.UploadAgentConversationMedia(ctx, projectapp.UploadAgentConversationMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID,
		ClientOperationID: "upload-operation", SourcePath: "/device/reference.png", OriginalFilename: "构图参考.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedUpload, err := service.PrepareAgentInputMedia(ctx, projectapp.PrepareAgentInputMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, ClientOperationID: "turn-upload",
		References: []projectapp.AgentConversationMediaReference{{ArtifactID: uploaded.Artifact.ID, Name: uploaded.Name, Origin: domain.AgentConversationMediaUploaded}},
	})
	if err != nil || len(preparedUpload) != 1 || preparedUpload[0].Media.Artifact.ID != uploaded.Artifact.ID || !filepath.IsAbs(preparedUpload[0].AbsolutePath) {
		t.Fatalf("prepared upload=%#v err=%v", preparedUpload, err)
	}
	if err := service.BindAgentInputMedia(ctx, projectapp.BindAgentInputMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, ClientOperationID: "turn-upload",
		TurnID: "agt_UploadTurn", ArtifactIDs: []domain.ArtifactID{uploaded.Artifact.ID},
	}); err != nil {
		t.Fatal(err)
	}

	processor.sha256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	generated, err := service.ImportAgentGeneratedMedia(ctx, projectapp.ImportAgentGeneratedMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID,
		TurnID: "agt_GeneratedTurn", SourcePath: "/runtime/generated.png", OriginalFilename: "generated.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	generationContext, err := service.PrepareAgentImageGeneration(ctx, projectapp.PrepareAgentImageGenerationInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, TurnID: "agt_UploadTurn",
	})
	if err != nil || generationContext.ProjectRoot != canonicalRoot || generationContext.Project.ID != created.Project.ID || len(generationContext.References) != 1 || generationContext.References[0].Media.Artifact.ID != uploaded.Artifact.ID {
		t.Fatalf("generation context=%#v err=%v", generationContext, err)
	}
	generatedContext, err := service.PrepareAgentImageGeneration(ctx, projectapp.PrepareAgentImageGenerationInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, TurnID: "agt_GeneratedTurn",
	})
	if err != nil || len(generatedContext.References) != 0 {
		t.Fatalf("generated media unexpectedly reused as a Turn reference: %#v err=%v", generatedContext, err)
	}
	preparedGenerated, err := service.PrepareAgentInputMedia(ctx, projectapp.PrepareAgentInputMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, ClientOperationID: "turn-edit",
		References: []projectapp.AgentConversationMediaReference{{ArtifactID: generated.Artifact.ID, Name: generated.Name, Origin: domain.AgentConversationMediaGenerated}},
	})
	if err != nil || len(preparedGenerated) != 1 || preparedGenerated[0].Media.Origin != domain.AgentConversationMediaGenerated {
		t.Fatalf("prepared generated=%#v err=%v", preparedGenerated, err)
	}

	asset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, Kind: domain.AssetKindProp, Name: "Agent result",
	})
	if err != nil {
		t.Fatal(err)
	}
	assetSlotID, err := domain.FixedMediaSlotID(asset.Asset.Asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := service.AssignArtifact(ctx, projectapp.AssignArtifactInput{
		ProjectID: created.Project.ID, MediaSlotID: assetSlotID, ArtifactID: generated.Artifact.ID,
		ExpectedSlotRevision: 1, MakeCurrent: true,
	})
	if err != nil || assigned.Current == nil || assigned.Current.ArtifactID != generated.Artifact.ID {
		t.Fatalf("assigned=%#v err=%v", assigned, err)
	}
	preparedAsset, err := service.PrepareAgentInputMedia(ctx, projectapp.PrepareAgentInputMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID, ClientOperationID: "turn-asset",
		References: []projectapp.AgentConversationMediaReference{{ArtifactID: generated.Artifact.ID, Name: "Agent result", Origin: domain.AgentConversationMediaAsset}},
	})
	if err != nil || len(preparedAsset) != 1 || preparedAsset[0].Media.Origin != domain.AgentConversationMediaAsset {
		t.Fatalf("prepared Asset=%#v err=%v", preparedAsset, err)
	}

	beforeReopen, err := service.ListAgentConversationMedia(ctx, projectapp.ListAgentConversationMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID,
	})
	if err != nil || len(beforeReopen) != 5 {
		t.Fatalf("conversation media before reopen=%#v err=%v", beforeReopen, err)
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}

	reopenedRegistry := projectapp.NewProjectStoreRegistry()
	defer reopenedRegistry.CloseAll()
	reopened := newRealService(t, library, reopenedRegistry, projectapp.NewSecureIDGenerator(), fixedClock{at: testNow()}, processor)
	if _, err := reopened.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root}); err != nil {
		t.Fatal(err)
	}
	afterReopen, err := reopened.ListAgentConversationMedia(ctx, projectapp.ListAgentConversationMediaInput{
		ProjectID: created.Project.ID, EpisodeID: episodeID, SessionID: sessionID,
	})
	if err != nil || len(afterReopen) != len(beforeReopen) {
		t.Fatalf("conversation media after reopen=%#v err=%v", afterReopen, err)
	}
}
