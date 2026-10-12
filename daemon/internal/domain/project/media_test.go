package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFixedMediaSlotIDValidatesOwnerAndKind(t *testing.T) {
	t.Parallel()
	clipID := "clp_" + strings.Repeat("A", 26)
	first, err := FixedMediaSlotID(clipID, MediaSlotClipFirstImage)
	if err != nil || first.String() != "msl_B"+strings.Repeat("A", 26) {
		t.Fatalf("FixedMediaSlotID() = %q, %v", first, err)
	}
	if _, err := FixedMediaSlotID(clipID, MediaSlotAssetPrimaryImage); !errors.Is(err, ErrInvalidMediaSlotID) {
		t.Fatalf("mismatched owner error = %v", err)
	}
	audio, err := FixedMediaSlotID(clipID, MediaSlotAudioPrimary)
	if err != nil || audio.String() != "msl_F"+strings.Repeat("A", 26) {
		t.Fatalf("reference audio slot = %q, %v", audio, err)
	}
}

func TestMediaSlotRejectsIDThatDoesNotMatchOwner(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0).UTC()
	value := MediaSlot{
		ID:        MediaSlotID("msl_C" + strings.Repeat("A", 26)),
		ProjectID: ProjectID("prj_" + strings.Repeat("A", 26)),
		EpisodeID: EpisodeID("ep_" + strings.Repeat("A", 26)),
		OwnerKind: MediaOwnerClip,
		OwnerID:   "clp_" + strings.Repeat("A", 26),
		Kind:      MediaSlotClipFirstImage,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := value.Validate(); !errors.Is(err, ErrInvalidMediaSlot) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestArtifactValidationKeepsKindsStrict(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0).UTC()
	base := Artifact{ID: ArtifactID("art_" + strings.Repeat("A", 26)), ProjectID: ProjectID("prj_" + strings.Repeat("A", 26)), Kind: MediaKindImage, RelativePath: "media/images/a.png", SHA256: strings.Repeat("a", 64), ByteSize: 1, MIMEType: "image/png", OriginalFilename: "a.png", Width: 720, Height: 1280, CreatedAt: now}
	if err := base.Validate(); err != nil {
		t.Fatalf("image Validate() error = %v", err)
	}
	base.DurationMilliseconds = 1
	if err := base.Validate(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("image duration error = %v", err)
	}
}
