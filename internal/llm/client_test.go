package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
