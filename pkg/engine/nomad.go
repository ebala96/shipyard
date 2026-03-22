package engine

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// NomadRunner implements Runner using the Nomad CLI.
// Nomad supports Docker, raw_exec, Java, and other task drivers.
// This runner handles the Docker task driver which is Nomad's most common use case.
type NomadRunner struct {
	addr  string
	token string
}

// NewNomadRunner creates a NomadRunner connected to a Nomad cluster.
// addr is the Nomad server address e.g. "http://localhost:4646".
func NewNomadRunner(addr, token string) (*NomadRunner, error) {
	if addr == "" {
		return nil, fmt.Errorf("nomad runner: nomadAddr is required")
	}

	// Verify nomad CLI is available.
	if err := exec.Command("nomad", "version").Run(); err != nil {
		return nil, fmt.Errorf("nomad runner: nomad CLI is not available — install it from https://developer.hashicorp.com/nomad/downloads: %w", err)
	}

	return &NomadRunner{addr: addr, token: token}, nil
}

func (r *NomadRunner) EngineName() shipfile.EngineType { return shipfile.EngineNomad }

// Deploy submits a Nomad job file.
// If no nomadJob path is set in the shipfile, it looks for <serviceName>.nomad
// or job.nomad in the context directory.
func (r *NomadRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	jobFile := req.Resolved.Build.NomadJob
	if jobFile == "" {
		jobFile = r.detectJobFile(req.ContextDir, req.ServiceName)
	}

	jobPath := filepath.Join(req.ContextDir, jobFile)

	args := r.nomadArgs("job", "run", jobPath)

	// Pass env vars as -var flags.
	for k, v := range req.Resolved.ResolvedEnv {
		args = append(args, "-var", fmt.Sprintf("%s=%s", k, v))
	}

	out, err := runCommand(ctx, req.ContextDir, "nomad", args...)
	if err != nil {
		return nil, fmt.Errorf("nomad runner: job run failed: %w\noutput: %s", err, out)
	}

	// Extract job ID from output — Nomad prints "==> Monitoring evaluation ..."
	jobID := r.extractJobID(out, req.ServiceName)

	return &DeployedInstance{
		ID:          jobID,
		Name:        jobID,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      shipfile.EngineNomad,
		Ports:       req.Resolved.ResolvedPorts,
	}, nil
}

// Stop stops a Nomad job (marks it as stopped, retains state).
func (r *NomadRunner) Stop(ctx context.Context, id string) error {
	args := r.nomadArgs("job", "stop", id)
	_, err := runCommand(ctx, "", "nomad", args...)
	return err
}

// Start re-runs a stopped Nomad job.
func (r *NomadRunner) Start(ctx context.Context, id string) error {
	args := r.nomadArgs("job", "run", "-detach", id)
	_, err := runCommand(ctx, "", "nomad", args...)
	return err
}

// Restart stops then starts a Nomad job.
func (r *NomadRunner) Restart(ctx context.Context, id string) error {
	if err := r.Stop(ctx, id); err != nil {
		return err
	}
	return r.Start(ctx, id)
}

// Remove permanently stops and purges a Nomad job.
func (r *NomadRunner) Remove(ctx context.Context, id string, force bool) error {
	args := r.nomadArgs("job", "stop", "-purge", id)
	_, err := runCommand(ctx, "", "nomad", args...)
	return err
}

// Status returns the status of a Nomad job.
func (r *NomadRunner) Status(ctx context.Context, id string) (string, error) {
	args := r.nomadArgs("job", "status", "-short", id)
	out, err := runCommand(ctx, "", "nomad", args...)
	if err != nil {
		return "unknown", err
	}

	// Parse "Status = running" from output.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Status") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "unknown", nil
}

// Exec runs a command in a Nomad allocation.
func (r *NomadRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	// Get the first allocation ID for the job.
	allocID, err := r.firstAllocID(ctx, id)
	if err != nil {
		return "", err
	}

	args := r.nomadArgs("alloc", "exec", allocID)
	args = append(args, cmd...)

	return runCommand(ctx, "", "nomad", args...)
}

// Logs streams logs from the first allocation of a Nomad job.
func (r *NomadRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		allocID, err := r.firstAllocID(ctx, id)
		if err != nil {
			errs <- err
			return
		}

		args := r.nomadArgs("alloc", "logs", "-f", "-tail", tail, allocID)
		cmd := exec.CommandContext(ctx, "nomad", args...)
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

// ── Helpers ───────────────────────────────────────────────────────────────────

func (r *NomadRunner) nomadArgs(args ...string) []string {
	base := []string{"-address=" + r.addr}
	if r.token != "" {
		base = append(base, "-token="+r.token)
	}
	return append(base, args...)
}

func (r *NomadRunner) detectJobFile(contextDir, serviceName string) string {
	candidates := []string{
		serviceName + ".nomad",
		serviceName + ".nomad.hcl",
		"job.nomad",
		"job.nomad.hcl",
	}
	for _, name := range candidates {
		if fileExists(filepath.Join(contextDir, name)) {
			return name
		}
	}
	return serviceName + ".nomad" // fallback
}

func (r *NomadRunner) extractJobID(output, fallback string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "job/") {
			parts := strings.Fields(line)
			for _, p := range parts {
				if strings.HasPrefix(p, "job/") {
					return strings.TrimPrefix(p, "job/")
				}
			}
		}
	}
	return sanitize(fallback)
}

func (r *NomadRunner) firstAllocID(ctx context.Context, jobID string) (string, error) {
	args := r.nomadArgs("job", "allocs", "-t", "{{range .}}{{.ID}}\n{{end}}", jobID)
	out, err := runCommand(ctx, "", "nomad", args...)
	if err != nil {
		return "", fmt.Errorf("nomad runner: could not list allocations for job %q: %w", jobID, err)
	}
	ids := strings.Fields(strings.TrimSpace(out))
	if len(ids) == 0 {
		return "", fmt.Errorf("nomad runner: no allocations found for job %q", jobID)
	}
	return ids[0], nil
}

// Down removes containers but keeps volumes (recoverable).
func (r *NomadRunner) Down(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Destroy removes containers and volumes (irreversible).
func (r *NomadRunner) Destroy(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Inspect returns runtime detail for this instance.
func (r *NomadRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
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
func (r *NomadRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

// Rollback redeploys from a previous request.
func (r *NomadRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_ = r.Remove(ctx, id, true)
	return r.Deploy(ctx, req)
}
