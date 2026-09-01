package extract

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
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

func TestReplaceIsAtomic(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	old, err := store.Create(ctx, CandidateInput{ID: "old", SessionID: "s", Kind: "decision"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CandidateInput{ID: "duplicate", SessionID: "s", Kind: "decision"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(ctx, old.ID, CandidateInput{ID: "duplicate", SessionID: "s", Kind: "decision"}, "reviewer", "edit"); err == nil {
		t.Fatal("replace with duplicate ID succeeded")
	}
	current, err := store.Get(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusDetected || current.Version != old.Version {
		t.Fatalf("old candidate changed after failed replace: %+v", current)
	}
	events, err := store.Events(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != ActionCreate {
		t.Fatalf("old events after failed replace = %+v", events)
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

func TestInitializeSchemaMigratesLegacyTrash(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE candidates (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL DEFAULT '',
			source_path TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'detected',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL DEFAULT 0, success_evidence TEXT NOT NULL DEFAULT '[]',
			one_off_risk REAL NOT NULL DEFAULT 0, secret_risk REAL NOT NULL DEFAULT 0,
			recommended_action TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1
		);
		CREATE TABLE candidate_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL REFERENCES candidates(id) ON DELETE RESTRICT,
			session_id TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '', timestamp TEXT NOT NULL, before_hash TEXT NOT NULL DEFAULT '', after_hash TEXT NOT NULL
		);
		CREATE TABLE candidate_trash (
			candidate_id TEXT PRIMARY KEY REFERENCES candidates(id) ON DELETE RESTRICT,
			session_id TEXT NOT NULL DEFAULT '', deleted_at TEXT NOT NULL,
			previous_status TEXT NOT NULL, snapshot TEXT NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	const snapshot = `{"id":"legacy","session_id":"session","payload":{},"status":"draft","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","success_evidence":[],"version":1}`
	if _, err := db.ExecContext(ctx, `INSERT INTO candidates(id, session_id, kind, payload, status, created_at, updated_at, deleted_at, version)
		VALUES ('legacy', 'session', 'decision', '{}', 'deleted', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 2),
		('replacement', 'session', 'decision', '{"supersedes":"legacy"}', 'detected', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO candidate_events(candidate_id, action, reason, timestamp, after_hash)
		VALUES ('legacy', 'delete', 'replaced by edit replacement', '2026-01-02T00:00:00Z', 'hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO candidate_trash(candidate_id, session_id, deleted_at, previous_status, snapshot)
		VALUES ('legacy', 'session', '2026-01-02T00:00:00Z', 'draft', ?)`, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := InitializeSchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	rows, err := db.QueryContext(ctx, "PRAGMA table_info(candidate_trash)")
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"candidate_id", "previous_status", "snapshot"}; !reflect.DeepEqual(columns, want) {
		t.Fatalf("candidate_trash columns = %v, want %v", columns, want)
	}
	store, err := OpenDB(db, "legacy.db")
	if err != nil {
		t.Fatal(err)
	}
	isSuperseded, err := store.IsSuperseded(ctx, "decision", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !isSuperseded {
		t.Fatal("legacy supersession was not backfilled")
	}
	restored, err := store.Restore(ctx, "legacy", "test", "restore migrated snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != StatusDraft || restored.SessionID != "session" {
		t.Fatalf("restored candidate = %+v", restored)
	}
}
