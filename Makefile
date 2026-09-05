# Implementation detail, not the interface: the public surface is the
# `shipyard` command (setup/dev/test/deploy/...). This file exists so the git
# hooks have stable targets to call and so make muscle-memory keeps working —
# every target delegates or stays a thin toolchain call.
SHELL := /bin/sh

GO ?= go
# The tool that owns the deploy engine. Overridable so a checkout of the
# shipyard repo itself can be used: make deploy SHIPYARD='go run /path/to/shipyard/cmd/shipyard'
SHIPYARD ?= shipyard
# Pinned by the `tool` directive in go.mod, so templ stays off PATH and its
# version can't drift from the library the generated code links against.
TEMPL ?= $(GO) tool templ
PORT ?= 8080
COVER_PROFILE ?= coverage.out
COVER_MIN ?= 85

.PHONY: all setup doctor deploy deploy-status ssh test cover-check build clean generate run-server

# Installs Nix if absent; everything else lives in the flake. Safe to re-run.
setup:
	@$(SHIPYARD) setup

# Everything below the classic targets delegates to shipyard — the engine is
# versioned with the tool, the declarations (this file included) with the app.
doctor:
	@$(SHIPYARD) doctor

deploy:
	@$(SHIPYARD) deploy

deploy-status:
	@$(SHIPYARD) status $(ARGS)

ssh:
	@$(SHIPYARD) ssh '$(ARGS)'

test:
	$(GO) test ./...

# Coverage with -coverpkg=./... so tests in external _test packages credit the
# code they exercise; deduped because every test binary re-reports every
# package; -count=1 because a cached result replays stale block layouts from
# unrelated packages (a phantom drop that reads exactly like a regression).
COVER_FLAGS = -coverpkg=./... -count=1
COVER_FILTER = awk 'NR==1 && /^mode:/ {print; next} {key=$$1; if ($$NF+0 >= max[key]+0) { max[key]=$$NF; line[key]=$$0 }} END {for (k in line) print line[k]}' $(COVER_PROFILE) | grep -v -e '_templ\.go:' -e '/cmd/server/main.go' > $(COVER_PROFILE).tmp && mv $(COVER_PROFILE).tmp $(COVER_PROFILE)

cover-check:
	$(GO) test ./... $(COVER_FLAGS) -coverprofile=$(COVER_PROFILE)
	@$(COVER_FILTER)
	@TOTAL=`$(GO) tool cover -func=$(COVER_PROFILE) | awk '/^total:/ {print $$3}' | tr -d '%'`; \
	if [ -z "$$TOTAL" ]; then \
		printf "Could not parse total coverage\n"; \
		exit 1; \
	fi; \
	awk -v total="$$TOTAL" -v min="$(COVER_MIN)" 'BEGIN { \
		printf "Total coverage: %.1f%% (required: %.1f%%)\n", total, min; \
		if (total+0 < min+0) exit 1; \
	}'

generate:
	$(TEMPL) generate

build: generate
	$(GO) build -o bin/server ./cmd/server

run-server: generate
	$(GO) run ./cmd/server --port $(PORT)

clean:
	rm -rf bin/ $(COVER_PROFILE)

# App-specific targets (watch modes, local servers, data tooling) live in
# Makefile.local so `shipyard sync` never fights them. Optional by design.
-include Makefile.local
