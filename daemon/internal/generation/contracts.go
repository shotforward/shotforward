package generation

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

var (
	ErrGeneratorNotFound      = errors.New("generation Generator not found")
	ErrInvalidGeneratorResult = errors.New("invalid generation Generator result")
)

type retryablePollError struct {
	cause error
}

func (e retryablePollError) Error() string { return e.cause.Error() }
func (e retryablePollError) Unwrap() error { return e.cause }

// NewRetryablePollError marks a transient error returned by Generator.Poll.
// The scheduler retries only explicitly marked polling errors, leaving all
// other provider, protocol, and transport failures terminal.
func NewRetryablePollError(err error) error {
	if err == nil || IsRetryablePollError(err) {
		return err
	}
	return retryablePollError{cause: err}
}

func IsRetryablePollError(err error) bool {
	var target retryablePollError
	return errors.As(err, &target)
}

type GeneratorRequest struct {
	JobID       domain.GenerationJobID
	Snapshot    domain.GenerationInputSnapshot
	ProjectRoot string
	References  []GeneratorArtifactReference
}

type GeneratorArtifactReference struct {
	Input        domain.GenerationArtifactReference
	Artifact     domain.Artifact
	AbsolutePath string
}

func (r GeneratorRequest) Validate() error {
	if r.JobID.Validate() != nil || r.Snapshot.Validate() != nil || r.ProjectRoot == "" || !filepath.IsAbs(r.ProjectRoot) || filepath.Clean(r.ProjectRoot) != r.ProjectRoot || len(r.References) != len(r.Snapshot.References) {
		return errors.New("invalid Generator request")
	}
	for index, reference := range r.References {
		if reference.Input != r.Snapshot.References[index] || reference.Artifact.ID != reference.Input.ArtifactID || reference.Artifact.Validate() != nil || reference.AbsolutePath == "" || !filepath.IsAbs(reference.AbsolutePath) {
			return errors.New("invalid Generator Artifact reference")
		}
	}
	return nil
}

type GeneratorOperation struct {
	ID string
}

func (o GeneratorOperation) Validate() error {
	if !validGeneratorText(o.ID, 500, true) {
		return errors.New("invalid Generator operation")
	}
	return nil
}

type GeneratorState string

const (
	GeneratorRunning   GeneratorState = "running"
	GeneratorSucceeded GeneratorState = "succeeded"
	GeneratorFailed    GeneratorState = "failed"
	GeneratorStopped   GeneratorState = "stopped"
)

type GeneratorOutput struct {
	SourcePath               string
	RemoteReferenceURL       string
	RemoteReferenceExpiresAt *time.Time
}

type GeneratorStatus struct {
	State          GeneratorState
	Stage          string
	Message        string
	Outputs        []GeneratorOutput
	FailureCode    string
	FailureMessage string
}

func (s GeneratorStatus) Validate() error {
	if s.State != GeneratorRunning && s.State != GeneratorSucceeded && s.State != GeneratorFailed && s.State != GeneratorStopped {
		return ErrInvalidGeneratorResult
	}
	if !validGeneratorText(s.Stage, 100, false) || !utf8.ValidString(s.Message) || utf8.RuneCountInString(s.Message) > domain.MaxGenerationProgressCharacters || strings.ContainsRune(s.Message, 0) {
		return ErrInvalidGeneratorResult
	}
	switch s.State {
	case GeneratorRunning:
		if len(s.Outputs) != 0 || s.FailureCode != "" || s.FailureMessage != "" {
			return ErrInvalidGeneratorResult
		}
	case GeneratorSucceeded:
		if len(s.Outputs) != 1 || strings.TrimSpace(s.Outputs[0].SourcePath) == "" || !filepath.IsAbs(s.Outputs[0].SourcePath) || filepath.Clean(s.Outputs[0].SourcePath) != s.Outputs[0].SourcePath || s.FailureCode != "" || s.FailureMessage != "" {
			return ErrInvalidGeneratorResult
		}
		if !validRemoteReference(s.Outputs[0]) {
			return ErrInvalidGeneratorResult
		}
	case GeneratorFailed:
		if len(s.Outputs) != 0 || !validGeneratorText(s.FailureCode, 100, true) || !utf8.ValidString(s.FailureMessage) || strings.TrimSpace(s.FailureMessage) == "" || utf8.RuneCountInString(s.FailureMessage) > 2_000 || strings.ContainsRune(s.FailureMessage, 0) {
			return ErrInvalidGeneratorResult
		}
	case GeneratorStopped:
		if len(s.Outputs) != 0 || s.FailureCode != "" || s.FailureMessage != "" {
			return ErrInvalidGeneratorResult
		}
	}
	return nil
}

func validRemoteReference(output GeneratorOutput) bool {
	if output.RemoteReferenceURL == "" {
		return output.RemoteReferenceExpiresAt == nil
	}
	if output.RemoteReferenceExpiresAt == nil || output.RemoteReferenceExpiresAt.IsZero() || len(output.RemoteReferenceURL) > 8_192 || strings.TrimSpace(output.RemoteReferenceURL) != output.RemoteReferenceURL {
		return false
	}
	parsed, err := url.Parse(output.RemoteReferenceURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

type Generator interface {
	ID() domain.GeneratorID
	ConcurrencyLimit() int
	SupportsRecovery(domain.GenerationInputSnapshot) bool
	Start(context.Context, GeneratorRequest) (GeneratorOperation, error)
	Poll(context.Context, GeneratorOperation) (GeneratorStatus, error)
	Stop(context.Context, GeneratorOperation) error
}

// PollIntervalGenerator lets an adapter request a slower successful-status
// polling cadence than the scheduler default. Transient-error backoff remains
// controlled by the scheduler.
type PollIntervalGenerator interface {
	PollInterval() time.Duration
}

type GeneratorRegistry interface {
	Generator(domain.GeneratorID) (Generator, bool)
}

func validGeneratorText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00") && utf8.RuneCountInString(value) <= maximum && value == strings.TrimSpace(value) && (!required || value != "")
}

func generatorError(code string, err error) (string, string) {
	var business interface{ GenerationFailureCode() string }
	if errors.As(err, &business) && IsCloudBusinessFailure(business.GenerationFailureCode()) {
		return business.GenerationFailureCode(), "ShotForward account cannot accept this request"
	}
	var public interface{ PublicFailureMessage() string }
	if errors.As(err, &public) {
		if message := PublicProviderFailureMessage(ProviderResponseFailureCode, public.PublicFailureMessage()); message != "" {
			return ProviderResponseFailureCode, message
		}
	}
	var categorized interface{ GenerationFailureCode() string }
	if errors.As(err, &categorized) && isPublicGenerationFailureCode(categorized.GenerationFailureCode()) {
		code = categorized.GenerationFailureCode()
	}
	message := strings.ReplaceAll(strings.ToValidUTF8(err.Error(), "�"), "\x00", "�")
	if strings.TrimSpace(message) == "" {
		message = code
	}
	runes := []rune(message)
	if len(runes) > 2_000 {
		message = string(runes[:2_000])
	}
	return code, message
}

// PublicGeneratorError converts a Generator adapter error into the same safe,
// user-facing code and message used by persisted generation jobs.
func PublicGeneratorError(code string, err error) (string, string) {
	return generatorError(code, err)
}
