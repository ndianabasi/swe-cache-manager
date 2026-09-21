// Package service manages the disposable Docker container which hosts the APT
// and OCI protocol-aware caches. Persistent data always belongs to config.Config.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type Manager struct {
	Config config.Config
	Runner Runner
}

func (m Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return CommandRunner{}
}

func (m Manager) GenerateRuntimeConfig() error {
	if err := m.Config.EnsureLayout(); err != nil {
		return err
	}
	apt := fmt.Sprintf("CacheDir: /var/cache/apt-cacher-ng\nLogDir: /var/log/swe-cache\nPort: %d\nForeGround: 1\n", m.Config.APT.Port)
	if err := os.WriteFile(filepath.Join(m.Config.APTConfigDir(), "acng.conf"), []byte(apt), 0640); err != nil {
		return err
	}
	zot, err := json.MarshalIndent(map[string]any{
		"distSpecVersion": "1.1.0",
		"storage":         map[string]any{"rootDirectory": "/var/lib/zot", "gc": true, "dedupe": true},
		"http":            map[string]any{"address": "0.0.0.0", "port": fmt.Sprint(m.Config.OCI.Port)},
		"log":             map[string]any{"level": "info"},
		// Docker Hub is the one registry Docker can transparently use through
		// its registry-mirrors setting. Other upstreams need explicit
		// registry-host mapping, so they are intentionally not guessed here.
		"extensions": map[string]any{"sync": map[string]any{
			"enable": true,
			"registries": []map[string]any{{
				"urls":     []string{"https://registry-1.docker.io"},
				"onDemand": true,
			}},
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
		_, err = r.Run(ctx, "docker", "start", ContainerName)
		return err
	}
	if _, err := r.Run(ctx, "docker", "image", "inspect", m.Config.Image); err != nil {
		if _, pullErr := r.Run(ctx, "docker", "pull", m.Config.Image); pullErr != nil {
			return fmt.Errorf("obtain service image %s: %w", m.Config.Image, pullErr)
		}
	}
	args := []string{"run", "--detach", "--name", ContainerName, "--restart", "unless-stopped", "--label", "io.swe-cache.managed=true"}
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
	_, err = m.runner().Run(ctx, "docker", "stop", ContainerName)
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
	_, err = m.runner().Run(ctx, "docker", "rm", ContainerName)
	return err
}

func (m Manager) Restart(ctx context.Context) error {
	if err := m.Remove(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

func (m Manager) containerState(ctx context.Context) (string, error) {
	out, err := m.runner().Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", ContainerName)
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
