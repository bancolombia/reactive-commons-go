FROM eclipse-temurin:21-jdk
WORKDIR /app

# The libs/ directory is pre-populated on the host before `container build`
# runs (the build container has no DNS on Apple's container tool, so
# fetching from Maven Central inside the build would fail).
COPY libs /app/libs
COPY Consumer.java /app/

RUN javac -cp 'libs/*' Consumer.java

ENTRYPOINT ["java", "-cp", "libs/*:.", "Consumer"]
