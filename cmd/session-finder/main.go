package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BayInl/session-finder/internal/brand"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
	"github.com/BayInl/session-finder/internal/tui"
	"github.com/BayInl/session-finder/internal/ui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		ui.PrintError(os.Stderr, err)
		os.Exit(2)
	}
}

func run(argv []string) error {
	if len(argv) == 0 {
		if tui.ShouldLaunch(os.Stdout) {
			return launchTUI(tui.Config{})
		}
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
	ui.PrintVersion(writer, version, commit, date)
}

func printRootUsage() {
	ui.PrintRootUsage(os.Stdout, Commands(), "Search local AI sessions from opencode, Grok, Codex, Kimi Code, and Claude.")
}

func usageError(message string) error {
	return fmt.Errorf("%s\n(use --help for usage)", message)
}

func runIndex(argv []string) error {
	set := flag.NewFlagSet("index", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	full := set.Bool("full", false, "rebuild the index from scratch")
	dbPath := set.String("db", "", "path to the SQLite index database")
	ui.AttachUsage(set, brand.Usage("index [flags]"), "Build or incrementally update the local session index.", []ui.FlagGroup{
		{Title: "Index", Names: []string{"full"}},
		{Title: "Database", Names: []string{"db"}},
	})
	if err := ui.Parse(set, argv); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return usageError("index accepts no positional arguments")
	}
	prog := ui.NewIndexProgress(os.Stderr)
	defer prog.Close()
	summary, err := index.IndexAllWithProgress(*full, *dbPath, prog.Report)
	if err != nil {
		return err
	}
	counts := make(map[string][2]int, len(summary.Tools))
	for tool, stats := range summary.Tools {
		counts[tool] = [2]int{stats.Sessions, stats.Messages}
	}
	ui.RenderIndexSummary(os.Stdout, summary.DBPath, record.Tools, counts, summary.Sources.Processed, summary.Sources.Unchanged, summary.Sources.Errors)
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
	asPlain := set.Bool("plain", false, "print results instead of opening the TUI")
	includeSystem := set.Bool("all", false, "include system/noise records")
	dbPath := set.String("db", "", "path to the SQLite index database")
	ui.AttachUsage(set, brand.Usage("search <query> [flags]"), "Search indexed session transcripts.", []ui.FlagGroup{
		{Title: "Filter", Names: []string{"tool", "cwd", "after", "all", "limit"}},
		{Title: "Output", Names: []string{"json", "plain"}},
		{Title: "Database", Names: []string{"db"}},
	})
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
	if wantsTUI(*asJSON, *asPlain) {
		return launchTUI(tui.Config{
			Query: query, Tool: *tool, CWD: *cwd, After: *after,
			Limit: *limit, DBPath: *dbPath, IncludeSystem: *includeSystem,
		})
	}
	db, err := index.Open(*dbPath)
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
		return ui.WriteJSON(os.Stdout, payload)
	}
	ui.RenderSearch(os.Stdout, query, tui.HitsFromResults(results))
	return nil
}

func runShow(argv []string) error {
	set := flag.NewFlagSet("show", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	role := set.String("role", "", "filter by role (user, assistant, or system)")
	limit := set.Int("limit", 0, "maximum messages to show")
	dbPath := set.String("db", "", "path to the SQLite index database")
	ui.AttachUsage(set, brand.Usage("show <session-id> [flags]"), "Show messages from a session.", []ui.FlagGroup{
		{Title: "Filter", Names: []string{"role", "limit"}},
		{Title: "Database", Names: []string{"db"}},
	})
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
	ui.RenderShow(os.Stdout, tui.MessagesFromRows(rows))
	return nil
}

func parseFlagsAndArg(set *flag.FlagSet, argv []string, command string) (string, bool, error) {
	return parseCommandArg(set, argv, command, true)
}

func parseOptionalArg(set *flag.FlagSet, argv []string, command string) (string, bool, error) {
	return parseCommandArg(set, argv, command, false)
}

func parseCommandArg(set *flag.FlagSet, argv []string, command string, required bool) (string, bool, error) {
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
		return "", false, ui.Printed(err)
	}
	if required && len(positionals) != 1 {
		return "", false, usageError(command + " requires exactly one positional argument")
	}
	if !required && len(positionals) > 1 {
		return "", false, usageError(command + " accepts at most one query")
	}
	if len(positionals) == 1 {
		return positionals[0], false, nil
	}
	return "", false, nil
}
