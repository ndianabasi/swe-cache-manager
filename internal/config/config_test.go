package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	c := Defaults()
	c.Root = t.TempDir()
	c.APT.Port = 43142
	c.OCI.Upstream = "https://registry.example.test"
	c.OCI.TLSVerify = false
	c.OCI.ManifestCheckInterval = "30m"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != c.Root || got.APT.Port != 43142 || got.OCI.Upstream != c.OCI.Upstream || got.OCI.TLSVerify != c.OCI.TLSVerify || got.OCI.ManifestCheckInterval != c.OCI.ManifestCheckInterval {
		t.Fatalf("loaded %#v, want %#v", got, c)
	}
	for _, name := range []string{"apt", "zot", "git", "npm", "go", "config", "logs"} {
		if _, err := os.Stat(filepath.Join(c.Root, name)); err != nil {
			t.Errorf("%s was not created: %v", name, err)
		}
	}
}

func TestRejectsRelativeRoot(t *testing.T) {
	c := Defaults()
	c.Root = "cache"
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRejectsInvalidManifestCheckInterval(t *testing.T) {
	c := Defaults()
	c.OCI.ManifestCheckInterval = "eventually"
	if err := c.Validate(); err == nil {
		t.Fatal("expected manifest check interval validation error")
	}
}

func TestDefaultsUseAnAbsoluteRoot(t *testing.T) {
	if !filepath.IsAbs(Defaults().Root) {
		t.Fatalf("default root is not absolute: %q", Defaults().Root)
	}
}

func TestDefaultPorts(t *testing.T) {
	c := Defaults()
	if c.APT.Port != DefaultAPTPort || c.OCI.Port != DefaultOCIPort || c.NPM.Port != DefaultNPMPort || c.Go.Port != DefaultGoPort {
		t.Fatalf("unexpected default ports: %#v", c)
	}
}

func TestRejectsDuplicateEnabledServicePorts(t *testing.T) {
	c := Defaults()
	c.NPM.Port = c.Go.Port
	if err := c.Validate(); err == nil {
		t.Fatal("expected duplicate port validation error")
	}
}
