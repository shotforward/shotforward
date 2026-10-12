package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const rendezvousFilename = "daemon.json"

type rendezvousRecord struct {
	Address    string `json:"address"`
	InstanceID string `json:"instance_id"`
	ProcessID  int    `json:"process_id"`
}

type rendezvousHandle struct {
	path    string
	encoded []byte
}

func acquireRendezvous(ctx context.Context, runtimeDirectory string, record rendezvousRecord) (*rendezvousHandle, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(runtimeDirectory, rendezvousFilename)
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.Write(encoded); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write daemon rendezvous: %w", writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close daemon rendezvous: %w", closeErr)
			}
			return &rendezvousHandle{path: path, encoded: encoded}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create daemon rendezvous: %w", err)
		}
		existing, readErr := readRendezvous(path)
		if readErr == nil && rendezvousAlive(ctx, existing) {
			return nil, ErrAlreadyRunning
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) < time.Second {
			return nil, ErrAlreadyRunning
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return nil, fmt.Errorf("remove stale daemon rendezvous: %w", removeErr)
		}
	}
	return nil, ErrAlreadyRunning
}

func readRendezvous(path string) (rendezvousRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rendezvousRecord{}, err
	}
	var result rendezvousRecord
	if err := json.Unmarshal(data, &result); err != nil {
		return rendezvousRecord{}, err
	}
	return result, nil
}

func rendezvousAlive(ctx context.Context, value rendezvousRecord) bool {
	if value.Address == "" || value.InstanceID == "" {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+value.Address+"/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 250 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024))
	return err == nil && response.StatusCode == http.StatusOK && bytes.Contains(data, []byte(value.InstanceID))
}

func (h *rendezvousHandle) Close() error {
	if h == nil {
		return nil
	}
	current, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read daemon rendezvous during close: %w", err)
	}
	if !bytes.Equal(current, h.encoded) {
		return nil
	}
	if err := os.Remove(h.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon rendezvous: %w", err)
	}
	return nil
}
