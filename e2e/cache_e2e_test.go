//go:build integration

// Package e2e_test exercises real cache daemons against local upstreams. It
// never contacts public package, image, or Git hosting services.
package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
	"github.com/ndianabasi/swe-cache-manager/internal/gitcache"
	"github.com/ndianabasi/swe-cache-manager/internal/service"
)

const e2eImageEnv = "SWE_CACHE_E2E_IMAGE"

var nextE2EPort atomic.Uint32

func TestAPTProxyServesCachedPackageAfterUpstreamIsOffline(t *testing.T) {
	upstream, online := newHostServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pool/main/s/swe-cache-test/swe-cache-test_1_all.deb" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
		_, _ = w.Write([]byte("not-a-real-deb-but-a-cacheable-package-payload"))
	})
	cache := startCache(t, upstream, true, nil)
	proxy, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cache.Config.APT.Port))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 10 * time.Second}
	packageURL := upstream + "/pool/main/s/swe-cache-test/swe-cache-test_1_all.deb"
	first := getBody(t, client, packageURL)
	online.Store(false)
	second := getBody(t, client, packageURL)
	if !bytes.Equal(first, second) {
		t.Fatalf("cached APT payload differed: %q != %q", first, second)
	}
}

func TestOCIPullThroughServesManifestAndBlobAfterUpstreamIsOffline(t *testing.T) {
	configBlob := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := digest(configBlob)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[]}`, configDigest, len(configBlob)))
	manifestDigest := digest(manifest)
	upstream, online, certificates := newHostTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/example/manifests/latest", "/v2/example/manifests/" + manifestDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(manifest)
			}
		case "/v2/example/blobs/" + configDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.config.v1+json")
			w.Header().Set("Docker-Content-Digest", configDigest)
			w.Header().Set("Content-Length", strconv.Itoa(len(configBlob)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(configBlob)
			}
		default:
			http.NotFound(w, r)
		}
	})
	cache := startCache(t, upstream, true, certificates)
	client := &http.Client{Timeout: 15 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d/v2/example", cache.Config.OCI.Port)
	if got := getBody(t, client, base+"/manifests/latest"); !bytes.Equal(got, manifest) {
		t.Fatalf("unexpected first manifest: %s", got)
	}
	if got := getBody(t, client, base+"/blobs/"+configDigest); !bytes.Equal(got, configBlob) {
		t.Fatalf("unexpected first blob: %s", got)
	}
	online.Store(false)
	if got := getBody(t, client, base+"/manifests/"+manifestDigest); !bytes.Equal(got, manifest) {
		t.Fatalf("cached manifest was unavailable after upstream shutdown: %s", got)
	}
	if got := getBody(t, client, base+"/blobs/"+configDigest); !bytes.Equal(got, configBlob) {
		t.Fatalf("cached blob was unavailable after upstream shutdown: %s", got)
	}
}

func TestGitCloneUsesBareMirrorAfterRemoteIsOffline(t *testing.T) {
	requireE2E(t)
	repositoryRoot := t.TempDir()
	worktree := filepath.Join(repositoryRoot, "worktree")
	runGit(t, repositoryRoot, "init", worktree)
	runGit(t, worktree, "config", "user.email", "e2e@example.invalid")
	runGit(t, worktree, "config", "user.name", "swe-cache e2e")
	if err := os.WriteFile(filepath.Join(worktree, "README"), []byte("cached commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "README")
	runGit(t, worktree, "commit", "-m", "fixture")
	if err := os.Mkdir(filepath.Join(repositoryRoot, "fixtures"), 0750); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "clone", "--bare", worktree, "fixtures/fixture.git")

	remote, stopDaemon := startGitDaemon(t, repositoryRoot)
	defer stopDaemon()
	manager := gitcache.Manager{Root: filepath.Join(t.TempDir(), "mirrors"), Runner: service.CommandRunner{}}
	first := filepath.Join(t.TempDir(), "first")
	if err := manager.Clone(context.Background(), remote, first, ""); err != nil {
		t.Fatalf("first clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first, "README")); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	second := filepath.Join(t.TempDir(), "second")
	if err := manager.Clone(context.Background(), remote, second, ""); err != nil {
		t.Fatalf("cached clone after remote shutdown: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(second, "README")); err != nil || string(body) != "cached commit\n" {
		t.Fatalf("cached checkout = %q, %v", body, err)
	}
}

type tlsMaterial struct {
	certificate []byte
	key         []byte
}

func startCache(t *testing.T, upstream string, tlsVerify bool, certificates *tlsMaterial) service.Manager {
	t.Helper()
	requireE2E(t)
	image := os.Getenv(e2eImageEnv)
	if image == "" {
		image = config.DefaultImage
	}
	c := config.Defaults()
	c.Root = t.TempDir()
	c.Image = image
	c.APT.Port = freePort(t)
	c.OCI.Port = freePort(t)
	c.OCI.Upstream = upstream
	c.OCI.TLSVerify = tlsVerify
	if certificates != nil {
		c.OCI.TLSCertDir = "/etc/swe-cache/certs"
		if err := os.MkdirAll(filepath.Join(c.OCIConfigDir(), "certs"), 0750); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{"ca.crt": certificates.certificate, "client.cert": certificates.certificate, "client.key": certificates.key} {
			if err := os.WriteFile(filepath.Join(c.OCIConfigDir(), "certs", name), content, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	manager := service.Manager{Config: c, Name: fmt.Sprintf("swe-cache-e2e-%d", time.Now().UnixNano())}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Remove(context.Background()) })
	t.Cleanup(func() {
		if t.Failed() {
			output, _ := exec.Command("docker", "logs", manager.Name).CombinedOutput()
			t.Logf("%s logs:\n%s", manager.Name, output)
		}
	})
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", c.APT.Port))
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/v2/", c.OCI.Port))
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/-/ping", c.NPM.Port))
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", c.Go.Port))
	return manager
}

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("SWE_CACHE_E2E") != "1" {
		t.Skip("set SWE_CACHE_E2E=1 to run Docker-backed cache tests")
	}
}

func newHostServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (string, *atomic.Bool) {
	t.Helper()
	online := &atomic.Bool{}
	online.Store(true)
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			http.Error(w, "upstream intentionally offline", http.StatusServiceUnavailable)
			return
		}
		handler(w, r)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "http://host.docker.internal:" + port, online
}

func newHostTLSServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (string, *atomic.Bool, *tlsMaterial) {
	t.Helper()
	online := &atomic.Bool{}
	online.Store(true)
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			http.Error(w, "upstream intentionally offline", http.StatusServiceUnavailable)
			return
		}
		handler(w, r)
	}))
	server.Listener = listener
	certificate, certificates := hostCertificate(t)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "https://host.docker.internal:" + port, online, certificates
}

func hostCertificate(t *testing.T) (tls.Certificate, *tlsMaterial) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "host.docker.internal"}, DNSNames: []string{"host.docker.internal"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, &tlsMaterial{certificate: certificatePEM, key: privateKeyPEM}
}

func getBody(t *testing.T, client *http.Client, rawURL string) []byte {
	t.Helper()
	response, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %s: %s", rawURL, response.Status, body)
	}
	return body
}

func waitForHTTP(t *testing.T, rawURL string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, err := (&http.Client{Timeout: time.Second}).Get(rawURL)
		if err == nil {
			response.Body.Close()
			if response.StatusCode < 500 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("service never became reachable at %s", rawURL)
}

func freePort(t *testing.T) int {
	t.Helper()
	for candidate := int(nextE2EPort.Add(1)) + 18000; candidate < 24000; candidate++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate)))
		if err == nil {
			_ = listener.Close()
			// A fallback scan may skip occupied ports. Advance the shared cursor
			// beyond the chosen port so the next allocation cannot reuse it.
			nextE2EPort.Store(uint32(candidate - 18000))
			return candidate
		}
	}
	t.Fatal("no available port in E2E range 18001-23999")
	return 0
}

func startGitDaemon(t *testing.T, repositoryRoot string) (string, func()) {
	t.Helper()
	var lastError error
	for attempt := 0; attempt < 10; attempt++ {
		port := freePort(t)
		remote := fmt.Sprintf("git://127.0.0.1:%d/fixtures/fixture.git", port)
		daemon := exec.Command("git", "daemon", "--reuseaddr", "--listen=127.0.0.1", "--export-all", "--base-path="+repositoryRoot, "--port="+strconv.Itoa(port), repositoryRoot)
		if err := daemon.Start(); err != nil {
			lastError = err
			continue
		}
		if err := waitForGitRemote(remote); err == nil {
			var once sync.Once
			return remote, func() {
				once.Do(func() {
					if daemon.ProcessState == nil {
						_ = daemon.Process.Kill()
					}
					_ = daemon.Wait()
				})
			}
		} else {
			lastError = err
		}
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	}
	t.Fatalf("git daemon never served fixture: %v", lastError)
	return "", func() {}
}

func waitForGitRemote(remote string) error {
	deadline := time.Now().Add(time.Second)
	var output []byte
	for time.Now().Before(deadline) {
		var err error
		output, err = exec.Command("git", "ls-remote", remote).CombinedOutput()
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("git daemon did not serve %s: %s", remote, output)
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
