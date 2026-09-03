package decisions

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type decisionSegment struct {
	Text    string
	Ordinal int
}

var (
	messageNoiseRE          = regexp.MustCompile(`(?is)<\/?(?:skill|subagent_notification|external_agent_tool_call|environment_context|proposed_plan|system(?:-reminder)?|user_info|developer(?:_context)?)\b|\[/?(?:external_agent_tool_call|subagent_notification|bash-(?:stdout|stderr))\]`)
	segmentNoiseRE          = messageNoiseRE
	segmentPromptRE         = regexp.MustCompile(`(?i)\b(?:only give|return only|output only|do not modify|don't modify|just answer|please inspect|key files?|focus on|must list|expected outcome|task|automation|context from my ide setup|system instructions?|developer instructions?)\b|只给|只返回|只接受|不要修改|请重点检查|关键文件|已验证|输出格式|任务|自动化|上下文`)
	segmentChoiceLabelRE    = regexp.MustCompile(`(?i)\b(?:approved|changes_requested|pass|fail|yes|no)\s*(?:or|/|、|或)\s*\b(?:approved|changes_requested|pass|fail|yes|no)\b`)
	segmentPlanRE           = regexp.MustCompile(`(?i)\b(?:next step|next|then|after that|later|i['’]?ll|i will|we['’]?ll|we will|going to|plan to|intend to|let me|todo|to do|first .* then)\b|下一步|接下来|然后|之后|稍后|我会|我们会|计划|打算|先.*再|待办`)
	segmentFuturePlanRE     = regexp.MustCompile(`(?i)^\s*(?:(?:i|we)(?:['’]?ll|\s+will)|going to|plan to|intend to)\b|^\s*(?:将|即将|准备|接下来|下一步|后续|稍后|我会|我们会|我将|我们将|计划|打算)|^\s*(?:现在|接下来)\s*(?:改|修改|调整|补|新增|实现|写|处理|做|停掉|准备)|^\s*先.+再|\bnext step\b|下一步`)
	segmentMetaRE           = regexp.MustCompile(`(?i)\b(?:i need to decide|i should decide|i['’]?m deciding|let me decide|thinking through|reasoning about|need to choose|we need to decide)\b|我(?:需要|应该)决定|我在(?:判断|考虑)|需要选择`)
	segmentDeliberationRE   = regexp.MustCompile(`(?i)\b(?:i['’]?m considering|i am considering|i might|i wonder|let me try|let me go with|could use|might be best)\b`)
	segmentRefusalRE        = regexp.MustCompile(`(?i)\b(?:i can['’]?t|cannot|can not|won['’]?t|not able|unable|not permitted|must refuse|refuse to|i['’]?m not allowed)\b|不能|无法|不可以|不允许|拒绝|没法`)
	segmentQuestionRE       = regexp.MustCompile(`(?i)[?？]|\b(?:is this correct|is that right|does this look right|can we use this)\b|(?:正确吗|对吗|是否正确|可以吗|行吗|是真的吗)`)
	segmentProgressRE       = regexp.MustCompile(`(?i)\b(?:i['’]?m|i am)\s+(?:pulling|reading|checking|inspecting|reviewing|verifying|confirming|comparing|collecting|gathering|looking(?:\s+at)?)\b|\bi\s+want\s+to\s+be\s+precise\b|\b(?:i['’]?m|i am)\s+using\b[^.!?\n]{0,80}\bskill\b`)
	segmentNegativeRE       = regexp.MustCompile(`(?i)\b(?:do not|don't|never|not|without)\s+use\b|^\s*(?:不要(?:使用|把)?|不使用|不用|未使用|没有使用|尚未使用)\b|\b(?:不会|并非|不是)\b[^。！？!?\n]{0,40}\b(?:使用|采用|选择)\b`)
	segmentReplacementRE    = regexp.MustCompile(`(?i)\b(?:instead of|rather than)\b|[,;，；]\s*(?:(?:do not|don't|not)\s+use|use|choose|pick|adopt|go with)\b|\b(?:改用|换成|改为|转为|切换到|不使用|不用|而非|而不是)\b`)
	segmentStatusRE         = regexp.MustCompile(`(?i)^\s*(?:done|completed|implemented|built|added|created|fixed|shipped|deployed|merged|passed|approved|changes_requested|approved with risks)\b|已完成|完成了|已实现|已修复|已合并|通过|批准|搞定`)
	segmentPredicateRE      = regexp.MustCompile(`(?i)\b(?:choose|chose|selected|select|prefer|preferred|recommend|recommended|adopt|go with|pick|we chose|we selected|we prefer|we recommend|we adopted|instead of|rather than|over)\b|选择|选用|偏好|推荐|建议|采用|取舍|替代|代替|改用|而非|而不是`)
	segmentExplicitChoiceRE = regexp.MustCompile(`(?i)\b(?:choose|chose|selected|select|prefer|preferred|recommend|recommended|adopt|go with|pick|use|we chose|we selected|we prefer|we recommend|we adopted)\b|选择|选用|偏好|推荐|建议|采用|使用|改用|更推荐|优先使用`)
	segmentBulletRE         = regexp.MustCompile(`(?m)^\s{0,3}(?:[-*+] |\d+[.)]\s+)`)
	consequencePrefixRE     = regexp.MustCompile(`(?i)^(?:(?:so|therefore|thus)\s*[,，]?\s+|(?:这样|因此|所以)\s*[,，]?\s*)`)
	segmentHeadingRE        = regexp.MustCompile(`(?s)^\s*(?:\*\*|__)[^\n]+(?:\*\*|__)\s*$`)
	segmentTableRE          = regexp.MustCompile(`(?m)^\s*\|.*\|`)
)

func splitDecisionSegments(text string) []decisionSegment {
	if trimmed := strings.TrimSpace(text); segmentHeadingRE.MatchString(trimmed) {
		return []decisionSegment{{Text: trimmed, Ordinal: 0}}
	}
	result := make([]decisionSegment, 0, 4)
	start, ordinal := 0, 0
	fenced, inline := false, false
	markdownLinkDepth := 0
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		if r == '`' {
			if !inline && strings.HasPrefix(text[offset:], "```") {
				fenced = !fenced
				offset += 3
				continue
			}
			if !fenced {
				inline = !inline
			}
			offset += size
			continue
		}
		if !fenced && !inline {
			if r == '(' && offset > 0 && text[offset-1] == ']' {
				markdownLinkDepth = 1
			} else if markdownLinkDepth > 0 && r == '(' {
				markdownLinkDepth++
			} else if markdownLinkDepth > 0 && r == ')' {
				markdownLinkDepth--
			}
			if markdownLinkDepth == 0 && isSegmentBoundary(r) && !startsConsequenceClause(text[offset+size:]) {
				end := offset + size
				if segment := strings.TrimSpace(text[start:end]); segment != "" {
					result = append(result, decisionSegment{Text: segment, Ordinal: ordinal})
					ordinal++
				}
				start = end
			}
		}
		offset += size
	}
	if segment := strings.TrimSpace(text[start:]); segment != "" {
		result = append(result, decisionSegment{Text: segment, Ordinal: ordinal})
	}
	return result
}

func isSegmentBoundary(r rune) bool {
	switch r {
	case '\n', '\r', '.', '!', '?', '。', '！', '？', ';', '；':
		return true
	default:
		return false
	}
}

func startsConsequenceClause(text string) bool {
	return consequencePrefixRE.MatchString(strings.TrimSpace(text))
}

func isUsableDecisionSegment(role, text, signalText string) bool {
	if len([]rune(strings.TrimSpace(text))) < 8 || strings.TrimSpace(signalText) == "" {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if segmentNoiseRE.MatchString(trimmed) {
		return false
	}
	// Structural fragments and future-action narration are not durable
	// decisions. Bullets remain eligible only when they contain an explicit
	// choice, so prose such as "Use SQLite because ..." is not lost merely
	// because it was formatted as a list item.
	if segmentFuturePlanRE.MatchString(signalText) || segmentTableRE.MatchString(trimmed) || segmentHeadingRE.MatchString(trimmed) {
		return false
	}
	if segmentBulletRE.MatchString(trimmed) && !segmentExplicitChoiceRE.MatchString(signalText) {
		return false
	}
	// Deliberation markers are noise even when the same sentence also names a
	// possible choice; a predicate must not turn an unresolved thought into a
	// resolved decision.
	if segmentDeliberationRE.MatchString(signalText) || segmentQuestionRE.MatchString(trimmed) || segmentProgressRE.MatchString(signalText) {
		return false
	}
	if segmentChoiceLabelRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if segmentPromptRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if segmentMetaRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if segmentRefusalRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if segmentNegativeRE.MatchString(signalText) && !segmentReplacementRE.MatchString(signalText) {
		return false
	}
	if segmentStatusRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if segmentPlanRE.MatchString(signalText) && !segmentPredicateRE.MatchString(signalText) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(role), "assistant") && strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return false
	}
	return true
}
