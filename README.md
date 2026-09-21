# swe-cache

`swe-cache` is a portable Go CLI that keeps five dependency caches outside
Docker: APT packages, OCI registry content, Git mirrors, npm packages, and Go
modules. Its Docker service container is disposable; the cache root is the
durable state.

It supports Linux, macOS, and Windows hosts. The service image is Linux, so
Docker Desktop must use Linux containers on macOS and Windows. Every platform
needs Go 1.25+ for development, Git, a running Docker engine, and
[Go Task](https://taskfile.dev/docs/installation). Run `task --list` to see
the available build, test, E2E, and installation commands.

## Defaults and configuration

`swe-cache` chooses a writable, OS-native root. Set `SWE_CACHE_ROOT` or use
`--root` to select a different absolute directory.

| Host | User install default | Root/admin install default |
| --- | --- | --- |
| Linux | `$XDG_CACHE_HOME/swe-cache`, normally `~/.cache/swe-cache` | `/var/lib/swe-cache` |
| macOS | `~/Library/Caches/swe-cache` | `/Library/Caches/swe-cache` |
| Windows | `%LocalAppData%\swe-cache` | `%LocalAppData%\swe-cache` |

Each root contains `apt/`, `registry/`, `git/`, `npm/`, `go/`, `config/`, and
`logs/`. Removing Docker images, build cache, or the service container does
not remove this data.

The APT proxy defaults to apt-cacher-ng's established port `3142`. The OCI
registry defaults to `5500`, npm registry to `4873`, and Go module proxy to
`3000`. Ports configure both the listener inside the service container and
Docker's localhost mapping.

```text
swe-cache init --apt-port 3142 --oci-port 5500 --npm-port 4873 --go-port 3000
swe-cache restart --oci-port 5510
```

`start` and `restart` persist supplied port flags in `config/swe-cache.toml`.
Use `swe-cache status` to display the active endpoints.

## Architecture

The cache deliberately uses protocol-aware components rather than a generic
HTTP proxy. Each protocol has different identity, freshness, and integrity
rules; keeping those concerns separate makes cache misses safe and warm hits
reliable.

```text
                               durable cache root
 ┌──────────────────────────────────────────────────────────────────────┐
 │ apt/       registry/   git/        npm/          go/                  │
 │ .deb       OCI blobs   bare        npm tarballs  Go module zips,      │
 │ payloads   manifests   mirrors     metadata      .mod files, metadata │
 └────┬──────────┬───────────┬───────────┬─────────────┬─────────────────┘
      │          │           │           │             │
      ▼          ▼           ▼           ▼             ▼
 apt-cacher-ng Distribution swe-cache   Verdaccio     Athens
      :3142    :5500     git clone       :4873        :3000
      │          │           │             │             │
 Debian/Ubuntu Docker Hub Git hosting   registry.npmjs.org proxy.golang.org
 mirrors       and OCI    (on demand)   (on demand)      or VCS
                upstreams
```

`swe-cache-services` runs apt-cacher-ng, CNCF Distribution, Verdaccio, and
Athens under
supervisord. The CLI owns generated component configuration, starts and
recreates that container, and bind-mounts all service state from the cache
root. Git is intentionally host-side: `swe-cache git clone` keeps bare
mirrors locally and clones from them without a Git server.

All service ports bind to `127.0.0.1` on the host. They are intentionally
unauthenticated local developer services; do not expose them to a network
without adding TLS and authentication.

| Layer | Component | Durable data | Client setting |
| --- | --- | --- | --- |
| APT | apt-cacher-ng | `apt/` | `Acquire::http::Proxy` |
| OCI | CNCF Distribution | `registry/` | Docker Hub `registry-mirrors` |
| Git | built-in CLI | `git/` | `swe-cache git clone` |
| npm | Verdaccio | `npm/` | npm `--registry` |
| Go | Athens | `go/` | `GOPROXY` |

The service image is paired with the CLI version. Version `0.4.0` pins CNCF
Distribution `3.1.0`, Verdaccio `6.9.2`, and Athens `v0.18.1`. After upgrading from an
older CLI, run `swe-cache init --root YOUR_CACHE_ROOT` and then
`swe-cache restart --root YOUR_CACHE_ROOT` to select the matching image while
retaining all durable cache directories. The previous `zot/` directory is left
untouched; Distribution uses its own incompatible `registry/` storage layout,
so its cache begins cold after this one-time migration.

## Development: Linux

Install Go, Git, and Docker Engine. Your user must have permission to use
Docker (for example, through Docker's `docker` group).

```sh
task test
task test:e2e
task build

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
task test
task test:e2e
task build

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
task test
task test:e2e
task build

.\bin\swe-cache.exe init
.\bin\swe-cache.exe start
.\bin\swe-cache.exe status
```

The default root is `%LocalAppData%\swe-cache`. To use another drive:

```powershell
.\bin\swe-cache.exe init --root D:\swe-cache
```

## Install a built binary

`init` is self-contained: it inspects the matching service-image tag and, when
the image is absent, builds it using the pinned Dockerfile and supervisord
configuration embedded in the binary. An installed executable therefore works
outside a source checkout. It never rebuilds an image whose requested tag is
already local. The canonical, reviewable build context is
`internal/service/assets/`; it is embedded directly at compile time.

To use a different tag, provide it during initialization; that tag is built
locally if absent:

```text
swe-cache init --image registry.example/swe-cache-services:0.4.0
```

### Linux

```sh
task install
swe-cache init
swe-cache start
swe-cache doctor
```

`task install` uses `~/.local/bin` by default. Set
`SWE_CACHE_INSTALL_DIR` before invoking it to choose another user-writable
directory. Ensure that directory is on your shell `PATH`. For a system-wide
command, use an administrator-approved directory such as `/usr/local/bin`,
then use `--root /var/lib/swe-cache`.

### macOS

```sh
task install
swe-cache init
swe-cache start
swe-cache doctor
```

`task install` uses `~/.local/bin` by default. Set
`SWE_CACHE_INSTALL_DIR` before invoking it to choose another user-writable
directory, and ensure that directory is on your shell `PATH`.

### Windows

In PowerShell:

```powershell
task install
swe-cache.exe init
swe-cache.exe start
swe-cache.exe doctor
```

By default this installs in `%LocalAppData%\Programs\swe-cache`, adds that
directory to the User `Path`, and takes effect in newly opened terminals. Set
`SWE_CACHE_INSTALL_DIR` before `task install` to override the directory.

## Configure Docker Hub pulls through CNCF Distribution

CNCF Distribution is a pull-through cache for Docker Hub. Docker's
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

### Pull-through behavior and prewarming

CNCF Distribution returns upstream image metadata immediately and caches blobs
as Docker requests them. It avoids a full-image, multi-platform sync on a cold
BuildKit metadata request. Tagged pulls still revalidate the upstream so
mutable tags remain correct.

Warm known evaluator bases before a time-sensitive build:

```sh
swe-cache oci warm node:24-bookworm docker/dockerfile:1.7
```

This runs ordinary Docker pulls through the configured Docker Hub mirror, so
it follows the same path as BuildKit while moving the initial sync outside the
build's critical path. It requires the Docker Engine `registry-mirrors`
configuration above.

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

## Use the npm cache in a Dockerfile

Verdaccio is an on-demand cache in front of `https://registry.npmjs.org/`.
Point npm at it for installs; npm lockfile integrity checks remain in force.

```dockerfile
ARG USE_SWE_CACHE=0
ARG SWE_CACHE_NPM_REGISTRY=http://host.docker.internal:4873

RUN if [ "$USE_SWE_CACHE" = "1" ]; then \
      npm ci --registry="$SWE_CACHE_NPM_REGISTRY"; \
    else \
      npm ci; \
    fi
```

On Linux, use `docker build --network=host` and replace the endpoint with
`http://127.0.0.1:4873`, as with the APT example. The registry setting is
passed to this one install and is not written into the final image.

## Use the Go module cache in a Dockerfile

Athens implements the Go module proxy protocol and persists fetched module
versions in `go/`. Set `GOPROXY` for dependency download steps:

```dockerfile
ARG USE_SWE_CACHE=0
ARG SWE_CACHE_GO_PROXY=http://host.docker.internal:3000

COPY go.mod go.sum ./
RUN if [ "$USE_SWE_CACHE" = "1" ]; then \
      GOPROXY="$SWE_CACHE_GO_PROXY" go mod download; \
    else \
      go mod download; \
    fi
```

The Go checksum database remains an independent integrity authority; preserve
your project-specific `GOPRIVATE`, `GONOSUMDB`, and credential configuration
for private modules. On Linux use host networking and
`http://127.0.0.1:3000`.

## Cross-compiling release binaries

Go can produce the CLI for supported architectures without a target host. The
embedded service-image recipe uses the target host's Docker Linux architecture
when `init` builds it.

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
- CNCF Distribution serves a cached OCI manifest and blob after the local
  upstream registry is
  disabled.
- Git creates a bare mirror from a local `git daemon`, then clones again after
  that daemon is stopped.
- Verdaccio and Athens are started, health-checked, and use their bind-mounted
  durable storage in the same isolated service container.

Run the suite with:

```sh
SWE_CACHE_E2E=1 go test -tags=integration ./e2e -v
```

The suite builds the requested service image if it is missing, starts isolated
containers for the service-cache cases, and removes those containers
afterwards; it does not use or alter the regular `swe-cache-services`
container. Set
`SWE_CACHE_E2E_IMAGE` if the service image uses a different reference. The
suite needs Docker, Git, and an available local port range `18001–23999`.

## Normal use

```text
swe-cache --readme
swe-cache start [--root PATH] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]
swe-cache stop [--root PATH]
swe-cache restart [--root PATH] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]
swe-cache status [--root PATH]
swe-cache doctor [--root PATH]
swe-cache git clone [--commit SHA] [--force] URL DESTINATION
swe-cache oci warm [--root PATH] IMAGE...
```

`swe-cache git clone` streams Git's progress while it creates a mirror or
clones from an existing mirror. Ctrl-C cancels the Git command and releases
the repository lock. If a prior process was forcibly terminated and left a
stale lock, use `--force` only after confirming no clone for that repository is
active.
