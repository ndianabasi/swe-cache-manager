package gitcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMirrorPath(t *testing.T) {
	m := Manager{Root: "/cache/git"}
	for _, raw := range []string{"https://github.com/pixijs/pixijs.git", "git@github.com:pixijs/pixijs.git", "ssh://git@github.com/pixijs/pixijs"} {
		got, err := m.MirrorPath(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != "/cache/git/github.com/pixijs/pixijs.git" {
			t.Fatalf("%s -> %s", raw, got)
		}
	}
}

func TestMirrorPathRejectsTraversal(t *testing.T) {
	_, err := (Manager{Root: "/cache"}).MirrorPath("https://github.com/org/../secret")
	if err == nil {
		t.Fatal("expected unsafe URL rejection")
	}
}

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if strings.Contains(strings.Join(args, " "), "cat-file") {
		return "", fmt.Errorf("not found")
	}
	return "", nil
}

func TestCloneUsesIndependentLocalCloneAndRestoresOrigin(t *testing.T) {
	root := t.TempDir()
	f := &fakeRunner{}
	m := Manager{Root: root, Runner: f}
	mirror, err := m.MirrorPath("https://github.com/org/project.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mirror, 0750); err != nil {
		t.Fatal(err)
	}
	if err := m.Clone(context.Background(), "https://github.com/org/project.git", root+"/checkout", ""); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "clone --progress --no-local") || !strings.Contains(joined, "remote set-url origin https://github.com/org/project.git") {
		t.Fatalf("unexpected commands:\n%s", joined)
	}
}

type streamingFakeRunner struct{ fakeRunner }

func (f *streamingFakeRunner) RunStream(_ context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	_, _ = fmt.Fprint(stdout, "Cloning into 'checkout'...\n")
	_, _ = fmt.Fprint(stderr, "Receiving objects: 100%\n")
	return nil
}

func TestCloneStreamsGitProgress(t *testing.T) {
	root := t.TempDir()
	f := &streamingFakeRunner{}
	mirror, err := (Manager{Root: root}).MirrorPath("https://github.com/org/project.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mirror, 0750); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	m := Manager{Root: root, Runner: f, Output: &out, ErrorOutput: &errOut}
	if err := m.Clone(context.Background(), "https://github.com/org/project.git", filepath.Join(root, "checkout"), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "clone --progress --no-local") {
		t.Fatalf("clone did not request progress: %s", strings.Join(f.calls, "\n"))
	}
	if got := out.String(); got != "Cloning into 'checkout'...\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := errOut.String(); got != "Receiving objects: 100%\n" {
		t.Errorf("stderr = %q", got)
	}
}

type failingRunner struct{ fakeRunner }

func (f *failingRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if strings.Contains(strings.Join(args, " "), "clone") {
		return "", context.Canceled
	}
	return "", nil
}

func TestCloneRemovesLockAfterFailure(t *testing.T) {
	root := t.TempDir()
	f := &failingRunner{}
	m := Manager{Root: root, Runner: f}
	mirror, err := m.MirrorPath("https://github.com/org/project.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Clone(context.Background(), "https://github.com/org/project.git", filepath.Join(root, "checkout"), ""); err == nil {
		t.Fatal("Clone succeeded, want failure")
	}
	if _, err := os.Stat(mirror + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock remains after failed clone: %v", err)
	}
}

type cancellationRunner struct {
	started chan struct{}
}

func (f *cancellationRunner) Run(ctx context.Context, _ string, args ...string) (string, error) {
	if strings.Contains(strings.Join(args, " "), "clone") {
		close(f.started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "", nil
}

func TestCloneRemovesLockAfterCancellation(t *testing.T) {
	root := t.TempDir()
	f := &cancellationRunner{started: make(chan struct{})}
	m := Manager{Root: root, Runner: f}
	mirror, err := m.MirrorPath("https://github.com/org/project.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mirror, 0750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.Clone(ctx, "https://github.com/org/project.git", filepath.Join(root, "checkout"), "")
	}()
	<-f.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Clone error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(mirror + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock remains after canceled clone: %v", err)
	}
}

func TestCloneForceRemovesExistingLock(t *testing.T) {
	root := t.TempDir()
	f := &fakeRunner{}
	m := Manager{Root: root, Runner: f, ForceLock: true}
	mirror, err := m.MirrorPath("https://github.com/org/project.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mirror, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mirror+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Clone(context.Background(), "https://github.com/org/project.git", filepath.Join(root, "checkout"), ""); err != nil {
		t.Fatalf("forced clone: %v", err)
	}
	if _, err := os.Stat(mirror + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock remains after forced clone: %v", err)
	}
}
