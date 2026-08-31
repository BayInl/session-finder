package skill

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"database/sql"

	commandregistry "github.com/BayInl/session-finder/cmd/session-finder/registry"
	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/llm"
)

func init() {
	// The main binary needs a blank import of internal/skill for this init hook
	// to be linked. Keeping registration here preserves the feature-package
	// boundary and lets other binaries opt into the command family.
	commandregistry.Register("skill", RunCommand)
}

// RunCommand dispatches `session-finder skill ...` subcommands.
func RunCommand(argv []string) error {
	if len(argv) == 0 || argv[0] == "--help" || argv[0] == "-h" {
		printSkillUsage()
		return nil
	}
	switch argv[0] {
	case "extract":
		return runExtract(argv[1:])
	case "review":
		return runReview(argv[1:])
	case "publish":
		return runPublish(argv[1:])
	case "disable":
		return runDisable(argv[1:])
	case "delete":
		return runDelete(argv[1:])
	case "list":
		return runList(argv[1:])
	default:
		return fmt.Errorf("unknown skill command %q", argv[0])
	}
}

// Commands returns the skill command family names.
func Commands() []string {
	return []string{"extract", "review", "publish", "disable", "delete", "list"}
}

func printSkillUsage() {
	fmt.Println("usage: session-finder skill <extract|review|publish|disable|delete|list> [flags]")
}

func runExtract(argv []string) error {
	set := flag.NewFlagSet("skill extract", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	session := set.String("session", "", "session ID or unique prefix")
	cwd := set.String("cwd", "", "restrict to sessions whose cwd contains this text")
	after := set.String("after", "", "only sessions on or after YYYY-MM-DD")
	pending := set.Bool("pending", false, "scan and queue sessions without a skill candidate")
	indexDB := set.String("db", "", "path to the indexed session SQLite database")
	candidateDB := set.String("candidate-db", "", "path to the candidate SQLite database")
	actor := set.String("actor", defaultActor, "audit actor")
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
		return errors.New("skill extract accepts no positional arguments")
	}
	mode, err := llm.JudgeMode(*judgeMode)
	if err != nil {
		return err
	}
	options := ExtractOptions{SessionID: *session, CWD: *cwd, After: *after, Pending: *pending,
		IndexDBPath: *indexDB, CandidateDBPath: *candidateDB, Actor: *actor, JudgeLimit: *judgeLimit}
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
	ctx := context.Background()
	if *pending || *session == "" {
		pendingSessions, candidates, err := ExtractPending(ctx, options)
		if err != nil {
			return err
		}
		return printJSONOrText(*asJSON, struct {
			Pending    []PendingSession    `json:"pending"`
			Candidates []extract.Candidate `json:"candidates"`
		}{pendingSessions, candidates}, fmt.Sprintf("queued %d of %d pending sessions", len(candidates), len(pendingSessions)))
	}
	db, err := openIndexForSkill(*indexDB)
	if err != nil {
		return err
	}
	defer db.Close()
	messages, err := IndexSessionMessages(ctx, db, *session, *cwd, *after)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return ErrNoTranscript
	}
	store, err := extract.Open(*candidateDB)
	if err != nil {
		return err
	}
	defer store.Close()
	bundle, candidate, err := ExtractAndPersistWithOptions(ctx, store, messages, *actor, options)
	if err != nil && !errors.Is(err, ErrNoTranscript) {
		return err
	}
	return printJSONOrText(*asJSON, struct {
		Bundle    CandidateBundle   `json:"bundle"`
		Candidate extract.Candidate `json:"candidate"`
	}{bundle, candidate}, "queued candidate "+candidate.ID)
}

func runReview(argv []string) error {
	set := flag.NewFlagSet("skill review", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	dbPath := set.String("db", "", "path to the candidate SQLite database")
	action := set.String("action", "", "approve, reject, defer, edit, or split")
	evidenceID := set.String("evidence", "", "evidence block ID")
	note := set.String("note", "", "reviewer note")
	slug := set.String("slug", "", "replacement skill slug for edit")
	trigger := set.String("trigger", "", "replacement trigger for edit")
	instructions := set.String("instructions", "", "replacement instructions for edit")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 1 {
		return errors.New("skill review requires exactly one candidate ID")
	}
	if strings.TrimSpace(*action) == "" {
		return errors.New("skill review requires --action")
	}
	result, err := ReviewCandidate(context.Background(), *dbPath, set.Arg(0), ReviewRequest{
		Action: *action, EvidenceID: *evidenceID, ReviewerNote: *note,
		Slug: *slug, Trigger: *trigger, Instructions: *instructions,
	})
	if err != nil {
		return err
	}
	return printJSONOrText(*asJSON, result, fmt.Sprintf("reviewed %s: %s", result.Candidate.ID, result.Action))
}

func runPublish(argv []string) error {
	set := flag.NewFlagSet("skill publish", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	target := set.String("target", TargetGeneric, "generic, claude, kimi, or project")
	dbPath := set.String("db", "", "path to the candidate SQLite database")
	home := set.String("home", "", "home directory override for tests")
	project := set.String("project", "", "project directory for --target project")
	root := set.String("skills-root", "", "explicit skills root override")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 1 {
		return errors.New("skill publish requires exactly one candidate ID")
	}
	result, err := PublishCandidate(context.Background(), *dbPath, set.Arg(0), PublishOptions{
		Target: *target, HomeDir: *home, ProjectDir: *project, SkillsRoot: *root,
	})
	if err != nil {
		return err
	}
	return printJSONOrText(*asJSON, result, "published "+result.Path)
}

func runDisable(argv []string) error {
	set := flag.NewFlagSet("skill disable", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	dbPath := set.String("db", "", "path to the candidate SQLite database")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 1 {
		return errors.New("skill disable requires exactly one candidate ID")
	}
	candidate, err := DisableCandidate(context.Background(), *dbPath, set.Arg(0))
	if err != nil {
		return err
	}
	return printJSONOrText(*asJSON, candidate, "disabled "+candidate.ID)
}

func runDelete(argv []string) error {
	set := flag.NewFlagSet("skill delete", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	dbPath := set.String("db", "", "path to the candidate SQLite database")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 1 {
		return errors.New("skill delete requires exactly one candidate ID")
	}
	candidate, err := DeleteCandidate(context.Background(), *dbPath, set.Arg(0))
	if err != nil {
		return err
	}
	return printJSONOrText(*asJSON, candidate, "deleted "+candidate.ID)
}

func runList(argv []string) error {
	set := flag.NewFlagSet("skill list", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	dbPath := set.String("db", "", "path to the candidate SQLite database")
	status := set.String("status", "", "candidate status filter")
	asJSON := set.Bool("json", false, "emit JSON")
	if err := set.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if set.NArg() != 0 {
		return errors.New("skill list accepts no positional arguments")
	}
	repository, err := Open(*dbPath)
	if err != nil {
		return err
	}
	defer repository.Close()
	candidates, err := repository.List(context.Background(), extract.ListOptions{Status: *status})
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(candidates)
	}
	for _, candidate := range candidates {
		fmt.Printf("%s\t%s\t%s\t%s\n", candidate.ID, candidate.Status, candidate.Title, candidate.SessionID)
	}
	return nil
}

func printJSONOrText(asJSON bool, value any, text string) error {
	if asJSON {
		return printJSON(value)
	}
	fmt.Println(text)
	return nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func openIndexForSkill(path string) (*sql.DB, error) {
	return index.Open(path)
}
