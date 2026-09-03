package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/llm"
	"github.com/BayInl/session-finder/internal/record"
)

func skillMessage(role, text string) record.MessageRecord {
	return record.MessageRecord{Tool: "codex", SessionID: "session-1", CWD: "/tmp/project", Role: role, Text: text, SourcePath: "/tmp/session.json"}
}

type segmentTestClient struct {
	calls [][]llm.Message
}

func (c *segmentTestClient) Complete(_ context.Context, request llm.CompletionRequest) (llm.CompletionResponse, error) {
	c.calls = append(c.calls, append([]llm.Message(nil), request.Transcript...))
	turns := make([]llm.SegmentTurn, len(request.Transcript))
	for index := range turns {
		decision := llm.SegmentDecisionSame
		if index == 0 {
			decision = llm.SegmentDecisionNew
		}
		turns[index] = llm.SegmentTurn{Index: index, Decision: decision}
	}
	data, err := json.Marshal(llm.SegmentResult{Turns: turns})
	return llm.CompletionResponse{Provider: "test", Model: "test", JSON: data}, err
}

func TestQualityGateSuppressesMissingEvidenceAndOneOff(t *testing.T) {
	for name, signals := range map[string]extract.SignalBundle{
		"missing evidence": {Confidence: 0.9, OneOffRisk: 0.1},
		"one off":          {Confidence: 0.9, SuccessEvidence: []string{"acceptance"}, OneOffRisk: 0.9},
		"secret":           {Confidence: 0.9, SuccessEvidence: []string{"acceptance"}, SecretRisk: 0.8},
	} {
		t.Run(name, func(t *testing.T) {
			report := EvaluateQuality(signals)
			if report.Disposition != QualitySuppress || len(report.Reasons) == 0 {
				t.Fatalf("report = %+v, want suppression with reason", report)
			}
		})
	}
}

func TestBuildCandidateUsesExtractSignalsAndEvidencePointers(t *testing.T) {
	bundle, err := BuildCandidate([]record.MessageRecord{
		skillMessage("user", "Document the release workflow."),
		skillMessage("assistant", "Run go test ./...; then build the release artifact."),
		skillMessage("user", "Looks good, approved. go test ./... passed with all tests green."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Quality.Disposition != QualityDraft {
		t.Fatalf("quality = %+v", bundle.Quality)
	}
	if bundle.Slug != "document-the-release-workflow" || bundle.SessionID != "session-1" {
		t.Fatalf("bundle metadata = %+v", bundle)
	}
	if len(bundle.Evidence) < 2 {
		t.Fatalf("evidence = %+v, want acceptance and tests", bundle.Evidence)
	}
	if strings.Contains(bundle.Instructions, "Looks good") {
		t.Fatalf("instructions unexpectedly contain reviewer evidence: %q", bundle.Instructions)
	}
}

func TestBuildCandidateUsesHashSlugForCJKTitle(t *testing.T) {
	messages := []record.MessageRecord{
		{Tool: "codex", SessionID: "cjk-session", Title: "发布流程", Role: "user", Text: "记录发布流程。"},
		{Tool: "codex", SessionID: "cjk-session", Title: "发布流程", Role: "assistant", Text: "运行 go test ./... 然后构建。"},
	}
	bundle, err := BuildCandidate(messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bundle.Slug, "skill-") || len(bundle.Slug) != len("skill-")+8 || !IsValidSlug(bundle.Slug) {
		t.Fatalf("CJK slug = %q, want skill-<8 hex chars>", bundle.Slug)
	}
}

func TestExtractAndPersistNumbersDuplicateCJKSlugs(t *testing.T) {
	store, err := extract.Open(filepath.Join(t.TempDir(), "candidates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	messages := func(sessionID string) []record.MessageRecord {
		return []record.MessageRecord{
			{Tool: "codex", SessionID: sessionID, Title: "发布流程", Role: "user", Text: "记录发布流程。"},
			{Tool: "codex", SessionID: sessionID, Title: "发布流程", Role: "assistant", Text: "运行 go test ./... 然后构建。"},
		}
	}
	first, _, err := ExtractAndPersist(context.Background(), store, messages("session-1"), "test")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := ExtractAndPersist(context.Background(), store, messages("session-2"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Slug, "skill-") || second.Slug != first.Slug+"-2" {
		t.Fatalf("duplicate CJK slugs = %q, %q", first.Slug, second.Slug)
	}
}

func TestBuildCandidateFiltersInjectedMetadata(t *testing.T) {
	messages := []record.MessageRecord{
		{Tool: "codex", SessionID: "noise", Title: "# AGENTS.md instructions for /Users/test", Role: "user", Text: "# Context from my IDE setup:\n<environment_context>hidden</environment_context>"},
		{Tool: "codex", SessionID: "noise", Title: "# AGENTS.md instructions for /Users/test", Role: "user", Text: "Document the release workflow."},
		{Tool: "codex", SessionID: "noise", Title: "# AGENTS.md instructions for /Users/test", Role: "assistant", Text: "Run go test ./...; then build the release artifact."},
		{Tool: "codex", SessionID: "noise", Title: "# AGENTS.md instructions for /Users/test", Role: "user", Text: "go test ./... passed; looks good."},
	}
	bundle, err := BuildCandidate(messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.Slug, "agents") || strings.Contains(bundle.Slug, "environment") {
		t.Fatalf("injected title entered slug: %q", bundle.Slug)
	}
	if strings.Contains(bundle.Trigger, "environment") || strings.Contains(bundle.Trigger, "AGENTS") {
		t.Fatalf("injected text entered trigger: %q", bundle.Trigger)
	}
	if strings.Contains(bundle.Instructions, "Context from my IDE") || strings.Contains(bundle.Instructions, "AGENTS.md") {
		t.Fatalf("injected text entered instructions: %q", bundle.Instructions)
	}
}

func TestRenderAndValidateSkillMarkdownOmitsEvidence(t *testing.T) {
	bundle := CandidateBundle{
		Slug:         "release-workflow",
		Trigger:      "Use when preparing a release.",
		Instructions: "1. Run the test suite.\n2. Build the artifact.",
		Evidence:     []EvidenceBlock{{ID: "evidence-1", Summary: "private transcript evidence"}},
		Quality: QualityReport{Disposition: QualityDraft, Signals: extract.SignalBundle{
			SuccessEvidence: []string{extract.EvidenceAcceptance}, OneOffRisk: 0.1,
		}},
	}
	markdown, err := RenderSkillMarkdown(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "name: release-workflow") || !strings.Contains(markdown, "description:") {
		t.Fatalf("markdown = %q", markdown)
	}
	if strings.Contains(markdown, "private transcript evidence") {
		t.Fatal("evidence leaked into SKILL.md")
	}
	frontmatter, err := ParseAndValidateSkillMarkdown(markdown, "release-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if frontmatter.Name != "release-workflow" || frontmatter.Description == "" {
		t.Fatalf("frontmatter = %+v", frontmatter)
	}
}

func TestRenderRejectsInvalidNameAndSensitiveContent(t *testing.T) {
	if _, err := RenderSkillMarkdown(CandidateBundle{Slug: "Bad_Name", Trigger: "x", Instructions: "y"}); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("invalid slug error = %v", err)
	}
	if _, err := RenderSkillMarkdown(CandidateBundle{Slug: "safe", Trigger: "x", Instructions: "use token=sk_live_1234567890abcdef"}); !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("secret error = %v", err)
	}
}

func publishableBundle() CandidateBundle {
	return CandidateBundle{
		Slug: "release-workflow", Trigger: "Use when preparing a release.", Instructions: "Run tests, then build.",
		Quality: QualityReport{Disposition: QualityDraft, Signals: extract.SignalBundle{
			SuccessEvidence: []string{extract.EvidenceTestsPassed}, OneOffRisk: 0.1,
		}},
	}
}

func TestPublishUsesTargetRootAndDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	result, err := Publish(publishableBundle(), PublishOptions{Target: TargetGeneric, SkillsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: release-workflow") {
		t.Fatalf("published content = %q", data)
	}
	if _, err := Publish(publishableBundle(), PublishOptions{Target: TargetGeneric, SkillsRoot: root}); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("second publish error = %v, want conflict", err)
	}
}

func TestRepositoryReviewAndPublishStateMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	candidate, err := repository.CreateBundle(context.Background(), publishableBundle(), "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewApprove})
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Status != extract.StatusApproved {
		t.Fatalf("status = %q, want approved", result.Candidate.Status)
	}
	published, err := PublishCandidate(context.Background(), path, candidate.ID, PublishOptions{Target: TargetGeneric, SkillsRoot: filepath.Join(t.TempDir(), "skills")})
	if err != nil {
		t.Fatal(err)
	}
	if published.Path == "" {
		t.Fatal("empty published path")
	}
	updated, err := repository.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != extract.StatusPublished {
		t.Fatalf("published status = %q", updated.Status)
	}
}

func TestRepositoryReviewPersistsEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	candidate, err := repository.CreateBundle(context.Background(), publishableBundle(), "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewEdit, Trigger: "Use for release preparation."})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bundle.Trigger != "Use for release preparation." {
		t.Fatalf("edited bundle = %+v", result.Bundle)
	}
	reloaded, bundle, err := repository.GetBundle(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Version <= candidate.Version || bundle.Trigger != result.Bundle.Trigger {
		t.Fatalf("reloaded candidate=%+v bundle=%+v", reloaded, bundle)
	}
}

func TestExtractPendingSkipsEmptySessionsAndContinues(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "index.db")
	candidatePath := filepath.Join(root, "candidates.db")
	db, err := index.Open(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.InitializeSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('grok', 'empty-session', '/tmp/empty', 'Empty', 1704067200, 1704067200, '/tmp/empty.jsonl');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'system', 1704067201, '<environment_context>hidden</environment_context>');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'normal-session', '/tmp/normal', 'Normal', 1704067100, 1704067100, '/tmp/normal.jsonl');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'user', 1704067101, 'Document the release workflow.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'assistant', 1704067102, 'Run go test ./...; then build the artifact.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'user', 1704067103, 'Looks good, go test ./... passed.');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	pending, created, err := ExtractPending(context.Background(), ExtractOptions{IndexDBPath: indexPath, CandidateDBPath: candidatePath})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || len(created) != 2 {
		t.Fatalf("pending=%d created=%d, want both sessions processed", len(pending), len(created))
	}
	statuses := map[string]string{}
	for _, candidate := range created {
		statuses[candidate.SessionID] = candidate.Status
	}
	if statuses["empty-session"] != extract.StatusFailed || statuses["normal-session"] != extract.StatusDraft {
		t.Fatalf("created statuses = %#v", statuses)
	}
	pending, created, err = ExtractPending(context.Background(), ExtractOptions{IndexDBPath: indexPath, CandidateDBPath: candidatePath})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || len(created) != 0 {
		t.Fatalf("second scan pending=%d created=%d, want no duplicates", len(pending), len(created))
	}
}

func TestUserTurnsSkipsResumeAndToolOutput(t *testing.T) {
	messages := []record.MessageRecord{
		skillMessage("user", "/resume-claude 48e97b70-12fd-4621-9113-1ac4af203053"),
		skillMessage("user", "查号池每个 key 还能用多少次"),
		skillMessage("user", "I executed a terminal command: `pwd` Output: ``` /tmp ```"),
		skillMessage("user", "用已有 frp 做端口映射"),
	}
	got := userTurns(messages)
	if len(got) != 2 || !strings.Contains(got[0].text, "号池") || !strings.Contains(got[1].text, "frp") {
		t.Fatalf("%#v", got)
	}
}

func TestFillSegmentTurnsDefaultsMissingToSame(t *testing.T) {
	got, missing := fillSegmentTurnsObserved(4, llm.SegmentResult{Turns: []llm.SegmentTurn{
		{Index: 2, Decision: llm.SegmentDecisionNew},
	}})
	if len(got.Turns) != 4 || len(missing) != 3 {
		t.Fatalf("turns=%#v missing=%#v", got.Turns, missing)
	}
	if got.Turns[0].Decision != llm.SegmentDecisionNew || got.Turns[1].Decision != llm.SegmentDecisionSame || got.Turns[2].Decision != llm.SegmentDecisionNew || got.Turns[3].Decision != llm.SegmentDecisionSame {
		t.Fatalf("%#v", got.Turns)
	}
}

func TestUserTurnsCoversLongTranscript(t *testing.T) {
	messages := make([]record.MessageRecord, 0, 120)
	for i := 0; i < 60; i++ {
		messages = append(messages, skillMessage("user", fmt.Sprintf("task turn %d", i)))
		messages = append(messages, skillMessage("assistant", "ack"))
	}
	turns := userTurns(messages)
	if len(turns) != 60 || !strings.Contains(turns[59].text, "59") {
		t.Fatalf("user turns = %d last=%#v", len(turns), turns[len(turns)-1])
	}
	client := &segmentTestClient{}
	result, err := NewLLMSegmenter(client).Segment(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || len(client.calls[0]) != 48 || len(client.calls[1]) != 13 || len(result.Turns) != 60 || result.Turns[59].Index != 59 {
		t.Fatalf("calls=%v turns=%#v", []int{len(client.calls[0]), len(client.calls[1])}, result.Turns)
	}
}

func TestSplitTranscriptUsesNewBoundariesAndFallsBack(t *testing.T) {
	messages := []record.MessageRecord{
		skillMessage("user", "flatten match newlines"),
		skillMessage("assistant", "use Preview"),
		skillMessage("user", "check redundant code"),
		skillMessage("assistant", "deleted MatchTerms"),
	}
	slices := applySegmentTurns(messages, []llm.SegmentTurn{
		{Index: 0, Decision: llm.SegmentDecisionNew},
		{Index: 1, Decision: llm.SegmentDecisionNew},
	})
	if len(slices) != 2 || !strings.Contains(slices[0][0].Text, "flatten") || !strings.Contains(slices[1][0].Text, "redundant") {
		t.Fatalf("%#v", slices)
	}
	same := applySegmentTurns(messages, []llm.SegmentTurn{
		{Index: 0, Decision: llm.SegmentDecisionNew},
		{Index: 1, Decision: llm.SegmentDecisionSame},
	})
	if len(same) != 1 || len(same[0]) != 4 {
		t.Fatalf("same-task slices = %#v", same)
	}
	fallback := SplitTranscriptDetailed(context.Background(), messages, IntentSegmenterFunc(func(context.Context, []record.MessageRecord) (llm.SegmentResult, error) {
		return llm.SegmentResult{}, errors.New("relay down")
	}))
	if len(fallback.Slices) != 1 || len(fallback.Slices[0]) != 4 || !fallback.Fallback || len(fallback.Observations) != 1 || fallback.Observations[0].Kind != SegmentFallbackError {
		t.Fatalf("fallback = %#v", fallback)
	}
	missing := SplitTranscriptDetailed(context.Background(), messages, IntentSegmenterFunc(func(context.Context, []record.MessageRecord) (llm.SegmentResult, error) {
		return llm.SegmentResult{Turns: []llm.SegmentTurn{{Index: 0, Decision: llm.SegmentDecisionNew}}}, nil
	}))
	if !missing.Fallback || len(missing.Observations) != 1 || missing.Observations[0].Kind != SegmentFallbackMissing || missing.Observations[0].Count != 1 {
		t.Fatalf("missing-index observation = %#v", missing)
	}
}

func TestPersistTranscriptMarksSegmentFallback(t *testing.T) {
	store, err := extract.Open(filepath.Join(t.TempDir(), "candidates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	messages := []record.MessageRecord{
		skillMessage("user", "Document the release workflow."),
		skillMessage("assistant", "Run go test ./...; then build the release artifact."),
		skillMessage("user", "Looks good, approved. go test ./... passed."),
	}
	created, err := persistTranscript(context.Background(), store, messages, "test", ExtractOptions{Segmenter: IntentSegmenterFunc(func(context.Context, []record.MessageRecord) (llm.SegmentResult, error) {
		return llm.SegmentResult{}, errors.New("offline relay")
	})})
	if err != nil || len(created) != 1 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	bundle, err := BundleFromCandidate(created[0])
	if err != nil {
		t.Fatal(err)
	}
	if !skillTestContains(bundle.Quality.Reasons, "segment:fallback:segmenter_error:1") {
		t.Fatalf("fallback reason missing: %#v", bundle.Quality.Reasons)
	}
}

func TestExtractPendingCreatesOneCandidatePerSegment(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "index.db")
	candidatePath := filepath.Join(root, "candidates.db")
	db, err := index.Open(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.InitializeSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'mixed-session', '/tmp/mixed', 'Mixed', 1704067200, 1704067200, '/tmp/mixed.jsonl');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067201, 'Flatten escaped newlines in match previews.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'assistant', 1704067202, 'Use Preview then wrap.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067203, 'Looks good.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067204, 'Inspect redundant MatchTerms lexer.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'assistant', 1704067205, 'Reuse index.PositiveTerms.');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067206, 'Approved, tests passed.');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, created, err := ExtractPending(context.Background(), ExtractOptions{
		IndexDBPath:     indexPath,
		CandidateDBPath: candidatePath,
		Segmenter: IntentSegmenterFunc(func(_ context.Context, messages []record.MessageRecord) (llm.SegmentResult, error) {
			return llm.SegmentResult{Turns: []llm.SegmentTurn{
				{Index: 0, Decision: llm.SegmentDecisionNew},
				{Index: 1, Decision: llm.SegmentDecisionConfirm},
				{Index: 2, Decision: llm.SegmentDecisionNew},
				{Index: 3, Decision: llm.SegmentDecisionConfirm},
			}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d want 2: %#v", len(created), created)
	}
}

func TestRejectedCandidateCanReturnToReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	candidate, err := repository.CreateBundle(context.Background(), publishableBundle(), "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewReject}); err != nil {
		t.Fatal(err)
	} else if result.Candidate.Status != extract.StatusRejected {
		t.Fatalf("rejected status = %q", result.Candidate.Status)
	}
	result, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewApprove})
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Status != extract.StatusApproved {
		t.Fatalf("recovered status = %q, want approved", result.Candidate.Status)
	}
	events, err := repository.CandidateStore().Events(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("events = %+v, want create plus five state transitions", events)
	}
}

func TestReviewEditSynchronizesCandidateMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	bundle := publishableBundle()
	bundle.Quality.Signals = extract.SignalBundle{
		Confidence: 0.63, SuccessEvidence: []string{extract.EvidenceTestsPassed},
		OneOffRisk: 0.21, SecretRisk: 0.04, RecommendedAction: extract.ActionReview,
	}
	candidate, err := repository.CreateBundle(context.Background(), bundle, "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE candidates SET confidence = 0.01, success_evidence = '[]', one_off_risk = 0.99, secret_risk = 0.99, recommended_action = 'stale' WHERE id = ?`, candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewEdit, Trigger: "Use for release preparation."}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repository.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Confidence != 0.63 || reloaded.OneOffRisk != 0.21 || reloaded.SecretRisk != 0.04 || reloaded.RecommendedAction != extract.ActionReview || len(reloaded.SuccessEvidence) != 1 || reloaded.SuccessEvidence[0] != extract.EvidenceTestsPassed {
		t.Fatalf("metadata = %+v", reloaded)
	}
}

func TestReviewApproveDoesNotWriteRedundantBundleEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	candidate, err := repository.CreateBundle(context.Background(), publishableBundle(), "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Review(context.Background(), candidate.ID, ReviewRequest{Action: ReviewApprove}); err != nil {
		t.Fatal(err)
	}
	events, err := repository.CandidateStore().Events(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want create, open review, approve", events)
	}
	if events[1].Reason != "open review" || events[2].Reason != "review approve" {
		t.Fatalf("events = %+v", events)
	}
}

func TestPublishAndTransitionCleansDirectoryOnTransitionFailure(t *testing.T) {
	root := t.TempDir()
	transitionErr := errors.New("transition failed")
	_, err := publishAndTransition(publishableBundle(), PublishOptions{Target: TargetGeneric, SkillsRoot: root}, func(string) error {
		return transitionErr
	})
	if !errors.Is(err, transitionErr) {
		t.Fatalf("error = %v, want transition error", err)
	}
	if _, err := os.Stat(filepath.Join(root, publishableBundle().Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published directory stat error = %v, want not exist", err)
	}
}

func skillTestContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func TestBoundCandidateReviewRetainsEveryMessage(t *testing.T) {
	messages := make([]JudgeMessage, 20)
	for index := range messages {
		messages[index] = JudgeMessage{Index: index + 1, Role: "user", Content: strings.Repeat(fmt.Sprintf("message-%d ", index), 200)}
	}
	bounded := boundCandidateReview(CandidateReview{Messages: messages, Conversation: messages})
	if len(bounded.Messages) != len(messages) || len(bounded.Conversation) != len(messages) {
		t.Fatalf("bounded lengths = %d/%d", len(bounded.Messages), len(bounded.Conversation))
	}
	if bounded.Messages[19].Index != 20 || !strings.Contains(bounded.Messages[19].Content, "message-19") {
		t.Fatalf("last candidate message lost: %#v", bounded.Messages[19])
	}
}

func TestBuildCandidateJudgeRunsForLowConfidenceCandidates(t *testing.T) {
	calls := 0
	var seen CandidateReview
	judge := CandidateJudgeFunc(func(_ context.Context, review CandidateReview) (CandidateJudgment, error) {
		calls++
		seen = review
		return CandidateJudgment{Disposition: QualityDraft, Confidence: 0.91, ReasonCodes: []string{"reusable"}}, nil
	})
	messages := []record.MessageRecord{
		skillMessage("user", "Document the release workflow."),
		skillMessage("assistant", "Run go test ./...; then build the release artifact."),
		skillMessage("user", "Add checksum verification."),
		skillMessage("assistant", "Verify checksums before publishing."),
		skillMessage("user", "Keep the rollback notes."),
		skillMessage("assistant", "Rollback notes retained."),
		skillMessage("user", "Looks good, approved. go test ./... passed with all tests green."),
	}
	conversation := append([]record.MessageRecord{skillMessage("user", "Parent conversation context before this segment.")}, messages...)
	bundle, err := BuildCandidateWithOptions(messages, ExtractOptions{Judge: judge, ConversationContext: conversation})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || seen.Candidate.Slug == "" || len(seen.Messages) != len(messages) || len(seen.Conversation) != len(conversation) {
		t.Fatalf("judge calls/review = %d/%#v", calls, seen)
	}
	if !strings.Contains(seen.Messages[len(seen.Messages)-1].Content, "passed") || !strings.Contains(seen.Conversation[0].Content, "Parent conversation") {
		t.Fatalf("judge did not receive complete candidate/parent context: %#v", seen)
	}
	if bundle.Quality.Signals.Confidence != 0.91 || bundle.Quality.Signals.RecommendedAction != extract.ActionDraft {
		t.Fatalf("judge metadata = %+v", bundle.Quality)
	}
	if !skillTestContains(bundle.Quality.Reasons, "llm:reusable") {
		t.Fatalf("judge reason missing: %+v", bundle.Quality.Reasons)
	}
}

func TestBuildCandidateHardSuppressionsSkipJudge(t *testing.T) {
	for name, messages := range map[string][]record.MessageRecord{
		"missing success evidence": {
			skillMessage("user", "Document the release workflow."),
			skillMessage("assistant", "Run the release workflow."),
		},
		"one off": {
			skillMessage("user", "Do this quick fix just this once."),
			skillMessage("assistant", "Done."),
			skillMessage("user", "Looks good, approved."),
		},
		"secret": {
			skillMessage("user", "Document the deployment workflow."),
			skillMessage("assistant", "Use token=sk_live_1234567890abcdef and deploy."),
			skillMessage("user", "Looks good, approved."),
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			bundle, err := BuildCandidateWithOptions(messages, ExtractOptions{Judge: CandidateJudgeFunc(func(context.Context, CandidateReview) (CandidateJudgment, error) {
				calls++
				return CandidateJudgment{Disposition: QualityDraft, Confidence: 1}, nil
			})})
			if err != nil && !errors.Is(err, ErrNoTranscript) {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("hard suppression invoked judge %d times: %+v", calls, bundle)
			}
			if name != "missing success evidence" && bundle.Quality.Disposition != QualitySuppress {
				t.Fatalf("bundle = %+v, want hard suppression", bundle)
			}
		})
	}
}

func TestApplyCandidateJudgmentOnlyRaisesRisksAndSynchronizesMetadata(t *testing.T) {
	bundle := publishableBundle()
	bundle.Quality.Score = 0.72
	bundle.Quality.Signals = extract.SignalBundle{Confidence: 0.7, SuccessEvidence: []string{extract.EvidenceTestsPassed}, OneOffRisk: 0.2, SecretRisk: 0.1, RecommendedAction: extract.ActionDraft}
	got := applyCandidateJudgment(bundle, CandidateJudgment{Disposition: "review", Confidence: 0.99, OneOffRisk: 0.8, SecretRisk: 0.7, ReasonCodes: []string{"ambiguous"}})
	if got.Quality.Signals.OneOffRisk != 0.8 || got.Quality.Signals.SecretRisk != 0.7 {
		t.Fatalf("risks not synchronized: %+v", got.Quality)
	}
	if got.Quality.Signals.Confidence != 0.7 || got.Quality.Score != 0.72 || got.Quality.Signals.RecommendedAction != extract.ActionReview || got.Quality.Disposition != QualityDraft {
		t.Fatalf("review metadata = %+v", got.Quality)
	}
	lower := applyCandidateJudgment(bundle, CandidateJudgment{Disposition: QualityDraft, Confidence: 0.1, OneOffRisk: 0.01, SecretRisk: 0.02})
	if lower.Quality.Signals.Confidence != 0.7 || lower.Quality.Signals.OneOffRisk != 0.2 || lower.Quality.Signals.SecretRisk != 0.1 {
		t.Fatalf("judge lowered existing quality: %+v", lower.Quality)
	}
}

func TestBuildCandidateJudgeFailureKeepsOfflineBundle(t *testing.T) {
	bundle, err := BuildCandidateWithOptions([]record.MessageRecord{
		skillMessage("user", "Document the release workflow."),
		skillMessage("assistant", "Run go test ./...; then build the release artifact."),
		skillMessage("user", "Looks good, approved. go test ./... passed."),
	}, ExtractOptions{Judge: CandidateJudgeFunc(func(context.Context, CandidateReview) (CandidateJudgment, error) {
		return CandidateJudgment{}, errors.New("offline provider")
	})})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Quality.Disposition != QualityDraft || !skillTestContains(bundle.Quality.Reasons, "llm:fallback") {
		t.Fatalf("fallback bundle = %+v", bundle)
	}
}
