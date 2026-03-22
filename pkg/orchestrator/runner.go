package orchestrator

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// RunResult holds the outcome of starting a container.
type RunResult struct {
	ContainerID   string
	ContainerName string
	Ports         map[string]int // port name -> host port
}

// Runner creates and starts Docker containers.
type Runner struct {
	docker *client.Client
}

// NewRunner creates a Runner connected to the local Docker daemon.
func NewRunner() (*Runner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("runner: failed to connect to Docker daemon: %w", err)
	}
	return &Runner{docker: cli}, nil
}

// Run creates and starts a container from a built image and a ResolvedMode.
// containerName is the name to assign to the container.
// networkName is the Docker network to attach the container to (stack network).
func (r *Runner) Run(ctx context.Context, imageTag string, containerName string, networkName string, mode *shipfile.ResolvedMode) (*RunResult, error) {

	// Build port bindings from resolved ports.
	portBindings, exposedPorts, err := buildPortConfig(mode)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	// Build env slice in KEY=VALUE format.
	envSlice := buildEnvSlice(mode.ResolvedEnv)

	// Build volume mounts.
	mounts := buildMounts(mode)

	// Resource limits.
	resources := container.Resources{}
	if mode.Runtime.Resources.CPU > 0 {
		// Docker expects CPU in nano CPUs (1 CPU = 1e9 nano CPUs).
		resources.NanoCPUs = int64(mode.Runtime.Resources.CPU * 1e9)
	}
	if mode.Runtime.Resources.Memory != "" {
		bytes, err := parseMemory(mode.Runtime.Resources.Memory)
		if err != nil {
			return nil, fmt.Errorf("runner: invalid memory value %q: %w", mode.Runtime.Resources.Memory, err)
		}
		resources.Memory = bytes
	}

	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		Mounts:       mounts,
		Resources:    resources,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	containerConfig := &container.Config{
		Image:        imageTag,
		Env:          envSlice,
		ExposedPorts: exposedPorts,
	}

	// Attach to the stack network if provided.
	var networkingConfig *network.NetworkingConfig
	if networkName != "" {
		networkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		}
	}

	// Remove any existing container with this name so redeploy and scale work cleanly.
	timeout := 5
	_ = r.docker.ContainerStop(ctx, containerName, container.StopOptions{Timeout: &timeout})
	_ = r.docker.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	// Also try removing by container ID if the name lookup above missed it.
	f := dockerfilters.NewArgs()
	f.Add("name", containerName)
	existing, _ := r.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	for _, c := range existing {
		_ = r.docker.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		_ = r.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}

	resp, err := r.docker.ContainerCreate(ctx, containerConfig, hostConfig, networkingConfig, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("runner: failed to create container %q: %w", containerName, err)
	}

	if err := r.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("runner: failed to start container %q: %w", containerName, err)
	}

	return &RunResult{
		ContainerID:   resp.ID,
		ContainerName: containerName,
		Ports:         mode.ResolvedPorts,
	}, nil
}

// buildPortConfig converts ResolvedMode ports into Docker port binding structures.
func buildPortConfig(mode *shipfile.ResolvedMode) (nat.PortMap, nat.PortSet, error) {
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}

	for _, port := range mode.Runtime.Ports {
		hostPort, ok := mode.ResolvedPorts[port.Name]
		if !ok {
			return nil, nil, fmt.Errorf("port %q has no resolved host port", port.Name)
		}

		containerPort, err := nat.NewPort("tcp", strconv.Itoa(port.Internal))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid container port %d: %w", port.Internal, err)
		}

		portBindings[containerPort] = []nat.PortBinding{
			{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)},
		}
		exposedPorts[containerPort] = struct{}{}
	}

	return portBindings, exposedPorts, nil
}

// buildEnvSlice converts a map of env vars to a KEY=VALUE slice.
func buildEnvSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}

// buildMounts converts shipfile Volume definitions to Docker mount specs.
func buildMounts(mode *shipfile.ResolvedMode) []mount.Mount {
	mounts := make([]mount.Mount, 0, len(mode.Runtime.Volumes))

	for _, vol := range mode.Runtime.Volumes {
		mountType := mount.TypeVolume
		if vol.Type == "bind" {
			mountType = mount.TypeBind
		}

		mounts = append(mounts, mount.Mount{
			Type:     mountType,
			Source:   vol.From,
			Target:   vol.To,
			ReadOnly: vol.ReadOnly,
		})
	}

	return mounts
}

// parseMemory converts a memory string like "512m" or "1g" to bytes.
func parseMemory(mem string) (int64, error) {
	if len(mem) < 2 {
		return 0, fmt.Errorf("invalid memory format %q", mem)
	}

	unit := mem[len(mem)-1]
	value, err := strconv.ParseFloat(mem[:len(mem)-1], 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse memory value %q: %w", mem, err)
	}

	switch unit {
	case 'm', 'M':
		return int64(value * 1024 * 1024), nil
	case 'g', 'G':
		return int64(value * 1024 * 1024 * 1024), nil
	case 'k', 'K':
		return int64(value * 1024), nil
	default:
		return 0, fmt.Errorf("unknown memory unit %q in %q (use m, g, or k)", unit, mem)
	}
}