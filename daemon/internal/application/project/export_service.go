package projectapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Service) GetExportPreview(ctx context.Context, input GetExportPreviewInput) (domain.ExportSnapshot, error) {
	handle, err := s.assetProjectHandle(ctx, "get export preview", input.ProjectID)
	if err != nil {
		return domain.ExportSnapshot{}, err
	}
	if _, err := episodeForProject(ctx, handle.Store, input.ProjectID, input.EpisodeID); err != nil {
		return domain.ExportSnapshot{}, fmt.Errorf("get export preview: %w", err)
	}
	project, err := handle.Store.Project(ctx)
	if err != nil {
		return domain.ExportSnapshot{}, fmt.Errorf("get export preview: %w", err)
	}
	storyboard, err := handle.Store.Storyboard(ctx, input.EpisodeID)
	if err != nil {
		return domain.ExportSnapshot{}, fmt.Errorf("get export preview: %w", err)
	}
	snapshot, err := buildExportSnapshot(ctx, handle.Store, project, storyboard)
	if err != nil {
		return domain.ExportSnapshot{}, fmt.Errorf("get export preview: %w", err)
	}
	return snapshot, nil
}

func (s *Service) SubmitExportJob(ctx context.Context, input SubmitExportJobInput) (domain.ExportJob, error) {
	handle, err := s.assetProjectHandle(ctx, "submit ExportJob", input.ProjectID)
	if err != nil {
		return domain.ExportJob{}, err
	}
	if input.ProjectID.Validate() != nil || input.EpisodeID.Validate() != nil || !validExportOperationKey(input.OperationKey) || input.ExpectedStoryboardRevision < 1 {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", ErrInvalidRequest)
	}
	if existing, err := handle.Store.ExportJobByOperationKey(ctx, input.ProjectID, input.OperationKey); err == nil {
		if existing.EpisodeID != input.EpisodeID || existing.Snapshot.StoryboardRevision != input.ExpectedStoryboardRevision {
			return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", ErrExportOperationConflict)
		}
		return existing, nil
	} else if !errors.Is(err, ErrExportJobNotFound) {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", err)
	}
	snapshot, err := s.GetExportPreview(ctx, GetExportPreviewInput{ProjectID: input.ProjectID, EpisodeID: input.EpisodeID})
	if err != nil {
		return domain.ExportJob{}, err
	}
	if snapshot.StoryboardRevision != input.ExpectedStoryboardRevision {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", ErrStoryboardRevisionConflict)
	}
	if len(snapshot.Clips) == 0 {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", ErrExportNotReady)
	}
	if reusable, found, err := s.reusableExportJob(ctx, handle, input.EpisodeID, snapshot); err != nil {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: find reusable export: %w", err)
	} else if found {
		return reusable, nil
	}
	jobID, err := s.ids.NewExportJobID()
	if err != nil {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w: %w", ErrProjectWriteFailed, err)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", err)
	}
	job := domain.ExportJob{ID: jobID, ProjectID: input.ProjectID, EpisodeID: input.EpisodeID, OperationKey: input.OperationKey, State: domain.ExportJobQueued, Snapshot: snapshot, ProgressStage: "queued", ProgressMessage: "Waiting to export episode", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := job.Validate(); err != nil {
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w: %w", ErrInvalidRequest, err)
	}
	if err := handle.Store.CreateExportJob(ctx, job); err != nil {
		if errors.Is(err, ErrExportEpisodeBusy) {
			if existing, readErr := handle.Store.ExportJobByOperationKey(ctx, input.ProjectID, input.OperationKey); readErr == nil {
				if existing.EpisodeID == input.EpisodeID && existing.Snapshot.StoryboardRevision == input.ExpectedStoryboardRevision {
					return existing, nil
				}
				return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", ErrExportOperationConflict)
			}
		}
		return domain.ExportJob{}, fmt.Errorf("submit ExportJob: %w", err)
	}
	s.publishExportJob(job)
	if s.exportQueue != nil {
		s.exportQueue.NotifyExport(job.ProjectID, job.ID)
	}
	return job, nil
}

func (s *Service) reusableExportJob(ctx context.Context, handle ProjectHandle, episodeID domain.EpisodeID, snapshot domain.ExportSnapshot) (domain.ExportJob, bool, error) {
	jobs, err := handle.Store.RecentExportJobs(ctx, 1_000)
	if err != nil {
		return domain.ExportJob{}, false, err
	}
	for _, job := range jobs {
		if job.EpisodeID != episodeID || !job.Snapshot.HasSameOutput(snapshot) {
			continue
		}
		if job.State.IsActive() {
			return job, true, nil
		}
		if job.State != domain.ExportJobSucceeded || job.OutputRelativePath == "" || s.projectFiles == nil {
			continue
		}
		file, openErr := s.projectFiles.OpenProjectFile(handle.RootPath, job.OutputRelativePath)
		if openErr != nil {
			continue
		}
		if closeErr := file.Content.Close(); closeErr == nil {
			return job, true, nil
		}
	}
	return domain.ExportJob{}, false, nil
}

func (s *Service) GetExportJob(ctx context.Context, input GetExportJobInput) (domain.ExportJob, error) {
	handle, err := s.assetProjectHandle(ctx, "get ExportJob", input.ProjectID)
	if err != nil {
		return domain.ExportJob{}, err
	}
	if input.ExportJobID.Validate() != nil {
		return domain.ExportJob{}, fmt.Errorf("get ExportJob: %w", ErrInvalidRequest)
	}
	job, err := handle.Store.ExportJob(ctx, input.ExportJobID)
	if err != nil {
		return domain.ExportJob{}, fmt.Errorf("get ExportJob: %w", err)
	}
	if job.ProjectID != input.ProjectID {
		return domain.ExportJob{}, fmt.Errorf("get ExportJob: %w", ErrExportJobNotFound)
	}
	return job, nil
}

func (s *Service) GetExportContent(ctx context.Context, input GetExportJobInput) (ArtifactContentSource, error) {
	job, err := s.GetExportJob(ctx, input)
	if err != nil {
		return ArtifactContentSource{}, err
	}
	if job.State != domain.ExportJobSucceeded || job.OutputRelativePath == "" {
		return ArtifactContentSource{}, fmt.Errorf("get Export content: %w", ErrExportNotReady)
	}
	handle, err := s.assetProjectHandle(ctx, "get Export content", input.ProjectID)
	if err != nil {
		return ArtifactContentSource{}, err
	}
	if s.projectFiles == nil {
		return ArtifactContentSource{}, fmt.Errorf("get Export content: %w", ErrMediaImportUnavailable)
	}
	file, err := s.projectFiles.OpenProjectFile(handle.RootPath, job.OutputRelativePath)
	if err != nil {
		return ArtifactContentSource{}, fmt.Errorf("get Export content: %w", ErrMediaImportUnreadable)
	}
	return ArtifactContentSource{Content: file.Content, MIMEType: "video/mp4", OriginalFilename: job.ID.String() + ".mp4", ByteSize: file.ByteSize, ModifiedAt: file.ModifiedAt}, nil
}

func (s *Service) CancelExportJob(ctx context.Context, input CancelExportJobInput) (domain.ExportJob, error) {
	handle, err := s.assetProjectHandle(ctx, "cancel ExportJob", input.ProjectID)
	if err != nil {
		return domain.ExportJob{}, err
	}
	if input.ExportJobID.Validate() != nil || input.ExpectedRevision < 1 {
		return domain.ExportJob{}, fmt.Errorf("cancel ExportJob: %w", ErrInvalidRequest)
	}
	current, err := handle.Store.ExportJob(ctx, input.ExportJobID)
	if err != nil || current.ProjectID != input.ProjectID {
		return domain.ExportJob{}, fmt.Errorf("cancel ExportJob: %w", ErrExportJobNotFound)
	}
	now, err := validOperationTime(s.clock.Now())
	if err != nil {
		return domain.ExportJob{}, fmt.Errorf("cancel ExportJob: %w", err)
	}
	job, err := handle.Store.RequestExportCancel(ctx, input.ExportJobID, input.ExpectedRevision, now)
	if err != nil {
		return domain.ExportJob{}, fmt.Errorf("cancel ExportJob: %w", err)
	}
	s.publishExportJob(job)
	if s.exportQueue != nil {
		s.exportQueue.NotifyExport(job.ProjectID, job.ID)
	}
	return job, nil
}

func buildExportSnapshot(ctx context.Context, store ProjectStore, project domain.Project, storyboard domain.Storyboard) (domain.ExportSnapshot, error) {
	snapshot := domain.ExportSnapshot{SchemaVersion: domain.ExportSnapshotVersion, StoryboardRevision: storyboard.Revision, OutputWidth: project.Output.Width(), OutputHeight: project.Output.Height(), Clips: []domain.ExportClipSnapshot{}, SkippedClips: []domain.ExportSkippedClip{}}
	position := 0
	for _, scene := range storyboard.Scenes {
		for _, details := range scene.Clips {
			position++
			clip := details.Clip
			slotID, err := domain.FixedMediaSlotID(clip.ID.String(), domain.MediaSlotClipVideo)
			if err != nil {
				return domain.ExportSnapshot{}, ErrProjectDataInvalid
			}
			slot, err := store.MediaSlot(ctx, slotID, false)
			if err != nil {
				return domain.ExportSnapshot{}, err
			}
			if slot.Current == nil {
				snapshot.SkippedClips = append(snapshot.SkippedClips, domain.ExportSkippedClip{SceneID: scene.Scene.ID, ClipID: clip.ID, Position: position, Reason: "missing_current_video"})
				continue
			}
			artifact, exists := slot.Artifacts[slot.Current.ArtifactID]
			if !exists || artifact.ProjectID != project.ID || artifact.Kind != domain.MediaKindVideo || slot.Current.TrimOutMilliseconds > artifact.DurationMilliseconds {
				return domain.ExportSnapshot{}, ErrProjectDataInvalid
			}
			snapshot.Clips = append(snapshot.Clips, domain.ExportClipSnapshot{SceneID: scene.Scene.ID, ClipID: clip.ID, Position: position, MediaSlotID: slot.Slot.ID, MediaSlotRevision: slot.Slot.Revision, MediaVersionID: slot.Current.ID, MediaVersionRevision: slot.Current.Revision, ArtifactID: artifact.ID, TrimInMilliseconds: slot.Current.TrimInMilliseconds, TrimOutMilliseconds: slot.Current.TrimOutMilliseconds})
		}
	}
	if err := snapshot.Validate(); err != nil {
		return domain.ExportSnapshot{}, ErrProjectDataInvalid
	}
	return snapshot, nil
}

func validExportOperationKey(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 200 && !strings.ContainsAny(value, "\r\n\x00")
}

func (s *Service) publishExportJob(job domain.ExportJob) {
	if s.exportEvents != nil {
		s.exportEvents.PublishExportJob(job)
	}
}
