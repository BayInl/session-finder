package ui

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/BayInl/session-finder/internal/brand"
)

// AttachUsage replaces the FlagSet Usage with grouped --flag help.
func AttachUsage(set *flag.FlagSet, usageLine, blurb string, groups []FlagGroup) {
	set.Usage = func() {
		PrintUsage(set.Output(), usageLine, blurb, set, groups)
	}
}

// PrintUsage writes grouped flag help. Ungrouped flags go under "Flags:".
func PrintUsage(w io.Writer, usageLine, blurb string, set *flag.FlagSet, groups []FlagGroup) {
	theme := NewTheme(w)
	fmt.Fprintln(w, usageLine)
	if blurb != "" {
		fmt.Fprintln(w, blurb)
	}
	if set == nil {
		return
	}
	byName := map[string]*flag.Flag{}
	set.VisitAll(func(f *flag.Flag) {
		byName[f.Name] = f
	})
	seen := map[string]bool{}
	printGroup := func(title string, names []string) {
		var items []*flag.Flag
		for _, name := range names {
			f, ok := byName[name]
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			items = append(items, f)
		}
		if len(items) == 0 {
			return
		}
		fmt.Fprintln(w)
		heading := title + ":"
		if ColorEnabled(w) {
			heading = theme.Style(TokenPrimary).Render(heading)
		}
		fmt.Fprintln(w, heading)
		writeFlags(w, items)
	}
	for _, group := range groups {
		printGroup(group.Title, group.Names)
	}
	var leftover []*flag.Flag
	set.VisitAll(func(f *flag.Flag) {
		if !seen[f.Name] {
			leftover = append(leftover, f)
		}
	})
	if len(leftover) > 0 {
		printGroup("Flags", flagNames(leftover))
	}
}

func flagNames(flags []*flag.Flag) []string {
	names := make([]string, len(flags))
	for i, f := range flags {
		names[i] = f.Name
	}
	return names
}

func writeFlags(w io.Writer, flags []*flag.Flag) {
	type row struct{ left, right string }
	rows := make([]row, 0, len(flags))
	leftWidth := 0
	for _, f := range flags {
		left := flagLeft(f)
		right := f.Usage
		if f.DefValue != "" && f.DefValue != "false" {
			if right == "" {
				right = "(default " + f.DefValue + ")"
			} else {
				right += " (default " + f.DefValue + ")"
			}
		}
		if DisplayWidth(left) > leftWidth {
			leftWidth = DisplayWidth(left)
		}
		rows = append(rows, row{left: left, right: right})
	}
	for _, r := range rows {
		pad := leftWidth - DisplayWidth(r.left)
		if pad < 0 {
			pad = 0
		}
		if r.right == "" {
			fmt.Fprintf(w, "  %s\n", r.left)
			continue
		}
		fmt.Fprintf(w, "  %s%s  %s\n", r.left, strings.Repeat(" ", pad), r.right)
	}
}

func flagLeft(f *flag.Flag) string {
	typeName := flagTypeName(f)
	if typeName == "" {
		return "--" + f.Name
	}
	return "--" + f.Name + " " + typeName
}

func flagTypeName(f *flag.Flag) string {
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return ""
	}
	name := fmt.Sprintf("%T", f.Value)
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "int"):
		return "int"
	case strings.Contains(lower, "float"):
		return "float"
	default:
		return "string"
	}
}

// PrintRootUsage writes the root command list. [flags] appears once.
func PrintRootUsage(w io.Writer, commands []string, blurb string) {
	joined := strings.Join(commands, "|")
	if ColorEnabled(w) {
		theme := NewTheme(w)
		parts := make([]string, len(commands))
		for i, command := range commands {
			parts[i] = theme.Style(TokenPrimary).Render(command)
		}
		joined = strings.Join(parts, "|")
	}
	fmt.Fprintf(w, "usage: %s <%s> [flags]\n", brand.Name, joined)
	if blurb != "" {
		fmt.Fprintln(w, blurb)
	}
}
