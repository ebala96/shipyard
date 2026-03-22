package idemanager

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const codeServerImage = "codercom/code-server:latest"

// IDEInstance holds the state of a running code-server container.
type IDEInstance struct {
	ServiceName   string `json:"serviceName"`
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	HostPort      int    `json:"hostPort"`
	// ProxyURL is the URL through Shipyard's reverse proxy.
	ProxyURL string `json:"proxyURL"`
	// DirectURL is the direct localhost URL.
	DirectURL string `json:"directURL"`
}

// Manager tracks running IDE containers keyed by service name.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*IDEInstance // serviceName → instance
	docker    *client.Client
}

// New creates a Manager connected to the local Docker daemon.
func New() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("idemanager: failed to connect to Docker: %w", err)
	}
	return &Manager{
		instances: make(map[string]*IDEInstance),
		docker:    cli,
	}, nil
}

// Spawn starts a code-server container for the given service if one isn't
// already running. Returns the existing instance if already running.
// sourceDir is the absolute path to the service source to mount.
// proxyURL is the URL through the Shipyard proxy (set by the API layer).
func (m *Manager) Spawn(ctx context.Context, serviceName, sourceDir, proxyURL string) (*IDEInstance, error) {
	// Return existing instance if healthy.
	m.mu.RLock()
	existing, ok := m.instances[serviceName]
	m.mu.RUnlock()

	if ok {
		// Verify it's still running.
		if m.isRunning(ctx, existing.ContainerID) {
			return existing, nil
		}
		// Container died — clean up and respawn.
		m.mu.Lock()
		delete(m.instances, serviceName)
		m.mu.Unlock()
	}

	// Ensure code-server image is present.
	if err := m.ensureImage(ctx); err != nil {
		return nil, fmt.Errorf("idemanager: could not pull code-server image: %w", err)
	}

	// Get a free port.
	port, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("idemanager: no free port available: %w", err)
	}

	containerName := ideContainerName(serviceName)

	// Remove any stale container with this name.
	m.removeStale(ctx, containerName)

	cp, _ := nat.NewPort("tcp", "8080")

	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			cp: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(port)}},
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: sourceDir,
				Target: "/home/coder/project",
			},
		},
	}

	containerCfg := &container.Config{
		Image:        codeServerImage,
		ExposedPorts: nat.PortSet{cp: struct{}{}},
		Env: []string{
			"PASSWORD=",
			"HASHED_PASSWORD=",
		},
		Cmd: []string{
			"--auth", "none",
			"--bind-addr", "0.0.0.0:8080",
			"/home/coder/project",
		},
	}

	resp, err := m.docker.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("idemanager: failed to create IDE container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("idemanager: failed to start IDE container: %w", err)
	}

	// Wait for code-server to be ready before returning the URL.
	// This prevents the "connection reset" error on first load.
	if err := waitForIDE(ctx, port); err != nil {
		// Non-fatal — return the URL anyway, user can refresh.
		fmt.Printf("idemanager: IDE health check timed out for %q (it may still be starting): %v\n", serviceName, err)
	}

	instance := &IDEInstance{
		ServiceName:   serviceName,
		ContainerID:   resp.ID,
		ContainerName: containerName,
		HostPort:      port,
		ProxyURL:      proxyURL,
		DirectURL:     fmt.Sprintf("http://localhost:%d", port),
	}

	m.mu.Lock()
	m.instances[serviceName] = instance
	m.mu.Unlock()

	return instance, nil
}

// Stop stops and removes the IDE container for a service.
func (m *Manager) Stop(ctx context.Context, serviceName string) error {
	m.mu.Lock()
	instance, ok := m.instances[serviceName]
	if ok {
		delete(m.instances, serviceName)
	}
	m.mu.Unlock()

	if !ok {
		return nil // nothing to stop
	}

	timeout := 5
	_ = m.docker.ContainerStop(ctx, instance.ContainerID, container.StopOptions{Timeout: &timeout})
	_ = m.docker.ContainerRemove(ctx, instance.ContainerID, container.RemoveOptions{Force: true})
	return nil
}

// Get returns the IDE instance for a service if running.
func (m *Manager) Get(serviceName string) (*IDEInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[serviceName]
	return inst, ok
}

// List returns all running IDE instances.
func (m *Manager) List() []*IDEInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*IDEInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		result = append(result, inst)
	}
	return result
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func ideContainerName(serviceName string) string {
	name := strings.ToLower(serviceName)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return "shipyard_ide_" + name
}

func (m *Manager) isRunning(ctx context.Context, containerID string) bool {
	info, err := m.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}
	return info.State.Running
}

func (m *Manager) removeStale(ctx context.Context, containerName string) {
	_ = m.docker.ContainerStop(ctx, containerName, container.StopOptions{})
	_ = m.docker.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
}

func (m *Manager) ensureImage(ctx context.Context) error {
	f := filters.NewArgs()
	f.Add("reference", codeServerImage)
	images, err := m.docker.ImageList(ctx, imagetypes.ListOptions{Filters: f})
	if err != nil {
		return err
	}
	if len(images) > 0 {
		return nil
	}
	reader, err := m.docker.ImagePull(ctx, codeServerImage, imagetypes.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	io.ReadAll(reader)
	return nil
}

// waitForIDE polls code-server's health endpoint until it responds
// or the context times out. Code-server typically starts in 2-5 seconds.
func waitForIDE(ctx context.Context, port int) error {
	url := fmt.Sprintf("http://localhost:%d/healthz", port)
	deadline := time.Now().Add(30 * time.Second)
	client := &http.Client{Timeout: 1 * time.Second}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil // code-server is up
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("IDE did not become ready within 30 seconds on port %d", port)
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}