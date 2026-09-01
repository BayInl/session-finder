package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

// RenderTable writes a TTY table. Pipe/non-TTY callers print TSV themselves.
func RenderTable(w io.Writer, data Table) {
	theme := NewTheme(w)
	if len(data.Rows) == 0 {
		fmt.Fprintln(w, theme.Style(TokenMuted).Render("No rows."))
		return
	}
	width := Width(w)
	tw := table.NewWriter()
	tw.SetOutputMirror(w)
	style := table.StyleRounded
	style.Format.Header = text.FormatDefault
	style.Color = table.ColorOptions{}
	tw.SetStyle(style)
	tw.SetAllowedRowLength(width)

	header := make(table.Row, len(data.Headers))
	for i, name := range data.Headers {
		header[i] = name
	}
	tw.AppendHeader(header)

	n := len(data.Headers)
	if n == 0 && len(data.Rows) > 0 {
		n = len(data.Rows[0])
	}
	colWidths := columnWidths(n, width)
	configs := make([]table.ColumnConfig, n)
	statusCol := data.StatusCol
	for i := 0; i < n; i++ {
		maxWidth := 8
		if i < len(colWidths) {
			maxWidth = colWidths[i]
		}
		col := i
		configs[i] = table.ColumnConfig{
			Number:   i + 1,
			WidthMax: maxWidth,
			WidthMaxEnforcer: func(s string, max int) string {
				clipped := Truncate(s, max)
				if statusCol >= 0 && col == statusCol {
					return theme.Status(strings.TrimSpace(clipped)).Render(clipped)
				}
				return clipped
			},
		}
	}
	tw.SetColumnConfigs(configs)

	for _, row := range data.Rows {
		item := make(table.Row, len(row))
		for i, cell := range row {
			item[i] = cell
		}
		tw.AppendRow(item)
	}
	tw.Render()
}

func columnWidths(n, total int) []int {
	if n <= 0 {
		return nil
	}
	overhead := 1 + n + 1 + 2*n
	usable := total - overhead
	if usable < n*6 {
		usable = n * 6
	}
	widths := make([]int, n)
	base := usable / n
	extra := usable % n
	for i := range widths {
		widths[i] = base
		if i >= n-extra {
			widths[i]++
		}
		if widths[i] < 6 {
			widths[i] = 6
		}
	}
	if n >= 3 {
		widths[n-1] += widths[0] / 4
	}
	return widths
}

// RenderIndexSummary writes the index summary. Line grammar is stable.
func RenderIndexSummary(w io.Writer, dbPath string, tools []string, counts map[string][2]int, processed, unchanged, errors int) {
	theme := NewTheme(w)
	fmt.Fprintf(w, "index: %s\n", dbPath)
	for _, tool := range tools {
		c := counts[tool]
		name := tool
		if ColorEnabled(w) {
			name = theme.Tool(tool).Render(tool)
		}
		fmt.Fprintf(w, "%s: sessions=%d messages=%d\n", name, c[0], c[1])
	}
	fmt.Fprintf(w, "sources: processed=%d unchanged=%d errors=%d\n", processed, unchanged, errors)
}
