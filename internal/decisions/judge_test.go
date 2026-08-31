package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
)

func TestApplyCandidateJudgeHonorsLimitAndAdmission(t *testing.T) {
	calls := 0
	judge := CandidateJudgeFunc(func(_ context.Context, _ CandidateReview) (CandidateJudgment, error) {
		calls++
		return CandidateJudgment{Disposition: "draft", Confidence: 0.8}, nil
	})
	candidates := []DecisionCandidate{
		{Decision: Decision{Chosen: "SQLite", Rationale: "local", Confidence: 0.8, Provenance: Provenance{SessionID: "s1", MessageStart: 1, MessageEnd: 1}}},
		{Decision: Decision{Chosen: "Redis", Rationale: "fast", Confidence: 0.5, Provenance: Provenance{SessionID: "s1", MessageStart: 2, MessageEnd: 2}}},
	}
	got := applyCandidateJudge(context.Background(), []record.MessageRecord{msg("s1", "user", "Choose SQLite because it is local.")}, candidates, ExtractOptions{
		Judge: judge, JudgeLimit: 1, MinConfidence: 0.62,
	})
	if calls != 1 {
		t.Fatalf("judge calls = %d, want one", calls)
	}
	if len(got) != 1 || got[0].Chosen != "SQLite" {
		t.Fatalf("candidates after limited judge = %#v, want only admitted candidate", got)
	}
}

func TestApplyJudgmentDoesNotRewriteCandidateFields(t *testing.T) {
	candidate := DecisionCandidate{Decision: Decision{
		Kind: KindDecision, Status: StatusProposed, Context: "Choose SQLite over Postgres",
		Options: []string{"SQLite", "Postgres"}, Chosen: "SQLite", Rationale: "local",
		Evidence: []Evidence{{Kind: EvidenceTranscript, Quote: "Choose SQLite over Postgres because it is local.", MessageIndex: 1}},
		Outcome:  OutcomeUnknown, Commits: []CommitRef{{Hash: "abc", Subject: "decision"}}, Confidence: 0.7,
		Provenance: Provenance{SessionID: "s1", MessageStart: 1, MessageEnd: 1},
	}, Reasons: []string{"because-rationale"}}
	got := applyJudgment(candidate, CandidateJudgment{Disposition: "draft", Confidence: 0.95, ReasonCodes: []string{"same-candidate"}})
	if got.Context != candidate.Context || got.Chosen != candidate.Chosen || got.Rationale != candidate.Rationale || got.Outcome != candidate.Outcome {
		t.Fatalf("judge rewrote durable fields: before=%#v after=%#v", candidate.Decision, got.Decision)
	}
	if len(got.Options) != len(candidate.Options) || got.Options[0] != candidate.Options[0] || got.Options[1] != candidate.Options[1] {
		t.Fatalf("judge rewrote options: %#v", got.Options)
	}
	if !reflect.DeepEqual(got.Evidence, candidate.Evidence) || !reflect.DeepEqual(got.Commits, candidate.Commits) {
		t.Fatalf("judge rewrote evidence/commits: %#v %#v", got.Evidence, got.Commits)
	}
	if got.Confidence != 0.95 || !containsString(got.Reasons, "llm:draft") || !containsString(got.Reasons, "llm:reason:same-candidate") {
		t.Fatalf("judge result = %#v", got)
	}
}

func TestApplyCandidateJudgeFallsBackOnJudgeError(t *testing.T) {
	candidate := DecisionCandidate{Decision: Decision{
		Chosen: "SQLite", Rationale: "local", Confidence: 0.7,
		Provenance: Provenance{SessionID: "s1", MessageStart: 1, MessageEnd: 1},
	}}
	got := applyCandidateJudge(context.Background(), nil, []DecisionCandidate{candidate}, ExtractOptions{
		Judge: CandidateJudgeFunc(func(context.Context, CandidateReview) (CandidateJudgment, error) {
			return CandidateJudgment{}, errors.New("provider unavailable")
		}), MinConfidence: 0.62,
	})
	if len(got) != 1 || !containsString(got[0].Reasons, "llm:fallback") || got[0].Chosen != "SQLite" {
		t.Fatalf("fallback result = %#v", got)
	}
}

func TestDecisionCandidateReviewNeverCrossesSessionBoundary(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s2", "user", "Choose unrelated option because other context."),
		msg("s1", "user", "Choose SQLite over Postgres because it is local."),
		msg("s1", "assistant", "SQLite implementation is ready."),
		msg("s2", "assistant", "Another session implementation is ready."),
	}
	for i := range messages {
		messages[i].SourcePath = "/tmp/" + messages[i].SessionID + ".jsonl"
	}
	candidate := DecisionCandidate{Decision: Decision{Chosen: "SQLite", Rationale: "local", Provenance: Provenance{
		SessionID: "s1", Tool: "codex", SourcePath: "/tmp/s1.jsonl", MessageStart: 2, MessageEnd: 2,
	}}}
	review := decisionCandidateReview(messages, candidate)
	if len(review.Messages) != 2 || review.Messages[0].Index != 2 || review.Messages[1].Index != 3 {
		t.Fatalf("review window = %#v, want only s1 messages", review.Messages)
	}
	for _, message := range review.Messages {
		if strings.Contains(message.Content, "unrelated") || strings.Contains(message.Content, "Another session") {
			t.Fatalf("cross-session content leaked into review: %#v", review.Messages)
		}
	}
}

func TestExtractGroupedKeepsSourceLocalAnchors(t *testing.T) {
	left := msg("s1", "user", "Choose SQLite because the left source is local.")
	left.SourcePath = "/tmp/left.jsonl"
	right := msg("s1", "user", "Choose SQLite because the right source is portable.")
	right.SourcePath = "/tmp/right.jsonl"
	candidates, err := extractGrouped(context.Background(), []record.MessageRecord{left, right}, ExtractOptions{ResolvedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("grouped candidates = %#v, want two", candidates)
	}
	for _, candidate := range candidates {
		if candidate.Provenance.MessageStart != 1 {
			t.Fatalf("source-local anchor = %d, want 1: %#v", candidate.Provenance.MessageStart, candidate)
		}
	}
	if candidates[0].Provenance.SourcePath == candidates[1].Provenance.SourcePath {
		t.Fatalf("grouped source identity collapsed: %#v", candidates)
	}
}

func TestDecisionCandidateReviewNeverCrossesSourceBoundary(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "user", "Choose SQLite over Postgres because left source."),
		msg("s1", "assistant", "Choose Redis because right source."),
		msg("s1", "assistant", "SQLite implementation is ready."),
	}
	messages[0].SourcePath = "/tmp/left.jsonl"
	messages[1].SourcePath = "/tmp/right.jsonl"
	messages[2].SourcePath = "/tmp/left.jsonl"
	candidate := DecisionCandidate{Decision: Decision{Chosen: "SQLite", Rationale: "left source", Provenance: Provenance{
		SessionID: "s1", Tool: "codex", SourcePath: "/tmp/left.jsonl", MessageStart: 1, MessageEnd: 1,
	}}}
	review := decisionCandidateReview(messages, candidate)
	if len(review.Messages) != 2 || review.Messages[0].Index != 1 || review.Messages[1].Index != 3 {
		t.Fatalf("review window = %#v, want only left source messages", review.Messages)
	}
	for _, message := range review.Messages {
		if strings.Contains(message.Content, "right source") {
			t.Fatalf("cross-source content leaked into review: %#v", review.Messages)
		}
	}
}

func TestRunExtractJudgeOnRejectsOfflineConfiguration(t *testing.T) {
	for _, name := range []string{
		"SESSION_FINDER_LLM_PROVIDER", "LLM_PROVIDER", "SESSION_FINDER_LLM_BASE_URL", "OPENAI_BASE_URL", "LLM_BASE_URL",
		"SESSION_FINDER_LLM_API_KEY", "OPENAI_API_KEY", "LLM_API_KEY", "SESSION_FINDER_LLM_MODEL", "OPENAI_MODEL", "LLM_MODEL",
	} {
		t.Setenv(name, "")
	}
	err := runExtract(io.Discard, []string{"--db", filepath.Join(t.TempDir(), "index.db"), "--judge", "on", "--json"})
	if err == nil || !strings.Contains(err.Error(), "judge=on requires a configured online llm provider") {
		t.Fatalf("judge=on error = %v", err)
	}
}

func TestRunExtractSessionPrefixMatchesSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.InitializeSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, session := range []struct {
		id   string
		text string
	}{
		{id: "prefix-session-001", text: "Choose SQLite over Postgres because it keeps the MVP local."},
		{id: "other-session-001", text: "Choose Redis over Memcached because it is already deployed."},
	} {
		result, err := db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, source_path) VALUES (?, ?, ?, ?, ?)`,
			"codex", session.id, "/tmp/project", "", "/tmp/"+session.id+".jsonl")
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		sessionPK, err := result.LastInsertId()
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages(session_pk, role, ts, text) VALUES (?, ?, ?, ?)`, sessionPK, "user", 1, session.text); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runExtract(&output, []string{
		"--db", dbPath, "--session", "prefix-session", "--judge", "off", "--json",
	}); err != nil {
		t.Fatalf("runExtract() = %v", err)
	}
	var result struct {
		Count      int                 `json:"count"`
		Candidates []DecisionCandidate `json:"candidates"`
	}
	if err := json.NewDecoder(&output).Decode(&result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Count != 1 || len(result.Candidates) != 1 {
		t.Fatalf("prefix result = %#v, want one matching candidate", result)
	}
	if result.Candidates[0].Provenance.SessionID != "prefix-session-001" {
		t.Fatalf("matched session = %q, want prefix-session-001", result.Candidates[0].Provenance.SessionID)
	}
}
