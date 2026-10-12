// Package securecredentials stores Provider secrets in the operating system's
// native credential vault. It deliberately exposes only opaque references to
// the rest of ShotForward.
package securecredentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	keyring "github.com/zalando/go-keyring"
)

const (
	defaultService         = "com.shotforward.video-studio"
	maxReferenceCharacters = 500
	maxSecretBytes         = 16 * 1024
)

var (
	ErrInvalidRequest = errors.New("invalid secure credential request")
	ErrUnavailable    = errors.New("secure credential store unavailable")
)

type backend struct {
	set    func(string, string, string) error
	get    func(string, string) (string, error)
	delete func(string, string) error
}

// Store is a cgo-free adapter over macOS Keychain, Windows Credential Manager,
// and Linux Secret Service. Constructing it does not open the native vault.
type Store struct {
	service  string
	backend  backend
	fallback *backend
}

func NewNativeStore() *Store {
	return &Store{service: defaultService, backend: backend{set: keyring.Set, get: keyring.Get, delete: keyring.Delete}}
}

// NewStore uses the operating-system vault when available and a daemon-owned,
// permission-restricted directory on headless systems without a user keyring.
func NewStore(dataDirectory string) *Store {
	store := NewNativeStore()
	fileBackend := newFileBackend(filepath.Join(dataDirectory, "credentials"))
	store.fallback = &fileBackend
	return store
}

func (s *Store) Put(ctx context.Context, reference, secret string) error {
	if err := validate(ctx, reference, secret, true); err != nil {
		return err
	}
	if s == nil || s.backend.set == nil {
		return ErrUnavailable
	}
	if err := s.backend.set(s.service, reference, secret); err != nil {
		if s.fallback == nil || s.fallback.set(s.service, reference, secret) != nil {
			return ErrUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Store) Get(ctx context.Context, reference string) (string, bool, error) {
	if err := validate(ctx, reference, "", false); err != nil {
		return "", false, err
	}
	if s == nil || s.backend.get == nil {
		return "", false, ErrUnavailable
	}
	secret, err := s.backend.get(s.service, reference)
	if err != nil && s.fallback != nil {
		secret, err = s.fallback.get(s.service, reference)
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	} else if err != nil {
		return "", false, ErrUnavailable
	}
	if err := validate(context.Background(), reference, secret, true); err != nil {
		return "", false, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return secret, true, nil
}

func (s *Store) Delete(ctx context.Context, reference string) (bool, error) {
	if err := validate(ctx, reference, "", false); err != nil {
		return false, err
	}
	if s == nil || s.backend.delete == nil {
		return false, ErrUnavailable
	}
	err := s.backend.delete(s.service, reference)
	if err != nil && s.fallback != nil {
		err = s.fallback.delete(s.service, reference)
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func newFileBackend(directory string) backend {
	credentialPath := func(reference string) string {
		return filepath.Join(directory, reference)
	}
	return backend{
		set: func(_, reference, secret string) error {
			if err := os.MkdirAll(directory, 0o700); err != nil || os.Chmod(directory, 0o700) != nil {
				return ErrUnavailable
			}
			file, err := os.CreateTemp(directory, ".credential-*")
			if err != nil {
				return ErrUnavailable
			}
			temporaryPath := file.Name()
			keep := false
			defer func() {
				_ = file.Close()
				if !keep {
					_ = os.Remove(temporaryPath)
				}
			}()
			if err := file.Chmod(0o600); err != nil {
				return ErrUnavailable
			}
			if _, err := file.WriteString(secret); err != nil || file.Sync() != nil || file.Close() != nil {
				return ErrUnavailable
			}
			if err := os.Rename(temporaryPath, credentialPath(reference)); err != nil {
				return ErrUnavailable
			}
			keep = true
			return nil
		},
		get: func(_, reference string) (string, error) {
			path := credentialPath(reference)
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				return "", keyring.ErrNotFound
			}
			if err != nil || !info.Mode().IsRegular() || !hasPrivateFilePermissions(info) {
				return "", ErrUnavailable
			}
			value, err := os.ReadFile(path)
			if err != nil {
				return "", ErrUnavailable
			}
			return string(value), nil
		},
		delete: func(_, reference string) error {
			if err := os.Remove(credentialPath(reference)); errors.Is(err, os.ErrNotExist) {
				return keyring.ErrNotFound
			} else if err != nil {
				return ErrUnavailable
			}
			return nil
		},
	}
}

func hasPrivateFilePermissions(info os.FileInfo) bool {
	// Windows protects files through ACLs inherited from the user-owned data
	// directory. os.FileMode permission bits do not represent those ACLs, and
	// os.Chmod only controls the read-only attribute there.
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0
}

func validate(ctx context.Context, reference, secret string, requireSecret bool) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validReference(reference) {
		return ErrInvalidRequest
	}
	if requireSecret && (secret == "" || len(secret) > maxSecretBytes || !utf8.ValidString(secret) || strings.ContainsAny(secret, "\r\n\x00") || secret != strings.TrimSpace(secret)) {
		return ErrInvalidRequest
	}
	return nil
}

func validReference(value string) bool {
	if value == "" || len(value) > maxReferenceCharacters || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
