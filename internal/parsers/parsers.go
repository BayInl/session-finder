// Package parsers discovers local AI session stores and emits normalized records.
package parsers

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/BayInl/session-finder/internal/record"
)

var (
	uuidRE       = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	workspaceRE  = regexp.MustCompile(`(?:Workspace Path|工作区路径):\s*(.+)`)
	codexJSONLRE = regexp.MustCompile(`"type"\s*:\s*"(?:message|session_meta)"`)
	kimiJSONLRE  = regexp.MustCompile(`"type"\s*:\s*"context\.append_message"`)
)

// Emit receives one normalized message. Returning an error stops parsing.
type Emit func(record.MessageRecord) error

// Discover returns existing sources in deterministic tool order.
func Discover() ([]record.SourceSpec, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var specs []record.SourceSpec

	opencode := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if isFile(opencode) {
		specs = append(specs, record.SourceSpec{Tool: "opencode", Path: opencode})
	}

	grokRoot := filepath.Join(home, ".grok", "sessions")
	for _, path := range matchingFiles(grokRoot, func(path string, d os.DirEntry) bool {
		return d.Name() == "chat_history.jsonl"
	}) {
		summary := filepath.Join(filepath.Dir(path), "summary.json")
		aux := []string{}
		if isFile(summary) {
			aux = append(aux, summary)
		}
		specs = append(specs, record.SourceSpec{Tool: "grok", Path: path, AuxiliaryPath: aux})
	}

	codexRoot := filepath.Join(home, ".codex", "sessions")
	for _, path := range matchingFiles(codexRoot, func(path string, d os.DirEntry) bool {
		return strings.HasSuffix(d.Name(), ".jsonl")
	}) {
		specs = append(specs, record.SourceSpec{Tool: "codex", Path: path})
	}

	kimiRoot := filepath.Join(home, ".kimi-code", "sessions")
	for _, path := range matchingFiles(kimiRoot, func(path string, d os.DirEntry) bool {
		if d.Name() != "wire.jsonl" {
			return false
		}
		agentsDir := filepath.Dir(filepath.Dir(path))
		sessionDir := filepath.Dir(agentsDir)
		return filepath.Base(agentsDir) == "agents" && strings.HasPrefix(filepath.Base(sessionDir), "session_")
	}) {
		specs = append(specs, record.SourceSpec{Tool: "kimi-code", Path: path})
	}

	claudeRoot := filepath.Join(home, ".claude", "projects")
	var claude []string
	if entries, readErr := os.ReadDir(claudeRoot); readErr == nil {
		for _, project := range entries {
			if !project.IsDir() {
				continue
			}
			files, _ := filepath.Glob(filepath.Join(claudeRoot, project.Name(), "*.jsonl"))
			claude = append(claude, files...)
		}
	}
	sort.Strings(claude)
	for _, path := range claude {
		if isFile(path) {
			specs = append(specs, record.SourceSpec{Tool: "claude", Path: path})
		}
	}
	return specs, nil
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func matchingFiles(root string, predicate func(string, os.DirEntry) bool) []string {
	var paths []string
	if _, err := os.Stat(root); err != nil {
		return paths
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if predicate(path, d) && isFile(path) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

// Parse dispatches a source specification to the corresponding parser.
func Parse(spec record.SourceSpec, emit Emit) error {
	switch spec.Tool {
	case "opencode":
		return Opencode(spec.Path, "", emit)
	case "grok":
		var summary string
		if len(spec.AuxiliaryPath) > 0 {
			summary = spec.AuxiliaryPath[0]
		}
		return Grok(spec.Path, summary, emit)
	case "codex":
		return Codex(spec.Path, emit)
	case "kimi-code":
		return Kimi(spec.Path, emit)
	case "claude":
		return Claude(spec.Path, emit)
	default:
		return fmt.Errorf("unknown tool %q", spec.Tool)
	}
}

// ExtractText extracts textual content from strings and common message blocks.
func ExtractText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, item := range value {
			switch item := item.(type) {
			case string:
				if item != "" {
					parts = append(parts, item)
				}
			case map[string]any:
				if text, ok := item["text"].(string); ok {
					if text != "" {
						parts = append(parts, text)
					}
				} else if nested, ok := item["content"]; ok {
					if text := ExtractText(nested); text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text, ok := value["text"].(string); ok {
			return text
		}
		if content, ok := value["content"]; ok {
			return ExtractText(content)
		}
	}
	return ""
}

// IsNoise identifies known injected/system noise records.
func IsNoise(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, prefix := range record.NoisePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// NormalizeRole maps source roles to user/assistant/system.
func NormalizeRole(role any, text string) string {
	if IsNoise(text) {
		return "system"
	}
	normalized := strings.ToLower(anyString(role))
	if normalized == "user" || normalized == "assistant" {
		return normalized
	}
	return "system"
}

// TitleFromText creates a compact fallback title from a user message.
func TitleFromText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	first := strings.Split(trimmed, "\n")[0]
	first = strings.Join(strings.Fields(first), " ")
	runes := []rune(first)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func anyString(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func makeRecord(tool string, sessionID, cwd, title, timestamp, role any, text, sourcePath string) (record.MessageRecord, bool) {
	if text == "" {
		text = anyString(text)
	}
	if text == "" {
		return record.MessageRecord{}, false
	}
	session := strings.TrimSpace(anyString(sessionID))
	if session == "" {
		return record.MessageRecord{}, false
	}
	return record.MessageRecord{
		Tool:       tool,
		SessionID:  session,
		CWD:        anyString(cwd),
		Title:      anyString(title),
		Timestamp:  timestamp,
		Role:       NormalizeRole(role, text),
		Text:       text,
		SourcePath: sourcePath,
	}, true
}

func fileURI(path string, readOnly bool) string {
	// SQLite file URIs use a slash-preserving escaped absolute path.
	escaped := url.PathEscape(path)
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	escaped = strings.ReplaceAll(escaped, "%2f", "/")
	uri := "file:" + escaped
	if readOnly {
		uri += "?mode=ro"
	}
	return uri
}

// Opencode streams text parts from an opencode SQLite store. If sessionID is
// non-empty, only that session's messages are emitted.
func Opencode(dbPath, sessionID string, emit Emit) error {
	db, err := sql.Open("sqlite", fileURI(dbPath, true))
	if err != nil {
		return err
	}
	defer db.Close()
	filter := ""
	args := []any{}
	if sessionID != "" {
		filter = " AND s.id = ?"
		args = append(args, sessionID)
	}
	query := `
		SELECT
			s.id AS session_id,
			s.directory AS cwd,
			s.title AS title,
			m.id AS message_id,
			m.time_created AS message_created,
			json_extract(m.data, '$.role') AS role,
			p.time_created AS part_created,
			json_extract(p.data, '$.text') AS text
		FROM session AS s
		JOIN message AS m ON m.session_id = s.id
		JOIN part AS p ON p.message_id = m.id
		WHERE json_extract(p.data, '$.text') IS NOT NULL` + filter + `
		ORDER BY s.id, m.time_created, m.id, p.time_created, p.id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var currentKey string
	var currentSession, currentCWD, currentTitle, currentRole string
	var messageCreated, partCreated any
	var textParts []string
	flush := func() error {
		if currentKey == "" {
			return nil
		}
		timestamp := messageCreated
		if isEmptyValue(timestamp) {
			timestamp = partCreated
		}
		value, ok := makeRecord("opencode", currentSession, currentCWD, currentTitle, timestamp, currentRole, strings.Join(textParts, "\n"), dbPath)
		if !ok {
			return nil
		}
		return emit(value)
	}
	for rows.Next() {
		var sid, cwd, title, messageID, role, text sql.NullString
		var msgCreated, pCreated any
		if err := rows.Scan(&sid, &cwd, &title, &messageID, &msgCreated, &role, &pCreated, &text); err != nil {
			return err
		}
		key := sid.String + "\x00" + messageID.String
		if currentKey != "" && key != currentKey {
			if err := flush(); err != nil {
				return err
			}
			textParts = nil
		}
		if key != currentKey {
			currentKey = key
			currentSession = sid.String
			currentCWD = cwd.String
			currentTitle = title.String
			currentRole = role.String
			messageCreated = msgCreated
			partCreated = pCreated
		}
		if text.Valid {
			textParts = append(textParts, text.String)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	switch value := value.(type) {
	case string:
		return value == ""
	case []byte:
		return len(value) == 0
	case bool:
		return !value
	case int:
		return value == 0
	case int32:
		return value == 0
	case int64:
		return value == 0
	case float32:
		return value == 0
	case float64:
		return value == 0
	case sql.NullString:
		return !value.Valid || value.String == ""
	case sql.NullInt64:
		return !value.Valid || value.Int64 == 0
	}
	return false
}

func loadJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func loadSummary(path string) map[string]any {
	if path == "" {
		return map[string]any{}
	}
	value, err := loadJSON(path)
	if err != nil {
		return map[string]any{}
	}
	return value
}

func nestedMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func firstNonEmpty(values ...any) any {
	for _, value := range values {
		if !isEmptyValue(value) {
			return value
		}
	}
	return nil
}

// Grok streams user and assistant records from a Grok chat history.
func Grok(chatPath, summaryPath string, emit Emit) error {
	summary := loadSummary(summaryPath)
	info := nestedMap(summary["info"])
	encodedCWD := filepath.Base(filepath.Dir(filepath.Dir(chatPath)))
	cwd := firstNonEmpty(info["cwd"], summary["cwd"])
	if isEmptyValue(cwd) {
		cwd, _ = url.PathUnescape(encodedCWD)
	}
	title := firstNonEmpty(summary["generated_title"], info["generated_title"])
	timestamp := firstNonEmpty(summary["created_at"], info["created_at"])
	sessionID := filepath.Base(filepath.Dir(chatPath))
	return eachJSONL(chatPath, nil, nil, func(value map[string]any) error {
		lineType, _ := value["type"].(string)
		if lineType != "user" && lineType != "assistant" {
			return nil
		}
		text := ExtractText(value["content"])
		if result, ok := makeRecord("grok", sessionID, cwd, title, timestamp, lineType, text, chatPath); ok {
			return emit(result)
		}
		return nil
	})
}

func filenameSessionID(path string) string {
	name := filepath.Base(path)
	if match := uuidRE.FindString(name); match != "" {
		return match
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// Codex streams message payloads from a Codex rollout JSONL file.
func Codex(rolloutPath string, emit Emit) error {
	sessionID := filenameSessionID(rolloutPath)
	cwd := ""
	var sessionTimestamp any
	return eachJSONL(rolloutPath, codexJSONLRE, [][]byte{[]byte(`"message"`), []byte(`session_meta`)}, func(value map[string]any) error {
		payload := nestedMap(value["payload"])
		if len(payload) == 0 {
			return nil
		}
		lineType, _ := value["type"].(string)
		if lineType == "session_meta" {
			if id := anyString(payload["id"]); id != "" {
				sessionID = id
			}
			if value := anyString(payload["cwd"]); value != "" {
				cwd = value
			}
			sessionTimestamp = firstNonEmpty(payload["timestamp"], value["timestamp"])
			return nil
		}
		if anyString(payload["type"]) != "message" {
			return nil
		}
		text := ExtractText(payload["content"])
		timestamp := firstNonEmpty(value["timestamp"], payload["timestamp"], sessionTimestamp)
		if result, ok := makeRecord("codex", sessionID, cwd, "", timestamp, payload["role"], text, rolloutPath); ok {
			return emit(result)
		}
		return nil
	})
}

func kimiWorkspacePath(value map[string]any, text string) string {
	for _, key := range []string{"cwd", "directory", "workspace_path"} {
		if candidate, ok := value[key].(string); ok && candidate != "" {
			return candidate
		}
	}
	if !strings.Contains(text, "Workspace Path") && !strings.Contains(text, "工作区路径") {
		return ""
	}
	match := workspaceRE.FindStringSubmatch(text)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

// Kimi streams context.append_message records from a Kimi wire file.
func Kimi(wirePath string, emit Emit) error {
	sessionDir := filepath.Dir(filepath.Dir(filepath.Dir(wirePath)))
	sessionID := filepath.Base(sessionDir)
	cwd := ""
	return eachJSONL(wirePath, kimiJSONLRE, [][]byte{[]byte(`context.append_message`)}, func(value map[string]any) error {
		if anyString(value["type"]) != "context.append_message" {
			return nil
		}
		message := nestedMap(value["message"])
		if len(message) == 0 {
			return nil
		}
		text := ExtractText(message["content"])
		if text == "" {
			return nil
		}
		if candidate := kimiWorkspacePath(message, text); candidate != "" {
			cwd = candidate
		}
		origin := nestedMap(message["origin"])
		if len(origin) == 0 {
			origin = nestedMap(value["origin"])
		}
		role := message["role"]
		if anyString(origin["kind"]) == "user" {
			role = "user"
		}
		timestamp := firstNonEmpty(value["time"], message["time"])
		if result, ok := makeRecord("kimi-code", sessionID, cwd, "", timestamp, role, text, wirePath); ok {
			return emit(result)
		}
		return nil
	})
}

// Claude streams user and assistant messages from a Claude transcript.
func Claude(transcriptPath string, emit Emit) error {
	fallback := strings.TrimSuffix(filepath.Base(transcriptPath), filepath.Ext(transcriptPath))
	return eachJSONL(transcriptPath, nil, [][]byte{[]byte(`"message"`)}, func(value map[string]any) error {
		message := nestedMap(value["message"])
		if len(message) == 0 {
			return nil
		}
		outerType := anyString(value["type"])
		if outerType != "user" && outerType != "assistant" {
			return nil
		}
		text := ExtractText(message["content"])
		sessionID := anyString(firstNonEmpty(value["sessionId"], value["session_id"]))
		if sessionID == "" {
			sessionID = fallback
		}
		cwd := firstNonEmpty(value["cwd"], value["directory"])
		timestamp := firstNonEmpty(value["timestamp"], message["timestamp"])
		title := value["title"]
		role := firstNonEmpty(message["role"], outerType)
		if result, ok := makeRecord("claude", sessionID, cwd, title, timestamp, role, text, transcriptPath); ok {
			return emit(result)
		}
		return nil
	})
}

func eachJSONL(path string, candidate *regexp.Regexp, hints [][]byte, emit func(map[string]any) error) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	reader := bufio.NewReaderSize(handle, 256*1024)
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			if len(hints) > 0 {
				matched := false
				for _, hint := range hints {
					if bytes.Contains(raw, hint) {
						matched = true
						break
					}
				}
				if !matched {
					if readErr != nil && !errors.Is(readErr, io.EOF) {
						return readErr
					}
					if readErr != nil {
						break
					}
					continue
				}
			}
			if candidate != nil && !candidate.Match(raw) {
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					return readErr
				}
				if readErr != nil {
					break
				}
				continue
			}
			var value map[string]any
			if json.Unmarshal(bytes.TrimSpace(raw), &value) == nil && value != nil {
				if err := emit(value); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			break
		}
	}
	return nil
}
