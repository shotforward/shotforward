// Package exportjob runs durable local Episode export jobs.
package exportjob

import (
	"context"
	"errors"
	"path/filepath"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type ClipSource struct {
	Snapshot     domain.ExportClipSnapshot
	Artifact     domain.Artifact
	AbsolutePath string
}

type Request struct {
	JobID        domain.ExportJobID
	ProjectRoot  string
	OutputWidth  int
	OutputHeight int
	Clips        []ClipSource
}

func (r Request) Validate() error {
	if r.JobID.Validate() != nil || r.ProjectRoot == "" || !filepath.IsAbs(r.ProjectRoot) || filepath.Clean(r.ProjectRoot) != r.ProjectRoot || len(r.Clips) == 0 {
		return errors.New("invalid export request")
	}
	if _, err := domain.NewOutputSpec(r.OutputWidth, r.OutputHeight); err != nil {
		return errors.New("invalid export output specification")
	}
	for _, clip := range r.Clips {
		expected := filepath.Join(r.ProjectRoot, filepath.FromSlash(clip.Artifact.RelativePath))
		if clip.Snapshot.Validate() != nil || clip.Artifact.Validate() != nil || clip.Artifact.ID != clip.Snapshot.ArtifactID || clip.Artifact.Kind != domain.MediaKindVideo || clip.AbsolutePath == "" || !filepath.IsAbs(clip.AbsolutePath) || filepath.Clean(clip.AbsolutePath) != clip.AbsolutePath || clip.AbsolutePath != expected {
			return errors.New("invalid export clip source")
		}
	}
	return nil
}

type Progress func(stage, message string) error

type Processor interface {
	Export(context.Context, Request, Progress) (string, error)
	RemoveOutput(string, string) error
}

var _ projectapp.ExportQueueNotifier = (*Scheduler)(nil)
