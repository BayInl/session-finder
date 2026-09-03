package llm

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	redactPrivateKeyRE   = regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)
	redactBearerRE       = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	redactTokenRE        = regexp.MustCompile(`(?i)\b(?:sk|pk|rk)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\b(?:ghp|github_pat|glpat|xox[baprs])[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\bAKIA[0-9A-Z]{16}\b`)
	redactSlackWebhookRE = regexp.MustCompile(`(?i)\bhttps?://hooks\.slack\.com/services/[^\s"'<>]+`)
	redactJWTRE          = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	redactAssignmentRE   = regexp.MustCompile(`(?i)\b[A-Za-z0-9_]*(?:api[_-]?key|access[_-]?key|secret|token|password|passwd)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s&,;]+)`)
	redactEmailRE        = regexp.MustCompile(`\b[A-Za-z0-9._%+!-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	redactUnixPathRE     = regexp.MustCompile(`(?:^|[\s"'=(])(?:file://)?(?:~/|/(?:Users|home|var/folders|private/var|tmp)/)[^\s"'<>]+`)
	redactWinPathRE      = regexp.MustCompile(`(?i)\b[A-Z]:\\[^\s"'<>]+`)
	redactURLCredRE      = regexp.MustCompile(`(?i)(://[^:/\s]+:)[^@\s]+@`)
)

// RedactRequest returns a deep-copied request with transcript and prompt values
// redacted before they can be serialized or sent externally.
func RedactRequest(request CompletionRequest) CompletionRequest {
	result := request
	result.Transcript = make([]Message, len(request.Transcript))
	for i, message := range request.Transcript {
		result.Transcript[i] = Message{Role: Redact(message.Role), Content: Redact(message.Content)}
	}
	result.Prompt = Redact(request.Prompt)
	if len(request.Schema) > 0 {
		result.Schema = append(json.RawMessage(nil), request.Schema...)
	}
	return result
}

// Redact removes high-confidence secrets and personal identifiers while
// preserving surrounding transcript structure. Replacement markers are stable
// for deterministic tests and safe provider prompts.
func Redact(text string) string {
	if text == "" {
		return ""
	}
	text = redactPrivateKeyRE.ReplaceAllString(text, "[REDACTED_PRIVATE_KEY]")
	text = redactBearerRE.ReplaceAllString(text, "Bearer [REDACTED_TOKEN]")
	text = redactTokenRE.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = redactSlackWebhookRE.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = redactJWTRE.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = redactAssignmentRE.ReplaceAllString(text, "[REDACTED_SECRET]")
	text = redactURLCredRE.ReplaceAllString(text, "$1[REDACTED_CREDENTIAL]@")
	text = redactEmailRE.ReplaceAllString(text, "[REDACTED_EMAIL]")
	text = redactUnixPathRE.ReplaceAllStringFunc(text, func(value string) string {
		prefix := ""
		if len(value) > 0 && strings.ContainsAny(value[:1], " \t\n\r\"'=(") {
			prefix = value[:1]
		}
		return prefix + "[REDACTED_PATH]"
	})
	text = redactWinPathRE.ReplaceAllString(text, "[REDACTED_PATH]")
	return text
}

// RedactTranscript redacts a transcript while preserving roles.
func RedactTranscript(messages []Message) []Message {
	result := make([]Message, len(messages))
	for i, message := range messages {
		result[i] = Message{Role: Redact(message.Role), Content: Redact(message.Content)}
	}
	return result
}
