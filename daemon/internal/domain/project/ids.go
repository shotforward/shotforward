package project

import (
	"errors"
	"fmt"
	"strings"
)

const minimumRandomIDSuffixLength = 26

var (
	ErrInvalidProjectID         = errors.New("invalid project ID")
	ErrInvalidEpisodeID         = errors.New("invalid episode ID")
	ErrInvalidAssetID           = errors.New("invalid asset ID")
	ErrInvalidAssetIdentityID   = errors.New("invalid asset identity ID")
	ErrInvalidSceneID           = errors.New("invalid scene ID")
	ErrInvalidClipID            = errors.New("invalid clip ID")
	ErrInvalidArtifactID        = errors.New("invalid artifact ID")
	ErrInvalidMediaSlotID       = errors.New("invalid media slot ID")
	ErrInvalidMediaVersionID    = errors.New("invalid media version ID")
	ErrInvalidAudioAssetID      = errors.New("invalid audio reference asset ID")
	ErrInvalidGenerationBatchID = errors.New("invalid generation batch ID")
	ErrInvalidGenerationJobID   = errors.New("invalid generation job ID")
	ErrInvalidExportJobID       = errors.New("invalid export job ID")
)

// ProjectID is the stable identity of one portable ShotForward project.
type ProjectID string

// ParseProjectID validates an externally supplied Project ID.
func ParseProjectID(value string) (ProjectID, error) {
	if err := validateTypedID(value, "prj_", ErrInvalidProjectID); err != nil {
		return "", err
	}
	return ProjectID(value), nil
}

func (id ProjectID) String() string {
	return string(id)
}

func (id ProjectID) Validate() error {
	return validateTypedID(string(id), "prj_", ErrInvalidProjectID)
}

// EpisodeID is the stable identity of one Episode inside a project.
type EpisodeID string

// ParseEpisodeID validates an externally supplied Episode ID.
func ParseEpisodeID(value string) (EpisodeID, error) {
	if err := validateTypedID(value, "ep_", ErrInvalidEpisodeID); err != nil {
		return "", err
	}
	return EpisodeID(value), nil
}

func (id EpisodeID) String() string {
	return string(id)
}

func (id EpisodeID) Validate() error {
	return validateTypedID(string(id), "ep_", ErrInvalidEpisodeID)
}

// AssetID is the stable identity of one Episode-owned Asset.
type AssetID string

// ParseAssetID validates an externally supplied Asset ID.
func ParseAssetID(value string) (AssetID, error) {
	if err := validateTypedID(value, "ast_", ErrInvalidAssetID); err != nil {
		return "", err
	}
	return AssetID(value), nil
}

func (id AssetID) String() string {
	return string(id)
}

func (id AssetID) Validate() error {
	return validateTypedID(string(id), "ast_", ErrInvalidAssetID)
}

// AssetIdentityID links the same narrative identity across Episodes.
type AssetIdentityID string

// ParseAssetIdentityID validates an externally supplied Asset Identity ID.
func ParseAssetIdentityID(value string) (AssetIdentityID, error) {
	if err := validateTypedID(value, "asi_", ErrInvalidAssetIdentityID); err != nil {
		return "", err
	}
	return AssetIdentityID(value), nil
}

func (id AssetIdentityID) String() string {
	return string(id)
}

func (id AssetIdentityID) Validate() error {
	return validateTypedID(string(id), "asi_", ErrInvalidAssetIdentityID)
}

// SceneID is the stable identity of one Scene inside an Episode.
type SceneID string

func ParseSceneID(value string) (SceneID, error) {
	if err := validateTypedID(value, "scn_", ErrInvalidSceneID); err != nil {
		return "", err
	}
	return SceneID(value), nil
}

func (id SceneID) String() string { return string(id) }

func (id SceneID) Validate() error {
	return validateTypedID(string(id), "scn_", ErrInvalidSceneID)
}

// ClipID is the stable identity of one Clip inside a Scene.
type ClipID string

func ParseClipID(value string) (ClipID, error) {
	if err := validateTypedID(value, "clp_", ErrInvalidClipID); err != nil {
		return "", err
	}
	return ClipID(value), nil
}

func (id ClipID) String() string { return string(id) }

func (id ClipID) Validate() error {
	return validateTypedID(string(id), "clp_", ErrInvalidClipID)
}

type ArtifactID string

func ParseArtifactID(value string) (ArtifactID, error) {
	if err := validateTypedID(value, "art_", ErrInvalidArtifactID); err != nil {
		return "", err
	}
	return ArtifactID(value), nil
}
func (id ArtifactID) String() string { return string(id) }
func (id ArtifactID) Validate() error {
	return validateTypedID(string(id), "art_", ErrInvalidArtifactID)
}

type MediaSlotID string

func ParseMediaSlotID(value string) (MediaSlotID, error) {
	if err := validateTypedID(value, "msl_", ErrInvalidMediaSlotID); err != nil {
		return "", err
	}
	return MediaSlotID(value), nil
}
func (id MediaSlotID) String() string { return string(id) }
func (id MediaSlotID) Validate() error {
	return validateTypedID(string(id), "msl_", ErrInvalidMediaSlotID)
}

type MediaVersionID string

func ParseMediaVersionID(value string) (MediaVersionID, error) {
	if err := validateTypedID(value, "mvr_", ErrInvalidMediaVersionID); err != nil {
		return "", err
	}
	return MediaVersionID(value), nil
}
func (id MediaVersionID) String() string { return string(id) }
func (id MediaVersionID) Validate() error {
	return validateTypedID(string(id), "mvr_", ErrInvalidMediaVersionID)
}

type AudioReferenceAssetID string

func ParseAudioReferenceAssetID(value string) (AudioReferenceAssetID, error) {
	if err := validateTypedID(value, "aud_", ErrInvalidAudioAssetID); err != nil {
		return "", err
	}
	return AudioReferenceAssetID(value), nil
}
func (id AudioReferenceAssetID) String() string { return string(id) }
func (id AudioReferenceAssetID) Validate() error {
	return validateTypedID(string(id), "aud_", ErrInvalidAudioAssetID)
}

type GenerationBatchID string

func ParseGenerationBatchID(value string) (GenerationBatchID, error) {
	if err := validateTypedID(value, "gbt_", ErrInvalidGenerationBatchID); err != nil {
		return "", err
	}
	return GenerationBatchID(value), nil
}
func (id GenerationBatchID) String() string { return string(id) }
func (id GenerationBatchID) Validate() error {
	return validateTypedID(string(id), "gbt_", ErrInvalidGenerationBatchID)
}

type GenerationJobID string

func ParseGenerationJobID(value string) (GenerationJobID, error) {
	if err := validateTypedID(value, "gjb_", ErrInvalidGenerationJobID); err != nil {
		return "", err
	}
	return GenerationJobID(value), nil
}
func (id GenerationJobID) String() string { return string(id) }
func (id GenerationJobID) Validate() error {
	return validateTypedID(string(id), "gjb_", ErrInvalidGenerationJobID)
}

type ExportJobID string

func ParseExportJobID(value string) (ExportJobID, error) {
	if err := validateTypedID(value, "exj_", ErrInvalidExportJobID); err != nil {
		return "", err
	}
	return ExportJobID(value), nil
}
func (id ExportJobID) String() string { return string(id) }
func (id ExportJobID) Validate() error {
	return validateTypedID(string(id), "exj_", ErrInvalidExportJobID)
}

func validateTypedID(value, prefix string, category error) error {
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%w: expected prefix %q", category, prefix)
	}
	suffix := strings.TrimPrefix(value, prefix)
	if len(suffix) < minimumRandomIDSuffixLength {
		return fmt.Errorf("%w: random suffix is too short", category)
	}
	for _, character := range suffix {
		if (character >= 'A' && character <= 'Z') || (character >= '2' && character <= '7') {
			continue
		}
		return fmt.Errorf("%w: random suffix must use RFC 4648 base32", category)
	}
	return nil
}
