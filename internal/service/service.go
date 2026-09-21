// Package service manages the disposable Docker container which hosts the APT
// and OCI protocol-aware caches. Persistent data always belongs to config.Config.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
)

const ContainerName = "swe-cache-services"

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

type CommandRunner struct{}

func (CommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err != nil {
		return output.String(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output.String(), nil
}

// RunStream connects a command directly to the supplied writers. It is useful
// for long-running, user-initiated commands whose progress should be visible
// while the command is still running.
func (CommandRunner) RunStream(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

type Manager struct {
	Config config.Config
	Runner Runner
	// Name is only intended for isolated integration tests. Production callers
	// use the stable default so normal lifecycle commands remain predictable.
	Name string
}

func (m Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return CommandRunner{}
}

func (m Manager) containerName() string {
	if m.Name != "" {
		return m.Name
	}
	return ContainerName
}

func (m Manager) GenerateRuntimeConfig() error {
	if err := m.Config.EnsureLayout(); err != nil {
		return err
	}
	apt := fmt.Sprintf("CacheDir: /var/cache/apt-cacher-ng\nLogDir: /var/log/swe-cache\nPort: %d\nAllowUserPorts: 0\nForeGround: 1\n", m.Config.APT.Port)
	if err := os.WriteFile(filepath.Join(m.Config.APTConfigDir(), "acng.conf"), []byte(apt), 0640); err != nil {
		return err
	}
	registry := map[string]any{
		"urls":                  []string{m.Config.OCI.Upstream},
		"onDemand":              true,
		"tlsVerify":             m.Config.OCI.TLSVerify,
		"manifestCheckInterval": m.Config.OCI.ManifestCheckInterval,
		"preserveDigest":        true,
	}
	if m.Config.OCI.TLSCertDir != "" {
		registry["certDir"] = m.Config.OCI.TLSCertDir
	}
	zot, err := json.MarshalIndent(map[string]any{
		"distSpecVersion": "1.1.0",
		"storage":         map[string]any{"rootDirectory": "/var/lib/zot", "gc": true, "dedupe": true},
		"http":            map[string]any{"address": "0.0.0.0", "port": fmt.Sprint(m.Config.OCI.Port), "compat": []string{"docker2s2"}},
		"log":             map[string]any{"level": "info"},
		// Docker Hub is the one registry Docker can transparently use through
		// its registry-mirrors setting. Other upstreams need explicit
		// registry-host mapping, so they are intentionally not guessed here.
		"extensions": map[string]any{"sync": map[string]any{
			"enable":     true,
			"registries": []map[string]any{registry},
		}},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.Config.OCIConfigDir(), "zot.json"), append(zot, '\n'), 0640); err != nil {
		return err
	}
	supervisor := `[supervisord]
nodaemon=true
logfile=/var/log/supervisor/supervisord.log
pidfile=/tmp/supervisord.pid

[program:apt-cacher-ng]
command=/usr/sbin/apt-cacher-ng -c /etc/apt-cacher-ng ForeGround=1
autorestart=true
startretries=3
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
redirect_stderr=true

[program:zot]
command=/usr/local/bin/zot serve /etc/swe-cache/zot.json
autorestart=true
startretries=3
stdout_logfile=/dev/fd/1
stdout_logfile_maxbytes=0
redirect_stderr=true
`
	return os.WriteFile(filepath.Join(m.Config.SupervisorConfigDir(), "swe-cache.conf"), []byte(supervisor), 0640)
}

func (m Manager) Start(ctx context.Context) error {
	if err := m.GenerateRuntimeConfig(); err != nil {
		return fmt.Errorf("generate runtime configuration: %w", err)
	}
	r := m.runner()
	if _, err := r.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return errors.New("Docker is unavailable; start Docker and retry")
	}
	state, err := m.containerState(ctx)
	if err != nil {
		return err
	}
	if state == "running" {
		return nil
	}
	if state == "exited" || state == "created" {
		_, err = r.Run(ctx, "docker", "start", m.containerName())
		return err
	}
	if err := m.EnsureImage(ctx); err != nil {
		return err
	}
	args := []string{"run", "--detach", "--name", m.containerName(), "--restart", "unless-stopped", "--label", "io.swe-cache.managed=true"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	if m.Config.APT.Enabled {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", m.Config.APT.Port, m.Config.APT.Port))
	}
	if m.Config.OCI.Enabled {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", m.Config.OCI.Port, m.Config.OCI.Port))
	}
	args = append(args,
		"--mount", "type=bind,src="+m.Config.APTDir()+",dst=/var/cache/apt-cacher-ng",
		"--mount", "type=bind,src="+m.Config.OCIDir()+",dst=/var/lib/zot",
		"--mount", "type=bind,src="+m.Config.APTConfigDir()+",dst=/etc/apt-cacher-ng,readonly",
		"--mount", "type=bind,src="+m.Config.OCIConfigDir()+",dst=/etc/swe-cache,readonly",
		"--mount", "type=bind,src="+m.Config.SupervisorConfigDir()+",dst=/etc/supervisor/conf.d,readonly",
		"--mount", "type=bind,src="+m.Config.LogDir()+",dst=/var/log/swe-cache",
		m.Config.Image,
	)
	_, err = r.Run(ctx, "docker", args...)
	return err
}

func (m Manager) Stop(ctx context.Context) error {
	state, err := m.containerState(ctx)
	if err != nil || state == "" || state == "exited" {
		return err
	}
	_, err = m.runner().Run(ctx, "docker", "stop", m.containerName())
	return err
}

func (m Manager) Remove(ctx context.Context) error {
	state, err := m.containerState(ctx)
	if err != nil || state == "" {
		return err
	}
	if state == "running" {
		if err := m.Stop(ctx); err != nil {
			return err
		}
	}
	_, err = m.runner().Run(ctx, "docker", "rm", m.containerName())
	return err
}

func (m Manager) Restart(ctx context.Context) error {
	if err := m.Remove(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

// Warm asks Docker to pull image references after ensuring the local cache
// service is available. With Docker Hub configured to use Zot as a registry
// mirror, these pulls populate Zot outside a time-sensitive evaluator build.
// Docker is deliberately used here rather than speaking Zot's private sync API:
// it exercises the exact request path used by BuildKit on the host.
func (m Manager) Warm(ctx context.Context, references []string) ([]string, error) {
	if !m.Config.OCI.Enabled {
		return nil, errors.New("OCI caching is disabled in configuration")
	}
	if len(references) == 0 {
		return nil, errors.New("at least one image reference is required")
	}
	if err := m.Start(ctx); err != nil {
		return nil, err
	}
	outputs := make([]string, 0, len(references))
	for _, reference := range references {
		output, err := m.runner().Run(ctx, "docker", "pull", reference)
		if err != nil {
			return outputs, fmt.Errorf("pull %s: %w", reference, err)
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

func (m Manager) containerState(ctx context.Context) (string, error) {
	out, err := m.runner().Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", m.containerName())
	if err != nil {
		// Docker returns an error for a missing container; other inspection
		// failures will be caught by the Docker availability check in Start.
		if strings.Contains(out, "No such") || strings.Contains(out, "No such object") {
			return "", nil
		}
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

func (m Manager) State(ctx context.Context) (string, error) { return m.containerState(ctx) }
