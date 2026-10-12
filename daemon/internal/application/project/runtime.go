package projectapp

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const randomIDBytes = 16

var unpaddedBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// SecureIDGenerator creates type-prefixed IDs with 128 bits of entropy using
// only the Go standard library.
type SecureIDGenerator struct {
	reader io.Reader
}

// NewSecureIDGenerator creates the production ID generator.
func NewSecureIDGenerator() SecureIDGenerator {
	return SecureIDGenerator{reader: rand.Reader}
}

func (g SecureIDGenerator) NewProjectID() (domain.ProjectID, error) {
	value, err := g.generate("prj_")
	if err != nil {
		return "", fmt.Errorf("generate Project ID: %w", err)
	}
	return domain.ParseProjectID(value)
}

func (g SecureIDGenerator) NewEpisodeID() (domain.EpisodeID, error) {
	value, err := g.generate("ep_")
	if err != nil {
		return "", fmt.Errorf("generate Episode ID: %w", err)
	}
	return domain.ParseEpisodeID(value)
}

func (g SecureIDGenerator) NewAssetID() (domain.AssetID, error) {
	value, err := g.generate("ast_")
	if err != nil {
		return "", fmt.Errorf("generate Asset ID: %w", err)
	}
	return domain.ParseAssetID(value)
}

func (g SecureIDGenerator) NewAssetIdentityID() (domain.AssetIdentityID, error) {
	value, err := g.generate("asi_")
	if err != nil {
		return "", fmt.Errorf("generate Asset Identity ID: %w", err)
	}
	return domain.ParseAssetIdentityID(value)
}

func (g SecureIDGenerator) NewSceneID() (domain.SceneID, error) {
	value, err := g.generate("scn_")
	if err != nil {
		return "", fmt.Errorf("generate Scene ID: %w", err)
	}
	return domain.ParseSceneID(value)
}

func (g SecureIDGenerator) NewClipID() (domain.ClipID, error) {
	value, err := g.generate("clp_")
	if err != nil {
		return "", fmt.Errorf("generate Clip ID: %w", err)
	}
	return domain.ParseClipID(value)
}

func (g SecureIDGenerator) NewArtifactID() (domain.ArtifactID, error) {
	value, err := g.generate("art_")
	if err != nil {
		return "", fmt.Errorf("generate Artifact ID: %w", err)
	}
	return domain.ParseArtifactID(value)
}

func (g SecureIDGenerator) NewMediaVersionID() (domain.MediaVersionID, error) {
	value, err := g.generate("mvr_")
	if err != nil {
		return "", fmt.Errorf("generate MediaVersion ID: %w", err)
	}
	return domain.ParseMediaVersionID(value)
}

func (g SecureIDGenerator) NewAudioReferenceAssetID() (domain.AudioReferenceAssetID, error) {
	value, err := g.generate("aud_")
	if err != nil {
		return "", fmt.Errorf("generate AudioReferenceAsset ID: %w", err)
	}
	return domain.ParseAudioReferenceAssetID(value)
}

func (g SecureIDGenerator) NewGenerationBatchID() (domain.GenerationBatchID, error) {
	value, err := g.generate("gbt_")
	if err != nil {
		return "", fmt.Errorf("generate GenerationBatch ID: %w", err)
	}
	return domain.ParseGenerationBatchID(value)
}

func (g SecureIDGenerator) NewGenerationJobID() (domain.GenerationJobID, error) {
	value, err := g.generate("gjb_")
	if err != nil {
		return "", fmt.Errorf("generate GenerationJob ID: %w", err)
	}
	return domain.ParseGenerationJobID(value)
}

func (g SecureIDGenerator) NewExportJobID() (domain.ExportJobID, error) {
	value, err := g.generate("exj_")
	return domain.ExportJobID(value), err
}

func (g SecureIDGenerator) generate(prefix string) (string, error) {
	if g.reader == nil {
		return "", errors.New("random source is required")
	}
	buffer := make([]byte, randomIDBytes)
	if _, err := io.ReadFull(g.reader, buffer); err != nil {
		return "", fmt.Errorf("read secure randomness: %w", err)
	}
	return prefix + unpaddedBase32.EncodeToString(buffer), nil
}

// UTCClock returns timestamps normalized for durable millisecond storage.
type UTCClock struct{}

func (UTCClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}
