package record

import "path/filepath"

// Tools is the deterministic order used by source discovery and CLI summaries.
var Tools = []string{"opencode", "grok", "codex", "kimi-code", "claude"}

// NoisePrefixes identifies injected records hidden by default in search results.
var NoisePrefixes = []string{
	"<supermemory",
	"[SUPERMEMORY",
	"<system-reminder>",
	"<user_info>",
}

// MessageRecord is one normalized message emitted by a source parser.
type MessageRecord struct {
	Tool       string
	SessionID  string
	CWD        string
	Title      string
	Timestamp  any
	Role       string
	Text       string
	SourcePath string
}

// SourceSpec describes a source file and optional metadata files used to parse it.
type SourceSpec struct {
	Tool          string
	Path          string
	AuxiliaryPath []string
}

func (s SourceSpec) String() string { return filepath.Clean(s.Path) }
