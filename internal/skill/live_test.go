//go:build live

package skill

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/llm"
	"github.com/BayInl/session-finder/internal/record"
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

func TestLiveSegmenterSplitsMixedSession(t *testing.T) {
	loadRepoEnv(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if llm.IsOffline(client) {
		t.Fatal("live relay client is offline; check .env")
	}
	messages := []record.MessageRecord{
		skillMessage("user", "Flatten escaped newlines in match previews and highlight hits."),
		skillMessage("assistant", "I will add Preview and background highlight."),
		skillMessage("user", "Looks good."),
		skillMessage("user", "Also highlight the boolean query terms in that same match pane."),
		skillMessage("assistant", "Positive terms get a background."),
		skillMessage("user", "Approved, go test passed."),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	slices := SplitTranscript(ctx, messages, NewLLMSegmenter(client))
	if len(slices) != 1 {
		t.Fatalf("same-product follow-ups should stay one thread, got %d slices", len(slices))
	}
	other := []record.MessageRecord{
		skillMessage("user", "Find the local opencode session about tavily_mcp."),
		skillMessage("assistant", "I searched the index."),
		skillMessage("user", "Looks good."),
		skillMessage("user", "Unrelated: write a task list from the civil-engineering meeting notes on the desktop."),
		skillMessage("assistant", "Drafting the checklist."),
	}
	split := SplitTranscript(ctx, other, NewLLMSegmenter(client))
	if len(split) < 2 {
		t.Fatalf("clearly different goals should split, got %d", len(split))
	}
}

func TestLiveSegmentRealIndexedSessions(t *testing.T) {
	loadRepoEnv(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if llm.IsOffline(client) {
		t.Fatal("live relay client is offline; check .env")
	}
	db, err := index.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	targets := [][2]string{
		{"kimi-code", "session_ad6f2fc9-1781-44dd-9ffc-70f88ecb58da"},
		{"kimi-code", "session_e623c2ba"},
		{"kimi-code", "session_563ab8a1"},
		{"grok", "019f6604-0efa-75f2-a419-39e6b48690f9"},
		{"kimi-code", "session_abccbdbd"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	segmenter := NewLLMSegmenter(client)
	ran := 0
	for _, target := range targets {
		tool, sessionID := target[0], target[1]
		messages, err := IndexToolSessionMessages(ctx, db, tool, sessionID, "", "")
		if err != nil || len(messages) == 0 {
			t.Logf("skip %s/%s: %v n=%d", tool, sessionID, err, len(messages))
			continue
		}
		clean := cleanLiveMessages(messages)
		turns := userTurns(clean)
		if len(turns) < 2 {
			t.Logf("skip %s/%s: %d user turns", tool, sessionID, len(turns))
			continue
		}
		result, err := segmenter.Segment(ctx, clean)
		if err != nil {
			t.Errorf("%s/%s segment: %v", tool, sessionID, err)
			continue
		}
		ran++
		slices := applySegmentTurns(clean, result.Turns)
		t.Logf("=== %s/%s users=%d slices=%d ===", tool, sessionID, len(turns), len(slices))
		decision := map[int]string{}
		for _, turn := range result.Turns {
			decision[turn.Index] = turn.Decision
		}
		for i, turn := range turns {
			text := strings.Join(strings.Fields(turn.text), " ")
			if len([]rune(text)) > 100 {
				text = string([]rune(text)[:100]) + "…"
			}
			label := decision[i]
			if label == "" {
				label = "?"
			}
			t.Logf("  [%d]=%-7s %s", i, label, text)
		}
		if len(slices) == 0 {
			t.Errorf("%s/%s: no slices", tool, sessionID)
		}
	}
	if ran == 0 {
		t.Skip("no targeted sessions in local index")
	}
}

func cleanLiveMessages(messages []record.MessageRecord) []record.MessageRecord {
	clean := make([]record.MessageRecord, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" || strings.EqualFold(message.Role, "system") || isInjectedNoiseText(message.Text) {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		message.Role = role
		clean = append(clean, message)
	}
	return clean
}
