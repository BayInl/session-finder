package skill

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Frontmatter is the subset of Agent Skills frontmatter emitted by this MVP.
// name and description are mandatory per the open standard.
type Frontmatter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RenderSkillMarkdown renders a short, portable Agent Skills file. Evidence and
// quality metadata are deliberately omitted; they remain in the local store.
func RenderSkillMarkdown(bundle CandidateBundle) (string, error) {
	bundle = normalizeBundle(bundle)
	if err := ValidateSlug(bundle.Slug); err != nil {
		return "", err
	}
	if strings.TrimSpace(bundle.Trigger) == "" {
		return "", fmt.Errorf("%w: description is required", ErrInvalidFrontmatter)
	}
	if strings.TrimSpace(bundle.Instructions) == "" {
		return "", fmt.Errorf("%w: instructions are required", ErrInvalidFrontmatter)
	}
	if ContainsSensitiveInformation(bundle) {
		return "", ErrSensitiveContent
	}
	description := strings.Join(strings.Fields(bundle.Trigger), " ")
	if len([]rune(description)) > 1024 {
		description = string([]rune(description)[:1023]) + "…"
	}
	body := cleanInstructionText(bundle.Instructions)
	if body == "" {
		return "", fmt.Errorf("%w: instructions are required", ErrInvalidFrontmatter)
	}
	var output strings.Builder
	output.WriteString("---\n")
	output.WriteString("name: ")
	output.WriteString(bundle.Slug)
	output.WriteByte('\n')
	output.WriteString("description: ")
	output.WriteString(strconv.Quote(description))
	output.WriteString("\n---\n\n")
	output.WriteString(body)
	output.WriteString("\n")
	markdown := output.String()
	if _, err := ParseAndValidateSkillMarkdown(markdown, bundle.Slug); err != nil {
		return "", err
	}
	return markdown, nil
}

// Render is a short alias for RenderSkillMarkdown.
func Render(bundle CandidateBundle) (string, error) { return RenderSkillMarkdown(bundle) }

// ParseAndValidateSkillMarkdown validates frontmatter and returns its fields.
// expectedName may be empty when validating a standalone file.
func ParseAndValidateSkillMarkdown(markdown, expectedName string) (Frontmatter, error) {
	var frontmatter Frontmatter
	if strings.TrimSpace(markdown) == "" {
		return frontmatter, ErrInvalidFrontmatter
	}
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) != "---" {
		return frontmatter, fmt.Errorf("%w: missing opening delimiter", ErrInvalidFrontmatter)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return frontmatter, fmt.Errorf("%w: missing closing delimiter", ErrInvalidFrontmatter)
	}
	seenName, seenDescription := false, false
	for _, line := range lines[1:end] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return frontmatter, fmt.Errorf("%w: malformed line %q", ErrInvalidFrontmatter, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			if seenName || value == "" {
				return frontmatter, fmt.Errorf("%w: name is missing or duplicated", ErrInvalidFrontmatter)
			}
			frontmatter.Name = strings.Trim(value, "\"")
			seenName = true
		case "description":
			if seenDescription || value == "" {
				return frontmatter, fmt.Errorf("%w: description is missing or duplicated", ErrInvalidFrontmatter)
			}
			if strings.HasPrefix(value, "\"") {
				decoded, err := strconv.Unquote(value)
				if err != nil {
					return frontmatter, fmt.Errorf("%w: invalid description quoting", ErrInvalidFrontmatter)
				}
				value = decoded
			}
			frontmatter.Description = value
			seenDescription = true
		default:
			// The standard permits additional keys, but this MVP rejects unknown
			// keys to keep output auditable and avoid accidentally storing evidence.
			return frontmatter, fmt.Errorf("%w: unsupported key %q", ErrInvalidFrontmatter, key)
		}
	}
	if !seenName || !seenDescription {
		return frontmatter, fmt.Errorf("%w: name and description are required", ErrInvalidFrontmatter)
	}
	if err := ValidateSlug(frontmatter.Name); err != nil {
		return frontmatter, fmt.Errorf("%w: frontmatter name %q", ErrInvalidFrontmatter, frontmatter.Name)
	}
	if expectedName != "" && frontmatter.Name != expectedName {
		return frontmatter, fmt.Errorf("%w: name %q does not match %q", ErrInvalidFrontmatter, frontmatter.Name, expectedName)
	}
	if strings.TrimSpace(frontmatter.Description) == "" || len([]rune(frontmatter.Description)) > 1024 {
		return frontmatter, fmt.Errorf("%w: description must be 1-1024 characters", ErrInvalidFrontmatter)
	}
	if SensitiveInformation(markdown) {
		return frontmatter, ErrSensitiveContent
	}
	if end+1 >= len(lines) || strings.TrimSpace(strings.Join(lines[end+1:], "")) == "" {
		return frontmatter, fmt.Errorf("%w: body is required", ErrInvalidFrontmatter)
	}
	return frontmatter, nil
}

// ValidateFrontmatter is an explicit alias retained for integrations.
func ValidateFrontmatter(markdown, expectedName string) error {
	_, err := ParseAndValidateSkillMarkdown(markdown, expectedName)
	return err
}

// ValidateSkillMarkdown validates a rendered file and checks that its directory
// name agrees with frontmatter when path is supplied.
func ValidateSkillMarkdown(markdown, directory string) error {
	expected := ""
	if directory != "" {
		expected = filepath.Base(filepath.Clean(directory))
	}
	_, err := ParseAndValidateSkillMarkdown(markdown, expected)
	return err
}

func userHomeDir(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

func resolveUserSkillsRoot(home, target string) (string, error) {
	if target == TargetClaude {
		return filepath.Join(home, ".claude", "skills"), nil
	}
	if target == TargetKimi {
		return filepath.Join(home, ".agents", "skills"), nil
	}
	configRoot := filepath.Join(home, ".config", "agents", "skills")
	legacyRoot := filepath.Join(home, ".agents", "skills")
	if info, err := os.Stat(configRoot); err == nil && info.IsDir() {
		return configRoot, nil
	}
	if info, err := os.Stat(legacyRoot); err == nil && info.IsDir() {
		return legacyRoot, nil
	}
	return configRoot, nil
}

func publishRoot(options PublishOptions) (string, error) {
	target := strings.ToLower(strings.TrimSpace(options.Target))
	if target == "" {
		target = TargetGeneric
	}
	switch target {
	case TargetGeneric, TargetClaude, TargetKimi:
		if options.SkillsRoot != "" {
			return filepath.Clean(options.SkillsRoot), nil
		}
		home, err := userHomeDir(options.HomeDir)
		if err != nil {
			return "", err
		}
		return resolveUserSkillsRoot(home, target)
	case TargetProject:
		if strings.TrimSpace(options.ProjectDir) == "" {
			return "", fmt.Errorf("%w: project directory is required", ErrInvalidTarget)
		}
		if options.SkillsRoot != "" {
			return filepath.Clean(options.SkillsRoot), nil
		}
		return filepath.Join(filepath.Clean(options.ProjectDir), ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidTarget, target)
	}
}

// Publish writes an immutable skill directory and never overwrites an existing
// skill with the same slug. The returned path points to the skill directory.
func Publish(bundle CandidateBundle, options PublishOptions) (PublishResult, error) {
	bundle = normalizeBundle(bundle)
	if err := ValidateSlug(bundle.Slug); err != nil {
		return PublishResult{}, err
	}
	if err := ValidateQualityForPublish(bundle); err != nil {
		return PublishResult{}, err
	}
	markdown, err := RenderSkillMarkdown(bundle)
	if err != nil {
		return PublishResult{}, err
	}
	if err := ValidateSkillMarkdown(markdown, bundle.Slug); err != nil {
		return PublishResult{}, err
	}
	root, err := publishRoot(options)
	if err != nil {
		return PublishResult{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return PublishResult{}, err
	}
	destination := filepath.Join(root, bundle.Slug)
	// Mkdir, rather than MkdirAll, is intentional: an existing slug is a
	// conflict and must enter the edit/new-slug workflow, never be overwritten.
	if err := os.Mkdir(destination, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return PublishResult{Target: options.Target, Slug: bundle.Slug, Path: destination}, ErrSkillConflict
		}
		return PublishResult{}, err
	}
	filePath := filepath.Join(destination, "SKILL.md")
	if err := atomicWriteFile(filePath, []byte(markdown), 0o644); err != nil {
		_ = os.RemoveAll(destination)
		return PublishResult{}, err
	}
	return PublishResult{Target: strings.ToLower(strings.TrimSpace(options.Target)), Slug: bundle.Slug, Path: destination}, nil
}

// PublishSkill is an explicit alias for Publish.
func PublishSkill(bundle CandidateBundle, options PublishOptions) (PublishResult, error) {
	return Publish(bundle, options)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".SKILL.md-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

// ReadSkillMarkdown reads and validates one local skill file.
func ReadSkillMarkdown(path string) (string, Frontmatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", Frontmatter{}, err
	}
	directory := filepath.Base(filepath.Dir(path))
	frontmatter, err := ParseAndValidateSkillMarkdown(string(data), directory)
	if err != nil {
		return "", Frontmatter{}, err
	}
	return string(data), frontmatter, nil
}

// FrontmatterFromReader parses only frontmatter from a markdown reader. It is
// useful for callers validating files without reading the body twice.
func FrontmatterFromReader(reader *bufio.Reader) (Frontmatter, error) {
	var frontmatter Frontmatter
	var buffer bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		buffer.WriteString(line)
		if strings.TrimSpace(line) == "---" && buffer.Len() > len(line) {
			break
		}
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return frontmatter, err
			}
			break
		}
	}
	frontmatter, err := ParseAndValidateSkillMarkdown(buffer.String()+"\nbody\n", "")
	return frontmatter, err
}
