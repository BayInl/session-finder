package main

import (
	"flag"
	"io"
	"testing"
)

func TestPythonStyleRepr(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "alpha", want: "'alpha'"},
		{name: "single quote chooses double", value: "a'b", want: `"a'b"`},
		{name: "double quote", value: `a"b`, want: `'a"b'`},
		{name: "both quotes", value: `a'b"c`, want: `'a\'b"c'`},
		{name: "backslash", value: `a\b`, want: `'a\\b'`},
		{name: "newlines", value: "a\nb\tc\rd", want: `'a\nb\tc\rd'`},
		{name: "control", value: string([]byte{0x01, 0x1f, 0x7f}), want: `'\x01\x1f\x7f'`},
		{name: "unicode", value: "é🙂", want: "'é🙂'"},
		{name: "unicode escape", value: "\u2028", want: `'\u2028'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pythonStyleRepr(test.value); got != test.want {
				t.Fatalf("pythonStyleRepr(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestParseFlagsAndArgAcceptsFlagsAfterPositional(t *testing.T) {
	set := flag.NewFlagSet("search", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	tool := set.String("tool", "", "")
	dbPath := set.String("db", "", "")
	asJSON := set.Bool("json", false, "")

	arg, helpRequested, err := parseFlagsAndArg(set, []string{"alpha", "--json", "--tool", "codex", "--db", "/tmp/index.db"}, "search")
	if err != nil {
		t.Fatal(err)
	}
	if helpRequested || arg != "alpha" || !*asJSON || *tool != "codex" || *dbPath != "/tmp/index.db" {
		t.Fatalf("arg=%q help=%v json=%v tool=%q db=%q", arg, helpRequested, *asJSON, *tool, *dbPath)
	}
}

func TestParseFlagsAndArgReportsHelp(t *testing.T) {
	set := flag.NewFlagSet("show", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.String("role", "", "")

	arg, helpRequested, err := parseFlagsAndArg(set, []string{"--help"}, "show")
	if err != nil {
		t.Fatal(err)
	}
	if !helpRequested || arg != "" {
		t.Fatalf("arg=%q help=%v, want empty arg and help=true", arg, helpRequested)
	}
}

func TestRunShowRejectsExplicitNonPositiveLimit(t *testing.T) {
	for _, args := range [][]string{
		{"session", "--limit", "0"},
		{"--limit=0", "session"},
		{"session", "--limit", "-1"},
	} {
		if err := runShow(args); err == nil || err.Error() != "--limit must be positive\n(use --help for usage)" {
			t.Errorf("runShow(%q) error = %v", args, err)
		}
	}
}
