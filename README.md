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
