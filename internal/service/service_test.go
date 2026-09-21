package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
)

type call struct {
	name string
	args []string
}
type fakeRunner struct {
	calls     []call
	responses map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, call{name, args})
	key := name + " " + strings.Join(args, " ")
	if value, ok := f.responses[key]; ok {
		return value, nil
	}
	if strings.HasPrefix(key, "docker inspect") {
		return "Error: No such object\n", fmt.Errorf("missing")
	}
	if strings.HasPrefix(key, "docker image inspect") {
		return "missing", fmt.Errorf("missing")
	}
	return "", nil
}

func TestStartCreatesContainerWithPersistentMounts(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	f := &fakeRunner{responses: map[string]string{}}
	m := Manager{Config: c, Runner: f}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var run call
	for _, item := range f.calls {
		if len(item.args) > 0 && item.args[0] == "run" {
			run = item
		}
	}
	got := strings.Join(run.args, " ")
	for _, want := range []string{c.APTDir(), c.OCIDir(), c.ConfigDir(), "127.0.0.1:3142:3142", c.Image} {
		if !strings.Contains(got, want) {
			t.Errorf("run arguments do not include %q: %s", want, got)
		}
	}
}

func TestStartIsIdempotentWhenRunning(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	f := &fakeRunner{responses: map[string]string{"docker inspect --format {{.State.Status}} " + ContainerName: "running\n"}}
	if err := (Manager{Config: c, Runner: f}).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, item := range f.calls {
		if len(item.args) > 0 && item.args[0] == "run" {
			t.Fatal("started an already running container")
		}
	}
}
