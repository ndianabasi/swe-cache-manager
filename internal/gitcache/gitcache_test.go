package gitcache

import (
	"context"
	"fmt"
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
	if !strings.Contains(joined, "clone --no-local") || !strings.Contains(joined, "remote set-url origin https://github.com/org/project.git") {
		t.Fatalf("unexpected commands:\n%s", joined)
	}
}
