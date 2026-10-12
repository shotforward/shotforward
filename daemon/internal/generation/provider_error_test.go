package generation

import (
	"net/http"
	"strings"
	"testing"
)

func TestProviderHTTPErrorExtractsOfficialFieldsAndBecomesPublic(t *testing.T) {
	t.Parallel()
	headers := make(http.Header)
	headers.Set("X-Request-ID", "request-123")
	err := NewProviderHTTPError("Seedance", http.StatusBadRequest, headers, strings.NewReader(`{
		"error":{"code":"InputImageSensitiveContentDetected.PrivacyInformation","message":"The input may contain a real person."}
	}`), "private-key")
	code, message := generatorError("generator_start_failed", err)
	if code != ProviderResponseFailureCode || !strings.Contains(message, "InputImageSensitiveContentDetected.PrivacyInformation") || !strings.Contains(message, "request-123") {
		t.Fatalf("generatorError() = %q, %q", code, message)
	}
	if PublicProviderFailureMessage(code, message) != message {
		t.Fatalf("public message was not retained: %q", message)
	}
}

func TestProviderHTTPErrorRedactsCredentialsDataAndSignedQueries(t *testing.T) {
	t.Parallel()
	err := NewProviderHTTPError("Wan 3.0", http.StatusBadRequest, nil, strings.NewReader(`{
		"code":"InvalidInput",
		"message":"private-key Bearer another-key data:image/png;base64,AAAA https://cdn.example/file?signature=secret"
	}`), "private-key")
	message := err.Error()
	for _, forbidden := range []string{"private-key", "another-key", "AAAA", "signature=secret"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("Provider error leaked %q: %q", forbidden, message)
		}
	}
	for _, expected := range []string{"InvalidInput", "[REDACTED]", "https://cdn.example/file"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("Provider error omitted %q: %q", expected, message)
		}
	}
}

func TestPublicProviderFailureMessageRejectsArbitraryStoredErrors(t *testing.T) {
	t.Parallel()
	if message := PublicProviderFailureMessage("generator_start_failed", "/private/path and internals"); message != "" {
		t.Fatalf("arbitrary error became public: %q", message)
	}
	if message := PublicProviderFailureMessage(ProviderResponseFailureCode, "line one\nline two"); message != "" {
		t.Fatalf("multiline error became public: %q", message)
	}
}

func TestPublicGenerationFailureClassifiesHistoricalStartErrorsWithoutExposingInternals(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		message, want string
	}{
		{"start Seedance: prepare reference video delivery: ShotForward cloud login required", ReferenceDeliveryLoginRequiredFailureCode},
		{"start Seedance: reference video needs an unexpired original URL or ShotForward cloud login", ReferenceDeliveryLoginRequiredFailureCode},
		{"start Seedance: prepare reference video delivery: upload unavailable", ReferenceDeliveryFailureCode},
		{"start Seedance: upstream unavailable", ProviderUnreachableFailureCode},
		{"start Seedance: invalid upstream response", GeneratorProtocolFailureCode},
		{"start Seedance: request exceeds the 64 MiB API limit", GeneratorRequestTooLargeFailureCode},
	} {
		code, message := PublicGenerationFailure("generator_start_failed", test.message)
		if code != test.want || message != "" {
			t.Errorf("PublicGenerationFailure(%q) = %q, %q", test.message, code, message)
		}
	}
	code, message := PublicGenerationFailure("generator_start_failed", "/private/project/reference.mp4")
	if code != "generator_start_failed" || message != "" {
		t.Fatalf("private failure became public: %q, %q", code, message)
	}
}
