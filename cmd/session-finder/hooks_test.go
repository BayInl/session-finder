package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	manualPlugin, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "hooks", "opencode-session-idle.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opencodeData, manualPlugin) {
		t.Fatalf("generated OpenCode plugin differs from manual asset")
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

func TestSessionEndHookRunsExtraction(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	script := filepath.Join(filepath.Dir(filename), "..", "..", "hooks", "session-end.sh")
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "invoked")
	fake := filepath.Join(bin, "session-finder")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s' \"$*\" > \"$HOOK_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(),
		"TMPDIR="+root,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOOK_MARKER="+marker,
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"ignored"}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("session-end hook error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			if string(data) != "skill extract --pending" {
				t.Fatalf("session-end hook arguments = %q", data)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read extraction marker: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("session-end hook did not run extraction")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHookLockReclaimsStaleOwner(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "invoked")
	fakeSessionFinder := filepath.Join(fakeBin, "session-finder")
	if err := os.WriteFile(fakeSessionFinder, []byte("#!/bin/sh\nprintf invoked > \"$HOOK_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	lockDir := filepath.Join(root, "session-finder-extract.lock")
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-11 * time.Minute).Unix()
	owner := fmt.Sprintf("99999999\n%d\n", started)
	if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMPDIR", root)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOOK_MARKER", marker)
	if err := exec.Command("/bin/sh", "-c", hookLockBody).Run(); err != nil {
		t.Fatalf("stale lock command failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("stale lock did not run extraction: %v", err)
	}
	if _, err := os.Stat(lockDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale lock directory remains: %v", err)
	}
}

func TestHookLockKeepsLiveOwner(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "invoked")
	fakeSessionFinder := filepath.Join(fakeBin, "session-finder")
	if err := os.WriteFile(fakeSessionFinder, []byte("#!/bin/sh\nprintf invoked > \"$HOOK_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	lockDir := filepath.Join(root, "session-finder-extract.lock")
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-11 * time.Minute).Unix()
	owner := fmt.Sprintf("%d\n%d\n", os.Getpid(), started)
	ownerPath := filepath.Join(lockDir, "owner")
	if err := os.WriteFile(ownerPath, []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMPDIR", root)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOOK_MARKER", marker)
	if err := exec.Command("/bin/sh", "-c", hookLockBody).Run(); err != nil {
		t.Fatalf("live lock command failed: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live owner lock was reclaimed: %v", err)
	}
	got, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatalf("live owner lock was removed: %v", err)
	}
	if string(got) != owner {
		t.Fatalf("live owner metadata changed: got %q, want %q", got, owner)
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
