package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
)

type call struct {
	name string
	args []string
}

func TestRuntimeZotConfigurationEnablesDigestPreservingDockerHubCache(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	m := Manager{Config: c}
	if err := m.GenerateRuntimeConfig(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.ConfigDir(), "zot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err := json.Unmarshal(b, &generated); err != nil {
		t.Fatal(err)
	}
	extensions := generated["extensions"].(map[string]any)
	sync := extensions["sync"].(map[string]any)
	registry := sync["registries"].([]any)[0].(map[string]any)
	compatibility := generated["compatibility"].(map[string]any)["docker"].(map[string]any)
	if registry["preserveDigest"] != true || registry["onDemand"] != true || compatibility["v2"] != true {
		t.Fatalf("unexpected sync config: %#v", registry)
	}
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
