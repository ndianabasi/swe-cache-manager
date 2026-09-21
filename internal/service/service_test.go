package service

import (
	"context"
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

func TestRuntimeDistributionConfigurationEnablesDockerHubPullThroughCache(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	m := Manager{Config: c}
	if err := m.GenerateRuntimeConfig(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.RegistryConfigDir(), "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	registry := string(b)
	for _, want := range []string{"version: 0.1", "rootdirectory: /var/lib/registry", fmt.Sprintf("addr: 0.0.0.0:%d", c.OCI.Port), "remoteurl: \"" + c.OCI.Upstream + "\""} {
		if !strings.Contains(registry, want) {
			t.Errorf("Distribution configuration does not include %q:\n%s", want, registry)
		}
	}
	verdaccio, err := os.ReadFile(filepath.Join(c.NPMConfigDir(), "verdaccio.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(verdaccio), "https://registry.npmjs.org/") || !strings.Contains(string(verdaccio), fmt.Sprintf("0.0.0.0:%d", c.NPM.Port)) {
		t.Fatalf("unexpected Verdaccio configuration:\n%s", verdaccio)
	}
	supervisor, err := os.ReadFile(filepath.Join(c.SupervisorConfigDir(), "swe-cache.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(supervisor), "ATHENS_STORAGE_TYPE=disk") || !strings.Contains(string(supervisor), fmt.Sprintf("ATHENS_PORT=:%d", c.Go.Port)) {
		t.Fatalf("unexpected Athens configuration:\n%s", supervisor)
	}
	if strings.Contains(string(supervisor), "{{ .GoPort }}") {
		t.Fatalf("unrendered supervisord template:\n%s", supervisor)
	}
	if !strings.Contains(string(supervisor), "[program:distribution]") || strings.Contains(string(supervisor), "zot") {
		t.Fatalf("unexpected Distribution supervisor configuration:\n%s", supervisor)
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
	for _, want := range []string{c.APTDir(), c.RegistryDir(), c.NPMDir(), c.GoDir(), c.APTConfigDir(), c.RegistryConfigDir(), c.NPMConfigDir(), c.SupervisorConfigDir(), fmt.Sprintf("127.0.0.1:%d:%d", c.APT.Port, c.APT.Port), fmt.Sprintf("127.0.0.1:%d:%d", c.OCI.Port, c.OCI.Port), fmt.Sprintf("127.0.0.1:%d:%d", c.NPM.Port, c.NPM.Port), fmt.Sprintf("127.0.0.1:%d:%d", c.Go.Port, c.Go.Port), c.Image} {
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

func TestWarmStartsCacheThenPullsEachImage(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	f := &fakeRunner{responses: map[string]string{}}
	output, err := (Manager{Config: c, Runner: f}).Warm(context.Background(), []string{"node:24-bookworm", "docker/dockerfile:1.7"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 2 {
		t.Fatalf("got %d pull outputs, want 2", len(output))
	}
	joined := ""
	for _, item := range f.calls {
		joined += item.name + " " + strings.Join(item.args, " ") + "\n"
	}
	for _, want := range []string{"docker pull node:24-bookworm", "docker pull docker/dockerfile:1.7"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warm commands do not include %q:\n%s", want, joined)
		}
	}
}

func TestEnsureImageBuildsOnlyWhenMissing(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	f := &fakeRunner{responses: map[string]string{}}
	if err := (Manager{Config: c, Runner: f}).EnsureImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, item := range f.calls {
		joined += item.name + " " + strings.Join(item.args, " ") + "\n"
	}
	if !strings.Contains(joined, "docker build --build-arg TARGETARCH=") || !strings.Contains(joined, "--tag "+c.Image) {
		t.Fatalf("missing image build command:\n%s", joined)
	}
}

func TestEnsureImageDoesNotRebuildExistingTag(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	f := &fakeRunner{responses: map[string]string{"docker image inspect " + c.Image: "existing"}}
	if err := (Manager{Config: c, Runner: f}).EnsureImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, item := range f.calls {
		if len(item.args) > 0 && item.args[0] == "build" {
			t.Fatal("rebuilt existing image")
		}
	}
}

func TestEmbeddedBuildContextIsPresent(t *testing.T) {
	if len(embeddedDockerfile) == 0 || len(embeddedSupervisorConfig) == 0 {
		t.Fatal("embedded service build context is empty")
	}
	if !strings.Contains(string(embeddedSupervisorConfig), "{{ .GoPort }}") {
		t.Fatal("embedded supervisor asset is not a Go template")
	}
}
