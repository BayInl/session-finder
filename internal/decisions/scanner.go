package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BayInl/session-finder/internal/record"
)

var (
	strongDecisionCueRE       = regexp.MustCompile(`(?i)\b(?:decide|decision|choose|chose|choice|select|selected|prefer|preferred|recommend|recommendation|should we|we['’]?ll use|we will use|let['’]?s use|adopt|go with|pick|trade[- ]off|instead of|rather than)\b|决定|决策|选择|选用|偏好|推荐|建议|采用|取舍|替代|代替|改用|不要.*改|不使用`)
	usageCueRE                = regexp.MustCompile(`(?i)\buse\b|使用`)
	semanticUsageRE           = regexp.MustCompile(`(?i)\b(?:because|since|due to|instead of|rather than|over|choose|pick|select|prefer|recommend|adopt|trade[- ]off|option|alternative|approach|proposal)\b|因为|由于|而非|而不是|替代|选择|考虑|方案|取舍|推荐|建议|采用|改用`)
	confirmRE                 = regexp.MustCompile(`(?i)^\s*(?:(?:yes|yep|yeah|correct|right|looks good|sounds good|good|great|perfect|approved|accept(?:ed)?|ship it|proceed|go ahead|do that|that works|works for me|please do|done|thanks|thank you)\b\s*[.!?,;:]?|(?:可以|好的|好吧|正确|对的|没问题|看起来不错|批准|通过|确认|继续|就这样|按这个|搞定|谢谢)(?:\s*[，,。.!！?？:：;；]|$)|(?:采用|发布)\s+[^.!?\n。！？]{1,120}(?:[.!?\n。！？]|$))`)
	implementRE               = regexp.MustCompile(`(?i)\b(?:implemented|built|added|created|completed|fixed|shipped|deployed|merged)\b|(?:实现了|已实现|完成了|已经完成|修复了|已修复|已合并|已发布|部署完成)`)
	negativeRE                = regexp.MustCompile(`(?i)\b(?:not|never|didn['’]?t|doesn['’]?t|isn['’]?t|is not|wasn['’]?t|was not)\s+(?:implemented|built|added|created|completed|fixed|shipped|deployed|merged)\b|(?:未实现|没有实现|尚未完成|未完成|没有修复|尚未修复)`)
	testRE                    = regexp.MustCompile(`(?i)(?:\b(?:go test|cargo test|pytest|npm test|yarn test|pnpm test|vitest|jest|gradle test|mvn test)\b[^\n]*(?:pass|passed|ok|success|green)\b)|(?:\btests?\b[^\n]*(?:pass|passed|ok|success|green)\b)|(?:测试(?:全部|都)?通过)|(?:^|\n)\s*(?:ok|PASS)\b`)
	testFailRE                = regexp.MustCompile(`(?i)(?:\b(?:go test|cargo test|pytest|npm test|yarn test|pnpm test|vitest|jest|gradle test|mvn test)\b[^\n]*(?:fail|failed|failure|error|panic|did not pass|does not pass|doesn['’]?t pass)\b)|(?:\btests?\b[^\n]*(?:fail|failed|failure|error|panic|did not pass|does not pass|doesn['’]?t pass)\b)|(?:测试失败|测试报错|build failed)|(?:^|\n)\s*FAIL\b`)
	becauseRE                 = regexp.MustCompile(`(?is)\b(?:because|since|so that|due to)\s+(.+?)(?:[.!?]|$)|(?:因为|由于|为了)\s*(.+?)(?:[。！？!?]|$)`)
	additionalReasonRE        = regexp.MustCompile(`(?is)(?:便于|方便|避免|确保|以便|适合|符合|有利于|保证|防止|减少|降低|支持)\s*(.+?)(?:[.!?]|$|[。！？!?])`)
	consequenceBeforeChoiceRE = regexp.MustCompile(`(?is)^(.+?)[，,；;]\s*(?:therefore|so|thus|因此|所以)\s*((?:(?:i|we|我|我们|主路径)\s*)?(?:(?:switch(?:ed)?\s+to|use|choose|chose|select(?:ed)?|recommend(?:ed)?|adopt(?:ed)?|go\s+with)\b|(?:改用|换成|改为|转为|切换到|采用|使用|选择|推荐|建议)(?:了)?).+?)(?:[.!?。！？]|$)`)
	consequenceAfterRE        = regexp.MustCompile(`(?is)[，,；;]\s*(?:so|therefore|thus|这样|因此|所以)\s*(.+?)(?:[.!?]|$|[。！？!?])`)
	consequenceChoiceRE       = regexp.MustCompile(`(?is)(?:(?:switch(?:ed)?\s+to|use|choose|chose|select(?:ed)?|recommend(?:ed)?|adopt(?:ed)?|go\s+with)\b\s+|(?:改用|换成|改为|转为|切换到|采用|使用|选择|推荐|建议)(?:了)?\s*)(.+)$`)
	choiceReasonRE            = regexp.MustCompile(`(?i)\b(?:because|since|due to|so that|so|therefore|thus)\b|因为|由于|为了|便于|方便|避免|确保|以便|适合|符合|有利于|保证|防止|减少|降低|支持|这样|因此|所以`)
	choiceRejectedTailRE      = regexp.MustCompile(`(?i)\s*(?:(?:[,;，；]\s*)?(?:do not use|don't use|not use|instead of|rather than)\b|(?:[,;，；]\s*)?(?:不使用|不用|而非|而不是))`)
	choiceDescriptorTailRE    = regexp.MustCompile(`(?i)\s+(?:as\s+(?:the\s+)?|作为)(?:[^,，;；]{0,40})$`)
	choiceTrailingClauseRE    = regexp.MustCompile(`[,，;；]\s*(?:because|since|due to|so that)\b|[,，;；]\s*(?:因为|由于|为了|避免|便于|确保)`)
	choiceSeparatorRE         = regexp.MustCompile(`(?i)\s+(?:or|或者)\s+|或者|、|\s+/\s+`)
	insteadRE                 = regexp.MustCompile(`(?is)\binstead of\s+(.+?)(?:,|;|\s+(?:use|choose|pick|adopt|go with)\s+)(.+?)(?:[.!?]|$)|(?:不使用|不要)\s*(.+?)(?:(?:，|,|；|;)\s*(?:改用|换成|改为|使用)|\s+(?:改用|换成|改为))\s*(.+?)(?:[。！？!?]|$)`)
	chooseOverRE              = regexp.MustCompile(`(?is)\b(?:choose|pick|select|prefer|use|adopt|go with)\b\s+(.+?)\s+(?:over|rather than|instead of)\s+(.+?)(?:\s+because\b|[.!?]|$)`)
	comparisonRE              = regexp.MustCompile(`(?is)^\s*(?:(?:我|我们)\s*)?(?:(?:建议|推荐)(?:使用|用)?|使用|采用|选择|改用)\s*(.{1,120}?)\s*[,，]\s*(?:不使用|不用|而非|而不是)\s*([^!?\n。！？]{1,120})|^\s*([^.!?\n，。！？]{1,120}?)\s+(?:而非|而不是)\s+([^.!?\n，。！？]{1,120})`)
	useRE                     = regexp.MustCompile(`(?is)(?:we['’]?ll use|we will use|let['’]?s use|recommend(?:ation)?|adopt|go with|choose|pick|select|^\s*(?:[-*+] |\d+[.)]\s+)?use|采用|改用|使用|选择|建议|推荐)\s+([^.!?\n，。！？]{1,120})`)
	leadingUseRE              = regexp.MustCompile(`(?is)^\s*use\s+([^.!?\n，。！？]{1,120})`)
	genericChoicePrefixRE     = regexp.MustCompile(`(?i)^(?:a|an|the|this|that|it|one|some|any|no|not)\s+`)
	genericChoiceRE           = regexp.MustCompile(`(?i)^(?:a|an|the|this|that|it|one|case|path|option|choice|solution|approach|method|way|prefix|thing|something|anything|nothing|no|not|不再|http|https?|json|api|url|token|response|request|data|annotation)$|^(?:this|that|the)\s+(?:path|option|choice|case|way)$|^(?:这个|那个|此|该)?(?:路径|方案|选项|选择|用例|案例)$`)
	choiceClauseRE            = regexp.MustCompile(`(?i)\b(?:because|since|due to|so that|could|might|should|would|will|can|want|need|try|using|use|uses|is|are|was|were|be|returns?|resolves?|passes?|fails?|missing|required?|rather|instead|while|which|that)\b|(?:因为|由于|为了|可保留|或改|等(?:工具|方式|方法|流程|技能)|而且|但是|因此|所以|以及|而|但)`)
	choicePronounRE           = regexp.MustCompile(`(?i)^(?:it|this|that|these|those|them|they|he|she|we|you|me|us)(?:\s+(?:up|down|out|in|on|off|over|there|here))?$`)
	toolMetaChoiceRE          = regexp.MustCompile(`(?i)\bskills?\b|技能`)
	negativeUseRE             = regexp.MustCompile(`(?is)^\s*(?:do not|don't|never|not|without)\s+use\b|\b(?:do not|don't|never|not)\s+use\b|^\s*(?:不要|不使用|未使用|没有使用|尚未使用)`)
	questionEndingRE          = regexp.MustCompile(`[?？]\s*$`)
	progressNoiseRE           = regexp.MustCompile(`(?i)\b(?:i['’]?m|i am)\s+(?:pulling|reading|checking|inspecting|reviewing|verifying|confirming|comparing|collecting|gathering|looking(?:\s+at)?)\b|\bi\s+want\s+to\s+be\s+precise\b|(?:我|我们)(?:正在|在)(?:拉取|读取|检查|核对|审查|验证|确认|比较|收集|查看)`)
	questionRE                = regexp.MustCompile(`(?i)\b(?:should we|which|what should|how should we)\b|是否|哪个方案|怎么选|如何选择`)
	planRE                    = regexp.MustCompile(`(?i)\b(?:next step|next|then|after that|later|i['’]?ll|i will|we['’]?ll|we will|going to|plan to|intend to|let me|todo|to do|first .* then)\b|下一步|接下来|然后|之后|稍后|我会|我们会|计划|打算|先.*再|待办`)
	metaReasoningRE           = regexp.MustCompile(`(?i)\b(?:i need to decide|i should decide|i['’]?m deciding|let me decide|thinking through|reasoning about|need to choose|we need to decide)\b|我(?:需要|应该)决定|我在(?:判断|考虑)|需要选择`)
	refusalRE                 = regexp.MustCompile(`(?i)\b(?:i can['’]?t|cannot|can not|won['’]?t|not able|unable|not permitted|must refuse|refuse to|i['’]?m not allowed)\b|不能|无法|不可以|不允许|拒绝|没法`)
	statusRE                  = regexp.MustCompile(`(?i)^\s*(?:done|completed|implemented|built|added|created|fixed|shipped|deployed|merged|passed|approved|changes_requested|approved with risks)\b|已完成|完成了|已实现|已修复|已合并|通过|批准|搞定`)
	promptNoiseRE             = regexp.MustCompile(`(?is)^\s*(?:\[/?(?:external_agent_tool_call|subagent_notification|bash-(?:stdout|stderr))\]|<\/?(?:skill|subagent_notification|external_agent_tool_call|environment_context|system(?:-reminder)?|user_info|developer(?:_context)?)\b|(?:#+\s*)?(?:TASK|EXPECTED OUTCOME|AVAILABLE SKILLS|AUTOMATION|CONTEXT FROM MY IDE SETUP|AGENTS(?:\.md)?|SYSTEM INSTRUCTIONS?|DEVELOPER INSTRUCTIONS?)\b|(?:name|description|path|status|agent_path)\s*:)`)
	loopEventNoiseRE          = regexp.MustCompile(`(?is)^\s*(?:tool[._-](?:call|result)\b|<tool(?:[ ._:-]|>)|\[/?tool(?:[ ._:-]|>))`)
	promptDirectiveRE         = regexp.MustCompile(`(?i)\b(?:only give|return only|output only|do not modify|don't modify|just answer|please inspect|key files?|focus on|must list|expected outcome|task:)\b|只给|只返回|只接受|不要修改|请重点检查|关键文件|已验证|输出格式|任务[:：]`)
	choiceLabelRE             = regexp.MustCompile(`(?i)\b(?:approved|changes_requested|pass|fail|yes|no)\s*(?:or|/|、|或)\s*\b(?:approved|changes_requested|pass|fail|yes|no)\b|APPROVED\s*(?:OR|/|或)\s*CHANGES_REQUESTED`)
	decisionPredicateRE       = regexp.MustCompile(`(?i)\b(?:choose|chose|selected|select|prefer|preferred|recommend|recommendation|adopt|go with|pick|we chose|we selected|we prefer|we recommend|we adopted)\b|选择|选用|偏好|推荐|采用|取舍|替代|代替|改用`)
	fencedCodeRE              = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRE              = regexp.MustCompile("`[^`]*`")
	markdownLinkRE            = regexp.MustCompile(`!?(?:\[[^\]]*\])\([^)]*\)`)
	markdownMarkerRE          = regexp.MustCompile(`(?m)^\s{0,3}(?:[#>*+\-]|\d+[.)])\s+`)
	xmlNoiseRE                = regexp.MustCompile(`(?is)<(?:environment_context|system(?:-reminder)?|user_info|developer(?:_context)?|instructions?)\b`)
	agentsNoiseRE             = regexp.MustCompile(`(?im)(?:^|\n)\s*(?:#\s*)?(?:AGENTS(?:\.md)?|system instructions?|developer instructions?|ignore previous|you are an ai)\b`)
	pathRE                    = regexp.MustCompile(`(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+|[A-Za-z0-9_.-]+\.(?:go|py|js|ts|tsx|jsx|rs|java|sql|yaml|yml|json|md|toml|sh)\b`)
	asciiTokenRE              = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_+-]{2,}`)
	cjkTokenRE                = regexp.MustCompile(`[\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}]{2,}`)
)

// ExtractOptions controls deterministic extraction and optional candidate-level
// judgment.
type ExtractOptions struct {
	Judge         CandidateJudge
	JudgeLimit    int
	ConfidenceCut float64
	MinConfidence float64
	Extractor     string
	// ResolvedOnly applies the strict publication admission gate. The default
	// Extract path remains high-recall for callers that explicitly request a
	// lower threshold; Scan and CLI paths enable this gate for user-visible data.
	ResolvedOnly bool
}

// Scan performs local candidate extraction for the user-visible decision list.
// It keeps the low-level Extract API high-recall, while requiring a chosen
// option and an identifiable rationale before publishing a candidate here.
func Scan(messages []record.MessageRecord) []DecisionCandidate {
	return Extract(messages, ExtractOptions{ResolvedOnly: true})
}

// Extract is the package-level extraction entry point. It never writes the
// candidate store; callers must explicitly confirm a candidate before calling
// Store.CreateDecision.
func Extract(messages []record.MessageRecord, options ExtractOptions) []DecisionCandidate {
	clean := cleanScanMessages(messages)
	if len(clean) == 0 {
		return []DecisionCandidate{}
	}
	if options.ConfidenceCut <= 0 || options.ConfidenceCut >= 1 {
		options.ConfidenceCut = 0.55
	}
	if options.MinConfidence <= 0 || options.MinConfidence >= 1 {
		options.MinConfidence = 0.62
	}
	if strings.TrimSpace(options.Extractor) == "" {
		options.Extractor = "rules"
	}
	candidates := make([]DecisionCandidate, 0, len(clean))
	byKey := map[string]int{}
	for cleanIndex, item := range clean {
		message := item.MessageRecord
		for _, segment := range splitDecisionSegments(message.Text) {
			text := strings.TrimSpace(segment.Text)
			signalText := signalTextForScan(text)
			isConfirmation := strings.EqualFold(message.Role, "user") && confirmRE.MatchString(signalText)
			isImplementation := strings.EqualFold(message.Role, "assistant") && (implementRE.MatchString(signalText) || testRE.MatchString(signalText) || testFailRE.MatchString(signalText))
			if isConfirmation && attachConfirmation(candidates, message, text, item.OriginalIndex) {
				continue
			}
			if isImplementation && attachImplementation(candidates, message, text, item.OriginalIndex) {
				continue
			}
			if !isUsableDecisionSegment(message.Role, text, signalText) {
				continue
			}
			if progressNoiseRE.MatchString(signalText) {
				continue
			}
			if !hasDecisionCue(signalText) || questionRE.MatchString(signalText) && !hasDecisionStatement(signalText) {
				continue
			}
			candidate := candidateFromSegment(clean, cleanIndex, segment, options.Extractor)
			if !admitCandidate(candidate, options) {
				continue
			}
			key := candidateKey(candidate.Decision)
			if previous, ok := byKey[key]; ok {
				mergeCandidate(&candidates[previous], &candidate)
				continue
			}
			if previous := attachRelatedCandidate(candidates, candidate, message, item.OriginalIndex); previous >= 0 {
				mergeCandidate(&candidates[previous], &candidate)
				continue
			}
			byKey[key] = len(candidates)
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func admitCandidate(candidate DecisionCandidate, options ExtractOptions) bool {
	if candidate.Decision.Confidence < options.MinConfidence {
		return false
	}
	if options.ResolvedOnly {
		chosen := strings.TrimSpace(candidate.Decision.Chosen)
		if chosen == "" || strings.TrimSpace(candidate.Decision.Rationale) == "" || !isUsableChosen(chosen, candidate.Decision.Options) {
			return false
		}
	}
	return true
}

func isUsableChosen(chosen string, options []string) bool {
	chosen = cleanChoice(chosen)
	lowerChosen := strings.ToLower(chosen)
	if chosen == "" || genericChoiceRE.MatchString(lowerChosen) || genericChoicePrefixRE.MatchString(lowerChosen) || choiceClauseRE.MatchString(chosen) || choicePronounRE.MatchString(chosen) || toolMetaChoiceRE.MatchString(chosen) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(chosen), "i ") || strings.HasPrefix(strings.ToLower(chosen), "we ") {
		return false
	}
	if len([]rune(chosen)) > 80 {
		return false
	}
	if len(options) > 0 {
		for _, option := range options {
			if strings.EqualFold(cleanChoice(option), chosen) {
				return true
			}
		}
		return false
	}
	return true
}

// ExtractContext is equivalent to Extract but lets a caller cancel optional
// candidate judgment. Judge failures are non-fatal: the candidate is retained
// with its deterministic rule result so provider issues cannot erase local data.
func ExtractContext(ctx context.Context, messages []record.MessageRecord, options ExtractOptions) ([]DecisionCandidate, error) {
	baseOptions := ExtractOptions{Extractor: options.Extractor, ConfidenceCut: options.ConfidenceCut, MinConfidence: 0.4}
	candidates := Extract(messages, baseOptions)
	if len(candidates) == 0 || options.Judge == nil {
		return filterCandidatesWithOptions(candidates, options), nil
	}
	if options.ConfidenceCut <= 0 || options.ConfidenceCut >= 1 {
		options.ConfidenceCut = 0.55
	}
	if options.MinConfidence <= 0 || options.MinConfidence >= 1 {
		options.MinConfidence = 0.62
	}
	return applyCandidateJudge(ctx, messages, candidates, options), nil
}

func applyCandidateJudge(ctx context.Context, messages []record.MessageRecord, candidates []DecisionCandidate, options ExtractOptions) []DecisionCandidate {
	result := make([]DecisionCandidate, 0, len(candidates))
	judged := 0
	for i := range candidates {
		candidate := candidates[i]
		if options.JudgeLimit > 0 && judged >= options.JudgeLimit {
			if admitCandidate(candidate, options) {
				result = append(result, candidate)
			}
			continue
		}
		judged++
		judgment, err := options.Judge.Judge(ctx, decisionCandidateReview(messages, candidate))
		if err != nil {
			candidate.Reasons = appendUnique(candidate.Reasons, "llm:fallback")
			if admitCandidate(candidate, options) {
				result = append(result, candidate)
			}
			continue
		}
		candidate = applyJudgment(candidate, judgment)
		if admitCandidate(candidate, options) {
			result = append(result, candidate)
		}
	}
	return result
}

func applyJudgment(candidate DecisionCandidate, judgment CandidateJudgment) DecisionCandidate {
	action := strings.ToLower(strings.TrimSpace(judgment.Disposition))
	candidate.Reasons = appendUnique(candidate.Reasons, "llm:"+action)
	for _, reason := range judgment.ReasonCodes {
		candidate.Reasons = appendUnique(candidate.Reasons, "llm:reason:"+strings.TrimSpace(reason))
	}
	switch action {
	case "suppress":
		candidate.Decision.Confidence = 0
	case "draft":
		if candidate.Decision.Chosen == "" || candidate.Decision.Rationale == "" {
			candidate.Reasons = appendUnique(candidate.Reasons, "llm:review-missing-fields")
			break
		}
		if judgment.Confidence > candidate.Decision.Confidence {
			candidate.Decision.Confidence = judgment.Confidence
		}
	case "review":
		candidate.Reasons = appendUnique(candidate.Reasons, "llm:review")
	}
	return candidate
}

func filterCandidatesWithOptions(candidates []DecisionCandidate, options ExtractOptions) []DecisionCandidate {
	if options.MinConfidence <= 0 || options.MinConfidence >= 1 {
		options.MinConfidence = 0.62
	}
	filtered := make([]DecisionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if admitCandidate(candidate, options) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

type scanMessage struct {
	record.MessageRecord
	OriginalIndex int
}

func cleanScanMessages(messages []record.MessageRecord) []scanMessage {
	clean := make([]scanMessage, 0, len(messages))
	for originalIndex, message := range messages {
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		if messageNoiseRE.MatchString(message.Text) || isNoise(message.Text) || loopEventNoiseRE.MatchString(strings.TrimSpace(message.Text)) {
			continue
		}
		message.Role = role
		clean = append(clean, scanMessage{MessageRecord: message, OriginalIndex: originalIndex})
	}
	return clean
}

func isNoise(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, prefix := range record.NoisePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return xmlNoiseRE.MatchString(trimmed) || agentsNoiseRE.MatchString(trimmed) || isAllCapsInstruction(trimmed)
}

func isAllCapsInstruction(text string) bool {
	letters, upper := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters < 12 || upper*100/letters < 85 {
		return false
	}
	return strings.ContainsAny(text, ":!\n") || strings.HasPrefix(text, "DO ") || strings.HasPrefix(text, "MUST ")
}

func signalTextForScan(text string) string {
	text = fencedCodeRE.ReplaceAllString(text, " ")
	text = inlineCodeRE.ReplaceAllString(text, " ")
	text = markdownLinkRE.ReplaceAllString(text, " ")
	text = markdownMarkerRE.ReplaceAllString(text, " ")
	text = strings.ReplaceAll(text, "**", " ")
	text = strings.ReplaceAll(text, "__", " ")
	text = strings.ReplaceAll(text, "~~", " ")
	return strings.TrimSpace(text)
}

func hasDecisionCue(text string) bool {
	if strongDecisionCueRE.MatchString(text) {
		return true
	}
	return usageCueRE.MatchString(text) && (semanticUsageRE.MatchString(text) || leadingUseRE.MatchString(text) && consequenceAfterRE.MatchString(text))
}

func candidateFromSegment(messages []scanMessage, index int, segment decisionSegment, extractor string) DecisionCandidate {
	item := messages[index]
	message := item.MessageRecord
	text := strings.TrimSpace(segment.Text)
	signalText := signalTextForScan(text)
	options, chosen := extractOptions(text)
	rationale := extractRationale(text)
	confidence := 0.54
	reasons := []string{"decision-word"}
	if usageCueRE.MatchString(signalText) && !strongDecisionCueRE.MatchString(signalText) && rationale == "" {
		confidence -= 0.04
		reasons = []string{"descriptive-usage"}
	}
	if len(options) > 1 {
		confidence += 0.13
		reasons = append(reasons, "alternatives")
	} else if len(options) == 1 && strongDecisionCueRE.MatchString(signalText) {
		confidence += 0.04
		reasons = append(reasons, "chosen-option")
	}
	if strings.Contains(signalText, "?") || strings.Contains(signalText, "？") {
		confidence -= 0.08
		reasons = append(reasons, "open-question")
	}
	if chosen != "" {
		confidence += 0.08
		reasons = appendUnique(reasons, "chosen-option")
	} else {
		confidence -= 0.08
		reasons = appendUnique(reasons, "missing-chosen-option")
	}
	if rationale != "" {
		confidence += 0.1
		reasons = append(reasons, "because-rationale")
	}
	lowerSignal := strings.ToLower(signalText)
	if strings.Contains(lowerSignal, "instead of") || strings.Contains(lowerSignal, "rather than") || strings.Contains(signalText, "改用") || strings.Contains(signalText, "而非") || strings.Contains(signalText, "而不是") {
		confidence += 0.09
		reasons = append(reasons, "instead-of")
	}
	if strings.EqualFold(message.Role, "user") && confirmRE.MatchString(signalText) {
		confidence += 0.12
		reasons = append(reasons, "explicit-confirmation")
	}
	confidence = clampDecision(confidence)
	messageIndex := item.OriginalIndex + 1
	evidence := []Evidence{{Kind: EvidenceTranscript, Quote: text, MessageIndex: messageIndex, Role: message.Role, Source: message.SourcePath}}
	if rationale != "" {
		evidence = append(evidence, Evidence{Kind: EvidenceRationale, Quote: text, MessageIndex: messageIndex, Role: message.Role, Source: message.SourcePath})
	}
	if len(options) > 0 {
		evidence = append(evidence, Evidence{Kind: EvidenceAlternative, Quote: text, MessageIndex: messageIndex, Role: message.Role, Source: message.SourcePath})
	}
	if strings.EqualFold(message.Role, "user") && confirmRE.MatchString(text) {
		evidence = append(evidence, Evidence{Kind: EvidenceExplicit, Quote: text, MessageIndex: messageIndex, Role: message.Role, Source: message.SourcePath})
	}
	decision := Decision{
		ID:         decisionID(message.SessionID, item.OriginalIndex, text, segment.Ordinal),
		Kind:       KindDecision,
		Status:     StatusProposed,
		Context:    text,
		Options:    options,
		Chosen:     chosen,
		Rationale:  rationale,
		Evidence:   evidence,
		Outcome:    OutcomeUnknown,
		Commits:    []CommitRef{},
		Confidence: confidence,
		Provenance: Provenance{Tool: message.Tool, SourcePath: message.SourcePath, SessionID: message.SessionID, Extractor: extractor, MessageStart: messageIndex, MessageEnd: messageIndex},
	}
	return DecisionCandidate{Decision: decision, Reasons: reasons}
}

func attachConfirmation(candidates []DecisionCandidate, message record.MessageRecord, text string, index int) bool {
	for i := len(candidates) - 1; i >= 0; i-- {
		decision := &candidates[i].Decision
		if !sameTranscript(decision.Provenance, message) {
			continue
		}
		if index+1-decision.Provenance.MessageEnd > 3 {
			break
		}
		if decision.Provenance.SessionID == "" {
			continue
		}
		if !hasEvidenceQuote(decision.Evidence, text, EvidenceExplicit) {
			decision.Evidence = append(decision.Evidence, Evidence{Kind: EvidenceExplicit, Quote: text, MessageIndex: index + 1, Role: "user"})
			decision.Confidence = clampDecision(decision.Confidence + 0.14)
			candidates[i].Reasons = appendUnique(candidates[i].Reasons, "explicit-confirmation")
			decision.Provenance.MessageEnd = index + 1
		}
		return true
	}
	return false
}

func attachImplementation(candidates []DecisionCandidate, message record.MessageRecord, text string, index int) bool {
	implementationTokens := decisionTokens(text)
	if len(implementationTokens) == 0 {
		return false
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		decision := &candidates[i].Decision
		if !sameTranscript(decision.Provenance, message) {
			continue
		}
		if index+1-decision.Provenance.MessageEnd > 4 {
			break
		}
		if !sharesDecisionToken(*decision, implementationTokens) {
			continue
		}
		kind := EvidenceImplementation
		if testRE.MatchString(text) || testFailRE.MatchString(text) {
			kind = EvidenceTest
		}
		if !hasEvidenceQuote(decision.Evidence, text, kind) {
			decision.Evidence = append(decision.Evidence, Evidence{Kind: kind, Quote: text, MessageIndex: index + 1, Role: "assistant"})
			if !negativeRE.MatchString(text) && (kind == EvidenceTest && !testFailRE.MatchString(text) || implementRE.MatchString(text)) {
				decision.Outcome = OutcomeImplemented
			}
			decision.Confidence = clampDecision(decision.Confidence + 0.08)
			candidates[i].Reasons = appendUnique(candidates[i].Reasons, "implementation-evidence")
			decision.Provenance.MessageEnd = index + 1
		}
		return true
	}
	return false
}

func decisionTokens(decisionOrText any) map[string]struct{} {
	var text string
	switch value := decisionOrText.(type) {
	case string:
		text = value
	case Decision:
		text = strings.Join([]string{value.Context, value.Chosen, value.Rationale, strings.Join(value.Options, " ")}, " ")
	}
	text = signalTextForScan(text)
	tokens := make(map[string]struct{})
	for _, token := range asciiTokenRE.FindAllString(strings.ToLower(text), -1) {
		if !isGenericToken(token) {
			tokens[token] = struct{}{}
		}
	}
	for _, token := range cjkTokenRE.FindAllString(text, -1) {
		tokens[token] = struct{}{}
	}
	for _, path := range pathRE.FindAllString(text, -1) {
		tokens[strings.ToLower(path)] = struct{}{}
	}
	return tokens
}

func isGenericToken(token string) bool {
	switch token {
	case "the", "and", "for", "with", "from", "that", "this", "use", "used", "using", "will", "would", "should", "because", "instead", "rather", "than", "implemented", "implementation", "tests", "test", "passed", "pass", "success", "green":
		return true
	default:
		return len(token) < 4
	}
}

func sharesDecisionToken(decision Decision, implementationTokens map[string]struct{}) bool {
	for token := range decisionTokens(decision) {
		if _, ok := implementationTokens[token]; ok {
			return true
		}
	}
	return false
}

func hasEvidenceQuote(values []Evidence, quote, kind string) bool {
	for _, value := range values {
		if value.Kind == kind && value.Quote == quote {
			return true
		}
	}
	return false
}

func attachRelatedCandidate(candidates []DecisionCandidate, candidate DecisionCandidate, message record.MessageRecord, originalIndex int) int {
	for i := len(candidates) - 1; i >= 0; i-- {
		previous := &candidates[i].Decision
		if !sameTranscript(previous.Provenance, message) {
			continue
		}
		distance := originalIndex + 1 - previous.Provenance.MessageEnd
		if distance < 0 {
			continue
		}
		if distance > 2 {
			break
		}
		if previous.Provenance.MessageStart == candidate.Decision.Provenance.MessageStart {
			continue
		}
		if !sharesDecisionToken(*previous, decisionTokens(candidate.Decision)) {
			continue
		}
		if strings.EqualFold(previousRole(*previous), message.Role) {
			continue
		}
		return i
	}
	return -1
}

func previousRole(decision Decision) string {
	for _, evidence := range decision.Evidence {
		if evidence.Kind == EvidenceTranscript {
			return evidence.Role
		}
	}
	return ""
}

func sameTranscript(provenance Provenance, message record.MessageRecord) bool {
	return strings.TrimSpace(provenance.Tool) == strings.TrimSpace(message.Tool) &&
		strings.TrimSpace(provenance.SessionID) == strings.TrimSpace(message.SessionID) &&
		strings.TrimSpace(provenance.SourcePath) == strings.TrimSpace(message.SourcePath)
}

func sameProvenance(left, right Provenance) bool {
	return strings.TrimSpace(left.Tool) == strings.TrimSpace(right.Tool) &&
		strings.TrimSpace(left.SessionID) == strings.TrimSpace(right.SessionID) &&
		strings.TrimSpace(left.SourcePath) == strings.TrimSpace(right.SourcePath)
}

func mergeCandidate(target, source *DecisionCandidate) {
	if !sameProvenance(target.Decision.Provenance, source.Decision.Provenance) {
		return
	}
	if target.Decision.Context == "" {
		target.Decision.Context = source.Decision.Context
	}
	if target.Decision.Chosen == "" {
		target.Decision.Chosen = source.Decision.Chosen
	}
	if target.Decision.Rationale == "" {
		target.Decision.Rationale = source.Decision.Rationale
	}
	for _, option := range source.Decision.Options {
		if !containsString(target.Decision.Options, option) {
			target.Decision.Options = append(target.Decision.Options, option)
		}
	}
	for _, evidence := range source.Decision.Evidence {
		if !hasEvidenceQuote(target.Decision.Evidence, evidence.Quote, evidence.Kind) {
			target.Decision.Evidence = append(target.Decision.Evidence, evidence)
		}
	}
	if source.Decision.Provenance.MessageEnd > target.Decision.Provenance.MessageEnd {
		target.Decision.Provenance.MessageEnd = source.Decision.Provenance.MessageEnd
	}
	if source.Decision.Confidence > target.Decision.Confidence {
		target.Decision.Confidence = source.Decision.Confidence
	}
	for _, reason := range source.Reasons {
		target.Reasons = appendUnique(target.Reasons, reason)
	}
}

func extractOptions(text string) ([]string, string) {
	trimmed := strings.TrimSpace(text)
	if match := consequenceBeforeChoiceRE.FindStringSubmatch(trimmed); len(match) >= 3 {
		choiceClause := strings.TrimSpace(match[2])
		if choice := consequenceChoiceRE.FindStringSubmatch(choiceClause); len(choice) > 1 {
			chosen := cleanConsequenceChoice(choiceBeforeRejectedTail(choiceBeforeReason(choice[1])))
			return uniqueChoices([]string{chosen}), chosen
		}
		return extractOptions(choiceClause)
	}
	if match := chooseOverRE.FindStringSubmatch(trimmed); len(match) >= 3 {
		chosen := cleanChoice(match[1])
		alternative := cleanChoice(match[2])
		return uniqueChoices([]string{chosen, alternative}), chosen
	}
	if match := insteadRE.FindStringSubmatch(trimmed); len(match) >= 3 {
		old, chosen := "", ""
		if match[1] != "" {
			old, chosen = cleanChoice(match[1]), cleanChoice(match[2])
		} else {
			old, chosen = cleanChoice(match[3]), cleanChoice(match[4])
		}
		if old != "" && chosen != "" {
			return uniqueChoices([]string{chosen, old}), chosen
		}
	}
	if match := comparisonRE.FindStringSubmatch(trimmed); len(match) >= 5 {
		chosen, alternative := "", ""
		if match[1] != "" {
			chosen, alternative = cleanComparisonChoice(match[1]), cleanComparisonAlternative(match[2])
		} else {
			chosen, alternative = cleanChoice(match[3]), cleanChoice(match[4])
		}
		if chosen != "" && alternative != "" {
			return uniqueChoices([]string{chosen, alternative}), chosen
		}
	}
	var choices []string
	chosen := ""
	useMatch := useRE.FindStringSubmatchIndex(trimmed)
	negativeMatch := negativeUseRE.FindStringIndex(trimmed)
	if len(useMatch) >= 4 && (negativeMatch == nil || useMatch[0] < negativeMatch[0]) {
		rawChosen := choiceBeforeRejectedTail(choiceBeforeReason(trimmed[useMatch[2]:useMatch[3]]))
		if selectedChoices := splitChoiceValues(rawChosen); len(selectedChoices) > 1 {
			choices = selectedChoices
			chosen = selectedChoices[0]
		} else {
			chosen = cleanChoice(rawChosen)
		}
	} else if negativeMatch == nil {
		if match := leadingUseRE.FindStringSubmatch(trimmed); len(match) > 1 {
			chosen = cleanChoice(choiceBeforeRejectedTail(choiceBeforeReason(match[1])))
		}
	}
	if len(choices) == 0 && chosen != "" {
		choices = []string{chosen}
	}
	return uniqueChoices(choices), chosen
}

func splitChoiceValues(text string) []string {
	if !choiceSeparatorRE.MatchString(text) {
		return nil
	}
	parts := choiceSeparatorRE.Split(text, -1)
	if len(parts) <= 1 || len(parts) > 5 {
		return nil
	}
	choices := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := cleanChoice(part); value != "" && len([]rune(value)) <= 120 {
			choices = append(choices, value)
		}
	}
	return choices
}

func extractRationale(text string) string {
	trimmed := strings.TrimSpace(text)
	if match := becauseRE.FindStringSubmatch(trimmed); len(match) > 1 {
		if match[1] != "" {
			return cleanChoice(match[1])
		}
		return cleanChoice(match[2])
	}
	if match := additionalReasonRE.FindStringSubmatch(trimmed); len(match) > 1 {
		return cleanChoice(match[1])
	}
	if match := consequenceBeforeChoiceRE.FindStringSubmatch(trimmed); len(match) >= 3 {
		return cleanChoice(match[1])
	}
	if match := consequenceAfterRE.FindStringSubmatchIndex(trimmed); len(match) >= 4 {
		choiceText := strings.TrimSpace(trimmed[:match[0]])
		if segmentExplicitChoiceRE.MatchString(choiceText) || leadingUseRE.MatchString(choiceText) {
			return cleanChoice(trimmed[match[2]:match[3]])
		}
	}
	return ""
}

func choiceBeforeReason(value string) string {
	if location := choiceReasonRE.FindStringIndex(value); location != nil {
		value = value[:location[0]]
	}
	return value
}

func choiceBeforeRejectedTail(value string) string {
	if location := choiceRejectedTailRE.FindStringIndex(value); location != nil {
		value = value[:location[0]]
	}
	return value
}

func cleanChoice(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`\"'“”‘’()[]{}:：,，;；")
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 120 {
		value = string([]rune(value)[:120])
	}
	return value
}

func cleanComparisonChoice(value string) string {
	value = strings.TrimSpace(value)
	if location := choiceDescriptorTailRE.FindStringIndex(value); location != nil {
		value = value[:location[0]]
	}
	return cleanChoice(value)
}

func cleanComparisonAlternative(value string) string {
	if location := choiceTrailingClauseRE.FindStringIndex(value); location != nil {
		value = value[:location[0]]
	}
	return cleanChoice(value)
}

func cleanConsequenceChoice(value string) string {
	value = cleanChoice(value)
	value = strings.TrimRight(value, "`\"'“”‘’()[]{}:：,，;；.!?。！？")
	return strings.TrimSpace(value)
}

func uniqueChoices(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !containsString(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func hasDecisionStatement(text string) bool {
	return strings.ContainsAny(text, ".。!！;；,") || strings.Contains(strings.ToLower(text), "use ") || strings.Contains(text, "采用") || strings.Contains(text, "选择") || semanticUsageRE.MatchString(text)
}

func candidateKey(decision Decision) string {
	// The transcript identity and anchor prevent unrelated decisions from
	// collapsing merely because they share a chosen option.
	return strings.ToLower(strings.Join([]string{decision.Provenance.Tool, decision.Provenance.SourcePath, decision.Provenance.SessionID, fmt.Sprint(decision.Provenance.MessageStart), decision.Chosen}, "\x00"))
}

func decisionID(session string, index int, text string, ordinals ...int) string {
	ordinal := 0
	if len(ordinals) > 0 {
		ordinal = ordinals[0]
	}
	sum := sha256.Sum256([]byte(session + "\x00" + string(rune(index)) + "\x00" + string(rune(ordinal)) + "\x00" + text))
	return "decision-" + hex.EncodeToString(sum[:8])
}

func appendUnique(values []string, value string) []string {
	if !containsString(values, value) {
		return append(values, value)
	}
	return values
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func clampDecision(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// SortCandidates provides deterministic CLI output independent of map/source
// iteration order.
func SortCandidates(values []DecisionCandidate) {
	sort.SliceStable(values, func(i, j int) bool {
		left, right := values[i].Decision, values[j].Decision
		if left.Provenance.SessionID != right.Provenance.SessionID {
			return left.Provenance.SessionID < right.Provenance.SessionID
		}
		if left.Provenance.MessageStart != right.Provenance.MessageStart {
			return left.Provenance.MessageStart < right.Provenance.MessageStart
		}
		return left.ID < right.ID
	})
}
