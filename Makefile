IMAGE ?= ghcr.io/strikesecurity/stepshell
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all ui build test lint fmt run docker clean

all: ui build

ui:
	cd web && (npm ci || npm install) && node build.mjs

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/stepshell ./cmd/stepshell

test:
	go test ./...
	cd web && ./node_modules/.bin/tsc --noEmit

fmt:
	gofmt -w internal cmd

lint:
	go vet ./...
	gofmt -l internal cmd

# Run locally against your current kube-context, no login (dev only).
# Requires the UI to be built first (make ui).
run:
	go run ./cmd/stepshell \
		--base-url=http://localhost:8080 \
		--auth.mode=none \
		--dev.user=$(USER)@local \
		--kubeconfig=$(HOME)/.kube/config

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

clean:
	rm -rf bin web/dist
