package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRendezvousRejectsFreshOwnerAndContainsNoSecret(t *testing.T) {
	t.Parallel()

	runtimeDirectory := t.TempDir()
	record := rendezvousRecord{Address: "127.0.0.1:43100", InstanceID: "instance-one", ProcessID: 42}
	handle, err := acquireRendezvous(context.Background(), runtimeDirectory, record)
	if err != nil {
		t.Fatalf("acquireRendezvous() error = %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	contents, err := os.ReadFile(filepath.Join(runtimeDirectory, rendezvousFilename))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, forbidden := range []string{"pairing", "token", "secret", "csrf"} {
		if containsFold(string(contents), forbidden) {
			t.Fatalf("rendezvous contains forbidden field %q: %s", forbidden, contents)
		}
	}
	if _, err := acquireRendezvous(context.Background(), runtimeDirectory, rendezvousRecord{Address: "127.0.0.1:43101", InstanceID: "instance-two", ProcessID: 43}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire error = %v, want ErrAlreadyRunning", err)
	}
}

func TestRendezvousReplacesStaleDescriptorAndOnlyOwnerRemoves(t *testing.T) {
	t.Parallel()

	runtimeDirectory := t.TempDir()
	path := filepath.Join(runtimeDirectory, rendezvousFilename)
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	stale := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	handle, err := acquireRendezvous(context.Background(), runtimeDirectory, rendezvousRecord{Address: "127.0.0.1:43102", InstanceID: "replacement", ProcessID: 44})
	if err != nil {
		t.Fatalf("acquireRendezvous() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement owned elsewhere\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(replacement) error = %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("non-owned rendezvous was removed: %v", err)
	}
}

func containsFold(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if equalFoldASCII(value[index:index+len(fragment)], fragment) {
			return true
		}
	}
	return false
}

func equalFoldASCII(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		a, b := left[index], right[index]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
