# syntax=docker/dockerfile:1
# The binary and this image are released with the same version. All valuable
# state is bind-mounted by swe-cache; this image may be deleted at any time.
ARG TARGETARCH
FROM ghcr.io/project-zot/zot-linux-${TARGETARCH}:v2.1.21 AS zot

FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
    apt-cacher-ng ca-certificates supervisor \
    && rm -rf /var/lib/apt/lists/*
COPY --from=zot /usr/local/bin/zot-linux-${TARGETARCH} /usr/local/bin/zot
COPY supervisord.conf /etc/supervisor/conf.d/swe-cache.conf
ENTRYPOINT ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/swe-cache.conf"]
