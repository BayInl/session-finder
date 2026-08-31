# session-finder

`session-finder` indexes and searches local AI coding-session transcripts from
opencode, Grok, Codex, Kimi Code, and Claude. It stores a rebuildable SQLite
FTS5 index under `~/.cache/session-finder/index.db`.

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
go build -o session-finder ./cmd/session-finder
```

To install the binary into your Go bin directory instead:

```sh
go install github.com/BayInl/session-finder/cmd/session-finder@latest
```

## Usage

Build or update the local index:

```sh
session-finder index
```

Rebuild it from scratch:

```sh
session-finder index --full
```

Search indexed messages:

```sh
session-finder search "deployment"
session-finder search "SQLite" --tool codex --limit 10
session-finder search "error" --json
```

Show the message stream for a full session ID or unique ID prefix:

```sh
session-finder show SESSION_ID
session-finder show SESSION_ID --role assistant --limit 20
```

Show the build version, commit, and build date:

```sh
session-finder --version
session-finder version
```

## Automatic session-end extraction hooks

`session-finder` can queue new skill candidates when a host session ends. The
hook only starts the existing incremental `skill extract --pending` scan; it
does not upload transcript data, and it returns immediately. If the
`session-finder` binary is not on `PATH`, the hook exits silently. Repeating the
installation is safe: existing configuration and an already-installed hook
are left unchanged.

Install one host or all supported hosts:

```sh
session-finder hooks install --tool claude
session-finder hooks install --tool kimi
session-finder hooks install --tool opencode
session-finder hooks install --tool all
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
uses the session/tool identity to make repeated lifecycle events harmless.

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

`session-finder` can mine indexed sessions for the *why* behind decisions —
options considered, the chosen one, and the rationale — with every record
backed by an exact quote from the original transcript.

```sh
session-finder decisions extract              # scan sessions for decision candidates
session-finder decisions list                 # review queue
session-finder decisions list --json
session-finder decisions review               # approve / reject / defer / edit each candidate
```

Nothing is recorded until you approve it: candidates start as drafts, every
review action is written to an append-only audit log, and evidence quotes are
verified against the source messages.

## Skill compiler

Successful sessions can be distilled into reusable skills following the
[Agent Skills](https://agentskills.io/) open format:

```sh
session-finder skill extract --pending        # scan new sessions for skill candidates
session-finder skill review                   # approve / reject / defer / edit / split evidence blocks
session-finder skill publish                  # write approved skills to ~/.agents/skills
session-finder skill list
```

Quality gates suppress one-off or low-evidence sessions automatically, and
publishing never overwrites an existing skill of the same name. Extraction
runs fully offline by default; setting `SESSION_FINDER_LLM_*` environment
variables opts into an OpenAI-compatible LLM for higher-precision extraction,
with automatic redaction of tokens, keys, and other secrets before anything
leaves the machine.

For all options, run:

```sh
session-finder --help
session-finder search --help
session-finder show --help
session-finder decisions --help
session-finder skill --help
session-finder hooks --help
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
