#!/bin/sh
# Custom entrypoint: advertises Kafka on two listeners:
#   - PLAINTEXT://kafka:9092            — reachable from other containers on the
#                                         testcontainers network (network alias)
#   - HOST://localhost:${KAFKA_HOST_PORT} — reachable from the host
# Then hands off to the image's default entrypoint chain.
set -eu
HOST_PORT="${KAFKA_HOST_PORT:-19094}"
export KAFKA_ADVERTISED_LISTENERS="PLAINTEXT://kafka:9092,HOST://localhost:${HOST_PORT}"
export KAFKA_LISTENERS="PLAINTEXT://0.0.0.0:9092,HOST://0.0.0.0:19094,CONTROLLER://0.0.0.0:9093"
export KAFKA_LISTENER_SECURITY_PROTOCOL_MAP="CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,HOST:PLAINTEXT"
export KAFKA_INTER_BROKER_LISTENER_NAME="PLAINTEXT"
exec /__cacert_entrypoint.sh "$@"
