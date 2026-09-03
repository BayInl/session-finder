package skill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

// BuildCandidate converts a normalized transcript into a reviewable bundle
// using only deterministic local signals.
func BuildCandidate(messages []record.MessageRecord) (CandidateBundle, error) {
	return BuildCandidateWithOptions(messages, ExtractOptions{})
}

// BuildCandidateWithOptions optionally runs the independent skill candidate
// judge after the local quality gate. Hard suppressions never invoke an LLM;
// judge failures retain the deterministic bundle and add a fallback reason.
func BuildCandidateWithOptions(messages []record.MessageRecord, options ExtractOptions) (CandidateBundle, error) {
	clean := cleanSkillMessages(messages)
	if len(clean) == 0 {
		return CandidateBundle{
			Quality:  EvaluateQuality(extract.Analyze(nil)),
			Evidence: []EvidenceBlock{},
			Risks:    []string{"empty transcript"},
		}, ErrNoTranscript
	}

	signals := extract.Analyze(clean)
	quality := EvaluateQuality(signals)
	first, hasFirst := firstUserMessage(clean)
	title := ""
	for _, message := range clean {
		if titleText := strings.TrimSpace(message.Title); titleText != "" && !isInjectedNoiseText(titleText) {
			title = titleText
			break
		}
	}
	if title == "" && hasFirst {
		title = messageSummary(first, 80)
	}
	if title == "" {
		title = "session skill"
	}
	slug := skillSlug(title, clean)
	trigger := triggerFromMessages(clean, signals.IntentKind)
	bundle := CandidateBundle{
		Slug:         slug,
		Trigger:      trigger,
		Instructions: instructionFromMessages(clean),
		Evidence:     SortEvidence(evidenceForMessages(clean)),
		Quality:      quality,
		Risks:        risksFromSignals(signals),
		SessionID:    firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.SessionID }),
		Tool:         firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.Tool }),
		Title:        title,
		CWD:          firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.CWD }),
		SourcePath:   firstNonEmptyString(clean, func(message record.MessageRecord) string { return message.SourcePath }),
	}
	for _, observation := range options.SegmentObservations {
		bundle.Quality.Reasons = appendSkillReason(bundle.Quality.Reasons, observation.reason())
	}
	if options.Judge != nil && quality.Disposition != QualitySuppress && quality.Signals.OneOffRisk < HighOneOffRisk && quality.Signals.SecretRisk < HighSecretRisk && len(quality.Signals.SuccessEvidence) >= MinimumSuccessEvidence && quality.Signals.Confidence < 0.85 {
		conversation := cleanSkillMessages(options.ConversationContext)
		judgment, err := options.Judge.Judge(context.Background(), candidateReview(clean, conversation, bundle))
		if err != nil {
			bundle.Quality.Reasons = appendSkillReason(bundle.Quality.Reasons, "llm:fallback")
		} else {
			bundle = applyCandidateJudgment(bundle, judgment)
		}
	}
	return normalizeBundle(bundle), nil
}

func cleanSkillMessages(messages []record.MessageRecord) []record.MessageRecord {
	clean := make([]record.MessageRecord, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" || strings.EqualFold(strings.TrimSpace(message.Role), "system") || isInjectedNoiseText(message.Text) {
			continue
		}
		message.Role = strings.ToLower(strings.TrimSpace(message.Role))
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		clean = append(clean, message)
	}
	return clean
}

// Extract is the concise public extraction API.
func Extract(messages []record.MessageRecord) (CandidateBundle, error) {
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

func skillSlug(title string, messages []record.MessageRecord) string {
	slug := slugify(title)
	if len(slug) < 3 {
		hash := sha256.New()
		_, _ = hash.Write([]byte(title))
		for _, message := range messages {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(message.Role))
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(message.Text))
		}
		slug = "skill-" + hex.EncodeToString(hash.Sum(nil)[:4])
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
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

// ExtractAndPersist builds an offline bundle and appends it to the candidate
// store. It remains a compatibility wrapper with no LLM calls.
func ExtractAndPersist(ctx context.Context, store *extract.Store, messages []record.MessageRecord, actor string) (CandidateBundle, extract.Candidate, error) {
	return ExtractAndPersistWithOptions(ctx, store, messages, actor, ExtractOptions{})
}

type candidateSlugAllocator struct {
	used map[string]bool
	next map[string]int
}

func newCandidateSlugAllocator(candidates []extract.Candidate) *candidateSlugAllocator {
	allocator := &candidateSlugAllocator{
		used: make(map[string]bool, len(candidates)),
		next: make(map[string]int),
	}
	for _, candidate := range candidates {
		existing, err := BundleFromCandidate(candidate)
		if err == nil && existing.Slug != "" {
			allocator.used[existing.Slug] = true
		}
	}
	return allocator
}

func loadCandidateSlugAllocator(ctx context.Context, store *extract.Store) (*candidateSlugAllocator, error) {
	candidates, err := store.List(ctx, extract.ListOptions{Kind: defaultCandidateKind, IncludeDeleted: true})
	if err != nil {
		return nil, err
	}
	return newCandidateSlugAllocator(candidates), nil
}

func (a *candidateSlugAllocator) unique(bundle CandidateBundle) CandidateBundle {
	base := bundle.Slug
	if !a.used[base] {
		a.used[base] = true
		a.next[base] = 2
		return bundle
	}
	sequence := max(2, a.next[base])
	for ; ; sequence++ {
		suffix := fmt.Sprintf("-%d", sequence)
		limit := 64 - len(suffix)
		candidateSlug := strings.Trim(base[:min(len(base), limit)], "-") + suffix
		if !a.used[candidateSlug] {
			bundle.Slug = candidateSlug
			a.used[candidateSlug] = true
			a.next[base] = sequence + 1
			return bundle
		}
	}
}

func buildTranscriptBundles(ctx context.Context, messages []record.MessageRecord, options ExtractOptions) ([]CandidateBundle, error) {
	split := SplitTranscriptDetailed(ctx, messages, options.Segmenter)
	if len(split.Slices) == 0 {
		return nil, ErrNoTranscript
	}
	// Segmenter already ran; do not split again inside BuildCandidate. Preserve
	// the complete parent conversation and any repair/fallback observations for
	// every candidate emitted from this transcript.
	options.Segmenter = nil
	options.ConversationContext = messages
	options.SegmentObservations = split.Observations
	bundles := make([]CandidateBundle, 0, len(split.Slices))
	for _, slice := range split.Slices {
		bundle, err := BuildCandidateWithOptions(slice, options)
		if errors.Is(err, ErrNoTranscript) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if bundle.SessionID == "" && len(slice) > 0 {
			bundle.SessionID = slice[0].SessionID
		}
		bundles = append(bundles, bundle)
	}
	if len(bundles) == 0 {
		return nil, ErrNoTranscript
	}
	return bundles, nil
}

func persistTranscript(ctx context.Context, store *extract.Store, messages []record.MessageRecord, actor string, options ExtractOptions) ([]extract.Candidate, error) {
	if store == nil {
		return nil, errors.New("nil candidate store")
	}
	bundles, err := buildTranscriptBundles(ctx, messages, options)
	if err != nil {
		return nil, err
	}
	unlock, err := acquirePendingLock(ctx, store.Path())
	if err != nil {
		return nil, err
	}
	defer unlock()
	allocator, err := loadCandidateSlugAllocator(ctx, store)
	if err != nil {
		return nil, err
	}
	return persistCandidateBundles(ctx, store, bundles, actor, allocator)
}

func persistCandidateBundles(ctx context.Context, store *extract.Store, bundles []CandidateBundle, actor string, allocator *candidateSlugAllocator) ([]extract.Candidate, error) {
	created := make([]extract.Candidate, 0, len(bundles))
	for _, bundle := range bundles {
		_, candidate, err := persistCandidateBundle(ctx, store, bundle, actor, allocator)
		if err != nil {
			return created, err
		}
		created = append(created, candidate)
	}
	return created, nil
}

// ExtractAndPersistWithOptions builds a bundle with an optional skill judge and
// appends it to the candidate store. Judge failures retain local results.
func ExtractAndPersistWithOptions(ctx context.Context, store *extract.Store, messages []record.MessageRecord, actor string, options ExtractOptions) (CandidateBundle, extract.Candidate, error) {
	if store == nil {
		return CandidateBundle{}, extract.Candidate{}, errors.New("nil candidate store")
	}
	bundle, err := BuildCandidateWithOptions(messages, options)
	if err != nil {
		return bundle, extract.Candidate{}, err
	}
	if bundle.SessionID == "" && len(messages) > 0 {
		bundle.SessionID = messages[0].SessionID
	}
	unlock, err := acquirePendingLock(ctx, store.Path())
	if err != nil {
		return bundle, extract.Candidate{}, err
	}
	defer unlock()
	allocator, err := loadCandidateSlugAllocator(ctx, store)
	if err != nil {
		return bundle, extract.Candidate{}, err
	}
	return persistCandidateBundle(ctx, store, bundle, actor, allocator)
}

func persistCandidateBundle(ctx context.Context, store *extract.Store, bundle CandidateBundle, actor string, allocator *candidateSlugAllocator) (CandidateBundle, extract.Candidate, error) {
	if store == nil {
		return bundle, extract.Candidate{}, errors.New("nil candidate store")
	}
	if allocator == nil {
		var err error
		allocator, err = loadCandidateSlugAllocator(ctx, store)
		if err != nil {
			return bundle, extract.Candidate{}, err
		}
	}
	bundle = allocator.unique(bundle)
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

// persistSkippedSession records a session that contains only filtered/system
// records. Marking it failed prevents every compensation scan from retrying the
// same unusable transcript while preserving an auditable reason for review.
func persistSkippedSession(ctx context.Context, store *extract.Store, session PendingSession, actor string) (extract.Candidate, error) {
	if store == nil {
		return extract.Candidate{}, errors.New("nil candidate store")
	}
	if actor == "" {
		actor = defaultActor
	}
	slug := slugify("empty-session-" + session.SessionID)
	if slug == "" {
		slug = "empty-session"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	bundle := CandidateBundle{
		Slug:         slug,
		Trigger:      "",
		Instructions: "",
		Evidence:     []EvidenceBlock{},
		Quality:      EvaluateQuality(extract.Analyze(nil)),
		Risks:        []string{"empty transcript"},
		SessionID:    session.SessionID,
		Tool:         session.Tool,
		Title:        session.Title,
		CWD:          session.CWD,
		SourcePath:   session.SourcePath,
	}
	payload, err := CandidatePayload(bundle)
	if err != nil {
		return extract.Candidate{}, err
	}
	return store.Create(ctx, extract.CandidateInput{
		SessionID:         session.SessionID,
		Tool:              session.Tool,
		SourcePath:        session.SourcePath,
		Kind:              defaultCandidateKind,
		Title:             session.Title,
		Summary:           "empty transcript",
		Payload:           payload,
		Status:            extract.StatusFailed,
		Actor:             actor,
		Reason:            "empty transcript after noise filtering",
		Confidence:        0,
		SuccessEvidence:   []string{},
		OneOffRisk:        1,
		SecretRisk:        0,
		RecommendedAction: extract.ActionSuppress,
	})
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
	queued, err := listSkillCandidates(ctx, store)
	if err != nil {
		return nil, err
	}
	return pendingSessions(ctx, indexDB, queued, options)
}

func listSkillCandidates(ctx context.Context, store *extract.Store) ([]extract.Candidate, error) {
	// IncludeDeleted keeps recoverably deleted candidates in the seen set. A
	// deletion does not mean the source session should be regenerated; callers
	// should restore and review the existing candidate instead of relying on
	// pending scans to create duplicates.
	return store.List(ctx, extract.ListOptions{Kind: defaultCandidateKind, IncludeDeleted: true})
}

func pendingSessions(ctx context.Context, indexDB *sql.DB, queued []extract.Candidate, options ExtractOptions) ([]PendingSession, error) {
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

type pendingTranscriptKey struct {
	tool      string
	sessionID string
	cwd       string
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
	unlock, err := acquirePendingLock(ctx, candidatePath)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	store, err := extract.Open(candidatePath)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	queued, err := listSkillCandidates(ctx, store)
	if err != nil {
		return nil, nil, err
	}
	pending, err := pendingSessions(ctx, indexDB, queued, options)
	if err != nil {
		return nil, nil, err
	}
	allocator := newCandidateSlugAllocator(queued)
	remaining := make(map[pendingTranscriptKey]int)
	for _, session := range pending {
		remaining[pendingTranscriptKey{tool: session.Tool, sessionID: session.SessionID, cwd: session.CWD}]++
	}
	messageCache := make(map[pendingTranscriptKey][]record.MessageRecord)
	bundleCache := make(map[pendingTranscriptKey][]CandidateBundle)
	cacheBundles := canCachePendingBundles(options)
	emptyCache := make(map[pendingTranscriptKey]bool)
	created := make([]extract.Candidate, 0, len(pending))
	for _, session := range pending {
		key := pendingTranscriptKey{tool: session.Tool, sessionID: session.SessionID, cwd: session.CWD}
		messages, loaded := messageCache[key]
		empty := emptyCache[key]
		if !loaded && !empty {
			messages, err = IndexToolSessionMessages(ctx, indexDB, key.tool, key.sessionID, key.cwd, options.After)
			if err != nil {
				return pending, created, err
			}
			if len(messages) == 0 {
				empty = true
				if remaining[key] > 1 {
					emptyCache[key] = true
				}
			} else if remaining[key] > 1 {
				messageCache[key] = messages
			}
		}
		bundles, cached := bundleCache[key]
		if !empty && (!cacheBundles || !cached) {
			bundles, err = buildTranscriptBundles(ctx, messages, options)
			if errors.Is(err, ErrNoTranscript) {
				empty = true
				if remaining[key] > 1 {
					emptyCache[key] = true
				}
			} else if err != nil {
				return pending, created, err
			} else if cacheBundles && remaining[key] > 1 {
				bundleCache[key] = bundles
				delete(messageCache, key)
			}
		}
		if empty {
			candidate, skipErr := persistSkippedSession(ctx, store, session, options.Actor)
			if skipErr != nil {
				return pending, created, skipErr
			}
			created = append(created, candidate)
			releasePendingCache(key, remaining, messageCache, bundleCache, emptyCache)
			continue
		}
		candidates, persistErr := persistCandidateBundles(ctx, store, bundles, options.Actor, allocator)
		if persistErr != nil {
			return pending, created, persistErr
		}
		created = append(created, candidates...)
		releasePendingCache(key, remaining, messageCache, bundleCache, emptyCache)
	}
	return pending, created, nil
}

func canCachePendingBundles(options ExtractOptions) bool {
	if options.Judge != nil {
		return false
	}
	if options.Segmenter == nil {
		return true
	}
	_, unavailable := options.Segmenter.(unavailableSegmenter)
	return unavailable
}

func releasePendingCache(key pendingTranscriptKey, remaining map[pendingTranscriptKey]int, messages map[pendingTranscriptKey][]record.MessageRecord, bundles map[pendingTranscriptKey][]CandidateBundle, empty map[pendingTranscriptKey]bool) {
	remaining[key]--
	if remaining[key] > 0 {
		return
	}
	delete(remaining, key)
	delete(messages, key)
	delete(bundles, key)
	delete(empty, key)
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
