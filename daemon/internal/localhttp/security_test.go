package localhttp

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewCredentialsReturnsSixCharacterAlphanumericPairingCode(t *testing.T) {
	credentials, pairingCode, err := NewCredentials(bytes.NewReader(bytes.Repeat([]byte{0x4a}, 96)))
	if err != nil {
		t.Fatalf("NewCredentials() error = %v", err)
	}
	if len(pairingCode) != pairingCodeLength {
		t.Fatalf("pairing code length = %d, want %d", len(pairingCode), pairingCodeLength)
	}
	for _, character := range pairingCode {
		if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", character) {
			t.Fatalf("pairing code %q contains invalid character %q", pairingCode, character)
		}
	}
	if _, ok := credentials.Exchange(strings.ToLower(pairingCode)); !ok {
		t.Fatal("lowercase form of pairing code was rejected")
	}
}

func TestReusableCredentialsAcceptConfiguredCodeMoreThanOnce(t *testing.T) {
	credentials, pairingCode, err := NewReusableCredentials(bytes.NewReader(bytes.Repeat([]byte{0x3a}, 64)), " ab12cd ")
	if err != nil {
		t.Fatalf("NewReusableCredentials() error = %v", err)
	}
	if pairingCode != "AB12CD" {
		t.Fatalf("pairingCode = %q", pairingCode)
	}
	for attempt := 0; attempt < 2; attempt++ {

		if _, ok := credentials.Exchange(strings.ToLower(pairingCode)); !ok {
			t.Fatalf("Exchange() attempt %d rejected reusable code", attempt+1)
		}
	}
}

func TestReusableCredentialsRejectInvalidConfiguredCode(t *testing.T) {
	if _, _, err := NewReusableCredentials(bytes.NewReader(bytes.Repeat([]byte{0x3a}, 64)), "bad"); err == nil {
		t.Fatal("NewReusableCredentials() error = nil")
	}
}
