package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// topicForEvent returns the Kafka topic name for an event.
func topicForEvent(cfg Config, name string) string {
	if cfg.TopicNameFunc != nil {
		return cfg.TopicNameFunc(name)
	}
	return cfg.AppName + "." + name
}

// topicForNotification returns the Kafka topic name for a notification.
func topicForNotification(cfg Config, name string) string {
	if cfg.NotificationTopicNameFunc != nil {
		return cfg.NotificationTopicNameFunc(name)
	}
	return cfg.AppName + "." + name
}

// topicForCommand returns the Kafka topic that carries commands addressed to
// the given application (own app for the listener, target service for the
// gateway).
func topicForCommand(cfg Config, app string) string {
	if cfg.CommandsTopicNameFunc != nil {
		return cfg.CommandsTopicNameFunc(app)
	}
	return app + ".commands"
}

// topicForQuery returns the Kafka topic that carries async queries addressed
// to the given application.
func topicForQuery(cfg Config, app string) string {
	if cfg.QueriesTopicNameFunc != nil {
		return cfg.QueriesTopicNameFunc(app)
	}
	return app + ".queries"
}

// topicForReply returns the Kafka topic where this app's query replies are
// published.
func topicForReply(cfg Config) string {
	if cfg.RepliesTopicNameFunc != nil {
		return cfg.RepliesTopicNameFunc(cfg.AppName)
	}
	return cfg.AppName + ".replies"
}

// groupIDForEvent returns the shared consumer group ID used for competing
// consumption of an event topic.
func groupIDForEvent(cfg Config, name string) string {
	return groupPrefix(cfg) + "." + name
}

// groupIDForNotification returns a per-instance consumer group ID used for
// fan-out consumption of a notification topic.
func groupIDForNotification(cfg Config, name, instanceID string) string {
	return groupPrefix(cfg) + "." + name + "." + instanceID
}

// groupIDForCommand returns the shared consumer group ID used for competing
// consumption of the app's commands topic.
func groupIDForCommand(cfg Config) string {
	return groupPrefix(cfg) + ".commands"
}

// groupIDForQuery returns the shared consumer group ID used for competing
// consumption of the app's queries topic.
func groupIDForQuery(cfg Config) string {
	return groupPrefix(cfg) + ".queries"
}

// groupIDForReply returns a per-instance consumer group ID used for fan-out
// consumption of the app's reply topic.
func groupIDForReply(cfg Config) string {
	return groupPrefix(cfg) + ".replies." + cfg.InstanceID
}

func groupPrefix(cfg Config) string {
	if cfg.ConsumerGroupPrefix != "" {
		return cfg.ConsumerGroupPrefix
	}
	return cfg.AppName
}

// partitionCount returns the number of partitions of topic by asking a broker.
// Used to size the reply-listener priming wait. Returns ErrBrokerUnreachable
// when no broker can be dialed.
func partitionCount(ctx context.Context, cfg Config, topic string) (int, error) {
	d := dialer(cfg)
	var lastErr error
	for _, addr := range cfg.BootstrapBrokers {
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		partitions, err := conn.ReadPartitions(topic)
		_ = conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return len(partitions), nil
	}
	if lastErr == nil {
		lastErr = errors.New("no brokers configured")
	}
	return 0, fmt.Errorf("%w: %w", ErrBrokerUnreachable, lastErr)
}

// replyTopicPartitions returns the partition count of the reply topic,
// retrying briefly to ride out metadata propagation right after the topic was
// created. Falls back to 1 with an error when the lookup keeps failing.
func replyTopicPartitions(ctx context.Context, cfg Config) (int, error) {
	topic := topicForReply(cfg)
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 1, lastErr
			case <-time.After(200 * time.Millisecond):
			}
		}
		n, err := partitionCount(ctx, cfg, topic)
		if err == nil && n > 0 {
			return n, nil
		}
		lastErr = err
	}
	return 1, lastErr
}

// verifyTopics checks that every entry in topics exists on the cluster; when a
// topic is absent it either creates it (if cfg.AllowAutoCreateTopics is true)
// or returns kafka.ErrTopicMissing. Returns kafka.ErrBrokerUnreachable when
// no broker can be dialed.
func verifyTopics(ctx context.Context, cfg Config, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	d := dialer(cfg)
	var lastErr error
	for _, addr := range cfg.BootstrapBrokers {
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		defer conn.Close()

		partitions, err := conn.ReadPartitions()
		if err != nil {
			lastErr = err
			continue
		}

		toCreate, err := topicsToCreate(partitions, cfg, topics)
		if err != nil {
			return err
		}
		if len(toCreate) == 0 {
			return nil
		}
		if err := createTopicsViaController(ctx, d, conn, cfg, toCreate); err != nil {
			return err
		}
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("no brokers configured")
	}
	return fmt.Errorf("%w: %w", ErrBrokerUnreachable, lastErr)
}

// topicsToCreate compares the required topics against the existing partition
// set and returns TopicConfig entries for any that are missing. Returns
// ErrTopicMissing if a topic is absent and auto-creation is disabled.
func topicsToCreate(partitions []kgo.Partition, cfg Config, topics []string) ([]kgo.TopicConfig, error) {
	existing := make(map[string]struct{}, len(partitions))
	for _, p := range partitions {
		existing[p.Topic] = struct{}{}
	}
	var toCreate []kgo.TopicConfig
	for _, t := range topics {
		if _, ok := existing[t]; ok {
			continue
		}
		if !cfg.AllowAutoCreateTopics {
			return nil, fmt.Errorf("%w: %s", ErrTopicMissing, t)
		}
		toCreate = append(toCreate, kgo.TopicConfig{
			Topic:             t,
			NumPartitions:     cfg.DefaultPartitions,
			ReplicationFactor: cfg.DefaultReplicationFactor,
		})
	}
	return toCreate, nil
}

// createTopicsViaController dials the cluster controller and creates toCreate.
func createTopicsViaController(ctx context.Context, d *kgo.Dialer, conn *kgo.Conn, cfg Config, toCreate []kgo.TopicConfig) error {
	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("kafka: read controller: %w", err)
	}
	controllerAddr := net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port))
	ctlConn, err := d.DialContext(ctx, "tcp", controllerAddr)
	if err != nil {
		return fmt.Errorf("%w: controller %s: %w", ErrBrokerUnreachable, controllerAddr, err)
	}
	defer ctlConn.Close()
	if err := ctlConn.CreateTopics(toCreate...); err != nil {
		return fmt.Errorf("kafka: create topics: %w", err)
	}
	return nil
}
