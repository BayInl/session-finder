package tui

import (
	"database/sql"
	"strings"

	"github.com/BayInl/session-finder/internal/index"
)

type dbBackend struct {
	db            *sql.DB
	cwd           string
	after         string
	includeSystem bool
}

func newDBBackend(db *sql.DB, cfg Config) Backend {
	return dbBackend{db: db, cwd: cfg.CWD, after: cfg.After, includeSystem: cfg.IncludeSystem}
}

func (b dbBackend) Search(query, tool string, limit int) ([]index.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return listRecent(b.db, tool, b.cwd, b.after, limit)
	}
	return index.Search(b.db, query, tool, b.cwd, b.after, limit, b.includeSystem)
}

func (b dbBackend) Show(sessionID string) ([]index.ShowRow, error) {
	return index.Show(b.db, sessionID, "", 0)
}

func listRecent(db *sql.DB, tool, cwd, after string, limit int) ([]index.SearchResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	where := make([]string, 0, 3)
	params := make([]any, 0, 4)
	if tool != "" {
		where = append(where, "s.tool = ?")
		params = append(params, tool)
	}
	if cwd != "" {
		where = append(where, `s.cwd LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escapeLike(cwd)+"%")
	}
	if after != "" {
		epoch, ok := index.TimestampEpoch(after)
		if !ok || epoch == nil {
			return nil, errAfterFormat
		}
		where = append(where, "s.updated IS NOT NULL AND s.updated >= ?")
		params = append(params, *epoch)
	}
	query := `SELECT s.tool, s.session_id, s.title, s.cwd, s.created, s.updated, s.source_path,
		(SELECT COUNT(*) FROM messages m WHERE m.session_pk = s.id) AS message_count
		FROM sessions s`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY COALESCE(s.updated, s.created, 0) DESC, s.tool, s.session_id LIMIT ?"
	params = append(params, limit)
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]index.SearchResult, 0, limit)
	for rows.Next() {
		var toolName, sessionID, title, cwdValue, sourcePath string
		var created, updated any
		var messageCount int
		if err := rows.Scan(&toolName, &sessionID, &title, &cwdValue, &created, &updated, &sourcePath, &messageCount); err != nil {
			return nil, err
		}
		paths := []string{}
		if sourcePath != "" {
			paths = []string{sourcePath}
		}
		results = append(results, index.SearchResult{
			Tool: toolName, SessionID: sessionID, Title: title, CWD: cwdValue,
			Created: index.FormatTimestamp(created), Updated: index.FormatTimestamp(updated),
			MessageCount: messageCount, Snippets: []string{}, SourcePaths: paths,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := index.AttachLastRounds(db, results); err != nil {
		return nil, err
	}
	return results, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

type afterError string

func (e afterError) Error() string { return string(e) }

const errAfterFormat afterError = "after must be YYYY-MM-DD"
