package llm

import (
	"errors"
	"os"
	"strings"
)

const (
	JudgeOff  = "off"
	JudgeAuto = "auto"
	JudgeOn   = "on"
)

// JudgeMode normalizes the command-line/environment judge selector.
func JudgeMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = JudgeAuto
	}
	switch mode {
	case JudgeOff, JudgeAuto, JudgeOn:
		return mode, nil
	default:
		return "", errors.New("judge must be off, auto, or on")
	}
}

// EnvJudgeMode reads the shared opt-in switch. An unset value is auto/offline.
func EnvJudgeMode() string { return strings.TrimSpace(os.Getenv("SESSION_FINDER_LLM_JUDGE")) }
