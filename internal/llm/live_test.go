//go:build live

package llm

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadRepoEnv(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var envPath string
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			envPath = candidate
			break
		}
		dir = filepath.Dir(dir)
	}
	if envPath == "" {
		t.Skip("no .env in repo")
	}
	file, err := os.Open(envPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if os.Getenv(key) == "" {
			t.Setenv(key, value)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRelaySegmentsUserTurns(t *testing.T) {
	loadRepoEnv(t)
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if IsOffline(client) {
		t.Fatal("live relay client is offline; check .env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := client.Complete(ctx, CompletionRequest{
		Transcript: []Message{
			{Role: "user", Content: "0: Flatten escaped newlines in TUI match previews."},
			{Role: "user", Content: "1: Looks good, keep going."},
			{Role: "user", Content: "2: Unrelated: write a Homebrew formula for a CLI named sfind."},
		},
		Prompt: "Classify each 0-based user turn as new, same, or confirm. Return JSON {turns:[{index,decision}]}.",
		Schema: SegmentSchema(),
	})
	if err != nil {
		t.Fatalf("live Complete: %v", err)
	}
	got, err := DecodeSegments(response.JSON)
	if err != nil {
		t.Fatalf("decode %s: %v", response.JSON, err)
	}
	if len(got.Turns) < 2 {
		t.Fatalf("turns = %#v json=%s", got.Turns, response.JSON)
	}
	decisions := map[int]string{}
	for _, turn := range got.Turns {
		decisions[turn.Index] = turn.Decision
	}
	if decisions[0] != SegmentDecisionNew {
		t.Fatalf("turn 0 = %q want new; %#v", decisions[0], got.Turns)
	}
	if decisions[2] != "" && decisions[2] != SegmentDecisionNew {
		t.Fatalf("turn 2 should start a new task, got %q; %#v", decisions[2], got.Turns)
	}
}
