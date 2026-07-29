.PHONY: build ci format-check setup test web

GOCACHE := $(CURDIR)/.gocache
PNPM_STORE := $(CURDIR)/.pnpm-store

setup:
	pnpm --dir web install --store-dir $(PNPM_STORE)

web:
	pnpm --dir web run build

build: web
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -o bin/docklane ./cmd/docklane

test: web
	pnpm --dir web run check
	GOCACHE=$(GOCACHE) go test ./...
	GOCACHE=$(GOCACHE) go vet ./...

format-check:
	test -z "$$(gofmt -l cmd internal)"

ci: format-check test build
	git diff --exit-code -- internal/webui/dist
