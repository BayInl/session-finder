package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BayInl/session-finder/internal/ui"
)

const (
	hookToolClaude   = "claude"
	hookToolKimi     = "kimi"
	hookToolOpenCode = "opencode"
	hookToolAll      = "all"

	hookMarker = "session-finder skill extract --pending"

	claudeHookTimeout = 3
	kimiHookTimeout   = 1
)

// hookLockBody is deliberately POSIX sh so it can run from all supported host
// hooks. The owner file makes a lock left by a killed process reclaimable after
// ten minutes, while a live owner always wins over the age check.
const hookLockBody = `command -v session-finder >/dev/null 2>&1 || exit 0
umask 077
lock_dir="${TMPDIR:-/tmp}/session-finder-extract.lock"
lock_owner="$lock_dir/owner"
lock_ttl=600
now=$(date +%s 2>/dev/null) || exit 0
reclaim_stale_lock() {
  stale_dir=$(find "$lock_dir" -prune -mmin +10 -print -quit 2>/dev/null) || return 1
  [ -n "$stale_dir" ] || return 1
  rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || return 1
  rmdir "$lock_dir" 2>/dev/null || return 1
  mkdir "$lock_dir" 2>/dev/null || return 1
}
while :; do
  if mkdir "$lock_dir" 2>/dev/null; then
    break
  fi
  if [ -r "$lock_owner" ]; then
    if ! {
      IFS= read -r holder_pid
      IFS= read -r holder_started
    } < "$lock_owner"; then
      reclaim_stale_lock || exit 0
      break
    fi
    case "$holder_pid" in
      *[!0-9]*|"")
        reclaim_stale_lock || exit 0
        break
        ;;
    esac
    [ "$holder_pid" -gt 0 ] 2>/dev/null || exit 0
    if kill -0 "$holder_pid" 2>/dev/null; then
      exit 0
    fi
    case "$holder_started" in
      *[!0-9]*|"")
        reclaim_stale_lock || exit 0
        break
        ;;
    esac
    [ "$now" -ge "$holder_started" ] 2>/dev/null || exit 0
    [ "$((now - holder_started))" -ge "$lock_ttl" ] 2>/dev/null || exit 0
    rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || exit 0
    rmdir "$lock_dir" 2>/dev/null || exit 0
    mkdir "$lock_dir" 2>/dev/null || exit 0
    break
  fi
  reclaim_stale_lock || exit 0
  break
done
owner_tmp="$lock_owner.tmp.$$"
if ! printf "%s\n%s\n" "$$" "$now" > "$owner_tmp" 2>/dev/null; then
  rm -f "$owner_tmp" 2>/dev/null || true
  rmdir "$lock_dir" 2>/dev/null || true
  exit 0
fi
if ! mv "$owner_tmp" "$lock_owner" 2>/dev/null; then
  rm -f "$owner_tmp" 2>/dev/null || true
  rmdir "$lock_dir" 2>/dev/null || true
  exit 0
fi
cleanup() {
  rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || true
  rmdir "$lock_dir" 2>/dev/null || true
}
trap cleanup 0
trap "exit 0" HUP INT TERM
session-finder skill extract --pending >/dev/null 2>&1
`

// Keep this command detached from hosts that may wait for hook commands. The
// lock itself is owned by the long-lived child, not this short-lived wrapper.
var hookCommand = "sh -c " + shellQuote("nohup sh -c "+shellQuote(hookLockBody)+" >/dev/null 2>&1 </dev/null &")

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

var opencodePlugin = `// session-finder managed plugin: session.idle -> incremental extraction
import { spawn } from "node:child_process"

const extractionCommand = ` + "`\n" + strings.TrimSuffix(strings.ReplaceAll(hookLockBody, "${", `\${`), "\n") + "\n`" + `

export const SessionFinderPlugin = async () => ({
  event: async ({ event }: { event?: { type?: string } }) => {
    if (event?.type !== "session.idle") return
    try {
      const child = spawn("sh", ["-c", extractionCommand], {
        detached: true,
        stdio: "ignore",
      })
      child.on("error", () => {})
      child.unref()
    } catch {
      // Hooks are observational and fail open when the binary is unavailable.
    }
  },
})

export default SessionFinderPlugin
`

func runHooks(argv []string) error {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" {
		printHooksUsage(os.Stdout)
		return nil
	}
	switch argv[0] {
	case "install":
		return runHooksInstall(argv[1:])
	default:
		return fmt.Errorf("unknown hooks command %q", argv[0])
	}
}

func printHooksUsage(writer io.Writer) {
	ui.PrintUsage(writer, "usage: session-finder hooks install [--tool claude|kimi|opencode|all]", "", nil, nil)
}

func runHooksInstall(argv []string) error {
	set := flag.NewFlagSet("hooks install", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	tool := set.String("tool", hookToolAll, "tool to configure: claude, kimi, opencode, or all")
	ui.AttachUsage(set, "usage: session-finder hooks install [flags]", "Install session-idle extraction hooks.", []ui.FlagGroup{
		{Title: "Filter", Names: []string{"tool"}},
	})
	if err := ui.Parse(set, argv); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return usageError("hooks install accepts no positional arguments")
	}
	installed, err := installHooks(*tool)
	if err != nil {
		return err
	}
	for _, path := range installed {
		printInstalledHook(path)
	}
	return nil
}

func printInstalledHook(path string) {
	if ui.IsTTY(os.Stdout) {
		theme := ui.NewTheme(os.Stdout)
		fmt.Fprintf(os.Stdout, "%s %s\n", theme.Style(ui.TokenSuccess).Render("installed hook:"), path)
		return
	}
	fmt.Println("installed hook:", path)
}

func installHooks(tool string) ([]string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool != hookToolClaude && tool != hookToolKimi && tool != hookToolOpenCode && tool != hookToolAll {
		return nil, fmt.Errorf("invalid tool %q (want claude, kimi, opencode, or all)", tool)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	return installHooksAt(home, tool)
}

// installHooksAt is kept separate from installHooks so tests can use a temporary
// home without changing process-wide environment variables.
func installHooksAt(home, tool string) ([]string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool != hookToolClaude && tool != hookToolKimi && tool != hookToolOpenCode && tool != hookToolAll {
		return nil, fmt.Errorf("invalid tool %q (want claude, kimi, opencode, or all)", tool)
	}
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("home directory must not be empty")
	}
	tools := []string{tool}
	if tool == hookToolAll {
		tools = []string{hookToolClaude, hookToolKimi, hookToolOpenCode}
		if err := validateOpenCodeHookTarget(home); err != nil {
			return nil, fmt.Errorf("install %s hook: %w", hookToolOpenCode, err)
		}
	}
	installed := make([]string, 0, len(tools))
	for _, name := range tools {
		var path string
		var err error
		switch name {
		case hookToolClaude:
			path, err = installClaudeHook(home)
		case hookToolKimi:
			path, err = installKimiHook(home)
		case hookToolOpenCode:
			path, err = installOpenCodeHook(home)
		}
		if err != nil {
			return installed, fmt.Errorf("install %s hook: %w", name, err)
		}
		installed = append(installed, path)
	}
	return installed, nil
}

func claudeSettingsPath(home string) string {
	if root := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); root != "" {
		return filepath.Join(root, "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func kimiConfigPath(home string) string {
	if root := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); root != "" {
		return filepath.Join(root, "config.toml")
	}
	return filepath.Join(home, ".kimi-code", "config.toml")
}

func opencodePluginPath(home string) string {
	if root := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_DIR")); root != "" {
		return filepath.Join(root, "plugins", "session-finder.ts")
	}
	configRoot := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configRoot == "" {
		configRoot = filepath.Join(home, ".config")
	}
	return filepath.Join(configRoot, "opencode", "plugins", "session-finder.ts")
}

func installClaudeHook(home string) (string, error) {
	path := claudeSettingsPath(home)
	changed, err := updateJSONFile(path, func(root map[string]any) (bool, error) {
		hooks, err := objectField(root, "hooks")
		if err != nil {
			return false, err
		}
		sessionEnd, exists := hooks["SessionEnd"]
		if !exists || sessionEnd == nil {
			sessionEnd = []any{}
		}
		groups, ok := sessionEnd.([]any)
		if !ok {
			return false, errors.New("hooks.SessionEnd must be an array")
		}
		if containsHookCommand(groups) {
			return false, nil
		}
		groups = append(groups, map[string]any{
			"matcher": "",
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": hookCommand,
					"async":   true,
					"timeout": claudeHookTimeout,
				},
			},
		})
		hooks["SessionEnd"] = groups
		return true, nil
	})
	if err != nil {
		return "", err
	}
	_ = changed
	return path, nil
}

func objectField(root map[string]any, key string) (map[string]any, error) {
	value, exists := root[key]
	if !exists || value == nil {
		result := map[string]any{}
		root[key] = result
		return result, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
}

func containsHookCommand(value any) bool {
	switch current := value.(type) {
	case string:
		return strings.Contains(current, hookMarker)
	case []any:
		for _, item := range current {
			if containsHookCommand(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range current {
			if key == "command" {
				if command, ok := item.(string); ok && strings.Contains(command, hookMarker) {
					return true
				}
				continue
			}
			if containsHookCommand(item) {
				return true
			}
		}
	}
	return false
}

func updateJSONFile(path string, update func(map[string]any) (bool, error)) (bool, error) {
	data, mode, err := readConfigFile(path)
	if err != nil {
		return false, err
	}
	root := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return false, fmt.Errorf("parse %s: %w", path, err)
		}
		var ok bool
		root, ok = decoded.(map[string]any)
		if !ok || root == nil {
			return false, fmt.Errorf("parse %s: top-level JSON value must be an object", path)
		}
	}
	changed, err := update(root)
	if err != nil || !changed {
		return changed, err
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", path, err)
	}
	encoded = append(encoded, '\n')
	if err := writeConfigFile(path, encoded, mode); err != nil {
		return false, err
	}
	return true, nil
}

func installKimiHook(home string) (string, error) {
	path := kimiConfigPath(home)
	data, mode, err := readConfigFile(path)
	if err != nil {
		return "", err
	}
	if hasKimiHook(string(data)) {
		return path, nil
	}
	block := fmt.Sprintf("[[hooks]]\nevent = \"SessionEnd\"\nmatcher = \"\"\ncommand = %s\ntimeout = %d\n", strconv.Quote(hookCommand), kimiHookTimeout)
	updated := string(data)
	if strings.TrimSpace(updated) != "" {
		if !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		if !strings.HasSuffix(updated, "\n\n") {
			updated += "\n"
		}
	}
	updated += block
	if err := writeConfigFile(path, []byte(updated), mode); err != nil {
		return "", err
	}
	return path, nil
}

func hasKimiHook(data string) bool {
	inHooks := false
	hasEvent := false
	hasCommand := false
	finish := func() bool { return inHooks && hasEvent && hasCommand }
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[") {
			if finish() {
				return true
			}
			inHooks = trimmed == "[[hooks]]"
			hasEvent = false
			hasCommand = false
			continue
		}
		if !inHooks {
			continue
		}
		if strings.HasPrefix(trimmed, "event") && strings.Contains(trimmed, "SessionEnd") {
			hasEvent = true
		}
		if strings.HasPrefix(trimmed, "command") && strings.Contains(trimmed, hookMarker) {
			hasCommand = true
		}
	}
	return finish()
}

func validateOpenCodeHookTarget(home string) error {
	path := opencodePluginPath(home)
	data, _, err := readConfigFile(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	contents := string(data)
	if strings.Contains(contents, "session-finder managed plugin") && strings.Contains(contents, "session.idle") {
		return nil
	}
	return fmt.Errorf("refusing to overwrite existing plugin %s", path)
}

func installOpenCodeHook(home string) (string, error) {
	path := opencodePluginPath(home)
	data, mode, err := readConfigFile(path)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if strings.Contains(string(data), "session-finder managed plugin") && strings.Contains(string(data), "session.idle") {
			return path, nil
		}
		return "", fmt.Errorf("refusing to overwrite existing plugin %s", path)
	}
	if err := writeConfigFile(path, []byte(opencodePlugin), mode); err != nil {
		return "", err
	}
	return path, nil
}

func readConfigFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, fmt.Errorf("%s is a directory", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func writeConfigFile(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	targetPath := path
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			targetPath, err = filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve configuration symlink %s: %w", path, err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(targetDir, ".session-finder-hook-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return err
	}
	return nil
}
