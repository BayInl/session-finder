package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

// RenderShow writes session messages. JSON callers must not use this.
func RenderShow(w io.Writer, rows []ShowMessage) {
	if !IsTTY(w) {
		renderShowPlain(w, rows)
		return
	}
	renderShowTTY(w, rows)
}

func renderShowPlain(w io.Writer, rows []ShowMessage) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No matching session.")
		return
	}
	currentTool, currentSession := "", ""
	for _, row := range rows {
		if row.Tool != currentTool || row.SessionID != currentSession {
			if currentTool != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "=== [%s] %s ===\n", row.Tool, row.SessionID)
			fmt.Fprintf(w, "title: %s\n", dash(row.Title))
			fmt.Fprintf(w, "cwd: %s\n", dash(row.CWD))
			currentTool, currentSession = row.Tool, row.SessionID
		}
		fmt.Fprintf(w, "\n[%s] %s\n", row.Timestamp, row.Role)
		fmt.Fprintln(w, row.Text)
	}
}

func renderShowTTY(w io.Writer, rows []ShowMessage) {
	theme := NewTheme(w)
	if len(rows) == 0 {
		fmt.Fprintln(w, theme.Style(TokenMuted).Render("No matching session."))
		return
	}
	width := Width(w)
	muted := theme.Style(TokenMuted)
	cardWidth := minInt(width, 80)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if cardWidth > 4 {
		card = card.Width(cardWidth - 2)
	}
	if ColorEnabled(w) {
		card = card.BorderForeground(lipgloss.Color(theme.Palette.Muted))
	}

	currentTool, currentSession := "", ""
	for _, row := range rows {
		if row.Tool != currentTool || row.SessionID != currentSession {
			if currentTool != "" {
				fmt.Fprintln(w)
			}
			header := lipgloss.JoinVertical(lipgloss.Left,
				theme.Badge(row.Tool)+" "+ShortID(row.SessionID),
				muted.Render(row.SessionID),
				"title: "+dash(row.Title),
				"cwd: "+dash(row.CWD),
			)
			fmt.Fprintln(w, card.Render(header))
			currentTool, currentSession = row.Tool, row.SessionID
		}
		fmt.Fprintf(w, "\n%s %s\n", muted.Render("["+row.Timestamp+"]"), theme.Role(row.Role).Render(row.Role))
		fmt.Fprintln(w, row.Text)
	}
}
