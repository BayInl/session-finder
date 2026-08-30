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

For all options, run:

```sh
session-finder --help
session-finder search --help
session-finder show --help
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
