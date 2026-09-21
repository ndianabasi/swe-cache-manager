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
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != c.Root || got.APT.Port != 43142 || got.OCI.Upstream != c.OCI.Upstream {
		t.Fatalf("loaded %#v, want %#v", got, c)
	}
	for _, name := range []string{"apt", "registry", "git", "npm", "go", "config", "logs"} {
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

func TestLoadAcceptsRetiredZotSettings(t *testing.T) {
	c := Defaults()
	c.Root = t.TempDir()
	if err := c.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Path(), []byte("cache_root = \""+c.Root+"\"\nservice_image = \"image\"\n\n[oci]\ntls_verify = true\nmanifest_check_interval = \"1h\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(c.Path()); err != nil {
		t.Fatalf("load legacy Zot settings: %v", err)
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
