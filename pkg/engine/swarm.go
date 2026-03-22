package engine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// SwarmRunner implements Runner using Docker Swarm (docker stack / docker service).
type SwarmRunner struct{}

// NewSwarmRunner creates a SwarmRunner.
func NewSwarmRunner() (*SwarmRunner, error) {
	return &SwarmRunner{}, nil
}

func (r *SwarmRunner) EngineName() shipfile.EngineType { return "swarm" }

// Deploy creates a Docker Swarm service.
func (r *SwarmRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	image := instanceImageTag(req.StackName, req.ServiceName, req.Mode)
	svcName := instanceContainerName(req.StackName, req.ServiceName, req.Mode, 0)

	args := []string{"service", "create",
		"--name", svcName,
		"--detach",
	}

	// Port bindings.
	if req.Resolved != nil {
		for _, port := range req.Resolved.ResolvedPorts {
			args = append(args, "--publish", fmt.Sprintf("published=%d,target=%d", port, port))
		}
		for k, v := range req.Resolved.ResolvedEnv {
			args = append(args, "--env", k+"="+v)
		}
	}

	// Network.
	if req.StackName != "" {
		args = append(args, "--network", stackNetwork(req.StackName))
	}

	args = append(args, image)

	if out, err := runDockerCmd(ctx, args...); err != nil {
		return nil, fmt.Errorf("swarm: service create failed: %w\n%s", err, out)
	}

	return &DeployedInstance{
		ID:          svcName,
		Name:        svcName,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      r.EngineName(),
		Ports:       req.Resolved.ResolvedPorts,
	}, nil
}

func (r *SwarmRunner) Stop(ctx context.Context, id string) error {
	// Scale to 0 replicas — keeps the service definition.
	_, err := runDockerCmd(ctx, "service", "scale", id+"=0")
	return err
}

func (r *SwarmRunner) Start(ctx context.Context, id string) error {
	// Scale back to 1 replica.
	_, err := runDockerCmd(ctx, "service", "scale", id+"=1")
	return err
}

func (r *SwarmRunner) Restart(ctx context.Context, id string) error {
	_, err := runDockerCmd(ctx, "service", "update", "--force", id)
	return err
}

func (r *SwarmRunner) Remove(ctx context.Context, id string, force bool) error {
	_, err := runDockerCmd(ctx, "service", "rm", id)
	return err
}

func (r *SwarmRunner) Down(ctx context.Context, id string) error {
	return r.Stop(ctx, id)
}

func (r *SwarmRunner) Destroy(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

func (r *SwarmRunner) Status(ctx context.Context, id string) (string, error) {
	out, err := runDockerCmd(ctx, "service", "ls", "--filter", "name="+id, "--format", "{{.Replicas}}")
	if err != nil {
		return "unknown", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "unknown", nil
	}
	// "1/1" means running, "0/1" means stopped
	if strings.HasPrefix(out, "0/") {
		return "stopped", nil
	}
	return "running", nil
}

func (r *SwarmRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
	out, err := runDockerCmd(ctx, "service", "inspect", "--pretty", id)
	if err != nil {
		return nil, fmt.Errorf("swarm: inspect failed: %w", err)
	}
	status, _ := r.Status(ctx, id)
	return &ServiceDetail{
		Name:     id,
		Platform: "swarm",
		Instances: []InstanceInfo{{ID: id, Status: status}},
		Command:  out,
	}, nil
}

func (r *SwarmRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	// Find a running task for this service.
	taskID, err := runDockerCmd(ctx, "service", "ps", id, "--filter", "desired-state=running",
		"--format", "{{.ID}}", "--no-trunc")
	if err != nil {
		return "", fmt.Errorf("swarm: could not find running task: %w", err)
	}
	taskID = strings.TrimSpace(strings.Split(taskID, "\n")[0])
	containerID := taskID + ".1." + id

	args := append([]string{"exec", containerID}, cmd...)
	return runDockerCmd(ctx, args...)
}

func (r *SwarmRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	ch := make(chan LogLine, 100)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		args := []string{"service", "logs", "--follow", "--tail", tail, id}
		cmd := exec.CommandContext(ctx, "docker", args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errCh <- err
			return
		}
		if err := cmd.Start(); err != nil {
			errCh <- err
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				ch <- LogLine{InstanceID: id, Stream: "stdout", Text: string(buf[:n])}
			}
			if err != nil {
				break
			}
		}
	}()
	return ch, errCh
}

func (r *SwarmRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

func (r *SwarmRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_, err := runDockerCmd(ctx, "service", "rollback", id)
	if err != nil {
		// If native rollback fails, redeploy.
		_ = r.Remove(ctx, id, true)
		return r.Deploy(ctx, req)
	}
	return &DeployedInstance{
		ID:          id,
		Name:        id,
		ServiceName: req.ServiceName,
		Mode:        req.Mode,
		Engine:      r.EngineName(),
	}, nil
}

// runDockerCmd runs a docker CLI command and returns combined output.
func runDockerCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
