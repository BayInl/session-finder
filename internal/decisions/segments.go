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
	segmentNoiseRE       = regexp.MustCompile(`(?is)<\/?(?:skill|subagent_notification|external_agent_tool_call|environment_context|system(?:-reminder)?|user_info|developer(?:_context)?)\b|\[/?(?:external_agent_tool_call|subagent_notification|bash-(?:stdout|stderr))\]`)
	segmentPromptRE      = regexp.MustCompile(`(?i)\b(?:only give|return only|output only|do not modify|don't modify|just answer|please inspect|key files?|focus on|must list|expected outcome|task|automation|context from my ide setup|system instructions?|developer instructions?)\b|只给|只返回|只接受|不要修改|请重点检查|关键文件|已验证|输出格式|任务|自动化|上下文`)
	segmentChoiceLabelRE = regexp.MustCompile(`(?i)\b(?:approved|changes_requested|pass|fail|yes|no)\s*(?:or|/|、|或)\s*\b(?:approved|changes_requested|pass|fail|yes|no)\b`)
	segmentPlanRE        = regexp.MustCompile(`(?i)\b(?:next step|next|then|after that|later|i['’]?ll|i will|we['’]?ll|we will|going to|plan to|intend to|let me|todo|to do|first .* then)\b|下一步|接下来|然后|之后|稍后|我会|我们会|计划|打算|先.*再|待办`)
	segmentMetaRE        = regexp.MustCompile(`(?i)\b(?:i need to decide|i should decide|i['’]?m deciding|let me decide|thinking through|reasoning about|need to choose|we need to decide)\b|我(?:需要|应该)决定|我在(?:判断|考虑)|需要选择`)
	segmentRefusalRE     = regexp.MustCompile(`(?i)\b(?:i can['’]?t|cannot|can not|won['’]?t|not able|unable|not permitted|must refuse|refuse to|i['’]?m not allowed)\b|不能|无法|不可以|不允许|拒绝|没法`)
	segmentStatusRE      = regexp.MustCompile(`(?i)^\s*(?:done|completed|implemented|built|added|created|fixed|shipped|deployed|merged|passed|approved|changes_requested|approved with risks)\b|已完成|完成了|已实现|已修复|已合并|通过|批准|搞定`)
	segmentPredicateRE   = regexp.MustCompile(`(?i)\b(?:choose|chose|selected|select|prefer|preferred|recommend|recommendation|adopt|go with|pick|we chose|we selected|we prefer|we recommend|we adopted|instead of|rather than|over)\b|选择|选用|偏好|推荐|采用|取舍|替代|代替|改用|而非|而不是`)
)

func splitDecisionSegments(text string) []decisionSegment {
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
			if markdownLinkDepth == 0 && isSegmentBoundary(r) {
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

func isUsableDecisionSegment(role, text, signalText string) bool {
	if len([]rune(strings.TrimSpace(text))) < 8 || strings.TrimSpace(signalText) == "" {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if segmentNoiseRE.MatchString(trimmed) {
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

func hasChoicePredicate(text string) bool { return segmentPredicateRE.MatchString(text) }
