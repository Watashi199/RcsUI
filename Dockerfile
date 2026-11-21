# syntax=docker/dockerfile:1.7
# RcsUI — image multi-stage (PLAN.md §10.2). Épingler les images par digest
# (Renovate) avant une release.

# 1) Frontend SvelteKit (adapter-static)
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npx svelte-kit sync && npm run build

# 2) Backend Go (pur Go, cross-compilation sans CGO)
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go web/embed_noweb.go ./web/
COPY --from=web /src/web/build ./web/build
ARG VERSION=dev
ARG RCLONE_VERSION=v1.76.0
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X github.com/rclone/rclone/fs.Version=${RCLONE_VERSION}" \
      -o /out/rcsui ./cmd/rcsui

# 3) Runtime : rsync + openssh-client, utilisateur non-root
FROM alpine:3.22
RUN apk add --no-cache rsync openssh-client ca-certificates tzdata tini su-exec shadow \
 && addgroup -S -g 1000 rcsui && adduser -S -D -u 1000 -G rcsui -h /home/rcsui rcsui \
 && mkdir -p /data && chown rcsui:rcsui /data
COPY --from=go /out/rcsui /usr/local/bin/rcsui
COPY deploy/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh
ENV RCSUI_DATA_DIR=/data \
    RCSUI_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["rcsui", "healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
CMD ["rcsui"]
