.PHONY: build ci format-check release release-repro-check setup test web

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

release: web
	test -n "$(VERSION)"
	GOCACHE=$(GOCACHE) ./ops/build-release.sh "$(VERSION)" "$(or $(DIST),dist)"

release-repro-check: web
	test -n "$(VERSION)"
	GOCACHE=$(GOCACHE) ./ops/check-release-reproducibility.sh "$(VERSION)"
