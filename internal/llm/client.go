// Package llm defines a small, provider-agnostic interface for optional
// transcript analysis. Offline is the safe default; network access is opt-in.
package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderOffline = "offline"
	ProviderOpenAI  = "openai"
)

// Config controls provider selection. Empty Provider means offline, except
// when all OpenAI environment values are explicitly present in NewFromEnv.
type Config struct {
	Provider   string
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	Timeout    time.Duration
	MaxRetries int // extra attempts after the first; 0 means 2, negative means none
}

// Message is a redacted transcript message sent to a provider.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest contains untrusted transcript data. The implementation
// redacts every message before serialization and wraps it as data, not prompt
// instructions. Schema is optional and defaults to SignalSchema.
type CompletionRequest struct {
	Transcript []Message       `json:"transcript"`
	Prompt     string          `json:"prompt,omitempty"`
	Schema     json.RawMessage `json:"schema,omitempty"`
}

// CompletionResponse is always JSON validated before it is returned.
type CompletionResponse struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	JSON     json.RawMessage `json:"json"`
}

// Client is the pluggable provider interface. Complete must return a
// schema-valid JSON object or an error; it never returns an untrusted raw body.
type Client interface {
	Complete(context.Context, CompletionRequest) (CompletionResponse, error)
}

type offlineFlag interface {
	Offline() bool
}

// New constructs a client. Offline is used for an empty provider; only
// ProviderOpenAI enables HTTP requests.
func New(config Config) (Client, error) {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	if provider == "" {
		provider = ProviderOffline
	}
	switch provider {
	case ProviderOffline:
		return &OfflineClient{}, nil
	case ProviderOpenAI:
		return newOpenAIClient(config)
	default:
		return nil, invalidProvider(config.Provider)
	}
}

// NewFromEnv selects the provider from environment. It remains offline unless
// SESSION_FINDER_LLM_PROVIDER/LLM_PROVIDER is set to openai, or the complete
// OpenAI tuple (base URL, key, model) is present.
func NewFromEnv() (Client, error) {
	provider := firstEnv("SESSION_FINDER_LLM_PROVIDER", "LLM_PROVIDER")
	baseURL := firstEnv("SESSION_FINDER_LLM_BASE_URL", "OPENAI_BASE_URL", "LLM_BASE_URL")
	apiKey := firstEnv("SESSION_FINDER_LLM_API_KEY", "OPENAI_API_KEY", "LLM_API_KEY", "CLIRELAY_API_KEY")
	model := firstEnv("SESSION_FINDER_LLM_MODEL", "OPENAI_MODEL", "LLM_MODEL")
	if provider == "" && baseURL != "" && apiKey != "" && model != "" {
		provider = ProviderOpenAI
	}
	if provider == "" {
		provider = ProviderOffline
	}
	var timeout time.Duration
	if raw := firstEnv("SESSION_FINDER_LLM_TIMEOUT"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return New(Config{Provider: provider, BaseURL: baseURL, APIKey: apiKey, Model: model, Timeout: timeout})
}

// Default returns the environment-selected client. Misconfigured opt-in
// settings fall back to an offline client rather than triggering a request.
func Default() Client {
	client, err := NewFromEnv()
	if err != nil {
		return &OfflineClient{}
	}
	return client
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// IsOffline reports whether client is a no-network implementation.
func IsOffline(client Client) bool {
	if client == nil {
		return true
	}
	if flag, ok := client.(offlineFlag); ok {
		return flag.Offline()
	}
	return false
}
