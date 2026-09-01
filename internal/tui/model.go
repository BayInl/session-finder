package tui

import (
	"io"
	"strings"

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
	results []index.SearchResult
	err     error
}

type showMsg struct {
	sessionID string
	rows      []index.ShowRow
	err       error
}

// Model is the Bubble Tea root model for the session-finder TUI.
type Model struct {
	cfg    Config
	store  Backend
	theme  ui.Theme
	color  bool
	keys   keyMap
	width  int
	height int

	query   string
	tool    string
	hits    []ui.SearchHit
	err     string
	loading bool

	list     list.Model
	viewport viewport.Model
	input    textinput.Model
	help     help.Model

	focus     pane
	searching bool
	showHelp  bool
	loadedID  string
	detail    []ui.ShowMessage
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
	color := ui.ColorEnabled(w)
	width, height := defaultWidth, defaultHeight
	sessionList, vp, input, h := newComponents(theme, color, width, height)
	query := strings.TrimSpace(cfg.Query)
	if query != "" {
		input.SetValue(query)
	}
	m := Model{
		cfg:      cfg,
		store:    store,
		theme:    theme,
		color:    color,
		keys:     newKeyMap(),
		width:    width,
		height:   height,
		query:    query,
		tool:     strings.TrimSpace(cfg.Tool),
		list:     sessionList,
		viewport: vp,
		input:    input,
		help:     h,
		loading:  true,
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
	query, tool, limit := m.query, m.tool, m.cfg.Limit
	store := m.store
	return func() tea.Msg {
		results, err := store.Search(query, tool, limit)
		return searchMsg{results: results, err: err}
	}
}

func (m Model) showCmd(sessionID string) tea.Cmd {
	if m.store == nil || sessionID == "" {
		return nil
	}
	store := m.store
	return func() tea.Msg {
		rows, err := store.Show(sessionID)
		return showMsg{sessionID: sessionID, rows: rows, err: err}
	}
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
		m.hits = hitsFromResults(msg.results)
		m.setListItems(m.hits)
		m.loadedID = ""
		m.detail = nil
		m.refreshDetail()
		return m, nil

	case showMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.loadedID = msg.sessionID
		m.detail = showFromRows(msg.rows)
		m.refreshDetail()
		return m, nil

	case tea.MouseWheelMsg:
		m.viewport, _ = m.viewport.Update(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.showHelp {
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help), key.Matches(msg, m.keys.Back), msg.String() == "q":
			m.showHelp = false
			return m, nil
		}
		return m, nil
	}

	if m.searching {
		return m.updateSearch(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showHelp = true
		return m, nil
	case key.Matches(msg, m.keys.Search):
		m.searching = true
		m.input.SetValue(m.query)
		m.input.CursorEnd()
		return m, m.input.Focus()
	case key.Matches(msg, m.keys.Back):
		return m.goBack()
	case key.Matches(msg, m.keys.Focus):
		if m.focus == paneList {
			m.focus = paneDetail
		} else {
			m.focus = paneList
		}
		return m, nil
	case key.Matches(msg, m.keys.ToolCycle):
		m.tool = nextTool(m.tool)
		m.loading = true
		return m, m.searchCmd()
	case len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '5':
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(record.Tools) {
			if m.tool == record.Tools[idx] {
				m.tool = ""
			} else {
				m.tool = record.Tools[idx]
			}
			m.loading = true
			return m, m.searchCmd()
		}
	case key.Matches(msg, m.keys.Load):
		hit := m.selectedHit()
		if hit == nil {
			return m, nil
		}
		m.focus = paneDetail
		return m, m.showCmd(hit.SessionID)
	case key.Matches(msg, m.keys.PageUp):
		m.viewport.PageUp()
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.viewport.PageDown()
		return m, nil
	case key.Matches(msg, m.keys.HalfUp):
		m.viewport.HalfPageUp()
		return m, nil
	case key.Matches(msg, m.keys.HalfDown):
		m.viewport.HalfPageDown()
		return m, nil
	case key.Matches(msg, m.keys.Top):
		if m.focus == paneDetail {
			m.viewport.GotoTop()
		} else {
			m.list.Select(0)
			m.refreshDetail()
		}
		return m, nil
	case key.Matches(msg, m.keys.Bottom):
		if m.focus == paneDetail {
			m.viewport.GotoBottom()
		} else if n := len(m.list.Items()); n > 0 {
			m.list.Select(n - 1)
			m.refreshDetail()
		}
		return m, nil
	case key.Matches(msg, m.keys.Up):
		if m.focus == paneDetail {
			m.viewport.ScrollUp(1)
		} else {
			m.list.CursorUp()
			m.refreshDetail()
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.focus == paneDetail {
			m.viewport.ScrollDown(1)
		} else {
			m.list.CursorDown()
			m.refreshDetail()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.searching = false
		m.input.Blur()
		m.input.SetValue(m.query)
		return m, nil
	case "enter":
		m.searching = false
		m.input.Blur()
		m.query = strings.TrimSpace(m.input.Value())
		m.loading = true
		m.focus = paneList
		return m, m.searchCmd()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) goBack() (tea.Model, tea.Cmd) {
	if m.searching {
		m.searching = false
		m.input.Blur()
		m.input.SetValue(m.query)
		return m, nil
	}
	if m.focus == paneDetail {
		m.focus = paneList
		return m, nil
	}
	if m.loadedID != "" || len(m.detail) > 0 {
		m.loadedID = ""
		m.detail = nil
		m.refreshDetail()
		return m, nil
	}
	if m.tool != "" {
		m.tool = ""
		m.loading = true
		return m, m.searchCmd()
	}
	if m.query != "" {
		m.query = ""
		m.input.SetValue("")
		m.loading = true
		return m, m.searchCmd()
	}
	return m, nil
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
	m.viewport.SetContent(m.detailContent())
	m.viewport.GotoTop()
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
