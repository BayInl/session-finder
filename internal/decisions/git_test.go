package decisions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeGitRunner struct {
	output []byte
	err    error
	calls  [][]string
	dirs   []string
}

func (runner *fakeGitRunner) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	runner.dirs = append(runner.dirs, dir)
	runner.calls = append(runner.calls, append([]string(nil), args...))
	return runner.output, runner.err
}

func TestFindCommitsUsesSingleLogAndParsesPaths(t *testing.T) {
	runner := &fakeGitRunner{output: []byte("\x1efull-one\x1fshort1\x1fFirst subject\x1fAlice\x1f2026-08-31T10:00:00Z\nsrc/a.go\nsrc/a.go\nREADME.md\n\x1efull-two\x1fshort2\x1fSecond subject\x1fBob\x1f2026-08-30T10:00:00Z\ninternal/b.go\n")}
	commits, err := FindCommits(context.Background(), runner, GitQuery{CWD: "/repo", Paths: []string{"src"}, MaxCommits: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) == 0 || runner.calls[0][0] != "log" {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
	joined := strings.Join(runner.calls[0], " ")
	for _, expected := range []string{"--name-only", "--no-renames", "--format=%x1e", "--max-count=1", "-- src"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("git args %q lack %q", joined, expected)
		}
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %#v", commits)
	}
	if commits[0].Hash != "full-one" || !reflect.DeepEqual(commits[0].Paths, []string{"README.md", "src/a.go"}) {
		t.Fatalf("commit = %#v", commits[0])
	}
}

func TestFindCommitsFiltersPathOverlapAfterParsing(t *testing.T) {
	runner := &fakeGitRunner{output: []byte("\x1eone\x1fone\x1fOne\x1fAlice\x1f2026-08-31T10:00:00Z\ndocs/readme.md\n\x1etwo\x1ftwo\x1fTwo\x1fBob\x1f2026-08-30T10:00:00Z\ninternal/decisions/store.go\n")}
	commits, err := FindCommits(context.Background(), runner, GitQuery{Paths: []string{"internal/decisions"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Hash != "two" {
		t.Fatalf("commits = %#v", commits)
	}
}

func TestFindCommitsWrapsRunnerError(t *testing.T) {
	runner := &fakeGitRunner{err: errors.New("git failed")}
	if _, err := FindCommits(context.Background(), runner, GitQuery{}); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
}
