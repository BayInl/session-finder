package skill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BayInl/session-finder/internal/extract"

	_ "modernc.org/sqlite"
)

// Repository combines the M13 candidate store with skill-specific payload
// updates. Status changes continue to use extract.Store's state machine; bundle
// edits are optimistic and append an audit event in the same SQLite database.
type Repository struct {
	store *extract.Store
	db    *sql.DB
	path  string
}

// Open opens the candidate repository at path. Empty path uses the shared M13
// candidate/index database default.
func Open(path string) (*Repository, error) {
	resolvedPath := path
	if resolvedPath == "" {
		resolvedPath = extract.DefaultDBPath
	}
	db, err := openSQLite(resolvedPath)
	if err != nil {
		return nil, err
	}
	store, err := extract.OpenDB(db, resolvedPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Repository{store: store, db: db, path: resolvedPath}, nil
}

// Close closes the underlying candidate store and its shared database.
func (r *Repository) Close() error {
	if r == nil {
		return nil
	}
	if r.store != nil {
		_ = r.store.Close()
	}
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// Path returns the repository's database path.
func (r *Repository) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// CandidateStore exposes the M13 store for integrations that only need status
// transitions or event inspection.
func (r *Repository) CandidateStore() *extract.Store {
	if r == nil {
		return nil
	}
	return r.store
}

// Get returns one candidate.
func (r *Repository) Get(ctx context.Context, id string) (extract.Candidate, error) {
	if r == nil || r.store == nil {
		return extract.Candidate{}, errors.New("nil skill repository")
	}
	return r.store.Get(ctx, id)
}

// List returns skill candidates in stable order.
func (r *Repository) List(ctx context.Context, options extract.ListOptions) ([]extract.Candidate, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("nil skill repository")
	}
	if options.Kind == "" {
		options.Kind = defaultCandidateKind
	}
	return r.store.List(ctx, options)
}

// GetBundle returns the persisted candidate and decoded skill bundle.
func (r *Repository) GetBundle(ctx context.Context, id string) (extract.Candidate, CandidateBundle, error) {
	candidate, err := r.Get(ctx, id)
	if err != nil {
		return extract.Candidate{}, CandidateBundle{}, err
	}
	bundle, err := BundleFromCandidate(candidate)
	if err != nil {
		return candidate, CandidateBundle{}, err
	}
	return candidate, bundle, nil
}

// CreateBundle appends a bundle candidate using the M13 audit store.
func (r *Repository) CreateBundle(ctx context.Context, bundle CandidateBundle, actor, reason string) (extract.Candidate, error) {
	if r == nil || r.store == nil {
		return extract.Candidate{}, errors.New("nil skill repository")
	}
	bundle = normalizeBundle(bundle)
	if err := ValidateSlug(bundle.Slug); err != nil {
		return extract.Candidate{}, err
	}
	payload, err := CandidatePayload(bundle)
	if err != nil {
		return extract.Candidate{}, err
	}
	status := extract.StatusDraft
	if IsSuppressed(bundle.Quality) {
		status = extract.StatusRejected
	}
	return r.store.Create(ctx, extract.CandidateInput{
		SessionID:         bundle.SessionID,
		Tool:              bundle.Tool,
		SourcePath:        bundle.SourcePath,
		Kind:              defaultCandidateKind,
		Title:             bundle.Title,
		Summary:           bundle.Trigger,
		Payload:           payload,
		Status:            status,
		Actor:             actor,
		Reason:            reason,
		Confidence:        bundle.Quality.Signals.Confidence,
		SuccessEvidence:   bundle.Quality.Signals.SuccessEvidence,
		OneOffRisk:        bundle.Quality.Signals.OneOffRisk,
		SecretRisk:        bundle.Quality.Signals.SecretRisk,
		RecommendedAction: bundle.Quality.Signals.RecommendedAction,
	})
}

// Transition delegates to the shared candidate state machine.
func (r *Repository) Transition(ctx context.Context, id, status, actor, reason string) (extract.Candidate, error) {
	if r == nil || r.store == nil {
		return extract.Candidate{}, errors.New("nil skill repository")
	}
	return r.store.Transition(ctx, id, status, actor, reason)
}

// Delete delegates to the recoverable M13 delete operation.
func (r *Repository) Delete(ctx context.Context, id, actor, reason string) (extract.Candidate, error) {
	return r.store.Delete(ctx, id, actor, reason)
}

// Disable transitions a candidate to disabled.
func (r *Repository) Disable(ctx context.Context, id, actor, reason string) (extract.Candidate, error) {
	return r.Transition(ctx, id, extract.StatusDisabled, actor, reason)
}

// Review applies a human decision to one candidate/evidence block.
func (r *Repository) Review(ctx context.Context, id string, request ReviewRequest) (ReviewResult, error) {
	if r == nil || r.store == nil {
		return ReviewResult{}, errors.New("nil skill repository")
	}
	candidate, bundle, err := r.GetBundle(ctx, id)
	if err != nil {
		return ReviewResult{}, err
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if !isReviewAction(request.Action) {
		return ReviewResult{}, fmt.Errorf("%w: %s", ErrReviewAction, request.Action)
	}
	bundle = normalizeBundle(bundle)
	if request.EvidenceID != "" {
		if !applyEvidenceReview(&bundle, request) {
			return ReviewResult{}, fmt.Errorf("%w: evidence %s not found", ErrReviewAction, request.EvidenceID)
		}
	} else if request.Action != ReviewEdit {
		// Bundle-level approve/reject/defer is allowed for callers that do not
		// render evidence UI; edit can update the whole bundle directly.
		if request.Action == ReviewSplit {
			return r.reviewSplit(ctx, candidate, bundle, request)
		}
	}
	if request.Action == ReviewEdit {
		if request.Slug != "" {
			bundle.Slug = strings.TrimSpace(request.Slug)
		}
		if request.Trigger != "" {
			bundle.Trigger = strings.TrimSpace(request.Trigger)
		}
		if request.Instructions != "" {
			bundle.Instructions = strings.TrimSpace(request.Instructions)
		}
		if err := ValidateSlug(bundle.Slug); err != nil {
			return ReviewResult{}, err
		}
	}
	if request.Action == ReviewSplit {
		return r.reviewSplit(ctx, candidate, bundle, request)
	}
	if err := validateReviewedBundle(bundle, request.Action); err != nil {
		return ReviewResult{}, err
	}
	if request.EvidenceID != "" || request.Action == ReviewEdit {
		updated, err := r.updateBundle(ctx, candidate, bundle, defaultActor, "review "+request.Action)
		if err != nil {
			return ReviewResult{}, err
		}
		candidate = updated
	}
	target := ""
	switch request.Action {
	case ReviewApprove:
		target = extract.StatusApproved
	case ReviewReject:
		target = extract.StatusRejected
	case ReviewDefer:
		target = extract.StatusDeferred
	case ReviewEdit:
		// Editing is non-terminal and preserves the current status.
		return ReviewResult{Bundle: bundle, Candidate: candidate, Action: request.Action}, nil
	}
	candidate, err = r.transitionToReviewStatus(ctx, candidate, target, defaultActor, "review "+request.Action)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Bundle: bundle, Candidate: candidate, Action: request.Action}, nil
}

func isReviewAction(action string) bool {
	switch action {
	case ReviewApprove, ReviewReject, ReviewDefer, ReviewEdit, ReviewSplit:
		return true
	default:
		return false
	}
}

func applyEvidenceReview(bundle *CandidateBundle, request ReviewRequest) bool {
	for i := range bundle.Evidence {
		if bundle.Evidence[i].ID != request.EvidenceID {
			continue
		}
		switch request.Action {
		case ReviewApprove, ReviewReject, ReviewDefer, ReviewSplit:
			bundle.Evidence[i].Decision = request.Action
		case ReviewEdit:
			bundle.Evidence[i].ReviewerNote = strings.TrimSpace(request.ReviewerNote)
		}
		return true
	}
	return false
}

func validateReviewedBundle(bundle CandidateBundle, action string) error {
	if action == ReviewApprove {
		if err := ValidateQualityForPublish(bundle); err != nil {
			return err
		}
	}
	if action == ReviewEdit {
		if ContainsSensitiveInformation(bundle) {
			return ErrSensitiveContent
		}
	}
	return nil
}

func (r *Repository) transitionToReviewStatus(ctx context.Context, candidate extract.Candidate, target, actor, reason string) (extract.Candidate, error) {
	if candidate.Status == target {
		return candidate, nil
	}
	var err error
	if candidate.Status == extract.StatusDetected || candidate.Status == extract.StatusRejected {
		// rejected -> draft is the only state-machine edge back into the
		// reviewable path. Do not attempt the nonexistent rejected -> in_review
		// transition; suppressed candidates must remain manually recoverable.
		candidate, err = r.Transition(ctx, candidate.ID, extract.StatusDraft, actor, "prepare review")
		if err != nil {
			return extract.Candidate{}, err
		}
	}
	if candidate.Status == extract.StatusDraft || candidate.Status == extract.StatusDeferred || candidate.Status == extract.StatusFailed {
		candidate, err = r.Transition(ctx, candidate.ID, extract.StatusInReview, actor, "open review")
		if err != nil {
			return extract.Candidate{}, err
		}
	}
	if candidate.Status != extract.StatusInReview {
		return extract.Candidate{}, fmt.Errorf("%w: candidate status %q cannot reach %q", extract.ErrInvalidTransition, candidate.Status, target)
	}
	return r.Transition(ctx, candidate.ID, target, actor, reason)
}

func (r *Repository) reviewSplit(ctx context.Context, candidate extract.Candidate, bundle CandidateBundle, request ReviewRequest) (ReviewResult, error) {
	if candidate.Status == extract.StatusPublished || candidate.Status == extract.StatusDeleted {
		return ReviewResult{}, fmt.Errorf("%w: cannot split status %q", extract.ErrInvalidTransition, candidate.Status)
	}
	left, right, err := SplitBundle(bundle, request.EvidenceID)
	if err != nil {
		return ReviewResult{}, err
	}
	updated, err := r.updateBundle(ctx, candidate, left, defaultActor, "review split")
	if err != nil {
		return ReviewResult{}, err
	}
	if updated.Status != extract.StatusDeferred {
		updated, err = r.transitionToReviewStatus(ctx, updated, extract.StatusDeferred, defaultActor, "review split")
		if err != nil {
			return ReviewResult{}, err
		}
	}
	right.Slug = uniqueSplitSlug(right.Slug)
	child, err := r.CreateBundle(ctx, right, defaultActor, "split from "+candidate.ID)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Bundle: left, Candidate: updated, Action: ReviewSplit, SplitCandidate: &child, SplitBundle: &right}, nil
}

func uniqueSplitSlug(slug string) string {
	if slug == "" {
		return "split-skill"
	}
	base := slug + "-part-2"
	if len(base) <= 64 {
		return base
	}
	limit := 64 - len("-part-2")
	if limit < 1 {
		limit = 1
	}
	return strings.Trim(slug[:limit], "-") + "-part-2"
}

// SplitBundle separates one selected evidence block from the remaining blocks.
// Instructions remain the same in this MVP; reviewers can edit each child.
func SplitBundle(bundle CandidateBundle, evidenceID string) (CandidateBundle, CandidateBundle, error) {
	bundle = normalizeBundle(bundle)
	if len(bundle.Evidence) < 2 {
		return CandidateBundle{}, CandidateBundle{}, fmt.Errorf("%w: split requires at least two evidence blocks", ErrReviewAction)
	}
	if evidenceID == "" {
		evidenceID = bundle.Evidence[0].ID
	}
	selected := -1
	for i, evidence := range bundle.Evidence {
		if evidence.ID == evidenceID {
			selected = i
			break
		}
	}
	if selected < 0 {
		return CandidateBundle{}, CandidateBundle{}, fmt.Errorf("%w: evidence %s not found", ErrReviewAction, evidenceID)
	}
	left := bundle
	left.Evidence = append([]EvidenceBlock(nil), bundle.Evidence[:selected]...)
	left.Evidence = append(left.Evidence, bundle.Evidence[selected+1:]...)
	right := bundle
	right.Evidence = []EvidenceBlock{bundle.Evidence[selected]}
	return normalizeBundle(left), normalizeBundle(right), nil
}

// ReviewCandidate opens a repository, applies a review, and closes it.
func ReviewCandidate(ctx context.Context, path, id string, request ReviewRequest) (ReviewResult, error) {
	repository, err := Open(path)
	if err != nil {
		return ReviewResult{}, err
	}
	defer repository.Close()
	return repository.Review(ctx, id, request)
}

// PublishCandidate publishes an approved candidate and records published state.
func PublishCandidate(ctx context.Context, path, id string, options PublishOptions) (PublishResult, error) {
	repository, err := Open(path)
	if err != nil {
		return PublishResult{}, err
	}
	defer repository.Close()
	candidate, bundle, err := repository.GetBundle(ctx, id)
	if err != nil {
		return PublishResult{}, err
	}
	if candidate.Status != extract.StatusApproved {
		return PublishResult{}, fmt.Errorf("%w: candidate status %q must be approved", extract.ErrInvalidTransition, candidate.Status)
	}
	return publishAndTransition(bundle, options, func(publishedPath string) error {
		_, err := repository.Transition(ctx, id, extract.StatusPublished, defaultActor, "published "+publishedPath)
		return err
	})
}

// publishAndTransition removes the newly written skill when recording its
// published state fails, so retrying cannot be blocked by an orphan directory.
func publishAndTransition(bundle CandidateBundle, options PublishOptions, transition func(string) error) (PublishResult, error) {
	result, err := Publish(bundle, options)
	if err != nil {
		return result, err
	}
	if transitionErr := transition(result.Path); transitionErr != nil {
		if cleanupErr := os.RemoveAll(result.Path); cleanupErr != nil {
			return PublishResult{}, fmt.Errorf("publish transition failed: %w; cleanup failed: %v", transitionErr, cleanupErr)
		}
		return PublishResult{}, transitionErr
	}
	return result, nil
}

// DisableCandidate disables one candidate.
func DisableCandidate(ctx context.Context, path, id string) (extract.Candidate, error) {
	repository, err := Open(path)
	if err != nil {
		return extract.Candidate{}, err
	}
	defer repository.Close()
	return repository.Disable(ctx, id, defaultActor, "disabled by user")
}

// DeleteCandidate deletes one candidate recoverably.
func DeleteCandidate(ctx context.Context, path, id string) (extract.Candidate, error) {
	repository, err := Open(path)
	if err != nil {
		return extract.Candidate{}, err
	}
	defer repository.Close()
	return repository.Delete(ctx, id, defaultActor, "deleted by user")
}

// ListCandidateBundles returns candidates and decoded bundle payloads, skipping
// malformed payloads only when includeInvalid is false.
func ListCandidateBundles(ctx context.Context, path string, includeInvalid bool) ([]extract.Candidate, []CandidateBundle, error) {
	repository, err := Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer repository.Close()
	candidates, err := repository.List(ctx, extract.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	resultCandidates := make([]extract.Candidate, 0, len(candidates))
	bundles := make([]CandidateBundle, 0, len(candidates))
	for _, candidate := range candidates {
		bundle, decodeErr := BundleFromCandidate(candidate)
		if decodeErr != nil {
			if includeInvalid {
				resultCandidates = append(resultCandidates, candidate)
				bundles = append(bundles, CandidateBundle{})
				continue
			}
			return nil, nil, decodeErr
		}
		resultCandidates = append(resultCandidates, candidate)
		bundles = append(bundles, bundle)
	}
	return resultCandidates, bundles, nil
}

func (r *Repository) updateBundle(ctx context.Context, candidate extract.Candidate, bundle CandidateBundle, actor, reason string) (extract.Candidate, error) {
	bundle = normalizeBundle(bundle)
	payload, err := CandidatePayload(bundle)
	if err != nil {
		return extract.Candidate{}, err
	}
	if candidate.Status == extract.StatusDeleted {
		return extract.Candidate{}, extract.ErrCandidateDeleted
	}
	if r == nil || r.db == nil {
		return extract.Candidate{}, errors.New("nil skill repository")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return extract.Candidate{}, err
	}
	rollback := func(err error) (extract.Candidate, error) {
		_ = tx.Rollback()
		return extract.Candidate{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	successEvidence := append([]string{}, bundle.Quality.Signals.SuccessEvidence...)
	successEvidenceJSON, err := json.Marshal(successEvidence)
	if err != nil {
		return rollback(err)
	}
	updated := candidate
	updated.Title = bundle.Title
	updated.Summary = bundle.Trigger
	updated.Confidence = bundle.Quality.Signals.Confidence
	updated.SuccessEvidence = successEvidence
	updated.OneOffRisk = bundle.Quality.Signals.OneOffRisk
	updated.SecretRisk = bundle.Quality.Signals.SecretRisk
	updated.RecommendedAction = bundle.Quality.Signals.RecommendedAction
	updated.Payload = json.RawMessage(payload)
	updated.UpdatedAt = now
	updated.Version++
	result, err := tx.ExecContext(ctx, `UPDATE candidates SET title = ?, summary = ?, confidence = ?, success_evidence = ?, one_off_risk = ?, secret_risk = ?, recommended_action = ?, payload = ?, updated_at = ?, version = ? WHERE id = ? AND version = ?`,
		updated.Title, updated.Summary, updated.Confidence, string(successEvidenceJSON), updated.OneOffRisk,
		updated.SecretRisk, updated.RecommendedAction, string(updated.Payload), updated.UpdatedAt,
		updated.Version, updated.ID, candidate.Version)
	if err != nil {
		return rollback(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return rollback(err)
		}
		return rollback(errors.New("candidate changed concurrently"))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_events
		(candidate_id, session_id, action, actor, reason, timestamp, before_hash, after_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, updated.ID, updated.SessionID, extract.ActionTransition,
		actor, reason, updated.UpdatedAt, extract.CandidateHashForAudit(candidate), extract.CandidateHashForAudit(updated)); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return extract.Candidate{}, err
	}
	return updated, nil
}

func openSQLite(path string) (*sql.DB, error) {
	if path == "" {
		path = extract.DefaultDBPath
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func sqliteURI(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return path
	}
	escaped := url.PathEscape(path)
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	escaped = strings.ReplaceAll(escaped, "%2f", "/")
	return "file:" + escaped
}

// Ensure JSON remains valid if callers hand-build a CandidateBundle.
var _ = json.Valid
