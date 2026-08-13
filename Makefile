# Narwhal build tasks.
#
# `make install` is the one that matters day to day: it rebuilds and replaces
# the binary on PATH. Note the `rm -f` before `cp` — macOS refuses to
# overwrite a running Mach-O in place, and the result is a binary that dies
# with SIGKILL on every invocation rather than a clear error. Removing first
# avoids it.

BIN       := narwhal
PKG       := ./cmd/narwhal
PREFIX    ?= $(HOME)/.local
BINDIR    := $(PREFIX)/bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(VERSION)
GOFLAGS   ?=

# Benchmark defaults; override on the command line, e.g.
#   make bench TASKS="task-...9e5 task-...9e4" AGENT_TIMEOUT=1800
QA_DIR         ?= $(CURDIR)/../AgentRadio/data/qa
RESULTS        ?= /tmp/narwhal-bench-results
AGENT_TIMEOUT  ?= 3600
CONC           ?= 3
ARMS           ?= b0 narwhal
TASKS          ?=

.DEFAULT_GOAL := help

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'
	@echo
	@echo "  install goes to $(BINDIR); override with PREFIX=..."

## build: compile the binary into ./narwhal
build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

## install: build and replace the installed binary
install:
	@mkdir -p $(BINDIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN).tmp $(PKG)
	@rm -f $(BINDIR)/$(BIN)
	@mv $(BIN).tmp $(BINDIR)/$(BIN)
	@echo "installed $(BINDIR)/$(BIN) ($(VERSION))"

## test: run the unit tests
test:
	go test ./...

## test-race: run the unit tests under the race detector
test-race:
	go test -race ./...

## vet: go vet the whole module
vet:
	go vet ./...

## check: vet + race tests, what CI would run
check: vet test-race

## fmt: gofmt every package
fmt:
	gofmt -l -w .

## cover: run tests with coverage and print the per-function summary
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -20

## daemon-restart: stop the running daemon and start a fresh one detached
#
# `daemon stop` refuses while workers are in flight, and that refusal must
# stop the restart — the `-` prefix that used to ignore it turned a routine
# rebuild into a killed run. Pass FORCE=1 when you mean it.
daemon-restart: install
	@if $(BINDIR)/$(BIN) daemon status >/dev/null 2>&1; then \
		$(BINDIR)/$(BIN) daemon stop $(if $(FORCE),--force,) || \
			{ echo "make daemon-restart: aborted (use FORCE=1 to stop anyway)"; exit 1; }; \
	fi
	@sleep 1
	@nohup $(BINDIR)/$(BIN) daemon start >/tmp/narwhal-daemon.log 2>&1 & sleep 2
	@$(BINDIR)/$(BIN) daemon status

## bench: run a benchmark slice (needs TASKS=..., see bench/README.md)
bench:
	@test -n "$(TASKS)" || { echo "set TASKS, e.g. make bench TASKS='task-6905333b74f22949d97ba9e5'"; exit 1; }
	QA_DIR=$(QA_DIR) AGENT_TIMEOUT=$(AGENT_TIMEOUT) CONC=$(CONC) ARMS="$(ARMS)" \
		bash bench/run_slice.sh $(RESULTS) $(TASKS)

## bench-summary: re-print the summary for an existing results directory
bench-summary:
	python3 bench/summarize.py $(RESULTS)

## clean: remove build and coverage artifacts
clean:
	rm -f $(BIN) $(BIN).tmp coverage.out

.PHONY: help build install test test-race vet check fmt cover \
	daemon-restart bench bench-summary clean
