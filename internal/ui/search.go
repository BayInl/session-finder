package ui

import (
	"fmt"
	"io"
)

func firstSnippet(hit SearchHit) string {
	if len(hit.Snippets) == 0 {
		return "-"
	}
	return dash(PlainField(hit.Snippets[0]))
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
		fmt.Fprintf(w, "%d. [%s] %s | title=%s | snippet=%s | path=%s | matches=%d | updated=%s\n",
			number+1, dash(PlainField(hit.Tool)), dash(PlainField(hit.SessionID)),
			Truncate(dash(hit.Title), 72),
			Truncate(firstSnippet(hit), 160),
			Truncate(PathSummary(hit.SourcePaths), 80),
			hit.MatchCount, dash(PlainField(hit.Updated)))
	}
}
