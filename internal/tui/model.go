package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
	"github.com/BayInl/session-finder/internal/ui"
)

type pane int

const (
	paneList pane = iota
	paneDetail
)

type searchMsg struct {
	seq     uint64
	results []index.SearchResult
	err     error
}

type showMsg struct {
	seq       uint64
	sessionID string
	rows      []index.ShowRow
	err       error
}

type clearCopyStatusMsg struct {
	seq uint64
}

// Model is the Bubble Tea root model for the session-finder TUI.
type Model struct {
	store     Backend
	clipboard clipboardWriter
	theme     ui.Theme
	color     bool
	keys      keyMap
	limit     int
	width     int
	height    int

	query     string
	tool      string
	hits      []ui.SearchHit
	err       string
	loading   bool
	searchSeq uint64
	showSeq   uint64

	list     list.Model
	viewport viewport.Model
	input    textinput.Model
	help     help.Model

	focus            pane
	searching        bool
	showHelp         bool
	loadedID         string
	detail           []ui.ShowMessage
	detailIndex      int
	detailHeaderLine int
	copyStatus       string
	copySeq          uint64
}

// New constructs a TUI model. Size messages are not required; tests pass fake sizes via Update.
func New(cfg Config, store Backend, w io.Writer) Model {
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if w == nil {
		w = io.Discard
	}
	theme := ui.NewTheme(w)
	color := theme.ColorEnabled()
	width, height := defaultWidth, defaultHeight
	sessionList, vp, input, h := newComponents(theme, color, width, height)
	query := strings.TrimSpace(cfg.Query)
	if query != "" {
		input.SetValue(query)
	}
	m := Model{
		store:     store,
		clipboard: newSystemClipboard(),
		theme:     theme,
		color:     color,
		keys:      newKeyMap(),
		limit:     cfg.Limit,
		width:     width,
		height:    height,
		query:     query,
		tool:      strings.TrimSpace(cfg.Tool),
		list:      sessionList,
		viewport:  vp,
		input:     input,
		help:      h,
		loading:   true,
		searchSeq: 1,
	}
	m.layout()
	return m
}

func (m Model) Init() tea.Cmd {
	return m.searchCmd()
}

func (m Model) searchCmd() tea.Cmd {
	if m.store == nil {
		return nil
	}
	seq, query, tool, limit := m.searchSeq, m.query, m.tool, m.limit
	store := m.store
	return func() tea.Msg {
		results, err := store.Search(query, tool, limit)
		return searchMsg{seq: seq, results: results, err: err}
	}
}

func (m *Model) startSearch() tea.Cmd {
	m.searchSeq++
	m.showSeq++
	m.loading = true
	return m.searchCmd()
}

func (m Model) showCmd(sessionID string) tea.Cmd {
	if m.store == nil || sessionID == "" {
		return nil
	}
	seq, store := m.showSeq, m.store
	return func() tea.Msg {
		rows, err := store.Show(sessionID)
		return showMsg{seq: seq, sessionID: sessionID, rows: rows, err: err}
	}
}

func (m *Model) startShow(sessionID string) tea.Cmd {
	m.showSeq++
	return m.showCmd(sessionID)
}

// Update handles keys, resize, search/show results, and mouse wheel.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = maxInt(20, msg.Width)
		m.height = maxInt(8, msg.Height)
		m.layout()
		m.refreshDetail()
		return m, nil

	case searchMsg:
		if msg.seq != m.searchSeq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			m.hits = nil
			m.setListItems(nil)
			m.loadedID = ""
			m.detail = nil
			m.refreshDetail()
			return m, nil
		}
		m.err = ""
		m.hits = ui.HitsFromResults(msg.results)
		m.setListItems(m.hits)
		m.loadedID = ""
		m.detail = nil
		m.refreshDetail()
		return m, nil

	case showMsg:
		if msg.seq != m.showSeq {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.loadedID = msg.sessionID
		m.detail = ui.MessagesFromRows(msg.rows)
		m.detailIndex = 0
		m.refreshDetail()
		return m, nil

	case clipboardResultMsg:
		if msg.seq != m.copySeq {
			return m, nil
		}
		m.setCopyStatus(msg.chars, msg.truncated, msg.err)
		return m, m.clearCopyStatusCmd(msg.seq)

	case clearCopyStatusMsg:
		if msg.seq == m.copySeq {
			m.copyStatus = ""
		}
		return m, nil

	case tea.MouseWheelMsg:
		if m.focus == paneDetail {
			m.viewport, _ = m.viewport.Update(msg)
			return m, nil
		}
		delta := maxInt(1, m.viewport.MouseWheelDelta)
		switch msg.Button {
		case tea.MouseWheelUp:
			m.moveList(-delta)
		case tea.MouseWheelDown:
			m.moveList(delta)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.KeyReleaseMsg:
		return m, nil
	}
	return m, nil
}

func bindingHit(msg tea.KeyPressMsg, b key.Binding) bool {
	if key.Matches(msg, b) {
		return true
	}
	s, stroke := msg.String(), msg.Keystroke()
	for _, name := range b.Keys() {
		if s == name || stroke == name {
			return true
		}
		if name == "esc" && (msg.Code == tea.KeyEsc || s == "escape") {
			return true
		}
		if name == "enter" && (msg.Code == tea.KeyEnter || s == "return") {
			return true
		}
	}
	return false
}

func isEsc(msg tea.KeyPressMsg) bool {
	return msg.Code == tea.KeyEsc || msg.String() == "esc" || msg.String() == "escape" || msg.Keystroke() == "esc"
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.showHelp {
		switch {
		case bindingHit(msg, m.keys.Quit):
			return m, tea.Quit
		case bindingHit(msg, m.keys.Help), bindingHit(msg, m.keys.Back), msg.String() == "q", isEsc(msg):
			m.showHelp = false
			return m, nil
		}
		return m, nil
	}

	if m.searching {
		return m.updateSearch(msg)
	}

	switch {
	case bindingHit(msg, m.keys.Quit):
		return m, tea.Quit
	case bindingHit(msg, m.keys.Help):
		m.showHelp = true
		return m, nil
	case bindingHit(msg, m.keys.Search):
		m.searching = true
		m.input.SetValue(m.query)
		m.input.CursorEnd()
		return m, m.input.Focus()
	case bindingHit(msg, m.keys.Back):
		if isEsc(msg) {
			return m.escBack()
		}
		return m.goBack()
	case bindingHit(msg, m.keys.Focus):
		if m.focus == paneList {
			m.focus = paneDetail
		} else {
			m.focus = paneList
		}
		return m, nil
	case bindingHit(msg, m.keys.ToolCycle):
		m.tool = nextTool(m.tool)
		return m, m.startSearch()
	case len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '5':
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(record.Tools) {
			if m.tool == record.Tools[idx] {
				m.tool = ""
			} else {
				m.tool = record.Tools[idx]
			}
			return m, m.startSearch()
		}
	case bindingHit(msg, m.keys.Load):
		hit := m.selectedHit()
		if hit == nil {
			return m, nil
		}
		m.focus = paneDetail
		return m, m.startShow(hit.SessionID)
	case bindingHit(msg, m.keys.Yank):
		if !m.hasLoadedDetail() {
			return m, nil
		}
		return m.copyText(m.detail[m.detailIndex].Text)
	case bindingHit(msg, m.keys.YankAll):
		if !m.hasLoadedDetail() {
			return m, nil
		}
		return m.copyText(m.transcriptText())
	case bindingHit(msg, m.keys.PageUp):
		if m.focus == paneDetail {
			m.viewport.PageUp()
		} else {
			m.moveList(-m.listPageSize())
		}
		return m, nil
	case bindingHit(msg, m.keys.PageDown):
		if m.focus == paneDetail {
			m.viewport.PageDown()
		} else {
			m.moveList(m.listPageSize())
		}
		return m, nil
	case bindingHit(msg, m.keys.HalfUp):
		if m.focus == paneDetail {
			m.viewport.HalfPageUp()
		} else {
			m.moveList(-maxInt(1, m.listPageSize()/2))
		}
		return m, nil
	case bindingHit(msg, m.keys.HalfDown):
		if m.focus == paneDetail {
			m.viewport.HalfPageDown()
		} else {
			m.moveList(maxInt(1, m.listPageSize()/2))
		}
		return m, nil
	case bindingHit(msg, m.keys.Top):
		if m.focus == paneDetail {
			m.detailIndex = 0
			m.refreshDetail()
		} else {
			m.selectList(0)
		}
		return m, nil
	case bindingHit(msg, m.keys.Bottom):
		if m.focus == paneDetail {
			if len(m.detail) > 0 {
				m.detailIndex = len(m.detail) - 1
			}
			m.refreshDetail()
			m.viewport.GotoBottom()
		} else if n := len(m.list.Items()); n > 0 {
			m.selectList(n - 1)
		}
		return m, nil
	case bindingHit(msg, m.keys.Up):
		if m.focus == paneDetail {
			m.moveDetail(-1)
		} else {
			m.moveList(-1)
		}
		return m, nil
	case bindingHit(msg, m.keys.Down):
		if m.focus == paneDetail {
			m.moveDetail(1)
		} else {
			m.moveList(1)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c", msg.Keystroke() == "ctrl+c", msg.String() == "ctrl+q":
		return m, tea.Quit
	case isEsc(msg):
		m.searching = false
		m.input.Blur()
		m.input.SetValue(m.query)
		return m, nil
	case msg.String() == "enter", msg.String() == "return", msg.Code == tea.KeyEnter:
		m.searching = false
		m.input.Blur()
		m.query = strings.TrimSpace(m.input.Value())
		m.focus = paneList
		return m, m.startSearch()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) escBack() (tea.Model, tea.Cmd) {
	if m.focus == paneDetail {
		m.focus = paneList
		return m, nil
	}
	return m, tea.Quit
}

func (m Model) goBack() (tea.Model, tea.Cmd) {
	if m.focus == paneDetail {
		m.focus = paneList
		return m, nil
	}
	if m.loadedID != "" || len(m.detail) > 0 {
		m.showSeq++
		m.loadedID = ""
		m.detail = nil
		m.refreshDetail()
		return m, nil
	}
	if m.tool != "" {
		m.tool = ""
		return m, m.startSearch()
	}
	if m.query != "" {
		m.query = ""
		m.input.SetValue("")
		return m, m.startSearch()
	}
	return m, tea.Quit
}

func (m *Model) setListItems(hits []ui.SearchHit) {
	items := make([]list.Item, len(hits))
	for i, hit := range hits {
		items[i] = sessionItem{hit: hit}
	}
	m.list.SetItems(items)
	if len(items) > 0 {
		m.list.Select(0)
	}
}

func (m Model) selectedHit() *ui.SearchHit {
	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		return nil
	}
	hit := item.hit
	return &hit
}

func (m Model) listPageSize() int {
	return maxInt(1, m.list.Height())
}

func (m Model) hasLoadedDetail() bool {
	hit := m.selectedHit()
	return m.focus == paneDetail && hit != nil && m.loadedID == hit.SessionID && len(m.detail) > 0 && m.detailIndex >= 0 && m.detailIndex < len(m.detail)
}

func (m *Model) moveList(delta int) {
	items := m.list.Items()
	if len(items) == 0 || delta == 0 {
		return
	}
	index := m.list.Index() + delta
	if index < 0 {
		index = 0
	}
	if index >= len(items) {
		index = len(items) - 1
	}
	m.selectList(index)
}

func (m *Model) moveDetail(delta int) {
	if len(m.detail) == 0 || delta == 0 {
		return
	}
	m.detailIndex += delta
	if m.detailIndex < 0 {
		m.detailIndex = 0
	}
	if m.detailIndex >= len(m.detail) {
		m.detailIndex = len(m.detail) - 1
	}
	m.refreshDetail()
}

func (m *Model) selectList(index int) {
	previous := m.list.Index()
	m.list.Select(index)
	if m.list.Index() != previous {
		m.showSeq++
	}
	m.refreshDetail()
}

func (m *Model) layout() {
	leftW, rightW, midH := m.paneSizes()
	listW, listH := paneInnerSize(leftW, midH)
	detailW, detailH := paneInnerSize(rightW, midH)
	m.list.SetSize(maxInt(8, listW), listH)
	m.viewport.SetWidth(maxInt(8, detailW))
	m.viewport.SetHeight(detailH)
	m.input.SetWidth(maxInt(8, m.width-4))
	m.help.SetWidth(m.width)
}

func (m Model) paneSizes() (leftW, rightW, midH int) {
	width := maxInt(20, m.width)
	height := maxInt(8, m.height)
	leftW = width * leftRatio / 100
	if leftW < 22 {
		leftW = minInt(22, width/2)
	}
	rightW = width - leftW
	if rightW < 12 {
		rightW = maxInt(12, width-leftW)
	}
	midH = height - 2
	if midH < 4 {
		midH = 4
	}
	return leftW, rightW, midH
}

func (m *Model) refreshDetail() {
	content, headerLine := m.detailContentWithHeaderLine()
	m.detailHeaderLine = headerLine
	m.viewport.SetContent(content)
	m.viewport.GotoTop()
	if !m.hasLoadedDetail() || m.detailIndex == 0 || headerLine < 0 {
		return
	}
	m.viewport.SetYOffset(maxInt(0, headerLine-m.viewport.Height()/2))
}

func (m Model) copyText(text string) (tea.Model, tea.Cmd) {
	if m.clipboard == nil || text == "" {
		return m, nil
	}
	m.copySeq++
	seq := m.copySeq
	plan := m.clipboard.Copy(text, seq)
	if plan.cmd == nil {
		return m, nil
	}
	if plan.async {
		return m, plan.cmd
	}
	m.setCopyStatus(plan.chars, plan.truncated, nil)
	return m, tea.Sequence(plan.cmd, m.clearCopyStatusCmd(seq))
}

func (m *Model) setCopyStatus(chars int, truncated bool, err error) {
	if err != nil {
		m.copyStatus = "copy failed: " + err.Error()
		return
	}
	m.copyStatus = "copied " + fmt.Sprint(chars) + " chars"
	if truncated {
		m.copyStatus += " (truncated)"
	}
}

func (m Model) clearCopyStatusCmd(seq uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return clearCopyStatusMsg{seq: seq}
	})
}

func (m Model) transcriptText() string {
	var b strings.Builder
	for i, row := range m.detail {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if row.Role != "" {
			b.WriteString(row.Role)
			b.WriteString(":\n")
		}
		b.WriteString(row.Text)
	}
	return b.String()
}

func nextTool(current string) string {
	if current == "" {
		if len(record.Tools) == 0 {
			return ""
		}
		return record.Tools[0]
	}
	for i, name := range record.Tools {
		if name == current {
			if i+1 < len(record.Tools) {
				return record.Tools[i+1]
			}
			return ""
		}
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
