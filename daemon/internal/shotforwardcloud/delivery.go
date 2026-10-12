package shotforwardcloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/generation"
)

const maximumDeliveryUploadBytes int64 = 512 << 20

// PrepareProviderDelivery uploads one local reference to the signed-in
// ShotForward account and returns a short-lived URL for an external provider.
// Provider API credentials never cross this boundary.
func (m *Manager) PrepareProviderDelivery(ctx context.Context, absolutePath, mediaType, provider, purpose, clientOperationID string) (string, time.Time, error) {
	if ctx == nil || !filepath.IsAbs(absolutePath) || filepath.Clean(absolutePath) != absolutePath || (mediaType != "video/mp4" && mediaType != "video/quicktime") || provider != "seedance" || purpose != "reference_video" || len(clientOperationID) > 191 {
		return "", time.Time{}, errors.New("invalid provider delivery request")
	}
	pathInfo, err := os.Lstat(absolutePath)
	if err != nil || !pathInfo.Mode().IsRegular() {
		return "", time.Time{}, errors.New("provider delivery media is unavailable")
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("open provider delivery media: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumDeliveryUploadBytes {
		file.Close()
		return "", time.Time{}, errors.New("provider delivery media is unavailable or too large")
	}
	hash := sha256.New()
	if written, copyErr := io.Copy(hash, file); copyErr != nil || written != info.Size() {
		file.Close()
		return "", time.Time{}, errors.New("hash provider delivery media")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return "", time.Time{}, err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	var upload struct {
		ID     string `json:"id"`
		Upload struct {
			URL             string            `json:"url"`
			RequiredHeaders map[string]string `json:"required_headers"`
		} `json:"upload"`
	}
	if err = m.DoAuthenticated(ctx, http.MethodPost, "/v1/media-inputs", map[string]any{
		"media_type": mediaType, "byte_size": info.Size(), "checksum_sha256": checksum,
	}, &upload); err != nil {
		file.Close()
		return "", time.Time{}, providerDeliveryError(err)
	}
	if upload.ID == "" || upload.Upload.URL == "" {
		file.Close()
		return "", time.Time{}, ErrCloudResponse
	}
	if err = m.uploadAuthenticatedStream(ctx, upload.Upload.URL, file, info.Size(), upload.Upload.RequiredHeaders); err != nil {
		file.Close()
		return "", time.Time{}, providerDeliveryError(err)
	}
	if err = file.Close(); err != nil {
		return "", time.Time{}, err
	}
	var grant struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	grantPath := "/v1/media-inputs/" + url.PathEscape(upload.ID) + "/delivery-grants"
	if err = m.DoAuthenticated(ctx, http.MethodPost, grantPath, map[string]any{
		"provider": provider, "purpose": purpose, "client_operation_id": clientOperationID,
	}, &grant); err != nil {
		return "", time.Time{}, providerDeliveryError(err)
	}
	parsed, parseErr := url.Parse(grant.URL)
	if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || grant.ExpiresAt.Before(time.Now().Add(5*time.Minute)) {
		return "", time.Time{}, ErrCloudResponse
	}
	return grant.URL, grant.ExpiresAt, nil
}

func providerDeliveryError(err error) error {
	if errors.Is(err, ErrLoginRequired) {
		return generation.NewGeneratorFailure(generation.ReferenceDeliveryLoginRequiredFailureCode, err)
	}
	return err
}

func (m *Manager) uploadAuthenticatedStream(ctx context.Context, rawURL string, source io.Reader, size int64, headers map[string]string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != m.apiBase.Scheme || parsed.Host != m.apiBase.Host || parsed.User != nil || size < 1 || source == nil {
		return ErrCloudResponse
	}
	if err = m.ensureAccess(ctx); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, parsed.String(), source)
	if err != nil {
		return err
	}
	request.ContentLength = size
	m.mu.Lock()
	token := m.accessToken
	m.mu.Unlock()
	request.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrCloudResponse, response.StatusCode)
	}
	return nil
}
