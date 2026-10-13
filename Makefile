TOOLS := go tool -modfile=tools/go.mod

.PHONY: check fmt docs docs-check lint vuln test-integration test-e2e test-compat cover mutate build cross hooks

## check: fast checks for every commit (format, generated docs, vet, unit tests with race detector)
check: fmt docs-check
	go vet ./...
	go vet -tags integration ./...
	go vet -tags e2e ./...
	go test -race ./...

## fmt: fail if any file is not formatted
fmt:
	@out=$$($(TOOLS) golangci-lint fmt --diff ./...) || { echo "$$out"; exit 1; }; if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## docs: regenerate docs/reference from the code (CLI reference, x-mutti reference and schemas)
docs:
	go run ./cmd/mutti-docs

## docs-check: fail if docs/reference is not what the code generates
docs-check: docs
	@out=$$(git diff --name-only -- docs/reference; git ls-files --others --exclude-standard -- docs/reference); if [ -n "$$out" ]; then \
		echo "docs/reference is out of date; run make docs and add the changes:"; echo "$$out"; exit 1; fi

## lint: static analysis
lint:
	$(TOOLS) golangci-lint run ./...

## vuln: known vulnerabilities in the code paths mutti calls, from the Go vulnerability database
vuln:
	$(TOOLS) govulncheck ./...

## test-integration: tests against the local Docker daemon
test-integration:
	go test -race -tags integration ./...

## test-e2e: end-to-end tests with the real binary and Docker (scenarios in cmd/mutti/testdata/script, nested mode)
# -parallel 2: every scenario creates Docker networks, and more at once exhaust the address
# pools of a default Docker installation.
test-e2e:
	go test -parallel 2 -tags e2e ./cmd/mutti

## test-compat: integration and e2e tests against the oldest supported Compose (COMPOSE_VERSION)
COMPOSE_VERSION ?= 2.24.4
COMPOSE_DIR := bin/compose-$(COMPOSE_VERSION)
test-compat: $(COMPOSE_DIR)/cli-plugins/docker-compose
	DOCKER_CONFIG=$(CURDIR)/$(COMPOSE_DIR) docker compose version
	# -count=1: the test cache does not see DOCKER_CONFIG, only the docker CLI reads it.
	DOCKER_CONFIG=$(CURDIR)/$(COMPOSE_DIR) GOFLAGS=-count=1 $(MAKE) test-integration test-e2e

$(COMPOSE_DIR)/cli-plugins/docker-compose:
	mkdir -p $(@D)
	curl -fsSL -o $@ https://github.com/docker/compose/releases/download/v$(COMPOSE_VERSION)/docker-compose-linux-$$(uname -m)
	chmod +x $@

## cover: unit + integration coverage, fails below 100%
cover:
	scripts/coverage.sh

## mutate: mutation testing of the unit test suite
mutate:
	$(TOOLS) gremlins unleash ./internal/...

## build: build the mutti binary into bin/
build:
	CGO_ENABLED=0 go build -trimpath -o bin/mutti ./cmd/mutti

## cross: make sure every supported platform compiles
cross:
	@for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		echo "build $$p"; GOOS=$${p%/*} GOARCH=$${p#*/} CGO_ENABLED=0 go build -o /dev/null ./... || exit 1; \
	done

## hooks: run checks on commit and push
hooks:
	git config core.hooksPath .githooks
