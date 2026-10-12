package projectapp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	projectstore "github.com/shotforward/shotforward/daemon/internal/persistence/project"
	sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"
	studio "github.com/shotforward/shotforward/daemon/internal/persistence/studio"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/scriptimport"
)

func TestEmptyScriptVideoAllowsManualAssetsScenesAndClips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "empty-script-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	service := newRealService(t, library, registry, testIDs(t), fixedClock{at: testNow()})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "无剧本创作", OutputWidth: 1920, OutputHeight: 1080,
	})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	projectID := created.Project.ID
	episodeID := created.Episodes[0].ID

	episode, err := service.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || episode.Script.Markdown != "" {
		t.Fatalf("initial episode = %#v, %v", episode, err)
	}
	asset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{
		ProjectID: projectID, EpisodeID: episodeID, Kind: domain.AssetKindProp, Name: "棱镜",
	})
	if err != nil || asset.Asset.Identity.Name != "棱镜" {
		t.Fatalf("CreateAsset() = %#v, %v", asset, err)
	}
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{
		ProjectID: projectID, EpisodeID: episodeID, Title: "光影实验", ExpectedStoryboardRevision: 1,
	})
	if err != nil {
		t.Fatalf("CreateScene() error = %v", err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{
		ProjectID: projectID, EpisodeID: episodeID, SceneID: scene.ID,
		ClipFields: projectapp.ClipFields{
			Title: "折射变化", PlannedDurationSeconds: 7, GenerationMode: domain.ClipGenerationFirstImage,
		},
		ExpectedStoryboardRevision: 2,
	})
	if err != nil || clip.Clip.Title != "折射变化" {
		t.Fatalf("CreateClip() = %#v, %v", clip, err)
	}
	episode, err = service.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || episode.Script.Markdown != "" {
		t.Fatalf("script after manual creation = %#v, %v", episode.Script, err)
	}
}

func TestImportFirstImageUsesProjectOutputDimensions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "media-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	processor := &recordingMediaProcessor{}
	ids := testIDs(t)
	service := newRealService(t, library, registry, ids, fixedClock{at: testNow()}, processor)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: root, Name: "媒体", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: "镜头", PlannedDurationSeconds: 5, GenerationMode: domain.ClipGenerationFirstImage}, ExpectedStoryboardRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	slotID, err := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipFirstImage)
	if err != nil {
		t.Fatal(err)
	}
	details, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: created.Project.ID, MediaSlotID: slotID, SourcePath: "/external/source.png", ExpectedSlotRevision: 1, MakeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	if processor.request.RequiredWidth != 720 || processor.request.RequiredHeight != 1280 {
		t.Fatalf("Prepare dimensions = %dx%d", processor.request.RequiredWidth, processor.request.RequiredHeight)
	}
	if details.Current == nil || details.Slot.Revision != 2 || details.Artifacts[details.Current.ArtifactID].Width != 720 {
		t.Fatalf("imported MediaSlot = %#v", details)
	}
}

func TestExtractVideoFrameUsesAdjacentTrimBoundaryAndRefreshesExistingFrame(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "continuity-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	processor := &recordingMediaProcessor{}
	service := newRealService(t, library, registry, &continuityIDs{fixedIDs: testIDs(t)}, fixedClock{at: testNow()}, processor)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: root, Name: "衔接", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	episodeID := created.Episodes[0].ID
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: episodeID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: episodeID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: "前镜头", PlannedDurationSeconds: 5, GenerationMode: domain.ClipGenerationFirstImage}, ExpectedStoryboardRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := registry.ByProjectID(created.Project.ID)
	secondID, _ := domain.ParseClipID("clp_BBCDEFGHIJKLMNOPQRSTUVWXYZ")
	second := first.Clip
	second.ID = secondID
	second.Position++
	second.Title = "后镜头"
	if err := handle.Store.CreateClip(ctx, second, 3, testNow()); err != nil {
		t.Fatal(err)
	}
	sourceSlotID, _ := domain.FixedMediaSlotID(first.Clip.ID.String(), domain.MediaSlotClipVideo)
	sourceArtifactID, _ := domain.ParseArtifactID("art_BBCDEFGHIJKLMNOPQRSTUVWXYZ")
	sourceVersionID, _ := domain.ParseMediaVersionID("mvr_BBCDEFGHIJKLMNOPQRSTUVWXYZ")
	sourceArtifact := domain.Artifact{ID: sourceArtifactID, ProjectID: created.Project.ID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/source.mp4", SHA256: strings.Repeat("c", 64), ByteSize: 100, MIMEType: "video/mp4", OriginalFilename: "source.mp4", Width: 720, Height: 1280, DurationMilliseconds: 2000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: testNow()}
	sourceVersion := domain.MediaVersion{ID: sourceVersionID, SlotID: sourceSlotID, ArtifactID: sourceArtifactID, SequenceNumber: 1, TrimInMilliseconds: 100, TrimOutMilliseconds: 1800, Revision: 1, CreatedAt: testNow(), UpdatedAt: testNow()}
	if err := handle.Store.AddMediaVersion(ctx, sourceArtifact, sourceVersion, 1, true, testNow()); err != nil {
		t.Fatal(err)
	}
	targetSlotID, _ := domain.FixedMediaSlotID(second.ID.String(), domain.MediaSlotClipFirstImage)
	result, err := service.ExtractVideoFrame(ctx, projectapp.ExtractVideoFrameInput{ProjectID: created.Project.ID, TargetMediaSlotID: targetSlotID, SourceVideoMediaSlotID: sourceSlotID, ExpectedTargetRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if processor.frameRequest.AtMilliseconds != 1766 || processor.frameRequest.SourceRelativePath != sourceArtifact.RelativePath || processor.frameRequest.RequiredWidth != 720 || processor.frameRequest.RequiredHeight != 1280 {
		t.Fatalf("frame request = %#v", processor.frameRequest)
	}
	if result.Current == nil || result.Slot.Revision != 2 || result.Artifacts[result.Current.ArtifactID].OriginalFilename != "previous-video-last-frame.png" {
		t.Fatalf("target MediaSlot = %#v", result)
	}
	refreshed, err := service.ExtractVideoFrame(ctx, projectapp.ExtractVideoFrameInput{ProjectID: created.Project.ID, TargetMediaSlotID: targetSlotID, SourceVideoMediaSlotID: sourceSlotID, ExpectedTargetRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Slot.Revision != 2 || refreshed.Current == nil || refreshed.Current.ArtifactID != result.Current.ArtifactID || len(refreshed.Versions) != 1 {
		t.Fatalf("unchanged refresh = %#v", refreshed)
	}
	processor.sha256 = strings.Repeat("e", 64)
	updated, err := service.ExtractVideoFrame(ctx, projectapp.ExtractVideoFrameInput{ProjectID: created.Project.ID, TargetMediaSlotID: targetSlotID, SourceVideoMediaSlotID: sourceSlotID, ExpectedTargetRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slot.Revision != 3 || updated.Current == nil || updated.Current.ArtifactID == result.Current.ArtifactID || len(updated.Versions) != 2 {
		t.Fatalf("changed refresh = %#v", updated)
	}
	secondVideoSlotID, _ := domain.FixedMediaSlotID(second.ID.String(), domain.MediaSlotClipVideo)
	nextArtifactID, _ := domain.ParseArtifactID("art_CBCDEFGHIJKLMNOPQRSTUVWXYZ")
	nextVersionID, _ := domain.ParseMediaVersionID("mvr_CBCDEFGHIJKLMNOPQRSTUVWXYZ")
	nextArtifact := sourceArtifact
	nextArtifact.ID = nextArtifactID
	nextArtifact.RelativePath = "media/videos/next.mp4"
	nextArtifact.SHA256 = strings.Repeat("d", 64)
	nextArtifact.OriginalFilename = "next.mp4"
	nextVersion := sourceVersion
	nextVersion.ID = nextVersionID
	nextVersion.SlotID = secondVideoSlotID
	nextVersion.ArtifactID = nextArtifactID
	nextVersion.TrimInMilliseconds = 250
	nextVersion.TrimOutMilliseconds = 1900
	if err := handle.Store.AddMediaVersion(ctx, nextArtifact, nextVersion, 1, true, testNow()); err != nil {
		t.Fatal(err)
	}
	lastFrameSlotID, _ := domain.FixedMediaSlotID(first.Clip.ID.String(), domain.MediaSlotClipLastImage)
	lastFrame, err := service.ExtractVideoFrame(ctx, projectapp.ExtractVideoFrameInput{ProjectID: created.Project.ID, TargetMediaSlotID: lastFrameSlotID, SourceVideoMediaSlotID: secondVideoSlotID, ExpectedTargetRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if processor.frameRequest.AtMilliseconds != 250 || processor.frameRequest.OriginalFilename != "next-video-first-frame.png" || lastFrame.Current == nil {
		t.Fatalf("next first-frame extraction = request %#v, slot %#v", processor.frameRequest, lastFrame)
	}
}

func TestExportPreviewAndSubmissionFreezeCurrentVideo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "export-project")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	service := newRealService(t, library, registry, testIDs(t), fixedClock{at: testNow()})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: root, Name: "导出", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	episodeID := created.Episodes[0].ID
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{ProjectID: created.Project.ID, EpisodeID: episodeID, Title: "场景", ExpectedStoryboardRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{ProjectID: created.Project.ID, EpisodeID: episodeID, SceneID: scene.ID, ClipFields: projectapp.ClipFields{Title: "镜头", PlannedDurationSeconds: 4, GenerationMode: domain.ClipGenerationFirstImage}, ExpectedStoryboardRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := registry.ByProjectID(created.Project.ID)
	slotID, _ := domain.FixedMediaSlotID(clip.Clip.ID.String(), domain.MediaSlotClipVideo)
	artifactID := domain.ArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	versionID := domain.MediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	artifact := domain.Artifact{ID: artifactID, ProjectID: created.Project.ID, Kind: domain.MediaKindVideo, RelativePath: "media/videos/clip.mp4", SHA256: strings.Repeat("a", 64), ByteSize: 100, MIMEType: "video/mp4", OriginalFilename: "clip.mp4", Width: 720, Height: 1280, DurationMilliseconds: 2000, FPSNumerator: 30, FPSDenominator: 1, CreatedAt: testNow()}
	version := domain.MediaVersion{ID: versionID, SlotID: slotID, ArtifactID: artifactID, SequenceNumber: 1, TrimInMilliseconds: 100, TrimOutMilliseconds: 1800, Revision: 1, CreatedAt: testNow(), UpdatedAt: testNow()}
	if err := handle.Store.AddMediaVersion(ctx, artifact, version, 1, true, testNow()); err != nil {
		t.Fatal(err)
	}
	preview, err := service.GetExportPreview(ctx, projectapp.GetExportPreviewInput{ProjectID: created.Project.ID, EpisodeID: episodeID})
	if err != nil || preview.StoryboardRevision != 3 || len(preview.Clips) != 1 || preview.Clips[0].ArtifactID != artifactID || preview.Clips[0].TrimInMilliseconds != 100 || preview.Clips[0].TrimOutMilliseconds != 1800 {
		t.Fatalf("preview = %#v, err=%v", preview, err)
	}
	input := projectapp.SubmitExportJobInput{ProjectID: created.Project.ID, EpisodeID: episodeID, OperationKey: "export-1", ExpectedStoryboardRevision: 3}
	job, err := service.SubmitExportJob(ctx, input)
	if err != nil || job.State != domain.ExportJobQueued || job.Snapshot.Clips[0].MediaVersionID != versionID {
		t.Fatalf("SubmitExportJob() = %#v, %v", job, err)
	}
	replayed, err := service.SubmitExportJob(ctx, input)
	if err != nil || replayed.ID != job.ID {
		t.Fatalf("idempotent SubmitExportJob() = %#v, %v", replayed, err)
	}
	if err := service.CloseProject(ctx, created.Project.ID); !errors.Is(err, projectapp.ErrProjectHasActiveExport) {
		t.Fatalf("CloseProject() with active ExportJob error = %v", err)
	}
	running, err := handle.Store.ClaimExportJob(ctx, job.ID, job.Revision, testNow().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := "exports/" + job.ID.String() + ".mp4"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(outputPath)), []byte("exported"), 0o600); err != nil {
		t.Fatal(err)
	}
	finished, err := handle.Store.FinishExportJob(ctx, job.ID, running.Revision, domain.ExportJobSucceeded, outputPath, "", "", testNow().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.SubmitExportJob(ctx, projectapp.SubmitExportJobInput{ProjectID: created.Project.ID, EpisodeID: episodeID, OperationKey: "export-2", ExpectedStoryboardRevision: 3})
	if err != nil || reused.ID != finished.ID || reused.State != domain.ExportJobSucceeded {
		t.Fatalf("reusable SubmitExportJob() = %#v, %v", reused, err)
	}
}

func TestCreateCloseAndOpenProjectWithRealStores(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "雨夜来信")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	service := newRealService(t, library, registry, testIDs(t), fixedClock{at: testNow()})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "雨夜来信", Description: "测试项目", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if len(created.Episodes) != 0 {
		t.Fatalf("CreateProject() Episodes = %#v", created.Episodes)
	}
	for _, relative := range []string{
		"project.db", "media/images", "media/videos", "media/audio", "imports", "exports", ".shotforward/staging",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("created path %q: %v", relative, err)
		}
	}
	entry, err := studioStore.Project(ctx, created.Project.ID.String())
	if err != nil {
		t.Fatalf("Studio Project() error = %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", root, err)
	}
	if entry.RootPath != canonicalRoot || entry.Snapshot.EpisodeCount != 0 || entry.Snapshot.Width != 720 || entry.Snapshot.Height != 1280 {
		t.Fatalf("Studio project entry = %#v", entry)
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatalf("first Registry.CloseAll() error = %v", err)
	}

	reopenedRegistry := projectapp.NewProjectStoreRegistry()
	reopenedService := newRealService(
		t,
		library,
		reopenedRegistry,
		testIDs(t),
		fixedClock{at: testNow().Add(time.Minute)},
	)
	reopened, err := reopenedService.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if reopened.Project.ID != created.Project.ID || len(reopened.Episodes) != 0 {
		t.Fatalf("reopened result = %#v, created = %#v", reopened, created)
	}
	updatedEntry, err := studioStore.Project(ctx, created.Project.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if updatedEntry.LastOpenedAt == nil || !updatedEntry.LastOpenedAt.Equal(testNow().Add(time.Minute)) {
		t.Fatalf("last opened = %v, want %v", updatedEntry.LastOpenedAt, testNow().Add(time.Minute))
	}
	if err := reopenedRegistry.CloseAll(); err != nil {
		t.Fatalf("reopened Registry.CloseAll() error = %v", err)
	}
}

func TestEpisodeAndScriptWorkflowWithRealStores(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "episode-workflow")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	projectID, firstEpisodeID, secondEpisodeID := workflowIDs(t)
	ids := &queuedIDs{projectID: projectID, episodeIDs: []domain.EpisodeID{firstEpisodeID, secondEpisodeID}}
	clock := &queuedClock{values: []time.Time{
		testNow(),
		testNow().Add(time.Second),
		testNow().Add(2 * time.Second),
		testNow().Add(3 * time.Second),
		testNow().Add(4 * time.Second),
	}}
	registry := projectapp.NewProjectStoreRegistry()
	service := newRealService(t, library, registry, ids, clock)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "Workflow", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Project.ID != projectID || len(created.Episodes) != 0 {
		t.Fatalf("created Project = %#v", created)
	}
	first, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: projectID, TargetDuration: time.Minute})
	if err != nil || first.Episode.ID != firstEpisodeID || first.Episode.SequenceNumber != 1 {
		t.Fatalf("first Episode = %#v, %v", first, err)
	}

	appended, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: projectID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Episode.ID != secondEpisodeID || appended.Episode.SequenceNumber != 2 || len(appended.Episodes) != 2 {
		t.Fatalf("appended Episode = %#v", appended)
	}
	updated, err := service.UpdateEpisode(ctx, projectapp.UpdateEpisodeInput{
		ProjectID: projectID, EpisodeID: secondEpisodeID, Title: "转折",
		TargetDuration: 90 * time.Second, ExpectedRevision: appended.Episode.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.SaveScript(ctx, projectapp.SaveScriptInput{
		ProjectID: projectID, EpisodeID: secondEpisodeID,
		Markdown: "## 场景一\r\n\r\n雨夜。", ExpectedScriptRevision: updated.Episode.Script.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Episode.Title != "转折" || saved.Episode.TargetDuration != 90*time.Second ||
		saved.Episode.Revision != 2 || saved.Episode.Script.Revision != 2 ||
		saved.Episode.Script.Markdown != "## 场景一\n\n雨夜。" {
		t.Fatalf("saved Episode = %#v", saved.Episode)
	}
	entry, err := studioStore.Project(ctx, projectID.String())
	if err != nil {
		t.Fatal(err)
	}
	if entry.Snapshot.EpisodeCount != 2 || !entry.Snapshot.ProjectUpdatedAt.Equal(testNow().Add(4*time.Second)) {
		t.Fatalf("Studio entry after workflow = %#v", entry)
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}

	reopenedRegistry := projectapp.NewProjectStoreRegistry()
	reopened := newRealService(t, library, reopenedRegistry, testIDs(t), fixedClock{at: testNow().Add(time.Minute)})
	if _, err := reopened.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root}); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetEpisode(ctx, projectapp.GetEpisodeInput{ProjectID: projectID, EpisodeID: secondEpisodeID})
	if err != nil {
		t.Fatal(err)
	}
	if got != saved.Episode {
		t.Fatalf("Episode after restart = %#v, want %#v", got, saved.Episode)
	}
	if err := reopenedRegistry.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestStoryboardAndAssetUsageWorkflowWithRealStores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "storyboard-workflow")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	clock := &queuedClock{values: []time.Time{
		testNow(), testNow().Add(time.Second), testNow().Add(2 * time.Second),
		testNow().Add(3 * time.Second), testNow().Add(4 * time.Second),
		testNow().Add(5 * time.Second), testNow().Add(6 * time.Second),
		testNow().Add(7 * time.Second), testNow().Add(8 * time.Second),
		testNow().Add(9 * time.Second), testNow().Add(10 * time.Second),
		testNow().Add(11 * time.Second), testNow().Add(12 * time.Second),
		testNow().Add(13 * time.Second),
	}}
	registry := projectapp.NewProjectStoreRegistry()
	defer registry.CloseAll()
	service := newRealService(t, library, registry, projectapp.NewSecureIDGenerator(), clock, &recordingMediaProcessor{})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{RootPath: root, Name: "Storyboard", OutputWidth: 720, OutputHeight: 1280})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	projectID := created.Project.ID
	episodeID := created.Episodes[0].ID
	markdown := "## 雨夜街道\n林晚撑伞走入画面。"
	if _, err := service.SaveScript(ctx, projectapp.SaveScriptInput{ProjectID: projectID, EpisodeID: episodeID, Markdown: markdown, ExpectedScriptRevision: 1}); err != nil {
		t.Fatal(err)
	}
	empty, err := service.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || empty.Storyboard.Revision != 1 || len(empty.Storyboard.Scenes) != 0 {
		t.Fatalf("empty Storyboard = %#v, %v", empty, err)
	}
	emptyValidation, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || emptyValidation.Valid || !hasStoryboardIssue(emptyValidation, projectapp.StoryboardIssueEmpty) || !hasStoryboardIssue(emptyValidation, projectapp.StoryboardIssueDurationOutsideTolerance) {
		t.Fatalf("empty Storyboard validation = %#v, %v", emptyValidation, err)
	}
	if _, err := service.CreateScene(ctx, projectapp.CreateSceneInput{
		ProjectID: projectID, EpisodeID: episodeID, Title: "过期来源",
		Source:                     &projectapp.SceneScriptSourceInput{ScriptRevision: 1, StartByte: 0, EndByte: len(markdown)},
		ExpectedStoryboardRevision: 1,
	}); !errors.Is(err, projectapp.ErrInvalidRequest) {
		t.Fatalf("CreateScene(stale source) error = %v", err)
	}
	scene, err := service.CreateScene(ctx, projectapp.CreateSceneInput{
		ProjectID: projectID, EpisodeID: episodeID, Title: "雨夜街道",
		Source:                     &projectapp.SceneScriptSourceInput{ScriptRevision: 2, StartByte: 0, EndByte: len(markdown)},
		ExpectedStoryboardRevision: 1,
	})
	if err != nil || scene.Source == nil || scene.Source.Excerpt != markdown {
		t.Fatalf("CreateScene() = %#v, %v", scene, err)
	}
	clip, err := service.CreateClip(ctx, projectapp.CreateClipInput{
		ProjectID: projectID, EpisodeID: episodeID, SceneID: scene.ID,
		ClipFields:                 projectapp.ClipFields{Title: "撑伞入场", PlannedDurationSeconds: 5, GenerationMode: domain.ClipGenerationFirstImage},
		ExpectedStoryboardRevision: 2,
	})
	if err != nil || clip.Clip.FirstImagePrompt != "" || clip.Clip.LastImagePrompt != "" {
		t.Fatalf("CreateClip() = %#v, %v", clip, err)
	}
	missingFrame, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || missingFrame.Valid || !hasStoryboardIssue(missingFrame, projectapp.StoryboardIssueFirstFrameMissing) {
		t.Fatalf("first-frame validation = %#v, %v", missingFrame, err)
	}
	clip, err = service.UpdateClip(ctx, projectapp.UpdateClipInput{ProjectID: projectID, ClipID: clip.Clip.ID,
		ClipFields: projectapp.ClipFields{Title: clip.Clip.Title, PlannedDurationSeconds: clip.Clip.PlannedDurationSeconds, GenerationMode: domain.ClipGenerationFirstLastImage}, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	missingFrames, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || !hasStoryboardIssue(missingFrames, projectapp.StoryboardIssueFirstFrameMissing) || !hasStoryboardIssue(missingFrames, projectapp.StoryboardIssueLastFrameMissing) {
		t.Fatalf("first/last-frame validation = %#v, %v", missingFrames, err)
	}
	asset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{ProjectID: projectID, EpisodeID: episodeID, Kind: domain.AssetKindCharacter, Name: "林晚"})
	if err != nil {
		t.Fatal(err)
	}
	clip, err = service.BindClipAsset(ctx, projectapp.BindClipAssetInput{ProjectID: projectID, ClipID: clip.Clip.ID, AssetID: asset.Asset.Asset.ID, ExpectedClipRevision: 2})
	if err != nil || clip.Clip.Revision != 3 || len(clip.AssetReferences) != 1 {
		t.Fatalf("BindClipAsset() = %#v, %v", clip, err)
	}
	clip, err = service.UpdateClip(ctx, projectapp.UpdateClipInput{ProjectID: projectID, ClipID: clip.Clip.ID,
		ClipFields: projectapp.ClipFields{Title: clip.Clip.Title, PlannedDurationSeconds: clip.Clip.PlannedDurationSeconds, GenerationMode: domain.ClipGenerationReference}, ExpectedRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	missingReference, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || missingReference.Valid || !hasStoryboardIssue(missingReference, projectapp.StoryboardIssueAssetImageMissing) || !hasStoryboardIssue(missingReference, projectapp.StoryboardIssueReferenceInputMissing) {
		t.Fatalf("reference validation = %#v, %v", missingReference, err)
	}
	assetImageSlot, err := domain.FixedMediaSlotID(asset.Asset.Asset.ID.String(), domain.MediaSlotAssetPrimaryImage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportMedia(ctx, projectapp.ImportMediaInput{ProjectID: projectID, MediaSlotID: assetImageSlot, SourcePath: "/external/reference.png", ExpectedSlotRevision: 1, MakeCurrent: true}); err != nil {
		t.Fatal(err)
	}
	readyReference, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || !readyReference.Valid || hasStoryboardIssue(readyReference, projectapp.StoryboardIssueAssetImageMissing) || hasStoryboardIssue(readyReference, projectapp.StoryboardIssueReferenceInputMissing) {
		t.Fatalf("ready reference validation = %#v, %v", readyReference, err)
	}
	usage, err := service.GetAssetUsage(ctx, projectapp.GetAssetInput{ProjectID: projectID, AssetID: asset.Asset.Asset.ID})
	if err != nil || len(usage.Clips) != 1 || usage.Clips[0].ID != clip.Clip.ID {
		t.Fatalf("GetAssetUsage() = %#v, %v", usage, err)
	}
	if _, err := service.DeleteAsset(ctx, projectapp.DeleteAssetInput{ProjectID: projectID, AssetID: asset.Asset.Asset.ID, ExpectedRevision: 1}); !errors.Is(err, projectapp.ErrAssetInUse) {
		t.Fatalf("DeleteAsset(unconfirmed) error = %v", err)
	}
	deleted, err := service.DeleteAsset(ctx, projectapp.DeleteAssetInput{ProjectID: projectID, AssetID: asset.Asset.Asset.ID, ExpectedRevision: 1, ConfirmReferenced: true})
	if err != nil || !deleted.Asset.Asset.IsDeleted() {
		t.Fatalf("DeleteAsset(confirmed) = %#v, %v", deleted, err)
	}
	deletedReference, err := service.GetStoryboardValidation(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || !hasStoryboardIssue(deletedReference, projectapp.StoryboardIssueDeletedAssetReference) {
		t.Fatalf("deleted Asset reference validation = %#v, %v", deletedReference, err)
	}
	storyboard, err := service.GetStoryboard(ctx, projectapp.GetStoryboardInput{ProjectID: projectID, EpisodeID: episodeID})
	if err != nil || storyboard.Storyboard.PlannedDurationSeconds != 5 || !storyboard.DurationWarning {
		t.Fatalf("GetStoryboard() = %#v, %v", storyboard, err)
	}
}

func hasStoryboardIssue(result projectapp.StoryboardValidationResult, code projectapp.StoryboardValidationCode) bool {
	for _, issue := range result.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestAssetWorkflowWithRealStores(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "asset-workflow")
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	clock := &queuedClock{values: []time.Time{
		testNow(), testNow().Add(time.Second), testNow().Add(2 * time.Second),
		testNow().Add(3 * time.Second), testNow().Add(4 * time.Second),
		testNow().Add(5 * time.Second), testNow().Add(6 * time.Second),
		testNow().Add(7 * time.Second), testNow().Add(8 * time.Second),
		testNow().Add(9 * time.Second), testNow().Add(10 * time.Second),
		testNow().Add(11 * time.Second),
	}}
	service := newRealService(t, library, registry, projectapp.NewSecureIDGenerator(), clock)
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "Assets", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	firstEpisodeID := created.Episodes[0].ID
	second, err := service.CreateEpisode(ctx, projectapp.CreateEpisodeInput{ProjectID: created.Project.ID, TargetDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	createdAsset, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{
		ProjectID: created.Project.ID, EpisodeID: firstEpisodeID,
		Kind: domain.AssetKindCharacter, Name: "林晚", Prompt: "青年，黑色风衣\r\n雨夜",
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdAsset.Asset.Asset.Prompt != "青年，黑色风衣\n雨夜" || createdAsset.Asset.Identity.Name != "林晚" {
		t.Fatalf("CreateAsset() = %#v", createdAsset.Asset)
	}
	candidates, err := service.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{ProjectID: created.Project.ID, EpisodeID: second.Episode.ID, Kind: domain.AssetKindCharacter, NameQuery: "林"})
	if err != nil || len(candidates) != 1 || candidates[0].Asset.Asset.ID != createdAsset.Asset.Asset.ID || candidates[0].EpisodeSequence != 1 {
		t.Fatalf("FindPriorAssetCandidates() = %#v, %v", candidates, err)
	}
	if _, err := service.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{ProjectID: created.Project.ID, EpisodeID: second.Episode.ID, NameQuery: " 林"}); !errors.Is(err, projectapp.ErrInvalidRequest) {
		t.Fatalf("invalid prior Asset query error = %v", err)
	}
	derived, err := service.DeriveAsset(ctx, projectapp.DeriveAssetInput{
		ProjectID: created.Project.ID, EpisodeID: second.Episode.ID,
		SourceAssetID: createdAsset.Asset.Asset.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if derived.Asset.Asset.Prompt != createdAsset.Asset.Asset.Prompt ||
		derived.Asset.Asset.DerivedFromAssetID != createdAsset.Asset.Asset.ID ||
		derived.Asset.Identity.ID != createdAsset.Asset.Identity.ID {
		t.Fatalf("DeriveAsset() = %#v", derived.Asset)
	}
	candidates, err = service.FindPriorAssetCandidates(ctx, projectapp.FindPriorAssetCandidatesInput{ProjectID: created.Project.ID, EpisodeID: second.Episode.ID})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("present identity candidates = %#v, %v", candidates, err)
	}
	updated, err := service.UpdateAssetPrompt(ctx, projectapp.UpdateAssetPromptInput{
		ProjectID: created.Project.ID, AssetID: derived.Asset.Asset.ID,
		Prompt: "中年，灰色大衣", ExpectedRevision: derived.Asset.Asset.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := service.RenameAssetIdentity(ctx, projectapp.RenameAssetIdentityInput{
		ProjectID: created.Project.ID, AssetIdentityID: updated.Asset.Identity.ID,
		Name: "林晚（统一身份）", ExpectedRevision: updated.Asset.Identity.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Identity.Name != "林晚（统一身份）" {
		t.Fatalf("RenameAssetIdentity() = %#v", renamed.Identity)
	}
	deleted, err := service.DeleteAsset(ctx, projectapp.DeleteAssetInput{
		ProjectID: created.Project.ID, AssetID: updated.Asset.Asset.ID,
		ExpectedRevision: updated.Asset.Asset.Revision,
	})
	if err != nil || !deleted.Asset.Asset.IsDeleted() {
		t.Fatalf("DeleteAsset() = %#v, %v", deleted, err)
	}
	active, err := service.ListEpisodeAssets(ctx, projectapp.ListEpisodeAssetsInput{
		ProjectID: created.Project.ID, EpisodeID: second.Episode.ID,
	})
	if err != nil || len(active) != 0 {
		t.Fatalf("ListEpisodeAssets(active) = %#v, %v", active, err)
	}
	restored, err := service.RestoreAsset(ctx, projectapp.RestoreAssetInput{
		ProjectID: created.Project.ID, AssetID: deleted.Asset.Asset.ID,
		ExpectedRevision: deleted.Asset.Asset.Revision,
	})
	if err != nil || restored.Asset.Asset.IsDeleted() {
		t.Fatalf("RestoreAsset() = %#v, %v", restored, err)
	}
	other, err := service.CreateAsset(ctx, projectapp.CreateAssetInput{
		ProjectID: created.Project.ID, EpisodeID: firstEpisodeID,
		Kind: domain.AssetKindCharacter, Name: "顾言", Prompt: "深色西装",
	})
	if err != nil {
		t.Fatal(err)
	}
	relinked, err := service.RelinkAssetIdentity(ctx, projectapp.RelinkAssetIdentityInput{
		ProjectID: created.Project.ID, AssetID: restored.Asset.Asset.ID,
		TargetAssetIdentityID: other.Asset.Identity.ID,
		ExpectedAssetRevision: restored.Asset.Asset.Revision,
	})
	if err != nil || relinked.Asset.Identity.Name != "顾言" ||
		relinked.Asset.Asset.DerivedFromAssetID != createdAsset.Asset.Asset.ID {
		t.Fatalf("RelinkAssetIdentity() = %#v, %v", relinked, err)
	}
	detached, err := service.DetachAssetIdentity(ctx, projectapp.DetachAssetIdentityInput{
		ProjectID: created.Project.ID, AssetID: relinked.Asset.Asset.ID,
		NewName: "顾言替身", ExpectedAssetRevision: relinked.Asset.Asset.Revision,
	})
	if err != nil || detached.Asset.Identity.Name != "顾言替身" ||
		detached.Asset.Asset.DerivedFromAssetID != createdAsset.Asset.Asset.ID {
		t.Fatalf("DetachAssetIdentity() = %#v, %v", detached, err)
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}

	reopenedRegistry := projectapp.NewProjectStoreRegistry()
	reopened := newRealService(t, library, reopenedRegistry, projectapp.NewSecureIDGenerator(), fixedClock{at: testNow().Add(time.Minute)})
	if _, err := reopened.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root}); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetAsset(ctx, projectapp.GetAssetInput{
		ProjectID: created.Project.ID, AssetID: detached.Asset.Asset.ID,
	})
	if err != nil || got.Asset.Prompt != "中年，灰色大衣" || got.Identity.Name != "顾言替身" {
		t.Fatalf("GetAsset() after restart = %#v, %v", got, err)
	}
	if err := reopenedRegistry.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestImportScriptWithRealFileAndDatabaseWithoutCopyingSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "import-workflow")
	source := filepath.Join(t.TempDir(), "episode.md")
	if err := os.WriteFile(source, []byte("# 标题\r\n\r\n## 场景一\r\n\r\n雨夜。"), 0o600); err != nil {
		t.Fatal(err)
	}
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectapp.NewProjectStoreRegistry()
	service := newRealService(t, library, registry, testIDs(t), fixedClock{at: testNow()})
	created, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "Import", OutputWidth: 720, OutputHeight: 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	created = addFirstEpisode(t, service, created)
	result, err := service.ImportScript(ctx, projectapp.ImportScriptInput{
		ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID,
		SourcePath: source, ExpectedScriptRevision: created.Episodes[0].ScriptRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Episode.Script.Markdown != "# 标题\n\n## 场景一\n\n雨夜。" || result.Episode.Script.Revision != 2 || len(result.Diagnostics) != 1 {
		t.Fatalf("ImportScript() = %#v", result)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source was modified or removed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("imports directory contains %d entries, want none", len(entries))
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}

	reopenedRegistry := projectapp.NewProjectStoreRegistry()
	reopened := newRealService(t, library, reopenedRegistry, testIDs(t), fixedClock{at: testNow().Add(time.Minute)})
	if _, err := reopened.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root}); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetEpisode(ctx, projectapp.GetEpisodeInput{
		ProjectID: created.Project.ID, EpisodeID: created.Episodes[0].ID,
	})
	if err != nil || got.Script != result.Episode.Script {
		t.Fatalf("GetEpisode() after restart = %#v, %v", got, err)
	}
	if err := reopenedRegistry.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationFailureLeavesRecoverableProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "recoverable")
	registry := projectapp.NewProjectStoreRegistry()
	service := newRealService(t, failingLibrary{}, registry, testIDs(t), fixedClock{at: testNow()})
	_, err := service.CreateProject(ctx, projectapp.CreateProjectInput{
		RootPath: root, Name: "Recoverable", OutputWidth: 720, OutputHeight: 1280,
	})
	if !errors.Is(err, projectapp.ErrProjectCreatedNotRegistered) {
		t.Fatalf("CreateProject() error = %v, want ErrProjectCreatedNotRegistered", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "project.db")); statErr != nil {
		t.Fatalf("valid Project was not preserved: %v", statErr)
	}

	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRegistry := projectapp.NewProjectStoreRegistry()
	recovery := newRealService(t, library, recoveryRegistry, testIDs(t), fixedClock{at: testNow().Add(time.Second)})
	result, err := recovery.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: root})
	if err != nil {
		t.Fatalf("recover OpenProject() error = %v", err)
	}
	if _, err := studioStore.Project(ctx, result.Project.ID.String()); err != nil {
		t.Fatalf("recovered Studio entry: %v", err)
	}
	if err := recoveryRegistry.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenProjectMapsRealDatabaseFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	studioStore := newStudioStore(t)
	defer studioStore.Close()
	library, err := studio.NewApplicationProjectLibrary(studioStore)
	if err != nil {
		t.Fatal(err)
	}
	service := newRealService(t, library, projectapp.NewProjectStoreRegistry(), testIDs(t), fixedClock{at: testNow()})

	emptyRoot := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(emptyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: emptyRoot}); !errors.Is(err, projectapp.ErrProjectDatabaseNotFound) {
		t.Fatalf("OpenProject(missing database) error = %v", err)
	}

	corruptRoot := filepath.Join(t.TempDir(), "corrupt")
	if err := os.Mkdir(corruptRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptRoot, "project.db"), []byte("not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: corruptRoot}); !errors.Is(err, projectapp.ErrProjectDatabaseInvalid) {
		t.Fatalf("OpenProject(corrupt database) error = %v", err)
	}

	newerRoot := filepath.Join(t.TempDir(), "newer")
	if err := os.Mkdir(newerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sharedsqlite.Open(ctx, sharedsqlite.Options{
		Path: filepath.Join(newerRoot, "project.db"), Kind: sharedsqlite.Project, Mode: sharedsqlite.CreateNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenProject(ctx, projectapp.OpenProjectInput{RootPath: newerRoot}); !errors.Is(err, projectapp.ErrProjectSchemaTooNew) {
		t.Fatalf("OpenProject(newer schema) error = %v", err)
	}
}

type fixedIDs struct {
	projectID domain.ProjectID
	episodeID domain.EpisodeID
}

type continuityIDs struct {
	fixedIDs
	mediaVersionCalls int
	artifactCalls     int
}

func (i *continuityIDs) NewArtifactID() (domain.ArtifactID, error) {
	i.artifactCalls++
	if i.artifactCalls == 1 {
		return domain.ParseArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	}
	return domain.ParseArtifactID("art_EBCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func (i *continuityIDs) NewMediaVersionID() (domain.MediaVersionID, error) {
	i.mediaVersionCalls++
	if i.mediaVersionCalls == 1 {
		return domain.ParseMediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	}
	if i.mediaVersionCalls == 2 {
		return domain.ParseMediaVersionID("mvr_DBCDEFGHIJKLMNOPQRSTUVWXYZ")
	}
	return domain.ParseMediaVersionID("mvr_FBCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func (f fixedIDs) NewProjectID() (domain.ProjectID, error) { return f.projectID, nil }
func (f fixedIDs) NewEpisodeID() (domain.EpisodeID, error) { return f.episodeID, nil }
func (f fixedIDs) NewAssetID() (domain.AssetID, error) {
	return domain.ParseAssetID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewAssetIdentityID() (domain.AssetIdentityID, error) {
	return domain.ParseAssetIdentityID("asi_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewSceneID() (domain.SceneID, error) {
	return domain.ParseSceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewClipID() (domain.ClipID, error) {
	return domain.ParseClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func (f fixedIDs) NewArtifactID() (domain.ArtifactID, error) {
	return domain.ParseArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewMediaVersionID() (domain.MediaVersionID, error) {
	return domain.ParseMediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewAudioReferenceAssetID() (domain.AudioReferenceAssetID, error) {
	return domain.ParseAudioReferenceAssetID("aud_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewGenerationBatchID() (domain.GenerationBatchID, error) {
	return domain.ParseGenerationBatchID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewGenerationJobID() (domain.GenerationJobID, error) {
	return domain.ParseGenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (f fixedIDs) NewExportJobID() (domain.ExportJobID, error) {
	return domain.ParseExportJobID("exj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type queuedIDs struct {
	projectID  domain.ProjectID
	episodeIDs []domain.EpisodeID
	next       int
}

func (q *queuedIDs) NewProjectID() (domain.ProjectID, error) { return q.projectID, nil }

func (q *queuedIDs) NewEpisodeID() (domain.EpisodeID, error) {
	if q.next >= len(q.episodeIDs) {
		return "", errors.New("no queued Episode ID")
	}
	value := q.episodeIDs[q.next]
	q.next++
	return value, nil
}

func (q *queuedIDs) NewAssetID() (domain.AssetID, error) {
	return domain.ParseAssetID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func (q *queuedIDs) NewAssetIdentityID() (domain.AssetIdentityID, error) {
	return domain.ParseAssetIdentityID("asi_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewSceneID() (domain.SceneID, error) {
	return domain.ParseSceneID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewClipID() (domain.ClipID, error) {
	return domain.ParseClipID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func (q *queuedIDs) NewArtifactID() (domain.ArtifactID, error) {
	return domain.ParseArtifactID("art_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewMediaVersionID() (domain.MediaVersionID, error) {
	return domain.ParseMediaVersionID("mvr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewAudioReferenceAssetID() (domain.AudioReferenceAssetID, error) {
	return domain.ParseAudioReferenceAssetID("aud_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewGenerationBatchID() (domain.GenerationBatchID, error) {
	return domain.ParseGenerationBatchID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewGenerationJobID() (domain.GenerationJobID, error) {
	return domain.ParseGenerationJobID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}
func (q *queuedIDs) NewExportJobID() (domain.ExportJobID, error) {
	return domain.ParseExportJobID("exj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

type queuedClock struct {
	values []time.Time
	next   int
}

func (q *queuedClock) Now() time.Time {
	if q.next >= len(q.values) {
		return time.Time{}
	}
	value := q.values[q.next]
	q.next++
	return value
}

type failingLibrary struct{}

func (failingLibrary) SyncProject(context.Context, projectapp.ProjectLibraryRecord) error {
	return errors.New("injected Studio failure")
}

func (failingLibrary) Project(context.Context, domain.ProjectID) (projectapp.ProjectCatalogEntry, error) {
	return projectapp.ProjectCatalogEntry{}, errors.New("injected Studio failure")
}

func (failingLibrary) CanDeleteProject(context.Context, domain.ProjectID) error {
	return errors.New("injected Studio failure")
}
func (failingLibrary) CanDeleteEpisode(context.Context, domain.ProjectID, domain.EpisodeID) error {
	return errors.New("injected Studio failure")
}
func (failingLibrary) DeleteProject(context.Context, domain.ProjectID) error {
	return errors.New("injected Studio failure")
}

func newRealService(
	t *testing.T,
	library projectapp.ProjectLibrary,
	registry *projectapp.ProjectStoreRegistry,
	ids projectapp.IDGenerator,
	clock projectapp.Clock,
	mediaFiles ...projectapp.MediaFileProcessor,
) *projectapp.Service {
	t.Helper()
	var processor projectapp.MediaFileProcessor
	if len(mediaFiles) != 0 {
		processor = mediaFiles[0]
	}
	service, err := projectapp.NewService(projectapp.Dependencies{
		IDs: ids, Clock: clock,
		Workspace:    platformpaths.ApplicationProjectWorkspace{},
		Stores:       projectstore.ApplicationStoreFactory{},
		Library:      library,
		Importer:     scriptimport.FileImporter{},
		Registry:     registry,
		MediaFiles:   processor,
		ProjectFiles: platformpaths.ApplicationProjectFileReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func addFirstEpisode(t *testing.T, service *projectapp.Service, created projectapp.ProjectResult) projectapp.ProjectResult {
	t.Helper()
	result, err := service.CreateEpisode(context.Background(), projectapp.CreateEpisodeInput{
		ProjectID: created.Project.ID, TargetDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateEpisode(first) error = %v", err)
	}
	return projectapp.ProjectResult{Project: result.Project, Episodes: result.Episodes}
}

type recordingMediaProcessor struct {
	request      projectapp.PrepareMediaRequest
	frameRequest projectapp.PrepareVideoFrameRequest
	sha256       string
}

func (p *recordingMediaProcessor) Prepare(_ context.Context, request projectapp.PrepareMediaRequest) (projectapp.PreparedMedia, error) {
	p.request = request
	width, height := request.RequiredWidth, request.RequiredHeight
	if width == 0 || height == 0 {
		width, height = 512, 512
	}
	hash := p.sha256
	if hash == "" {
		hash = strings.Repeat("a", 64)
	}
	return projectapp.PreparedMedia{Token: "prepared.png", Kind: request.Kind, Extension: ".png", SHA256: hash, ByteSize: 100, MIMEType: "image/png", OriginalFilename: "source.png", Width: width, Height: height}, nil
}
func (p *recordingMediaProcessor) PrepareVideoFrame(_ context.Context, request projectapp.PrepareVideoFrameRequest) (projectapp.PreparedMedia, error) {
	p.frameRequest = request
	hash := p.sha256
	if hash == "" {
		hash = strings.Repeat("b", 64)
	}
	return projectapp.PreparedMedia{Token: "prepared-frame.png", Kind: domain.MediaKindImage, Extension: ".png", SHA256: hash, ByteSize: 100, MIMEType: "image/png", OriginalFilename: request.OriginalFilename, Width: request.RequiredWidth, Height: request.RequiredHeight}, nil
}
func (*recordingMediaProcessor) Install(_ context.Context, _ string, prepared projectapp.PreparedMedia, id domain.ArtifactID) (projectapp.InstalledMedia, error) {
	return projectapp.InstalledMedia{RelativePath: "media/images/" + id.String() + prepared.Extension}, nil
}
func (*recordingMediaProcessor) Discard(string, projectapp.PreparedMedia) error { return nil }
func (*recordingMediaProcessor) Remove(string, projectapp.InstalledMedia) error { return nil }

func newStudioStore(t *testing.T) *studio.Store {
	t.Helper()
	store, err := studio.Open(context.Background(), filepath.Join(t.TempDir(), "studio.db"), sharedsqlite.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testIDs(t *testing.T) fixedIDs {
	t.Helper()
	projectID, err := domain.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	return fixedIDs{projectID: projectID, episodeID: episodeID}
}

func testNow() time.Time {
	return time.Date(2026, time.September, 2, 16, 0, 0, 123_000_000, time.UTC)
}

func workflowIDs(t *testing.T) (domain.ProjectID, domain.EpisodeID, domain.EpisodeID) {
	t.Helper()
	projectID, err := domain.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	first, err := domain.ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := domain.ParseEpisodeID("ep_77777777777777777777777777")
	if err != nil {
		t.Fatal(err)
	}
	return projectID, first, second
}
