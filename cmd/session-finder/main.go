package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
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
	switch argv[0] {
	case "index":
		return runIndex(argv[1:])
	case "search":
		return runSearch(argv[1:])
	case "show":
		return runShow(argv[1:])
	case "-h", "--help":
		printRootUsage()
		return nil
	default:
		return usageError("unknown command: " + argv[0])
	}
}

func printRootUsage() {
	fmt.Println("usage: session-finder <index|search|show> [flags]")
	fmt.Println("Search local AI sessions from opencode, Grok, Codex, Kimi Code, and Claude.")
}

func usageError(message string) error {
	return fmt.Errorf("%s\n(use --help for usage)", message)
}

func runIndex(argv []string) error {
	set := flag.NewFlagSet("index", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	full := set.Bool("full", false, "rebuild the index from scratch")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 0 {
		return usageError("index accepts no positional arguments")
	}
	summary, err := index.IndexAll(*full, "")
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
	includeSystem := set.Bool("all", false, "include system/noise records (default hides them)")
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
	db, err := index.Open("")
	if err != nil {
		return err
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		return err
	}
	results, err := index.Search(db, query, *tool, *cwd, *after, *limit, *includeSystem)
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
	printSearchResults(query, results)
	return nil
}

func runShow(argv []string) error {
	set := flag.NewFlagSet("show", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	role := set.String("role", "", "filter by role (user, assistant, or system)")
	limit := set.Int("limit", 0, "maximum messages to show")
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
	db, err := index.Open("")
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
			if !strings.Contains(arg, "=") && (name == "tool" || name == "cwd" || name == "after" || name == "limit" || name == "role") && i+1 < len(argv) {
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

func printSearchResults(query string, results []index.SearchResult) {
	fmt.Printf("search: %s (%d sessions)\n", pythonStyleRepr(query), len(results))
	if len(results) == 0 {
		fmt.Println("No matches.")
		return
	}
	for number, result := range results {
		fmt.Printf("%d. [%s] %s\n", number+1, result.Tool, result.SessionID)
		fmt.Printf("   title: %s\n", dash(result.Title))
		fmt.Printf("   cwd: %s\n", dash(result.CWD))
		fmt.Printf("   time: %s .. %s\n", result.Created, result.Updated)
		fmt.Printf("   messages: %d\n", result.MessageCount)
		for _, path := range result.SourcePaths {
			fmt.Printf("   path: %s\n", path)
		}
		for _, snippet := range result.Snippets {
			fmt.Printf("   snippet: %s\n", snippet)
		}
	}
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
