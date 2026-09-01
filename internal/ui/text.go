package ui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

var (
	uuidRE = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

func StripANSI(s string) string {
	var result strings.Builder
	result.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI, including SGR colors.
				i++
				for i < len(s) {
					ch := s[i]
					i++
					if ch >= 0x40 && ch <= 0x7e {
						break
					}
				}
			case ']': // OSC title/hyperlink; terminate at BEL or ST.
				i++
				for i < len(s) {
					if s[i] == '\a' {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				i++
			}
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(s[i:])
		if runeValue == utf8.RuneError && size == 1 {
			i++
			continue
		}
		i += size
		if runeValue < 0x20 || runeValue == 0x7f {
			if runeValue == '\n' || runeValue == '\r' || runeValue == '\t' {
				result.WriteByte(' ')
			}
			continue
		}
		result.WriteRune(runeValue)
	}
	return result.String()
}

func cellWidth(value rune) int {
	if unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe19) ||
		(value >= 0xfe30 && value <= 0xfe6f) || (value >= 0xff00 && value <= 0xff60) ||
		(value >= 0xffe0 && value <= 0xffe6)) {
		return 2
	}
	return 1
}

func DisplayWidth(s string) int {
	s = StripANSI(s)
	width := 0
	for _, runeValue := range s {
		width += cellWidth(runeValue)
	}
	return width
}

func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = PlainField(s)
	if DisplayWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	remaining := max - 1
	var result strings.Builder
	width := 0
	for _, runeValue := range s {
		runeWidth := cellWidth(runeValue)
		if width+runeWidth > remaining {
			break
		}
		result.WriteRune(runeValue)
		width += runeWidth
	}
	result.WriteRune('…')
	return result.String()
}

func cutWidth(s string, max int) (head, rest string) {
	if max <= 0 {
		return "", s
	}
	if DisplayWidth(s) <= max {
		return s, ""
	}
	width := 0
	cut := len(s)
	for i, runeValue := range s {
		w := cellWidth(runeValue)
		if width+w > max {
			cut = i
			break
		}
		width += w
	}
	head, rest = s[:cut], s[cut:]
	if idx := strings.LastIndexByte(head, ' '); idx >= 1 {
		if DisplayWidth(head[:idx]) >= max/2 {
			return head[:idx], strings.TrimSpace(head[idx+1:] + rest)
		}
	}
	return head, rest
}

func wrapLines(s string, width, maxLines int) []string {
	s = PlainField(s)
	if s == "" || s == "-" {
		return nil
	}
	if maxLines == 0 {
		maxLines = 1 << 20
	}
	if maxLines < 0 {
		return nil
	}
	if width < 8 {
		width = 8
	}
	var lines []string
	rest := s
	for len(lines) < maxLines && rest != "" {
		if len(lines) == maxLines-1 && DisplayWidth(rest) > width {
			lines = append(lines, Truncate(rest, width))
			break
		}
		head, next := cutWidth(rest, width)
		if head == "" {
			break
		}
		lines = append(lines, head)
		rest = strings.TrimSpace(next)
	}
	return lines
}

// WrapLines wraps plain text to width display cells. maxLines 0 means no cap.
func WrapLines(s string, width, maxLines int) []string {
	return wrapLines(s, width, maxLines)
}

func PlainField(s string) string {
	s = StripANSI(s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "-"
	}
	return s
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func PathSummary(paths []string) string {
	if len(paths) == 0 {
		return "-"
	}
	path := PlainField(paths[0])
	if len(paths) > 1 {
		path += fmt.Sprintf(" (+%d)", len(paths)-1)
	}
	return path
}

func RelativeTime(ts string) string {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", ts)
	if err != nil || ts == "-" {
		return "-"
	}
	delta := time.Now().UTC().Sub(parsed)
	future := delta < 0
	if future {
		delta = -delta
	}
	var value string
	switch {
	case delta < time.Minute:
		value = "just now"
	case delta < time.Hour:
		value = fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		value = fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 30*24*time.Hour:
		value = fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	case delta < 365*24*time.Hour:
		value = fmt.Sprintf("%dmo", int(delta/(30*24*time.Hour)))
	default:
		value = fmt.Sprintf("%dy", int(delta/(365*24*time.Hour)))
	}
	if future && value != "just now" {
		return "in " + value
	}
	if value == "just now" {
		return value
	}
	return value + " ago"
}

func PythonQuote(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}

	var result strings.Builder
	result.Grow(len(s) + 2)
	result.WriteByte(quote)
	for _, char := range s {
		switch char {
		case '\\':
			result.WriteString(`\\`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		case rune(quote):
			result.WriteByte('\\')
			result.WriteRune(char)
		default:
			if unicode.IsPrint(char) {
				result.WriteRune(char)
			} else {
				writeUnicodeEscape(&result, char)
			}
		}
	}
	result.WriteByte(quote)
	return result.String()
}

func writeUnicodeEscape(result *strings.Builder, char rune) {
	switch {
	case char <= 0xff:
		fmt.Fprintf(result, `\x%02x`, char)
	case char <= 0xffff:
		fmt.Fprintf(result, `\u%04x`, char)
	default:
		fmt.Fprintf(result, `\U%08x`, char)
	}
}

// Preview flattens stored/escaped whitespace so match windows stay readable.
func Preview(s string) string {
	s = strings.NewReplacer(`\r\n`, " ", `\n`, " ", `\r`, " ", `\t`, " ", `\"`, `"`).Replace(s)
	return PlainField(s)
}

func HighlightTerms(s string, terms []string, match lipgloss.Style) string {
	if len(terms) == 0 || s == "" {
		return s
	}
	runes := []rune(s)
	var result strings.Builder
	for i := 0; i < len(runes); {
		found := 0
		for _, term := range terms {
			n := len([]rune(term))
			if n == 0 || i+n > len(runes) {
				continue
			}
			if strings.EqualFold(string(runes[i:i+n]), term) {
				found = n
				break
			}
		}
		if found > 0 {
			result.WriteString(match.Render(string(runes[i : i+found])))
			i += found
			continue
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func ShortID(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	if s == "" {
		return "-"
	}
	if loc := uuidRE.FindStringIndex(s); loc != nil {
		return s[loc[0] : loc[0]+8]
	}
	if DisplayWidth(s) > 12 {
		return Truncate(s, 12)
	}
	return s
}
