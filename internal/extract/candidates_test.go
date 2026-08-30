package extract

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenDBConfiguresSQLitePragmas(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenDB(db, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections = %d, want 1", got)
	}
	var foreignKeys, busyTimeout int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 {
		t.Fatalf("pragmas = foreign_keys:%d busy_timeout:%d, want 1 and 5000", foreignKeys, busyTimeout)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("caller-owned database was closed: %v", err)
	}
}

func TestStatusMachine(t *testing.T) {
	valid := [][2]string{
		{StatusDetected, StatusDraft},
		{StatusDetected, StatusDisabled},
		{StatusDraft, StatusInReview},
		{StatusInReview, StatusApproved},
		{StatusApproved, StatusPublished},
		{StatusInReview, StatusRejected},
		{StatusInReview, StatusDeferred},
		{StatusInReview, StatusFailed},
		{StatusPublished, StatusDisabled},
		{StatusDraft, StatusDeleted},
	}
	for _, pair := range valid {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = false", pair[0], pair[1])
		}
	}
	invalid := [][2]string{
		{StatusDetected, StatusInReview},
		{StatusDraft, StatusApproved},
		{StatusInReview, StatusPublished},
		{StatusPublished, StatusDraft},
		{StatusDeleted, StatusDraft},
		{"unknown", StatusDraft},
	}
	for _, pair := range invalid {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = true", pair[0], pair[1])
		}
		if err := ValidateTransition(pair[0], pair[1]); !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("ValidateTransition(%q, %q) = %v", pair[0], pair[1], err)
		}
	}
}

func TestCreateTransitionAuditAndRecoverableTrash(t *testing.T) {
	store := testStore(t)
	clock := time.Date(2026, time.August, 31, 1, 2, 3, 0, time.UTC)
	store.SetNowForTesting(func() time.Time { return clock })
	ctx := context.Background()
	candidate, err := store.Create(ctx, CandidateInput{
		ID: "candidate-1", SessionID: "session-1", Tool: "codex", Kind: "decision",
		Title: "Use SQLite", Summary: "Prefer local storage", Payload: []byte(`{"choice":"sqlite"}`),
		Actor: "extractor", Reason: "heuristic signal", Confidence: 0.9,
		SuccessEvidence: []string{"test passed"}, OneOffRisk: 0.2, SecretRisk: 0.1,
		RecommendedAction: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != StatusDetected || candidate.Version != 1 {
		t.Fatalf("created candidate = %+v", candidate)
	}
	candidate, err = store.Transition(ctx, candidate.ID, StatusDraft, "reviewer", "candidate has reusable context")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != StatusDraft || candidate.Version != 2 {
		t.Fatalf("draft candidate = %+v", candidate)
	}
	events, err := store.Events(ctx, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Action != ActionCreate || events[1].Action != ActionTransition {
		t.Fatalf("events = %+v", events)
	}
	if events[0].BeforeHash != "" || events[0].AfterHash == "" || events[1].BeforeHash == "" || events[1].AfterHash == "" {
		t.Fatalf("event hashes = %+v", events)
	}
	if events[0].Timestamp != timestamp(clock) || events[1].Timestamp != timestamp(clock) {
		t.Fatalf("event timestamps = %+v", events)
	}
	if events[0].AfterHash != Hash(candidateFromStatus(t, candidate, StatusDetected)) {
		t.Fatal("create after hash does not match candidate snapshot")
	}

	deleted, err := store.Delete(ctx, candidate.ID, "reviewer", "remove duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != StatusDeleted || deleted.DeletedAt == "" {
		t.Fatalf("deleted candidate = %+v", deleted)
	}
	trash, err := store.Trash(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].ID != candidate.ID {
		t.Fatalf("trash = %+v", trash)
	}
	listed, err := store.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("default list includes deleted: %+v", listed)
	}
	restored, err := store.Restore(ctx, candidate.ID, "reviewer", "undo duplicate removal")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != StatusDraft || restored.DeletedAt != "" {
		t.Fatalf("restored candidate = %+v", restored)
	}
	if _, err := store.Restore(ctx, candidate.ID, "reviewer", "again"); !errors.Is(err, ErrCandidateNotDelete) {
		t.Fatalf("second restore error = %v", err)
	}
	events, err = store.Events(ctx, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[2].Action != ActionDelete || events[3].Action != ActionRestore {
		t.Fatalf("final events = %+v", events)
	}
}

func candidateFromStatus(t *testing.T, candidate Candidate, status string) Candidate {
	t.Helper()
	candidate.Status = status
	candidate.Version--
	candidate.UpdatedAt = candidate.CreatedAt
	return candidate
}

func TestCreateRejectsInvalidPayloadAndStatus(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, CandidateInput{ID: "bad-payload", Payload: []byte("not-json")}); err == nil {
		t.Fatal("invalid payload accepted")
	}
	if _, err := store.Create(ctx, CandidateInput{ID: "bad-status", Status: StatusDeleted}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("deleted create error = %v", err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	candidate, err := store.Create(ctx, CandidateInput{ID: "candidate-events", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, candidate.ID, StatusInReview, "bad", "skip"); err == nil {
		t.Fatal("invalid transition accepted")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM candidate_events WHERE candidate_id = ?", candidate.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("event count after failed transition = %d, want 1", count)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM candidate_events WHERE candidate_id = ?", candidate.ID); err == nil {
		t.Fatal("append-only audit delete unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE candidate_events SET reason = 'tampered' WHERE candidate_id = ?", candidate.ID); err == nil {
		t.Fatal("append-only audit update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO candidate_events(candidate_id, action, timestamp, after_hash) VALUES (?, 'bad', 'now', '')", candidate.ID); err == nil {
		t.Fatal("empty audit hash unexpectedly accepted")
	}
}

func TestRestoreRequiresTrashSnapshot(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	candidate, err := store.Create(ctx, CandidateInput{ID: "candidate-no-trash", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, candidate.ID, "test", "delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM candidate_trash WHERE candidate_id = ?", candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, candidate.ID, "test", "restore"); !errors.Is(err, ErrCandidateNotDelete) {
		t.Fatalf("restore without snapshot error = %v", err)
	}
}
