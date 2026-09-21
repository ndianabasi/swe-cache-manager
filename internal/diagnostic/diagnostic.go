// Package diagnostic provides read-mostly health and cache-use inspection.
package diagnostic

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
	"github.com/ndianabasi/swe-cache-manager/internal/service"
)

type Report struct {
	Container string
	APT       string
	OCI       string
	NPM       string
	Go        string
	Docker    string
	Git       string
	Paths     map[string]string
}

func Status(ctx context.Context, c config.Config, runner service.Runner) Report {
	m := service.Manager{Config: c, Runner: runner}
	state, err := m.State(ctx)
	if err != nil || state == "" {
		state = "not created"
	}
	r := Report{Container: state, APT: "disabled", OCI: "disabled", NPM: "disabled", Go: "disabled", Paths: map[string]string{"apt": c.APTDir(), "oci": c.OCIDir(), "git": c.GitDir(), "npm": c.NPMDir(), "go": c.GoDir()}}
	if c.APT.Enabled {
		r.APT = endpointHealth(c.APT.Port, "/")
	}
	if c.OCI.Enabled {
		r.OCI = endpointHealth(c.OCI.Port, "/v2/")
	}
	if c.NPM.Enabled {
		r.NPM = endpointHealth(c.NPM.Port, "/-/ping")
	}
	if c.Go.Enabled {
		r.Go = endpointHealth(c.Go.Port, "/")
	}
	return r
}

func Doctor(ctx context.Context, c config.Config, runner service.Runner) Report {
	r := Status(ctx, c, runner)
	if _, err := exec.LookPath("docker"); err != nil {
		r.Docker = "missing"
	} else if _, err := runner.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		r.Docker = "not running"
	} else {
		r.Docker = "available"
	}
	if _, err := exec.LookPath("git"); err != nil {
		r.Git = "missing"
	} else {
		r.Git = "available"
	}
	for name, path := range r.Paths {
		if err := writable(path); err != nil {
			r.Paths[name] = path + " (not writable: " + err.Error() + ")"
		}
	}
	return r
}

func endpointHealth(port int, path string) string {
	client := http.Client{Timeout: 750 * time.Millisecond}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return "unreachable"
	}
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 500 {
		return "healthy"
	}
	return fmt.Sprintf("unhealthy (%s)", response.Status)
}

func writable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	probe, err := os.CreateTemp(path, ".swe-cache-doctor-")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

type Stats struct {
	APTBytes, OCIBytes, GitBytes, NPMBytes, GoBytes int64
	GitMirrors                                      int
}

func CollectStats(c config.Config) (Stats, error) {
	apt, err := directoryBytes(c.APTDir())
	if err != nil {
		return Stats{}, err
	}
	oci, err := directoryBytes(c.OCIDir())
	if err != nil {
		return Stats{}, err
	}
	gitBytes, err := directoryBytes(c.GitDir())
	if err != nil {
		return Stats{}, err
	}
	npm, err := directoryBytes(c.NPMDir())
	if err != nil {
		return Stats{}, err
	}
	goModules, err := directoryBytes(c.GoDir())
	if err != nil {
		return Stats{}, err
	}
	mirrors := 0
	err = filepath.WalkDir(c.GitDir(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".git") {
			mirrors++
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return Stats{}, err
	}
	return Stats{APTBytes: apt, OCIBytes: oci, GitBytes: gitBytes, NPMBytes: npm, GoBytes: goModules, GitMirrors: mirrors}, nil
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func PortAvailable(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	return listener.Close()
}
