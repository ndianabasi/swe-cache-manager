// Package config owns the small, dependency-free configuration surface exposed
// by swe-cache. Service-specific files are generated from this configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultRoot  = "/var/lib/swe-cache"
	DefaultImage = "ghcr.io/ndianabasi/swe-cache-services:0.1.0"
)

// Config is deliberately a small, stable top-level configuration. The cache
// owns generated apt-cacher-ng, zot, and supervisord files below Root/config.
type Config struct {
	Root        string
	Image       string
	APT         APTConfig
	OCI         OCIConfig
	Git         GitConfig
	Maintenance MaintenanceConfig
}

type APTConfig struct {
	Enabled bool
	Port    int
}
type OCIConfig struct {
	Enabled bool
	Port    int
}
type GitConfig struct{ Enabled bool }
type MaintenanceConfig struct{ Enabled bool }

func Defaults() Config {
	root := os.Getenv("SWE_CACHE_ROOT")
	if root == "" {
		root = DefaultRoot
	}
	image := os.Getenv("SWE_CACHE_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	return Config{Root: root, Image: image, APT: APTConfig{Enabled: true, Port: 3142}, OCI: OCIConfig{Enabled: true, Port: 5000}, Git: GitConfig{Enabled: true}}
}

func (c Config) ConfigDir() string { return filepath.Join(c.Root, "config") }
func (c Config) APTDir() string    { return filepath.Join(c.Root, "apt") }
func (c Config) OCIDir() string    { return filepath.Join(c.Root, "zot") }
func (c Config) GitDir() string    { return filepath.Join(c.Root, "git") }
func (c Config) LogDir() string    { return filepath.Join(c.Root, "logs") }
func (c Config) Path() string      { return filepath.Join(c.ConfigDir(), "swe-cache.toml") }

func (c Config) Validate() error {
	if c.Root == "" || !filepath.IsAbs(c.Root) {
		return errors.New("cache root must be an absolute path")
	}
	if c.Image == "" {
		return errors.New("service image must not be empty")
	}
	for name, port := range map[string]int{"apt": c.APT.Port, "oci": c.OCI.Port} {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s port must be between 1 and 65535", name)
		}
	}
	if c.APT.Enabled && c.OCI.Enabled && c.APT.Port == c.OCI.Port {
		return errors.New("apt and oci ports must differ")
	}
	return nil
}

// EnsureLayout creates only persistent host paths. No cache data is kept in
// the service container, so removing Docker state cannot remove these paths.
func (c Config) EnsureLayout() error {
	if err := c.Validate(); err != nil {
		return err
	}
	for _, dir := range []string{c.Root, c.APTDir(), c.OCIDir(), c.GitDir(), c.ConfigDir(), c.LogDir()} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	c := Defaults()
	if path == "" {
		path = c.Path()
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	section := ""
	for lineNo, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[] ")
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return Config{}, fmt.Errorf("config line %d: expected key = value", lineNo+1)
		}
		key, value := strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), "\"")
		if err := set(&c, section, key, value); err != nil {
			return Config{}, fmt.Errorf("config line %d: %w", lineNo+1, err)
		}
	}
	return c, c.Validate()
}

func set(c *Config, section, key, value string) error {
	boolValue := func(dst *bool) error {
		v, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		*dst = v
		return nil
	}
	portValue := func(dst *int) error {
		v, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		*dst = v
		return nil
	}
	switch section + "." + key {
	case ".cache_root":
		c.Root = value
	case ".service_image":
		c.Image = value
	case "apt.enabled":
		return boolValue(&c.APT.Enabled)
	case "apt.port":
		return portValue(&c.APT.Port)
	case "oci.enabled":
		return boolValue(&c.OCI.Enabled)
	case "oci.port":
		return portValue(&c.OCI.Port)
	case "git.enabled":
		return boolValue(&c.Git.Enabled)
	case "maintenance.enabled":
		return boolValue(&c.Maintenance.Enabled)
	default:
		return fmt.Errorf("unknown setting %q in section %q", key, section)
	}
	return nil
}

func (c Config) Save() error {
	if err := c.EnsureLayout(); err != nil {
		return err
	}
	data := fmt.Sprintf("# Managed by swe-cache. Edit this top-level file; service files are regenerated.\ncache_root = %q\nservice_image = %q\n\n[apt]\nenabled = %t\nport = %d\n\n[oci]\nenabled = %t\nport = %d\n\n[git]\nenabled = %t\n\n[maintenance]\nenabled = %t\n", c.Root, c.Image, c.APT.Enabled, c.APT.Port, c.OCI.Enabled, c.OCI.Port, c.Git.Enabled, c.Maintenance.Enabled)
	return os.WriteFile(c.Path(), []byte(data), 0640)
}
