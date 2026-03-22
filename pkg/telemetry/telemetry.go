// Package telemetry provides the NATS JetStream event bus for Shipyard.
// All lifecycle events (deploy, stop, scale, destroy) and metrics are
// published here and consumed by the autoscaler and future subscribers.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Subject constants for all event types.
const (
	SubjectDeploy    = "shipyard.events.deploy"
	SubjectStop      = "shipyard.events.stop"
	SubjectStart     = "shipyard.events.start"
	SubjectRestart   = "shipyard.events.restart"
	SubjectScale     = "shipyard.events.scale"
	SubjectDestroy   = "shipyard.events.destroy"
	SubjectDown      = "shipyard.events.down"
	SubjectRollback  = "shipyard.events.rollback"
	SubjectMetrics   = "shipyard.metrics.containers"

	// Stream names
	streamEvents  = "SHIPYARD_EVENTS"
	streamMetrics = "SHIPYARD_METRICS"
)

// Event is a lifecycle event published to NATS.
type Event struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`       // deploy, stop, start, etc.
	ServiceName string     `json:"serviceName"`
	StackName   string     `json:"stackName,omitempty"`
	ContainerID string     `json:"containerID,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Status      string     `json:"status"`     // success, failed
	Error       string     `json:"error,omitempty"`
	Operator    string     `json:"operator"`   // user, reconciler, autoscaler
	At          time.Time  `json:"at"`
	Meta        map[string]interface{} `json:"meta,omitempty"`
}

// MetricSample is a container resource sample published to NATS.
type MetricSample struct {
	ContainerID   string    `json:"containerID"`
	ContainerName string    `json:"containerName"`
	ServiceName   string    `json:"serviceName"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemUsageMB    float64   `json:"memUsageMB"`
	MemPercent    float64   `json:"memPercent"`
	At            time.Time `json:"at"`
}

// Bus is the NATS JetStream event bus.
type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// New connects to NATS and ensures the required streams exist.
// url defaults to "nats://localhost:4222" if empty.
func New(url string) (*Bus, error) {
	if url == "" {
		url = "nats://localhost:4222"
	}

	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(10),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to connect to NATS at %q: %w", url, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("telemetry: failed to create JetStream context: %w", err)
	}

	bus := &Bus{nc: nc, js: js}
	if err := bus.ensureStreams(); err != nil {
		nc.Close()
		return nil, err
	}

	return bus, nil
}

// Close closes the NATS connection.
func (b *Bus) Close() {
	b.nc.Close()
}

// PublishEvent publishes a lifecycle event.
func (b *Bus) PublishEvent(ctx context.Context, event Event) error {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("%s_%d", event.Type, event.At.UnixNano())
	}

	subject := subjectForType(event.Type)
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("telemetry: marshal failed: %w", err)
	}

	_, err = b.js.Publish(ctx, subject, data)
	return err
}

// PublishMetric publishes a container metric sample.
func (b *Bus) PublishMetric(ctx context.Context, sample MetricSample) error {
	if sample.At.IsZero() {
		sample.At = time.Now()
	}
	data, err := json.Marshal(sample)
	if err != nil {
		return fmt.Errorf("telemetry: marshal failed: %w", err)
	}
	_, err = b.js.Publish(ctx, SubjectMetrics, data)
	return err
}

// Subscribe returns a consumer for a subject. fn is called for each message.
func (b *Bus) Subscribe(ctx context.Context, subject string, fn func(event Event)) error {
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, streamEvents, jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("telemetry: failed to create consumer for %q: %w", subject, err)
	}

	_, err = consumer.Consume(func(msg jetstream.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data(), &event); err != nil {
			msg.Nak()
			return
		}
		fn(event)
		msg.Ack()
	})
	return err
}

// ── Helpers ───────────────────────────────────────────────────────────────

// ensureStreams creates the JetStream streams if they don't exist.
func (b *Bus) ensureStreams() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Events stream — all lifecycle events, 24h retention.
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamEvents,
		Subjects:  []string{"shipyard.events.*"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("telemetry: failed to ensure events stream: %w", err)
	}

	// Metrics stream — container metrics, 4h retention.
	_, err = b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamMetrics,
		Subjects:  []string{"shipyard.metrics.*"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    4 * time.Hour,
		Storage:   jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("telemetry: failed to ensure metrics stream: %w", err)
	}

	fmt.Printf("telemetry: NATS streams ready (%s, %s)\n", streamEvents, streamMetrics)
	return nil
}

func subjectForType(eventType string) string {
	switch eventType {
	case "deploy":
		return SubjectDeploy
	case "stop":
		return SubjectStop
	case "start":
		return SubjectStart
	case "restart":
		return SubjectRestart
	case "scale":
		return SubjectScale
	case "destroy":
		return SubjectDestroy
	case "down":
		return SubjectDown
	case "rollback":
		return SubjectRollback
	default:
		return "shipyard.events." + eventType
	}
}
