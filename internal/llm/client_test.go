package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BayInl/session-finder/internal/fault"
)

func TestDefaultIsOfflineAndDoesNotCallNetwork(t *testing.T) {
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !IsOffline(client) {
		t.Fatalf("client %T is not offline", client)
	}
	response, err := client.Complete(context.Background(), CompletionRequest{Transcript: []Message{{Role: "user", Content: "Looks good; go test passed."}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != ProviderOffline || len(response.JSON) == 0 {
		t.Fatalf("response = %+v", response)
	}
	var bundle map[string]any
	if err := json.Unmarshal(response.JSON, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle["recommended_action"] == nil {
		t.Fatalf("offline response = %#v", bundle)
	}
}

func TestRedactRemovesSecretsPIIAndPaths(t *testing.T) {
	text := `curl -H "Authorization: Bearer abcdefghijklmnop" https://alice:password@example.com/v1 \
--token sk-test_12345678901234567890 --file /Users/alice/project/main.go --file ~/project/main.go email alice@example.com \
OPENAI_API_KEY=generic-value CLIENT_SECRET="multi word secret"
-----BEGIN PRIVATE KEY-----
secret
-----END PRIVATE KEY-----`
	redacted := Redact(text)
	for _, secret := range []string{"abcdefghijklmnop", "password", "sk-test_12345678901234567890", "/Users/alice/project/main.go", "alice@example.com", "secret", "generic-value", "multi word secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted text contains %q: %s", secret, redacted)
		}
	}
	for _, marker := range []string{"[REDACTED_TOKEN]", "[REDACTED_CREDENTIAL]", "[REDACTED_PATH]", "[REDACTED_EMAIL]", "[REDACTED_PRIVATE_KEY]", "[REDACTED_SECRET]"} {
		if !strings.Contains(redacted, marker) {
			t.Fatalf("redacted text lacks %q: %s", marker, redacted)
		}
	}
}

func TestRedactSupportsUnderscoreSecretsSlackAndJWT(t *testing.T) {
	secrets := []string{
		"ghp_1234567890abcdef",
		"github_pat_1234567890abcdef",
		"sk_live_1234567890abcdef",
		"rk_live_1234567890abcdef",
		"https://hooks.slack.com/services/T00000000/B00000000/abcdefghijklmnop",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.signature12345",
	}
	redacted := Redact(strings.Join(secrets, "\n"))
	for _, secret := range secrets {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted text contains %q: %s", secret, redacted)
		}
	}
	if got := strings.Count(redacted, "[REDACTED_TOKEN]"); got != len(secrets) {
		t.Fatalf("redacted token marker count = %d, want %d: %s", got, len(secrets), redacted)
	}
}

func TestRedactRequestRedactsSchemaStringsAndKeepsValidJSON(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","description":"send token=supersecret to /Users/alice/private","properties":{"value":{"type":"string"}}}`)
	redacted := RedactRequest(CompletionRequest{Schema: schema})
	if !json.Valid(redacted.Schema) {
		t.Fatalf("redacted schema is invalid: %s", redacted.Schema)
	}
	if strings.Contains(string(redacted.Schema), "supersecret") || strings.Contains(string(redacted.Schema), "/Users/alice/private") {
		t.Fatalf("schema leaked sensitive values: %s", redacted.Schema)
	}
}
func TestOpenAICompatibleClientRedactsBeforeRequestAndValidatesSchema(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"{\"intent_kind\":\"workflow\",\"confidence\":0.8,\"success_evidence\":[\"acceptance\"],\"one_off_risk\":0.1,\"secret_risk\":0,\"recommended_action\":\"draft\"}"}}]}`))
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), CompletionRequest{
		Transcript: []Message{{Role: "user", Content: "Use /Users/alice/project and token sk-test_12345678901234567890"}},
		Prompt:     "Decide for alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != ProviderOpenAI || response.Model != "test-model" {
		t.Fatalf("response = %+v", response)
	}
	body := string(requestBody)
	for _, secret := range []string{"/Users/alice/project", "sk-test_12345678901234567890", "alice@example.com"} {
		if strings.Contains(body, secret) {
			t.Fatalf("request body leaked %q: %s", secret, body)
		}
	}
	for _, marker := range []string{"[REDACTED_PATH]", "[REDACTED_TOKEN]", "[REDACTED_EMAIL]", "json_schema", "strict"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("request body lacks %q: %s", marker, body)
		}
	}
}

func TestOpenAIResponseRejectsUnknownOrMissingFields(t *testing.T) {
	for _, content := range []string{
		`{"intent_kind":"workflow"}`,
		`{"intent_kind":"workflow","confidence":0.8,"success_evidence":[],"one_off_risk":0,"secret_risk":0,"recommended_action":"draft","extra":true}`,
		`{"intent_kind":"workflow","confidence":2,"success_evidence":[],"one_off_risk":0,"secret_risk":0,"recommended_action":"draft"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":` + jsonString(content) + `}}]}`))
		}))
		client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "model"})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		if _, err := client.Complete(context.Background(), CompletionRequest{}); err == nil || !strings.Contains(err.Error(), ErrSchemaViolation.Error()) {
			server.Close()
			t.Fatalf("content %s error = %v", content, err)
		}
		server.Close()
	}
}

func TestOpenAIClientValidatesCustomSchemaAndRejectsMalformedResponses(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["disposition","confidence","reason_codes"],"properties":{"disposition":{"type":"string","enum":["draft","review"]},"confidence":{"type":"number","minimum":0,"maximum":1},"reason_codes":{"type":"array","items":{"type":"string"}}}}`)
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid", content: `{"disposition":"draft","confidence":0.8,"reason_codes":["clear"]}`},
		{name: "missing", content: `{"disposition":"draft","confidence":0.8}`, wantErr: true},
		{name: "unknown", content: `{"disposition":"draft","confidence":0.8,"reason_codes":[],"extra":true}`, wantErr: true},
		{name: "out of range", content: `{"disposition":"draft","confidence":2,"reason_codes":[]}`, wantErr: true},
		{name: "trailing", content: `{"disposition":"draft","confidence":0.8,"reason_codes":[]} {}`, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					ResponseFormat struct {
						JSONSchema struct {
							Schema json.RawMessage `json:"schema"`
						} `json:"json_schema"`
					} `json:"response_format"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if string(body.ResponseFormat.JSONSchema.Schema) != string(schema) {
					t.Errorf("schema = %s, want %s", body.ResponseFormat.JSONSchema.Schema, schema)
				}
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":` + jsonString(testCase.content) + `}}]}`))
			}))
			defer server.Close()
			client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Complete(context.Background(), CompletionRequest{Schema: schema})
			if testCase.wantErr && err == nil {
				t.Fatalf("response accepted malformed content")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("valid response error = %v", err)
			}
		})
	}
}

func TestValidateJSONSchemaRejectsTrailingAndSupportsNestedStrictFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"integer"}}}}}}`)
	for _, value := range []string{
		`{"items":[{"id":1}]}`,
		`{"items":[{"id":1}]} {}`,
		`{"items":[{"id":1,"extra":true}]}`,
		`{"items":[{"id":1.5}]}`,
	} {
		err := ValidateJSONSchema([]byte(value), schema)
		if strings.HasSuffix(value, `}`) && value == `{"items":[{"id":1}]}` {
			if err != nil {
				t.Fatalf("valid nested document error = %v", err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("malformed nested document accepted: %s", value)
		}
	}
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestNewFromEnvRequiresSessionFinderTupleOrExplicitProvider(t *testing.T) {
	clearLLMEnv(t)
	var warnings strings.Builder
	oldWriter := warningWriter
	legacyEnvWarning = sync.Once{}
	warningWriter = &warnings
	t.Cleanup(func() {
		legacyEnvWarning = sync.Once{}
		warningWriter = oldWriter
	})
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !IsOffline(client) {
		t.Fatalf("empty env client %T", client)
	}

	t.Setenv("OPENAI_BASE_URL", "https://example.test/v1")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_MODEL", "unit-model")
	client, err = NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !IsOffline(client) {
		t.Fatal("generic OpenAI tuple must not silently enable transcript upload")
	}

	t.Setenv("SESSION_FINDER_LLM_PROVIDER", "openai")
	client, err = NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if IsOffline(client) {
		t.Fatal("explicit provider should permit legacy OpenAI tuple")
	}
	if _, err := NewFromEnv(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(warnings.String(), "generic LLM environment variables"); got != 1 {
		t.Fatalf("legacy warning count = %d: %q", got, warnings.String())
	}

	clearLLMEnv(t)
	t.Setenv("SESSION_FINDER_LLM_BASE_URL", "https://example.test/v1")
	t.Setenv("SESSION_FINDER_LLM_API_KEY", "sk-test")
	t.Setenv("SESSION_FINDER_LLM_MODEL", "unit-model")
	client, err = NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if IsOffline(client) {
		t.Fatal("complete SESSION_FINDER_LLM tuple should promote online")
	}

	clearLLMEnv(t)
	t.Setenv("SESSION_FINDER_LLM_PROVIDER", "openai")
	t.Setenv("SESSION_FINDER_LLM_BASE_URL", "https://example.test/v1")
	t.Setenv("SESSION_FINDER_LLM_API_KEY", "sk-test")
	t.Setenv("SESSION_FINDER_LLM_MODEL", "unit-model")
	t.Setenv("SESSION_FINDER_LLM_TIMEOUT", "7")
	t.Setenv("SESSION_FINDER_LLM_MAX_REQUEST_BYTES", "1234")
	t.Setenv("SESSION_FINDER_LLM_MAX_OUTPUT_TOKENS", "55")
	t.Setenv("SESSION_FINDER_LLM_MAX_CALLS", "3")
	t.Setenv("SESSION_FINDER_LLM_MAX_TOTAL_TOKENS", "4567")
	client, err = NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	configured := client.(*openAIClient)
	if configured.timeout != 7*time.Second || configured.maxRequestBytes != 1234 || configured.maxOutputTokens != 55 || configured.maxCalls != 3 || configured.maxTotalTokens != 4567 {
		t.Fatalf("environment budgets not applied: %+v", configured)
	}

	t.Setenv("SESSION_FINDER_LLM_PROVIDER", "anthropic")
	_, err = NewFromEnv()
	if !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("invalid provider error = %v", err)
	}
	if fault.KindOf(err) != fault.KindConfig {
		t.Fatalf("kind = %q", fault.KindOf(err))
	}

	clearLLMEnv(t)
	t.Setenv("SESSION_FINDER_LLM_PROVIDER", "openai")
	_, err = NewFromEnv()
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("missing key error = %v", err)
	}
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SESSION_FINDER_LLM_PROVIDER", "LLM_PROVIDER",
		"SESSION_FINDER_LLM_BASE_URL", "OPENAI_BASE_URL", "LLM_BASE_URL",
		"SESSION_FINDER_LLM_API_KEY", "OPENAI_API_KEY", "LLM_API_KEY", "CLIRELAY_API_KEY",
		"SESSION_FINDER_LLM_MODEL", "OPENAI_MODEL", "LLM_MODEL",
		"SESSION_FINDER_LLM_TIMEOUT", "SESSION_FINDER_LLM_MAX_REQUEST_BYTES",
		"SESSION_FINDER_LLM_MAX_OUTPUT_TOKENS", "SESSION_FINDER_LLM_MAX_CALLS",
		"SESSION_FINDER_LLM_MAX_TOTAL_TOKENS",
	} {
		t.Setenv(name, "")
	}
}

func TestOfflineRejectsCustomSchema(t *testing.T) {
	client := NewOffline()
	_, err := client.Complete(context.Background(), CompletionRequest{
		Schema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, ErrOfflineUnsupported) {
		t.Fatalf("custom schema error = %v", err)
	}
	if _, err := client.Complete(context.Background(), CompletionRequest{Schema: SignalSchema()}); err != nil {
		t.Fatalf("signal schema error = %v", err)
	}
}

func TestIsOfflineNilAndOpenAI(t *testing.T) {
	if !IsOffline(nil) {
		t.Fatal("nil client should be offline")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if IsOffline(client) {
		t.Fatal("openai client must not report offline")
	}
}

func TestOpenAIRejectsRemoteHTTPAndRedactsBaseURLError(t *testing.T) {
	_, err := New(Config{Provider: ProviderOpenAI, BaseURL: "http://alice:secret@example.test/private?api_key=hidden", APIKey: "key"})
	if !errors.Is(err, ErrInvalidBaseURL) {
		t.Fatalf("credential URL error = %v", err)
	}
	for _, secret := range []string{"alice", "secret", "private", "hidden"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("base URL error leaked %q: %v", secret, err)
		}
	}

	_, err = New(Config{Provider: ProviderOpenAI, BaseURL: "http://example.test/private", APIKey: "key"})
	if !errors.Is(err, ErrInsecureBaseURL) {
		t.Fatalf("remote HTTP error = %v", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("insecure URL error leaked details: %v", err)
	}
}

func TestOpenAIRefusesRedirectsWithAuthorization(t *testing.T) {
	var redirectedHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedHits.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: source.URL, APIKey: "key", MaxRetries: -1, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), CompletionRequest{})
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect error = %v", err)
	}
	if redirectedHits.Load() != 0 {
		t.Fatalf("authorization-bearing redirect reached target %d times", redirectedHits.Load())
	}
}

func TestOpenAIRequestLimitPreventsNetworkCall(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", MaxRequestBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), CompletionRequest{Transcript: []Message{{Role: "user", Content: strings.Repeat("x", 512)}}})
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("request limit error = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("oversize request made %d calls", hits.Load())
	}
}

func TestOpenAITotalDeadlineIncludesRetrySleep(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Timeout: 30 * time.Millisecond, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.Complete(context.Background(), CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("deadline did not cancel retry sleep")
	}
	if hits.Load() != 1 {
		t.Fatalf("deadline allowed %d calls", hits.Load())
	}
}

func TestOpenAITracksUsageAndEnforcesBudgets(t *testing.T) {
	var requestBody map[string]any
	var hits atomic.Int32
	response := `{"choices":[{"message":{"content":"{\"intent_kind\":\"workflow\",\"confidence\":0.8,\"success_evidence\":[],\"one_off_risk\":0,\"secret_risk\":0,\"recommended_action\":\"draft\"}"}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", MaxOutputTokens: 64, MaxCalls: 1, MaxTotalTokens: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if requestBody["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens = %#v", requestBody["max_tokens"])
	}
	usageClient, ok := client.(interface{ Usage() UsageStats })
	if !ok {
		t.Fatalf("client %T has no Usage method", client)
	}
	usage := usageClient.Usage()
	if usage.Calls != 1 || usage.SuccessfulCalls != 1 || usage.InputTokens != 20 || usage.OutputTokens != 10 || usage.TotalTokens != 30 || usage.EstimatedCalls != 0 {
		t.Fatalf("usage = %+v", usage)
	}
	if _, err := client.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCallBudgetExceeded) {
		t.Fatalf("call budget error = %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("budget allowed %d calls", hits.Load())
	}

	tokenLimited, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", MaxOutputTokens: 64, MaxTotalTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokenLimited.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("token budget error = %v", err)
	}
}

func TestOpenAIRetriesThenSucceeds(t *testing.T) {
	oldSleep := sleepContext
	sleepContext = func(context.Context, time.Duration) error { return nil }
	defer func() { sleepContext = oldSleep }()

	var hits atomic.Int32
	ok := `{"choices":[{"message":{"content":"{\"intent_kind\":\"workflow\",\"confidence\":0.8,\"success_evidence\":[\"acceptance\"],\"one_off_risk\":0.1,\"secret_risk\":0,\"recommended_action\":\"draft\"}"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(ok))
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "m", MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIDoesNotRetryClientErrors(t *testing.T) {
	oldSleep := sleepContext
	sleepContext = func(context.Context, time.Duration) error { return nil }
	defer func() { sleepContext = oldSleep }()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad schema"}`))
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	err = mustCompleteErr(t, client)
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestOpenAIFallsBackToJSONObject(t *testing.T) {
	oldSleep := sleepContext
	sleepContext = func(context.Context, time.Duration) error { return nil }
	defer func() { sleepContext = oldSleep }()

	var hits atomic.Int32
	ok := `{"choices":[{"message":{"content":"{\"intent_kind\":\"workflow\",\"confidence\":0.8,\"success_evidence\":[\"acceptance\"],\"one_off_risk\":0.1,\"secret_risk\":0,\"recommended_action\":\"draft\"}"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := hits.Add(1)
		if n == 1 {
			if !strings.Contains(string(body), "json_schema") {
				t.Errorf("first request should use json_schema: %s", body)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"response_format json_schema is not supported"}`))
			return
		}
		if !strings.Contains(string(body), `"json_object"`) {
			t.Errorf("second request should use json_object: %s", body)
		}
		_, _ = w.Write([]byte(ok))
	}))
	defer server.Close()
	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: server.URL, APIKey: "key", Model: "m", MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestDecodeSegmentsNormalizesTurns(t *testing.T) {
	got, err := DecodeSegments([]byte(`{"turns":[{"index":1,"decision":"SAME"},{"index":0,"decision":"new"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 || got.Turns[0].Index != 0 || got.Turns[1].Decision != SegmentDecisionSame {
		t.Fatalf("%#v", got.Turns)
	}
}

func TestAPIErrorKindRateLimitAndRedaction(t *testing.T) {
	err := newAPIError(429, "429 Too Many Requests", `{"error":"token=supersecret for alice@example.com at /Users/alice/private"}`)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("unwrap = %v", err)
	}
	if fault.KindOf(err) != fault.KindNetwork {
		t.Fatalf("kind = %q", fault.KindOf(err))
	}
	for _, secret := range []string{"supersecret", "alice@example.com", "/Users/alice/private"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("API error leaked %q: %v", secret, err)
		}
	}
}

func mustCompleteErr(t *testing.T, client Client) error {
	t.Helper()
	_, err := client.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	return err
}
