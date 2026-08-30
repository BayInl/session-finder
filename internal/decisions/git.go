package decisions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrGitUnavailable = errors.New("git is unavailable")

// GitQuery describes a bounded, read-only git log lookup. CWD and time range
// narrow candidate commits; Paths optionally require path overlap.
type GitQuery struct {
	CWD        string
	After      time.Time
	Before     time.Time
	Paths      []string
	MaxCommits int
}

// GitRunner is injectable for tests and guarantees no mutation commands are
// required by this package. Production uses `git log` and `git show` only.
type GitRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type commandGit struct{}

func (commandGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	return command.Output()
}

// FindCommitCandidates returns commits that overlap the decision's session
// window and paths. It never writes notes, refs, index files, or history.
func FindCommitCandidates(ctx context.Context, query GitQuery) ([]CommitRef, error) {
	return FindCommits(ctx, commandGit{}, query)
}

// FindCommits is the injectable form of FindCommitCandidates.
func FindCommits(ctx context.Context, runner GitRunner, query GitQuery) ([]CommitRef, error) {
	if runner == nil {
		return nil, ErrGitUnavailable
	}
	dir := strings.TrimSpace(query.CWD)
	if dir == "" {
		dir = "."
	}
	args := []string{"log", "--all", "--no-merges", "--date-order", "--format=%H%x1f%h%x1f%s%x1f%an%x1f%aI"}
	if !query.After.IsZero() {
		args = append(args, "--since="+query.After.UTC().Format(time.RFC3339))
	}
	if !query.Before.IsZero() {
		args = append(args, "--until="+query.Before.UTC().Format(time.RFC3339))
	}
	args = append(args, "--")
	for _, path := range normalizePaths(query.Paths) {
		args = append(args, path)
	}
	data, err := runner.Run(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGitUnavailable, err)
	}
	rows := bytes.Split(data, []byte{'\n'})
	result := make([]CommitRef, 0, len(rows))
	for _, row := range rows {
		fields := strings.Split(string(row), "\x1f")
		if len(fields) < 5 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		ref := CommitRef{Hash: fields[0], ShortHash: fields[1], Subject: fields[2], Author: fields[3], Timestamp: fields[4]}
		ref.Paths = commitPaths(ctx, runner, dir, ref.Hash)
		if len(query.Paths) > 0 && !pathOverlap(query.Paths, ref.Paths) {
			continue
		}
		result = append(result, ref)
		if query.MaxCommits > 0 && len(result) >= query.MaxCommits {
			break
		}
	}
	return result, nil
}

func commitPaths(ctx context.Context, runner GitRunner, dir, hash string) []string {
	data, err := runner.Run(ctx, dir, "show", "--format=", "--name-only", "--no-renames", hash)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	paths := make([]string, 0)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		path := strings.TrimSpace(scanner.Text())
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func normalizePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		path = strings.TrimPrefix(path, "./")
		if path != "" && !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}

func pathOverlap(wanted, actual []string) bool {
	for _, left := range normalizePaths(wanted) {
		for _, right := range normalizePaths(actual) {
			if left == right || strings.HasPrefix(right, left+"/") || strings.HasPrefix(left, right+"/") {
				return true
			}
		}
	}
	return false
}

// SelectCommitCandidates is the only association helper exposed to an LLM
// adapter: a model may choose from these exact refs, never fabricate a hash.
func SelectCommitCandidates(candidates []CommitRef, hashes []string) ([]CommitRef, error) {
	if len(hashes) == 0 {
		return []CommitRef{}, nil
	}
	byHash := map[string]CommitRef{}
	for _, candidate := range candidates {
		byHash[candidate.Hash] = candidate
		if candidate.ShortHash != "" {
			byHash[candidate.ShortHash] = candidate
		}
	}
	selected := make([]CommitRef, 0, len(hashes))
	seen := map[string]bool{}
	for _, hash := range hashes {
		ref, ok := byHash[strings.TrimSpace(hash)]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrCommitNotCandidate, hash)
		}
		if seen[ref.Hash] {
			continue
		}
		seen[ref.Hash] = true
		selected = append(selected, ref)
	}
	return selected, nil
}
