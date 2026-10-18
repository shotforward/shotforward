package agenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type failingUploadMedia struct {
	fakeAgentMedia
	err   error
	calls int
}

func (m *failingUploadMedia) UploadAgentConversationMedia(ctx context.Context, input projectapp.UploadAgentConversationMediaInput) (project.AgentConversationMedia, error) {
	m.calls++
	if m.err != nil {
		m.uploadInput = input
		return project.AgentConversationMedia{}, m.err
	}
	return m.fakeAgentMedia.UploadAgentConversationMedia(ctx, input)
}

func TestAgentMediaImportErrorsAreActionableAndDiagnosable(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unsupported", projectapp.ErrMediaImportUnsupported, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"invalid", projectapp.ErrMediaImportInvalid, http.StatusUnprocessableEntity, "invalid_media"},
		{"unreadable", projectapp.ErrMediaImportUnreadable, http.StatusUnprocessableEntity, "unreadable_media"},
		{"unavailable", projectapp.ErrMediaImportUnavailable, http.StatusServiceUnavailable, "media_import_unavailable"},
		{"unexpected", errors.New("unexpected storage failure"), http.StatusInternalServerError, "external_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := testHandler(t, fakeEvents{})
			media := &failingUploadMedia{err: errors.Join(test.err, errors.New("private detail: Bearer private-token api_key=private-key\nprobe failed"))}
			handler.media = media
			var logs bytes.Buffer
			handler.logger = slog.New(slog.NewTextHandler(&logs, nil))
			request := agentMediaMultipartRequest(t, "attachment.png", 20, "upload-operation")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertApplicationError(t, response.Body.Bytes())
			var result struct {
				Error applicationError `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || result.Error.Code != test.code || media.calls != 1 {
				t.Fatalf("response = %d %s, calls = %d", response.Code, response.Body.String(), media.calls)
			}
			for _, secret := range []string{"private detail", "private-token", "private-key", "probe failed"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("response exposed internal diagnostics: %s", response.Body.String())
				}
			}
			diagnostic := logs.String()
			for _, expected := range []string{"Agent image upload failed", "stage=import", "error_code=" + test.code, "request_id=" + result.Error.RequestID, "probe failed"} {
				if !strings.Contains(diagnostic, expected) {
					t.Fatalf("missing %q in diagnostic: %s", expected, diagnostic)
				}
			}
			if strings.Contains(diagnostic, "private-token") || strings.Contains(diagnostic, "private-key") {
				t.Fatalf("diagnostic exposed a secret: %s", diagnostic)
			}
			if _, err := os.Stat(media.uploadInput.SourcePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("upload temporary file was not removed: %v", err)
			}
		})
	}
}

func TestAgentMediaUploadSizeBoundaryAndTemporaryCleanup(t *testing.T) {
	// The oversized cases spill multipart data to disk. All paths stay inside
	// this test directory; both multipart and import temporary files must go.
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	for _, test := range []struct {
		name   string
		size   int64
		status int
		code   string
	}{
		{"empty", 0, http.StatusUnprocessableEntity, "invalid_media"},
		{"at_limit", maxAgentMediaUploadBytes, http.StatusCreated, ""},
		{"one_byte_over", maxAgentMediaUploadBytes + 1, http.StatusRequestEntityTooLarge, "agent_media_too_large"},
		{"body_limit", maxAgentMediaUploadBytes + 1024*1024 + 1, http.StatusRequestEntityTooLarge, "agent_media_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := testHandler(t, fakeEvents{})
			media := &failingUploadMedia{}
			handler.media = media
			request := agentMediaMultipartRequest(t, "image.png", test.size, "size-operation")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("response = %d %s, want %d", response.Code, response.Body.String(), test.status)
			}
			if test.status == http.StatusCreated {
				if media.calls != 1 || int64(len(media.uploadBytes)) != test.size {
					t.Fatalf("media calls = %d, bytes = %d", media.calls, len(media.uploadBytes))
				}
			} else {
				assertApplicationError(t, response.Body.Bytes())
				if media.calls != 0 || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
					t.Fatalf("invalid upload reached import or incorrect error: calls=%d, %s", media.calls, response.Body.String())
				}
			}
			entries, err := os.ReadDir(temporaryRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("upload temporary files leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestAgentMediaMalformedRequestsDoNotReachImport(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(*testing.T) *http.Request
		code string
	}{
		{"missing_operation", func(t *testing.T) *http.Request {
			return agentMediaMultipartRequest(t, "image.png", 10, "")
		}, "invalid_argument"},
		{"missing_file", func(t *testing.T) *http.Request {
			return agentMediaMultipartRequest(t, "", 0, "operation")
		}, "invalid_agent_media"},
		{"malformed_multipart", func(t *testing.T) *http.Request {
			request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/media", strings.NewReader("truncated multipart body"))
			request.Header.Set("Content-Type", "multipart/form-data; boundary=missing-boundary")
			return request
		}, "invalid_agent_media"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := testHandler(t, fakeEvents{})
			media := &failingUploadMedia{}
			handler.media = media
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.make(t))
			if response.Code != http.StatusBadRequest || media.calls != 0 || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s, import calls = %d", response.Code, response.Body.String(), media.calls)
			}
			assertApplicationError(t, response.Body.Bytes())
		})
	}
}

func agentMediaMultipartRequest(t *testing.T, filename string, size int64, operationID string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("client_operation_id", operationID); err != nil {
		t.Fatal(err)
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(part, io.LimitReader(agentMediaZeroReader{}, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(http.MethodPost, "/api/v1/agent-sessions/"+testSession.String()+"/media", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

type agentMediaZeroReader struct{}

func (agentMediaZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
