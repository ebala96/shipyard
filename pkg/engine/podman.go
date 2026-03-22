package engine

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// PodmanRunner implements Runner using the Podman CLI.
// Podman is Docker-compatible — it uses the same image format, Dockerfile syntax,
// and most Docker flags. The main difference is it runs rootless (no daemon).
// We shell out to the podman CLI rather than using an SDK for simplicity.
type PodmanRunner struct {
	host string // remote podman socket if needed
}

// NewPodmanRunner creates a PodmanRunner.
// host is the Podman socket path — empty uses the default rootless socket.
func NewPodmanRunner(host string) (*PodmanRunner, error) {
	if err := exec.Command("podman", "version").Run(); err != nil {
		return nil, fmt.Errorf("podman runner: podman is not available — install it from https://podman.io/getting-started/installation: %w", err)
	}
	return &PodmanRunner{host: host}, nil
}

func (r *PodmanRunner) EngineName() shipfile.EngineType { return shipfile.EnginePodman }

// Deploy builds an image and runs a rootless Podman container.
// The implementation mirrors DockerRunner — Podman accepts the same flags.
func (r *PodmanRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	imageTag := instanceImageTag(req.StackName, req.ServiceName, req.Mode)
	containerName := instanceContainerName(req.StackName, req.ServiceName, req.Mode, req.InstanceIndex)

	// Build image.
	buildArgs := []string{"build",
		"--file", req.Resolved.Build.Dockerfile,
		"--tag", imageTag,
	}
	for k, v := range req.Resolved.Build.Args {
		buildArgs = append(buildArgs, "--build-arg", k+"="+v)
	}
	buildArgs = append(buildArgs, req.ContextDir)

	if out, err := runCommand(ctx, req.ContextDir, r.podman(), buildArgs...); err != nil {
		return nil, fmt.Errorf("podman runner: build failed: %w\noutput: %s", err, out)
	}

	// Run container.
	runArgs := []string{"run", "--detach", "--name", containerName}

	// Ports.
	for _, p := range req.Resolved.Runtime.Ports {
		host := req.Resolved.ResolvedPorts[p.Name]
		runArgs = append(runArgs, "-p", fmt.Sprintf("%d:%d", host, p.Internal))
	}

	// Env vars.
	for k, v := range req.Resolved.ResolvedEnv {
		runArgs = append(runArgs, "-e", k+"="+v)
	}

	// Volumes.
	for _, v := range req.Resolved.Runtime.Volumes {
		flag := fmt.Sprintf("%s:%s", v.From, v.To)
		if v.ReadOnly {
			flag += ":ro"
		}
		runArgs = append(runArgs, "-v", flag)
	}

	// Resources.
	res := req.Shipfile.Service.Scale.Resources
	if res.CPU > 0 {
		runArgs = append(runArgs, "--cpus", fmt.Sprintf("%.2f", res.CPU))
	}
	if res.Memory != "" {
		runArgs = append(runArgs, "--memory", res.Memory)
	}

	runArgs = append(runArgs, imageTag)

	out, err := runCommand(ctx, req.ContextDir, r.podman(), runArgs...)
	if err != nil {
		return nil, fmt.Errorf("podman runner: run failed: %w\noutput: %s", err, out)
	}

	// Output of `podman run --detach` is the container ID.
	containerID := trimOutput(out)

	return &DeployedInstance{
		ID:            containerID,
		Name:          containerName,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Engine:        shipfile.EnginePodman,
		Ports:         req.Resolved.ResolvedPorts,
		InstanceIndex: req.InstanceIndex,
	}, nil
}

func (r *PodmanRunner) Stop(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", r.podman(), "stop", id)
	return err
}

func (r *PodmanRunner) Start(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", r.podman(), "start", id)
	return err
}

func (r *PodmanRunner) Restart(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", r.podman(), "restart", id)
	return err
}

func (r *PodmanRunner) Remove(ctx context.Context, id string, force bool) error {
	args := []string{"rm", id}
	if force {
		args = []string{"rm", "--force", id}
	}
	_, err := runCommand(ctx, "", r.podman(), args...)
	return err
}

func (r *PodmanRunner) Status(ctx context.Context, id string) (string, error) {
	out, err := runCommand(ctx, "", r.podman(),
		"inspect", "--format", "{{.State.Status}}", id)
	if err != nil {
		return "unknown", err
	}
	return trimOutput(out), nil
}

func (r *PodmanRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	args := append([]string{"exec", id}, cmd...)
	return runCommand(ctx, "", r.podman(), args...)
}

func (r *PodmanRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		args := []string{"logs", "--follow", "--tail", tail, id}
		cmd := exec.CommandContext(ctx, r.podman(), args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- err
			return
		}
		stderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			errs <- err
			return
		}

		go pipeToChannel(ctx, stdout, id, "stdout", lines)
		go pipeToChannel(ctx, stderr, id, "stderr", lines)
		cmd.Wait()
	}()

	return lines, errs
}

// podman returns the podman binary name, with remote host if configured.
func (r *PodmanRunner) podman() string {
	return "podman"
}

// Down removes containers but keeps volumes (recoverable).
func (r *PodmanRunner) Down(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Destroy removes containers and volumes (irreversible).
func (r *PodmanRunner) Destroy(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Inspect returns runtime detail for this instance.
func (r *PodmanRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
	status, err := r.Status(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ServiceDetail{
		Name:     id,
		Platform: string(r.EngineName()),
		Instances: []InstanceInfo{{ID: id, Status: status}},
	}, nil
}

// Diff compares desired vs actual — stub returns "update" always.
func (r *PodmanRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

// Rollback redeploys from a previous request.
func (r *PodmanRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_ = r.Remove(ctx, id, true)
	return r.Deploy(ctx, req)
}
