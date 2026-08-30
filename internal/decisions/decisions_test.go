package decisions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/extract"
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
