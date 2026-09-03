package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Quit      key.Binding
	Search    key.Binding
	Up        key.Binding
	Down      key.Binding
	Load      key.Binding
	Back      key.Binding
	Focus     key.Binding
	ToolCycle key.Binding
	Top       key.Binding
	Bottom    key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	HalfUp    key.Binding
	HalfDown  key.Binding
	Yank      key.Binding
	YankAll   key.Binding
	Help      key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c", "ctrl+q"), key.WithHelp("q/esc", "quit")),
		Search:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Up:        key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("j/k", "move")),
		Down:      key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "move")),
		Load:      key.NewBinding(key.WithKeys("enter", "return", "l"), key.WithHelp("enter/l", "load")),
		Back:      key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "back")),
		Focus:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "pane")),
		ToolCycle: key.NewBinding(key.WithKeys("t"), key.WithHelp("t/1-5", "tool")),
		Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g/G", "top/end")),
		Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "end")),
		PageUp:    key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "scroll")),
		HalfUp:    key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u/d", "scroll")),
		HalfDown:  key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "scroll")),
		Yank:      key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy message")),
		YankAll:   key.NewBinding(key.WithKeys("Y"), key.WithHelp("Y", "copy transcript")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Search, k.Up, k.Load, k.Yank, k.YankAll, k.Back, k.Focus, k.ToolCycle, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Search, k.Up, k.Down, k.Load, k.Back},
		{k.Focus, k.ToolCycle, k.Top, k.Bottom},
		{k.PageUp, k.PageDown, k.HalfUp, k.HalfDown},
		{k.Yank, k.YankAll, k.Help, k.Quit},
	}
}
