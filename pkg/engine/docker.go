package engine

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// DockerRunner implements Runner using the Docker SDK.
type DockerRunner struct {
	docker *client.Client
}

// NewDockerRunner creates a DockerRunner.
// host is the Docker daemon address — empty string uses the local socket.
func NewDockerRunner(host string) (*DockerRunner, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker runner: failed to connect: %w", err)
	}
	return &DockerRunner{docker: cli}, nil
}

func (r *DockerRunner) EngineName() shipfile.EngineType { return shipfile.EngineDocker }

// Deploy builds an image and starts a container for one service instance.
func (r *DockerRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	imageTag := instanceImageTag(req.StackName, req.ServiceName, req.Mode)
	containerName := instanceContainerName(req.StackName, req.ServiceName, req.Mode, req.InstanceIndex)
	networkName := stackNetwork(req.StackName)

	// Ensure stack network exists.
	if req.StackName != "" {
		if err := r.ensureNetwork(ctx, networkName); err != nil {
			return nil, err
		}
	}

	// Build the image.
	if err := r.buildImage(ctx, req.ContextDir, req.Resolved.Build.Dockerfile, strPtrMap(req.Resolved.Build.Args), imageTag); err != nil {
		return nil, fmt.Errorf("docker runner: build failed: %w", err)
	}

	// Wire ports.
	portBindings, exposedPorts, err := buildPortBindings(req.Resolved)
	if err != nil {
		return nil, err
	}

	// Wire env.
	envSlice := envToSlice(req.Resolved.ResolvedEnv)

	// Wire mounts.
	mounts := buildMounts(req.Resolved)

	// Resource limits.
	resources := buildResources(req.Shipfile.Service.Scale.Resources)

	hostCfg := &container.HostConfig{
		PortBindings:  portBindings,
		Mounts:        mounts,
		Resources:     resources,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	containerCfg := &container.Config{
		Image:        imageTag,
		Env:          envSlice,
		ExposedPorts: exposedPorts,
	}

	var netCfg *network.NetworkingConfig
	if networkName != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		}
	}

	resp, err := r.docker.ContainerCreate(ctx, containerCfg, hostCfg, netCfg, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("docker runner: create container failed: %w", err)
	}

	if err := r.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("docker runner: start container failed: %w", err)
	}

	return &DeployedInstance{
		ID:            resp.ID,
		Name:          containerName,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Engine:        shipfile.EngineDocker,
		Ports:         req.Resolved.ResolvedPorts,
		InstanceIndex: req.InstanceIndex,
	}, nil
}

func (r *DockerRunner) Stop(ctx context.Context, id string) error {
	timeout := 10
	return r.docker.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (r *DockerRunner) Start(ctx context.Context, id string) error {
	return r.docker.ContainerStart(ctx, id, container.StartOptions{})
}

func (r *DockerRunner) Restart(ctx context.Context, id string) error {
	timeout := 10
	return r.docker.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (r *DockerRunner) Remove(ctx context.Context, id string, force bool) error {
	return r.docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

func (r *DockerRunner) Status(ctx context.Context, id string) (string, error) {
	info, err := r.docker.ContainerInspect(ctx, id)
	if err != nil {
		return "", err
	}
	return info.State.Status, nil
}

func (r *DockerRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	execID, err := r.docker.ContainerExecCreate(ctx, id, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	})
	if err != nil {
		return "", err
	}

	resp, err := r.docker.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer resp.Close()

	out, _ := io.ReadAll(resp.Reader)
	return string(out), nil
}

func (r *DockerRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		reader, err := r.docker.ContainerLogs(ctx, id, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Tail:       tail,
		})
		if err != nil {
			errs <- err
			return
		}
		defer reader.Close()
		streamDockerLogs(ctx, reader, id, lines)
	}()

	return lines, errs
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (r *DockerRunner) ensureNetwork(ctx context.Context, name string) error {
	nets, err := r.docker.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = r.docker.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	return err
}

func (r *DockerRunner) buildImage(ctx context.Context, contextDir, dockerfile string, args map[string]*string, tag string) error {
	tar, err := tarDir(contextDir)
	if err != nil {
		return err
	}
	resp, err := r.docker.ImageBuild(ctx, tar, dockerBuildOptions(tag, dockerfile, args))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) // drain build output
	return nil
}

func buildPortBindings(mode *shipfile.ResolvedMode) (nat.PortMap, nat.PortSet, error) {
	pm := nat.PortMap{}
	ps := nat.PortSet{}
	for _, p := range mode.Runtime.Ports {
		host, ok := mode.ResolvedPorts[p.Name]
		if !ok {
			return nil, nil, fmt.Errorf("port %q has no resolved value", p.Name)
		}
		cp, _ := nat.NewPort("tcp", strconv.Itoa(p.Internal))
		pm[cp] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(host)}}
		ps[cp] = struct{}{}
	}
	return pm, ps, nil
}

func buildMounts(mode *shipfile.ResolvedMode) []mount.Mount {
	var mounts []mount.Mount
	for _, v := range mode.Runtime.Volumes {
		t := mount.TypeVolume
		if v.Type == "bind" {
			t = mount.TypeBind
		}
		mounts = append(mounts, mount.Mount{Type: t, Source: v.From, Target: v.To, ReadOnly: v.ReadOnly})
	}
	return mounts
}

func buildResources(r shipfile.Resources) container.Resources {
	res := container.Resources{}
	if r.CPU > 0 {
		res.NanoCPUs = int64(r.CPU * 1e9)
	}
	if r.Memory != "" {
		if bytes, err := parseMemory(r.Memory); err == nil {
			res.Memory = bytes
		}
	}
	return res
}

func envToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// Down stops and removes containers but keeps volumes (recoverable).
func (r *DockerRunner) Down(ctx context.Context, id string) error {
	timeout := 10
	_ = r.docker.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	return r.docker.ContainerRemove(ctx, id, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: false,
	})
}

// Destroy stops and removes containers including volumes (irreversible).
func (r *DockerRunner) Destroy(ctx context.Context, id string) error {
	timeout := 5
	_ = r.docker.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	return r.docker.ContainerRemove(ctx, id, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
}

// Inspect returns detailed runtime info about a container.
func (r *DockerRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
	info, err := r.docker.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("docker: inspect failed: %w", err)
	}
	detail := &ServiceDetail{
		Name:    info.Name,
		Image:   info.Config.Image,
		Platform: "docker",
		Command: strings.Join(info.Config.Cmd, " "),
		Created: info.Created,
		Instances: []InstanceInfo{{
			ID:     info.ID[:12],
			Status: info.State.Status,
		}},
		Env: info.Config.Env,
	}
	for port, bindings := range info.HostConfig.PortBindings {
		for _, b := range bindings {
			hostPort, _ := strconv.Atoi(b.HostPort)
			cPort, _ := strconv.Atoi(port.Port())
			detail.Ports = append(detail.Ports, PortBinding{
				ContainerPort: cPort,
				HostPort:      hostPort,
				Protocol:      port.Proto(),
			})
		}
	}
	for _, m := range info.Mounts {
		detail.Mounts = append(detail.Mounts, MountInfo{
			Type:        string(m.Type),
			Source:      m.Source,
			Destination: m.Destination,
			ReadOnly:    !m.RW,
		})
	}
	return detail, nil
}

// Diff compares desired vs actual state for this container.
func (r *DockerRunner) Diff(ctx context.Context, desiredReq DeployRequest, actualID string) (*DiffResult, error) {
	info, err := r.docker.ContainerInspect(ctx, actualID)
	if err != nil {
		return &DiffResult{Action: "create"}, nil
	}
	var changes []string
	desiredImage := instanceImageTag(desiredReq.StackName, desiredReq.ServiceName, desiredReq.Mode)
	if info.Config.Image != desiredImage {
		changes = append(changes, fmt.Sprintf("image: %s → %s", info.Config.Image, desiredImage))
	}
	action := "unchanged"
	if len(changes) > 0 {
		action = "update"
	}
	return &DiffResult{Action: action, Changes: changes}, nil
}

// Rollback redeploys a service from a previous request.
func (r *DockerRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_ = r.Down(ctx, id)
	return r.Deploy(ctx, req)
}
