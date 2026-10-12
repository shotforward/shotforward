package securecredentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

func TestStoreKeepsSecretsInsideBackend(t *testing.T) {
	t.Parallel()
	values := map[string]string{}
	store := &Store{service: "test", backend: backend{
		set: func(service, reference, secret string) error { values[service+":"+reference] = secret; return nil },
		get: func(service, reference string) (string, error) {
			value, ok := values[service+":"+reference]
			if !ok {
				return "", keyring.ErrNotFound
			}
			return value, nil
		},
		delete: func(service, reference string) error {
			key := service + ":" + reference
			if _, ok := values[key]; !ok {
				return keyring.ErrNotFound
			}
			delete(values, key)
			return nil
		},
	}}
	ctx := context.Background()
	if err := store.Put(ctx, "pcr_TEST", "sk-private-value"); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.Get(ctx, "pcr_TEST")
	if err != nil || !found || value != "sk-private-value" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if removed, err := store.Delete(ctx, "pcr_TEST"); err != nil || !removed {
		t.Fatalf("Delete() = %v, %v", removed, err)
	}
	if _, found, err := store.Get(ctx, "pcr_TEST"); err != nil || found {
		t.Fatalf("Get(deleted) = %v, %v", found, err)
	}
}

func TestStoreMapsBackendErrorsWithoutLeakingThem(t *testing.T) {
	t.Parallel()
	secretBearingError := errors.New("backend rejected sk-do-not-leak")
	store := &Store{service: "test", backend: backend{
		set:    func(string, string, string) error { return secretBearingError },
		get:    func(string, string) (string, error) { return "", secretBearingError },
		delete: func(string, string) error { return secretBearingError },
	}}
	if err := store.Put(context.Background(), "pcr_TEST", "sk-do-not-leak"); !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
		t.Fatalf("Put() error = %v", err)
	}
	if _, _, err := store.Get(context.Background(), "pcr_TEST"); !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := store.Delete(context.Background(), "pcr_TEST"); !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestStoreFallsBackToPrivateFilesWhenNativeVaultIsUnavailable(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "credentials")
	fileBackend := newFileBackend(directory)
	store := &Store{service: "test", backend: backend{
		set:    func(string, string, string) error { return errors.New("native unavailable") },
		get:    func(string, string) (string, error) { return "", errors.New("native unavailable") },
		delete: func(string, string) error { return errors.New("native unavailable") },
	}, fallback: &fileBackend}
	ctx := context.Background()
	if err := store.Put(ctx, "pcr_HEADLESS", "sk-private-value"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(directory); err != nil {
		t.Fatalf("credential directory: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory mode = %o, want 700", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Join(directory, "pcr_HEADLESS")); err != nil {
		t.Fatalf("credential file: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %o, want 600", info.Mode().Perm())
	}
	value, found, err := store.Get(ctx, "pcr_HEADLESS")
	if err != nil || !found || value != "sk-private-value" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if removed, err := store.Delete(ctx, "pcr_HEADLESS"); err != nil || !removed {
		t.Fatalf("Delete() = %v, %v", removed, err)
	}
	if _, found, err := store.Get(ctx, "pcr_HEADLESS"); err != nil || found {
		t.Fatalf("Get(deleted) = %v, %v", found, err)
	}
}

func TestStoreRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	store := &Store{service: "test", backend: backend{
		set: func(string, string, string) error { t.Fatal("backend called"); return nil },
	}}
	for _, test := range []struct{ reference, secret string }{
		{"", "secret"}, {"../escape", "secret"}, {"pcr_OK", ""}, {"pcr_OK", " secret"}, {"pcr_OK", "secret\nnext"},
	} {
		if err := store.Put(context.Background(), test.reference, test.secret); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Put(%q) error = %v", test.reference, err)
		}
	}
}
