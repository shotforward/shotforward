package exportjob

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type Options struct {
	Registry  *projectapp.ProjectStoreRegistry
	Processor Processor
	Clock     projectapp.Clock
	Events    projectapp.ExportEventPublisher
}

type Scheduler struct {
	registry  *projectapp.ProjectStoreRegistry
	processor Processor
	clock     projectapp.Clock
	events    projectapp.ExportEventPublisher
	wake      chan struct{}
	mutex     sync.Mutex
	active    map[string]context.CancelFunc
	waitGroup sync.WaitGroup
}

func NewScheduler(options Options) (*Scheduler, error) {
	if options.Registry == nil || options.Processor == nil || options.Clock == nil {
		return nil, errors.New("create Export scheduler: invalid dependencies")
	}
	return &Scheduler{registry: options.Registry, processor: options.Processor, clock: options.Clock, events: options.Events, wake: make(chan struct{}, 1), active: map[string]context.CancelFunc{}}, nil
}

func (s *Scheduler) NotifyExport(projectID domain.ProjectID, jobID domain.ExportJobID) {
	key := projectID.String() + "/" + jobID.String()
	s.mutex.Lock()
	cancel := s.active[key]
	s.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run Export scheduler: context is required")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			s.waitGroup.Wait()
			return nil
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) ProcessNext(ctx context.Context) (bool, error) {
	candidates, err := s.candidates(ctx)
	if err != nil || len(candidates) == 0 {
		return false, err
	}
	return true, s.process(ctx, ctx, candidates[0].handle, candidates[0].job)
}

type candidate struct {
	handle projectapp.ProjectHandle
	job    domain.ExportJob
}

func (s *Scheduler) candidates(ctx context.Context) ([]candidate, error) {
	result := []candidate{}
	for _, handle := range s.registry.Handles() {
		jobs, err := handle.Store.ExportJobsByStates(ctx, []domain.ExportJobState{domain.ExportJobQueued, domain.ExportJobRunning, domain.ExportJobCancelRequested}, 10_000)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			result = append(result, candidate{handle, job})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].job.CreatedAt.Equal(result[j].job.CreatedAt) {
			return result[i].job.ID.String() < result[j].job.ID.String()
		}
		return result[i].job.CreatedAt.Before(result[j].job.CreatedAt)
	})
	return result, nil
}

func (s *Scheduler) dispatch(shutdown context.Context) {
	s.mutex.Lock()
	if len(s.active) != 0 {
		s.mutex.Unlock()
		return
	}
	s.mutex.Unlock()
	candidates, err := s.candidates(shutdown)
	if err != nil || len(candidates) == 0 {
		return
	}
	candidate := candidates[0]
	key := candidate.job.ProjectID.String() + "/" + candidate.job.ID.String()
	execution, cancel := context.WithCancel(shutdown)
	s.mutex.Lock()
	if len(s.active) != 0 {
		s.mutex.Unlock()
		cancel()
		return
	}
	s.active[key] = cancel
	s.waitGroup.Add(1)
	s.mutex.Unlock()
	go func() {
		defer func() {
			s.mutex.Lock()
			delete(s.active, key)
			s.mutex.Unlock()
			cancel()
			s.waitGroup.Done()
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}()
		_ = s.process(shutdown, execution, candidate.handle, candidate.job)
	}()
}

func (s *Scheduler) process(shutdown, execution context.Context, handle projectapp.ProjectHandle, job domain.ExportJob) error {
	if job.State == domain.ExportJobQueued {
		at, err := s.now()
		if err != nil {
			return err
		}
		job, err = handle.Store.ClaimExportJob(execution, job.ID, job.Revision, at)
		if err != nil {
			if errors.Is(err, projectapp.ErrExportJobStateConflict) {
				return nil
			}
			return err
		}
		s.publish(job)
	}
	if job.State == domain.ExportJobCancelRequested {
		return s.finish(context.WithoutCancel(shutdown), handle.Store, job, domain.ExportJobCancelled, "", "", "")
	}
	if job.State != domain.ExportJobRunning {
		return nil
	}
	request, err := s.request(execution, handle, job)
	if err != nil {
		current, readErr := handle.Store.ExportJob(context.WithoutCancel(shutdown), job.ID)
		if readErr == nil && current.State == domain.ExportJobCancelRequested {
			return s.finish(context.WithoutCancel(shutdown), handle.Store, current, domain.ExportJobCancelled, "", "", "")
		}
		if shutdown.Err() != nil {
			return nil
		}
		return s.fail(context.WithoutCancel(shutdown), handle.Store, job, "export_source_unavailable", err)
	}
	progress := func(stage, message string) error {
		if !validProgress(stage, message) {
			return errors.New("invalid Export progress")
		}
		at, err := s.now()
		if err != nil {
			return err
		}
		updated, err := handle.Store.UpdateExportProgress(execution, job.ID, job.Revision, stage, message, at)
		if err != nil {
			return err
		}
		job = updated
		s.publish(job)
		return nil
	}
	output, exportErr := s.processor.Export(execution, request, progress)
	current, readErr := handle.Store.ExportJob(context.WithoutCancel(shutdown), job.ID)
	if readErr == nil {
		job = current
	}
	if job.State == domain.ExportJobCancelRequested {
		if output != "" {
			_ = s.processor.RemoveOutput(handle.RootPath, output)
		}
		return s.finish(context.WithoutCancel(shutdown), handle.Store, job, domain.ExportJobCancelled, "", "", "")
	}
	if exportErr != nil {
		if shutdown.Err() != nil {
			return nil
		}
		return s.fail(context.WithoutCancel(shutdown), handle.Store, job, "ffmpeg_failed", exportErr)
	}
	if readErr != nil {
		_ = s.processor.RemoveOutput(handle.RootPath, output)
		return readErr
	}
	return s.finish(context.WithoutCancel(shutdown), handle.Store, job, domain.ExportJobSucceeded, output, "", "")
}

func (s *Scheduler) request(ctx context.Context, handle projectapp.ProjectHandle, job domain.ExportJob) (Request, error) {
	clips := make([]ClipSource, 0, len(job.Snapshot.Clips))
	for _, item := range job.Snapshot.Clips {
		artifact, err := handle.Store.Artifact(ctx, item.ArtifactID)
		if err != nil {
			return Request{}, err
		}
		clips = append(clips, ClipSource{Snapshot: item, Artifact: artifact, AbsolutePath: filepath.Join(handle.RootPath, filepath.FromSlash(artifact.RelativePath))})
	}
	request := Request{JobID: job.ID, ProjectRoot: handle.RootPath, OutputWidth: job.Snapshot.OutputWidth, OutputHeight: job.Snapshot.OutputHeight, Clips: clips}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func (s *Scheduler) fail(ctx context.Context, store projectapp.ProjectStore, job domain.ExportJob, code string, cause error) error {
	message := safeMessage(cause)
	return s.finish(ctx, store, job, domain.ExportJobFailed, "", code, message)
}

func (s *Scheduler) finish(ctx context.Context, store projectapp.ProjectStore, job domain.ExportJob, state domain.ExportJobState, output, code, message string) error {
	current, err := store.ExportJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if current.State.IsTerminal() {
		return nil
	}
	at, err := s.now()
	if err != nil {
		return err
	}
	finished, err := store.FinishExportJob(ctx, current.ID, current.Revision, state, output, code, message, at)
	if err != nil {
		return err
	}
	s.publish(finished)
	return nil
}

func (s *Scheduler) publish(job domain.ExportJob) {
	if s.events != nil {
		s.events.PublishExportJob(job)
	}
}
func (s *Scheduler) now() (time.Time, error) {
	value := s.clock.Now()
	if value.IsZero() || value.Location() != time.UTC || value.UnixMilli() < 0 || value.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("invalid Export scheduler time")
	}
	return value, nil
}
func validProgress(stage, message string) bool {
	return stage != "" && stage == strings.TrimSpace(stage) && utf8.RuneCountInString(stage) <= 100 && !strings.ContainsAny(stage, "\r\n\x00") && utf8.ValidString(message) && utf8.RuneCountInString(message) <= 500 && !strings.ContainsRune(message, 0)
}
func safeMessage(err error) string {
	message := "Episode export failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = strings.ReplaceAll(strings.ToValidUTF8(err.Error(), "�"), "\x00", "�")
	}
	runes := []rune(message)
	if len(runes) > 2000 {
		message = string(runes[:2000])
	}
	return fmt.Sprintf("%s", message)
}
