package ui

import (
	"fmt"
	"io"
)

func firstSnippet(hit SearchHit) string {
	if len(hit.Snippets) == 0 {
		return "-"
	}
	return hit.Snippets[0]
}

// RenderSearch writes compact one-line hits. JSON callers must not use this.
// TTY interactive search goes through the TUI, not this renderer.
func RenderSearch(w io.Writer, query string, hits []SearchHit) {
	fmt.Fprintf(w, "search: %s (%d sessions)\n", PythonQuote(StripANSI(query)), len(hits))
	if len(hits) == 0 {
		fmt.Fprintln(w, "No matches.")
		return
	}
	for number, hit := range hits {
		fmt.Fprintf(w, "%d. [%s] %s | title=%s | snippet=%s | path=%s | messages=%d | updated=%s\n",
			number+1, PlainField(hit.Tool), PlainField(hit.SessionID),
			PlainField(Truncate(dash(hit.Title), 72)),
			PlainField(Truncate(firstSnippet(hit), 160)),
			PlainField(Truncate(PathSummary(hit.SourcePaths), 80)),
			hit.MessageCount, PlainField(hit.Updated))
	}
}
