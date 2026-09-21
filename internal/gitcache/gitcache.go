// Package gitcache maintains independent bare Git mirrors on the host. It
// deliberately does not run a Git server or place mirrors in the service
// container.
package gitcache

import (
	"context"
	"errors"
	"fmt"
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

type Manager struct {
	Root     string
	Runner   Runner
	LockWait time.Duration
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
	unlock, err := acquireLock(mirror+".lock", m.lockWait())
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
	if _, err := m.Runner.Run(ctx, "git", "clone", "--no-local", mirror, destination); err != nil {
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
	if _, err := m.Runner.Run(ctx, "git", "clone", "--mirror", repository, temporary); err != nil {
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

func acquireLock(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		err := os.Mkdir(path, 0700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create repository lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for repository lock: %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
