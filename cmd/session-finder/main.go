package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(argv []string) error {
	if len(argv) == 0 {
		return usageError("missing command")
	}
	if argv[0] == "-h" || argv[0] == "--help" {
		printRootUsage()
		return nil
	}
	if argv[0] == "-v" || argv[0] == "--version" {
		if len(argv) != 1 {
			return usageError(argv[0] + " accepts no arguments")
		}
		printVersion(os.Stdout)
		return nil
	}
	return runRegistered(argv[0], argv[1:])
}

func runVersion(argv []string) error {
	if len(argv) != 0 {
		return usageError("version accepts no arguments")
	}
	printVersion(os.Stdout)
	return nil
}

func printVersion(writer io.Writer) {
	fmt.Fprintf(writer, "session-finder version %s\ncommit: %s\ndate: %s\n", version, commit, date)
}

func printRootUsage() {
	fmt.Println(rootUsage())
	fmt.Println("Search local AI sessions from opencode, Grok, Codex, Kimi Code, and Claude.")
}

func usageError(message string) error {
	return fmt.Errorf("%s\n(use --help for usage)", message)
}

func runIndex(argv []string) error {
	set := flag.NewFlagSet("index", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	full := set.Bool("full", false, "rebuild the index from scratch")
	dbPath := set.String("db", "", "path to the SQLite index database")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 0 {
		return usageError("index accepts no positional arguments")
	}
	summary, err := index.IndexAll(*full, *dbPath)
	if err != nil {
		return err
	}
	printIndexSummary(summary)
	return nil
}

func runSearch(argv []string) error {
	set := flag.NewFlagSet("search", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	tool := set.String("tool", "", "restrict to one tool")
	cwd := set.String("cwd", "", "restrict to sessions whose cwd contains this text")
	after := set.String("after", "", "only messages on or after YYYY-MM-DD")
	limit := set.Int("limit", 20, "maximum sessions to show")
	asJSON := set.Bool("json", false, "emit JSON")
	var includeSystem bool
	set.BoolVar(&includeSystem, "all", false, "include system/noise records (default hides them)")
	set.BoolVar(&includeSystem, "include-system", false, "alias for --all")
	verbose := set.Bool("verbose", false, "show full result cards")
	dbPath := set.String("db", "", "path to the SQLite index database")
	query, helpRequested, err := parseFlagsAndArg(set, argv, "search")
	if err != nil {
		return err
	}
	if helpRequested {
		return nil
	}
	if query == "" {
		return usageError("search requires exactly one query")
	}
	if *limit <= 0 {
		return usageError("limit must be positive")
	}
	db, err := index.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		return err
	}
	results, err := index.Search(db, query, *tool, *cwd, *after, *limit, includeSystem)
	if err != nil {
		return err
	}
	if *asJSON {
		payload := struct {
			Query   string               `json:"query"`
			Count   int                  `json:"count"`
			Results []index.SearchResult `json:"results"`
		}{Query: query, Count: len(results), Results: results}
		var output bytes.Buffer
		encoder := json.NewEncoder(&output)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			return err
		}
		fmt.Print(output.String())
		return nil
	}
	printSearchResults(query, results, *verbose)
	return nil
}

func runShow(argv []string) error {
	set := flag.NewFlagSet("show", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	role := set.String("role", "", "filter by role (user, assistant, or system)")
	limit := set.Int("limit", 0, "maximum messages to show")
	dbPath := set.String("db", "", "path to the SQLite index database")
	arg, helpRequested, err := parseFlagsAndArg(set, argv, "show")
	if err != nil {
		return err
	}
	if helpRequested {
		return nil
	}
	if arg == "" {
		return usageError("show requires exactly one session ID or prefix")
	}
	if *role != "" && *role != "user" && *role != "assistant" && *role != "system" {
		return usageError("role must be one of: user, assistant, system")
	}
	limitSet := false
	set.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			limitSet = true
		}
	})
	if limitSet && *limit <= 0 {
		return usageError("--limit must be positive")
	}
	db, err := index.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		return err
	}
	rows, err := index.Show(db, arg, *role, *limit)
	if err != nil {
		return err
	}
	printShow(rows)
	return nil
}

func parseFlagsAndArg(set *flag.FlagSet, argv []string, command string) (string, bool, error) {
	// Python argparse accepts options both before and after the positional
	// argument. Go's standard flag package stops at the first positional, so
	// parse a reordered copy and return the sole positional argument.
	flags := make([]string, 0, len(argv))
	positionals := make([]string, 0, 1)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			positionals = append(positionals, argv[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
			if !strings.Contains(arg, "=") && (name == "tool" || name == "cwd" || name == "after" || name == "limit" || name == "role" || name == "db") && i+1 < len(argv) {
				flags = append(flags, argv[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	if err := set.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", true, nil
		}
		return "", false, err
	}
	if len(positionals) != 1 {
		return "", false, usageError(command + " requires exactly one positional argument")
	}
	return positionals[0], false, nil
}

func printIndexSummary(summary index.Summary) {
	fmt.Printf("index: %s\n", summary.DBPath)
	for _, tool := range record.Tools {
		counts := summary.Tools[tool]
		fmt.Printf("%s: sessions=%d messages=%d\n", tool, counts.Sessions, counts.Messages)
	}
	fmt.Printf("sources: processed=%d unchanged=%d errors=%d\n", summary.Sources.Processed, summary.Sources.Unchanged, summary.Sources.Errors)
}

// printSearchResults keeps the historical two-argument call usable while
// accepting an optional verbose switch for the search command.
func printSearchResults(query string, results []index.SearchResult, verbose ...bool) {
	isVerbose := len(verbose) > 0 && verbose[0]
	tty := stdoutIsTTY()
	if !tty {
		printSearchPlain(query, results, isVerbose)
		return
	}
	printSearchTTY(query, results, isVerbose)
}

func printSearchPlain(query string, results []index.SearchResult, verbose bool) {
	fmt.Printf("search: %s (%d sessions)\n", pythonStyleRepr(stripANSIAndControls(query)), len(results))
	if len(results) == 0 {
		fmt.Println("No matches.")
		return
	}
	for number, result := range results {
		fmt.Printf("%d. [%s] %s | title=%s | snippet=%s | path=%s | messages=%d | updated=%s\n",
			number+1, plainField(result.Tool), plainField(result.SessionID),
			plainField(truncateCells(dash(result.Title), 72)),
			plainField(truncateCells(firstSnippet(result), 160)),
			plainField(truncateCells(pathSummary(result.SourcePaths), 80)),
			result.MessageCount, plainField(result.Updated))
	}
}

func printSearchTTY(query string, results []index.SearchResult, verbose bool) {
	width := terminalColumns()
	color := func(code, value string) string {
		return colorText(code, value)
	}
	fmt.Printf("search: %s (%d sessions)\n", pythonStyleRepr(stripANSIAndControls(query)), len(results))
	if len(results) == 0 {
		fmt.Println("No matches.")
		return
	}
	if verbose {
		for number, result := range results {
			fmt.Printf("%s %s\n", color("1;36", fmt.Sprintf("%d. [%s] %s", number+1, plainField(result.Tool), plainField(result.SessionID))),
				color("2", relativeTime(result.Updated)))
			fmt.Printf("  title: %s\n", truncateCells(dash(result.Title), 72))
			fmt.Printf("  cwd: %s\n", truncateCells(dash(result.CWD), width-8))
			fmt.Printf("  time: %s .. %s\n", plainField(result.Created), plainField(result.Updated))
			fmt.Printf("  messages: %d\n", result.MessageCount)
			fmt.Printf("  snippet: %s\n", truncateCells(firstSnippet(result), 160))
			fmt.Printf("  path: %s\n\n", truncateCells(pathSummary(result.SourcePaths), 80))
		}
		return
	}
	for number, result := range results {
		name := fmt.Sprintf("%d. [%s] %s", number+1, plainField(result.Tool), plainField(result.SessionID))
		title := truncateCells(dash(result.Title), 72)
		lineOne := fmt.Sprintf("%s  %s  %s  %s", name, title, relativeTime(result.Updated), messageCount(result.MessageCount))
		fmt.Println(color("1", truncateCells(lineOne, width)))
		lineTwo := fmt.Sprintf("   %s  %s", truncateCells(firstSnippet(result), 160), truncateCells(pathSummary(result.SourcePaths), 80))
		fmt.Println(truncateCells(lineTwo, width))
	}
}

func colorText(code, value string) string {
	if os.Getenv("NO_COLOR") != "" {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func stdoutIsTTY() bool {
	stat, err := os.Stdout.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}

func terminalColumns() int {
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return columns
	}
	return 80
}

func plainField(value string) string {
	value = stripANSIAndControls(value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	return value
}

func firstSnippet(result index.SearchResult) string {
	if len(result.Snippets) == 0 {
		return "-"
	}
	return result.Snippets[0]
}

func pathSummary(paths []string) string {
	if len(paths) == 0 {
		return "-"
	}
	path := plainField(paths[0])
	if len(paths) > 1 {
		path += fmt.Sprintf(" (+%d)", len(paths)-1)
	}
	return path
}

func messageCount(count int) string {
	return fmt.Sprintf("%d msg%s", count, map[bool]string{true: "s", false: ""}[count != 1])
}

func relativeTime(timestamp string) string {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", timestamp)
	if err != nil || timestamp == "-" {
		return "-"
	}
	delta := time.Now().UTC().Sub(parsed)
	future := delta < 0
	if future {
		delta = -delta
	}
	var value string
	switch {
	case delta < time.Minute:
		value = "just now"
	case delta < time.Hour:
		value = fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		value = fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 30*24*time.Hour:
		value = fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	case delta < 365*24*time.Hour:
		value = fmt.Sprintf("%dmo", int(delta/(30*24*time.Hour)))
	default:
		value = fmt.Sprintf("%dy", int(delta/(365*24*time.Hour)))
	}
	if future && value != "just now" {
		return "in " + value
	}
	if value == "just now" {
		return value
	}
	return value + " ago"
}

func stripANSIAndControls(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for i := 0; i < len(value); {
		if value[i] == 0x1b {
			i++
			if i >= len(value) {
				break
			}
			switch value[i] {
			case '[': // CSI, including SGR colors.
				i++
				for i < len(value) {
					ch := value[i]
					i++
					if ch >= 0x40 && ch <= 0x7e {
						break
					}
				}
			case ']': // OSC title/hyperlink; terminate at BEL or ST.
				i++
				for i < len(value) {
					if value[i] == '\a' {
						i++
						break
					}
					if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				i++
			}
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[i:])
		if runeValue == utf8.RuneError && size == 1 {
			i++
			continue
		}
		i += size
		if runeValue < 0x20 || runeValue == 0x7f {
			if runeValue == '\n' || runeValue == '\r' || runeValue == '\t' {
				result.WriteByte(' ')
			}
			continue
		}
		result.WriteRune(runeValue)
	}
	return result.String()
}

func cellWidth(value rune) int {
	if unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe19) ||
		(value >= 0xfe30 && value <= 0xfe6f) || (value >= 0xff00 && value <= 0xff60) ||
		(value >= 0xffe0 && value <= 0xffe6)) {
		return 2
	}
	return 1
}

func truncateCells(value string, max int) string {
	if max <= 0 {
		return ""
	}
	value = plainField(value)
	if displayWidth(value) <= max {
		return value
	}
	if max == 1 {
		return "…"
	}
	remaining := max - 1
	var result strings.Builder
	width := 0
	for _, runeValue := range value {
		runeWidth := cellWidth(runeValue)
		if width+runeWidth > remaining {
			break
		}
		result.WriteRune(runeValue)
		width += runeWidth
	}
	result.WriteRune('…')
	return result.String()
}

func displayWidth(value string) int {
	width := 0
	for _, runeValue := range value {
		width += cellWidth(runeValue)
	}
	return width
}

func pythonStyleRepr(value string) string {
	quote := byte('\'')
	if strings.Contains(value, "'") && !strings.Contains(value, `"`) {
		quote = '"'
	}

	var result strings.Builder
	result.Grow(len(value) + 2)
	result.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			result.WriteString(`\\`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		case rune(quote):
			result.WriteByte('\\')
			result.WriteRune(char)
		default:
			if unicode.IsPrint(char) {
				result.WriteRune(char)
			} else {
				writeUnicodeEscape(&result, char)
			}
		}
	}
	result.WriteByte(quote)
	return result.String()
}

func writeUnicodeEscape(result *strings.Builder, char rune) {
	switch {
	case char <= 0xff:
		fmt.Fprintf(result, `\x%02x`, char)
	case char <= 0xffff:
		fmt.Fprintf(result, `\u%04x`, char)
	default:
		fmt.Fprintf(result, `\U%08x`, char)
	}
}

func printShow(rows []index.ShowRow) {
	if len(rows) == 0 {
		fmt.Println("No matching session.")
		return
	}
	currentTool, currentSession := "", ""
	for _, row := range rows {
		if row.Tool != currentTool || row.SessionID != currentSession {
			if currentTool != "" {
				fmt.Println()
			}
			fmt.Printf("=== [%s] %s ===\n", row.Tool, row.SessionID)
			fmt.Printf("title: %s\n", dash(row.Title))
			fmt.Printf("cwd: %s\n", dash(row.CWD))
			currentTool, currentSession = row.Tool, row.SessionID
		}
		fmt.Printf("\n[%s] %s\n", row.Timestamp, row.Role)
		fmt.Println(row.Text)
	}
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
