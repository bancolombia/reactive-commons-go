.PHONY: test test-unit test-integration lint build

GO ?= go
GOFLAGS ?=
MODULE := github.com/bancolombia/reactive-commons-go

build:
	$(GO) build $(GOFLAGS) ./...

test: test-unit test-integration

test-unit:
	$(GO) test $(GOFLAGS) ./tests/unit/... -v -count=1

test-integration:
# 	The test will need docker to run, so we will use testcontainers to run the tests in a container
# 	The env var TEST_RABBITMQ_IMAGE can be used to specify the image to use for the tests, if not set it will use rabbitmq:3.12-alpine
# 	The env var DOCKER_HOST can be used to specify the docker host to use for the tests, if not set it will use the default docker host"
#   In case a write error using Colima when trying to mount the docker socket, you can set the env var TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE="/var/run/docker.sock"
	$(GO) test $(GOFLAGS) -tags integration ./tests/integration/... -v -count=1 -timeout 120s

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy
