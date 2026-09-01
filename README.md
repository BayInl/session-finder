# sfind

`sfind` (short for session-finder) indexes and searches local AI coding-session
transcripts from opencode, Grok, Codex, Kimi Code, and Claude. It stores a
rebuildable SQLite FTS5 index under `~/.cache/session-finder/index.db`.

The old `session-finder` command name still works as an alias.

## Install with Homebrew

```sh
brew install BayInl/tap/session-finder
```

The Homebrew tap is updated from tagged GitHub releases. Pre-built binaries are
provided for Apple Silicon macOS, Intel macOS, and Intel Linux.

## Build from source

Requirements: Go 1.25 or newer.

```sh
git clone https://github.com/BayInl/session-finder.git
cd session-finder
go build -o sfind ./cmd/session-finder
```

To install the binary into your Go bin directory instead:

```sh
go install github.com/BayInl/session-finder/cmd/session-finder@latest
```

## Usage

On a terminal, `sfind` with no arguments opens a full-screen browser. Search
inside it with `/`, or pass a query:

```sh
sfind
sfind search "deployment"
sfind search "deployment" --plain
```

Build or update the local index:

```sh
sfind index
```

Rebuild it from scratch:

```sh
sfind index --full
```

Search indexed messages:

```sh
sfind search "deployment"
sfind search "SQLite" --tool codex --limit 10
sfind search "error" --json
```

### Query syntax

Plain space-separated terms match at the session level (a session matches when
the terms appear across its messages). For finer control, the query string
supports:

- **Boolean operators** — `NOT` > `AND` > `OR`, with parentheses:
  `docker AND NOT test`, `(error OR panic) AND timeout`
- **Phrases** — `"exact phrase"` matches the literal text
- **Field prefixes** — `tool:codex`, `cwd:project`, `after:2026-01-01`
  (combinable with boolean expressions: `tool:codex 部署`)

### Output modes

In a terminal, each hit shows the session header plus the last human/assistant
round (wrapped to the terminal width). Piped output stays a single compact line
per session. Use `--verbose` for full result cards, `--json` for
machine-readable output (`last_user` / `last_assistant` are included when
present). Colors follow `NO_COLOR`; column width adapts to the terminal.
`--include-system` (alias `--all`) includes system/noise records.

Show the message stream for a full session ID or unique ID prefix:

```sh
sfind show SESSION_ID
sfind show SESSION_ID --role assistant --limit 20
```

Show the build version, commit, and build date:

```sh
sfind --version
sfind version
```

## Automatic session-end extraction hooks

`sfind` can queue new skill candidates when a host session ends. The
hook only starts the existing incremental `skill extract --pending` scan; it
does not upload transcript data, and it returns immediately. If neither
`sfind` nor `session-finder` is on `PATH`, the hook exits silently. Repeating the
installation is safe: existing configuration and an already-installed hook
are left unchanged.

Install one host or all supported hosts:

```sh
sfind hooks install --tool claude
sfind hooks install --tool kimi
sfind hooks install --tool opencode
sfind hooks install --tool all
```

The installer updates these user-level locations:

- Claude Code: `~/.claude/settings.json` (or `CLAUDE_CONFIG_DIR/settings.json`)
  with a `SessionEnd` command hook.
- Kimi Code: `~/.kimi-code/config.toml` (or
  `$KIMI_CODE_HOME/config.toml`) with a `[[hooks]]` `SessionEnd` entry.
- OpenCode: `~/.config/opencode/plugins/session-finder.ts` (or
  `$XDG_CONFIG_HOME/opencode/plugins/session-finder.ts`) with a `session.idle`
  plugin.

The generated hooks are intentionally fail-open and detached from the host
process. They ignore lifecycle JSON because the compensation scan discovers
all indexed sessions that do not already have a candidate. Candidate storage
uses the session/tool identity to make repeated lifecycle events harmless. A
short-lived lock records its owner PID and start time, refuses to reclaim a
live owner, and reclaims a dead or malformed owner only after ten minutes; an
unexpected lock-file error simply skips that scan.

### Manual installation

The repository includes equivalent hook assets under `hooks/`:

- `hooks/session-end.sh` is a portable Claude/Kimi command hook. Point a
  `SessionEnd` command entry at its absolute path and set the host timeout to a
  few seconds or less.
- `hooks/opencode-session-idle.ts` is the OpenCode plugin. Copy it to the
  global plugin directory shown above, or to a project `.opencode/plugins/`
  directory when only one project should use it.

For Claude Code, add the script to the `SessionEnd` hook group in
`settings.json`; for Kimi Code, add the script as the `command` in a
`[[hooks]]` block with `event = "SessionEnd"`. Keep the command asynchronous or
use the short timeout supported by the host.

To uninstall, remove only the `session-finder`-managed `SessionEnd` entry from
Claude/Kimi configuration, and remove
`~/.config/opencode/plugins/session-finder.ts` (or the configured equivalent)
for OpenCode. Leave other hook entries and unrelated configuration intact.

## Decision ledger

`sfind` can mine indexed sessions for the *why* behind decisions —
options considered, the chosen one, and the rationale — with every record
backed by an exact quote from the original transcript.

```sh
sfind decisions extract              # scan sessions for decision candidates
sfind decisions list                 # review queue
sfind decisions list --json
sfind decisions review               # approve / reject / defer / edit each candidate
```

Nothing is recorded until you approve it: candidates start as drafts, every
review action is written to an append-only audit log, and evidence quotes are
verified against the source messages.

## Skill compiler

Successful sessions can be distilled into reusable skills following the
[Agent Skills](https://agentskills.io/) open format:

```sh
sfind skill extract --pending        # scan new sessions for skill candidates
sfind skill review                   # approve / reject / defer / edit / split evidence blocks
sfind skill publish                  # write approved skills to ~/.agents/skills
sfind skill list
```

Quality gates suppress one-off or low-evidence sessions automatically, and
publishing never overwrites an existing skill of the same name. Extraction
runs fully offline by default; setting `SESSION_FINDER_LLM_*` environment
variables opts into an OpenAI-compatible LLM for higher-precision extraction,
with automatic redaction of tokens, keys, and other secrets before anything
leaves the machine.

For all options, run:

```sh
sfind --help
sfind search --help
sfind show --help
sfind decisions --help
sfind skill --help
sfind hooks --help
```

## Supported sources

The indexer discovers local sessions for:

- opencode
- Grok
- Codex
- Kimi Code
- Claude

Sessions are read locally; no transcript data is uploaded by this tool.

## License

MIT
