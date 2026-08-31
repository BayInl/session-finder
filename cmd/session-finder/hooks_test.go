package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrintVersionFormat(t *testing.T) {
	originalVersion, originalCommit, originalDate := version, commit, date
	t.Cleanup(func() { version, commit, date = originalVersion, originalCommit, originalDate })
	version, commit, date = "v1.2.3", "abc123", "2026-08-31T10:11:12Z"

	var output bytes.Buffer
	printVersion(&output)
	want := "session-finder version v1.2.3\ncommit: abc123\ndate: 2026-08-31T10:11:12Z\n"
	if output.String() != want {
		t.Fatalf("printVersion() = %q, want %q", output.String(), want)
	}
}

func TestRunVersionRejectsArguments(t *testing.T) {
	if err := runVersion([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), "version accepts no arguments") {
		t.Fatalf("runVersion() error = %v", err)
	}
}

func TestInstallHooksAllPreservesConfigAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	claudeRoot := filepath.Join(home, "claude")
	kimiRoot := filepath.Join(home, "kimi")
	opencodeRoot := filepath.Join(home, "config")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	t.Setenv("KIMI_CODE_HOME", kimiRoot)
	t.Setenv("XDG_CONFIG_HOME", opencodeRoot)
	t.Setenv("OPENCODE_CONFIG_DIR", "")

	claudePath := filepath.Join(claudeRoot, "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"theme":"dark","hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo keep"}]}]}}`), 0o640); err != nil {
		t.Fatal(err)
	}

	paths, err := installHooksAt(home, hookToolAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("installHooksAt() paths = %v, want three paths", paths)
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		before[path] = data
	}

	var settings map[string]any
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("existing Claude setting lost: %#v", settings)
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("Claude hooks = %#v, want object", settings["hooks"])
	}
	stop, ok := hooks["Stop"].([]any)
	if !ok || len(stop) != 1 {
		t.Fatalf("existing Claude Stop hook changed: %#v", hooks["Stop"])
	}
	sessionEnd, ok := hooks["SessionEnd"].([]any)
	if !ok || len(sessionEnd) != 1 || !containsHookCommand(sessionEnd) {
		t.Fatalf("Claude SessionEnd hook = %#v", hooks["SessionEnd"])
	}

	kimiData, err := os.ReadFile(filepath.Join(kimiRoot, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(kimiData), hookMarker); got != 1 || !hasKimiHook(string(kimiData)) {
		t.Fatalf("Kimi config marker count=%d content=%q", got, kimiData)
	}

	opencodeData, err := os.ReadFile(filepath.Join(opencodeRoot, "opencode", "plugins", "session-finder.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(opencodeData) != opencodePlugin || !strings.Contains(string(opencodeData), "session.idle") {
		t.Fatalf("OpenCode plugin mismatch: %q", opencodeData)
	}

	if _, err := installHooksAt(home, hookToolAll); err != nil {
		t.Fatal(err)
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("second install changed %s:\n before %q\n after  %q", path, want, got)
		}
	}
	if got := strings.Count(string(kimiData), hookMarker); got != 1 {
		t.Fatalf("initial Kimi marker count = %d, want one", got)
	}
}

func TestInstallHooksRejectsUnknownTool(t *testing.T) {
	if _, err := installHooksAt(t.TempDir(), "cursor"); err == nil || !strings.Contains(err.Error(), "invalid tool") {
		t.Fatalf("installHooksAt() error = %v", err)
	}
}

func TestInstallOpenCodeHookRefusesUnmanagedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	path := opencodePluginPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("export default {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := installOpenCodeHook(home); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("installOpenCodeHook() error = %v", err)
	}
}

func TestSessionEndHookExitsWhenBinaryMissing(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	script := filepath.Join(filepath.Dir(filename), "..", "..", "hooks", "session-end.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty-bin")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "PATH="+path)
	cmd.Stdin = strings.NewReader(`{"session_id":"ignored"}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("session-end hook error = %v", err)
	}
}

func TestUpdateJSONFileRejectsNonObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := updateJSONFile(path, func(map[string]any) (bool, error) { return true, nil })
	if err == nil || !strings.Contains(err.Error(), "top-level JSON value must be an object") {
		t.Fatalf("updateJSONFile() error = %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected missing-file error: %v", err)
	}
}
