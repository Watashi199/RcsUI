# RcsUI — cibles de développement (PLAN.md §10.1)
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
RCLONE_VERSION ?= v1.76.0
LDFLAGS        := -s -w -X main.version=$(VERSION) -X github.com/rclone/rclone/fs.Version=$(RCLONE_VERSION)
BIN            := bin/rcsui
IMAGE          ?= rcsui:dev

.PHONY: all web-install web build build-noweb test test-go test-web check lint dev docker e2e fixtures clean

all: build

web-install:
	cd web && npm ci

web: web-install
	cd web && npx svelte-kit sync && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/rcsui

# Backend seul (le frontend tourne sous `vite dev` avec proxy vers :8080).
build-noweb:
	CGO_ENABLED=0 go build -tags noweb -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/rcsui

test: test-go test-web

# -race sauf internal/engine/rclone (data race connue dans rclone v1.76.0, cf. docs/spikes.md).
test-go:
	go test -tags noweb -race $$(go list -tags noweb ./... | grep -v /internal/engine/rclone)
	go test -tags noweb ./internal/engine/rclone/

test-web:
	cd web && npm test

check:
	go vet -tags noweb ./...
	cd web && npm run check

lint: check
	golangci-lint run --build-tags noweb ./...
	govulncheck -tags noweb ./...

# Deux terminaux : backend (données dans ./.dev) et `make dev-web`.
dev:
	RCSUI_DATA_DIR=./.dev RCSUI_ALLOWED_ROOTS=$(CURDIR)/.dev/files RCSUI_LISTEN=127.0.0.1:8080 \
		go run -tags noweb ./cmd/rcsui

dev-web:
	cd web && npm run dev

docker:
	docker buildx build --build-arg VERSION=$(VERSION) --build-arg RCLONE_VERSION=$(RCLONE_VERSION) -t $(IMAGE) --load .

# Tests de bout en bout (Playwright) contre l'image, dans un conteneur jetable.
e2e: IMAGE = rcsui:e2e
e2e: docker
	cd e2e && npm ci && npx playwright install chromium
	bash e2e/run.sh $(IMAGE)

# Régénère les fixtures du parser rsync avec la version de rsync locale.
fixtures:
	sh spikes/rsync/capture.sh testdata/rsync

clean:
	rm -rf bin web/build web/.svelte-kit
