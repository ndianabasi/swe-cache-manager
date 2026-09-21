# syntax=docker/dockerfile:1
# The binary and this image are released with the same version. All valuable
# state is bind-mounted by swe-cache; this image may be deleted at any time.
ARG TARGETARCH
FROM ghcr.io/project-zot/zot-linux-${TARGETARCH}:v2.1.21 AS zot

FROM golang:1.25.1-bookworm AS athens
RUN go install github.com/gomods/athens/cmd/proxy@v0.18.1

FROM node:24-bookworm-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
    apt-cacher-ng ca-certificates git supervisor \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global --ignore-scripts verdaccio@6.9.2 \
    && npm cache clean --force
COPY --from=zot /usr/local/bin/zot-linux-${TARGETARCH} /usr/local/bin/zot
COPY --from=athens /go/bin/proxy /usr/local/bin/athens-proxy
COPY --from=athens /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
COPY supervisord.conf /etc/supervisor/conf.d/swe-cache.conf
ENTRYPOINT ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/swe-cache.conf"]
