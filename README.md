# swe-cache

`swe-cache` is a single Go executable that keeps APT packages, OCI registry
content, and Git mirrors outside Docker. The service container is disposable;
the persistent cache root defaults to `/var/lib/swe-cache`.

```text
/var/lib/swe-cache/{apt,zot,git,config,logs}
```

Initialize a non-default location (useful for development) with:

```sh
go run ./cmd/swe-cache init --root /absolute/cache/path
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

## Development

```sh
go test ./...
go build ./cmd/swe-cache
```
