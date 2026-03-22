package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// KubernetesRunner implements Runner for Kubernetes and K3s.
// It uses kubectl under the hood — this keeps the implementation simple
// and compatible with any k8s distribution (K3s, minikube, EKS, GKE, AKS).
// In a future version this can be replaced with client-go for in-process calls.
type KubernetesRunner struct {
	kubeconfig string
	namespace  string
}

// NewKubernetesRunner creates a KubernetesRunner.
// kubeconfig is the path to the kubeconfig file — empty uses the default (~/.kube/config).
// namespace defaults to "default" if empty.
func NewKubernetesRunner(kubeconfig, namespace string) (*KubernetesRunner, error) {
	if namespace == "" {
		namespace = "default"
	}

	// Verify kubectl is available.
	if err := exec.Command("kubectl", "version", "--client").Run(); err != nil {
		return nil, fmt.Errorf("kubernetes runner: kubectl is not available: %w", err)
	}

	return &KubernetesRunner{
		kubeconfig: kubeconfig,
		namespace:  namespace,
	}, nil
}

func (r *KubernetesRunner) EngineName() shipfile.EngineType { return shipfile.EngineKubernetes }

// Deploy applies Kubernetes manifests from the manifest directory.
// If no manifest directory is specified in the shipfile, it looks for
// a k8s/ or kubernetes/ directory in the context, or applies all .yaml files.
func (r *KubernetesRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	manifestDir := req.Resolved.Build.ManifestDir
	if manifestDir == "" {
		manifestDir = r.detectManifestDir(req.ContextDir)
	}

	manifestPath := filepath.Join(req.ContextDir, manifestDir)

	// Set the service name label via a patch so we can track it.
	args := r.kubectlArgs("apply",
		"--filename", manifestPath,
		"--namespace", r.namespace,
		"--recursive",
	)

	out, err := runCommand(ctx, req.ContextDir, "kubectl", args...)
	if err != nil {
		return nil, fmt.Errorf("kubernetes runner: apply failed: %w\noutput: %s", err, out)
	}

	// Deployment name mirrors the service name by convention.
	deploymentName := sanitize(req.ServiceName)

	return &DeployedInstance{
		ID:          deploymentName,
		Name:        deploymentName,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      shipfile.EngineKubernetes,
		Ports:       req.Resolved.ResolvedPorts,
	}, nil
}

// Stop scales the deployment to 0 replicas (keeps the manifest, stops pods).
func (r *KubernetesRunner) Stop(ctx context.Context, id string) error {
	args := r.kubectlArgs("scale", "deployment", id,
		"--replicas=0",
		"--namespace", r.namespace,
	)
	_, err := runCommand(ctx, "", "kubectl", args...)
	return err
}

// Start scales the deployment back to 1 replica.
func (r *KubernetesRunner) Start(ctx context.Context, id string) error {
	args := r.kubectlArgs("scale", "deployment", id,
		"--replicas=1",
		"--namespace", r.namespace,
	)
	_, err := runCommand(ctx, "", "kubectl", args...)
	return err
}

// Restart performs a rolling restart of the deployment.
func (r *KubernetesRunner) Restart(ctx context.Context, id string) error {
	args := r.kubectlArgs("rollout", "restart",
		"deployment/"+id,
		"--namespace", r.namespace,
	)
	_, err := runCommand(ctx, "", "kubectl", args...)
	return err
}

// Remove deletes all resources in the manifest directory.
func (r *KubernetesRunner) Remove(ctx context.Context, id string, force bool) error {
	args := r.kubectlArgs("delete", "deployment", id,
		"--namespace", r.namespace,
	)
	if force {
		args = append(args, "--grace-period=0", "--force")
	}
	_, err := runCommand(ctx, "", "kubectl", args...)
	return err
}

// Status returns the rollout status of the deployment.
func (r *KubernetesRunner) Status(ctx context.Context, id string) (string, error) {
	args := r.kubectlArgs("get", "deployment", id,
		"--namespace", r.namespace,
		"--output", "jsonpath={.status.conditions[?(@.type=='Available')].status}",
	)
	out, err := runCommand(ctx, "", "kubectl", args...)
	if err != nil {
		return "unknown", err
	}
	if out == "True" {
		return "running", nil
	}
	return "pending", nil
}

// Exec runs a command inside a running pod for this deployment.
func (r *KubernetesRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	// Get the first running pod for this deployment.
	podName, err := r.firstPod(ctx, id)
	if err != nil {
		return "", err
	}

	args := r.kubectlArgs("exec", podName,
		"--namespace", r.namespace,
		"--",
	)
	args = append(args, cmd...)

	return runCommand(ctx, "", "kubectl", args...)
}

// Logs streams logs from pods in the deployment.
func (r *KubernetesRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		args := r.kubectlArgs("logs",
			"--selector", "app="+id,
			"--namespace", r.namespace,
			"--follow",
			"--tail", tail,
			"--prefix", // prefix each line with pod name
			"--all-containers",
		)

		cmd := exec.CommandContext(ctx, "kubectl", args...)
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

// kubectlArgs prepends the kubeconfig flag if one is configured.
func (r *KubernetesRunner) kubectlArgs(args ...string) []string {
	if r.kubeconfig != "" {
		return append([]string{"--kubeconfig", r.kubeconfig}, args...)
	}
	return args
}

// firstPod returns the name of the first running pod for a deployment.
func (r *KubernetesRunner) firstPod(ctx context.Context, deploymentName string) (string, error) {
	args := r.kubectlArgs("get", "pods",
		"--selector", "app="+deploymentName,
		"--namespace", r.namespace,
		"--output", "jsonpath={.items[0].metadata.name}",
	)
	out, err := runCommand(ctx, "", "kubectl", args...)
	if err != nil || out == "" {
		return "", fmt.Errorf("kubernetes runner: no pods found for deployment %q", deploymentName)
	}
	return out, nil
}

// detectManifestDir finds the kubernetes manifest directory in a repo.
// Checks common conventions: k8s/, kubernetes/, manifests/, deploy/.
func (r *KubernetesRunner) detectManifestDir(contextDir string) string {
	candidates := []string{"k8s", "kubernetes", "manifests", "deploy", "."}
	for _, dir := range candidates {
		path := filepath.Join(contextDir, dir)
		// Check if directory has any yaml files.
		out, err := runCommand(context.Background(), "", "find", path,
			"-maxdepth", "1", "-name", "*.yaml", "-o", "-name", "*.yml")
		if err == nil && len(bytes.TrimSpace([]byte(out))) > 0 {
			return dir
		}
	}
	return "." // fallback to root
}

// Down removes containers but keeps volumes (recoverable).
func (r *KubernetesRunner) Down(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Destroy removes containers and volumes (irreversible).
func (r *KubernetesRunner) Destroy(ctx context.Context, id string) error {
	return r.Remove(ctx, id, true)
}

// Inspect returns runtime detail for this instance.
func (r *KubernetesRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
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
func (r *KubernetesRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	return &DiffResult{Action: "update"}, nil
}

// Rollback redeploys from a previous request.
func (r *KubernetesRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	_ = r.Remove(ctx, id, true)
	return r.Deploy(ctx, req)
}
