import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;

import java.time.Duration;
import java.util.Arrays;
import java.util.Properties;

/**
 * Interop consumer for the reactive-commons Kafka wire format.
 *
 * Reads the outer envelope as JSON, extracts name + eventId, and if the
 * envelope's data field looks like a CloudEvent (contains specversion) also
 * extracts id + type. Prints one line per message so the Go test can grep
 * the container logs.
 *
 * Env vars:
 *   KAFKA_BOOTSTRAP  — bootstrap server (default: localhost:9092)
 *   KAFKA_TOPICS     — comma-separated list of topics to subscribe to
 *   INTEROP_GROUP_ID — consumer group id (default: interop-<time>)
 */
@SuppressWarnings("all")
public class Consumer {
    public static void main(String[] args) throws Exception {
        String bootstrap = env("KAFKA_BOOTSTRAP", "localhost:9092");
        String topicsCsv = env("KAFKA_TOPICS", "");
        String groupId = env("INTEROP_GROUP_ID", "interop-" + System.currentTimeMillis());
        System.out.println("CONFIG bootstrap=[" + bootstrap + "] topics=[" + topicsCsv + "] group=[" + groupId + "]");
        if (topicsCsv.isEmpty()) {
            System.err.println("KAFKA_TOPICS is required");
            System.exit(2);
        }

        Properties p = new Properties();
        p.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrap);
        p.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        p.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG,
                "org.apache.kafka.common.serialization.StringDeserializer");
        p.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG,
                "org.apache.kafka.common.serialization.StringDeserializer");
        p.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        p.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "true");

        try (KafkaConsumer<String, String> c = new KafkaConsumer<>(p)) {
            c.subscribe(Arrays.asList(topicsCsv.split(",")));
            ObjectMapper m = new ObjectMapper();
            System.out.println("READY bootstrap=" + bootstrap + " topics=" + topicsCsv + " group=" + groupId);
            while (true) {
                ConsumerRecords<String, String> recs = c.poll(Duration.ofSeconds(1));
                for (ConsumerRecord<String, String> r : recs) {
                    String name = "", eventId = "", ceId = "-", ceType = "-";
                    try {
                        JsonNode env = m.readTree(r.value());
                        name = env.path("name").asText("");
                        eventId = env.path("eventId").asText("");
                        JsonNode data = env.path("data");
                        if (data != null && data.isObject() && data.has("specversion")) {
                            ceId = data.path("id").asText("-");
                            ceType = data.path("type").asText("-");
                        }
                    } catch (Exception e) {
                        System.out.println("PARSE_ERR topic=" + r.topic() + " err=" + e.getMessage());
                        continue;
                    }
                    System.out.println("MSG topic=" + r.topic()
                            + " name=" + name
                            + " eventId=" + eventId
                            + " ce.id=" + ceId
                            + " ce.type=" + ceType);
                }
            }
        }
    }

    private static String env(String k, String def) {
        String v = System.getenv(k);
        return (v == null || v.isEmpty()) ? def : v;
    }
}
