package generation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ProviderResponseFailureCode               = "provider_response_error"
	ProviderTaskFailureCode                   = "provider_task_failed"
	GeneratorUnavailableFailureCode           = "generator_unavailable"
	GeneratorProtocolFailureCode              = "generator_protocol_error"
	GeneratorRequestTooLargeFailureCode       = "generator_request_too_large"
	ProviderUnreachableFailureCode            = "provider_unreachable"
	ReferenceDeliveryLoginRequiredFailureCode = "reference_delivery_login_required"
	ReferenceDeliveryFailureCode              = "reference_delivery_failed"
	maximumProviderErrorBytes                 = 64 << 10
	maximumProviderMessageRunes               = 1_800
)

var (
	credentialPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+|(sk-[a-z0-9_-]+)|((?:token|secret|api[_-]?key)\s*[=:]\s*)[^\s,;]+`)
	dataURLPattern    = regexp.MustCompile(`(?i)data:[^;,\s]+;base64,[a-z0-9+/=]+`)
	httpURLPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
)

type providerResponseError struct{ publicMessage string }

func (e providerResponseError) Error() string                { return e.publicMessage }
func (e providerResponseError) PublicFailureMessage() string { return e.publicMessage }

type codedGeneratorError struct {
	code  string
	cause error
}

func (e codedGeneratorError) Error() string                 { return e.cause.Error() }
func (e codedGeneratorError) Unwrap() error                 { return e.cause }
func (e codedGeneratorError) GenerationFailureCode() string { return e.code }

// NewGeneratorFailure assigns a safe, user-facing category to an internal
// Generator error without making its potentially sensitive message public.
func NewGeneratorFailure(code string, cause error) error {
	if cause == nil {
		return nil
	}
	var existing interface{ GenerationFailureCode() string }
	if errors.As(cause, &existing) || !isPublicGenerationFailureCode(code) {
		return cause
	}
	return codedGeneratorError{code: code, cause: cause}
}

// NewProviderHTTPError extracts only documented error-shaped fields from a
// bounded Provider response. Arbitrary response JSON is never exposed.
func NewProviderHTTPError(provider string, status int, headers http.Header, body io.Reader, secrets ...string) error {
	fields := providerErrorFields{requestID: requestIDFromHeaders(headers)}
	if body != nil {
		limited, readErr := io.ReadAll(io.LimitReader(body, maximumProviderErrorBytes+1))
		if readErr == nil && len(limited) <= maximumProviderErrorBytes {
			var decoded any
			decoder := json.NewDecoder(strings.NewReader(string(limited)))
			decoder.UseNumber()
			if decoder.Decode(&decoded) == nil {
				extractProviderErrorFields(decoded, &fields, 0)
			}
		}
	}
	return providerResponseError{publicMessage: formatProviderFailure(provider, status, fields.code, fields.message, fields.requestID, secrets...)}
}

// ProviderTaskFailureMessage formats error fields returned by a successfully
// queried asynchronous task. The result is safe to persist and expose.
func ProviderTaskFailureMessage(provider, code, message, requestID string, secrets ...string) string {
	return formatProviderFailure(provider, 0, code, message, requestID, secrets...)
}

// PublicProviderFailureMessage returns a persisted message only for the
// reserved codes written by the bounded Provider-error sanitizer.
func PublicProviderFailureMessage(code, message string) string {
	if code != ProviderResponseFailureCode && code != ProviderTaskFailureCode {
		return ""
	}
	message = strings.TrimSpace(strings.ToValidUTF8(message, "�"))
	if message == "" || !utf8.ValidString(message) || utf8.RuneCountInString(message) > maximumProviderMessageRunes || strings.ContainsAny(message, "\r\n\x00") {
		return ""
	}
	return message
}

// PublicGenerationFailure returns a safe failure code and an optional
// sanitized Provider message. It also classifies historical start/poll errors
// so existing failed tasks gain useful UI messages after an upgrade.
func PublicGenerationFailure(code, storedMessage string) (string, string) {
	if code == "" {
		return "", ""
	}
	if message := PublicProviderFailureMessage(code, storedMessage); message != "" {
		return code, message
	}
	if isPublicGenerationFailureCode(code) {
		return code, ""
	}
	if code != "generator_start_failed" && code != "generator_poll_failed" {
		return code, ""
	}
	lower := strings.ToLower(storedMessage)
	switch {
	case strings.Contains(lower, "shotforward cloud login"):
		return ReferenceDeliveryLoginRequiredFailureCode, ""
	case strings.Contains(lower, "prepare reference video delivery"):
		return ReferenceDeliveryFailureCode, ""
	case strings.Contains(lower, "provider is unavailable"), strings.Contains(lower, "provider configuration changed"):
		return GeneratorUnavailableFailureCode, ""
	case strings.Contains(lower, "upstream unavailable"), strings.Contains(lower, "service is unavailable"):
		return ProviderUnreachableFailureCode, ""
	case strings.Contains(lower, "request exceeds"):
		return GeneratorRequestTooLargeFailureCode, ""
	case strings.Contains(lower, "invalid upstream response"), strings.Contains(lower, "unknown task state"):
		return GeneratorProtocolFailureCode, ""
	default:
		return code, ""
	}
}

func isPublicGenerationFailureCode(code string) bool {
	if IsCloudBusinessFailure(code) {
		return true
	}
	switch code {
	case ProviderResponseFailureCode, ProviderTaskFailureCode,
		GeneratorUnavailableFailureCode, GeneratorProtocolFailureCode,
		GeneratorRequestTooLargeFailureCode, ProviderUnreachableFailureCode,
		ReferenceDeliveryLoginRequiredFailureCode, ReferenceDeliveryFailureCode:
		return true
	default:
		return false
	}
}

func IsCloudBusinessFailure(code string) bool {
	switch code {
	case "shotforward_negative_balance", "shotforward_insufficient_points", "shotforward_wallet_not_found":
		return true
	default:
		return false
	}
}

type providerErrorFields struct {
	code, message, requestID string
}

func extractProviderErrorFields(value any, fields *providerErrorFields, depth int) {
	if depth > 6 || fields == nil {
		return
	}
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if fields.requestID == "" {
		fields.requestID = firstScalar(object, "request_id", "requestId", "trace_id", "traceId")
	}
	for _, key := range []string{"error", "base_resp", "base_response"} {
		if nested, exists := object[key]; exists {
			extractProviderErrorFields(nested, fields, depth+1)
		}
	}
	if fields.code == "" {
		fields.code = firstScalar(object, "code", "error_code", "status_code")
	}
	if fields.message == "" {
		fields.message = firstScalar(object, "message", "error_message", "error_msg", "status_msg")
	}
	for _, key := range []string{"output", "task", "data"} {
		if nested, exists := object[key]; exists {
			extractProviderErrorFields(nested, fields, depth+1)
		}
	}
}

func firstScalar(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, exists := object[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case json.Number:
			return typed.String()
		case float64, bool:
			return fmt.Sprint(typed)
		}
	}
	return ""
}

func requestIDFromHeaders(headers http.Header) string {
	for _, name := range []string{"X-Request-Id", "X-Tt-Logid", "Request-Id", "Trace-Id", "X-Requestid"} {
		if value := headers.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func formatProviderFailure(provider string, status int, code, message, requestID string, secrets ...string) string {
	provider = sanitizeProviderField(provider, nil, 80)
	if provider == "" {
		provider = "Provider"
	}
	code = sanitizeProviderField(code, secrets, 240)
	message = sanitizeProviderField(message, secrets, 1_200)
	requestID = sanitizeProviderField(requestID, secrets, 320)

	prefix := provider
	if status >= 100 && status <= 599 {
		prefix += fmt.Sprintf(" (HTTP %d)", status)
	}
	detail := message
	if code != "" && message != "" && !strings.EqualFold(code, message) {
		detail = code + ": " + message
	} else if code != "" {
		detail = code
	}
	if detail == "" {
		detail = "The provider rejected the request."
	}
	result := prefix + ": " + detail
	if requestID != "" && !strings.Contains(result, requestID) {
		result += " (Request ID: " + requestID + ")"
	}
	runes := []rune(result)
	if len(runes) > maximumProviderMessageRunes {
		result = string(runes[:maximumProviderMessageRunes])
	}
	return result
}

func sanitizeProviderField(value string, secrets []string, maximum int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Join(strings.Fields(value), " ")
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = credentialPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[REDACTED]"
		}
		if strings.HasPrefix(strings.ToLower(match), "bearer ") {
			return match[:7] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
	value = dataURLPattern.ReplaceAllString(value, "[REDACTED DATA URL]")
	value = httpURLPattern.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil {
			return "[REDACTED URL]"
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	})
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum]) + "…"
	}
	return value
}
