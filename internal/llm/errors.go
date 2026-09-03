package llm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BayInl/session-finder/internal/fault"
)

var (
	ErrInvalidProvider     = fault.New(fault.KindConfig, "invalid llm provider")
	ErrSchemaViolation     = fault.New(fault.KindSchema, "llm response violates JSON schema")
	ErrMissingAPIKey       = fault.New(fault.KindConfig, "openai api key is required")
	ErrOfflineUnsupported  = fault.New(fault.KindOffline, "offline client supports only the signal schema")
	ErrEmptyResponse       = fault.New(fault.KindSchema, "openai response has no JSON message")
	ErrRateLimited         = fault.New(fault.KindNetwork, "llm provider rate limited")
	ErrInvalidSchema       = fault.New(fault.KindInvalid, "request schema must be valid JSON")
	ErrInvalidBaseURL      = fault.New(fault.KindConfig, "invalid openai base URL")
	ErrInsecureBaseURL     = fault.New(fault.KindConfig, "openai base URL must use HTTPS unless it is localhost")
	ErrRequestTooLarge     = fault.New(fault.KindInvalid, "llm request exceeds the configured size limit")
	ErrResponseTooLarge    = fault.New(fault.KindNetwork, "llm response exceeds the size limit")
	ErrCallBudgetExceeded  = fault.New(fault.KindConfig, "llm call budget exceeded")
	ErrTokenBudgetExceeded = fault.New(fault.KindConfig, "llm token budget exceeded")
)

// APIError is a provider HTTP failure. Body is compacted and may be empty.
type APIError struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	status := e.Message
	if status == "" {
		status = strconv.Itoa(e.StatusCode)
	}
	if e.Body == "" {
		return "openai request failed (" + status + ")"
	}
	return "openai request failed (" + status + "): " + e.Body
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.StatusCode == 429 {
		return ErrRateLimited
	}
	return nil
}

func (e *APIError) Kind() fault.Kind { return fault.KindNetwork }

func newAPIError(statusCode int, status, body string) error {
	msg := Redact(strings.TrimSpace(status))
	if msg == "" {
		msg = strconv.Itoa(statusCode)
	}
	return &APIError{StatusCode: statusCode, Message: msg, Body: compactBody([]byte(Redact(body)))}
}

func invalidProvider(id string) error {
	return fmt.Errorf("%w: %q", ErrInvalidProvider, Redact(id))
}

func invalidBaseURL(value string) error {
	return fmt.Errorf("%w %q", ErrInvalidBaseURL, redactURL(value))
}

func insecureBaseURL(value string) error {
	return fmt.Errorf("%w: %q", ErrInsecureBaseURL, redactURL(value))
}

func requestTooLarge(size, limit int) error {
	return fmt.Errorf("%w: %d bytes (limit %d)", ErrRequestTooLarge, size, limit)
}
