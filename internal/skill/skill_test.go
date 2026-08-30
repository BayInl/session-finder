package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/record"
)

func skillMessage(role, text string) record.MessageRecord {
	return record.MessageRecord{Tool: "codex", SessionID: "session-1", CWD: "/tmp/project", Role: role, Text: text, SourcePath: "/tmp/session.json"}
}

func TestQualityGateSuppressesMissingEvidenceAndOneOff(t *testing.T) {
	for name, signals := range map[string]extract.SignalBundle{
		"missing evidence": {Confidence: 0.9, OneOffRisk: 0.1},
		"one off":          {Confidence: 0.9, SuccessEvidence: []string{"acceptance"}, OneOffRisk: 0.9},
		"secret":           {Confidence: 0.9, SuccessEvidence: []string{"acceptance"}, SecretRisk: 0.8},
	} {
		t.Run(name, func(t *testing.T) {
			report := QualityGate(signals)
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
	if !IsValidSlug(bundle.Slug) || bundle.SessionID != "session-1" {
		t.Fatalf("bundle metadata = %+v", bundle)
	}
	if len(bundle.Evidence) < 2 {
		t.Fatalf("evidence = %+v, want acceptance and tests", bundle.Evidence)
	}
	if strings.Contains(bundle.Instructions, "Looks good") {
		t.Fatalf("instructions unexpectedly contain reviewer evidence: %q", bundle.Instructions)
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
