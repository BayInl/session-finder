// Package skill turns successful session traces into reviewable Agent Skills.
//
// The package is deliberately offline-first: transcript analysis uses the local
// extract signal engine and rendered skills contain instructions only. Evidence
// remains in the local candidate store and is never copied into SKILL.md.
package skill

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/record"
)

const (
	// TargetGeneric publishes to the user-level Agent Skills directory.
	TargetGeneric = "generic"
	// TargetClaude publishes to Claude's user skill directory.
	TargetClaude = "claude"
	// TargetKimi publishes to Kimi's user skill directory.
	TargetKimi = "kimi"
	// TargetProject publishes below a project directory.
	TargetProject = "project"
)

const (
	QualityDraft    = "draft"
	QualitySuppress = "suppress"
)

const (
	ReviewApprove = "approve"
	ReviewReject  = "reject"
	ReviewDefer   = "defer"
	ReviewEdit    = "edit"
	ReviewSplit   = "split"
)

var (
	ErrInvalidSlug        = errors.New("invalid skill slug")
	ErrInvalidFrontmatter = errors.New("invalid skill frontmatter")
	ErrSensitiveContent   = errors.New("skill contains sensitive information")
	ErrSkillConflict      = errors.New("skill already exists")
	ErrInvalidTarget      = errors.New("invalid skill publish target")
	ErrQualitySuppressed  = errors.New("skill candidate suppressed by quality gate")
	ErrReviewAction       = errors.New("invalid review action")
	ErrNoTranscript       = errors.New("session transcript is empty")
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// EvidenceBlock is a local-only pointer to evidence supporting a candidate.
// Excerpt is optional and is stored in the candidate database, never rendered
// into SKILL.md. IDs are stable within a candidate bundle.
type EvidenceBlock struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	SessionID    string `json:"session_id"`
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	SourcePath   string `json:"source_path,omitempty"`
	Summary      string `json:"summary"`
	Excerpt      string `json:"excerpt,omitempty"`
	Decision     string `json:"decision,omitempty"`
	ReviewerNote string `json:"reviewer_note,omitempty"`
}

// QualityReport is the deterministic quality-gate result. A suppress result
// is still returned to explain why a candidate was not publishable. Signals is
// the single source of truth for confidence, evidence, risks, and recommendation.
type QualityReport struct {
	Disposition string               `json:"disposition"`
	Score       float64              `json:"score"`
	Reasons     []string             `json:"reasons"`
	Signals     extract.SignalBundle `json:"signals"`
}

// CandidateBundle is the review/publish contract for one proposed skill.
// Evidence and risks are local review metadata. RenderSkillMarkdown
// intentionally consumes only Slug, Trigger, and Instructions.
type CandidateBundle struct {
	Slug         string          `json:"slug"`
	Trigger      string          `json:"trigger"`
	Instructions string          `json:"instructions"`
	Evidence     []EvidenceBlock `json:"evidence"`
	Quality      QualityReport   `json:"quality"`
	Risks        []string        `json:"risks"`
	SessionID    string          `json:"session_id,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	Title        string          `json:"title,omitempty"`
	CWD          string          `json:"cwd,omitempty"`
	SourcePath   string          `json:"source_path,omitempty"`
}

// ExtractOptions controls transcript extraction, optional candidate review, and
// candidate persistence. Judge is deliberately separate from the shared
// extract.SignalBundle path so skill quality remains independently gated.
type ExtractOptions struct {
	SessionID       string
	CWD             string
	After           string
	Pending         bool
	IndexDBPath     string
	CandidateDBPath string
	Actor           string
	Judge           CandidateJudge
	Segmenter       IntentSegmenter
}

// ReviewRequest describes a human review operation. EvidenceID is optional for
// bundle-level actions and required by callers for evidence-specific actions.
type ReviewRequest struct {
	Action       string
	EvidenceID   string
	ReviewerNote string
	Instructions string
	Trigger      string
	Slug         string
}

// PublishOptions controls the destination and conflict behavior of publish.
type PublishOptions struct {
	Target        string
	HomeDir       string
	ProjectDir    string
	SkillsRoot    string
	AllowExisting bool
}

// PublishResult describes an immutable published skill directory.
type PublishResult struct {
	Target string `json:"target"`
	Slug   string `json:"slug"`
	Path   string `json:"path"`
}

// PendingSession is a session that has not yet produced a skill candidate.
type PendingSession struct {
	Tool       string `json:"tool"`
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	CWD        string `json:"cwd"`
	SourcePath string `json:"source_path"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
}

// ReviewResult returns the updated in-memory bundle and persisted candidate
// state. Edit and split are intentionally non-destructive; callers can publish
// the returned bundle or create a follow-up candidate.
type ReviewResult struct {
	Bundle         CandidateBundle    `json:"bundle"`
	Candidate      extract.Candidate  `json:"candidate"`
	Action         string             `json:"action"`
	SplitBundle    *CandidateBundle   `json:"split_bundle,omitempty"`
	SplitCandidate *extract.Candidate `json:"split_candidate,omitempty"`
}

// ValidateSlug enforces the Agent Skills naming rule and directory equality.
func ValidateSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" || len(slug) > 64 || !slugRE.MatchString(slug) {
		return ErrInvalidSlug
	}
	return nil
}

// IsValidSlug reports whether slug is a valid Agent Skills name.
func IsValidSlug(slug string) bool { return ValidateSlug(slug) == nil }

// normalizeStringList makes JSON and review output deterministic.
func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// normalizeBundle applies safe defaults without changing review metadata.
func normalizeBundle(bundle CandidateBundle) CandidateBundle {
	bundle.Slug = strings.TrimSpace(bundle.Slug)
	bundle.Trigger = strings.TrimSpace(bundle.Trigger)
	bundle.Instructions = strings.TrimSpace(bundle.Instructions)
	bundle.Evidence = append([]EvidenceBlock(nil), bundle.Evidence...)
	bundle.Quality.Reasons = normalizeStringList(bundle.Quality.Reasons)
	bundle.Risks = normalizeStringList(bundle.Risks)
	return bundle
}

// CandidatePayload is a stable JSON representation used in extract.Store.
func CandidatePayload(bundle CandidateBundle) ([]byte, error) {
	return jsonMarshal(normalizeBundle(bundle))
}

// BundleFromCandidate decodes a skill bundle stored in an extract candidate.
func BundleFromCandidate(candidate extract.Candidate) (CandidateBundle, error) {
	var bundle CandidateBundle
	if len(candidate.Payload) == 0 {
		return bundle, errors.New("candidate payload is empty")
	}
	if err := json.Unmarshal(candidate.Payload, &bundle); err != nil {
		return bundle, err
	}
	return normalizeBundle(bundle), nil
}

// messageSummary returns a bounded, whitespace-normalized evidence description.
func messageSummary(message record.MessageRecord, max int) string {
	text := strings.Join(strings.Fields(message.Text), " ")
	if max <= 0 || len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max-1]) + "…"
}

// Keep JSON behavior in one place so callers get deterministic encoding.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
