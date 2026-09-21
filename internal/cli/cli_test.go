package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d: %s", code, errOut.String())
	}
	if out.String() != "swe-cache "+Version+"\n" {
		t.Fatalf("unexpected version %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"wat"}, &out, &errOut); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestLifecycleOptionsAcceptPorts(t *testing.T) {
	var errOut bytes.Buffer
	got, ok := lifecycleOptions([]string{"--oci-port", "5510", "--apt-port", "3142"}, &errOut)
	if !ok || got.ociPort != 5510 || got.aptPort != 3142 || !got.ociPortSet || !got.aptPortSet {
		t.Fatalf("parsed %#v, stderr %s", got, errOut.String())
	}
}

func TestGitCloneHelpIncludesCommitExample(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"git", "clone", "--help"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d: %s", code, errOut.String())
	}
	for _, want := range []string{"--commit SHA", "host-side bare mirror", "https://github.com/acme/project.git"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Git help does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestGitCloneForceIsAValuelessOption(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"git", "clone", "--force"}, &out, &errOut); code != 2 {
		t.Fatalf("code = %d, stderr %s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "requires a value") || !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("unexpected force parsing error: %s", errOut.String())
	}
}

func TestReadmeFlagPrintsEmbeddedDocumentation(t *testing.T) {
	var out, errOut bytes.Buffer
	const readme = "# swe-cache\n\nEmbedded documentation.\n"
	if code := RunWithReadme([]string{"--readme"}, &out, &errOut, readme); code != 0 {
		t.Fatalf("code = %d: %s", code, errOut.String())
	}
	if out.String() != readme {
		t.Fatalf("readme = %q, want %q", out.String(), readme)
	}
}
