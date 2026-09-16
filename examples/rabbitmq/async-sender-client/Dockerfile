# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/sender ./examples/async-sender-client

FROM alpine:3.20
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/sender /sender
EXPOSE 4001
USER app:app
ENTRYPOINT ["/sender"]
