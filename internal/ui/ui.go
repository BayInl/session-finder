package ui

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/BayInl/session-finder/internal/brand"
	"github.com/BayInl/session-finder/internal/fault"
	"github.com/BayInl/session-finder/internal/index"
	"golang.org/x/term"
)

// ColorEnabled implements: nonempty NO_COLOR => off;
// nonempty CLICOLOR_FORCE => on; else IsTTY(w) && TERM != "dumb".
func ColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}
	return IsTTY(w) && os.Getenv("TERM") != "dumb"
}

type fdWriter interface {
	Fd() uintptr
}

// IsTTY reports a character device via w.(interface{ Fd() uintptr })
// and golang.org/x/term.IsTerminal. bytes.Buffer / pipes => false.
func IsTTY(w io.Writer) bool {
	f, ok := w.(fdWriter)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Width is x/term.GetSize(fd) if possible, else COLUMNS, else 80.
func Width(w io.Writer) int {
	if f, ok := w.(fdWriter); ok {
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
			return cols
		}
	}
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return columns
	}
	return 80
}

// WriteJSON encodes v with SetEscapeHTML(false), indent "  ", and a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// printedError marks a FlagSet parse failure that was already written to the
// FlagSet output, so PrintError must not reprint it.
type printedError struct{ err error }

func (e printedError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e printedError) Unwrap() error { return e.err }

// Printed wraps a FlagSet parse error that has already been shown. Help is nil.
func Printed(err error) error {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return printedError{err}
}

// Parse parses set and returns nil for --help. Other parse errors are marked
// already-printed because FlagSet wrote them to its output.
func Parse(set *flag.FlagSet, argv []string) error {
	if set == nil {
		return nil
	}
	if err := set.Parse(argv); err != nil {
		return Printed(err)
	}
	return nil
}

// PrintError writes err. TTY adds an "error:" prefix; pipes print err unchanged.
// Errors already shown by flag.FlagSet are skipped.
func PrintError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var shown printedError
	if errors.As(err, &shown) {
		return
	}
	if !IsTTY(w) {
		fmt.Fprintln(w, err)
		return
	}
	theme := NewTheme(w)
	lines := strings.Split(err.Error(), "\n")
	label := "error:"
	if kind := fault.KindOf(err); kind != "" {
		label = "error [" + string(kind) + "]:"
	}
	fmt.Fprintln(w, theme.Style(TokenError).Render(label)+" "+lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintln(w, theme.Style(TokenMuted).Render(line))
	}
}

// PrintVersion writes version metadata. Non-color output is byte-stable.
func PrintVersion(w io.Writer, version, commit, date string) {
	if !ColorEnabled(w) {
		fmt.Fprintf(w, "%s version %s\ncommit: %s\ndate: %s\n", brand.Name, version, commit, date)
		return
	}
	theme := NewTheme(w)
	muted := theme.Style(TokenMuted)
	fmt.Fprintf(w, "%s version %s\n%s %s\n%s %s\n",
		brand.Name, version, muted.Render("commit:"), commit, muted.Render("date:"), date)
}

// SearchHit is a presentation DTO for one search result.
type SearchHit struct {
	Tool, SessionID, Title, CWD, Created, Updated string
	MessageCount, MatchCount                      int
	Snippets                                      []string
	SourcePaths                                   []string
	LastUser                                      string
	LastAssistant                                 string
}

// HitsFromResults converts index results into presentation DTOs.
func HitsFromResults(results []index.SearchResult) []SearchHit {
	hits := make([]SearchHit, len(results))
	for i, result := range results {
		hits[i] = SearchHit{
			Tool: result.Tool, SessionID: result.SessionID, Title: result.Title,
			CWD: result.CWD, Created: result.Created, Updated: result.Updated,
			MessageCount: result.MessageCount, MatchCount: result.MatchCount,
			Snippets: result.Snippets, SourcePaths: result.SourcePaths,
			LastUser: result.LastUser, LastAssistant: result.LastAssistant,
		}
	}
	return hits
}

// ShowMessage is a presentation DTO for one show row.
type ShowMessage struct {
	Tool, SessionID, Title, CWD, Role, Timestamp, Text string
}

// MessagesFromRows converts index rows into presentation DTOs.
func MessagesFromRows(rows []index.ShowRow) []ShowMessage {
	out := make([]ShowMessage, len(rows))
	for i, row := range rows {
		out[i] = ShowMessage{
			Tool: row.Tool, SessionID: row.SessionID, Title: row.Title, CWD: row.CWD,
			Role: row.Role, Timestamp: row.Timestamp, Text: row.Text,
		}
	}
	return out
}

// Table is a TTY table. Pipe/TSV callers do not use this type.
type Table struct {
	Headers []string
	Rows    [][]string
	// StatusCol is the 0-based column colored with Theme.Status.
	// A negative value disables status coloring.
	StatusCol int
}

// FlagGroup names flags shown together in grouped --help output.
type FlagGroup struct {
	Title string
	Names []string
}
