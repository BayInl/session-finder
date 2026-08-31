package decisions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	commandregistry "github.com/BayInl/session-finder/cmd/session-finder/registry"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/llm"
	"github.com/BayInl/session-finder/internal/record"
)

// RegisterCommand registers the decisions command family in the shared CLI
// registry. The root binary may call this from its package init; keeping the
// registration function here avoids an import cycle from internal packages.
func RegisterCommand() { commandregistry.Register("decisions", RunCommand) }

// RunCommand executes `decisions extract|list|review`. It is exported for the
// registry and for embedding applications that provide their own CLI.
func RunCommand(argv []string) error {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" {
		printUsage(os.Stdout)
		return nil
	}
	switch argv[0] {
	case "extract":
		return runExtract(os.Stdout, argv[1:])
	case "list":
		return runList(os.Stdout, argv[1:])
	case "review":
		return runReview(os.Stdin, os.Stdout, argv[1:])
	default:
		return fmt.Errorf("unknown decisions command %q", argv[0])
	}
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: session-finder decisions <extract|list|review> [flags]")
	fmt.Fprintln(writer, "  extract [--session ID] [--db PATH] [--judge off|auto|on] [--judge-limit N] [--json]")
	fmt.Fprintln(writer, "  list [--json] [--status STATUS] [--session ID] [--db PATH]")
	fmt.Fprintln(writer, "  review [--db PATH] [--id ID] [--action approve|reject|defer|edit]")
}

func runExtract(writer io.Writer, argv []string) error {
	set := flag.NewFlagSet("decisions extract", flag.ContinueOnError)
	set.SetOutput(writer)
	sessionID := set.String("session", "", "restrict to one session ID or prefix")
	dbPath := set.String("db", "", "path to SQLite index database")
	judgeMode := set.String("judge", llm.EnvJudgeMode(), "candidate judge: off, auto, or on")
	judgeLimit := set.Int("judge-limit", 0, "maximum candidate judge calls (0 means unlimited)")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("decisions extract accepts no positional arguments")
	}
	db, err := index.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := index.InitializeSchema(db); err != nil {
		return err
	}
	messages, err := loadMessages(db, *sessionID)
	if err != nil {
		return err
	}
	mode, err := llm.JudgeMode(*judgeMode)
	if err != nil {
		return err
	}
	options := ExtractOptions{JudgeLimit: *judgeLimit, ResolvedOnly: true}
	if mode != llm.JudgeOff {
		client, clientErr := llm.NewFromEnv()
		if clientErr != nil {
			if mode == llm.JudgeOn {
				return clientErr
			}
		} else if llm.IsOffline(client) {
			if mode == llm.JudgeOn {
				return errors.New("judge=on requires a configured online llm provider")
			}
		} else {
			options.Judge = NewLLMCandidateJudge(client)
		}
	}
	candidates, err := extractGrouped(context.Background(), messages, options)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(writer, struct {
			SessionID  string              `json:"session_id,omitempty"`
			Count      int                 `json:"count"`
			Candidates []DecisionCandidate `json:"candidates"`
		}{SessionID: *sessionID, Count: len(candidates), Candidates: candidates})
	}
	for _, candidate := range candidates {
		fmt.Fprintf(writer, "%s\t%s\t%.2f\t%s\n", candidate.ID, candidate.Chosen, candidate.Confidence, candidate.Context)
	}
	return nil
}

func runList(writer io.Writer, argv []string) error {
	set := flag.NewFlagSet("decisions list", flag.ContinueOnError)
	set.SetOutput(writer)
	status := set.String("status", "", "filter by status")
	sessionID := set.String("session", "", "filter by session ID")
	dbPath := set.String("db", "", "path to SQLite index database")
	limit := set.Int("limit", 0, "maximum decisions to show")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("decisions list accepts no positional arguments")
	}
	store, err := Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	decisions, err := store.List(context.Background(), ListOptions{Status: *status, SessionID: *sessionID, Limit: *limit})
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(writer, struct {
			Count     int        `json:"count"`
			Decisions []Decision `json:"decisions"`
		}{Count: len(decisions), Decisions: decisions})
	}
	for _, decision := range decisions {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", decision.ID, decision.Status, decision.Chosen, decision.Context)
	}
	return nil
}

func runReview(reader io.Reader, writer io.Writer, argv []string) error {
	set := flag.NewFlagSet("decisions review", flag.ContinueOnError)
	set.SetOutput(writer)
	id := set.String("id", "", "decision ID")
	action := set.String("action", "", "approve, reject, defer, or edit")
	dbPath := set.String("db", "", "path to SQLite index database")
	actor := set.String("actor", "reviewer", "audit actor")
	reason := set.String("reason", "", "audit reason")
	confirmed := set.Bool("confirm", false, "confirm an edit")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*id) == "" || strings.TrimSpace(*action) == "" {
		return fmt.Errorf("decisions review requires --id and --action")
	}
	store, err := Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	input := ReviewInput{ID: *id, Action: ReviewAction(strings.ToLower(strings.TrimSpace(*action))), Actor: *actor, Reason: *reason, Confirmed: *confirmed}
	if input.Action == ReviewEdit {
		var edited Decision
		decoder := json.NewDecoder(reader)
		if err := decoder.Decode(&edited); err != nil {
			return fmt.Errorf("edit expects a JSON decision on stdin: %w", err)
		}
		input.Decision = &edited
		if !input.Confirmed {
			return ErrConfirmationRequired
		}
	}
	decision, err := store.Review(context.Background(), input)
	if err != nil {
		return err
	}
	return encodeJSON(writer, decision)
}

func encodeJSON(writer io.Writer, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	_, err := writer.Write(buffer.Bytes())
	return err
}

type transcriptIdentity struct {
	Tool       string
	SessionID  string
	SourcePath string
}

func extractGrouped(ctx context.Context, messages []record.MessageRecord, options ExtractOptions) ([]DecisionCandidate, error) {
	groups := make(map[transcriptIdentity][]record.MessageRecord)
	order := make([]transcriptIdentity, 0)
	for _, message := range messages {
		key := transcriptIdentity{Tool: message.Tool, SessionID: message.SessionID, SourcePath: message.SourcePath}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], message)
	}
	candidates := make([]DecisionCandidate, 0)
	for _, key := range order {
		group := groups[key]
		var found []DecisionCandidate
		var err error
		if options.Judge != nil {
			found, err = ExtractContext(ctx, group, options)
		} else {
			found = Extract(group, options)
		}
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	SortCandidates(candidates)
	return candidates, nil
}

func loadMessages(db *sql.DB, sessionID string) ([]record.MessageRecord, error) {
	where := ""
	args := []any{}
	if strings.TrimSpace(sessionID) != "" {
		where = " WHERE s.session_id = ? OR s.session_id LIKE ? ESCAPE '\\'"
		prefix := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(sessionID, "\\", "\\\\"), "%", "\\%"), "_", "\\_")
		args = append(args, sessionID, prefix+"%")
	}
	rows, err := db.Query(`SELECT s.tool, s.session_id, s.cwd, s.title, s.source_path,
		m.ts, m.role, m.text FROM sessions AS s JOIN messages AS m ON m.session_pk = s.id`+where+`
		ORDER BY s.session_id, COALESCE(m.ts, 0), m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []record.MessageRecord{}
	for rows.Next() {
		var tool, sid, cwd, title, source, role, text string
		var timestamp any
		if err := rows.Scan(&tool, &sid, &cwd, &title, &source, &timestamp, &role, &text); err != nil {
			return nil, err
		}
		result = append(result, record.MessageRecord{Tool: tool, SessionID: sid, CWD: cwd, Title: title, SourcePath: source, Timestamp: timestamp, Role: role, Text: text})
	}
	return result, rows.Err()
}
