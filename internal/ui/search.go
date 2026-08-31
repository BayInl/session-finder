package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
)

func firstSnippet(hit SearchHit) string {
	if len(hit.Snippets) == 0 {
		return "-"
	}
	return hit.Snippets[0]
}

func messageCount(count int) string {
	return fmt.Sprintf("%d msg%s", count, map[bool]string{true: "s", false: ""}[count != 1])
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// RenderSearch writes search hits. JSON callers must not use this.
func RenderSearch(w io.Writer, query string, hits []SearchHit, verbose bool) {
	if !IsTTY(w) {
		renderSearchPlain(w, query, hits)
		return
	}
	if verbose {
		renderSearchTTYCards(w, query, hits)
		return
	}
	renderSearchTTYCompact(w, query, hits)
}

func renderSearchPlain(w io.Writer, query string, hits []SearchHit) {
	fmt.Fprintf(w, "search: %s (%d sessions)\n", PythonQuote(StripANSI(query)), len(hits))
	if len(hits) == 0 {
		fmt.Fprintln(w, "No matches.")
		return
	}
	for number, hit := range hits {
		fmt.Fprintf(w, "%d. [%s] %s | title=%s | snippet=%s | path=%s | messages=%d | updated=%s\n",
			number+1, PlainField(hit.Tool), PlainField(hit.SessionID),
			PlainField(Truncate(dash(hit.Title), 72)),
			PlainField(Truncate(firstSnippet(hit), 160)),
			PlainField(Truncate(PathSummary(hit.SourcePaths), 80)),
			hit.MessageCount, PlainField(hit.Updated))
	}
}

func renderSearchHeaderTTY(w io.Writer, query string, n int) {
	theme := NewTheme(w)
	count := theme.Style(TokenMuted).Render(fmt.Sprintf("(%d sessions)", n))
	fmt.Fprintf(w, "%s %s %s\n",
		theme.Style(TokenPrimary).Render("search:"),
		theme.Style(TokenPrimary).Render(StripANSI(query)),
		count)
}

func renderSearchTTYCompact(w io.Writer, query string, hits []SearchHit) {
	theme := NewTheme(w)
	width := Width(w)
	renderSearchHeaderTTY(w, query, len(hits))
	if len(hits) == 0 {
		fmt.Fprintln(w, theme.Style(TokenMuted).Render("No matches."))
		return
	}
	muted := theme.Style(TokenMuted)
	match := theme.Style(TokenPrimary)
	for number, hit := range hits {
		num := fmt.Sprintf("%d.", number+1)
		badge := theme.Badge(hit.Tool)
		short := ShortID(hit.SessionID)
		rel := RelativeTime(hit.Updated)
		msgs := messageCount(hit.MessageCount)
		reserved := DisplayWidth(num) + 1 + DisplayWidth(badge) + 1 + DisplayWidth(short) + 2 + DisplayWidth(rel) + 2 + DisplayWidth(msgs)
		titleWidth := minInt(72, maxInt(4, width-reserved))
		title := Truncate(dash(hit.Title), titleWidth)
		fmt.Fprintln(w, strings.Join([]string{
			num, badge, short, title, muted.Render(rel), muted.Render(msgs),
		}, " "))
		writeLastRound(w, hit, query, theme, match, width, 6)
		if number+1 < len(hits) {
			fmt.Fprintln(w)
		}
	}
}

func renderSearchTTYCards(w io.Writer, query string, hits []SearchHit) {
	theme := NewTheme(w)
	width := Width(w)
	renderSearchHeaderTTY(w, query, len(hits))
	if len(hits) == 0 {
		fmt.Fprintln(w, theme.Style(TokenMuted).Render("No matches."))
		return
	}
	muted := theme.Style(TokenMuted)
	match := theme.Style(TokenPrimary)
	for number, hit := range hits {
		fmt.Fprintf(w, "%s %s %s %s\n",
			theme.Style(TokenPrimary).Render(fmt.Sprintf("%d.", number+1)),
			theme.Badge(hit.Tool),
			ShortID(hit.SessionID),
			muted.Render(PlainField(hit.SessionID)))
		fmt.Fprintf(w, "  title: %s\n", Truncate(dash(hit.Title), maxInt(8, width-8)))
		fmt.Fprintf(w, "  cwd: %s\n", muted.Render(Truncate(dash(hit.CWD), maxInt(8, width-8))))
		fmt.Fprintf(w, "  time: %s\n", muted.Render(PlainField(hit.Created)+" .. "+PlainField(hit.Updated)))
		fmt.Fprintf(w, "  messages: %s\n", muted.Render(fmt.Sprintf("%d", hit.MessageCount)))
		fmt.Fprintf(w, "  path: %s\n", muted.Render(Truncate(PathSummary(hit.SourcePaths), maxInt(8, width-8))))
		writeLastRound(w, hit, query, theme, match, width, 10)
		fmt.Fprintln(w)
	}
}

func writeLastRound(w io.Writer, hit SearchHit, query string, theme Theme, match lipgloss.Style, width, maxLines int) {
	userText := strings.TrimSpace(hit.LastUser)
	assistantText := strings.TrimSpace(hit.LastAssistant)
	if userText == "" && assistantText == "" {
		snippet := firstSnippet(hit)
		if snippet == "" || snippet == "-" {
			return
		}
		writeWrappedRole(w, "match", snippet, query, theme.Style(TokenPrimary), match, width, maxLines)
		return
	}
	if userText != "" {
		writeWrappedRole(w, "user", userText, query, theme.Role("user"), match, width, maxLines)
	}
	if assistantText != "" {
		writeWrappedRole(w, "assistant", assistantText, query, theme.Role("assistant"), match, width, maxLines)
	}
}

func writeWrappedRole(w io.Writer, role, text, query string, roleStyle, match lipgloss.Style, width, maxLines int) {
	label := roleStyle.Render(role) + ": "
	pad := DisplayWidth(role) + 2
	if pad < 6 {
		pad = 6
	}
	indent := strings.Repeat(" ", pad)
	budget := maxInt(16, width-pad)
	lines := wrapLines(text, budget, maxLines)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s%s\n", label, Highlight(lines[0], query, match))
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "  %s%s\n", indent, Highlight(line, query, match))
	}
}
