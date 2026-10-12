package providerconfig

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type SecureIDGenerator struct{ reader io.Reader }

func NewSecureIDGenerator() SecureIDGenerator { return SecureIDGenerator{reader: rand.Reader} }

func (g SecureIDGenerator) NewCredentialReference() (string, error) {
	if g.reader == nil {
		return "", fmt.Errorf("generate credential reference: random source is required")
	}
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(g.reader, buffer); err != nil {
		return "", fmt.Errorf("generate credential reference: %w", err)
	}
	return "pcr_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}

type UTCClock struct{}

func (UTCClock) Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

type EmptyCheckerRegistry struct{}

func (EmptyCheckerRegistry) Checker(domain.GeneratorID) (Checker, bool) { return nil, false }

type MapCheckerRegistry map[domain.GeneratorID]Checker

func (r MapCheckerRegistry) Checker(id domain.GeneratorID) (Checker, bool) {
	value, ok := r[id]
	return value, ok
}
