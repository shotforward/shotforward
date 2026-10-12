package project

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

var (
	ErrInvalidArtifact               = errors.New("invalid artifact")
	ErrInvalidAgentConversationMedia = errors.New("invalid Agent conversation media")
	ErrInvalidEpisodeReferenceMedia  = errors.New("invalid episode reference media")
	ErrInvalidMediaSlot              = errors.New("invalid media slot")
	ErrInvalidMediaVersion           = errors.New("invalid media version")
	ErrInvalidAudioReferenceAsset    = errors.New("invalid audio reference asset")
)

type AgentConversationMediaOrigin string

const (
	AgentConversationMediaUploaded  AgentConversationMediaOrigin = "uploaded"
	AgentConversationMediaAsset     AgentConversationMediaOrigin = "asset"
	AgentConversationMediaGenerated AgentConversationMediaOrigin = "generated"
)

// AgentConversationMedia keeps an Artifact in a conversation without adding it
// to the Episode Asset or reference-media libraries. Session and Turn IDs are
// intentionally opaque here because those records live in the Studio database.
type AgentConversationMedia struct {
	EpisodeID         EpisodeID
	SessionID         string
	ClientOperationID string
	TurnID            string
	Artifact          Artifact
	Name              string
	Origin            AgentConversationMediaOrigin
	CreatedAt         time.Time
}

func (m AgentConversationMedia) Validate() error {
	if m.EpisodeID.Validate() != nil || m.Artifact.Validate() != nil ||
		!validConversationMediaText(m.SessionID, 200) ||
		!validConversationMediaText(m.Name, 200) ||
		(m.ClientOperationID == "" && m.TurnID == "") ||
		(m.ClientOperationID != "" && !validConversationMediaText(m.ClientOperationID, 200)) ||
		(m.TurnID != "" && !validConversationMediaText(m.TurnID, 200)) ||
		(m.Origin != AgentConversationMediaUploaded && m.Origin != AgentConversationMediaAsset && m.Origin != AgentConversationMediaGenerated) ||
		!m.CreatedAt.Equal(m.Artifact.CreatedAt) && m.CreatedAt.Before(m.Artifact.CreatedAt) {
		return ErrInvalidAgentConversationMedia
	}
	return nil
}

func validConversationMediaText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]rune(value)) <= maximum && !strings.ContainsAny(value, "\r\n\x00")
}

// EpisodeReferenceMedia gives an immutable Artifact an Episode-scoped,
// user-editable library identity. The same Artifact may be referenced by many
// Clips without duplicating the physical file.
type EpisodeReferenceMedia struct {
	EpisodeID             EpisodeID
	Artifact              Artifact
	Name                  string
	Description           string
	TrimStartMilliseconds int64
	TrimEndMilliseconds   int64
	UsageCount            int
	Revision              int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	DeletedAt             *time.Time
}

// EffectiveTrimRange resolves the legacy/full-source 0/0 selection without
// changing the immutable Artifact's duration. Images have no timed selection.
func (m EpisodeReferenceMedia) EffectiveTrimRange() (int64, int64) {
	if m.Artifact.Kind == MediaKindImage {
		return 0, 0
	}
	if m.TrimStartMilliseconds == 0 && m.TrimEndMilliseconds == 0 {
		return 0, m.Artifact.DurationMilliseconds
	}
	return m.TrimStartMilliseconds, m.TrimEndMilliseconds
}

func (m EpisodeReferenceMedia) EffectiveDurationMilliseconds() int64 {
	start, end := m.EffectiveTrimRange()
	return end - start
}

func (m EpisodeReferenceMedia) Validate() error {
	if err := m.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisodeReferenceMedia, err)
	}
	if err := m.Artifact.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisodeReferenceMedia, err)
	}
	if m.Artifact.Kind == MediaKindImage {
		if m.TrimStartMilliseconds != 0 || m.TrimEndMilliseconds != 0 {
			return fmt.Errorf("%w: images cannot have a trim range", ErrInvalidEpisodeReferenceMedia)
		}
	} else if m.TrimStartMilliseconds != 0 || m.TrimEndMilliseconds != 0 {
		if m.TrimStartMilliseconds < 0 || m.TrimEndMilliseconds <= m.TrimStartMilliseconds || m.TrimEndMilliseconds > m.Artifact.DurationMilliseconds {
			return fmt.Errorf("%w: trim range must be within the original duration", ErrInvalidEpisodeReferenceMedia)
		}
	}
	name := strings.TrimSpace(m.Name)
	if name == "" || name != m.Name || len([]rune(name)) > 40 || strings.ContainsAny(name, "\r\n\x00@") {
		return fmt.Errorf("%w: invalid name", ErrInvalidEpisodeReferenceMedia)
	}
	if len([]rune(m.Description)) > 2000 || strings.ContainsRune(m.Description, '\x00') {
		return fmt.Errorf("%w: invalid description", ErrInvalidEpisodeReferenceMedia)
	}
	if m.UsageCount < 0 || m.Revision < 1 {
		return fmt.Errorf("%w: invalid usage or revision", ErrInvalidEpisodeReferenceMedia)
	}
	if err := validateTimestamp("created at", m.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEpisodeReferenceMedia, err)
	}
	if err := validateTimestamp("updated at", m.UpdatedAt); err != nil || m.UpdatedAt.Before(m.CreatedAt) {
		return fmt.Errorf("%w: invalid updated at", ErrInvalidEpisodeReferenceMedia)
	}
	if m.DeletedAt != nil && m.DeletedAt.Before(m.CreatedAt) {
		return fmt.Errorf("%w: invalid deleted at", ErrInvalidEpisodeReferenceMedia)
	}
	return nil
}

type MediaKind string

const (
	MediaKindImage MediaKind = "image"
	MediaKindVideo MediaKind = "video"
	MediaKindAudio MediaKind = "audio"
)

func (k MediaKind) IsValid() bool {
	return k == MediaKindImage || k == MediaKindVideo || k == MediaKindAudio
}

type MediaSlotKind string

const (
	MediaSlotAssetPrimaryImage MediaSlotKind = "primary_image"
	MediaSlotClipFirstImage    MediaSlotKind = "first_image"
	MediaSlotClipLastImage     MediaSlotKind = "last_image"
	MediaSlotClipVideo         MediaSlotKind = "video"
	MediaSlotAudioPrimary      MediaSlotKind = "primary_audio"
)

func (k MediaSlotKind) IsValid() bool {
	switch k {
	case MediaSlotAssetPrimaryImage, MediaSlotClipFirstImage, MediaSlotClipLastImage, MediaSlotClipVideo, MediaSlotAudioPrimary:
		return true
	}
	return false
}
func (k MediaSlotKind) MediaKind() MediaKind {
	switch k {
	case MediaSlotAssetPrimaryImage, MediaSlotClipFirstImage, MediaSlotClipLastImage:
		return MediaKindImage
	case MediaSlotClipVideo:
		return MediaKindVideo
	case MediaSlotAudioPrimary:
		return MediaKindAudio
	}
	return ""
}

type MediaOwnerKind string

const (
	MediaOwnerAsset          MediaOwnerKind = "asset"
	MediaOwnerClip           MediaOwnerKind = "clip"
	MediaOwnerAudioReference MediaOwnerKind = "audio_reference_asset"
)

func (k MediaOwnerKind) IsValid() bool {
	return k == MediaOwnerAsset || k == MediaOwnerClip || k == MediaOwnerAudioReference
}

type Artifact struct {
	ID                       ArtifactID
	ProjectID                ProjectID
	Kind                     MediaKind
	RelativePath             string
	SHA256                   string
	ByteSize                 int64
	MIMEType                 string
	OriginalFilename         string
	Width                    int
	Height                   int
	DurationMilliseconds     int64
	FPSNumerator             int64
	FPSDenominator           int64
	AudioSampleRate          int
	AudioChannels            int
	RemoteReferenceURL       string
	RemoteReferenceExpiresAt *time.Time
	CreatedAt                time.Time
}

func (a Artifact) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArtifact, err)
	}
	if err := a.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArtifact, err)
	}
	if !a.Kind.IsValid() {
		return fmt.Errorf("%w: invalid media kind", ErrInvalidArtifact)
	}
	if a.RelativePath == "" || path.IsAbs(a.RelativePath) || path.Clean(a.RelativePath) != a.RelativePath || strings.HasPrefix(a.RelativePath, "../") || strings.Contains(a.RelativePath, "\\") {
		return fmt.Errorf("%w: invalid relative path", ErrInvalidArtifact)
	}
	if len(a.SHA256) != 64 {
		return fmt.Errorf("%w: SHA-256 must have 64 hexadecimal characters", ErrInvalidArtifact)
	}
	for _, r := range a.SHA256 {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return fmt.Errorf("%w: invalid SHA-256", ErrInvalidArtifact)
		}
	}
	if a.ByteSize < 1 || strings.TrimSpace(a.MIMEType) == "" || strings.ContainsAny(a.MIMEType, "\r\n\x00") {
		return fmt.Errorf("%w: invalid file metadata", ErrInvalidArtifact)
	}
	if a.OriginalFilename == "" || a.OriginalFilename != path.Base(strings.ReplaceAll(a.OriginalFilename, "\\", "/")) {
		return fmt.Errorf("%w: invalid original filename", ErrInvalidArtifact)
	}
	if a.RemoteReferenceURL == "" {
		if a.RemoteReferenceExpiresAt != nil {
			return fmt.Errorf("%w: invalid remote reference", ErrInvalidArtifact)
		}
	} else if a.RemoteReferenceExpiresAt == nil || a.RemoteReferenceExpiresAt.IsZero() || len(a.RemoteReferenceURL) > 8_192 || strings.TrimSpace(a.RemoteReferenceURL) != a.RemoteReferenceURL || !strings.HasPrefix(a.RemoteReferenceURL, "https://") {
		return fmt.Errorf("%w: invalid remote reference", ErrInvalidArtifact)
	}
	switch a.Kind {
	case MediaKindImage:
		if a.Width < 1 || a.Height < 1 || a.DurationMilliseconds != 0 {
			return fmt.Errorf("%w: invalid image metadata", ErrInvalidArtifact)
		}
	case MediaKindVideo:
		if a.Width < 1 || a.Height < 1 || a.DurationMilliseconds < 1 || a.FPSNumerator < 1 || a.FPSDenominator < 1 {
			return fmt.Errorf("%w: invalid video metadata", ErrInvalidArtifact)
		}
	case MediaKindAudio:
		if a.DurationMilliseconds < 1 || a.AudioSampleRate < 1 || a.AudioChannels < 1 {
			return fmt.Errorf("%w: invalid audio metadata", ErrInvalidArtifact)
		}
	}
	if err := validateTimestamp("created at", a.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArtifact, err)
	}
	return nil
}

type MediaSlot struct {
	ID               MediaSlotID
	ProjectID        ProjectID
	EpisodeID        EpisodeID
	OwnerKind        MediaOwnerKind
	OwnerID          string
	Kind             MediaSlotKind
	CurrentVersionID MediaVersionID
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (s MediaSlot) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaSlot, err)
	}
	if err := s.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaSlot, err)
	}
	if err := s.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaSlot, err)
	}
	if !s.OwnerKind.IsValid() || s.OwnerID == "" || !s.Kind.IsValid() {
		return fmt.Errorf("%w: invalid owner or kind", ErrInvalidMediaSlot)
	}
	if err := validateMediaSlotOwner(s.OwnerKind, s.OwnerID, s.Kind); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaSlot, err)
	}
	expectedID, err := FixedMediaSlotID(s.OwnerID, s.Kind)
	if err != nil || expectedID != s.ID {
		return fmt.Errorf("%w: slot ID does not match owner", ErrInvalidMediaSlot)
	}
	if s.CurrentVersionID != "" {
		if err := s.CurrentVersionID.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidMediaSlot, err)
		}
	}
	return validateMediaRevisionTimes(ErrInvalidMediaSlot, s.Revision, s.CreatedAt, s.UpdatedAt, nil)
}

type MediaVersion struct {
	ID                  MediaVersionID
	SlotID              MediaSlotID
	ArtifactID          ArtifactID
	SequenceNumber      int
	TrimInMilliseconds  int64
	TrimOutMilliseconds int64
	Revision            int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

func (v MediaVersion) Validate() error {
	if err := v.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaVersion, err)
	}
	if err := v.SlotID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaVersion, err)
	}
	if err := v.ArtifactID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMediaVersion, err)
	}
	if v.SequenceNumber < 1 || v.TrimInMilliseconds < 0 || v.TrimOutMilliseconds < 0 {
		return fmt.Errorf("%w: invalid sequence or trim", ErrInvalidMediaVersion)
	}
	return validateMediaRevisionTimes(ErrInvalidMediaVersion, v.Revision, v.CreatedAt, v.UpdatedAt, v.DeletedAt)
}
func (v MediaVersion) IsDeleted() bool { return v.DeletedAt != nil }

type MediaSlotDetails struct {
	Slot      MediaSlot
	Current   *MediaVersion
	Versions  []MediaVersion
	Artifacts map[ArtifactID]Artifact
}

func (s MediaSlotDetails) Validate() error {
	if err := s.Slot.Validate(); err != nil {
		return err
	}
	seen := map[MediaVersionID]struct{}{}
	for _, v := range s.Versions {
		if err := v.Validate(); err != nil {
			return err
		}
		if v.SlotID != s.Slot.ID {
			return fmt.Errorf("%w: version belongs to another slot", ErrInvalidMediaSlot)
		}
		if _, ok := s.Artifacts[v.ArtifactID]; !ok {
			return fmt.Errorf("%w: missing Artifact", ErrInvalidMediaSlot)
		}
		seen[v.ID] = struct{}{}
	}
	if s.Slot.CurrentVersionID == "" {
		if s.Current != nil {
			return fmt.Errorf("%w: unexpected current version", ErrInvalidMediaSlot)
		}
	} else {
		if s.Current == nil || s.Current.ID != s.Slot.CurrentVersionID || s.Current.IsDeleted() {
			return fmt.Errorf("%w: invalid current version", ErrInvalidMediaSlot)
		}
		if _, ok := seen[s.Current.ID]; !ok {
			return fmt.Errorf("%w: current version missing from history", ErrInvalidMediaSlot)
		}
	}
	return nil
}

type AudioReferenceAsset struct {
	ID               AudioReferenceAssetID
	ProjectID        ProjectID
	EpisodeID        EpisodeID
	Name             string
	CharacterAssetID AssetID
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

func (a AudioReferenceAsset) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAudioReferenceAsset, err)
	}
	if err := a.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAudioReferenceAsset, err)
	}
	if err := a.EpisodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAudioReferenceAsset, err)
	}
	if err := validateNamedTitle("name", a.Name, 200); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAudioReferenceAsset, err)
	}
	if a.CharacterAssetID != "" {
		if err := a.CharacterAssetID.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidAudioReferenceAsset, err)
		}
	}
	return validateMediaRevisionTimes(ErrInvalidAudioReferenceAsset, a.Revision, a.CreatedAt, a.UpdatedAt, a.DeletedAt)
}
func (a AudioReferenceAsset) IsDeleted() bool { return a.DeletedAt != nil }

func FixedMediaSlotID(ownerID string, kind MediaSlotKind) (MediaSlotID, error) {
	if err := validateMediaSlotOwnerForKind(ownerID, kind); err != nil {
		return "", ErrInvalidMediaSlotID
	}
	parts := strings.SplitN(ownerID, "_", 2)
	if len(parts) != 2 {
		return "", ErrInvalidMediaSlotID
	}
	code := ""
	switch kind {
	case MediaSlotAssetPrimaryImage:
		code = "A"
	case MediaSlotClipFirstImage:
		code = "B"
	case MediaSlotClipLastImage:
		code = "C"
	case MediaSlotClipVideo:
		code = "D"
	case MediaSlotAudioPrimary:
		if ClipID(ownerID).Validate() == nil {
			code = "F"
		} else {
			code = "E"
		}
	default:
		return "", ErrInvalidMediaSlotID
	}
	return ParseMediaSlotID("msl_" + code + parts[1])
}

func validateMediaSlotOwner(ownerKind MediaOwnerKind, ownerID string, kind MediaSlotKind) error {
	switch ownerKind {
	case MediaOwnerAsset:
		if kind != MediaSlotAssetPrimaryImage {
			return errors.New("Asset only supports primary image")
		}
		return AssetID(ownerID).Validate()
	case MediaOwnerClip:
		if kind != MediaSlotClipFirstImage && kind != MediaSlotClipLastImage && kind != MediaSlotClipVideo && kind != MediaSlotAudioPrimary {
			return errors.New("Clip only supports first image, last image, video, and reference audio")
		}
		return ClipID(ownerID).Validate()
	case MediaOwnerAudioReference:
		if kind != MediaSlotAudioPrimary {
			return errors.New("AudioReferenceAsset only supports primary audio")
		}
		return AudioReferenceAssetID(ownerID).Validate()
	default:
		return errors.New("invalid media owner")
	}
}

func validateMediaSlotOwnerForKind(ownerID string, kind MediaSlotKind) error {
	switch kind {
	case MediaSlotAssetPrimaryImage:
		return AssetID(ownerID).Validate()
	case MediaSlotClipFirstImage, MediaSlotClipLastImage, MediaSlotClipVideo:
		return ClipID(ownerID).Validate()
	case MediaSlotAudioPrimary:
		if ClipID(ownerID).Validate() == nil {
			return nil
		}
		return AudioReferenceAssetID(ownerID).Validate()
	default:
		return ErrInvalidMediaSlotID
	}
}
func validateMediaRevisionTimes(category error, revision int64, createdAt, updatedAt time.Time, deletedAt *time.Time) error {
	if revision < 1 {
		return fmt.Errorf("%w: revision must be at least 1", category)
	}
	if err := validateTimestamp("created at", createdAt); err != nil {
		return fmt.Errorf("%w: %w", category, err)
	}
	if err := validateTimestamp("updated at", updatedAt); err != nil {
		return fmt.Errorf("%w: %w", category, err)
	}
	if updatedAt.Before(createdAt) {
		return fmt.Errorf("%w: updated before creation", category)
	}
	if deletedAt != nil {
		if err := validateTimestamp("deleted at", *deletedAt); err != nil {
			return fmt.Errorf("%w: %w", category, err)
		}
		if deletedAt.Before(createdAt) {
			return fmt.Errorf("%w: deleted before creation", category)
		}
	}
	return nil
}
