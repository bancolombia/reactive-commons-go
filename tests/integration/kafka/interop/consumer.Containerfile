FROM eclipse-temurin:21-jdk
WORKDIR /app

# The libs/ directory is pre-populated on the host before the testcontainers
# Docker build runs, so the image build does not need Maven access.
COPY libs /app/libs
COPY Consumer.java /app/

RUN javac -cp 'libs/*' Consumer.java

# Run as a non-root user (docker:S6471): the consumer only reads Kafka
# messages and prints them to stdout, so it needs no privileges.
RUN useradd --system --create-home --shell /usr/sbin/nologin appuser \
    && chown -R appuser /app
USER appuser

ENTRYPOINT ["java", "-cp", "libs/*:.", "Consumer"]
