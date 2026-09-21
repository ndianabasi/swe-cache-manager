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

func TestRuntimeZotConfigurationEnablesDockerHubPullThroughCache(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	m := Manager{Config: c}
	if err := m.GenerateRuntimeConfig(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.OCIConfigDir(), "zot.json"))
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
	httpConfig := generated["http"].(map[string]any)
	if registry["onDemand"] != true || httpConfig["port"] != fmt.Sprint(c.OCI.Port) {
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
	for _, want := range []string{c.APTDir(), c.OCIDir(), c.APTConfigDir(), c.OCIConfigDir(), c.SupervisorConfigDir(), fmt.Sprintf("127.0.0.1:%d:%d", c.APT.Port, c.APT.Port), fmt.Sprintf("127.0.0.1:%d:%d", c.OCI.Port, c.OCI.Port), c.Image} {
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
