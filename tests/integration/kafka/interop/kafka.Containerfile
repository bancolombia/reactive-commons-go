FROM apache/kafka:latest
USER root
COPY kafka-entrypoint.sh /kafka-entrypoint.sh
RUN chmod 0755 /kafka-entrypoint.sh
ENV KAFKA_NODE_ID=1 \
    KAFKA_PROCESS_ROLES=broker,controller \
    KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
    KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
    KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
    CLUSTER_ID=rc-interop-cluster
ENTRYPOINT ["/kafka-entrypoint.sh"]
CMD ["/etc/kafka/docker/run"]
