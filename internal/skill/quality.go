package skill

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/record"
)

const (
	// MinimumSuccessEvidence is the minimum number of independent success
	// signals required before a transcript may become a draft skill.
	MinimumSuccessEvidence = 1
	// HighOneOffRisk suppresses ephemeral fixes instead of sending them to review.
	HighOneOffRisk = 0.80
	// HighSecretRisk is intentionally lower than the extract engine's suppress
	// threshold so publication never relies on a borderline secret heuristic.
	HighSecretRisk = 0.75
)

var (
	acceptanceEvidenceRE       = regexp.MustCompile(`(?i)\b(yes|yep|yeah|correct|right|looks good|good|great|perfect|thanks|thank you|works|working|approved|accept|ship it|done)\b|可以|正确|对的|很好|完美|谢谢|通过|批准|发布|搞定|没问题`)
	injectedNoiseRE            = regexp.MustCompile(`(?is)^\s*(?:<environment_context\b|<cwd\b|<system(?:-reminder)?\b|<user_info\b|<permissions(?:\s+instructions)?\b|\[system(?:-reminder)?\]|\[environment_context\b|(?:#+\s*)?agents\.md\b|(?:#+\s*)?context\s+from\s+my\s+ide\s+setup\b|#+\s+(?:skills|available\s+skills)\b|(?:you must|must follow|do not|don't|never)\b)`)
	testSuccessEvidenceRE      = regexp.MustCompile(`(?i)(?:\b(?:go test|cargo test|pytest|npm test|yarn test|pnpm test|vitest|jest|gradle test|mvn test)\b[^\n]*\b(?:pass|passed|ok|success|successful|green)\b)|(?:\b(?:all tests?|tests?)\b[^\n]*(?:pass|passed|ok|success|successful|green)\b)|(?:测试(?:全部|都)?通过)|(?:\b0\s+(?:failures?|failed)\b)|(?m)^\s*(?:ok|PASS)\b`)
	secretTokenEvidenceRE      = regexp.MustCompile(`(?i)\b(?:sk|pk|rk)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\b(?:ghp|github_pat|xox[baprs]|glpat)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\bAKIA[0-9A-Z]{16}\b|\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`)
	secretAssignmentEvidenceRE = regexp.MustCompile(`(?i)\b[A-Za-z0-9_]*(?:api[_-]?key|access[_-]?key|secret|token|password|passwd)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s&,;]+)`)
	slackWebhookEvidenceRE     = regexp.MustCompile(`(?i)\bhttps?://hooks\.slack\.com/services/[^\s"'<>]+`)
	jwtEvidenceRE              = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	privateKeyEvidenceRE       = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)
)

// EvaluateQuality applies the non-negotiable suppression rules from the skill
// compiler contract. It is deterministic and safe to run without an LLM.
func EvaluateQuality(signals extract.SignalBundle) QualityReport {
	reasons := []string{}
	if len(signals.SuccessEvidence) < MinimumSuccessEvidence {
		reasons = append(reasons, "insufficient success evidence")
	}
	if signals.OneOffRisk >= HighOneOffRisk {
		reasons = append(reasons, fmt.Sprintf("one-off risk %.2f is high", signals.OneOffRisk))
	}
	if signals.SecretRisk >= HighSecretRisk {
		reasons = append(reasons, fmt.Sprintf("secret risk %.2f is high", signals.SecretRisk))
	}
	if len(reasons) > 0 {
		return QualityReport{Disposition: QualitySuppress, Reasons: reasons, Signals: signals}
	}
	// Confidence is the only quality score exposed by the local signal engine.
	// Keep it in [0,1] and reward independent evidence without overfitting.
	score := signals.Confidence
	if len(signals.SuccessEvidence) > 1 {
		score += minFloat(0.15, float64(len(signals.SuccessEvidence)-1)*0.05)
	}
	if score > 1 {
		score = 1
	}
	return QualityReport{Disposition: QualityDraft, Score: score, Reasons: []string{}, Signals: signals}
}

// QualityGateMessages analyzes and gates a normalized transcript in one call.
func QualityGateMessages(messages []record.MessageRecord) QualityReport {
	return EvaluateQuality(extract.Analyze(messages))
}

// IsSuppressed reports whether a quality report must not proceed to review or
// publication without a new, human-approved candidate.
func IsSuppressed(report QualityReport) bool { return report.Disposition == QualitySuppress }

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

// evidenceForMessages identifies bounded, local-only evidence pointers. It
// intentionally records summaries, not full transcript text.
func evidenceForMessages(messages []record.MessageRecord) []EvidenceBlock {
	result := []EvidenceBlock{}
	seen := map[string]bool{}
	for i, message := range messages {
		text := strings.TrimSpace(message.Text)
		if text == "" || strings.EqualFold(message.Role, "system") || isInjectedNoiseText(text) {
			continue
		}
		kinds := make([]string, 0, 2)
		if strings.EqualFold(message.Role, "user") && acceptanceEvidenceRE.MatchString(text) {
			kinds = append(kinds, extract.EvidenceAcceptance)
		}
		if testSuccessEvidenceRE.MatchString(text) {
			kinds = append(kinds, extract.EvidenceTestsPassed)
		}
		for _, kind := range kinds {
			key := kind + ":" + fmt.Sprint(i)
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, EvidenceBlock{
				ID:           fmt.Sprintf("evidence-%d", len(result)+1),
				Kind:         kind,
				SessionID:    message.SessionID,
				MessageIndex: i,
				Role:         strings.ToLower(strings.TrimSpace(message.Role)),
				SourcePath:   message.SourcePath,
				Summary:      messageSummary(message, 240),
				Excerpt:      "",
			})
		}
	}
	return result
}

// isInjectedNoiseText identifies common system/environment/AGENTS.md blocks
// that should not influence skill metadata or instructions.
func isInjectedNoiseText(text string) bool {
	return injectedNoiseRE.MatchString(strings.TrimSpace(text))
}

// SensitiveInformation reports whether text contains a high-confidence secret.
// Paths, emails, and ordinary identifiers are intentionally not considered
// sensitive by this check.
func SensitiveInformation(text string) bool {
	return secretTokenEvidenceRE.MatchString(text) ||
		secretAssignmentEvidenceRE.MatchString(text) ||
		slackWebhookEvidenceRE.MatchString(text) ||
		jwtEvidenceRE.MatchString(text) ||
		privateKeyEvidenceRE.MatchString(text)
}

// ContainsSensitiveInformation scans the skill-facing fields and returns true
// if any text would make publication unsafe.
func ContainsSensitiveInformation(bundle CandidateBundle) bool {
	parts := []string{bundle.Slug, bundle.Trigger, bundle.Instructions}
	for _, evidence := range bundle.Evidence {
		parts = append(parts, evidence.Summary, evidence.Excerpt, evidence.ReviewerNote)
	}
	for _, part := range parts {
		if SensitiveInformation(part) {
			return true
		}
	}
	return false
}

// ValidateQualityForPublish enforces quality and content safety together.
func ValidateQualityForPublish(bundle CandidateBundle) error {
	if bundle.Quality.Disposition == QualitySuppress {
		return ErrQualitySuppressed
	}
	if len(bundle.Quality.Signals.SuccessEvidence) < MinimumSuccessEvidence {
		return ErrQualitySuppressed
	}
	if bundle.Quality.Signals.OneOffRisk >= HighOneOffRisk {
		return ErrQualitySuppressed
	}
	if bundle.Quality.Signals.SecretRisk >= HighSecretRisk || ContainsSensitiveInformation(bundle) {
		return ErrSensitiveContent
	}
	return nil
}

// SortEvidence returns deterministic evidence ordering for JSON/UI output.
func SortEvidence(values []EvidenceBlock) []EvidenceBlock {
	result := append([]EvidenceBlock(nil), values...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].MessageIndex != result[j].MessageIndex {
			return result[i].MessageIndex < result[j].MessageIndex
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// cleanInstructionText removes transcript artifacts while preserving useful
// Markdown instructions.
func cleanInstructionText(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || isInjectedNoiseText(line) || strings.HasPrefix(line, "<system") || strings.HasPrefix(line, "[system") {
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func firstUserMessage(messages []record.MessageRecord) (record.MessageRecord, bool) {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") && strings.TrimSpace(message.Text) != "" && !isInjectedNoiseText(message.Text) {
			return message, true
		}
	}
	return record.MessageRecord{}, false
}

func instructionFromMessages(messages []record.MessageRecord) string {
	parts := make([]string, 0, 4)
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		text := cleanInstructionText(message.Text)
		if text == "" {
			continue
		}
		if len([]rune(text)) > 800 {
			text = string([]rune(text)[:799]) + "…"
		}
		parts = append(parts, text)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		if first, ok := firstUserMessage(messages); ok {
			return cleanInstructionText(first.Text)
		}
		return "Follow the validated workflow from the source session."
	}
	return strings.Join(parts, "\n\n")
}

// slugify converts a title/intent into the Agent Skills slug format.
func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var builder strings.Builder
	lastDash := false
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			// Keep the slug portable rather than transliterating locale-specific text.
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
