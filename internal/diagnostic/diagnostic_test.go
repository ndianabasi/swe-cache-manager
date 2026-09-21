package diagnostic

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
)

func TestCollectStats(t *testing.T) {
	c := config.Defaults()
	c.Root = t.TempDir()
	if err := c.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.APTDir(), "package.deb"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.NPMDir(), "package.tgz"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.GoDir(), "module.zip"), []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.GitDir(), "github.com", "org", "repo.git"), 0750); err != nil {
		t.Fatal(err)
	}
	stats, err := CollectStats(c)
	if err != nil {
		t.Fatal(err)
	}
	if stats.APTBytes != 4 || stats.NPMBytes != 5 || stats.GoBytes != 6 || stats.GitMirrors != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}
