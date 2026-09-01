// Package tui is the btop-like full-screen session search and browse app.
package tui

import (
	"io"
	"os"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/ui"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
	defaultLimit  = 100
	leftRatio     = 40
)

// Config is the TUI launch configuration. JSON and pipe callers must not use it.
type Config struct {
	Query         string
	Tool          string
	CWD           string
	After         string
	Limit         int
	DBPath        string
	IncludeSystem bool
	Writer        io.Writer
}

// Backend loads search hits and session transcripts. Tests inject a fake.
type Backend interface {
	Search(query, tool string, limit int) ([]index.SearchResult, error)
	Show(sessionID string) ([]index.ShowRow, error)
}

// ShouldLaunch reports whether a full-screen TUI is allowed.
// Pipes, --json/--plain callers, and TERM=dumb stay on print paths.
func ShouldLaunch(w io.Writer) bool {
	if w == nil {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return ui.IsTTY(w)
}

// Run opens the alt-screen TUI. The caller must have already checked ShouldLaunch.
func Run(cfg Config) error {
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}
	db, err := index.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		return err
	}
	m := New(cfg, newDBBackend(db, cfg), w)
	opts := []tea.ProgramOption{tea.WithOutput(w)}
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		opts = append(opts, tea.WithInput(os.Stdin))
	}
	_, err = tea.NewProgram(m, opts...).Run()
	return err
}

func HitsFromResults(results []index.SearchResult) []ui.SearchHit {
	hits := make([]ui.SearchHit, len(results))
	for i, result := range results {
		hits[i] = ui.SearchHit{
			Tool: result.Tool, SessionID: result.SessionID, Title: result.Title,
			CWD: result.CWD, Created: result.Created, Updated: result.Updated,
			MessageCount: result.MessageCount, Snippets: result.Snippets, SourcePaths: result.SourcePaths,
			LastUser: result.LastUser, LastAssistant: result.LastAssistant,
		}
	}
	return hits
}

func MessagesFromRows(rows []index.ShowRow) []ui.ShowMessage {
	out := make([]ui.ShowMessage, len(rows))
	for i, row := range rows {
		out[i] = ui.ShowMessage{
			Tool: row.Tool, SessionID: row.SessionID, Title: row.Title, CWD: row.CWD,
			Role: row.Role, Timestamp: row.Timestamp, Text: row.Text,
		}
	}
	return out
}

func newComponents(theme ui.Theme, color bool, width, height int) (list.Model, viewport.Model, textinput.Model, help.Model) {
	delegate := sessionDelegate{theme: theme}
	sessionList := list.New(nil, delegate, width, height)
	sessionList.SetShowTitle(false)
	sessionList.SetShowStatusBar(false)
	sessionList.SetShowPagination(false)
	sessionList.SetShowHelp(false)
	sessionList.SetShowFilter(false)
	sessionList.SetFilteringEnabled(false)
	sessionList.DisableQuitKeybindings()
	sessionList.SetStatusBarItemName("session", "sessions")

	vp := viewport.New(viewport.WithWidth(width), viewport.WithHeight(height))
	vp.SoftWrap = true
	vp.MouseWheelEnabled = true
	vp.KeyMap.Up.SetEnabled(false)
	vp.KeyMap.Down.SetEnabled(false)
	vp.KeyMap.Left.SetEnabled(false)
	vp.KeyMap.Right.SetEnabled(false)

	input := textinput.New()
	input.Placeholder = "search sessions"
	input.Prompt = "/ "
	input.CharLimit = 256
	input.SetWidth(maxInt(8, width-4))
	input.SetStyles(inputStyles(theme, color))

	h := help.New()
	h.ShowAll = false
	h.Styles = helpStyles(theme, color)
	h.SetWidth(width)
	return sessionList, vp, input, h
}

func helpStyles(theme ui.Theme, color bool) help.Styles {
	if !color {
		return help.Styles{}
	}
	key := theme.Style(ui.TokenPrimary)
	muted := theme.Style(ui.TokenMuted)
	return help.Styles{
		Ellipsis:       muted,
		ShortKey:       key,
		ShortDesc:      muted,
		ShortSeparator: muted,
		FullKey:        key,
		FullDesc:       muted,
		FullSeparator:  muted,
	}
}

func inputStyles(theme ui.Theme, color bool) textinput.Styles {
	if !color {
		return textinput.Styles{
			Cursor: textinput.CursorStyle{Shape: tea.CursorBlock, Blink: true},
		}
	}
	primary := theme.Style(ui.TokenPrimary)
	muted := theme.Style(ui.TokenMuted)
	return textinput.Styles{
		Focused: textinput.StyleState{
			Placeholder: muted,
			Suggestion:  muted,
			Prompt:      primary,
			Text:        lipgloss.NewStyle(),
		},
		Blurred: textinput.StyleState{
			Placeholder: muted,
			Suggestion:  muted,
			Prompt:      muted,
			Text:        muted,
		},
		Cursor: textinput.CursorStyle{
			Color: lipgloss.Color(theme.Palette.Primary),
			Shape: tea.CursorBlock,
			Blink: true,
		},
	}
}
