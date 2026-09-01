// Package decisions implements decision-ledger extraction, validation, review,
// and persistence on top of the shared extraction candidate store.
package decisions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/BayInl/session-finder/internal/record"
)

const (
	KindDecision = "decision"

	StatusProposed   = "proposed"
	StatusAccepted   = "accepted"
	StatusRejected   = "rejected"
	StatusDeferred   = "deferred"
	StatusSuperseded = "superseded"
	StatusDraft      = "draft"

	OutcomeUnknown     = "unknown"
	OutcomePlanned     = "planned"
	OutcomeImplemented = "implemented"
	OutcomeAbandoned   = "abandoned"

	EvidenceExplicit       = "explicit"
	EvidenceTranscript     = "transcript"
	EvidenceRationale      = "rationale"
	EvidenceAlternative    = "alternative"
	EvidenceImplementation = "implementation"
	EvidenceTest           = "test"
	EvidenceTestsPassed    = "tests_passed"
	EvidenceCommit         = "commit"
)

const minEvidenceQuoteRunes = 8

var (
	ErrInvalidDecision      = errors.New("invalid decision")
	ErrEvidenceNotFound     = errors.New("evidence quote does not match session message")
	ErrEvidenceTooShort     = errors.New("evidence quote is shorter than 8 Unicode characters")
	ErrExplicitEvidenceRole = errors.New("explicit evidence must quote a user message")
	ErrConfirmationRequired = errors.New("confirmation is required before writing a decision")
	ErrInvalidReviewAction  = errors.New("invalid decision review action")
	ErrCommitNotCandidate   = errors.New("selected commit is not a candidate")
)

// Evidence is a quote-backed claim from a session transcript. Quote is kept
// verbatim: validation uses a byte-exact substring match against the original
// message text, not a normalized or case-insensitive comparison.
type Evidence struct {
	Kind         string `json:"kind"`
	Quote        string `json:"quote"`
	MessageIndex int    `json:"message_index,omitempty"`
	Role         string `json:"role,omitempty"`
	Source       string `json:"source,omitempty"`
}

// CommitRef is a read-only local-git candidate associated with a decision.
// Hash is the full object ID returned by git; callers must not invent hashes.
type CommitRef struct {
	Hash      string   `json:"hash"`
	ShortHash string   `json:"short_hash,omitempty"`
	Subject   string   `json:"subject"`
	Author    string   `json:"author,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	Paths     []string `json:"paths,omitempty"`
}

// Provenance identifies where a decision was extracted. It intentionally keeps
// source metadata separate from evidence quotes so quote validation remains
// independent of parser-specific identifiers.
type Provenance struct {
	Tool         string `json:"tool,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	SessionID    string `json:"session_id"`
	Extractor    string `json:"extractor,omitempty"`
	ExtractedAt  string `json:"extracted_at,omitempty"`
	MessageStart int    `json:"message_start,omitempty"`
	MessageEnd   int    `json:"message_end,omitempty"`
}

// Decision is the ADR-style durable ledger record. The field names mirror the
// MVP schema: kind/status/context/options/chosen/rationale/evidence/outcome/
// commits/confidence/provenance/supersedes.
type Decision struct {
	ID         string      `json:"id,omitempty"`
	Kind       string      `json:"kind"`
	Status     string      `json:"status"`
	Context    string      `json:"context"`
	Options    []string    `json:"options"`
	Chosen     string      `json:"chosen"`
	Rationale  string      `json:"rationale"`
	Evidence   []Evidence  `json:"evidence"`
	Outcome    string      `json:"outcome"`
	Commits    []CommitRef `json:"commits"`
	Confidence float64     `json:"confidence"`
	Provenance Provenance  `json:"provenance"`
	Supersedes string      `json:"supersedes,omitempty"`
	CreatedAt  string      `json:"created_at,omitempty"`
	UpdatedAt  string      `json:"updated_at,omitempty"`
}

// DecisionCandidate is the non-durable result of high-recall extraction. A
// candidate is safe to display or send to review; persistence is explicit.
type DecisionCandidate struct {
	Decision
	Reasons []string `json:"reasons,omitempty"`
}

func validDecisionStatus(status string) bool {
	switch status {
	case StatusProposed, StatusAccepted, StatusRejected, StatusDeferred, StatusSuperseded, StatusDraft:
		return true
	default:
		return false
	}
}

func validOutcome(outcome string) bool {
	switch outcome {
	case OutcomeUnknown, OutcomePlanned, OutcomeImplemented, OutcomeAbandoned:
		return true
	default:
		return false
	}
}

func validEvidenceKind(kind string) bool {
	switch kind {
	case EvidenceExplicit, EvidenceTranscript, EvidenceRationale, EvidenceAlternative,
		EvidenceImplementation, EvidenceTest, EvidenceTestsPassed, EvidenceCommit:
		return true
	default:
		return false
	}
}

// HasImplementationEvidence reports whether a decision contains evidence that
// can support an implemented outcome. A plan, recommendation, or decision
// quote alone is deliberately not implementation evidence.
func (d Decision) HasImplementationEvidence() bool {
	for _, evidence := range d.Evidence {
		switch strings.ToLower(strings.TrimSpace(evidence.Kind)) {
		case EvidenceImplementation, EvidenceTest, EvidenceTestsPassed, EvidenceCommit:
			return true
		}
	}
	return false
}

// Normalize returns a copy with safe defaults. In particular, a decision with
// no implementation evidence cannot claim an implemented outcome.
func (d Decision) Normalize() Decision {
	if strings.TrimSpace(d.Kind) == "" {
		d.Kind = KindDecision
	}
	if strings.TrimSpace(d.Status) == "" {
		d.Status = StatusProposed
	}
	if strings.TrimSpace(d.Outcome) == "" {
		d.Outcome = OutcomeUnknown
	}
	if !d.HasImplementationEvidence() && d.Outcome == OutcomeImplemented {
		d.Outcome = OutcomeUnknown
	}
	if d.Options == nil {
		d.Options = []string{}
	}
	if d.Evidence == nil {
		d.Evidence = []Evidence{}
	}
	if d.Commits == nil {
		d.Commits = []CommitRef{}
	}
	return d
}

// Validate checks the structural ADR contract. Transcript-dependent quote
// checks are performed by ValidateWithMessages.
func (d Decision) Validate() error {
	d = d.Normalize()
	if d.Kind != KindDecision {
		return fmt.Errorf("%w: kind must be %q", ErrInvalidDecision, KindDecision)
	}
	if !validDecisionStatus(d.Status) {
		return fmt.Errorf("%w: invalid status %q", ErrInvalidDecision, d.Status)
	}
	if strings.TrimSpace(d.Context) == "" {
		return fmt.Errorf("%w: context is required", ErrInvalidDecision)
	}
	if !validOutcome(d.Outcome) {
		return fmt.Errorf("%w: invalid outcome %q", ErrInvalidDecision, d.Outcome)
	}
	if math.IsNaN(d.Confidence) || math.IsInf(d.Confidence, 0) || d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("%w: confidence must be between 0 and 1", ErrInvalidDecision)
	}
	if d.Status == StatusSuperseded && strings.TrimSpace(d.Supersedes) == "" {
		return fmt.Errorf("%w: superseded decision requires supersedes", ErrInvalidDecision)
	}
	for i, option := range d.Options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("%w: option %d is empty", ErrInvalidDecision, i)
		}
	}
	for i, evidence := range d.Evidence {
		if !validEvidenceKind(strings.ToLower(strings.TrimSpace(evidence.Kind))) {
			return fmt.Errorf("%w: evidence %d has invalid kind %q", ErrInvalidDecision, i, evidence.Kind)
		}
		if err := validateEvidenceQuote(evidence.Quote); err != nil {
			return fmt.Errorf("%w: evidence %d: %w", ErrInvalidDecision, i, err)
		}
		if evidence.MessageIndex < 0 {
			return fmt.Errorf("%w: evidence %d has invalid message index", ErrInvalidDecision, i)
		}
	}
	if strings.TrimSpace(d.Provenance.SessionID) == "" {
		return fmt.Errorf("%w: provenance.session_id is required", ErrInvalidDecision)
	}
	return nil
}

// ValidateEvidence verifies every evidence quote against original messages.
// Explicit evidence is additionally restricted to a user-role message, which
// prevents assistant text from being mistaken for user confirmation.
func ValidateEvidence(messages []record.MessageRecord, evidence []Evidence) error {
	for i, item := range evidence {
		if err := validateEvidenceQuote(item.Quote); err != nil {
			return fmt.Errorf("%w: evidence %d: %v", err, i, err)
		}
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		matchedAny, matchedUser := false, false
		for messageIndex, message := range messages {
			// MessageIndex is optional; positive values pin the quote to one
			// message. Indices are one-based in serialized evidence. Zero means
			// search all messages for payloads that omit the field.
			if item.MessageIndex > 0 && item.MessageIndex != messageIndex+1 {
				continue
			}
			if !strings.Contains(message.Text, item.Quote) {
				continue
			}
			matchedAny = true
			if strings.ToLower(strings.TrimSpace(message.Role)) == "user" {
				matchedUser = true
			}
		}
		if kind == EvidenceExplicit && !matchedUser {
			if matchedAny {
				return fmt.Errorf("%w: evidence %d", ErrExplicitEvidenceRole, i)
			}
			return fmt.Errorf("%w: evidence %d quote %q", ErrEvidenceNotFound, i, item.Quote)
		}
		if !matchedAny {
			return fmt.Errorf("%w: evidence %d quote %q", ErrEvidenceNotFound, i, item.Quote)
		}
	}
	return nil
}

// ValidateWithMessages enforces both the schema and exact quote provenance.
func (d Decision) ValidateWithMessages(messages []record.MessageRecord) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return ValidateEvidence(messages, d.Evidence)
}

// MarshalDecision validates a decision before serializing it for durable
// storage. Callers that need transcript validation should call ValidateWithMessages
// before this function.
func MarshalDecision(d Decision) ([]byte, error) {
	d = d.Normalize()
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

// DecodeDecision strictly decodes and validates a persisted decision payload.
func DecodeDecision(data []byte) (Decision, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decision Decision
	if err := decoder.Decode(&decision); err != nil {
		return Decision{}, fmt.Errorf("%w: %v", ErrInvalidDecision, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Decision{}, fmt.Errorf("%w: trailing JSON", ErrInvalidDecision)
		}
		return Decision{}, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidDecision, err)
	}
	decision = decision.Normalize()
	if err := decision.Validate(); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// StatusForCandidate maps the generic extraction state to ADR display state.
func StatusForCandidate(status string) string {
	switch status {
	case "approved", "published":
		return StatusAccepted
	case "rejected":
		return StatusRejected
	case "deferred":
		return StatusDeferred
	case "draft", "in_review":
		return StatusDraft
	default:
		return StatusProposed
	}
}

// CandidateStatusForDecision maps an ADR state to the shared extraction state.
func CandidateStatusForDecision(status string) string {
	switch status {
	case StatusAccepted:
		return "approved"
	case StatusRejected:
		return "rejected"
	case StatusDeferred:
		return "deferred"
	case StatusDraft:
		return "draft"
	default:
		return "detected"
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func validateEvidenceQuote(quote string) error {
	if strings.TrimSpace(quote) == "" {
		return ErrEvidenceNotFound
	}
	if len([]rune(strings.TrimSpace(quote))) < minEvidenceQuoteRunes {
		return ErrEvidenceTooShort
	}
	return nil
}

// ExactQuoteMatch is a small exported helper used by callers and tests.
func ExactQuoteMatch(message, quote string) bool {
	return validateEvidenceQuote(quote) == nil && strings.Contains(message, quote)
}

// IsEvidenceError reports whether an error is caused by quote provenance.
func IsEvidenceError(err error) bool {
	return errors.Is(err, ErrEvidenceNotFound) || errors.Is(err, ErrEvidenceTooShort) || errors.Is(err, ErrExplicitEvidenceRole)
}

// EnsureOutcome applies the MVP safety rule and returns a normalized copy.
func EnsureOutcome(d Decision) Decision { return d.Normalize() }

// Schema returns the JSON schema-shaped field contract used by integrations.
// It is intentionally returned as a copy so callers cannot mutate package state.
func Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["kind","status","context","options","chosen","rationale","evidence","outcome","commits","confidence","provenance"],"properties":{"id":{"type":"string"},"kind":{"const":"decision"},"status":{"enum":["proposed","accepted","rejected","deferred","superseded","draft"]},"context":{"type":"string"},"options":{"type":"array","items":{"type":"string"}},"chosen":{"type":"string"},"rationale":{"type":"string"},"evidence":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["kind","quote"],"properties":{"kind":{"type":"string"},"quote":{"type":"string"},"message_index":{"type":"integer","minimum":0},"role":{"type":"string"},"source":{"type":"string"}}}},"outcome":{"enum":["unknown","planned","implemented","abandoned"]},"commits":{"type":"array","items":{"type":"object"}},"confidence":{"type":"number","minimum":0,"maximum":1},"provenance":{"type":"object"},"supersedes":{"type":"string"},"created_at":{"type":"string"},"updated_at":{"type":"string"}}}`)
}
