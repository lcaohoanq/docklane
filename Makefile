.PHONY: build ci dev dev-api dev-web docs docs-check docs-setup format-check release release-repro-check runtime-image-context setup test web

GOCACHE := $(CURDIR)/.gocache
PNPM_STORE := $(CURDIR)/.pnpm-store

setup:
	go mod download
	pnpm --dir web install --store-dir $(PNPM_STORE)

dev:
	./ops/dev.sh

dev-api:
	mkdir -p data .tmp/air
	go tool air

dev-web:
	pnpm --dir web run dev

docs-setup:
	pnpm --dir docs-site install --store-dir $(PNPM_STORE)

docs-check:
	pnpm --dir docs-site run build
	./ops/render-release-notes.sh docs-site/src/content/docs/docs/releases/v0.1.0-alpha.2.md | grep -Fx "# Docklane v0.1.0-alpha.2"

docs:
	pnpm --dir docs-site run dev

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

runtime-image-context:
	test -n "$(VERSION)"
	test -n "$(DIST)"
	test -n "$(OUTPUT)"
	./ops/prepare-runtime-image-context.sh "$(VERSION)" "$(DIST)" "$(OUTPUT)"
