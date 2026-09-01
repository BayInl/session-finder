package ui

import (
	"fmt"
	"io"
)

// RenderShow writes the session transcript as plain text.
func RenderShow(w io.Writer, rows []ShowMessage) {
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
