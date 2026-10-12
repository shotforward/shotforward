package changesetapp

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"

	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const randomIDBytes = 16

var unpaddedBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type SecureIDGenerator struct{ reader io.Reader }

func NewSecureIDGenerator() SecureIDGenerator { return SecureIDGenerator{reader: rand.Reader} }
func (g SecureIDGenerator) NewChangeSetID() (changeset.ChangeSetID, error) {
	value, err := g.generate("cst_")
	if err != nil {
		return "", err
	}
	return changeset.ParseChangeSetID(value)
}
func (g SecureIDGenerator) NewOperationID() (changeset.OperationID, error) {
	value, err := g.generate("cop_")
	if err != nil {
		return "", err
	}
	return changeset.ParseOperationID(value)
}
func (g SecureIDGenerator) NewPreconditionID() (changeset.PreconditionID, error) {
	value, err := g.generate("cpc_")
	if err != nil {
		return "", err
	}
	return changeset.ParsePreconditionID(value)
}
func (g SecureIDGenerator) NewConflictID() (changeset.ConflictID, error) {
	value, err := g.generate("ccf_")
	if err != nil {
		return "", err
	}
	return changeset.ParseConflictID(value)
}
func (g SecureIDGenerator) NewAssetID() (project.AssetID, error) {
	value, err := g.generate("ast_")
	if err != nil {
		return "", err
	}
	return project.ParseAssetID(value)
}
func (g SecureIDGenerator) NewAssetIdentityID() (project.AssetIdentityID, error) {
	value, err := g.generate("asi_")
	if err != nil {
		return "", err
	}
	return project.ParseAssetIdentityID(value)
}
func (g SecureIDGenerator) NewMediaVersionID() (project.MediaVersionID, error) {
	value, err := g.generate("mvr_")
	if err != nil {
		return "", err
	}
	return project.ParseMediaVersionID(value)
}
func (g SecureIDGenerator) NewSceneID() (project.SceneID, error) {
	value, err := g.generate("scn_")
	if err != nil {
		return "", err
	}
	return project.ParseSceneID(value)
}
func (g SecureIDGenerator) NewClipID() (project.ClipID, error) {
	value, err := g.generate("clp_")
	if err != nil {
		return "", err
	}
	return project.ParseClipID(value)
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
