package orchestrator

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Lifecycle manages the runtime state of existing containers.
type Lifecycle struct {
	docker *client.Client
}

// NewLifecycle creates a Lifecycle manager connected to the local Docker daemon.
func NewLifecycle() (*Lifecycle, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("lifecycle: failed to connect to Docker daemon: %w", err)
	}
	return &Lifecycle{docker: cli}, nil
}

// Start starts a stopped container by ID or name.
func (l *Lifecycle) Start(ctx context.Context, containerID string) error {
	if err := l.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("lifecycle: failed to start container %q: %w", containerID, err)
	}
	return nil
}

// Stop gracefully stops a running container.
// timeoutSeconds controls how long Docker waits before force-killing.
func (l *Lifecycle) Stop(ctx context.Context, containerID string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	stopOptions := container.StopOptions{Timeout: &timeout}

	if err := l.docker.ContainerStop(ctx, containerID, stopOptions); err != nil {
		return fmt.Errorf("lifecycle: failed to stop container %q: %w", containerID, err)
	}
	return nil
}

// Restart restarts a container.
func (l *Lifecycle) Restart(ctx context.Context, containerID string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	stopOptions := container.StopOptions{Timeout: &timeout}

	if err := l.docker.ContainerRestart(ctx, containerID, stopOptions); err != nil {
		return fmt.Errorf("lifecycle: failed to restart container %q: %w", containerID, err)
	}
	return nil
}

// Kill force-kills a container immediately without a graceful shutdown.
func (l *Lifecycle) Kill(ctx context.Context, containerID string) error {
	if err := l.docker.ContainerKill(ctx, containerID, "SIGKILL"); err != nil {
		return fmt.Errorf("lifecycle: failed to kill container %q: %w", containerID, err)
	}
	return nil
}

// Remove stops and removes a container. If force is true it kills the
// container first even if it is still running.
func (l *Lifecycle) Remove(ctx context.Context, containerID string, force bool) error {
	options := container.RemoveOptions{
		Force:         force,
		RemoveVolumes: false, // keep volumes by default — data safety
	}

	if err := l.docker.ContainerRemove(ctx, containerID, options); err != nil {
		return fmt.Errorf("lifecycle: failed to remove container %q: %w", containerID, err)
	}
	return nil
}

// Status returns the current status string of a container (e.g. "running", "exited").
func (l *Lifecycle) Status(ctx context.Context, containerID string) (string, error) {
	info, err := l.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("lifecycle: failed to inspect container %q: %w", containerID, err)
	}
	return info.State.Status, nil
}

// Exec runs a command inside a running container and returns the output.
// This is the docker exec equivalent used by the remote exec API.
func (l *Lifecycle) Exec(ctx context.Context, containerID string, cmd []string) (string, error) {
	execConfig := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}

	execID, err := l.docker.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("lifecycle: failed to create exec in container %q: %w", containerID, err)
	}

	resp, err := l.docker.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("lifecycle: failed to attach to exec in container %q: %w", containerID, err)
	}
	defer resp.Close()

	// Read all output from the exec session.
	var output []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Reader.Read(buf)
		if n > 0 {
			output = append(output, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	return string(output), nil
}
