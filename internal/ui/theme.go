package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
)

type Token string

const (
	TokenPrimary Token = "primary"
	TokenMuted   Token = "muted"
	TokenSuccess Token = "success"
	TokenWarn    Token = "warn"
	TokenError   Token = "error"
	TokenInfo    Token = "info"
)

// Palette is the raw semantic map (hex or ANSI-256). Stable for TUI reuse.
type Palette struct {
	Primary, Muted, Success, Warn, Error, Info string
	Tools                                      map[string]string
	Roles                                      map[string]string
}

// DefaultPalette returns 16/256-color-safe defaults.
func DefaultPalette() Palette {
	return Palette{
		Primary: "6",
		Muted:   "8",
		Success: "2",
		Warn:    "3",
		Error:   "1",
		Info:    "4",
		Tools: map[string]string{
			"opencode":  "6",
			"grok":      "5",
			"codex":     "3",
			"kimi-code": "4",
			"claude":    "168",
		},
		Roles: map[string]string{
			"user":      "6",
			"assistant": "2",
			"system":    "8",
		},
	}
}

type Theme struct {
	Palette Palette
	r       *Renderer
}

func NewTheme(w io.Writer) Theme {
	return Theme{Palette: DefaultPalette(), r: NewRenderer(w)}
}

func (t Theme) color() bool {
	return t.r != nil && t.r.color
}

func (t Theme) colorValue(value string) lipgloss.Style {
	s := lipgloss.NewStyle()
	if !t.color() || value == "" {
		return s
	}
	return s.Foreground(lipgloss.Color(value))
}

func (t Theme) Style(token Token) lipgloss.Style {
	if !t.color() {
		return lipgloss.NewStyle()
	}
	switch token {
	case TokenPrimary:
		return t.colorValue(t.Palette.Primary)
	case TokenMuted:
		return t.colorValue(t.Palette.Muted).Faint(true)
	case TokenSuccess:
		return t.colorValue(t.Palette.Success)
	case TokenWarn:
		return t.colorValue(t.Palette.Warn)
	case TokenError:
		return t.colorValue(t.Palette.Error)
	case TokenInfo:
		return t.colorValue(t.Palette.Info)
	default:
		return lipgloss.NewStyle()
	}
}

func (t Theme) Tool(name string) lipgloss.Style {
	if !t.color() {
		return lipgloss.NewStyle()
	}
	if t.Palette.Tools != nil {
		if value, ok := t.Palette.Tools[strings.ToLower(strings.TrimSpace(name))]; ok {
			return t.colorValue(value)
		}
	}
	return t.Style(TokenPrimary)
}

func (t Theme) Role(name string) lipgloss.Style {
	if !t.color() {
		return lipgloss.NewStyle()
	}
	if t.Palette.Roles != nil {
		if value, ok := t.Palette.Roles[strings.ToLower(strings.TrimSpace(name))]; ok {
			return t.colorValue(value)
		}
	}
	return t.Style(TokenMuted)
}

func (t Theme) Status(name string) lipgloss.Style {
	return t.Style(statusToken(name))
}

func statusToken(name string) Token {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "approved", "published", "accepted", "implemented":
		return TokenSuccess
	case "in_review", "proposed":
		return TokenInfo
	case "deferred", "disabled", "warn", "warn-ish":
		return TokenWarn
	case "rejected", "failed", "deleted":
		return TokenError
	default:
		return TokenMuted
	}
}

func (t Theme) Badge(tool string) string {
	name := strings.TrimSpace(tool)
	if name == "" {
		name = "?"
	}
	label := fmt.Sprintf(" %s ", name)
	if !t.color() {
		return label
	}
	bg := t.Palette.Primary
	if t.Palette.Tools != nil {
		if value, ok := t.Palette.Tools[strings.ToLower(name)]; ok {
			bg = value
		}
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color(bg)).
		Render(label)
}
