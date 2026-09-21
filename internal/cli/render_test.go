package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
	"github.com/ndianabasi/swe-cache-manager/internal/diagnostic"
)

func TestRenderStatusGroupsOperationalDetails(t *testing.T) {
	c := config.Defaults()
	c.Root = "/cache"
	c.APT.Port = 3142
	c.NPM.Port = 4873
	c.Go.Port = 3000
	c.OCI.Port = 5500
	report := diagnostic.Report{
		Container: "running",
		APT:       "healthy",
		NPM:       "healthy",
		Go:        "healthy",
		OCI:       "healthy",
		Paths: map[string]string{
			"apt": "/cache/apt", "oci": "/cache/zot", "git": "/cache/git", "npm": "/cache/npm", "go": "/cache/go",
		},
	}
	var output bytes.Buffer
	renderStatus(&output, c, report)
	got := output.String()
	for _, want := range []string{
		"swe-cache status", "SERVICES", "ENDPOINTS", "PERSISTENT STORAGE", "RUNTIME",
		"Container        running", "APT proxy        http://127.0.0.1:3142", "OCI registry     http://127.0.0.1:5500", "npm registry     http://127.0.0.1:4873", "Go modules   /cache/go",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status output does not contain %q:\n%s", want, got)
		}
	}
}
