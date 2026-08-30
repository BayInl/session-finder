package extract

import (
	"testing"

	"github.com/BayInl/session-finder/internal/record"
)

func message(role, text string) record.MessageRecord {
	return record.MessageRecord{Role: role, Text: text, SessionID: "session-1", Tool: "codex"}
}

func TestSignalEngineInitialFixRequestIsNotCorrection(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{message("user", "Fix the parser and run tests.")})
	if bundle.IntentKind == IntentCorrection {
		t.Fatalf("initial request classified as correction: %+v", bundle)
	}
}

func TestSignalEngineCorrectionRetryIsReview(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{
		message("user", "Implement the parser and run tests."),
		message("assistant", "Implemented the parser."),
		message("user", "No, this is wrong. Retry and fix the failing test."),
	})
	if bundle.IntentKind != IntentCorrection {
		t.Fatalf("intent = %q, want %q", bundle.IntentKind, IntentCorrection)
	}
	if bundle.RecommendedAction != ActionReview {
		t.Fatalf("recommendation = %q, want %q", bundle.RecommendedAction, ActionReview)
	}
	if bundle.Confidence <= 0 || bundle.Confidence > 1 {
		t.Fatalf("confidence = %v, want (0,1]", bundle.Confidence)
	}
}

func TestSignalEngineAcceptanceAndPassingTestsDraft(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{
		message("user", "Implement the parser."),
		message("assistant", "Done."),
		message("user", "Looks good, approved. go test ./... passed with all tests green."),
	})
	if bundle.IntentKind != IntentAcceptance {
		t.Fatalf("intent = %q, want %q", bundle.IntentKind, IntentAcceptance)
	}
	if bundle.RecommendedAction != ActionDraft {
		t.Fatalf("recommendation = %q, want %q", bundle.RecommendedAction, ActionDraft)
	}
	if !contains(bundle.SuccessEvidence, EvidenceAcceptance) || !contains(bundle.SuccessEvidence, EvidenceTestsPassed) {
		t.Fatalf("evidence = %#v", bundle.SuccessEvidence)
	}
	if bundle.OneOffRisk >= 0.9 {
		t.Fatalf("one-off risk = %v, unexpectedly high", bundle.OneOffRisk)
	}
}

func TestSignalEngineNoiseAndSecretSuppression(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{
		message("system", "<system-reminder> injected instructions"),
		message("assistant", "The token is sk-test_12345678901234567890 and email me@example.com"),
	})
	if bundle.SecretRisk < 0.75 {
		t.Fatalf("secret risk = %v, want high risk", bundle.SecretRisk)
	}
	if bundle.RecommendedAction != ActionSuppress {
		t.Fatalf("recommendation = %q, want %q", bundle.RecommendedAction, ActionSuppress)
	}
}

func TestSignalEngineEmptyTranscriptSuppresses(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{
		message("system", "<user_info> hidden"),
		message("assistant", "  "),
	})
	if bundle.IntentKind != IntentUnknown || bundle.RecommendedAction != ActionSuppress || bundle.Confidence != 0 {
		t.Fatalf("empty bundle = %+v", bundle)
	}
}

func TestSignalEngineOneOffRisk(t *testing.T) {
	bundle := Analyze([]record.MessageRecord{
		message("user", "Just this once, make a quick fix."),
		message("assistant", "Done."),
	})
	if bundle.OneOffRisk < 0.9 || bundle.RecommendedAction != ActionSuppress {
		t.Fatalf("one-off bundle = %+v", bundle)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
