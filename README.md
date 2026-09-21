# swe-cache

`swe-cache` is a portable Go CLI that keeps APT packages, OCI registry content,
and Git mirrors outside Docker. Its Docker service container is disposable; the
cache root is the durable state.

It supports Linux, macOS, and Windows hosts. The service image is Linux, so
Docker Desktop must use Linux containers on macOS and Windows. Every platform
needs Go 1.25+ for development, Git, and a running Docker engine.

## Defaults and configuration

`swe-cache` chooses a writable, OS-native root. Set `SWE_CACHE_ROOT` or use
`--root` to select a different absolute directory.

| Host | User install default | Root/admin install default |
| --- | --- | --- |
| Linux | `$XDG_CACHE_HOME/swe-cache`, normally `~/.cache/swe-cache` | `/var/lib/swe-cache` |
| macOS | `~/Library/Caches/swe-cache` | `/Library/Caches/swe-cache` |
| Windows | `%LocalAppData%\swe-cache` | `%LocalAppData%\swe-cache` |

Each root contains `apt/`, `zot/`, `git/`, `config/`, and `logs/`. Removing
Docker images, build cache, or the service container does not remove this data.

The APT proxy defaults to apt-cacher-ng's established port `3142`. The OCI
registry defaults to `5500`, avoiding common development ports. Ports configure
both the listener inside the service container and Docker's localhost mapping.

```text
swe-cache init --apt-port 3142 --oci-port 5500
swe-cache restart --oci-port 5510
```

`start` and `restart` persist supplied port flags in `config/swe-cache.toml`.
Use `swe-cache status` to display the active endpoints.

## Development: Linux

Install Go, Git, and Docker Engine. Your user must have permission to use
Docker (for example, through Docker's `docker` group).

```sh
go test ./...
mkdir -p bin
go build -o ./bin/swe-cache ./cmd/swe-cache
docker build --build-arg TARGETARCH="$(go env GOARCH)" \
  -t ghcr.io/ndianabasi/swe-cache-services:0.1.0 \
  -f services/Dockerfile services

./bin/swe-cache init
./bin/swe-cache start
./bin/swe-cache status
```

For a system-wide Linux cache, initialize with an account permitted to write
the root:

```sh
sudo ./bin/swe-cache init --root /var/lib/swe-cache
sudo ./bin/swe-cache start --root /var/lib/swe-cache
```

## Development: macOS

Install Go, Git, and Docker Desktop. Start Docker Desktop and select Linux
containers. The build works on both Apple Silicon (`arm64`) and Intel (`amd64`)
Macs.

```sh
go test ./...
mkdir -p bin
go build -o ./bin/swe-cache ./cmd/swe-cache
docker build --build-arg TARGETARCH="$(go env GOARCH)" \
  -t ghcr.io/ndianabasi/swe-cache-services:0.1.0 \
  -f services/Dockerfile services

./bin/swe-cache init
./bin/swe-cache start
./bin/swe-cache status
```

The default durable root is `~/Library/Caches/swe-cache`. For a larger external
volume, use an absolute path such as:

```sh
./bin/swe-cache init --root /Volumes/cache-disk/swe-cache
```

## Development: Windows

Install Go, Git for Windows, and Docker Desktop with Linux containers running
(the WSL 2 backend is supported). Run the following from PowerShell in the
repository root:

```powershell
go test ./...
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\swe-cache.exe .\cmd\swe-cache
docker build --build-arg TARGETARCH="$(go env GOARCH)" `
  -t ghcr.io/ndianabasi/swe-cache-services:0.1.0 `
  -f .\services\Dockerfile .\services

.\bin\swe-cache.exe init
.\bin\swe-cache.exe start
.\bin\swe-cache.exe status
```

The default root is `%LocalAppData%\swe-cache`. To use another drive:

```powershell
.\bin\swe-cache.exe init --root D:\swe-cache
```

## Install a built binary

Building the CLI and building the service image are separate first-run steps.
Build the matching `ghcr.io/ndianabasi/swe-cache-services:0.1.0` image using
the development command above, or initialize with a published image via
`swe-cache init --image registry.example/swe-cache-services:0.1.0`.

### Linux

```sh
install -d "$HOME/.local/bin"
install -m 0755 ./bin/swe-cache "$HOME/.local/bin/swe-cache"
export PATH="$HOME/.local/bin:$PATH" # add to your shell profile

swe-cache init
swe-cache start
swe-cache doctor
```

For a system-wide command, install it to `/usr/local/bin/swe-cache` with the
appropriate administrator privileges, then use `--root /var/lib/swe-cache`.

### macOS

```sh
install -d "$HOME/.local/bin"
install -m 0755 ./bin/swe-cache "$HOME/.local/bin/swe-cache"
export PATH="$HOME/.local/bin:$PATH" # add to ~/.zprofile or ~/.zshrc

swe-cache init
swe-cache start
swe-cache doctor
```

### Windows

In PowerShell, after building `swe-cache.exe`:

```powershell
$installDir = Join-Path $env:LOCALAPPDATA "Programs\swe-cache"
New-Item -ItemType Directory -Force $installDir | Out-Null
Copy-Item .\bin\swe-cache.exe (Join-Path $installDir "swe-cache.exe")

# Enables it in this PowerShell session. Add $installDir to the User Path in
# Windows Settings to persist this for future shells.
$env:Path += ";$installDir"

swe-cache.exe init
swe-cache.exe start
swe-cache.exe doctor
```

## Configure Docker Hub pulls through Zot

Zot is an on-demand pull-through cache for Docker Hub. Docker's
`registry-mirrors` setting is global to the Docker daemon and applies to Docker
Hub only; it does not redirect GHCR, Quay, or other registries. Restart the
Docker engine after changing its settings, then run `swe-cache start` again if
Docker did not automatically restore the service container.

### Linux Docker Engine

With the default OCI port, create or merge this into `/etc/docker/daemon.json`:

```json
{
  "registry-mirrors": ["http://127.0.0.1:5500"],
  "insecure-registries": ["127.0.0.1:5500"]
}
```

Then reload the daemon:

```sh
sudo systemctl restart docker
swe-cache start
docker info | grep -A3 'Registry Mirrors'
```

If the OCI port differs, replace `5500` everywhere. This service intentionally
publishes only on localhost; do not expose it beyond the host without adding
authentication and TLS.

### Docker Desktop: macOS and Windows

Open Docker Desktop **Settings → Docker Engine**, merge the following JSON,
then select **Apply & restart**:

```json
{
  "registry-mirrors": ["http://host.docker.internal:5500"],
  "insecure-registries": ["host.docker.internal:5500"]
}
```

`host.docker.internal` lets Docker Desktop's Linux VM reach the host-published
cache port. Confirm the configuration with `docker info`, then run
`swe-cache start` and `docker pull alpine:latest`.

## Selectively use the APT cache in a Dockerfile

APT proxying is opt-in per build. Add build arguments and create the temporary
APT configuration only when `USE_SWE_CACHE=1`:

```dockerfile
FROM debian:bookworm

ARG USE_SWE_CACHE=0
ARG SWE_CACHE_APT_PROXY=http://host.docker.internal:3142

RUN if [ "$USE_SWE_CACHE" = "1" ]; then \
      printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\n' \
        "$SWE_CACHE_APT_PROXY" "$SWE_CACHE_APT_PROXY" \
        > /etc/apt/apt.conf.d/99swe-cache; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends curl \
 && rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99swe-cache
```

On macOS and Windows Docker Desktop, build with:

```sh
docker build --build-arg USE_SWE_CACHE=1 \
  --build-arg SWE_CACHE_APT_PROXY=http://host.docker.internal:3142 .
```

On Linux, the service deliberately binds to host localhost. Use host networking
for only the build that needs the cache:

```sh
docker build --network=host --build-arg USE_SWE_CACHE=1 \
  --build-arg SWE_CACHE_APT_PROXY=http://127.0.0.1:3142 .
```

Omit `USE_SWE_CACHE=1` to bypass the proxy completely. The configuration file
is removed in the same Dockerfile layer, so it is not present in the final
image. HTTPS sources can use the proxy as a CONNECT tunnel; use HTTP Debian or
Ubuntu mirrors when actual HTTPS package-response caching is required.

## Cross-compiling release binaries

Go can produce the CLI for supported architectures without a target host. The
service image must still be built for the target's Docker Linux architecture.

```sh
mkdir -p dist
GOOS=linux GOARCH=amd64 go build -o dist/swe-cache-linux-amd64 ./cmd/swe-cache
GOOS=darwin GOARCH=arm64 go build -o dist/swe-cache-darwin-arm64 ./cmd/swe-cache
GOOS=windows GOARCH=amd64 go build -o dist/swe-cache-windows-amd64.exe ./cmd/swe-cache
```

## End-to-end cache tests

The normal Go suite is hermetic and does not require Docker. A separate,
opt-in integration suite starts isolated cache containers and uses local test
upstreams only. It proves each warm cache survives its upstream becoming
unavailable:

- APT serves a cached package payload after the HTTP upstream is disabled.
- Zot serves a cached OCI manifest and blob after the local TLS registry is
  disabled.
- Git creates a bare mirror from a local `git daemon`, then clones again after
  that daemon is stopped.

Build the version-matched service image first, then run:

```sh
SWE_CACHE_E2E=1 go test -tags=integration ./e2e -v
```

Set `SWE_CACHE_E2E_IMAGE` if the service image uses a different reference.
The suite needs Docker, Git, and an available local port range `18001–23999`.

## Normal use

```text
swe-cache start [--root PATH] [--apt-port PORT] [--oci-port PORT]
swe-cache stop [--root PATH]
swe-cache restart [--root PATH] [--apt-port PORT] [--oci-port PORT]
swe-cache status [--root PATH]
swe-cache doctor [--root PATH]
swe-cache git clone [--commit SHA] URL DESTINATION
```
