package tui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/BayInl/session-finder/internal/brand"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
	"github.com/BayInl/session-finder/internal/ui"
)

type sessionItem struct {
	hit ui.SearchHit
}

func (s sessionItem) FilterValue() string {
	return s.hit.Tool + " " + s.hit.SessionID + " " + s.hit.Title
}

type sessionDelegate struct {
	theme ui.Theme
}

func (d sessionDelegate) Height() int                         { return 1 }
func (d sessionDelegate) Spacing() int                        { return 0 }
func (d sessionDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d sessionDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	session, ok := item.(sessionItem)
	if !ok {
		return
	}
	hit := session.hit
	width := m.Width()
	if width < 8 {
		width = 8
	}
	selected := index == m.Index()
	cursor := " "
	if selected {
		cursor = "▸"
	}
	badge := d.theme.Badge(hit.Tool)
	short := ui.ShortID(hit.SessionID)
	rel := ui.RelativeTime(hit.Updated)
	msgs := fmt.Sprintf("%d", hit.MessageCount)
	muted := d.theme.Style(ui.TokenMuted)
	meta := muted.Render(rel) + " " + muted.Render(msgs)
	reserved := ui.DisplayWidth(cursor) + 1 + ui.DisplayWidth(badge) + 1 + ui.DisplayWidth(short) + 1 + ui.DisplayWidth(rel) + 1 + ui.DisplayWidth(msgs) + 1
	titleWidth := maxInt(4, width-reserved)
	title := ui.Truncate(dash(hit.Title), titleWidth)
	line := strings.Join([]string{cursor, badge, short, title, meta}, " ")
	if selected {
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}
	if ui.DisplayWidth(line) > width {
		line = ui.Truncate(ui.StripANSI(line), width)
	}
	fmt.Fprint(w, line)
}

// View renders the alt-screen frame. Tests inspect View().Content with fake sizes.
func (m Model) View() tea.View {
	body := m.renderFrame()
	if m.width > 0 && m.height > 0 {
		body = lipgloss.NewStyle().
			Width(m.width).
			MaxWidth(m.width).
			Height(m.height).
			MaxHeight(m.height).
			Render(body)
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = brand.Name
	return v
}

func (m Model) renderFrame() string {
	if m.showHelp {
		return m.renderHelp()
	}
	leftW, rightW, midH := m.paneSizes()
	status := m.renderStatus(m.width)
	footer := m.renderFooter(m.width)
	left := m.renderListPane(leftW, midH)
	right := m.renderDetailPane(rightW, midH)
	mid := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return lipgloss.JoinVertical(lipgloss.Left, status, mid, footer)
}

func (m Model) renderStatus(width int) string {
	theme := m.theme
	primary := theme.Style(ui.TokenPrimary)
	muted := theme.Style(ui.TokenMuted)
	if m.copyStatus != "" {
		return clipLine(primary.Render(brand.Name)+"  "+m.copyStatus, width)
	}
	query := m.query
	if m.searching {
		query = m.input.View()
	} else if query == "" {
		query = "—"
	}
	count := fmt.Sprintf("%d hits", len(m.hits))
	if m.loading {
		count = "loading…"
	}
	if m.err != "" {
		count = theme.Style(ui.TokenError).Render(m.err)
	} else {
		count = muted.Render(count)
	}
	chips := m.toolChips()
	line := strings.Join([]string{
		primary.Render(brand.Name),
		muted.Render("query"),
		query,
		count,
		chips,
	}, "  ")
	return clipLine(line, width)
}

func (m Model) toolChips() string {
	parts := make([]string, 0, len(record.Tools)+1)
	allLabel := "all"
	if m.tool == "" {
		parts = append(parts, m.theme.Badge(allLabel))
	} else {
		parts = append(parts, m.theme.Style(ui.TokenMuted).Render(allLabel))
	}
	for i, name := range record.Tools {
		label := fmt.Sprintf("%d:%s", i+1, name)
		if m.tool == name {
			parts = append(parts, m.theme.Badge(name))
		} else {
			parts = append(parts, m.theme.Style(ui.TokenMuted).Render(label))
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) renderFooter(width int) string {
	if m.searching {
		return clipLine(m.theme.Style(ui.TokenMuted).Render("enter search  esc cancel  ctrl+c quit"), width)
	}
	return clipLine(m.help.View(m.keys), width)
}

func (m Model) renderListPane(width, height int) string {
	title := "sessions"
	if m.focus == paneList {
		title = "▸ sessions"
	}
	inner := m.list.View()
	if len(m.hits) == 0 && !m.loading {
		inner = m.theme.Style(ui.TokenMuted).Render("No matches. Press / to search.")
	}
	header := m.theme.Style(ui.TokenPrimary).Render(title)
	body := lipgloss.JoinVertical(lipgloss.Left, header, inner)
	return m.paneStyle(width, height, m.focus == paneList).Render(body)
}

func (m Model) renderDetailPane(width, height int) string {
	title := "detail"
	if m.focus == paneDetail {
		title = "▸ detail"
	}
	header := m.theme.Style(ui.TokenPrimary).Render(title)
	body := lipgloss.JoinVertical(lipgloss.Left, header, m.viewport.View())
	return m.paneStyle(width, height, m.focus == paneDetail).Render(body)
}

func (m Model) paneStyle(width, height int, focused bool) lipgloss.Style {
	// lipgloss v2 Width/Height include borders; do not subtract them again.
	s := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		Width(maxInt(1, width)).
		Height(maxInt(1, height)).
		MaxWidth(maxInt(1, width)).
		MaxHeight(maxInt(1, height))
	if m.color {
		fg := m.theme.Palette.Muted
		if focused {
			fg = m.theme.Palette.Primary
		}
		s = s.BorderForeground(lipgloss.Color(fg))
	}
	return s
}

func paneFrame() lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder())
}

func paneInnerSize(width, height int) (innerW, innerH int) {
	frame := paneFrame()
	innerW = maxInt(1, width-frame.GetHorizontalFrameSize())
	innerH = maxInt(1, height-frame.GetVerticalFrameSize()-1) // title row inside the border
	return innerW, innerH
}

func (m Model) detailContent() string {
	content, _ := m.detailContentWithHeaderLine()
	return content
}

func (m Model) detailContentWithHeaderLine() (string, int) {
	hit := m.selectedHit()
	if hit == nil {
		return m.theme.Style(ui.TokenMuted).Render("Select a session."), -1
	}
	muted := m.theme.Style(ui.TokenMuted)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", m.theme.Badge(hit.Tool), ui.ShortID(hit.SessionID))
	fmt.Fprintf(&b, "%s\n", muted.Render(hit.SessionID))
	fmt.Fprintf(&b, "title: %s\n", dash(hit.Title))
	fmt.Fprintf(&b, "cwd: %s\n", dash(hit.CWD))
	fmt.Fprintf(&b, "time: %s .. %s  %s\n", dash(hit.Created), dash(hit.Updated), ui.RelativeTime(hit.Updated))
	countLabel, count := "messages", hit.MessageCount
	if strings.TrimSpace(m.query) != "" {
		countLabel, count = "matches", hit.MatchCount
	}
	fmt.Fprintf(&b, "%s: %d\n\n", countLabel, count)

	userText, assistantText := hit.LastUser, hit.LastAssistant
	if userText == "" && assistantText == "" {
		userText, assistantText = lastRoundFrom(m.detail)
	}
	wrapW := m.viewport.Width()
	if wrapW < 16 {
		wrapW = 16
	}
	terms := index.PositiveTerms(m.query)
	match := matchStyle(m.theme, m.color)
	if userText != "" {
		b.WriteString(wrapRoleText(m.theme.Role("user").Render("user")+": ", "user: ", userText, terms, match, wrapW, 8))
	}
	if assistantText != "" {
		b.WriteString(wrapRoleText(m.theme.Role("assistant").Render("assistant")+": ", "assistant: ", assistantText, terms, match, wrapW, 8))
	}
	b.WriteByte('\n')
	if m.loadedID != hit.SessionID || len(m.detail) == 0 {
		b.WriteString(muted.Render("enter/l load transcript"))
		writeMatchSnippets(&b, hit.Snippets, terms, match, muted, wrapW)
		return b.String(), -1
	}
	b.WriteString(muted.Render("transcript"))
	b.WriteString("\n\n")
	headerLine := -1
	line := strings.Count(b.String(), "\n")
	for i, row := range m.detail {
		if i > 0 {
			b.WriteByte('\n')
			line++
		}
		if i == m.detailIndex {
			headerLine = line
		}
		cursor := " "
		if i == m.detailIndex {
			cursor = "▸"
		}
		fmt.Fprintf(&b, "%s %s %s\n", cursor, muted.Render("["+row.Timestamp+"]"), m.theme.Role(row.Role).Render(row.Role))
		line++
		for _, wrapped := range ui.WrapTextLines(row.Text, wrapW) {
			b.WriteString(ui.HighlightTerms(wrapped, terms, match))
			b.WriteByte('\n')
			line++
		}
	}
	return b.String(), headerLine
}

func matchStyle(theme ui.Theme, color bool) lipgloss.Style {
	if !color {
		return lipgloss.NewStyle().Bold(true).Underline(true)
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color(theme.Palette.Primary))
}

func writeMatchSnippets(b *strings.Builder, snippets []string, terms []string, match, muted lipgloss.Style, width int) {
	if len(snippets) == 0 {
		return
	}
	b.WriteString("\n\n")
	header := muted.Render("match")
	if len(terms) > 0 {
		chips := make([]string, 0, len(terms))
		for _, term := range terms {
			chips = append(chips, match.Render(ui.Truncate(term, 24)))
		}
		header += "  " + strings.Join(chips, muted.Render(" · "))
	}
	b.WriteString(clipLine(header, width))
	b.WriteByte('\n')
	shown := 0
	for _, snippet := range snippets {
		preview := ui.Preview(snippet)
		if preview == "" {
			continue
		}
		if shown > 0 {
			b.WriteString(muted.Render("···"))
			b.WriteByte('\n')
		}
		for _, line := range ui.WrapLines(preview, width, 3) {
			b.WriteString(ui.HighlightTerms(line, terms, match))
			b.WriteByte('\n')
		}
		shown++
	}
}

func (m Model) renderHelp() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		Padding(0, 1).
		Width(minInt(56, maxInt(24, m.width-4)))
	if m.color {
		box = box.BorderForeground(lipgloss.Color(m.theme.Palette.Primary))
	}
	body := strings.Join([]string{
		m.theme.Style(ui.TokenPrimary).Render(brand.Name + " keys"),
		"",
		m.help.FullHelpView(m.keys.FullHelp()),
		"",
		m.theme.Style(ui.TokenMuted).Render("esc/? close"),
	}, "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box.Render(body))
}

func wrapRoleText(styledPrefix, plainPrefix, text string, terms []string, match lipgloss.Style, width, maxLines int) string {
	pad := ui.DisplayWidth(plainPrefix)
	budget := width - pad
	if budget < 8 {
		budget = 8
	}
	lines := ui.WrapLines(ui.Preview(text), budget, maxLines)
	if len(lines) == 0 {
		return ""
	}
	indent := strings.Repeat(" ", pad)
	var b strings.Builder
	b.WriteString(styledPrefix)
	b.WriteString(ui.HighlightTerms(lines[0], terms, match))
	b.WriteByte('\n')
	for _, line := range lines[1:] {
		b.WriteString(indent)
		b.WriteString(ui.HighlightTerms(line, terms, match))
		b.WriteByte('\n')
	}
	return b.String()
}

func lastRoundFrom(rows []ui.ShowMessage) (user, assistant string) {
	for i := len(rows) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(rows[i].Role))
		text := strings.TrimSpace(rows[i].Text)
		if text == "" {
			continue
		}
		switch role {
		case "user":
			if user == "" {
				user = text
			}
		case "assistant":
			if assistant == "" {
				assistant = text
			}
		}
		if user != "" && assistant != "" {
			break
		}
	}
	return user, assistant
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func clipLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	if ui.DisplayWidth(s) <= width {
		return s
	}
	return ui.Truncate(ui.StripANSI(s), width)
}
