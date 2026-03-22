// Package infra manages the infrastructure services (etcd, NATS) that
// Shipyard depends on. It starts them automatically as Docker containers
// on startup and optionally stops them on shutdown.
package infra

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	etcdImage     = "quay.io/coreos/etcd:v3.5.17"
	etcdContainer = "shipyard-etcd"
	etcdPort      = "2379"

	natsImage     = "nats:2.10-alpine"
	natsContainer = "shipyard-nats"
	natsPort      = "4222"
	natsMonPort   = "8222"
)

// Manager starts and stops the infrastructure services.
type Manager struct {
	docker *client.Client
}

// New creates an infra Manager.
func New() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("infra: failed to connect to Docker: %w", err)
	}
	return &Manager{docker: cli}, nil
}

// EnsureRunning starts etcd and NATS if they are not already running.
// This is idempotent — safe to call on every Shipyard startup.
func (m *Manager) EnsureRunning(ctx context.Context) error {
	fmt.Println("infra: checking infrastructure services...")

	if err := m.ensureEtcd(ctx); err != nil {
		return fmt.Errorf("infra: etcd setup failed: %w", err)
	}

	if err := m.ensureNATS(ctx); err != nil {
		return fmt.Errorf("infra: NATS setup failed: %w", err)
	}

	// Wait for both to be healthy.
	if err := m.waitForEtcd(ctx); err != nil {
		return fmt.Errorf("infra: etcd did not become healthy: %w", err)
	}

	if err := m.waitForNATS(ctx); err != nil {
		return fmt.Errorf("infra: NATS did not become healthy: %w", err)
	}

	fmt.Println("infra: etcd and NATS are ready")
	return nil
}

// StopAll stops and removes the infra containers.
// Called on Shipyard shutdown if --stop-infra flag is set.
func (m *Manager) StopAll(ctx context.Context) {
	for _, name := range []string{etcdContainer, natsContainer} {
		timeout := 5
		_ = m.docker.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout})
		_ = m.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
		fmt.Printf("infra: stopped %s\n", name)
	}
}

// Close closes the Docker client.
func (m *Manager) Close() {
	m.docker.Close()
}

// ── etcd ──────────────────────────────────────────────────────────────────

func (m *Manager) ensureEtcd(ctx context.Context) error {
	// Already running?
	if m.isRunning(ctx, etcdContainer) {
		fmt.Println("infra: etcd already running")
		return nil
	}

	// Remove stale stopped container if any.
	_ = m.docker.ContainerRemove(ctx, etcdContainer, container.RemoveOptions{Force: true})

	// Pull image if needed.
	if err := m.pullIfMissing(ctx, etcdImage); err != nil {
		return err
	}

	cp, _ := nat.NewPort("tcp", etcdPort)
	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image: etcdImage,
			Cmd: []string{
				"etcd",
				"--listen-client-urls", "http://0.0.0.0:2379",
				"--advertise-client-urls", "http://0.0.0.0:2379",
				"--data-dir", "/etcd-data",
				"--log-level", "warn",
			},
			ExposedPorts: nat.PortSet{cp: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				cp: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: etcdPort}},
			},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
		nil, nil, etcdContainer,
	)
	if err != nil {
		return fmt.Errorf("infra: failed to create etcd container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("infra: failed to start etcd container: %w", err)
	}

	fmt.Println("infra: etcd started")
	return nil
}

// ── NATS ──────────────────────────────────────────────────────────────────

func (m *Manager) ensureNATS(ctx context.Context) error {
	if m.isRunning(ctx, natsContainer) {
		fmt.Println("infra: NATS already running")
		return nil
	}

	_ = m.docker.ContainerRemove(ctx, natsContainer, container.RemoveOptions{Force: true})

	if err := m.pullIfMissing(ctx, natsImage); err != nil {
		return err
	}

	cp4222, _ := nat.NewPort("tcp", natsPort)
	cp8222, _ := nat.NewPort("tcp", natsMonPort)

	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        natsImage,
			Cmd:          []string{"-js", "-m", "8222"},
			ExposedPorts: nat.PortSet{cp4222: struct{}{}, cp8222: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				cp4222: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: natsPort}},
				cp8222: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: natsMonPort}},
			},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
		nil, nil, natsContainer,
	)
	if err != nil {
		return fmt.Errorf("infra: failed to create NATS container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("infra: failed to start NATS container: %w", err)
	}

	fmt.Println("infra: NATS started")
	return nil
}

// ── Health checks ─────────────────────────────────────────────────────────

func (m *Manager) waitForEtcd(ctx context.Context) error {
	return waitForHTTP(ctx, "http://localhost:2379/health", 30*time.Second)
}

func (m *Manager) waitForNATS(ctx context.Context) error {
	return waitForHTTP(ctx, "http://localhost:8222/healthz", 30*time.Second)
}

func waitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := hc.Get(url)
		if err == nil && resp.StatusCode < 500 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", url)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (m *Manager) isRunning(ctx context.Context, name string) bool {
	f := filters.NewArgs()
	f.Add("name", "^/"+name+"$")
	containers, err := m.docker.ContainerList(ctx, container.ListOptions{
		Filters: f,
	})
	return err == nil && len(containers) > 0
}

func (m *Manager) pullIfMissing(ctx context.Context, image string) error {
	f := filters.NewArgs()
	f.Add("reference", image)
	images, err := m.docker.ImageList(ctx, dockerimage.ListOptions{Filters: f})
	if err == nil && len(images) > 0 {
		return nil // already present
	}

	fmt.Printf("infra: pulling %s...\n", image)
	reader, err := m.docker.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("infra: failed to pull %s: %w", image, err)
	}
	defer reader.Close()
	io.ReadAll(reader) // drain to completion
	fmt.Printf("infra: pulled %s\n", image)
	return nil
}
