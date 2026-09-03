package skill

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/BayInl/session-finder/internal/index"
)

func BenchmarkExtractPending(b *testing.B) {
	for _, sessions := range []int{100, 200, 400, 800} {
		b.Run(fmt.Sprintf("distinct=%d", sessions), func(b *testing.B) {
			root := b.TempDir()
			indexPath := filepath.Join(root, "index.db")
			createPendingBenchmarkIndex(b, indexPath, sessions)
			benchmarkPendingIndex(b, root, indexPath, sessions)
		})
	}
	for _, sources := range []int{50, 100, 200, 400} {
		b.Run(fmt.Sprintf("duplicate-sources=%d", sources), func(b *testing.B) {
			root := b.TempDir()
			indexPath := filepath.Join(root, "index.db")
			createDuplicatePendingBenchmarkIndex(b, indexPath, sources)
			benchmarkPendingIndex(b, root, indexPath, sources)
		})
	}
}

func benchmarkPendingIndex(b *testing.B, root, indexPath string, sessions int) {
	b.Helper()
	b.ReportMetric(float64(sessions), "sessions/op")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		candidatePath := filepath.Join(root, fmt.Sprintf("candidates-%d.db", iteration))
		pending, created, err := ExtractPending(context.Background(), ExtractOptions{
			IndexDBPath:     indexPath,
			CandidateDBPath: candidatePath,
			Actor:           "benchmark",
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(pending) != sessions || len(created) != sessions {
			b.Fatalf("pending=%d created=%d, want %d", len(pending), len(created), sessions)
		}
	}
}

func createDuplicatePendingBenchmarkIndex(b *testing.B, path string, sources int) {
	b.Helper()
	db, err := index.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	insertSession, err := tx.Prepare(`INSERT INTO sessions(id, tool, session_id, cwd, title, created, updated, source_path)
		VALUES (?, 'kimi-code', 'parent-session', '', 'Release workflow', ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertSession.Close()
	insertMessage, err := tx.Prepare(`INSERT INTO messages(session_pk, role, ts, text) VALUES (?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertMessage.Close()
	for source := 1; source <= sources; source++ {
		base := float64(1704067200 + source*10)
		if _, err := insertSession.Exec(source, base, base+3, fmt.Sprintf("/tmp/agent-%06d.jsonl", source)); err != nil {
			b.Fatal(err)
		}
		if source == 1 {
			for offset, message := range []struct {
				role string
				text string
			}{
				{role: "user", text: "Document the release workflow."},
				{role: "assistant", text: "Run go test ./...; then build the release artifact."},
				{role: "user", text: "Looks good, approved. go test ./... passed."},
			} {
				if _, err := insertMessage.Exec(source, message.role, base+float64(offset+1), message.text); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func createPendingBenchmarkIndex(b *testing.B, path string, sessions int) {
	b.Helper()
	db, err := index.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	insertSession, err := tx.Prepare(`INSERT INTO sessions(id, tool, session_id, cwd, title, created, updated, source_path)
		VALUES (?, 'codex', ?, '/tmp/project', 'Release workflow', ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertSession.Close()
	insertMessage, err := tx.Prepare(`INSERT INTO messages(session_pk, role, ts, text) VALUES (?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertMessage.Close()
	for session := 1; session <= sessions; session++ {
		sessionID := fmt.Sprintf("session-%06d", session)
		base := float64(1704067200 + session*10)
		if _, err := insertSession.Exec(session, sessionID, base, base+3, "/tmp/"+sessionID+".jsonl"); err != nil {
			b.Fatal(err)
		}
		for offset, message := range []struct {
			role string
			text string
		}{
			{role: "user", text: "Document the release workflow."},
			{role: "assistant", text: "Run go test ./...; then build the release artifact."},
			{role: "user", text: "Looks good, approved. go test ./... passed."},
		} {
			if _, err := insertMessage.Exec(session, message.role, base+float64(offset+1), message.text); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}
