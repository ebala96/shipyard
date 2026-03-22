package orchestrator

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

const (
	// codeServerImage is the official code-server Docker image.
	codeServerImage = "codercom/code-server:latest"

	// codeServerInternalPort is the port code-server listens on inside the container.
	codeServerInternalPort = 8080
)

// IDEInstance holds the runtime details of a code-server sidecar.
type IDEInstance struct {
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	HostPort      int    `json:"hostPort"`
	// URL is the full address to open in the browser.
	URL string `json:"url"`
}

// IDERunner manages code-server sidecar containers.
type IDERunner struct {
	orchestrator *Orchestrator
}

// newIDERunner creates an IDERunner backed by the given orchestrator.
func newIDERunner(o *Orchestrator) *IDERunner {
	return &IDERunner{orchestrator: o}
}

// Launch pulls code-server (if needed) and starts a sidecar container
// with the service source directory mounted into /home/coder/project.
// The sidecar shares the same Docker network as the main service container.
func (r *IDERunner) Launch(ctx context.Context, serviceName, mode, networkName, sourceDir string) (*IDEInstance, error) {
	// Pull code-server image if not already present.
	if err := r.ensureImage(ctx); err != nil {
		return nil, fmt.Errorf("ide: failed to pull code-server image: %w", err)
	}

	// Get a free port for the IDE.
	idePort, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("ide: could not assign a free port: %w", err)
	}

	containerName := ideContainerName(serviceName, mode)

	// Remove any existing sidecar with this name first.
	r.removeSidecar(ctx, containerName)

	cp, _ := nat.NewPort("tcp", strconv.Itoa(codeServerInternalPort))

	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			cp: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: strconv.Itoa(idePort)},
			},
		},
		Mounts: []mount.Mount{
			{
				// Mount the service source into code-server's workspace.
				Type:   mount.TypeBind,
				Source: sourceDir,
				Target: "/home/coder/project",
			},
		},
	}

	containerCfg := &container.Config{
		Image: codeServerImage,
		ExposedPorts: nat.PortSet{
			cp: struct{}{},
		},
		Env: []string{
			// Disable password auth for local development.
			"PASSWORD=",
			"HASHED_PASSWORD=",
		},
		// Open the project directory on startup.
		Cmd: []string{"--auth", "none", "--bind-addr", fmt.Sprintf("0.0.0.0:%d", codeServerInternalPort), "/home/coder/project"},
	}

	var netCfg *network.NetworkingConfig
	if networkName != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		}
	}

	resp, err := r.orchestrator.docker.ContainerCreate(
		ctx, containerCfg, hostCfg, netCfg, nil, containerName,
	)
	if err != nil {
		return nil, fmt.Errorf("ide: failed to create code-server container: %w", err)
	}

	if err := r.orchestrator.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("ide: failed to start code-server container: %w", err)
	}

	return &IDEInstance{
		ContainerID:   resp.ID,
		ContainerName: containerName,
		HostPort:      idePort,
		URL:           fmt.Sprintf("http://localhost:%d", idePort),
	}, nil
}

// Stop stops and removes the code-server sidecar for a service.
func (r *IDERunner) Stop(ctx context.Context, serviceName, mode string) error {
	name := ideContainerName(serviceName, mode)
	return r.removeSidecar(ctx, name)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// ensureImage pulls the code-server image if it is not already present locally.
func (r *IDERunner) ensureImage(ctx context.Context) error {
	// Check if image already exists locally.
	filterArgs := filters.NewArgs()
	filterArgs.Add("reference", codeServerImage)

	images, err := r.orchestrator.docker.ImageList(ctx, dockerImageListOptions(filterArgs))
	if err != nil {
		return err
	}
	if len(images) > 0 {
		return nil // already pulled
	}

	// Pull the image.
	reader, err := r.orchestrator.docker.ImagePull(ctx, codeServerImage, dockerImagePullOptions())
	if err != nil {
		return err
	}
	defer reader.Close()
	io.ReadAll(reader) // drain to completion
	return nil
}

// removeSidecar stops and removes a code-server container by name.
func (r *IDERunner) removeSidecar(ctx context.Context, containerName string) error {
	_ = r.orchestrator.docker.ContainerStop(ctx, containerName, container.StopOptions{})
	_ = r.orchestrator.docker.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	return nil
}

// ideContainerName returns the Docker container name for a code-server sidecar.
func ideContainerName(serviceName, mode string) string {
	return fmt.Sprintf("shipyard_%s_%s_ide", sanitize(serviceName), mode)
}
