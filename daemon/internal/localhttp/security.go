// Package localhttp owns the process-scoped authentication boundary for the
// ShotForward localhost HTTP service.
package localhttp

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"strings"
	"sync"
)

const (
	SessionCookieName      = "shotforward_session"
	maximumPairingAttempts = 5
	pairingCodeLength      = 6
	pairingCodeRadix       = 36
	pairingCodeSpace       = 2176782336 // 36^6
)

var ErrInvalidSecurityConfiguration = errors.New("invalid localhost HTTP security configuration")

// Credentials contains only process-memory secrets. The pairing code returned
// by NewCredentials is intentionally not retained in plaintext.
type Credentials struct {
	mutex         sync.Mutex
	pairingDigest [sha256.Size]byte
	paired        bool
	reusable      bool
	attempts      int
	sessionToken  string
	csrfToken     string
}

func NewCredentials(random io.Reader) (*Credentials, string, error) {
	if random == nil {
		return nil, "", ErrInvalidSecurityConfiguration
	}
	pairingNumber, err := cryptorand.Int(random, big.NewInt(pairingCodeSpace))
	if err != nil {
		return nil, "", err
	}
	rawPairing := strings.ToUpper(pairingNumber.Text(pairingCodeRadix))
	rawPairing = strings.Repeat("0", pairingCodeLength-len(rawPairing)) + rawPairing
	return newCredentials(random, rawPairing, false)
}

// NewReusableCredentials creates credentials whose configured pairing code
// remains valid after a successful exchange. Session and CSRF tokens remain
// random and process-scoped.
func NewReusableCredentials(random io.Reader, pairingCode string) (*Credentials, string, error) {
	normalized := normalizePairingCode(pairingCode)
	if !validPairingCode(normalized) {
		return nil, "", ErrInvalidSecurityConfiguration
	}
	return newCredentials(random, normalized, true)
}

func newCredentials(random io.Reader, rawPairing string, reusable bool) (*Credentials, string, error) {
	if random == nil {
		return nil, "", ErrInvalidSecurityConfiguration
	}
	sessionBytes := make([]byte, 32)
	csrfBytes := make([]byte, 32)
	for _, target := range [][]byte{sessionBytes, csrfBytes} {
		if _, err := io.ReadFull(random, target); err != nil {
			return nil, "", err
		}
	}
	return &Credentials{
		pairingDigest: sha256.Sum256([]byte(rawPairing)),
		reusable:      reusable,
		sessionToken:  base64.RawURLEncoding.EncodeToString(sessionBytes),
		csrfToken:     base64.RawURLEncoding.EncodeToString(csrfBytes),
	}, rawPairing, nil
}

func validPairingCode(value string) bool {
	if len(value) != pairingCodeLength {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func normalizePairingCode(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", ""), " ", ""))
}

// Exchange validates the pairing code and returns the CSRF token. Random
// credentials are consumed once; explicitly configured credentials are
// reusable. Failed attempts are bounded so a caller cannot probe indefinitely.
func (c *Credentials) Exchange(pairingCode string) (string, bool) {
	if c == nil {
		return "", false
	}
	digest := sha256.Sum256([]byte(normalizePairingCode(pairingCode)))
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if (c.paired && !c.reusable) || c.attempts >= maximumPairingAttempts {
		return "", false
	}
	c.attempts++
	if subtle.ConstantTimeCompare(digest[:], c.pairingDigest[:]) != 1 {
		return "", false
	}
	c.paired = true
	c.attempts = 0
	if !c.reusable {
		c.pairingDigest = [sha256.Size]byte{}
	}
	return c.csrfToken, true
}

func (c *Credentials) Authenticate(sessionToken, csrfToken string, requireCSRF bool) bool {
	if c == nil {
		return false
	}
	c.mutex.Lock()
	paired := c.paired
	expectedSession := c.sessionToken
	expectedCSRF := c.csrfToken
	c.mutex.Unlock()
	if !paired || !secretEqual(sessionToken, expectedSession) {
		return false
	}
	return !requireCSRF || secretEqual(csrfToken, expectedCSRF)
}

// InternalTokens are passed only to the already-authenticated Agent adapter.
// They are never serialized or written to the rendezvous file.
func (c *Credentials) InternalTokens() (string, string) {
	if c == nil {
		return "", ""
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.sessionToken, c.csrfToken
}

func secretEqual(left, right string) bool {
	return left != "" && right != "" && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
