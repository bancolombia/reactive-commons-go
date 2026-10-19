.PHONY: test test-unit test-integration lint build coverage

GO ?= go
GOFLAGS ?=
MODULE := github.com/bancolombia/reactive-commons-go

build:
	$(GO) build $(GOFLAGS) ./...

test: test-unit test-integration

test-unit:
	$(GO) test $(GOFLAGS) ./tests/unit/... -v -count=1

test-integration:
#   Integration tests use testcontainers-go and need a Docker-compatible runtime
#   (Docker Desktop, Colima, OrbStack, Podman, nerdctl, ...). Testcontainers starts
#   RabbitMQ and Kafka automatically; no pre-provisioned broker is required.
#   The Java interop test is nightly-only and can be skipped with `-short`:
#     go test -tags integration -short ./tests/integration/kafka/...
#   If Colima fails to mount the Docker socket, set
#   TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE="/var/run/docker.sock".
	$(GO) test $(GOFLAGS) -tags integration ./tests/integration/rabbit/... -v -count=1 -timeout 120s
	$(GO) test $(GOFLAGS) -tags integration ./tests/integration/kafka/... -v -count=1 -timeout 600s

# Mirrors the Sonar pipeline: runs unit + integration with -coverpkg covering all
# non-example packages, merges the profiles into coverage.out, and prints a summary.
# Uses `go tool covdata` (Go 1.20+) instead of gocovmerge because gocovmerge
# cannot reconcile the multiple block layouts modern Go emits when the same file
# is instrumented across many test binaries via -coverpkg.

coverage:
	@rm -rf coverage.covdata && mkdir -p coverage.covdata/unit coverage.covdata/integration
	@PKG_LIST=$$($(GO) list ./... | grep -v '/examples/'); \
	PKGS=$$(echo "$$PKG_LIST" | paste -sd, -); \
	$(GO) test $(GOFLAGS) -race -covermode=atomic -coverpkg=$$PKGS $$PKG_LIST -args -test.gocoverdir=$$PWD/coverage.covdata/unit && \
	$(GO) test $(GOFLAGS) -race -tags integration -short -covermode=atomic -coverpkg=$$PKGS ./tests/integration/... -timeout 280s -args -test.gocoverdir=$$PWD/coverage.covdata/integration
	@$(GO) tool covdata textfmt -i=coverage.covdata/unit,coverage.covdata/integration -o=coverage.out
	@$(GO) tool cover -func=coverage.out | tail -n 1

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy
