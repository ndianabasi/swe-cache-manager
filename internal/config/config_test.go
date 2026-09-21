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
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != c.Root || got.APT.Port != 43142 {
		t.Fatalf("loaded %#v, want %#v", got, c)
	}
	for _, name := range []string{"apt", "zot", "git", "config", "logs"} {
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

func TestDefaultsUseAnAbsoluteRoot(t *testing.T) {
	if !filepath.IsAbs(Defaults().Root) {
		t.Fatalf("default root is not absolute: %q", Defaults().Root)
	}
}
