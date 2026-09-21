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

## Development

```sh
go test ./...
go build ./cmd/swe-cache
```
