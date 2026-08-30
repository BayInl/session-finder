package skill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/record"
)

const (
	defaultCandidateKind = "skill"
	defaultActor         = "skill-extractor"
)

// BuildCandidate converts a normalized transcript into a reviewable bundle.
// It never emits transcript excerpts in the rendered skill; evidence pointers
// stay in the bundle persisted to the local candidate store.
func BuildCandidate(messages []record.MessageRecord) (CandidateBundle, error) {
	clean := make([]record.MessageRecord, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" || strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			continue
		}
		message.Role = strings.ToLower(strings.TrimSpace(message.Role))
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		clean = append(clean, message)
	}
	if len(clean) == 0 {
		return CandidateBundle{
			Quality:   EvaluateQuality(extract.Analyze(nil)),
			Evidence:  []EvidenceBlock{},
			Risks:     []string{"empty transcript"},
			Conflicts: []string{},
		}, ErrNoTranscript
	}

	signals := extract.Analyze(clean)
	quality := EvaluateQuality(signals)
	first, hasFirst := firstUserMessage(clean)
	title := ""
	for _, message := range clean {
		if strings.TrimSpace(message.Title) != "" {
			title = strings.TrimSpace(message.Title)
			break
		}
	}
	if title == "" && hasFirst {
		title = messageSummary(first, 80)
	}
	if title == "" {
		title = "session skill"
	}
	slug := slugify(title)
	if slug == "" {
		slug = slugify(signals.IntentKind + " workflow")
	}
	if slug == "" {
		slug = "session-skill"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	trigger := triggerFromMessages(clean, signals.IntentKind)
	bundle := CandidateBundle{
		Slug:         slug,
		Trigger:      trigger,
		Instructions: instructionFromMessages(clean),
		Evidence:     SortEvidence(evidenceForMessages(clean)),
		Quality:      quality,
		Risks:        risksFromSignals(signals),
		Conflicts:    []string{},
		SessionID:    firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.SessionID }),
		Tool:         firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.Tool }),
		Title:        title,
		CWD:          firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.CWD }),
		SourcePath:   firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.SourcePath }),
	}
	return normalizeBundle(bundle), nil
}

// Extract is the concise public extraction API.
func Extract(messages []record.MessageRecord) (CandidateBundle, error) {
	return BuildCandidate(messages)
}

// ExtractBundle is an explicit alias for Extract.
func ExtractBundle(messages []record.MessageRecord) (CandidateBundle, error) {
	return BuildCandidate(messages)
}

func firstNonEmptyString(messages []record.MessageRecord, getter func(record.MessageRecord) string) string {
	for _, message := range messages {
		if value := strings.TrimSpace(getter(message)); value != "" {
			return value
		}
	}
	return ""
}

func triggerFromMessages(messages []record.MessageRecord, intent string) string {
	if first, ok := firstUserMessage(messages); ok {
		text := messageSummary(first, 180)
		if text != "" {
			return "Use when the user asks to " + strings.TrimSuffix(strings.TrimSpace(text), ".") + "."
		}
	}
	if intent == "" || intent == extract.IntentUnknown {
		return "Use when the validated workflow from this session is requested."
	}
	return "Use when the user requests a " + intent + " workflow."
}

func risksFromSignals(signals extract.SignalBundle) []string {
	risks := []string{}
	if len(signals.SuccessEvidence) == 0 {
		risks = append(risks, "insufficient success evidence")
	}
	if signals.OneOffRisk >= HighOneOffRisk {
		risks = append(risks, fmt.Sprintf("one-off risk %.2f", signals.OneOffRisk))
	}
	if signals.SecretRisk > 0 {
		risks = append(risks, fmt.Sprintf("secret risk %.2f", signals.SecretRisk))
	}
	if signals.IntentKind == extract.IntentCorrection {
		risks = append(risks, "correction or retry pattern")
	}
	return normalizeStringList(risks)
}

// ExtractAndPersist builds a bundle and appends it to the candidate store. A
// suppressed bundle is persisted as rejected with recommended_action=suppress,
// preserving the reason for later inspection without allowing publication.
func ExtractAndPersist(ctx context.Context, store *extract.Store, messages []record.MessageRecord, actor string) (CandidateBundle, extract.Candidate, error) {
	if store == nil {
		return CandidateBundle{}, extract.Candidate{}, errors.New("nil candidate store")
	}
	bundle, err := BuildCandidate(messages)
	if err != nil {
		return bundle, extract.Candidate{}, err
	}
	if bundle.SessionID == "" && len(messages) > 0 {
		bundle.SessionID = messages[0].SessionID
	}
	payload, payloadErr := CandidatePayload(bundle)
	if payloadErr != nil {
		return bundle, extract.Candidate{}, payloadErr
	}
	if actor == "" {
		actor = defaultActor
	}
	status := extract.StatusDraft
	if IsSuppressed(bundle.Quality) {
		status = extract.StatusRejected
	}
	candidate, createErr := store.Create(ctx, extract.CandidateInput{
		SessionID:         bundle.SessionID,
		Tool:              bundle.Tool,
		SourcePath:        bundle.SourcePath,
		Kind:              defaultCandidateKind,
		Title:             bundle.Title,
		Summary:           bundle.Trigger,
		Payload:           payload,
		Status:            status,
		Actor:             actor,
		Reason:            strings.Join(bundle.Quality.Reasons, "; "),
		Confidence:        bundle.Quality.Signals.Confidence,
		SuccessEvidence:   bundle.Quality.Signals.SuccessEvidence,
		OneOffRisk:        bundle.Quality.Signals.OneOffRisk,
		SecretRisk:        bundle.Quality.Signals.SecretRisk,
		RecommendedAction: bundle.Quality.Signals.RecommendedAction,
	})
	if createErr != nil {
		return bundle, extract.Candidate{}, createErr
	}
	return bundle, candidate, nil
}

// ExtractCandidate is a readable alias for ExtractAndPersist.
func ExtractCandidate(ctx context.Context, store *extract.Store, messages []record.MessageRecord, actor string) (CandidateBundle, extract.Candidate, error) {
	return ExtractAndPersist(ctx, store, messages, actor)
}

// IndexSessionMessages loads one session's normalized messages from the local
// index. The session argument accepts a complete ID or a unique prefix.
func IndexSessionMessages(ctx context.Context, db *sql.DB, sessionID, cwd, after string) ([]record.MessageRecord, error) {
	return indexSessionMessages(ctx, db, "", sessionID, cwd, after)
}

// IndexToolSessionMessages is the tool-scoped variant used by pending scans.
// Session IDs are not globally unique across transcript providers, so callers
// processing an indexed session should include its tool when available.
func IndexToolSessionMessages(ctx context.Context, db *sql.DB, tool, sessionID, cwd, after string) ([]record.MessageRecord, error) {
	return indexSessionMessages(ctx, db, tool, sessionID, cwd, after)
}

func indexSessionMessages(ctx context.Context, db *sql.DB, tool, sessionID, cwd, after string) ([]record.MessageRecord, error) {
	if db == nil {
		return nil, errors.New("nil index database")
	}
	where := []string{"s.session_id LIKE ? ESCAPE '\\'"}
	args := []any{escapeLike(sessionID) + "%"}
	if tool != "" {
		where = append(where, "s.tool = ?")
		args = append(args, tool)
	}
	if cwd != "" {
		where = append(where, "s.cwd LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(cwd)+"%")
	}
	if after != "" {
		epoch, ok := afterEpoch(after)
		if !ok {
			return nil, errors.New("after must be YYYY-MM-DD or ISO-8601")
		}
		where = append(where, "m.ts IS NOT NULL AND m.ts >= ?")
		args = append(args, epoch)
	}
	query := `SELECT s.tool, s.session_id, s.cwd, s.title, s.source_path,
		m.ts, m.role, m.text FROM sessions AS s JOIN messages AS m ON m.session_pk = s.id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY COALESCE(m.ts, 0), m.id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []record.MessageRecord{}
	for rows.Next() {
		var message record.MessageRecord
		if err := rows.Scan(&message.Tool, &message.SessionID, &message.CWD, &message.Title,
			&message.SourcePath, &message.Timestamp, &message.Role, &message.Text); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func afterEpoch(value string) (float64, bool) {
	if epoch, ok := index.TimestampEpoch(value); ok && epoch != nil {
		return *epoch, true
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, false
	}
	return float64(parsed.UTC().Unix()), true
}

// PendingSessions scans indexed sessions and excludes sessions already queued
// as skill candidates. This is the compensation scan used before hook support.
func PendingSessions(ctx context.Context, indexDBPath, candidateDBPath string, options ExtractOptions) ([]PendingSession, error) {
	indexDB, err := index.Open(indexDBPath)
	if err != nil {
		return nil, err
	}
	defer indexDB.Close()
	if err := index.InitializeSchema(indexDB); err != nil {
		return nil, err
	}
	candidateDB := candidateDBPath
	if candidateDB == "" {
		candidateDB = indexDBPath
	}
	store, err := extract.Open(candidateDB)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	queued, err := store.List(ctx, extract.ListOptions{Kind: defaultCandidateKind, IncludeDeleted: true})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	seenSession := map[string]bool{}
	for _, candidate := range queued {
		if candidate.SessionID == "" {
			continue
		}
		if candidate.Tool == "" {
			// Candidates created by older callers may omit Tool. Treat those as
			// wildcards, while preserving tool-scoped IDs for current records.
			seenSession[candidate.SessionID] = true
			continue
		}
		seen[candidate.Tool+"\x00"+candidate.SessionID] = true
	}
	where := []string{}
	args := []any{}
	if options.SessionID != "" {
		where = append(where, "s.session_id LIKE ? ESCAPE '\\'")
		args = append(args, escapeLike(options.SessionID)+"%")
	}
	if options.CWD != "" {
		where = append(where, "s.cwd LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(options.CWD)+"%")
	}
	if options.After != "" {
		epoch, ok := afterEpoch(options.After)
		if !ok {
			return nil, errors.New("after must be YYYY-MM-DD or ISO-8601")
		}
		where = append(where, "COALESCE(s.updated, s.created, 0) >= ?")
		args = append(args, epoch)
	}
	query := `SELECT s.tool, s.session_id, s.title, s.cwd, s.source_path, s.created, s.updated
		FROM sessions AS s`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY COALESCE(s.updated, s.created, 0) DESC, s.tool, s.session_id"
	rows, err := indexDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PendingSession{}
	for rows.Next() {
		var item PendingSession
		var created, updated any
		if err := rows.Scan(&item.Tool, &item.SessionID, &item.Title, &item.CWD, &item.SourcePath, &created, &updated); err != nil {
			return nil, err
		}
		if seen[item.Tool+"\x00"+item.SessionID] || seenSession[item.SessionID] {
			continue
		}
		item.Created = index.FormatTimestamp(created)
		item.Updated = index.FormatTimestamp(updated)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanPending is an alias used by CLI and hook integrations.
func ScanPending(ctx context.Context, options ExtractOptions) ([]PendingSession, error) {
	return PendingSessions(ctx, options.IndexDBPath, options.CandidateDBPath, options)
}

// ExtractPending performs the compensation scan and queues each unprocessed
// session. It returns both the discovered sessions and created candidates.
func ExtractPending(ctx context.Context, options ExtractOptions) ([]PendingSession, []extract.Candidate, error) {
	indexDB, err := index.Open(options.IndexDBPath)
	if err != nil {
		return nil, nil, err
	}
	defer indexDB.Close()
	if err := index.InitializeSchema(indexDB); err != nil {
		return nil, nil, err
	}
	candidatePath := options.CandidateDBPath
	if candidatePath == "" {
		candidatePath = options.IndexDBPath
	}
	store, err := extract.Open(candidatePath)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	pending, err := PendingSessions(ctx, options.IndexDBPath, candidatePath, options)
	if err != nil {
		return nil, nil, err
	}
	created := make([]extract.Candidate, 0, len(pending))
	for _, session := range pending {
		messages, loadErr := IndexToolSessionMessages(ctx, indexDB, session.Tool, session.SessionID, session.CWD, options.After)
		if loadErr != nil {
			return pending, created, loadErr
		}
		if len(messages) == 0 {
			continue
		}
		_, candidate, createErr := ExtractAndPersist(ctx, store, messages, options.Actor)
		if createErr != nil {
			return pending, created, createErr
		}
		if candidate.ID != "" {
			created = append(created, candidate)
		}
	}
	return pending, created, nil
}

// SortPending returns deterministic ordering for callers that merge scans.
func SortPending(values []PendingSession) []PendingSession {
	result := append([]PendingSession(nil), values...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Updated != result[j].Updated {
			return result[i].Updated > result[j].Updated
		}
		if result[i].Tool != result[j].Tool {
			return result[i].Tool < result[j].Tool
		}
		return result[i].SessionID < result[j].SessionID
	})
	return result
}

// DefaultCandidateDBPath mirrors extract.DefaultDBPath without exposing a
// second database default in callers.
func DefaultCandidateDBPath() string { return filepath.Clean(extract.DefaultDBPath) }
