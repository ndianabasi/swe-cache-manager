// Package config owns the small, dependency-free configuration surface exposed
// by swe-cache. Service-specific files are generated from this configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultImage   = "ghcr.io/ndianabasi/swe-cache-services:0.3.0"
	DefaultAPTPort = 3142 // apt-cacher-ng's established default
	DefaultOCIPort = 5500 // intentionally avoids the commonly occupied 5000
	DefaultNPMPort = 4873 // Verdaccio's established default
	DefaultGoPort  = 3000 // Athens' established default
)

// Config is deliberately a small, stable top-level configuration. The cache
// owns generated apt-cacher-ng, zot, and supervisord files below Root/config.
type Config struct {
	Root        string
	Image       string
	APT         APTConfig
	OCI         OCIConfig
	Git         GitConfig
	NPM         NPMConfig
	Go          GoConfig
	Maintenance MaintenanceConfig
}

type APTConfig struct {
	Enabled bool
	Port    int
}
type OCIConfig struct {
	Enabled               bool
	Port                  int
	Upstream              string
	TLSVerify             bool
	ManifestCheckInterval string
	// TLSCertDir is an in-container directory containing registry CA material.
	// Normal public registries leave this empty; integration tests and private
	// registries can mount certificates beneath the generated Zot config.
	TLSCertDir string
}
type GitConfig struct{ Enabled bool }
type NPMConfig struct {
	Enabled bool
	Port    int
}
type GoConfig struct {
	Enabled bool
	Port    int
}
type MaintenanceConfig struct{ Enabled bool }

func Defaults() Config {
	root := os.Getenv("SWE_CACHE_ROOT")
	if root == "" {
		root = defaultRoot()
	}
	image := os.Getenv("SWE_CACHE_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	return Config{Root: root, Image: image, APT: APTConfig{Enabled: true, Port: DefaultAPTPort}, OCI: OCIConfig{Enabled: true, Port: DefaultOCIPort, Upstream: "https://registry-1.docker.io", TLSVerify: true, ManifestCheckInterval: "1h"}, Git: GitConfig{Enabled: true}, NPM: NPMConfig{Enabled: true, Port: DefaultNPMPort}, Go: GoConfig{Enabled: true, Port: DefaultGoPort}}
}

// defaultRoot selects a writable, OS-native location for user installs while
// retaining conventional system-wide locations for root-managed deployments.
func defaultRoot() string {
	switch runtime.GOOS {
	case "linux":
		if os.Geteuid() == 0 {
			return "/var/lib/swe-cache"
		}
	case "darwin":
		if os.Geteuid() == 0 {
			return "/Library/Caches/swe-cache"
		}
	}
	if root, err := os.UserCacheDir(); err == nil && root != "" {
		return filepath.Join(root, "swe-cache")
	}
	if runtime.GOOS == "linux" {
		return "/var/lib/swe-cache"
	}
	// This only applies to an unusually restricted user environment where the
	// OS did not expose a cache directory. Keep the fallback absolute.
	if root, err := filepath.Abs("swe-cache"); err == nil {
		return root
	}
	return filepath.Join(string(os.PathSeparator), "swe-cache")
}

func (c Config) ConfigDir() string { return filepath.Join(c.Root, "config") }
func (c Config) APTConfigDir() string {
	return filepath.Join(c.ConfigDir(), "apt-cacher-ng")
}
func (c Config) OCIConfigDir() string { return filepath.Join(c.ConfigDir(), "zot") }
func (c Config) NPMConfigDir() string { return filepath.Join(c.ConfigDir(), "npm") }
func (c Config) SupervisorConfigDir() string {
	return filepath.Join(c.ConfigDir(), "supervisor")
}
func (c Config) APTDir() string { return filepath.Join(c.Root, "apt") }
func (c Config) OCIDir() string { return filepath.Join(c.Root, "zot") }
func (c Config) GitDir() string { return filepath.Join(c.Root, "git") }
func (c Config) NPMDir() string { return filepath.Join(c.Root, "npm") }
func (c Config) GoDir() string  { return filepath.Join(c.Root, "go") }
func (c Config) LogDir() string { return filepath.Join(c.Root, "logs") }
func (c Config) Path() string   { return filepath.Join(c.ConfigDir(), "swe-cache.toml") }

func (c Config) Validate() error {
	if c.Root == "" || !filepath.IsAbs(c.Root) {
		return errors.New("cache root must be an absolute path")
	}
	if c.Image == "" {
		return errors.New("service image must not be empty")
	}
	if c.OCI.Enabled && c.OCI.Upstream == "" {
		return errors.New("OCI upstream must not be empty when OCI caching is enabled")
	}
	if c.OCI.Enabled && c.OCI.ManifestCheckInterval == "" {
		return errors.New("OCI manifest check interval must not be empty when OCI caching is enabled")
	}
	if c.OCI.Enabled {
		interval, err := time.ParseDuration(c.OCI.ManifestCheckInterval)
		if err != nil || interval <= 0 {
			return fmt.Errorf("OCI manifest check interval must be a positive duration: %q", c.OCI.ManifestCheckInterval)
		}
	}
	ports := map[string]struct {
		enabled bool
		port    int
	}{
		"apt": {c.APT.Enabled, c.APT.Port}, "oci": {c.OCI.Enabled, c.OCI.Port},
		"npm": {c.NPM.Enabled, c.NPM.Port}, "go": {c.Go.Enabled, c.Go.Port},
	}
	usedPorts := map[int]string{}
	for name, candidate := range ports {
		port := candidate.port
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s port must be between 1 and 65535", name)
		}
		if candidate.enabled {
			if previous, exists := usedPorts[port]; exists {
				return fmt.Errorf("%s and %s ports must differ", previous, name)
			}
			usedPorts[port] = name
		}
	}
	return nil
}

// EnsureLayout creates only persistent host paths. No cache data is kept in
// the service container, so removing Docker state cannot remove these paths.
func (c Config) EnsureLayout() error {
	if err := c.Validate(); err != nil {
		return err
	}
	for _, dir := range []string{c.Root, c.APTDir(), c.OCIDir(), c.GitDir(), c.NPMDir(), c.GoDir(), c.ConfigDir(), c.APTConfigDir(), c.OCIConfigDir(), c.NPMConfigDir(), c.SupervisorConfigDir(), c.LogDir()} {
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
	case "oci.upstream":
		c.OCI.Upstream = value
	case "oci.tls_verify":
		return boolValue(&c.OCI.TLSVerify)
	case "oci.manifest_check_interval":
		c.OCI.ManifestCheckInterval = value
	case "git.enabled":
		return boolValue(&c.Git.Enabled)
	case "npm.enabled":
		return boolValue(&c.NPM.Enabled)
	case "npm.port":
		return portValue(&c.NPM.Port)
	case "go.enabled":
		return boolValue(&c.Go.Enabled)
	case "go.port":
		return portValue(&c.Go.Port)
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
	data := fmt.Sprintf("# Managed by swe-cache. Edit this top-level file; service files are regenerated.\ncache_root = %q\nservice_image = %q\n\n[apt]\nenabled = %t\nport = %d\n\n[oci]\nenabled = %t\nport = %d\nupstream = %q\ntls_verify = %t\nmanifest_check_interval = %q\n\n[git]\nenabled = %t\n\n[npm]\nenabled = %t\nport = %d\n\n[go]\nenabled = %t\nport = %d\n\n[maintenance]\nenabled = %t\n", c.Root, c.Image, c.APT.Enabled, c.APT.Port, c.OCI.Enabled, c.OCI.Port, c.OCI.Upstream, c.OCI.TLSVerify, c.OCI.ManifestCheckInterval, c.Git.Enabled, c.NPM.Enabled, c.NPM.Port, c.Go.Enabled, c.Go.Port, c.Maintenance.Enabled)
	return os.WriteFile(c.Path(), []byte(data), 0640)
}
