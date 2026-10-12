package shotforwardcloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestPrepareProviderDeliveryStreamsVideoAndCreatesGrant(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reference.mp4")
	if err := os.WriteFile(path, []byte("reference-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	var grantRequest map[string]string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/auth/desktop-code/exchanges" && request.Header.Get("Authorization") != "Bearer access-1" {
			t.Errorf("authorization=%q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/v1/auth/desktop-code/exchanges":
			return jsonResponse(http.StatusOK, Tokens{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900}), nil
		case "/v1/media-inputs":
			return jsonResponse(http.StatusCreated, map[string]any{"id": "min_1", "upload": map[string]any{
				"url":              "https://cloud.example/v1/media-inputs/min_1/content",
				"required_headers": map[string]string{"Content-Type": "video/mp4", "X-Content-SHA256": strings.Repeat("a", 64)},
			}}), nil
		case "/v1/media-inputs/min_1/content":
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPut || request.ContentLength != int64(len("reference-video")) || string(body) != "reference-video" || request.Header.Get("Content-Type") != "video/mp4" {
				t.Errorf("upload method=%s length=%d type=%q body=%q", request.Method, request.ContentLength, request.Header.Get("Content-Type"), body)
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": "min_1", "status": "ready"}), nil
		case "/v1/media-inputs/min_1/delivery-grants":
			if err := json.NewDecoder(request.Body).Decode(&grantRequest); err != nil {
				t.Error(err)
			}
			return jsonResponse(http.StatusCreated, map[string]any{"url": "https://media.example/d/dlv_1?s=secret", "expires_at": expiresAt}), nil
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
			return nil, nil
		}
	})}
	manager, err := NewManager("https://cloud.example", &memoryCredentials{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ExchangeDesktopCode(context.Background(), "234567-89ABCDE"); err != nil {
		t.Fatal(err)
	}
	deliveryURL, gotExpiry, err := manager.PrepareProviderDelivery(context.Background(), path, "video/mp4", "seedance", "reference_video", "job_1")
	if err != nil || deliveryURL != "https://media.example/d/dlv_1?s=secret" || !gotExpiry.Equal(expiresAt) {
		t.Fatalf("url=%q expiry=%s err=%v", deliveryURL, gotExpiry, err)
	}
	if grantRequest["provider"] != "seedance" || grantRequest["purpose"] != "reference_video" || grantRequest["client_operation_id"] != "job_1" {
		t.Fatalf("grant request=%#v", grantRequest)
	}
}

func TestProviderDeliveryPreservesLoginRequiredCategory(t *testing.T) {
	t.Parallel()
	err := providerDeliveryError(ErrLoginRequired)
	err = generation.NewGeneratorFailure(generation.ReferenceDeliveryFailureCode, err)
	var categorized interface{ GenerationFailureCode() string }
	if !errors.As(err, &categorized) || categorized.GenerationFailureCode() != generation.ReferenceDeliveryLoginRequiredFailureCode {
		t.Fatalf("delivery error=%v category=%v", err, categorized)
	}
}
