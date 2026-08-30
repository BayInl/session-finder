package decisions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/record"

	_ "modernc.org/sqlite"
)

// Store persists decisions through internal/extract's candidate state machine.
// It intentionally contains no direct SQL mutations: create/transition/delete
// operations all go through extract.Store and therefore append candidate events.
type Store struct {
	candidates *extract.Store
	path       string
}

// DecisionStore is a compatibility alias for callers using the domain name.
type DecisionStore = Store

// CreateInput is the explicit confirmation boundary for durable writes.
type CreateInput struct {
	Decision  Decision
	Messages  []record.MessageRecord
	Confirmed bool
	Actor     string
	Reason    string
}

// Input is a concise alias for CreateInput.
type Input = CreateInput

// ReviewAction is one Git-style action available to a reviewer.
type ReviewAction string

const (
	ReviewApprove ReviewAction = "approve"
	ReviewReject  ReviewAction = "reject"
	ReviewDefer   ReviewAction = "defer"
	ReviewEdit    ReviewAction = "edit"
)

// ReviewInput describes one append-only review action.
type ReviewInput struct {
	ID        string
	Action    ReviewAction
	Decision  *Decision
	Messages  []record.MessageRecord
	Actor     string
	Reason    string
	Confirmed bool
}

// ListOptions filters persisted decisions.
type ListOptions struct {
	Status         string
	SessionID      string
	IncludeDeleted bool
	Limit          int
}

// Open opens the shared candidate store at path. Empty path uses the shared
// extraction default database, keeping decisions visible to other features.
func Open(path string) (*Store, error) {
	candidateStore, err := extract.Open(path)
	if err != nil {
		return nil, err
	}
	return &Store{candidates: candidateStore, path: candidateStore.Path()}, nil
}

func OpenStore(path string) (*Store, error)        { return Open(path) }
func NewStore(path string) (*Store, error)         { return Open(path) }
func NewDecisionStore(path string) (*Store, error) { return Open(path) }

// Close closes the underlying candidate store.
func (s *Store) Close() error {
	if s == nil || s.candidates == nil {
		return nil
	}
	return s.candidates.Close()
}

// Path returns the database path used by the store.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// CandidateStore exposes the shared state-machine store for integrations that
// need to inspect generic candidate events.
func (s *Store) CandidateStore() *extract.Store {
	if s == nil {
		return nil
	}
	return s.candidates
}

// Create writes a confirmed decision as a detected extraction candidate.
// Confirmation is mandatory even when the caller has already run the scanner.
func (s *Store) Create(ctx context.Context, input CreateInput) (Decision, error) {
	if !input.Confirmed {
		return Decision{}, ErrConfirmationRequired
	}
	if s == nil || s.candidates == nil {
		return Decision{}, errors.New("nil decision store")
	}
	if len(input.Messages) == 0 {
		return Decision{}, fmt.Errorf("%w: original session messages are required", ErrEvidenceNotFound)
	}
	decision, err := prepareDecision(input.Decision, input.Messages)
	if err != nil {
		return Decision{}, err
	}
	payload, err := MarshalDecision(decision)
	if err != nil {
		return Decision{}, err
	}
	quotes := make([]string, 0, len(decision.Evidence))
	for _, evidence := range decision.Evidence {
		quotes = append(quotes, evidence.Quote)
	}
	candidate, err := s.candidates.Create(ctx, extract.CandidateInput{
		ID:                decision.ID,
		SessionID:         decision.Provenance.SessionID,
		Tool:              decision.Provenance.Tool,
		SourcePath:        decision.Provenance.SourcePath,
		Kind:              KindDecision,
		Title:             decision.Chosen,
		Summary:           decision.Context,
		Payload:           payload,
		Status:            extract.StatusDetected,
		Actor:             input.Actor,
		Reason:            input.Reason,
		Confidence:        decision.Confidence,
		SuccessEvidence:   quotes,
		RecommendedAction: extract.ActionReview,
	})
	if err != nil {
		return Decision{}, err
	}
	decision.ID = candidate.ID
	decision.Status = StatusProposed
	return decision, nil
}

// CreateDecision is the operation-oriented create API.
func (s *Store) CreateDecision(ctx context.Context, decision Decision, messages []record.MessageRecord, confirmed bool) (Decision, error) {
	return s.Create(ctx, CreateInput{Decision: decision, Messages: messages, Confirmed: confirmed, Actor: "user", Reason: "confirmed decision"})
}

// Confirm persists a candidate after explicit human confirmation.
func (s *Store) Confirm(ctx context.Context, candidate DecisionCandidate, messages []record.MessageRecord, actor, reason string) (Decision, error) {
	return s.Create(ctx, CreateInput{Decision: candidate.Decision, Messages: messages, Confirmed: true, Actor: actor, Reason: reason})
}

func prepareDecision(input Decision, messages []record.MessageRecord) (Decision, error) {
	decision := input.Normalize()
	if strings.TrimSpace(decision.ID) == "" {
		id, err := newDecisionID()
		if err != nil {
			return Decision{}, err
		}
		decision.ID = id
	}
	if decision.Provenance.SessionID == "" && len(messages) > 0 {
		decision.Provenance.SessionID = messages[0].SessionID
	}
	if decision.Provenance.Tool == "" && len(messages) > 0 {
		decision.Provenance.Tool = messages[0].Tool
	}
	if decision.Provenance.SourcePath == "" && len(messages) > 0 {
		decision.Provenance.SourcePath = messages[0].SourcePath
	}
	if decision.Provenance.ExtractedAt == "" {
		decision.Provenance.ExtractedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if decision.CreatedAt == "" {
		decision.CreatedAt = decision.Provenance.ExtractedAt
	}
	if decision.UpdatedAt == "" {
		decision.UpdatedAt = decision.CreatedAt
	}
	decision.Status = StatusProposed
	if err := decision.ValidateWithMessages(messages); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func newDecisionID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "decision-" + hex.EncodeToString(raw[:]), nil
}

// Get loads a decision and derives its display status from the shared
// candidate state machine, so generic transitions cannot be bypassed by stale
// payload data.
func (s *Store) Get(ctx context.Context, id string) (Decision, error) {
	if s == nil || s.candidates == nil {
		return Decision{}, errors.New("nil decision store")
	}
	candidate, err := s.candidates.Get(ctx, id)
	if err != nil {
		return Decision{}, err
	}
	if candidate.Kind != KindDecision {
		return Decision{}, fmt.Errorf("candidate %q is not a decision", id)
	}
	decision, err := DecodeDecision(candidate.Payload)
	if err != nil {
		return Decision{}, err
	}
	decision.ID = candidate.ID
	decision.Status = StatusForCandidate(candidate.Status)
	if candidate.Status == extract.StatusDeleted && decision.Supersedes != "" {
		decision.Status = StatusSuperseded
	}
	if decision.Provenance.SessionID == "" {
		decision.Provenance.SessionID = candidate.SessionID
	}
	if decision.Provenance.Tool == "" {
		decision.Provenance.Tool = candidate.Tool
	}
	if decision.Provenance.SourcePath == "" {
		decision.Provenance.SourcePath = candidate.SourcePath
	}
	if decision.CreatedAt == "" {
		decision.CreatedAt = candidate.CreatedAt
	}
	decision.UpdatedAt = candidate.UpdatedAt
	return decision, nil
}

func (s *Store) GetDecision(ctx context.Context, id string) (Decision, error) { return s.Get(ctx, id) }

// List returns durable decisions in the shared store's stable order.
func (s *Store) List(ctx context.Context, options ListOptions) ([]Decision, error) {
	if s == nil || s.candidates == nil {
		return nil, errors.New("nil decision store")
	}
	candidateOptions := extract.ListOptions{SessionID: options.SessionID, Kind: KindDecision, IncludeDeleted: options.IncludeDeleted, Limit: options.Limit}
	if options.Status != "" {
		if options.Status == StatusSuperseded {
			candidateOptions.Status = extract.StatusDeleted
			candidateOptions.IncludeDeleted = true
		} else {
			candidateOptions.Status = CandidateStatusForDecision(options.Status)
		}
	}
	candidates, err := s.candidates.List(ctx, candidateOptions)
	if err != nil {
		return nil, err
	}
	result := make([]Decision, 0, len(candidates))
	for _, candidate := range candidates {
		decision, err := DecodeDecision(candidate.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode decision %s: %w", candidate.ID, err)
		}
		decision.ID = candidate.ID
		decision.Status = StatusForCandidate(candidate.Status)
		if candidate.Status == extract.StatusDeleted && decision.Supersedes != "" {
			decision.Status = StatusSuperseded
		}
		if options.Status != "" && decision.Status != options.Status {
			continue
		}
		decision.UpdatedAt = candidate.UpdatedAt
		result = append(result, decision)
	}
	return result, nil
}

func (s *Store) ListDecisions(ctx context.Context, options ListOptions) ([]Decision, error) {
	return s.List(ctx, options)
}

// Events returns the append-only generic candidate audit trail.
func (s *Store) Events(ctx context.Context, id string) ([]extract.CandidateEvent, error) {
	if s == nil || s.candidates == nil {
		return nil, errors.New("nil decision store")
	}
	return s.candidates.Events(ctx, id)
}

// Review applies one Git-style action. Approve/reject/defer use the shared
// state machine. Edit creates a replacement candidate and deletes the old one,
// preserving both operations as append-only candidate events rather than
// mutating history.
func (s *Store) Review(ctx context.Context, input ReviewInput) (Decision, error) {
	if s == nil || s.candidates == nil {
		return Decision{}, errors.New("nil decision store")
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return Decision{}, fmt.Errorf("%w: missing decision ID", ErrInvalidReviewAction)
	}
	old, err := s.Get(ctx, id)
	if err != nil {
		return Decision{}, err
	}
	actor := input.Actor
	if strings.TrimSpace(actor) == "" {
		actor = "reviewer"
	}
	reason := input.Reason
	if strings.TrimSpace(reason) == "" {
		reason = string(input.Action)
	}
	switch input.Action {
	case ReviewApprove:
		if err := transitionForReview(ctx, s.candidates, id, extract.StatusApproved, actor, reason); err != nil {
			return Decision{}, err
		}
		return s.Get(ctx, id)
	case ReviewReject:
		if err := transitionForReview(ctx, s.candidates, id, extract.StatusRejected, actor, reason); err != nil {
			return Decision{}, err
		}
		return s.Get(ctx, id)
	case ReviewDefer:
		if err := transitionForReview(ctx, s.candidates, id, extract.StatusDeferred, actor, reason); err != nil {
			return Decision{}, err
		}
		return s.Get(ctx, id)
	case ReviewEdit:
		if !input.Confirmed {
			return Decision{}, ErrConfirmationRequired
		}
		if input.Decision == nil {
			return Decision{}, fmt.Errorf("%w: edit requires decision payload", ErrInvalidReviewAction)
		}
		edited := *input.Decision
		edited.ID = ""
		edited.Status = StatusProposed
		if edited.Provenance.SessionID == "" {
			edited.Provenance = old.Provenance
		}
		if edited.Supersedes == "" {
			edited.Supersedes = old.ID
		}
		messages, err := s.reviewMessages(ctx, old, input.Messages)
		if err != nil {
			return Decision{}, err
		}
		created, err := s.Create(ctx, CreateInput{Decision: edited, Messages: messages, Confirmed: true, Actor: actor, Reason: "edit: " + reason})
		if err != nil {
			return Decision{}, err
		}
		if _, err := s.candidates.Delete(ctx, old.ID, actor, "replaced by edit "+created.ID); err != nil {
			return Decision{}, err
		}
		return created, nil
	default:
		return Decision{}, fmt.Errorf("%w: %q", ErrInvalidReviewAction, input.Action)
	}
}

func (s *Store) reviewMessages(ctx context.Context, old Decision, provided []record.MessageRecord) ([]record.MessageRecord, error) {
	if len(provided) > 0 {
		return provided, nil
	}
	messages, err := loadReviewMessages(ctx, s.path, old.Provenance)
	if err == nil && len(messages) > 0 {
		return messages, nil
	}
	// A store created over a database that has no source-session tables (for
	// example, an isolated candidate database in an API test) can still prove
	// the old evidence. Keep this fallback limited to persisted, validated
	// quotes; edited payloads cannot introduce unverified transcript text.
	fallback := make([]record.MessageRecord, 0, len(old.Evidence))
	for _, evidence := range old.Evidence {
		quote := strings.TrimSpace(evidence.Quote)
		if quote == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(evidence.Role))
		if role != "user" && role != "assistant" {
			role = "assistant"
			if strings.EqualFold(evidence.Kind, EvidenceExplicit) {
				role = "user"
			}
		}
		fallback = append(fallback, record.MessageRecord{
			Tool: old.Provenance.Tool, SessionID: old.Provenance.SessionID,
			SourcePath: old.Provenance.SourcePath, Role: role, Text: evidence.Quote,
		})
	}
	if len(fallback) > 0 {
		return fallback, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: unable to reload original session messages: %v", ErrEvidenceNotFound, err)
	}
	return nil, fmt.Errorf("%w: unable to reload original session messages", ErrEvidenceNotFound)
}

func loadReviewMessages(ctx context.Context, path string, provenance Provenance) ([]record.MessageRecord, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" {
		return nil, errors.New("session database path is unavailable")
	}
	uri := "file:" + strings.ReplaceAll(strings.ReplaceAll(url.PathEscape(path), "%2F", "/"), "%2f", "/") + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	where := "s.session_id = ?"
	args := []any{provenance.SessionID}
	if strings.TrimSpace(provenance.SourcePath) != "" {
		where += " AND s.source_path = ?"
		args = append(args, provenance.SourcePath)
	}
	rows, err := db.QueryContext(ctx, `SELECT s.tool, s.session_id, s.cwd, s.title, s.source_path,
		m.ts, m.role, m.text FROM sessions AS s JOIN messages AS m ON m.session_pk = s.id WHERE `+where+`
		ORDER BY COALESCE(m.ts, 0), m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]record.MessageRecord, 0)
	for rows.Next() {
		var tool, sessionID, cwd, title, sourcePath, role, text string
		var timestamp any
		if err := rows.Scan(&tool, &sessionID, &cwd, &title, &sourcePath, &timestamp, &role, &text); err != nil {
			return nil, err
		}
		messages = append(messages, record.MessageRecord{
			Tool: tool, SessionID: sessionID, CWD: cwd, Title: title,
			Timestamp: timestamp, Role: role, Text: text, SourcePath: sourcePath,
		})
	}
	return messages, rows.Err()
}

func transitionForReview(ctx context.Context, store *extract.Store, id, target, actor, reason string) error {
	candidate, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	if candidate.Status == extract.StatusDetected {
		candidate, err = store.Transition(ctx, id, extract.StatusDraft, actor, "review: draft")
		if err != nil {
			return err
		}
	}
	if candidate.Status == extract.StatusDraft {
		candidate, err = store.Transition(ctx, id, extract.StatusInReview, actor, "review: in_review")
		if err != nil {
			return err
		}
	}
	_, err = store.Transition(ctx, id, target, actor, reason)
	return err
}

func (s *Store) Approve(ctx context.Context, id, actor, reason string) (Decision, error) {
	return s.Review(ctx, ReviewInput{ID: id, Action: ReviewApprove, Actor: actor, Reason: reason})
}
func (s *Store) Reject(ctx context.Context, id, actor, reason string) (Decision, error) {
	return s.Review(ctx, ReviewInput{ID: id, Action: ReviewReject, Actor: actor, Reason: reason})
}
func (s *Store) Defer(ctx context.Context, id, actor, reason string) (Decision, error) {
	return s.Review(ctx, ReviewInput{ID: id, Action: ReviewDefer, Actor: actor, Reason: reason})
}
func (s *Store) Edit(ctx context.Context, id string, decision Decision, messages []record.MessageRecord, actor, reason string) (Decision, error) {
	return s.Review(ctx, ReviewInput{ID: id, Action: ReviewEdit, Decision: &decision, Messages: messages, Actor: actor, Reason: reason, Confirmed: true})
}

// MarshalList emits a stable JSON array for CLI and API callers.
func MarshalList(decisions []Decision) ([]byte, error) {
	if decisions == nil {
		decisions = []Decision{}
	}
	return json.Marshal(decisions)
}
