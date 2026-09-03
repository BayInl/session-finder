// Package llm defines a small, provider-agnostic interface for optional
// transcript analysis. Offline is the safe default; network access is opt-in.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderOffline = "offline"
	ProviderOpenAI  = "openai"
)

// Config controls provider selection and bounds online use. Empty Provider is
// always offline when passed to New. Zero budget values use conservative
// defaults; negative MaxCalls/MaxTotalTokens disable those two budgets.
type Config struct {
	Provider        string
	BaseURL         string
	APIKey          string
	Model           string
	HTTPClient      *http.Client
	Timeout         time.Duration
	MaxRetries      int // extra attempts after the first; 0 means 2, negative means none
	MaxRequestBytes int // serialized request limit; 0 uses the default
	MaxOutputTokens int // maximum tokens requested from one completion; 0 uses the default
	MaxCalls        int // provider HTTP call budget; 0 uses the default, negative disables it
	MaxTotalTokens  int // aggregate token budget; 0 uses the default, negative disables it
}

// UsageStats reports provider requests and token usage for one client. When a
// compatible endpoint omits usage, TotalTokens contains a conservative estimate.
type UsageStats struct {
	Calls           int64 `json:"calls"`
	SuccessfulCalls int64 `json:"successful_calls"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
	EstimatedCalls  int64 `json:"estimated_calls"`
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

// NewFromEnv selects the provider from environment. Automatic online selection
// only considers SESSION_FINDER_LLM_* variables. Legacy generic variables are
// accepted only after an explicit provider opt-in and emit one process warning.
func NewFromEnv() (Client, error) {
	provider, providerSource := firstEnvSource("SESSION_FINDER_LLM_PROVIDER", "LLM_PROVIDER")
	explicitProvider := provider != ""

	baseURL, baseSource := firstEnvSource("SESSION_FINDER_LLM_BASE_URL")
	apiKey, keySource := firstEnvSource("SESSION_FINDER_LLM_API_KEY")
	model, modelSource := firstEnvSource("SESSION_FINDER_LLM_MODEL")
	if !explicitProvider && baseURL != "" && apiKey != "" && model != "" {
		provider = ProviderOpenAI
	}
	if provider == "" {
		provider = ProviderOffline
	}
	if explicitProvider && strings.EqualFold(strings.TrimSpace(provider), ProviderOpenAI) {
		if baseURL == "" {
			baseURL, baseSource = firstEnvSource("OPENAI_BASE_URL", "LLM_BASE_URL")
		}
		if apiKey == "" {
			apiKey, keySource = firstEnvSource("OPENAI_API_KEY", "LLM_API_KEY", "CLIRELAY_API_KEY")
		}
		if model == "" {
			model, modelSource = firstEnvSource("OPENAI_MODEL", "LLM_MODEL")
		}
		if isLegacyEnv(providerSource) || isLegacyEnv(baseSource) || isLegacyEnv(keySource) || isLegacyEnv(modelSource) {
			warnLegacyEnv()
		}
	}

	return New(Config{
		Provider:        provider,
		BaseURL:         baseURL,
		APIKey:          apiKey,
		Model:           model,
		Timeout:         envDurationSeconds("SESSION_FINDER_LLM_TIMEOUT"),
		MaxRequestBytes: envPositiveInt("SESSION_FINDER_LLM_MAX_REQUEST_BYTES"),
		MaxOutputTokens: envPositiveInt("SESSION_FINDER_LLM_MAX_OUTPUT_TOKENS"),
		MaxCalls:        envPositiveInt("SESSION_FINDER_LLM_MAX_CALLS"),
		MaxTotalTokens:  envPositiveInt("SESSION_FINDER_LLM_MAX_TOTAL_TOKENS"),
	})
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

func firstEnvSource(names ...string) (string, string) {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, name
		}
	}
	return "", ""
}

func envPositiveInt(name string) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func envDurationSeconds(name string) time.Duration {
	seconds := envPositiveInt(name)
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func isLegacyEnv(name string) bool {
	return name != "" && !strings.HasPrefix(name, "SESSION_FINDER_LLM_")
}

var (
	legacyEnvWarning sync.Once
	warningWriter    io.Writer = os.Stderr
)

func warnLegacyEnv() {
	legacyEnvWarning.Do(func() {
		_, _ = fmt.Fprintln(warningWriter, "warning: generic LLM environment variables are deprecated; use SESSION_FINDER_LLM_* to make transcript upload configuration explicit")
	})
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

// Usage returns a snapshot of provider usage. Clients without usage tracking
// report zero values.
func Usage(client Client) UsageStats {
	if reporter, ok := client.(interface{ Usage() UsageStats }); ok {
		return reporter.Usage()
	}
	return UsageStats{}
}
