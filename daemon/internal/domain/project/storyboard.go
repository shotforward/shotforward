package project

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxSceneTitleCharacters     = 200
	MaxClipTitleCharacters      = 200
	MaxStoryboardTextCharacters = 20_000
	MaxPlannedDurationSeconds   = math.MaxInt64 / int64(time.Second)
	// Portable Clip planning deliberately uses the intersection shared by
	// Seedance 2.5, Wan 3.0, and MiniMax H3. Provider-specific generation may
	// accept a wider range, but Agent-authored Storyboards stay portable.
	MinPortableClipDurationSeconds int64 = 4
	MaxPortableClipDurationSeconds int64 = 15
	// StoryboardOrderStep leaves room for inserts without renumbering sibling
	// resources. Position is a durable sort key, never a user-facing ordinal.
	StoryboardOrderStep = 1024
)

var (
	ErrInvalidScene              = errors.New("invalid scene")
	ErrInvalidClip               = errors.New("invalid clip")
	ErrInvalidClipGenerationMode = errors.New("invalid clip generation mode")
	ErrInvalidStoryboard         = errors.New("invalid storyboard")
)

type ClipGenerationMode string

const (
	ClipGenerationFirstImage     ClipGenerationMode = "first_image"
	ClipGenerationFirstLastImage ClipGenerationMode = "first_last_image"
	ClipGenerationReference      ClipGenerationMode = "reference"
)

func (m ClipGenerationMode) IsValid() bool {
	switch m {
	case ClipGenerationFirstImage, ClipGenerationFirstLastImage, ClipGenerationReference:
		return true
	default:
		return false
	}
}

type ClipVideoPrompts struct {
	FirstImage     string
	FirstLastImage string
	Reference      string
}

func (p ClipVideoPrompts) ForMode(mode ClipGenerationMode) string {
	switch mode {
	case ClipGenerationFirstImage:
		return p.FirstImage
	case ClipGenerationFirstLastImage:
		return p.FirstLastImage
	case ClipGenerationReference:
		return p.Reference
	default:
		return ""
	}
}

func (p *ClipVideoPrompts) SetForMode(mode ClipGenerationMode, value string) bool {
	switch mode {
	case ClipGenerationFirstImage:
		p.FirstImage = value
	case ClipGenerationFirstLastImage:
		p.FirstLastImage = value
	case ClipGenerationReference:
		p.Reference = value
	default:
		return false
	}
	return true
}

// SceneScriptSource identifies the exact source range used to create a Scene.
// It is immutable provenance; later script edits make it stale rather than
// silently remapping it.
type SceneScriptSource struct {
	ScriptRevision int64
	StartByte      int
	EndByte        int
	Excerpt        string
}

func (s SceneScriptSource) Validate() error {
	if s.ScriptRevision < 1 {
		return fmt.Errorf("%w: source script revision must be at least 1", ErrInvalidScene)
	}
	if s.StartByte < 0 || s.EndByte <= s.StartByte {
		return fmt.Errorf("%w: source byte range is invalid", ErrInvalidScene)
	}
	if !utf8.ValidString(s.Excerpt) || s.Excerpt == "" || strings.ContainsRune(s.Excerpt, 0) || strings.ContainsRune(s.Excerpt, '\r') {
		return fmt.Errorf("%w: source excerpt is invalid", ErrInvalidScene)
	}
	if len(s.Excerpt) != s.EndByte-s.StartByte {
		return fmt.Errorf("%w: source excerpt length does not match its byte range", ErrInvalidScene)
	}
	return nil
}

type Scene struct {
	ID          SceneID
	ProjectID   ProjectID
	EpisodeID   EpisodeID
	Position    int // sparse sort key; presentation ordinals are derived
	Title       string
	Description string
	Source      *SceneScriptSource
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

func (s Scene) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	if err := s.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	if err := s.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	if s.Position < 1 {
		return fmt.Errorf("%w: position must be at least 1", ErrInvalidScene)
	}
	if err := validateNamedTitle("title", s.Title, MaxSceneTitleCharacters); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	if err := validateStoryboardText("description", s.Description); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	if s.Source != nil {
		if err := s.Source.Validate(); err != nil {
			return err
		}
	}
	if err := validateRevisionAndTimes("Scene", s.Revision, s.CreatedAt, s.UpdatedAt, s.DeletedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScene, err)
	}
	return nil
}

func (s Scene) IsDeleted() bool { return s.DeletedAt != nil }

type Clip struct {
	ID                     ClipID
	ProjectID              ProjectID
	EpisodeID              EpisodeID
	SceneID                SceneID
	Position               int // sparse sort key within Scene
	Title                  string
	Description            string
	Dialogue               string
	CreativeIntent         string
	PlannedDurationSeconds int64
	FirstImagePrompt       string
	LastImagePrompt        string
	VideoPrompts           ClipVideoPrompts
	GenerationMode         ClipGenerationMode
	ContinueFromPrevious   bool
	Revision               int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
	DeletedAt              *time.Time
	DeletedWithSceneID     SceneID
}

func (c Clip) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if err := c.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if err := c.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if err := c.SceneID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if c.Position < 1 {
		return fmt.Errorf("%w: position must be at least 1", ErrInvalidClip)
	}
	if err := validateNamedTitle("title", c.Title, MaxClipTitleCharacters); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	texts := []struct{ name, value string }{
		{"description", c.Description}, {"dialogue", c.Dialogue}, {"creative intent", c.CreativeIntent},
		{"first image prompt", c.FirstImagePrompt}, {"last image prompt", c.LastImagePrompt},
		{"first-image video prompt", c.VideoPrompts.FirstImage}, {"first-last-image video prompt", c.VideoPrompts.FirstLastImage},
		{"reference video prompt", c.VideoPrompts.Reference},
	}
	for _, text := range texts {
		if err := validateStoryboardText(text.name, text.value); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidClip, err)
		}
	}
	if c.PlannedDurationSeconds < 1 || c.PlannedDurationSeconds > MaxPlannedDurationSeconds {
		return fmt.Errorf("%w: planned duration is outside the supported whole-second range", ErrInvalidClip)
	}
	if !c.GenerationMode.IsValid() {
		return fmt.Errorf("%w: %w", ErrInvalidClip, ErrInvalidClipGenerationMode)
	}
	if c.DeletedWithSceneID != "" {
		if err := c.DeletedWithSceneID.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidClip, err)
		}
		if c.DeletedAt == nil || c.DeletedWithSceneID != c.SceneID {
			return fmt.Errorf("%w: Scene deletion marker is inconsistent", ErrInvalidClip)
		}
	}
	if err := validateRevisionAndTimes("Clip", c.Revision, c.CreatedAt, c.UpdatedAt, c.DeletedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	return nil
}

func (c Clip) IsDeleted() bool { return c.DeletedAt != nil }

type ClipAssetReference struct {
	ClipID          ClipID
	AssetID         AssetID
	AssetIdentityID AssetIdentityID
	Kind            AssetKind
	Name            string
	AssetDeleted    bool
	Position        int
}

func (r ClipAssetReference) Validate() error {
	if err := r.ClipID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if err := r.AssetID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if err := r.AssetIdentityID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if !r.Kind.IsValid() {
		return fmt.Errorf("%w: %w", ErrInvalidClip, ErrInvalidAssetKind)
	}
	if err := validateNamedTitle("Asset reference name", r.Name, MaxAssetIdentityNameCharacters); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClip, err)
	}
	if r.Position < 1 {
		return fmt.Errorf("%w: Asset reference position must be at least 1", ErrInvalidClip)
	}
	return nil
}

type ClipDetails struct {
	Clip               Clip
	AssetReferences    []ClipAssetReference
	DerivedFromClipIDs []ClipID
}

// ClipReplacement is the complete new object written by one atomic
// Split/Merge operation. Asset bindings and source audit are explicit; media
// versions are never copied implicitly.
type ClipReplacement struct {
	Clip          Clip
	AssetIDs      []AssetID
	SourceClipIDs []ClipID
}

func (r ClipReplacement) Validate() error {
	if err := r.Clip.Validate(); err != nil || r.Clip.Revision != 1 || r.Clip.IsDeleted() {
		return fmt.Errorf("%w: invalid replacement Clip", ErrInvalidClip)
	}
	assets := make(map[AssetID]struct{}, len(r.AssetIDs))
	for _, id := range r.AssetIDs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%w: invalid replacement Asset", ErrInvalidClip)
		}
		if _, exists := assets[id]; exists {
			return fmt.Errorf("%w: duplicate replacement Asset", ErrInvalidClip)
		}
		assets[id] = struct{}{}
	}
	sources := make(map[ClipID]struct{}, len(r.SourceClipIDs))
	if len(r.SourceClipIDs) == 0 {
		return fmt.Errorf("%w: replacement source is required", ErrInvalidClip)
	}
	for _, id := range r.SourceClipIDs {
		if err := id.Validate(); err != nil || id == r.Clip.ID {
			return fmt.Errorf("%w: invalid replacement source", ErrInvalidClip)
		}
		if _, exists := sources[id]; exists {
			return fmt.Errorf("%w: duplicate replacement source", ErrInvalidClip)
		}
		sources[id] = struct{}{}
	}
	return nil
}

func (c ClipDetails) Validate() error {
	if err := c.Clip.Validate(); err != nil {
		return err
	}
	seen := make(map[AssetID]struct{}, len(c.AssetReferences))
	for index, reference := range c.AssetReferences {
		if err := reference.Validate(); err != nil {
			return err
		}
		if reference.ClipID != c.Clip.ID || reference.Position != index+1 {
			return fmt.Errorf("%w: Asset references are not contiguous or owned by the Clip", ErrInvalidClip)
		}
		if _, ok := seen[reference.AssetID]; ok {
			return fmt.Errorf("%w: duplicate Asset reference", ErrInvalidClip)
		}
		seen[reference.AssetID] = struct{}{}
	}
	derived := make(map[ClipID]struct{}, len(c.DerivedFromClipIDs))
	for _, id := range c.DerivedFromClipIDs {
		if err := id.Validate(); err != nil || id == c.Clip.ID {
			return fmt.Errorf("%w: invalid source Clip", ErrInvalidClip)
		}
		if _, exists := derived[id]; exists {
			return fmt.Errorf("%w: duplicate source Clip", ErrInvalidClip)
		}
		derived[id] = struct{}{}
	}
	return nil
}

type StoryboardScene struct {
	Scene Scene
	Clips []ClipDetails
}

type Storyboard struct {
	EpisodeID              EpisodeID
	Revision               int64
	PlannedDurationSeconds int64
	Scenes                 []StoryboardScene
}

func (s Storyboard) Validate() error {
	if err := s.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStoryboard, err)
	}
	if s.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidStoryboard)
	}
	var duration int64
	previousScenePosition := 0
	for _, scene := range s.Scenes {
		if err := scene.Scene.Validate(); err != nil {
			return err
		}
		if scene.Scene.IsDeleted() || scene.Scene.EpisodeID != s.EpisodeID || scene.Scene.Position <= previousScenePosition {
			return fmt.Errorf("%w: Scenes are not active, strictly ordered, or owned by the Episode", ErrInvalidStoryboard)
		}
		previousScenePosition = scene.Scene.Position
		previousClipPosition := 0
		for _, clip := range scene.Clips {
			if err := clip.Validate(); err != nil {
				return err
			}
			if clip.Clip.IsDeleted() || clip.Clip.SceneID != scene.Scene.ID || clip.Clip.Position <= previousClipPosition {
				return fmt.Errorf("%w: Clips are not active, strictly ordered, or owned by the Scene", ErrInvalidStoryboard)
			}
			previousClipPosition = clip.Clip.Position
			if duration > math.MaxInt64-clip.Clip.PlannedDurationSeconds {
				return fmt.Errorf("%w: planned duration overflow", ErrInvalidStoryboard)
			}
			duration += clip.Clip.PlannedDurationSeconds
		}
	}
	if duration != s.PlannedDurationSeconds {
		return fmt.Errorf("%w: planned duration does not match Clips", ErrInvalidStoryboard)
	}
	return nil
}

func NormalizeStoryboardText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func validateNamedTitle(name, value string, maximum int) error {
	if err := validateSingleLine(name, value); err != nil {
		return err
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s exceeds %d characters", name, maximum)
	}
	return nil
}

func validateStoryboardText(name, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.ContainsRune(value, '\r') {
		return fmt.Errorf("%s is not normalized UTF-8 text", name)
	}
	if utf8.RuneCountInString(value) > MaxStoryboardTextCharacters {
		return fmt.Errorf("%s exceeds %d characters", name, MaxStoryboardTextCharacters)
	}
	return nil
}

// ValidateClipPrompt checks the shared durable text constraints used by all
// three Clip prompt fields without requiring callers to construct a Clip.
func ValidateClipPrompt(value string) error {
	return validateStoryboardText("Clip prompt", value)
}

func validateRevisionAndTimes(name string, revision int64, createdAt, updatedAt time.Time, deletedAt *time.Time) error {
	if revision < 1 {
		return fmt.Errorf("%s revision must be at least 1", name)
	}
	if err := validateTimestamp("created at", createdAt); err != nil {
		return err
	}
	if err := validateTimestamp("updated at", updatedAt); err != nil {
		return err
	}
	if updatedAt.Before(createdAt) {
		return fmt.Errorf("updated at precedes created at")
	}
	if deletedAt != nil {
		if err := validateTimestamp("deleted at", *deletedAt); err != nil {
			return err
		}
		if deletedAt.Before(createdAt) {
			return fmt.Errorf("deleted at precedes created at")
		}
	}
	return nil
}
