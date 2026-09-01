package tui

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/ui"
)

type fakeStore struct {
	hits []index.SearchResult
	rows map[string][]index.ShowRow
}

func (f fakeStore) Search(query, tool string, limit int) ([]index.SearchResult, error) {
	out := make([]index.SearchResult, 0, len(f.hits))
	q := strings.ToLower(strings.TrimSpace(query))
	for _, hit := range f.hits {
		if tool != "" && hit.Tool != tool {
			continue
		}
		if q != "" {
			blob := strings.ToLower(hit.Title + " " + hit.LastUser + " " + hit.LastAssistant + " " + strings.Join(hit.Snippets, " "))
			if !strings.Contains(blob, q) {
				continue
			}
		}
		out = append(out, hit)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f fakeStore) Show(sessionID string) ([]index.ShowRow, error) {
	if f.rows == nil {
		return nil, nil
	}
	return f.rows[sessionID], nil
}

func testHits() []index.SearchResult {
	return []index.SearchResult{
		{
			Tool: "codex", SessionID: "session-alpha", Title: "Alpha deploy",
			CWD: "/workspace/alpha", Created: "2024-01-01T00:00:00Z", Updated: "2024-01-02T00:00:00Z",
			MessageCount: 4, Snippets: []string{"deploy alpha"}, LastUser: "deploy alpha", LastAssistant: "ok",
		},
		{
			Tool: "grok", SessionID: "session-beta", Title: "Beta search",
			CWD: "/workspace/beta", Created: "2024-01-03T00:00:00Z", Updated: "2024-01-04T00:00:00Z",
			MessageCount: 2, Snippets: []string{"search beta"}, LastUser: "search beta", LastAssistant: "found",
		},
		{
			Tool: "claude", SessionID: "session-gamma", Title: "Gamma notes",
			CWD: "/tmp", Created: "2024-01-05T00:00:00Z", Updated: "2024-01-06T00:00:00Z",
			MessageCount: 8, Snippets: []string{"notes"}, LastUser: "write notes", LastAssistant: "done",
		},
	}
}

func testRows() map[string][]index.ShowRow {
	return map[string][]index.ShowRow{
		"session-alpha": {
			{Tool: "codex", SessionID: "session-alpha", Title: "Alpha deploy", CWD: "/workspace/alpha", Role: "user", Timestamp: "2024-01-01T00:00:01Z", Text: "deploy alpha"},
			{Tool: "codex", SessionID: "session-alpha", Title: "Alpha deploy", CWD: "/workspace/alpha", Role: "assistant", Timestamp: "2024-01-01T00:00:02Z", Text: "ok"},
		},
		"session-beta": {
			{Tool: "grok", SessionID: "session-beta", Title: "Beta search", Role: "user", Timestamp: "2024-01-03T00:00:01Z", Text: "search beta"},
		},
	}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	store := fakeStore{hits: testHits(), rows: testRows()}
	m := New(Config{Query: "alpha", Limit: 20}, store, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()[:1]})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	return m
}

func apply(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T", next)
	}
	if cmd == nil {
		return model
	}
	result := cmd()
	if result == nil {
		return model
	}
	if _, isQuit := result.(tea.QuitMsg); isQuit {
		return model
	}
	follow, _ := model.Update(result)
	updated, ok := follow.(Model)
	if !ok {
		t.Fatalf("follow-up Update returned %T", follow)
	}
	return updated
}

func applyKey(t *testing.T, m Model, stroke string) Model {
	t.Helper()
	return apply(t, m, keyPress(stroke))
}

func keyPress(stroke string) tea.KeyPressMsg {
	switch stroke {
	case "ctrl+c":
		return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'}
	case "ctrl+u":
		return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'u'}
	case "ctrl+d":
		return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'd'}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	default:
		r := []rune(stroke)
		code := rune(0)
		if len(r) == 1 {
			code = r[0]
		}
		return tea.KeyPressMsg{Text: stroke, Code: code}
	}
}

func TestShouldLaunchHonorsPipeAndDumb(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("TERM", "xterm")
	if ShouldLaunch(&buf) {
		t.Fatal("pipe must not launch TUI")
	}
	t.Setenv("TERM", "dumb")
	if ShouldLaunch(&buf) {
		t.Fatal("TERM=dumb must not launch TUI")
	}
}

func TestShouldLaunchHonorsDumbTermOnTTY(t *testing.T) {
	pty := openTestTTY(t)
	if !ui.IsTTY(pty) {
		t.Fatal("test pty must be a TTY so TERM=dumb is independent of the pipe guard")
	}
	var buf bytes.Buffer
	t.Setenv("TERM", "xterm")
	if ShouldLaunch(&buf) {
		t.Fatal("pipe must not launch TUI")
	}
	if !ShouldLaunch(pty) {
		t.Fatal("TTY with TERM=xterm should launch TUI")
	}
	t.Setenv("TERM", "dumb")
	if ShouldLaunch(pty) {
		t.Fatal("TERM=dumb must not launch TUI even on a TTY")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, stroke := range []string{"q", "ctrl+c"} {
		m := newTestModel(t)
		_, cmd := m.Update(keyPress(stroke))
		if cmd == nil {
			t.Fatalf("%s: expected quit cmd", stroke)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s: expected QuitMsg, got %T", stroke, cmd())
		}
	}
}

func TestSearchInputAndSubmit(t *testing.T) {
	m := New(Config{Limit: 20}, fakeStore{hits: testHits(), rows: testRows()}, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	m = applyKey(t, m, "/")
	if !m.searching {
		t.Fatal("expected search mode")
	}
	m = applyKey(t, m, "b")
	m = applyKey(t, m, "e")
	m = applyKey(t, m, "t")
	m = applyKey(t, m, "a")
	m = applyKey(t, m, "enter")
	if m.searching {
		t.Fatal("search mode should end on enter")
	}
	if m.query != "beta" {
		t.Fatalf("query = %q", m.query)
	}
	if len(m.hits) != 1 || m.hits[0].SessionID != "session-beta" {
		t.Fatalf("hits = %#v", m.hits)
	}
}

func TestMoveAndLoadDetail(t *testing.T) {
	m := New(Config{Limit: 20}, fakeStore{hits: testHits(), rows: testRows()}, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	if hit := m.selectedHit(); hit == nil || hit.SessionID != "session-alpha" {
		t.Fatalf("start hit = %#v", hit)
	}
	m = applyKey(t, m, "j")
	if hit := m.selectedHit(); hit == nil || hit.SessionID != "session-beta" {
		t.Fatalf("j hit = %#v", hit)
	}
	m = applyKey(t, m, "k")
	if hit := m.selectedHit(); hit == nil || hit.SessionID != "session-alpha" {
		t.Fatalf("k hit = %#v", hit)
	}
	m = applyKey(t, m, "enter")
	if m.loadedID != "session-alpha" {
		t.Fatalf("loaded = %q", m.loadedID)
	}
	if m.focus != paneDetail {
		t.Fatal("enter should focus detail")
	}
	if len(m.detail) != 2 {
		t.Fatalf("detail rows = %d", len(m.detail))
	}
	view := m.View().Content
	if !strings.Contains(view, "deploy alpha") || !strings.Contains(view, "transcript") {
		t.Fatalf("detail view missing transcript: %q", view)
	}
}

func TestLoadWithLAndEscBack(t *testing.T) {
	m := New(Config{Limit: 20}, fakeStore{hits: testHits(), rows: testRows()}, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = applyKey(t, m, "l")
	if m.loadedID != "session-alpha" || m.focus != paneDetail {
		t.Fatalf("l load: id=%q focus=%v", m.loadedID, m.focus)
	}
	m = applyKey(t, m, "esc")
	if m.focus != paneList {
		t.Fatal("esc from detail should focus list")
	}
	m = applyKey(t, m, "h")
	if m.loadedID != "" {
		t.Fatalf("h should clear detail, loaded=%q", m.loadedID)
	}
}

func TestTabFocusAndToolFilter(t *testing.T) {
	m := New(Config{Limit: 20}, fakeStore{hits: testHits(), rows: testRows()}, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.focus != paneList {
		t.Fatal("default focus is list")
	}
	m = applyKey(t, m, "tab")
	if m.focus != paneDetail {
		t.Fatal("tab should focus detail")
	}
	m = applyKey(t, m, "tab")
	if m.focus != paneList {
		t.Fatal("tab should return to list")
	}

	m = applyKey(t, m, "2")
	if m.tool != "grok" {
		t.Fatalf("key 2 tool = %q", m.tool)
	}
	if len(m.hits) != 1 || m.hits[0].Tool != "grok" {
		t.Fatalf("filtered hits = %#v", m.hits)
	}
	m = applyKey(t, m, "t")
	if m.tool != "codex" {
		t.Fatalf("t cycle tool = %q want codex", m.tool)
	}
}

func TestHelpOverlayAndGoto(t *testing.T) {
	m := New(Config{Limit: 20}, fakeStore{hits: testHits(), rows: testRows()}, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = applyKey(t, m, "?")
	if !m.showHelp {
		t.Fatal("expected help overlay")
	}
	view := m.View().Content
	if !strings.Contains(view, "quit") && !strings.Contains(strings.ToLower(view), "help") {
		t.Fatalf("help view = %q", view)
	}
	m = applyKey(t, m, "esc")
	if m.showHelp {
		t.Fatal("esc should close help")
	}
	m = applyKey(t, m, "G")
	if hit := m.selectedHit(); hit == nil || hit.SessionID != "session-gamma" {
		t.Fatalf("G should select last: %#v", hit)
	}
	m = applyKey(t, m, "g")
	if hit := m.selectedHit(); hit == nil || hit.SessionID != "session-alpha" {
		t.Fatalf("g should select first: %#v", hit)
	}
}

func TestDetailScrollKeys(t *testing.T) {
	rows := make([]index.ShowRow, 80)
	for i := range rows {
		rows[i] = index.ShowRow{
			Tool: "codex", SessionID: "session-alpha", Title: "Alpha deploy",
			Role: "assistant", Timestamp: "2024-01-01T00:00:00Z",
			Text: fmt.Sprintf("line %d of a long transcript", i),
		}
	}
	store := fakeStore{
		hits: testHits()[:1],
		rows: map[string][]index.ShowRow{"session-alpha": rows},
	}
	m := New(Config{Limit: 20}, store, io.Discard)
	m = apply(t, m, searchMsg{results: testHits()[:1]})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = applyKey(t, m, "enter")
	if m.viewport.YOffset() != 0 {
		t.Fatalf("start offset = %d", m.viewport.YOffset())
	}
	m = applyKey(t, m, "pgdown")
	if m.viewport.YOffset() <= 0 {
		t.Fatalf("pgdown did not scroll: offset=%d height=%d", m.viewport.YOffset(), m.viewport.Height())
	}
	afterPage := m.viewport.YOffset()
	m = applyKey(t, m, "ctrl+d")
	if m.viewport.YOffset() <= afterPage {
		t.Fatalf("ctrl+d did not scroll: offset=%d after pgdown=%d", m.viewport.YOffset(), afterPage)
	}
	afterHalf := m.viewport.YOffset()
	m = applyKey(t, m, "pgup")
	if m.viewport.YOffset() >= afterHalf {
		t.Fatalf("pgup did not scroll up: offset=%d before=%d", m.viewport.YOffset(), afterHalf)
	}
	afterUp := m.viewport.YOffset()
	m = applyKey(t, m, "ctrl+u")
	if m.viewport.YOffset() >= afterUp {
		t.Fatalf("ctrl+u did not scroll up: offset=%d before=%d", m.viewport.YOffset(), afterUp)
	}
}

func TestViewRespectsWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	store := fakeStore{hits: testHits(), rows: testRows()}
	for _, width := range []int{60, 80, 120} {
		m := New(Config{Query: "alpha", Limit: 20}, store, io.Discard)
		m = apply(t, m, searchMsg{results: testHits()})
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		content := m.View().Content
		if content == "" {
			t.Fatalf("width %d: empty view", width)
		}
		if got := lipgloss.Width(content); got > width {
			t.Fatalf("width %d: view width %d\n%s", width, got, content)
		}
		if got := ui.DisplayWidth(firstLine(content)); got > width {
			t.Fatalf("width %d: first line width %d: %q", width, got, firstLine(content))
		}
		if !strings.Contains(content, "session-finder") || !strings.Contains(content, "sessions") {
			t.Fatalf("width %d missing chrome: %q", width, content)
		}
		if !strings.Contains(content, "Alpha deploy") {
			t.Fatalf("width %d missing title: %q", width, content)
		}
	}
}

func TestViewDoesNotPanicWithoutSize(t *testing.T) {
	m := New(Config{}, fakeStore{}, io.Discard)
	_ = m.View()
}

func TestLayoutLeavesRoomForPaneTitle(t *testing.T) {
	m := newTestModel(t)
	leftW, rightW, midH := m.paneSizes()
	listW, listH := paneInnerSize(leftW, midH)
	detailW, detailH := paneInnerSize(rightW, midH)
	if m.list.Width() != listW || m.list.Height() != listH {
		t.Fatalf("list size = %dx%d want %dx%d (pane %dx%d)", m.list.Width(), m.list.Height(), listW, listH, leftW, midH)
	}
	if m.viewport.Width() != detailW || m.viewport.Height() != detailH {
		t.Fatalf("viewport size = %dx%d want %dx%d (pane %dx%d)", m.viewport.Width(), m.viewport.Height(), detailW, detailH, rightW, midH)
	}
	if listH >= midH-paneFrame().GetVerticalFrameSize() {
		t.Fatalf("list height %d should leave a title row inside pane inner height", listH)
	}
}

func TestViewFillsHeightWithoutBlankFooterPad(t *testing.T) {
	m := newTestModel(t)
	content := m.View().Content
	if got := lipgloss.Height(content); got != m.height {
		t.Fatalf("view height = %d want %d\n%s", got, m.height, content)
	}
	lines := strings.Split(content, "\n")
	last := strings.TrimSpace(ui.StripANSI(lines[len(lines)-1]))
	if last == "" {
		t.Fatal("blank row below key-hint footer")
	}
	if !strings.Contains(last, "quit") && !strings.Contains(last, "/") {
		t.Fatalf("last line should be key hints: %q", last)
	}
}

func TestHelpAndInputFollowTheme(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	off := New(Config{Limit: 20}, fakeStore{hits: testHits()}, io.Discard)
	off = apply(t, off, searchMsg{results: testHits()})
	off = apply(t, off, tea.WindowSizeMsg{Width: 100, Height: 24})
	if hasForeground(off.help.Styles.ShortKey) || hasForeground(off.help.Styles.ShortDesc) {
		t.Fatal("help styles must not set color under NO_COLOR")
	}
	if hasForeground(off.input.Styles().Focused.Prompt) || hasForeground(off.input.Styles().Focused.Placeholder) {
		t.Fatal("textinput styles must not set color under NO_COLOR")
	}
	if helpView := off.help.View(off.keys); strings.Contains(helpView, "\x1b[") {
		t.Fatalf("NO_COLOR help has ANSI: %q", helpView)
	}
	if inputView := off.input.View(); strings.Contains(inputView, "\x1b[") {
		t.Fatalf("NO_COLOR textinput has ANSI: %q", inputView)
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	var buf bytes.Buffer
	on := New(Config{Limit: 20}, fakeStore{hits: testHits()}, &buf)
	theme := ui.NewTheme(&buf)
	if !colorEquals(on.help.Styles.ShortKey.GetForeground(), theme.Style(ui.TokenPrimary).GetForeground()) {
		t.Fatalf("help key color = %v want theme primary", on.help.Styles.ShortKey.GetForeground())
	}
	if !colorEquals(on.help.Styles.ShortDesc.GetForeground(), theme.Style(ui.TokenMuted).GetForeground()) {
		t.Fatalf("help desc color = %v want theme muted", on.help.Styles.ShortDesc.GetForeground())
	}
	if !colorEquals(on.input.Styles().Focused.Prompt.GetForeground(), theme.Style(ui.TokenPrimary).GetForeground()) {
		t.Fatalf("input prompt color = %v want theme primary", on.input.Styles().Focused.Prompt.GetForeground())
	}
	if !colorEquals(on.input.Styles().Focused.Placeholder.GetForeground(), theme.Style(ui.TokenMuted).GetForeground()) {
		t.Fatalf("input placeholder color = %v want theme muted", on.input.Styles().Focused.Placeholder.GetForeground())
	}
}

func hasForeground(s lipgloss.Style) bool {
	fg := s.GetForeground()
	if fg == nil {
		return false
	}
	_, ok := fg.(lipgloss.NoColor)
	return !ok
}

func colorEquals(left, right color.Color) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	_, leftNone := left.(lipgloss.NoColor)
	_, rightNone := right.(lipgloss.NoColor)
	if leftNone || rightNone {
		return leftNone && rightNone
	}
	lr, lg, lb, la := left.RGBA()
	rr, rg, rb, ra := right.RGBA()
	return lr == rr && lg == rg && lb == rb && la == ra
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
