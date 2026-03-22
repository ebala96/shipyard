package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// LogLine represents a single line of log output from a container.
type LogLine struct {
	ContainerID string
	Stream      string // "stdout" or "stderr"
	Text        string
}

// LogStreamer streams logs from Docker containers as Go channels.
type LogStreamer struct {
	docker *client.Client
}

// NewLogStreamer creates a LogStreamer connected to the local Docker daemon.
func NewLogStreamer() (*LogStreamer, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("logstreamer: failed to connect to Docker daemon: %w", err)
	}
	return &LogStreamer{docker: cli}, nil
}

// Stream returns a channel that emits log lines from a container in real time.
// The channel is closed when the context is cancelled or the container stops.
// tail controls how many historical lines to include ("all" or a number string).
func (ls *LogStreamer) Stream(ctx context.Context, containerID string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true, // stream live — don't stop after existing logs
			Tail:       tail,
			Timestamps: false,
		}

		reader, err := ls.docker.ContainerLogs(ctx, containerID, options)
		if err != nil {
			errs <- fmt.Errorf("logstreamer: failed to open logs for %q: %w", containerID, err)
			return
		}
		defer reader.Close()

		if err := readDockerLogs(ctx, reader, containerID, lines); err != nil {
			if ctx.Err() == nil {
				// Only report the error if it wasn't a context cancellation.
				errs <- err
			}
		}
	}()

	return lines, errs
}

// Fetch returns recent log lines from a container without streaming.
// Useful for showing the last N lines on the dashboard on first load.
func (ls *LogStreamer) Fetch(ctx context.Context, containerID string, tail string) ([]LogLine, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Tail:       tail,
		Timestamps: false,
	}

	reader, err := ls.docker.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return nil, fmt.Errorf("logstreamer: failed to fetch logs for %q: %w", containerID, err)
	}
	defer reader.Close()

	lines := make(chan LogLine, 500)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)
		if err := readDockerLogs(ctx, reader, containerID, lines); err != nil {
			errs <- err
		}
	}()

	var result []LogLine
	for line := range lines {
		result = append(result, line)
	}

	if err := <-errs; err != nil {
		return nil, err
	}

	return result, nil
}

// readDockerLogs reads from a Docker log stream and sends lines to the channel.
// Docker multiplexes stdout and stderr into a single stream with an 8-byte header.
// Byte 0 is the stream type: 1 = stdout, 2 = stderr.
// Bytes 4-7 are the payload size as a big-endian uint32.
func readDockerLogs(ctx context.Context, reader io.Reader, containerID string, lines chan<- LogLine) error {
	header := make([]byte, 8)
	bufReader := bufio.NewReader(reader)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Read the 8-byte multiplexing header.
		_, err := io.ReadFull(bufReader, header)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("logstreamer: error reading log header: %w", err)
		}

		streamType := header[0]
		streamName := "stdout"
		if streamType == 2 {
			streamName = "stderr"
		}

		// Read payload size from bytes 4-7.
		payloadSize := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])

		// Read the actual log line payload.
		payload := make([]byte, payloadSize)
		_, err = io.ReadFull(bufReader, payload)
		if err != nil {
			return fmt.Errorf("logstreamer: error reading log payload: %w", err)
		}

		lines <- LogLine{
			ContainerID: containerID,
			Stream:      streamName,
			Text:        string(payload),
		}
	}
}
