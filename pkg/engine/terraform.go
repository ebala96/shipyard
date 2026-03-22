package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// TerraformRunner implements Runner using Terraform CLI.
// It generates HCL and runs terraform apply/destroy.
type TerraformRunner struct {
	workDir string // base directory for generated Terraform configs
}

// NewTerraformRunner creates a TerraformRunner.
func NewTerraformRunner() (*TerraformRunner, error) {
	workDir := os.ExpandEnv("$HOME/.shipyard/terraform")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("terraform: failed to create work dir: %w", err)
	}
	return &TerraformRunner{workDir: workDir}, nil
}

func (r *TerraformRunner) EngineName() shipfile.EngineType { return "terraform" }

// Deploy generates HCL for the service and runs terraform apply.
func (r *TerraformRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	dir := filepath.Join(r.workDir, sanitize(req.ServiceName))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("terraform: failed to create state dir: %w", err)
	}

	hcl := r.generateHCL(req)
	hclPath := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(hclPath, []byte(hcl), 0644); err != nil {
		return nil, fmt.Errorf("terraform: failed to write HCL: %w", err)
	}

	// terraform init + apply
	if err := r.tf(ctx, dir, "init", "-input=false"); err != nil {
		return nil, fmt.Errorf("terraform: init failed: %w", err)
	}
	if err := r.tf(ctx, dir, "apply", "-auto-approve", "-input=false"); err != nil {
		return nil, fmt.Errorf("terraform: apply failed: %w", err)
	}

	id := fmt.Sprintf("tf_%s_%s", sanitize(req.ServiceName), req.Mode)
	return &DeployedInstance{
		ID:          id,
		Name:        id,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      r.EngineName(),
		Ports:       req.Resolved.ResolvedPorts,
	}, nil
}

func (r *TerraformRunner) Stop(ctx context.Context, id string) error {
	return fmt.Errorf("terraform: stop not supported — use Down or Destroy")
}

func (r *TerraformRunner) Start(ctx context.Context, id string) error {
	return fmt.Errorf("terraform: start not supported — redeploy instead")
}

func (r *TerraformRunner) Restart(ctx context.Context, id string) error {
	return fmt.Errorf("terraform: restart not supported — redeploy instead")
}

func (r *TerraformRunner) Remove(ctx context.Context, id string, force bool) error {
	return r.Destroy(ctx, id)
}

func (r *TerraformRunner) Down(ctx context.Context, id string) error {
	return r.destroyResources(ctx, id)
}

func (r *TerraformRunner) Destroy(ctx context.Context, id string) error {
	return r.destroyResources(ctx, id)
}

func (r *TerraformRunner) destroyResources(ctx context.Context, id string) error {
	name := strings.TrimPrefix(id, "tf_")
	if idx := strings.LastIndex(name, "_"); idx > 0 {
		name = name[:idx]
	}
	dir := filepath.Join(r.workDir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // nothing to destroy
	}
	return r.tf(ctx, dir, "destroy", "-auto-approve", "-input=false")
}

func (r *TerraformRunner) Status(ctx context.Context, id string) (string, error) {
	return "running", nil // Terraform resources are assumed running if apply succeeded
}

func (r *TerraformRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
	return &ServiceDetail{
		Name:     id,
		Platform: "terraform",
		Instances: []InstanceInfo{{ID: id, Status: "running"}},
	}, nil
}

func (r *TerraformRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", fmt.Errorf("terraform: exec not supported")
}

func (r *TerraformRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	ch := make(chan LogLine)
	errCh := make(chan error, 1)
	errCh <- fmt.Errorf("terraform: logs not supported")
	return ch, errCh
}

func (r *TerraformRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

func (r *TerraformRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	return r.Deploy(ctx, req)
}

// generateHCL produces a minimal Terraform config for a Docker container resource.
// In production this would target AWS ECS / GCP Cloud Run / Azure Container Apps.
func (r *TerraformRunner) generateHCL(req DeployRequest) string {
	var sb strings.Builder
	sb.WriteString(`terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}

provider "docker" {}

`)
	image := "shipyard/" + sanitize(req.ServiceName) + ":" + req.Mode
	sb.WriteString(fmt.Sprintf(`resource "docker_container" "%s" {
  name  = "shipyard_%s_%s"
  image = "%s"
`, sanitize(req.ServiceName), sanitize(req.ServiceName), req.Mode, image))

	if req.Resolved != nil {
		for name, port := range req.Resolved.ResolvedPorts {
			sb.WriteString(fmt.Sprintf(`
  ports {
    internal = %d
    external = %d
    # port: %s
  }
`, port, port, name))
		}
		for k, v := range req.Resolved.ResolvedEnv {
			sb.WriteString(fmt.Sprintf("  env = [\"%s=%s\"]\n", k, v))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

func (r *TerraformRunner) tf(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "terraform", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}
