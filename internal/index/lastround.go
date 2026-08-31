package index

import (
	"database/sql"
	"strings"
)

const lastRoundMaxRunes = 2000

func attachLastRounds(db *sql.DB, results []SearchResult) error {
	if db == nil || len(results) == 0 {
		return nil
	}
	var query strings.Builder
	query.WriteString(`SELECT s.tool, s.session_id, m.role, m.text
FROM messages AS m
JOIN sessions AS s ON s.id = m.session_pk
WHERE m.role IN ('user', 'assistant') AND (`)
	args := make([]any, 0, len(results)*2)
	for i, result := range results {
		if i > 0 {
			query.WriteString(" OR ")
		}
		query.WriteString("(s.tool = ? AND s.session_id = ?)")
		args = append(args, result.Tool, result.SessionID)
	}
	query.WriteString(") ORDER BY s.tool, s.session_id, COALESCE(m.ts, 0) DESC, m.id DESC")
	rows, err := db.Query(query.String(), args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	type round struct {
		user, assistant string
	}
	found := map[string]*round{}
	for rows.Next() {
		var tool, sessionID, role string
		var text sql.NullString
		if err := rows.Scan(&tool, &sessionID, &role, &text); err != nil {
			return err
		}
		key := tool + "\x00" + sessionID
		item := found[key]
		if item == nil {
			item = &round{}
			found[key] = item
		}
		body := clipRunes(strings.TrimSpace(text.String), lastRoundMaxRunes)
		if body == "" || isToolNoise(body) {
			continue
		}
		switch role {
		case "user":
			if item.user == "" {
				item.user = body
			}
		case "assistant":
			if item.assistant == "" {
				item.assistant = body
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range results {
		item := found[results[i].Tool+"\x00"+results[i].SessionID]
		if item == nil {
			continue
		}
		results[i].LastUser = item.user
		results[i].LastAssistant = item.assistant
	}
	return nil
}

func isToolNoise(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	prefixes := []string{
		"tool.call", "tool.result", "tool_use", "tool_result",
		"<tool", "{\"type\":\"tool", "{\"name\":",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func clipRunes(value string, max int) string {
	if max <= 0 || value == "" {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
