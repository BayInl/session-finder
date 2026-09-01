package main

import (
	"errors"
	"flag"
	"os"
	"strings"

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
	var includeSystem bool
	set.BoolVar(&includeSystem, "all", false, "include system/noise records (default hides them)")
	set.BoolVar(&includeSystem, "include-system", false, "alias for --all")
	ui.AttachUsage(set, "usage: session-finder tui [query] [flags]", "Browse indexed sessions in a full-screen TUI.", []ui.FlagGroup{
		{Title: "Filter", Names: []string{"tool", "cwd", "after", "all", "include-system", "limit"}},
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
		Limit: *limit, DBPath: *dbPath, IncludeSystem: includeSystem,
	}, false, nil
}

func parseOptionalArg(set *flag.FlagSet, argv []string, command string) (string, bool, error) {
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
	if len(positionals) > 1 {
		return "", false, usageError(command + " accepts at most one query")
	}
	if len(positionals) == 1 {
		return positionals[0], false, nil
	}
	return "", false, nil
}

func wantsTUI(asJSON, asPlain bool) bool {
	return !asJSON && !asPlain && tui.ShouldLaunch(os.Stdout)
}
