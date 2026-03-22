package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// ComposeRunner implements Runner by shelling out to `docker compose`.
// It expects docker-compose.yml (or the file named in build.composeFile)
// to exist in the service context directory.
type ComposeRunner struct{}

func NewComposeRunner() (*ComposeRunner, error) {
	// Verify docker compose is available.
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		return nil, fmt.Errorf("compose runner: docker compose is not available: %w", err)
	}
	return &ComposeRunner{}, nil
}

func (r *ComposeRunner) EngineName() shipfile.EngineType { return shipfile.EngineCompose }

// Deploy runs `docker compose up` for the service.
// The InstanceIndex is ignored for Compose — Compose manages its own scaling.
func (r *ComposeRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	composeFile := req.Resolved.Build.ComposeFile
	if composeFile == "" {
		composeFile = "docker-compose.yml"
	}

	composePath := filepath.Join(req.ContextDir, composeFile)
	projectName := composeProjectName(req.StackName, req.ServiceName)

	// Build env overrides from resolved env.
	envArgs := buildEnvArgs(req.Resolved.ResolvedEnv)

	args := []string{
		"compose",
		"--project-name", projectName,
		"--file", composePath,
		"up",
		"--build",
		"--detach",
		"--wait", // wait for health checks
	}
	args = append(args, envArgs...)

	out, err := runCommand(ctx, req.ContextDir, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("compose runner: docker compose up failed: %w\noutput: %s", err, out)
	}

	// The "ID" for a compose deployment is the project name.
	// Individual container IDs can be listed with `docker compose ps`.
	return &DeployedInstance{
		ID:          projectName,
		Name:        projectName,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      shipfile.EngineCompose,
		Ports:       req.Resolved.ResolvedPorts,
	}, nil
}

// Stop runs `docker compose stop`.
func (r *ComposeRunner) Stop(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", "docker", "compose", "--project-name", id, "stop")
	return err
}

// Start runs `docker compose start`.
func (r *ComposeRunner) Start(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", "docker", "compose", "--project-name", id, "start")
	return err
}

// Restart runs `docker compose restart`.
func (r *ComposeRunner) Restart(ctx context.Context, id string) error {
	_, err := runCommand(ctx, "", "docker", "compose", "--project-name", id, "restart")
	return err
}

// Remove runs `docker compose down` removing containers and networks.
func (r *ComposeRunner) Remove(ctx context.Context, id string, force bool) error {
	args := []string{"compose", "--project-name", id, "down"}
	if force {
		args = append(args, "--volumes") // also remove named volumes
	}
	_, err := runCommand(ctx, "", "docker", args...)
	return err
}

// Status returns the overall status of the compose project.
func (r *ComposeRunner) Status(ctx context.Context, id string) (string, error) {
	out, err := runCommand(ctx, "", "docker",
		"compose", "--project-name", id, "ps", "--format", "json")
	if err != nil {
		return "unknown", err
	}

	if strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "[]" {
		return "stopped", nil
	}

	// If any containers appear in the output the project is considered running.
	return "running", nil
}

// Exec runs a command inside the first running container of the compose project.
func (r *ComposeRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	// Get the first container ID in the project.
	containerID, err := firstContainerID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("compose runner: exec: could not find a running container: %w", err)
	}

	args := append([]string{"exec", containerID}, cmd...)
	return runCommand(ctx, "", "docker", args...)
}

// Logs streams logs from the compose project using `docker compose logs --follow`.
func (r *ComposeRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		args := []string{"compose", "--project-name", id, "logs",
			"--follow", "--tail", tail, "--no-color"}
		cmd := exec.CommandContext(ctx, "docker", args...)
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

		// Stream stdout.
		go pipeToChannel(ctx, stdout, id, "stdout", lines)
		go pipeToChannel(ctx, stderr, id, "stderr", lines)

		cmd.Wait()
	}()

	return lines, errs
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// composeProjectName builds a stable project name for docker compose.
func composeProjectName(stackName, serviceName string) string {
	if stackName != "" {
		return sanitize(stackName + "_" + serviceName)
	}
	return sanitize("shipyard_" + serviceName)
}

// buildEnvArgs converts the resolved env map to --env KEY=VALUE args.
func buildEnvArgs(env map[string]string) []string {
	var args []string
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	return args
}

// firstContainerID returns the first running container ID in a compose project.
func firstContainerID(ctx context.Context, projectName string) (string, error) {
	out, err := runCommand(ctx, "", "docker",
		"compose", "--project-name", projectName,
		"ps", "-q")
	if err != nil {
		return "", err
	}
	ids := strings.Fields(strings.TrimSpace(out))
	if len(ids) == 0 {
		return "", fmt.Errorf("no running containers found in project %q", projectName)
	}
	return ids[0], nil
}

// runCommand executes a shell command in a working directory and returns combined output.
func runCommand(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// Down removes containers but keeps volumes (recoverable).
func (r *ComposeRunner) Down(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Destroy removes containers and volumes (irreversible).
func (r *ComposeRunner) Destroy(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Inspect returns runtime detail for this instance.
func (r *ComposeRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
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
func (r *ComposeRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

// Rollback redeploys from a previous request.
func (r *ComposeRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_ = r.Remove(ctx, id, true)
	return r.Deploy(ctx, req)
}
