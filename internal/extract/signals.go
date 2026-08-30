package extract

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BayInl/session-finder/internal/record"
)

const (
	IntentUnknown       = "unknown"
	IntentCorrection    = "correction"
	IntentAcceptance    = "acceptance"
	IntentDecision      = "decision"
	IntentQuestion      = "question"
	IntentImplement     = "implementation"
	IntentWorkflow      = "workflow"
	ActionDraft         = "draft"
	ActionReview        = "review"
	ActionSuppress      = "suppress"
	EvidenceAcceptance  = "acceptance"
	EvidenceTestsPassed = "tests_passed"
)

var (
	correctionRE = regexp.MustCompile(`(?i)\b(no|not|wrong|incorrect|actually|instead|change|fix|retry|try again|again|re-run|rerun|doesn['’]?t work|failed)\b|不对|不正确|不是这样|改一下|修改|重试|再试|重新|失败|报错|应该`)
	acceptanceRE = regexp.MustCompile(`(?i)\b(yes|yep|yeah|correct|right|looks good|good|great|perfect|thanks|thank you|works|working|approved|accept|ship it|done)\b|可以|正确|对的|很好|完美|谢谢|通过|批准|发布|搞定|没问题`)
	decisionRE   = regexp.MustCompile(`(?i)\b(decide|decision|choose|choice|recommend|prefer|should we|use)\b|决定|选择|建议|采用|使用|取舍|方案`)
	questionRE   = regexp.MustCompile(`(?i)\?|\b(how|what|why|when|where|which|can|could)\b|怎么|如何|什么|为什么|是否|哪个`)
	workflowRE   = regexp.MustCompile(`(?i)\b(always|workflow|process|step[- ]by[- ]step|runbook|standard|reusable|repeatable|from now on)\b|以后|流程|步骤|规范|可复用|重复使用|长期`)
	oneOffRE     = regexp.MustCompile(`(?i)\b(one[- ]off|one time|just this once|temporary|temp|quick fix|throwaway)\b|一次性|临时|只要这次|快速修一下`)

	// Paths and ordinary PII are privacy concerns, but are not counted as
	// secret risk or quality failures. These patterns are high-confidence secrets.
	secretTokenRE      = regexp.MustCompile(`(?i)\b(?:sk|pk|rk)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\b(?:ghp|github_pat|xox[baprs]|glpat)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b|\bAKIA[0-9A-Z]{16}\b|\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`)
	secretAssignmentRE = regexp.MustCompile(`(?i)\b[A-Za-z0-9_]*(?:api[_-]?key|access[_-]?key|secret|token|password|passwd)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s&,;]+)`)
	slackWebhookRE     = regexp.MustCompile(`(?i)\bhttps?://hooks\.slack\.com/services/[^\s"'<>]+`)
	jwtRE              = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	privateKeyRE       = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)

	passTestRE = regexp.MustCompile(`(?i)(?:\b(?:go test|cargo test|pytest|npm test|yarn test|pnpm test|vitest|jest|gradle test|mvn test)\b[^\n]*\b(?:pass|passed|ok|success|successful|green)\b)|(?:\b(?:all tests?|tests?)\b[^\n]*(?:pass|passed|ok|success|successful|green)\b)|(?:测试(?:全部|都)?通过)|(?:\b0\s+(?:failures?|failed)\b)|(?m)^\s*(?:ok|PASS)\b`)
	failTestRE = regexp.MustCompile(`(?i)(?:\b(?:go test|cargo test|pytest|npm test|yarn test|pnpm test|vitest|jest|gradle test|mvn test)\b[^\n]*\b(?:fail|failed|failure|error|panic|did not pass|does not pass|doesn['’]?t pass|not passing)\b)|(?:\b(?:tests?|test suite)\b[^\n]*(?:fail|failed|failure|error|panic|did not pass|does not pass|doesn['’]?t pass|not passing)\b)|(?:测试失败|测试报错|panic|build failed)|(?m)^\s*FAIL\b`)
)

// SignalBundle is a probabilistic summary. It deliberately does not contain a
// binary accept/reject verdict: callers must use the recommendation as one
// input to human review and downstream policy.
type SignalBundle struct {
	IntentKind        string   `json:"intent_kind"`
	Confidence        float64  `json:"confidence"`
	SuccessEvidence   []string `json:"success_evidence"`
	OneOffRisk        float64  `json:"one_off_risk"`
	SecretRisk        float64  `json:"secret_risk"`
	RecommendedAction string   `json:"recommended_action"`
}

// SignalEngine applies deterministic, local heuristics to normalized records.
// It never performs network calls and never emits transcript text.
type SignalEngine struct{}

// NewSignalEngine constructs the default offline signal engine.
func NewSignalEngine() SignalEngine { return SignalEngine{} }

// Analyze is the package-level convenience API for the default engine.
func Analyze(messages []record.MessageRecord) SignalBundle {
	return NewSignalEngine().Analyze(messages)
}

// AnalyzeSession is a readable alias used by session-oriented callers.
func AnalyzeSession(messages []record.MessageRecord) SignalBundle { return Analyze(messages) }

// Analyze computes signal values from a normalized transcript. Noise and
// system-injected records are excluded before any transition heuristic runs.
func (SignalEngine) Analyze(messages []record.MessageRecord) SignalBundle {
	clean := filterSignalRecords(messages)
	if len(clean) == 0 {
		return SignalBundle{
			IntentKind: IntentUnknown, Confidence: 0, SuccessEvidence: []string{},
			OneOffRisk: 1, SecretRisk: 0, RecommendedAction: ActionSuppress,
		}
	}

	positive, negative := 0.0, 0.0
	pairAcceptance, pairCorrection := 0, 0
	decision, question, workflow, implementation := 0, 0, 0, 0
	passTests, failTests := 0, 0
	evidence := map[string]bool{}
	textParts := make([]string, 0, len(clean))
	for _, message := range clean {
		text := strings.TrimSpace(message.Text)
		textParts = append(textParts, text)
		if decisionRE.MatchString(text) {
			decision++
		}
		if questionRE.MatchString(text) {
			question++
		}
		if workflowRE.MatchString(text) {
			workflow++
		}
		if looksLikeImplementation(text) {
			implementation++
		}
		passed := passTestRE.MatchString(text)
		failed := failTestRE.MatchString(text)
		if passed && !failed {
			passTests++
			evidence[EvidenceTestsPassed] = true
		}
		if failed {
			failTests++
		}
	}
	for i := 1; i < len(clean); i++ {
		previous, current := clean[i-1], clean[i]
		if previous.Role != "assistant" || current.Role != "user" {
			continue
		}
		text := strings.TrimSpace(current.Text)
		switch {
		case correctionRE.MatchString(text):
			// A correction/retry after an assistant answer is a negative
			// quality signal and should normally receive human review.
			pairCorrection++
			negative += 1.25
		case acceptanceRE.MatchString(text):
			pairAcceptance++
			positive += 1.0
			evidence[EvidenceAcceptance] = true
		}
	}
	// Explicit validation reports are useful even without an assistant→user
	// transition. A failed test is evidence against success; it is not a veto.
	positive += float64(passTests) * 0.9
	negative += float64(failTests) * 1.0

	intent := inferIntent(pairAcceptance, pairCorrection, decision, question, workflow, implementation, passTests, failTests)
	secretRisk := riskForSecrets(textParts)
	oneOffRisk := riskForOneOff(clean, pairAcceptance, pairCorrection, workflow, passTests)
	confidence := signalConfidence(clean, positive, negative, intent, secretRisk)
	recommended := recommendation(clean, intent, confidence, oneOffRisk, secretRisk, positive, negative, passTests, failTests)

	successEvidence := make([]string, 0, len(evidence))
	for item := range evidence {
		successEvidence = append(successEvidence, item)
	}
	sort.Strings(successEvidence)
	return SignalBundle{
		IntentKind: intent, Confidence: confidence, SuccessEvidence: successEvidence,
		OneOffRisk: oneOffRisk, SecretRisk: secretRisk, RecommendedAction: recommended,
	}
}

func filterSignalRecords(messages []record.MessageRecord) []record.MessageRecord {
	result := make([]record.MessageRecord, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" || message.Role == "system" || isNoise(message.Text) {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		message.Role = role
		result = append(result, message)
	}
	return result
}

func isNoise(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, prefix := range record.NoisePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func looksLikeImplementation(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "```") || strings.Contains(lower, "func ") ||
		strings.Contains(lower, "class ") || strings.Contains(lower, "implement") ||
		strings.Contains(lower, "实现") || strings.Contains(lower, "代码")
}

func inferIntent(acceptance, correction, decision, question, workflow, implementation, passed, failed int) string {
	switch {
	case correction > 0 && correction >= acceptance:
		return IntentCorrection
	case acceptance > 0 && acceptance > correction && passed > 0:
		return IntentAcceptance
	case decision > 0:
		return IntentDecision
	case workflow > 0:
		return IntentWorkflow
	case implementation > 0:
		return IntentImplement
	case question > 0:
		return IntentQuestion
	case acceptance > 0:
		return IntentAcceptance
	case failed > 0:
		return IntentCorrection
	default:
		return IntentUnknown
	}
}

func signalConfidence(messages []record.MessageRecord, positive, negative float64, intent string, secretRisk float64) float64 {
	if len(messages) == 0 {
		return 0
	}
	magnitude := positive + negative
	confidence := 0.34 + minFloat(0.42, magnitude*0.08)
	if intent != IntentUnknown {
		confidence += 0.08
	}
	if len(messages) >= 3 {
		confidence += 0.06
	}
	if secretRisk > 0 {
		confidence += minFloat(0.1, secretRisk*0.1)
	}
	return clamp(confidence)
}

func riskForOneOff(messages []record.MessageRecord, acceptance, correction, workflow, passed int) float64 {
	joined := strings.Join(messageTexts(messages), " ")
	if oneOffRE.MatchString(joined) {
		return 0.95
	}
	risk := 0.58
	if len(messages) <= 2 {
		risk += 0.2
	}
	if workflow > 0 {
		risk -= 0.35
	}
	if acceptance > 0 {
		risk -= 0.12
	}
	if correction > 0 {
		risk -= 0.04
	}
	if passed > 0 {
		risk -= 0.08
	}
	return clamp(risk)
}

func recommendation(messages []record.MessageRecord, intent string, confidence, oneOffRisk, secretRisk, positive, negative float64, passed, failed int) string {
	if len(messages) == 0 || secretRisk >= 0.75 || oneOffRisk >= 0.92 {
		return ActionSuppress
	}
	if negative > positive+0.35 || failed > passed || intent == IntentCorrection || confidence < 0.45 {
		return ActionReview
	}
	if positive > 0 || passed > 0 || intent == IntentWorkflow || intent == IntentDecision {
		return ActionDraft
	}
	return ActionReview
}

func secretMatchCount(text string) int {
	count := len(secretTokenRE.FindAllStringIndex(text, -1))
	count += len(secretAssignmentRE.FindAllStringIndex(text, -1))
	count += len(slackWebhookRE.FindAllStringIndex(text, -1))
	count += len(jwtRE.FindAllStringIndex(text, -1))
	count += len(privateKeyRE.FindAllStringIndex(text, -1))
	return count
}

func riskForSecrets(parts []string) float64 {
	count := 0
	for _, text := range parts {
		count += secretMatchCount(text)
	}
	if count == 0 {
		return 0
	}
	// One or more high-confidence matches should require suppression, but
	// ordinary paths/emails do not enter this score at all.
	return clamp(float64(count) * 0.75)
}

func messageTexts(messages []record.MessageRecord) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.Text)
	}
	return result
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
