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

func TestScanRequiresChosenAndRationaleForResolvedOutput(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "descriptive usage", text: "你可以考虑代码复用、模板使用来减少代码量。"},
		{name: "recommendation without rationale", text: "I recommend SQLite for the local cache."},
		{name: "tradeoff without rationale", text: "Choose SQLite over Postgres."},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if candidates := Scan([]record.MessageRecord{msg("s1", "user", testCase.text)}); len(candidates) != 0 {
				t.Fatalf("unresolved candidates = %#v", candidates)
			}
		})
	}
	resolved := Scan([]record.MessageRecord{msg("s1", "user", "Choose SQLite over Postgres because it is local.")})
	if len(resolved) != 1 || resolved[0].Chosen == "" || resolved[0].Rationale == "" {
		t.Fatalf("resolved candidates = %#v", resolved)
	}
}

func TestScanConfirmationChineseBoundary(t *testing.T) {
	base := msg("s1", "user", "Use SQLite because it is local.")
	for _, testCase := range []struct {
		name     string
		text     string
		explicit bool
	}{
		{name: "descriptive 可以", text: "可以考虑代码复用、模板使用来减少代码量。"},
		{name: "explicit 可以", text: "可以，就采用 SQLite。", explicit: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidates := Scan([]record.MessageRecord{base, msg("s1", "user", testCase.text)})
			if len(candidates) != 1 {
				t.Fatalf("candidates = %#v, want one", candidates)
			}
			hasExplicit := hasEvidenceQuote(candidates[0].Evidence, testCase.text, EvidenceExplicit)
			if hasExplicit != testCase.explicit {
				t.Fatalf("explicit evidence = %v, want %v: %#v", hasExplicit, testCase.explicit, candidates[0].Evidence)
			}
		})
	}
}

func TestScanKeepsTranscriptSourcesSeparate(t *testing.T) {
	left := msg("s1", "user", "Choose SQLite because it is local.")
	left.SourcePath = "/tmp/left.jsonl"
	right := msg("s1", "user", "Choose SQLite because it is portable.")
	right.SourcePath = "/tmp/right.jsonl"
	candidates := Scan([]record.MessageRecord{left, right})
	if len(candidates) != 2 {
		t.Fatalf("source-mixed candidates = %#v, want two", candidates)
	}
	if candidates[0].Provenance.SourcePath == candidates[1].Provenance.SourcePath {
		t.Fatalf("source identity collapsed: %#v", candidates)
	}
}

func TestScanFiltersKimiLoopEventNoise(t *testing.T) {
	messages := []record.MessageRecord{
		msg("s1", "assistant", `tool.call Bash {"command":"Choose SQLite because it is local."}`),
		msg("s1", "assistant", `tool.result call-1 {"output":"Choose SQLite because it is local."}`),
		msg("s1", "user", "Choose SQLite because it is local."),
	}
	if candidates := Scan(messages); len(candidates) != 1 {
		t.Fatalf("loop-event candidates = %#v, want only the user decision", candidates)
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

func TestScanRejectsUsageFragmentsAndDeliberation(t *testing.T) {
	cases := []string{
		"判断“有没有 use case”，我先用 text parser 快速拿一版章节骨架，因为需要先验证章节类型。",
		"The models already use google prefix because the provider names require it.",
		"不要把 /responses 再写进 base_url，因为 wire_api 会自动使用这个路径。",
		"I'm considering SQLite over Postgres because the adapter may need fewer changes.",
		"I might choose SQLite because it is local.",
		"I wonder whether to use SQLite because it is local.",
		"Let me try using PUT instead of PATCH, since the management panel probably uses PUT.",
		"We could use SQLite because it is local, but the requirement is still open.",
	}
	for _, text := range cases {
		if candidates := Scan([]record.MessageRecord{msg("s1", "assistant", text)}); len(candidates) != 0 {
			t.Errorf("noise candidate for %q = %#v", text, candidates)
		}
	}
}

func TestScanCutsChineseReasonWithoutLeadingSpace(t *testing.T) {
	candidate := Scan([]record.MessageRecord{msg("s1", "user", "使用 SQLite，因为它适合本地 MVP。")})
	if len(candidate) != 1 {
		t.Fatalf("candidates = %#v, want one", candidate)
	}
	if candidate[0].Chosen != "SQLite" || candidate[0].Rationale != "它适合本地 MVP" {
		t.Fatalf("choice/rationale = %q/%q", candidate[0].Chosen, candidate[0].Rationale)
	}
}

func TestScanDropsMessageLevelNotificationNoise(t *testing.T) {
	for _, text := range []string{
		"prefix <subagent_notification>{\"status\":\"done\"}</subagent_notification> Residual note",
		"prefix <subagent_notification>{\"status\":\"done\"}</subagent_notification> Choose SQLite because it is local.",
	} {
		if candidates := Scan([]record.MessageRecord{msg("s1", "assistant", text)}); len(candidates) != 0 {
			t.Fatalf("notification candidate for %q = %#v", text, candidates)
		}
	}
}

func TestScanKeepsExplicitResolvedChoices(t *testing.T) {
	cases := []struct {
		text   string
		chosen string
	}{
		{"Choose SQLite over Postgres because SQLite keeps the MVP local.", "SQLite"},
		{"Use SQLite instead of Postgres because tests are simpler.", "SQLite"},
		{"推荐使用 SQLite，因为它适合本地 MVP。", "SQLite"},
	}
	for _, testCase := range cases {
		candidates := Scan([]record.MessageRecord{msg("s1", "user", testCase.text)})
		if len(candidates) != 1 || candidates[0].Chosen != testCase.chosen || candidates[0].Rationale == "" {
			t.Fatalf("resolved %q = %#v", testCase.text, candidates)
		}
	}
}

func TestScanPreservesPathAndShortOptions(t *testing.T) {
	cases := []struct {
		text   string
		chosen string
	}{
		{"Use edge_bundle because it keeps the release artifact self-contained.", "edge_bundle"},
		{"Use 1 because it inserts the table directly in the chapter.", "1"},
		{"Use MS-Swift because it supports the research workflow.", "MS-Swift"},
		{"Use SurGE because it matches the existing proxy setup.", "SurGE"},
		{"Use subprojects/paper_report_agent/ because the package is already organized there.", "subprojects/paper_report_agent/"},
		{"Use opencode/deepseek-v4-flash-free because that provider is available locally.", "opencode/deepseek-v4-flash-free"},
	}
	for _, testCase := range cases {
		candidates := Scan([]record.MessageRecord{msg("s1", "user", testCase.text)})
		if len(candidates) != 1 || candidates[0].Chosen != testCase.chosen || candidates[0].Rationale == "" {
			t.Fatalf("resolved %q = %#v", testCase.text, candidates)
		}
	}

	alternatives := Scan([]record.MessageRecord{msg("s1", "user", "Choose opencode/deepseek-v4-flash-free or edge_bundle because both are available locally.")})
	if len(alternatives) != 1 || alternatives[0].Chosen != "opencode/deepseek-v4-flash-free" || len(alternatives[0].Options) != 2 {
		t.Fatalf("path alternatives = %#v", alternatives)
	}
}

func TestScanRejectsMetadataNegativeAndOpenQuestionNoise(t *testing.T) {
	for _, text := range []string{
		"不要使用 http://localhost:5173/，因为 localhost 可能打开另一个服务。",
		"不要把 /responses 再写进 base_url，因为 wire_api 会自动使用这个路径。",
		"这里会使用 `web-research` skill，因为你明确要做外部调研。",
		"使用 `documents:documents` 技能，因为这次需要同步修改正式 Word 文档。",
		"这次不用 pane run，改用 send-text + send-keys 手动方式？",
		"Since `&` might be an alignment point in equations, should I go with `Qwen and Llama` instead to avoid issues?",
		"The issue is that the route builder doesn't pick them up because no channel returns them.",
		"I’m pulling the exact installed versions now, because the command recommendation depends on the environment.",
		"I want to be precise about whether the command works with the existing environment.",
	} {
		if candidates := Scan([]record.MessageRecord{msg("s1", "assistant", text)}); len(candidates) != 0 {
			t.Errorf("metadata/negative/question candidate for %q = %#v", text, candidates)
		}
	}
}

func TestScanDoesNotSplitRationaleEnumerationsIntoOptions(t *testing.T) {
	candidates := Scan([]record.MessageRecord{msg("s1", "user", "Use SurGE because it supports 检索、生成和引用。")})
	if len(candidates) != 1 || candidates[0].Chosen != "SurGE" || len(candidates[0].Options) != 1 || candidates[0].Options[0] != "SurGE" {
		t.Fatalf("rationale enumeration changed choice/options = %#v", candidates)
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
