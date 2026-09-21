# swe-cache

`swe-cache` is a single Go executable that keeps APT packages, OCI registry
content, and Git mirrors outside Docker. The service container is disposable.
Root-managed installations use `/var/lib/swe-cache`; unprivileged installs use
the platform user cache directory (for example, `~/Library/Caches/swe-cache` on
macOS).

```text
/var/lib/swe-cache/{apt,zot,git,config,logs}
```

Initialize a non-default location (useful for development) with:

```sh
go run ./cmd/swe-cache init --root /absolute/cache/path
```

The APT proxy defaults to apt-cacher-ng's conventional port `3142`; the OCI
registry defaults to `5500` to avoid common development ports. Set either at
initialization or persist a new value while starting/restarting:

```sh
swe-cache init --apt-port 3142 --oci-port 5500
swe-cache restart --oci-port 5510
```

The tracked configuration is intentionally small. `swe-cache` generates the
service-specific configurations below `config/`; users do not edit them.

The service image recipe is in `services/`. Build and publish it with the same
tag as the binary, then supply that image at initialization when using a
private registry:

```sh
swe-cache init --image registry.example/swe-cache-services:0.1.0
swe-cache start
```

`start` only binds cache directories and generated configuration into the
container; deleting the container or Docker's own image store does not delete
APT, OCI, or Git cache data. `swe-cache git clone --commit <sha> URL DIR`
checks its local mirror before contacting the upstream repository.

The generated Zot configuration is an on-demand pull-through cache for Docker
Hub. Point Docker's `registry-mirrors` at the published OCI endpoint to keep
ordinary Docker Hub pulls unchanged. Registries such as GHCR and Quay require
a per-registry Docker/containerd host mapping; the tool intentionally does not
rewrite daemon-wide settings automatically.

## Development

```sh
go test ./...
go build ./cmd/swe-cache
```
