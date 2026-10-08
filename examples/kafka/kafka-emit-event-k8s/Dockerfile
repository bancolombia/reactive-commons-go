FROM alpinelinux/golang AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/emitter ./examples/kafka/kafka-emit-event-k8s

FROM alpine:3.23
USER root
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/emitter /emitter
USER app:app
ENTRYPOINT ["/emitter"]
