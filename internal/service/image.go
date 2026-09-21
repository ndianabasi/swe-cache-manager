package service

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed assets/Dockerfile
var embeddedDockerfile []byte

//go:embed assets/supervisord.conf
var embeddedSupervisorConfig []byte

// EnsureImage keeps first-run setup self-contained. The compact, pinned build
// context is embedded in the binary so an installed executable need not be run
// from a source checkout. An existing matching tag is never rebuilt.
func (m Manager) EnsureImage(ctx context.Context) error {
	r := m.runner()
	if _, err := r.Run(ctx, "docker", "image", "inspect", m.Config.Image); err == nil {
		return nil
	}
	if _, err := r.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("Docker is unavailable; start Docker and retry")
	}
	contextDir, err := os.MkdirTemp("", "swe-cache-image-")
	if err != nil {
		return fmt.Errorf("create temporary image context: %w", err)
	}
	defer os.RemoveAll(contextDir)
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), embeddedDockerfile, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextDir, "supervisord.conf"), embeddedSupervisorConfig, 0600); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "docker", "build", "--build-arg", "TARGETARCH="+runtime.GOARCH, "--tag", m.Config.Image, "--file", filepath.Join(contextDir, "Dockerfile"), contextDir); err != nil {
		return fmt.Errorf("build service image %s: %w", m.Config.Image, err)
	}
	return nil
}
