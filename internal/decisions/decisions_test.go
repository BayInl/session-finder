package decisions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
)

func msg(session, role, text string) record.MessageRecord {
	return record.MessageRecord{SessionID: session, Tool: "codex", CWD: "/tmp/project", SourcePath: "/tmp/session.jsonl", Role: role, Text: text}
}

func TestScanDecisionPatternsAndExplicitConfirmation(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "user", "Choose SQLite over Postgres because it keeps the MVP local."),
		msg("s1", "assistant", "I recommend SQLite."),
		msg("s1", "user", "Looks good, approved."),
	}
	candidates := Scan(messages)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	decision := candidates[0].Decision
	if decision.Chosen == "" || len(decision.Options) < 2 {
		t.Fatalf("decision choices = %#v", decision)
	}
	if decision.Rationale == "" || decision.Outcome != OutcomeUnknown {
		t.Fatalf("decision rationale/outcome = %q/%q", decision.Rationale, decision.Outcome)
	}
	if !hasEvidenceQuote(decision.Evidence, "Looks good, approved.", EvidenceExplicit) {
		t.Fatalf("missing explicit evidence: %#v", decision.Evidence)
	}
}

func TestScanInsteadOfAndImplementationEvidence(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "user", "Use SQLite instead of Postgres because tests are simpler."),
		msg("s1", "assistant", "Implemented the SQLite storage and ran go test ./... passed."),
	}
	candidates := Scan(messages)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	decision := candidates[0].Decision
	if decision.Outcome != OutcomeImplemented || !decision.HasImplementationEvidence() {
		t.Fatalf("implementation outcome/evidence = %q/%#v", decision.Outcome, decision.Evidence)
	}
	if err := decision.ValidateWithMessages(messages); err != nil {
		t.Fatalf("ValidateWithMessages() = %v", err)
	}
}

func TestScanAvoidsQuestionOnlyFalsePositive(t *testing.T) {
	messages := []record.MessageRecord{msg("s1", "user", "Which option should we choose?")}
	if candidates := Scan(messages); len(candidates) != 0 {
		t.Fatalf("question-only candidates = %#v", candidates)
	}
}

func TestScanFiltersUsageNoiseAndMarkdownCode(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "assistant", "使用 SQLite 来做本地缓存。"),
		msg("s1", "assistant", "```go\nuse SQLite\n```"),
		msg("s1", "assistant", "[Use SQLite](https://example.com)"),
		msg("s1", "assistant", "Use SQLite because it is local."),
	}
	candidates := Scan(messages)
	if len(candidates) != 1 {
		t.Fatalf("usage/markdown candidates = %#v", candidates)
	}
	if candidates[0].Rationale == "" {
		t.Fatalf("candidate lost rationale: %#v", candidates[0])
	}
	if candidates[0].Confidence < 0.62 {
		t.Fatalf("candidate below default admission threshold: %#v", candidates[0])
	}
}

func TestScanRequiresSemanticRelationForImplementation(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "user", "Choose SQLite because it is local."),
		msg("s1", "assistant", "Implemented an unrelated dashboard component."),
	}
	candidates := Scan(messages)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if candidates[0].Outcome != OutcomeUnknown {
		t.Fatalf("unrelated implementation changed outcome: %#v", candidates[0])
	}
}

func TestValidateEvidenceRequiresExactQuoteAndUserExplicit(t *testing.T) {
	messages := []record.MessageRecord{msg("s1", "assistant", "Looks good, approved."), msg("s1", "user", "Looks good, approved!")}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "Looks good, approved."}}); err != nil {
		t.Fatalf("assistant exact quote should match as transcript: %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceExplicit, Quote: "Looks good, approved"}}); err != nil {
		t.Fatalf("user exact substring should match: %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceExplicit, Quote: "Looks good, approve"}}); err != nil {
		t.Fatalf("user substring should match: %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceExplicit, Quote: "Looks good, approved.", MessageIndex: 1}}); !errors.Is(err, ErrExplicitEvidenceRole) {
		t.Fatalf("assistant-pinned explicit error = %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "Looks good, approved"}}); err != nil {
		t.Fatalf("exact transcript substring should match: %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "Looks good, approved" + "?"}}); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("missing quote error = %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "short"}}); !errors.Is(err, ErrEvidenceTooShort) {
		t.Fatalf("short quote error = %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "1234567"}}); !errors.Is(err, ErrEvidenceTooShort) {
		t.Fatalf("seven-rune quote error = %v", err)
	}
	if err := ValidateEvidence(messages, []Evidence{{Kind: EvidenceTranscript, Quote: "approved"}}); err != nil {
		t.Fatalf("eight-rune quote should match: %v", err)
	}
	if ExactQuoteMatch("12345678 in a message", "1234567") {
		t.Fatal("short quote should not match")
	}
	if !ExactQuoteMatch("12345678 in a message", "12345678") {
		t.Fatal("eight-rune quote should match")
	}
}

func TestDecisionSchemaRoundTripAndOutcomeSafety(t *testing.T) {
	decision := Decision{Context: "Use local storage", Options: []string{"SQLite", "Postgres"}, Chosen: "SQLite", Evidence: []Evidence{{Kind: EvidenceTranscript, Quote: "Use SQLite"}}, Provenance: Provenance{SessionID: "s1"}, Confidence: 0.7, Outcome: OutcomeImplemented}
	normalized := decision.Normalize()
	if normalized.Outcome != OutcomeUnknown {
		t.Fatalf("unsafe outcome = %q", normalized.Outcome)
	}
	data, err := MarshalDecision(normalized)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDecision(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Chosen != decision.Chosen || decoded.Kind != KindDecision {
		t.Fatalf("decoded = %#v", decoded)
	}
	if !strings.Contains(string(Schema()), "supersedes") {
		t.Fatal("schema omitted supersedes")
	}
}

func TestStoreConfirmationReviewAndAppendOnlyEvents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	messages := []record.MessageRecord{msg("s1", "user", "Use SQLite because it is local.")}
	candidate := Scan(messages)[0]
	if _, err := store.Create(context.Background(), CreateInput{Decision: candidate.Decision, Messages: messages}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("unconfirmed create error = %v", err)
	}
	created, err := store.Confirm(context.Background(), candidate, messages, "user", "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.Approve(context.Background(), created.ID, "reviewer", "accept")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusAccepted {
		t.Fatalf("approved status = %q", approved.Status)
	}
	events, err := store.Events(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Action != extract.ActionCreate || events[1].Action != extract.ActionTransition || events[2].Action != extract.ActionTransition || events[3].Action != extract.ActionTransition {
		t.Fatalf("events = %#v", events)
	}
	listed, err := store.List(context.Background(), ListOptions{Status: StatusAccepted})
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %#v err=%v", listed, err)
	}
}

func TestStoreReviewEditHydratesPersistedSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	messages := []record.MessageRecord{
		msg("s1", "user", "Choose SQLite over Postgres because it keeps the MVP local."),
	}
	candidate := Scan(messages)[0]
	created, err := store.Confirm(context.Background(), candidate, messages, "user", "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	// Add the source/session rows to the same database, matching the index
	// schema used by the CLI. Review must reload them when Messages is omitted.
	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.InitializeSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(tool, session_id, cwd, source_path) VALUES ('codex', 's1', '/tmp/project', '/tmp/session.jsonl')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages(session_pk, role, ts, text) VALUES (last_insert_rowid(), 'user', 1, 'Choose SQLite over Postgres because it keeps the MVP local.')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	edited := created
	edited.Context = "Choose SQLite for the local MVP"
	edited.Evidence = []Evidence{{Kind: EvidenceTranscript, Quote: "Choose SQLite over Postgres because it keeps the MVP local."}}
	edited.Provenance = created.Provenance
	replacement, err := store.Review(context.Background(), ReviewInput{ID: created.ID, Action: ReviewEdit, Decision: &edited, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Context != edited.Context || replacement.Supersedes != created.ID {
		t.Fatalf("replacement = %#v", replacement)
	}
}

func TestConfidenceBandsReflectEvidence(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "user", "使用 SQLite 方案。"),
		msg("s2", "user", "Choose SQLite over Postgres because it keeps the MVP local."),
		msg("s2", "user", "Looks good, approved."),
	}
	candidates := Extract(messages, ExtractOptions{MinConfidence: 0.4})
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Confidence == candidates[1].Confidence {
		t.Fatalf("confidence lacks separation: %#v", candidates)
	}
	if candidates[1].Confidence <= candidates[0].Confidence {
		t.Fatalf("stronger evidence did not score higher: %#v", candidates)
	}
	defaultCandidates := Extract(messages, ExtractOptions{})
	if len(defaultCandidates) != 1 || defaultCandidates[0].Provenance.SessionID != "s2" {
		t.Fatalf("default admission threshold did not filter the weaker candidate: %#v", defaultCandidates)
	}
}

func TestGitSelectionRejectsFabricatedHashes(t *testing.T) {
	candidates := []CommitRef{{Hash: "abc123", ShortHash: "abc123", Subject: "decision"}}
	if _, err := SelectCommitCandidates(candidates, []string{"deadbeef"}); !errors.Is(err, ErrCommitNotCandidate) {
		t.Fatalf("fabricated hash error = %v", err)
	}
	selected, err := SelectCommitCandidates(candidates, []string{"abc123"})
	if err != nil || len(selected) != 1 {
		t.Fatalf("selected = %#v err=%v", selected, err)
	}
}
