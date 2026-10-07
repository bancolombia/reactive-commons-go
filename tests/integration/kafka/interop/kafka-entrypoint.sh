#!/bin/sh
# Custom entrypoint: computes the container's own IP at start time and
# advertises Kafka on two listeners:
#   - PLAINTEXT://<container-ip>:9092  — reachable from other containers
#   - HOST://localhost:19094           — reachable from the host
# Then hands off to the image's default entrypoint chain.
set -eu
IP="$(hostname -i | awk '{print $1}')"
export KAFKA_ADVERTISED_LISTENERS="PLAINTEXT://${IP}:9092,HOST://localhost:19094"
export KAFKA_LISTENERS="PLAINTEXT://0.0.0.0:9092,HOST://0.0.0.0:19094,CONTROLLER://0.0.0.0:9093"
export KAFKA_LISTENER_SECURITY_PROTOCOL_MAP="CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,HOST:PLAINTEXT"
export KAFKA_INTER_BROKER_LISTENER_NAME="PLAINTEXT"
exec /__cacert_entrypoint.sh "$@"
