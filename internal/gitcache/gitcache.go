// Package gitcache maintains independent bare Git mirrors on the host. It
// deliberately does not run a Git server or place mirrors in the service
// container.
package gitcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var scpURL = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

// StreamingRunner is implemented by command runners that can forward a
// command's output before it exits. Manager still accepts a Runner so callers
// with a non-streaming programmatic runner remain supported.
type StreamingRunner interface {
	RunStream(context.Context, io.Writer, io.Writer, string, ...string) error
}

type Manager struct {
	Root        string
	Runner      Runner
	LockWait    time.Duration
	ForceLock   bool
	Output      io.Writer
	ErrorOutput io.Writer
}

func (m Manager) lockWait() time.Duration {
	if m.LockWait > 0 {
		return m.LockWait
	}
	return 30 * time.Second
}

// MirrorPath maps equivalent supported URLs to a deterministic, traversal-safe
// bare-mirror path such as <root>/github.com/owner/project.git.
func (m Manager) MirrorPath(rawURL string) (string, error) {
	host, repo, err := splitURL(rawURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSuffix(repo, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("repository URL must include an owner and repository")
	}
	for _, part := range append([]string{host}, parts...) {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "/") || strings.Contains(part, "\\") {
			return "", fmt.Errorf("repository URL contains an unsafe path component %q", part)
		}
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, ".git") {
		parts[len(parts)-1] = last + ".git"
	}
	return filepath.Join(append([]string{m.Root, host}, parts...)...), nil
}

func splitURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		match := scpURL.FindStringSubmatch(raw)
		if match != nil {
			return match[1], match[2], nil
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path == "" {
		return "", "", fmt.Errorf("unsupported repository URL %q", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git" {
		return "", "", fmt.Errorf("unsupported repository URL scheme %q", u.Scheme)
	}
	return u.Hostname(), strings.TrimPrefix(u.EscapedPath(), "/"), nil
}

func (m Manager) Clone(ctx context.Context, repository, destination, commit string) error {
	if m.Root == "" {
		return errors.New("Git cache root is required")
	}
	if m.Runner == nil {
		return errors.New("Git command runner is required")
	}
	mirror, err := m.MirrorPath(repository)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0750); err != nil {
		return fmt.Errorf("create mirror parent: %w", err)
	}
	unlock, err := acquireLock(ctx, mirror+".lock", m.lockWait(), m.ForceLock)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(mirror); errors.Is(err, os.ErrNotExist) {
		if err := m.createMirror(ctx, repository, mirror); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("inspect mirror: %w", err)
	}
	if commit != "" && !m.hasCommit(ctx, mirror, commit) {
		if _, err := m.Runner.Run(ctx, "git", "-C", mirror, "fetch", "origin", commit); err != nil {
			return fmt.Errorf("fetch requested commit %s: %w", commit, err)
		}
		if !m.hasCommit(ctx, mirror, commit) {
			return fmt.Errorf("requested commit %s was not received from upstream", commit)
		}
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// --no-local makes an independent clone rather than a hardlinked or
	// alternate-object clone. Later mirror maintenance cannot break workspaces.
	if err := m.runClone(ctx, "--no-local", mirror, destination); err != nil {
		return fmt.Errorf("clone cached mirror: %w", err)
	}
	if _, err := m.Runner.Run(ctx, "git", "-C", destination, "remote", "set-url", "origin", repository); err != nil {
		return fmt.Errorf("restore upstream origin: %w", err)
	}
	if commit != "" {
		if _, err := m.Runner.Run(ctx, "git", "-C", destination, "checkout", "--detach", commit); err != nil {
			return fmt.Errorf("checkout requested commit: %w", err)
		}
	}
	return nil
}

func (m Manager) createMirror(ctx context.Context, repository, mirror string) error {
	temporary := mirror + ".partial-" + fmt.Sprintf("%d", time.Now().UnixNano())
	defer os.RemoveAll(temporary)
	if err := m.runClone(ctx, "--mirror", repository, temporary); err != nil {
		return fmt.Errorf("create mirror: %w", err)
	}
	if err := os.Rename(temporary, mirror); err != nil {
		return fmt.Errorf("publish mirror: %w", err)
	}
	return nil
}

func (m Manager) hasCommit(ctx context.Context, mirror, commit string) bool {
	_, err := m.Runner.Run(ctx, "git", "-C", mirror, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func (m Manager) runClone(ctx context.Context, args ...string) error {
	// Git suppresses its useful progress meter when stderr is not a terminal;
	// --progress makes it visible through pipes and captured CLI output too.
	args = append([]string{"clone", "--progress"}, args...)
	if runner, ok := m.Runner.(StreamingRunner); ok {
		return runner.RunStream(ctx, m.output(), m.errorOutput(), "git", args...)
	}
	output, err := m.Runner.Run(ctx, "git", args...)
	_, _ = fmt.Fprint(m.output(), output)
	return err
}

func (m Manager) output() io.Writer {
	if m.Output != nil {
		return m.Output
	}
	return io.Discard
}

func (m Manager) errorOutput() io.Writer {
	if m.ErrorOutput != nil {
		return m.ErrorOutput
	}
	return io.Discard
}

func acquireLock(ctx context.Context, path string, wait time.Duration, force bool) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := os.Mkdir(path, 0700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create repository lock: %w", err)
		}
		if force {
			// Remove only the lock observed when --force was requested. A new
			// lock that appears afterwards is treated normally, so the flag
			// cannot repeatedly disrupt a concurrently-started clone.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("remove stale repository lock: %w", err)
			}
			force = false
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for repository lock: %s", path)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
