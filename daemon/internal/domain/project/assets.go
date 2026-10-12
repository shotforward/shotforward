package project

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxAssetIdentityNameCharacters   = 100
	MaxAssetPromptCharacters         = 20_000
	MaxEditableAssetPromptCharacters = 2_000
)

var (
	ErrInvalidAsset         = errors.New("invalid asset")
	ErrInvalidAssetIdentity = errors.New("invalid asset identity")
	ErrInvalidAssetKind     = errors.New("invalid asset kind")
)

// AssetKind is the narrative role of an Asset Identity.
type AssetKind string

const (
	AssetKindCharacter AssetKind = "character"
	AssetKindLocation  AssetKind = "location"
	AssetKindProp      AssetKind = "prop"
	AssetKindStyle     AssetKind = "style"
)

func (k AssetKind) IsValid() bool {
	switch k {
	case AssetKindCharacter, AssetKindLocation, AssetKindProp, AssetKindStyle:
		return true
	default:
		return false
	}
}

// AssetIdentity contains the project-wide name shared by one narrative
// identity across Episodes. It is not an editable project-level Asset.
type AssetIdentity struct {
	ID        AssetIdentityID
	ProjectID ProjectID
	Kind      AssetKind
	Name      string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (i AssetIdentity) Validate() error {
	if err := i.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, err)
	}
	if err := i.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, err)
	}
	if !i.Kind.IsValid() {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, ErrInvalidAssetKind)
	}
	if err := ValidateAssetIdentityName(i.Name); err != nil {
		return err
	}
	if i.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidAssetIdentity)
	}
	if err := validateTimestamp("created at", i.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, err)
	}
	if err := validateTimestamp("updated at", i.UpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, err)
	}
	if i.UpdatedAt.Before(i.CreatedAt) {
		return fmt.Errorf("%w: updated at precedes created at", ErrInvalidAssetIdentity)
	}
	return nil
}

func ValidateAssetIdentityName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: name must be valid UTF-8", ErrInvalidAssetIdentity)
	}
	if err := validateSingleLine("name", name); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAssetIdentity, err)
	}
	if utf8.RuneCountInString(name) > MaxAssetIdentityNameCharacters {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalidAssetIdentity, MaxAssetIdentityNameCharacters)
	}
	return nil
}

// Asset contains the Episode-specific prompt and provenance for one shared
// narrative identity. Media artifacts are modeled separately.
type Asset struct {
	ID                 AssetID
	ProjectID          ProjectID
	EpisodeID          EpisodeID
	IdentityID         AssetIdentityID
	DerivedFromAssetID AssetID
	Prompt             string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

func (a Asset) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if err := a.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if err := a.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if err := a.IdentityID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if a.DerivedFromAssetID != "" {
		if err := a.DerivedFromAssetID.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
		}
		if a.DerivedFromAssetID == a.ID {
			return fmt.Errorf("%w: an Asset cannot derive from itself", ErrInvalidAsset)
		}
	}
	if err := ValidateAssetPrompt(a.Prompt); err != nil {
		return err
	}
	if a.Revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidAsset)
	}
	if err := validateTimestamp("created at", a.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if err := validateTimestamp("updated at", a.UpdatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	if a.UpdatedAt.Before(a.CreatedAt) {
		return fmt.Errorf("%w: updated at precedes created at", ErrInvalidAsset)
	}
	if a.DeletedAt != nil {
		if err := validateTimestamp("deleted at", *a.DeletedAt); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidAsset, err)
		}
		if a.DeletedAt.Before(a.CreatedAt) {
			return fmt.Errorf("%w: deleted at precedes created at", ErrInvalidAsset)
		}
	}
	return nil
}

func (a Asset) IsDeleted() bool {
	return a.DeletedAt != nil
}

// EpisodeAsset is the read model used by Asset operations.
type EpisodeAsset struct {
	Identity AssetIdentity
	Asset    Asset
}

func (a EpisodeAsset) Validate() error {
	if err := a.Identity.Validate(); err != nil {
		return err
	}
	if err := a.Asset.Validate(); err != nil {
		return err
	}
	if a.Identity.ID != a.Asset.IdentityID || a.Identity.ProjectID != a.Asset.ProjectID {
		return fmt.Errorf("%w: identity and Asset ownership do not match", ErrInvalidAsset)
	}
	return nil
}

// ValidateAssetPrompt checks durable prompt invariants. An empty prompt is
// valid for an extracted Asset, but media generation may impose a non-empty
// requirement later.
func ValidateAssetPrompt(prompt string) error {
	if !utf8.ValidString(prompt) {
		return fmt.Errorf("%w: prompt must be valid UTF-8", ErrInvalidAsset)
	}
	if strings.ContainsRune(prompt, 0) {
		return fmt.Errorf("%w: prompt contains a null byte", ErrInvalidAsset)
	}
	if strings.ContainsRune(prompt, '\r') {
		return fmt.Errorf("%w: prompt must use normalized LF line endings", ErrInvalidAsset)
	}
	if utf8.RuneCountInString(prompt) > MaxAssetPromptCharacters {
		return fmt.Errorf("%w: prompt exceeds %d characters", ErrInvalidAsset, MaxAssetPromptCharacters)
	}
	return nil
}

func NormalizeAssetPrompt(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	characters := []rune(value)
	if len(characters) > MaxEditableAssetPromptCharacters {
		value = string(characters[:MaxEditableAssetPromptCharacters])
	}
	return value
}
