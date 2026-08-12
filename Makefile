GO ?= go
BIN := gql-compat
OUT ?= ./reports

# The version a locally built binary reports. A report states the tool version
# that produced it, so a hand-built binary says so rather than claiming a tag.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build install test race cover bench lint fmt vet vuln validate \
        verify-artifacts smoke report clean tidy check

all: check

## build: compile the CLI into ./gql-compat
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/gql-compat

## install: put the CLI on $GOPATH/bin
install:
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/gql-compat

## test: run the unit suite
test:
	$(GO) test ./...

## race: the sampler runs in a goroutine beside the runner; this is where a
## data race between measuring and reporting would show up
race:
	$(GO) test -race ./...

## cover: coverage across every package
cover:
	$(GO) test -coverprofile=cover.out -covermode=atomic ./...
	$(GO) tool cover -func=cover.out | tail -1

## bench: the harness's own benchmarks, not an engine's
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./...

## lint: golangci-lint, fetched on demand so a clean checkout needs nothing
lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run

## fmt: format everything
fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

## vuln: known vulnerabilities in the dependency graph
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

## tidy: go.mod and go.sum
tidy:
	$(GO) mod tidy

## verify-artifacts: re-check the vendored ISO files against their checksums.
##
## Every conformance claim in this project is a reference into these files. If
## one of them changes, the scores change with it, and a score that moved for a
## reason nobody recorded is worthless. This makes such a change a failure
## rather than a surprise.
verify-artifacts:
	@cd iso/artifacts && \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum --check --quiet SHA256SUMS && echo "ISO artifacts match SHA256SUMS"; \
	elif command -v shasum >/dev/null 2>&1; then \
		shasum -a 256 --check --status SHA256SUMS && echo "ISO artifacts match SHA256SUMS"; \
	else \
		echo "no sha256sum or shasum on PATH" >&2; exit 1; \
	fi

## validate: load the corpus and check every ISO reference in it
validate:
	$(GO) run ./cmd/gql-compat validate

## smoke: the whole pipeline end to end against the scripted engine. Needs no
## database, and proves that a run produces all five report formats.
smoke:
	$(GO) run ./cmd/gql-compat run -adapter fake -repeats 1 -warmups 0 \
		-fail-on none -quiet -out $(OUT)/fake

## report: run against a real engine. ADAPTER is required; pass BINARY for an
## embedded engine or URI/USER for a server. Example:
##   make report ADAPTER=zu BINARY=../zu/target/release/zu
report:
	@test -n "$(ADAPTER)" || { echo "usage: make report ADAPTER=zu [BINARY=...] [URI=...] [USER=...]" >&2; exit 2; }
	$(GO) run ./cmd/gql-compat run -adapter $(ADAPTER) \
		$(if $(BINARY),-binary $(BINARY),) \
		$(if $(URI),-uri $(URI),) \
		$(if $(USER),-user $(USER),) \
		$(if $(REPEATS),-repeats $(REPEATS),) \
		-fail-on none -out $(OUT)/$(ADAPTER)

## check: everything CI runs, apart from the engine jobs
check: fmt vet lint test race verify-artifacts validate smoke

clean:
	rm -f $(BIN) cover.out
	rm -rf $(OUT)

## help: list the targets
help:
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed 's/^## /  /'
