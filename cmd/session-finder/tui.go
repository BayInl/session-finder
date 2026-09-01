package main

import (
	"flag"
	"os"

	"github.com/BayInl/session-finder/internal/brand"
	"github.com/BayInl/session-finder/internal/tui"
	"github.com/BayInl/session-finder/internal/ui"
)

func launchTUI(cfg tui.Config) error {
	if !tui.ShouldLaunch(os.Stdout) {
		return usageError("tui requires a TTY")
	}
	return tui.Run(cfg)
}

func runTUI(argv []string) error {
	cfg, helpRequested, err := parseTUIArgs(argv)
	if err != nil {
		return err
	}
	if helpRequested {
		return nil
	}
	return launchTUI(cfg)
}

func parseTUIArgs(argv []string) (tui.Config, bool, error) {
	set := flag.NewFlagSet("tui", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	tool := set.String("tool", "", "restrict to one tool")
	cwd := set.String("cwd", "", "restrict to sessions whose cwd contains this text")
	after := set.String("after", "", "only sessions on or after YYYY-MM-DD")
	limit := set.Int("limit", 100, "maximum sessions to show")
	dbPath := set.String("db", "", "path to the SQLite index database")
	includeSystem := set.Bool("all", false, "include system/noise records")
	ui.AttachUsage(set, brand.Usage("tui [query] [flags]"), "Browse indexed sessions in a full-screen TUI.", []ui.FlagGroup{
		{Title: "Filter", Names: []string{"tool", "cwd", "after", "all", "limit"}},
		{Title: "Database", Names: []string{"db"}},
	})
	query, helpRequested, err := parseOptionalArg(set, argv, "tui")
	if err != nil {
		return tui.Config{}, false, err
	}
	if helpRequested {
		return tui.Config{}, true, nil
	}
	if *limit <= 0 {
		return tui.Config{}, false, usageError("limit must be positive")
	}
	return tui.Config{
		Query: query, Tool: *tool, CWD: *cwd, After: *after,
		Limit: *limit, DBPath: *dbPath, IncludeSystem: *includeSystem,
	}, false, nil
}

func wantsTUI(asJSON, asPlain bool) bool {
	return !asJSON && !asPlain && tui.ShouldLaunch(os.Stdout)
}
