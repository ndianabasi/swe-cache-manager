package cli

import (
	"bytes"
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
