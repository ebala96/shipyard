package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	dockercontainertype "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	composelib "github.com/shipyard/shipyard/pkg/compose"
	enginepkg "github.com/shipyard/shipyard/pkg/engine"
	"github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/vnc"
	"gopkg.in/yaml.v3"
)

// yamlUnmarshal is a local alias so we don't shadow the import.
func yamlUnmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

// DeployRequest describes everything needed to deploy a single service.
type DeployRequest struct {
	// ServiceName is the logical name of the service (from shipfile).
	ServiceName string

	// Mode is the mode to deploy in ("dev" or "production").
	Mode string

	// ContextDir is the absolute path to the service source directory.
	ContextDir string

	// Shipfile is the parsed and validated service manifest.
	Shipfile *shipfile.Shipfile

	// StackName is the name of the stack this service belongs to.
	// Used for the shared Docker network name.
	// Leave empty for standalone deployments.
	StackName string

	// TargetNode is the node selected by the scheduler.
	// Empty means localhost (single-node mode).
	TargetNode string
}

// DeployedService holds the runtime state of a deployed service.
type DeployedService struct {
	ContainerID   string       `json:"containerID"`
	ContainerName string       `json:"containerName"`
	ImageTag      string       `json:"imageTag"`
	ServiceName   string       `json:"serviceName"`
	StackName     string       `json:"stackName"`
	Mode          string       `json:"mode"`
	Ports         map[string]int `json:"ports"`
	// IDE is populated when the service is deployed in dev mode with IDE enabled.
	IDE *IDEInstance `json:"ide,omitempty"`
	// VNC is populated when the shipfile mode has vnc.enabled = true.
	VNC *vnc.Instance `json:"vnc,omitempty"`
}

// Orchestrator is the top-level coordinator for building, running,
// and managing Docker containers from shipfile manifests.
type Orchestrator struct {
	builder   *Builder
	runner    *Runner
	lifecycle *Lifecycle
	logs      *LogStreamer
	docker    *client.Client
}

// New creates a fully initialised Orchestrator connected to the local Docker daemon.
func New() (*Orchestrator, error) {
	builder, err := NewBuilder()
	if err != nil {
		return nil, err
	}

	runner, err := NewRunner()
	if err != nil {
		return nil, err
	}

	lifecycle, err := NewLifecycle()
	if err != nil {
		return nil, err
	}

	logs, err := NewLogStreamer()
	if err != nil {
		return nil, err
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("orchestrator: failed to connect to Docker daemon: %w", err)
	}

	return &Orchestrator{
		builder:   builder,
		runner:    runner,
		lifecycle: lifecycle,
		logs:      logs,
		docker:    cli,
	}, nil
}

// Deploy builds and runs a service container from a DeployRequest.
// Routes to the correct engine based on the shipfile engine config.
func (o *Orchestrator) Deploy(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	engineType := req.Shipfile.Service.Engine.Type
	if engineType == "" {
		engineType = shipfile.EngineDocker
	}

	// Swarm and Terraform route through pkg/engine adapters.
	switch engineType {
	case "swarm", "terraform":
		return o.deployViaEngineAdapter(ctx, req, engineType)
	case shipfile.EngineCompose:
		return o.deployCompose(ctx, req)
	case shipfile.EngineKubernetes, shipfile.EngineK3s:
		return o.deployKubernetes(ctx, req)
	case shipfile.EngineNomad:
		return o.deployNomad(ctx, req)
	default:
		return o.deployDocker(ctx, req)
	}
}

// deployViaEngineAdapter routes a deploy through the pkg/engine PlatformAdapter interface.
// Used for Swarm and Terraform — new adapters added here without touching the orchestrator.
func (o *Orchestrator) deployViaEngineAdapter(ctx context.Context, req DeployRequest, engineType shipfile.EngineType) (*DeployedService, error) {
	cfg := req.Shipfile.Service.Engine
	cfg.Type = engineType

	runner, err := enginepkg.Factory(cfg)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: engine factory failed: %w", err)
	}

	resolved, err := shipfile.Resolve(req.Shipfile, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve failed: %w", err)
	}

	instance, err := runner.Deploy(ctx, enginepkg.DeployRequest{
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		ContextDir:  req.ContextDir,
		Shipfile:    req.Shipfile,
		Resolved:    resolved,
	})
	if err != nil {
		return nil, fmt.Errorf("orchestrator: %s deploy failed: %w", engineType, err)
	}

	return &DeployedService{
		ContainerID:   instance.ID,
		ContainerName: instance.Name,
		ServiceName:   instance.ServiceName,
		StackName:     instance.StackName,
		Mode:          instance.Mode,
		Ports:         instance.Ports,
	}, nil
}

// Inspect returns full runtime detail for a container via the engine adapter.
func (o *Orchestrator) Inspect(ctx context.Context, containerID string) (*enginepkg.ServiceDetail, error) {
	runner, err := enginepkg.NewDockerRunner("")
	if err != nil {
		return nil, fmt.Errorf("orchestrator: inspect failed: %w", err)
	}
	return runner.Inspect(ctx, containerID)
}

// deployDocker handles Docker and Podman engine deployments.
func (o *Orchestrator) deployDocker(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	// Step 1: Resolve the shipfile mode.
	resolved, err := shipfile.Resolve(req.Shipfile, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve failed: %w", err)
	}

	// Step 2: Ensure stack network exists.
	networkName := ""
	if req.StackName != "" {
		networkName = networkNameFor(req.StackName)
		if err := o.ensureNetwork(ctx, networkName); err != nil {
			return nil, fmt.Errorf("orchestrator: network setup failed: %w", err)
		}
	}

	// Step 3: Build or pull the image.
	imageTag := imageTagFor(req.StackName, req.ServiceName, req.Mode)
	dockerfile := resolved.Build.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	dockerfilePath := filepath.Join(req.ContextDir, dockerfile)
	hasDockerfile := fileExists(dockerfilePath)

	// Runtime.Image takes highest priority — used by templates and catalog.
	if resolved.Runtime.Image != "" {
		fmt.Printf("orchestrator: using runtime image %s for %q\n", resolved.Runtime.Image, req.ServiceName)
		pullCtx := context.Background()
		reader, err := o.docker.ImagePull(pullCtx, resolved.Runtime.Image, dockerImagePullOptions())
		if err == nil {
			io.ReadAll(reader)
			reader.Close()
			imageTag = resolved.Runtime.Image
		}
		hasDockerfile = false // skip build step
	} else if known, ok := knownPublishedImage(req.ServiceName); ok {
		fmt.Printf("orchestrator: using known published image %s for %q\n", known, req.ServiceName)
		pullCtx := context.Background()
		reader, err := o.docker.ImagePull(pullCtx, known, dockerImagePullOptions())
		if err == nil {
			io.ReadAll(reader)
			reader.Close()
			imageTag = known
			hasDockerfile = false // skip build path below
		}
	}

	if hasDockerfile {
		_, buildErr := o.builder.Build(ctx, req.ContextDir, resolved, imageTag)
		if buildErr != nil {
			fmt.Printf("orchestrator: build failed (%v), falling back to published image\n", buildErr)
			// Use a fresh context — the request context may have timed out during the long build.
			pullCtx := context.Background()
			pulled, pullTag := o.tryPullPublishedImage(pullCtx, req.ServiceName)
			if pulled {
				imageTag = pullTag
			} else {
				return nil, fmt.Errorf("orchestrator: build failed and no published image found: %w", buildErr)
			}
		}
	} else {
		fmt.Printf("orchestrator: no Dockerfile found for %q, pulling official image\n", req.ServiceName)
		pullCtx := context.Background()
		pulled, pullTag := o.tryPullPublishedImage(pullCtx, req.ServiceName)
		if pulled {
			imageTag = pullTag
			fmt.Printf("orchestrator: using image %s\n", imageTag)
		} else {
			return nil, fmt.Errorf("orchestrator: no Dockerfile and could not find a published image for %q — try onboarding with a compose file instead", req.ServiceName)
		}
	}

	// Step 4: Run the main service container.
	containerName := containerNameFor(req.StackName, req.ServiceName, req.Mode)
	result, err := o.runner.Run(ctx, imageTag, containerName, networkName, resolved)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: run failed: %w", err)
	}

	deployed := &DeployedService{
		ContainerID:   result.ContainerID,
		ContainerName: result.ContainerName,
		ImageTag:      imageTag,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Ports:         result.Ports,
	}

	// Step 5: Launch code-server sidecar if IDE enabled.
	mode, _ := req.Shipfile.GetMode(req.Mode)
	if mode.IDE.Enabled && req.ContextDir != "" {
		ideRunner := newIDERunner(o)
		ide, err := ideRunner.Launch(ctx, req.ServiceName, req.Mode, networkName, req.ContextDir)
		if err != nil {
			fmt.Printf("orchestrator: IDE sidecar failed (non-fatal): %v\n", err)
		} else {
			deployed.IDE = ide
		}
	}

	// Step 6: Launch noVNC sidecar if VNC enabled.
	if mode.VNC.Enabled {
		vncLauncher, err := vnc.NewLauncher()
		if err != nil {
			fmt.Printf("orchestrator: VNC launcher init failed (non-fatal): %v\n", err)
		} else {
			vncInst, err := vncLauncher.Launch(ctx, req.ServiceName, req.Mode, networkName, result.ContainerName, mode.VNC.Port)
			if err != nil {
				fmt.Printf("orchestrator: VNC sidecar failed (non-fatal): %v\n", err)
			} else {
				deployed.VNC = vncInst
			}
		}
	}

	return deployed, nil
}

// deployCompose handles Docker Compose engine deployments.
func (o *Orchestrator) deployCompose(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	resolved, err := shipfile.Resolve(req.Shipfile, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve failed: %w", err)
	}

	projectName := composeProjectNameWithMode(req.StackName, req.ServiceName, req.Mode)

	composeFile := resolved.Build.ComposeFile
	if composeFile == "" {
		composeFile = detectComposeFile(req.ContextDir)
	}

	composeFilePath := filepath.Join(req.ContextDir, composeFile)

	// Generate a dynamic port override so hardcoded ports don't conflict.
	override, err := composelib.GenerateDynamicPortOverride(composeFilePath, req.ContextDir)
	if err != nil {
		fmt.Printf("orchestrator: could not generate port override: %v\n", err)
		override = &composelib.OverrideResult{Ports: map[string]int{}}
	} else if len(override.Ports) > 0 {
		fmt.Printf("orchestrator: port override for %s: %v\n", req.ServiceName, override.Ports)
		if override.OverrideFilePath != "" {
			if data, err := os.ReadFile(override.OverrideFilePath); err == nil {
				fmt.Printf("orchestrator: override file contents:\n%s\n", string(data))
			}
		}
	} else {
		fmt.Printf("orchestrator: no ports found to override in compose file\n")
	}

	// Force remove any stale containers from previous attempts.
	// This handles containers created before project-name based tracking.
	runShellCommand(ctx, req.ContextDir, "docker", "compose",
		"--project-name", projectName,
		"--file", composeFilePath,
		"down", "--remove-orphans")

	// Also try bringing down with just the compose file (no project name)
	// to catch containers created by earlier direct `docker compose up` calls.
	runShellCommand(ctx, req.ContextDir, "docker", "compose",
		"--file", composeFilePath,
		"down", "--remove-orphans")

	// Force remove containers by the names compose would use.
	// This clears Docker's cached port config from previous deployments.
	o.forceRemoveComposeContainers(ctx, composeFilePath, req.ContextDir)

	// Build the docker compose command.
	// Use the merged file directly if one was generated — this avoids
	// compose override caching issues entirely.
	composeArg := composeFilePath
	if override.OverrideFilePath != "" {
		composeArg = override.OverrideFilePath
		fmt.Printf("orchestrator: using merged compose file: %s\n", composeArg)
	}

	args := []string{"compose",
		"--project-name", projectName,
		"--file", composeArg,
		"up", "--build", "--detach",
	}

	out, err := runShellCommand(ctx, req.ContextDir, "docker", args...)

	fmt.Printf("orchestrator: compose command: docker %v\n", args)
	if override.OverrideFilePath != "" {
		if data, readErr := os.ReadFile(override.OverrideFilePath); readErr == nil {
			fmt.Printf("orchestrator: override file content:\n%s\n", string(data))
		}
	}

	// Always clean up the override file after deploy attempt.
	composelib.CleanOverride(req.ContextDir)

	if err != nil {
		return nil, fmt.Errorf("orchestrator: docker compose up failed: %w\noutput: %s", err, out)
	}

	// Get the actual container ID from compose.
	realID, realName := o.getComposeContainerID(ctx, projectName)

	// Merge override ports with resolved ports — override wins.
	finalPorts := make(map[string]int)
	for k, v := range resolved.ResolvedPorts {
		finalPorts[k] = v
	}
	for k, v := range override.Ports {
		// key is "serviceName/containerPort" — simplify to just containerPort as string.
		parts := strings.SplitN(k, "/", 2)
		if len(parts) == 2 {
			finalPorts[parts[1]] = v
		}
	}

	deployed := &DeployedService{
		ContainerID:   realID,
		ContainerName: realName,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Ports:         finalPorts,
	}

	// Launch IDE sidecar for dev mode.
	mode, _ := req.Shipfile.GetMode(req.Mode)
	if mode.IDE.Enabled && req.ContextDir != "" {
		ideRunner := newIDERunner(o)
		ide, err := ideRunner.Launch(ctx, req.ServiceName, req.Mode, "", req.ContextDir)
		if err != nil {
			fmt.Printf("orchestrator: IDE sidecar failed (non-fatal): %v\n", err)
		} else {
			deployed.IDE = ide
		}
	}

	return deployed, nil
}

// knownPublishedImage returns the official Docker Hub image for well-known
// open source services that cannot be easily built from source.
func knownPublishedImage(serviceName string) (string, bool) {
	known := map[string]string{
		"prometheus":   "prom/prometheus:latest",
		"cadvisor":     "ghcr.io/google/cadvisor:latest",
		"alertmanager": "prom/alertmanager:latest",
		"grafana":      "grafana/grafana:latest",
		"loki":         "grafana/loki:latest",
		"traefik":      "traefik:latest",
		"gitea":        "gitea/gitea:latest",
		"minio":        "minio/minio:latest",
		"vault":        "hashicorp/vault:latest",
		"keycloak":     "quay.io/keycloak/keycloak:latest",
		"netdata":      "netdata/netdata:latest",
		"outline":      "outlinewiki/outline:latest",
		"authentik":    "ghcr.io/goauthentik/server:latest",
		"verdaccio":    "verdaccio/verdaccio:latest",
		"pocketbase":   "ghcr.io/muchobien/pocketbase:latest",
		"drone":        "drone/drone:latest",
		"sonarqube":    "sonarqube:latest",
	}
	img, ok := known[serviceName]
	return img, ok
}

// tryPullPublishedImage attempts to pull a published Docker image for a service.
// It tries common image naming patterns used by open source projects.
func (o *Orchestrator) tryPullPublishedImage(ctx context.Context, serviceName string) (bool, string) {
	// Build candidates in priority order.
	// Many projects use prom/prometheus, grafana/grafana etc as org/name.
	candidates := []string{
		serviceName + ":latest",                      // prometheus:latest
		serviceName + "/" + serviceName + ":latest",  // grafana/grafana:latest
	}

	// Well-known image name mappings for common services.
	knownImages := map[string]string{
		"prometheus":   "prom/prometheus:latest",
		"grafana":      "grafana/grafana:latest",
		"loki":         "grafana/loki:latest",
		"traefik":      "traefik:latest",
		"gitea":        "gitea/gitea:latest",
		"minio":        "minio/minio:latest",
		"vault":        "hashicorp/vault:latest",
		"keycloak":     "quay.io/keycloak/keycloak:latest",
		"netdata":      "netdata/netdata:latest",
		"outline":      "outlinewiki/outline:latest",
		"plane":        "makeplane/plane-backend:latest",
		"authentik":    "ghcr.io/goauthentik/server:latest",
		"verdaccio":    "verdaccio/verdaccio:latest",
		"pocketbase":   "ghcr.io/muchobien/pocketbase:latest",
	}

	if known, ok := knownImages[serviceName]; ok {
		candidates = append([]string{known}, candidates...)
	}

	for _, candidate := range candidates {
		fmt.Printf("orchestrator: trying to pull %s\n", candidate)
		reader, err := o.docker.ImagePull(ctx, candidate, dockerImagePullOptions())
		if err != nil {
			continue
		}
		io.ReadAll(reader)
		reader.Close()
		fmt.Printf("orchestrator: pulled %s successfully\n", candidate)
		return true, candidate
	}
	return false, ""
}

// forceRemoveComposeContainers reads the compose file, finds all service names,
// and force-removes any containers matching those names.
// This clears Docker's cached port config so the override takes effect cleanly.
func (o *Orchestrator) forceRemoveComposeContainers(ctx context.Context, composeFilePath, contextDir string) {
	data, err := os.ReadFile(composeFilePath)
	if err != nil {
		return
	}

	var cf struct {
		Services map[string]struct {
			ContainerName string `yaml:"container_name"`
		} `yaml:"services"`
	}

	if err := yamlUnmarshal(data, &cf); err != nil {
		return
	}

	for svcName, svc := range cf.Services {
		// Try the explicit container_name first, then the service name.
		names := []string{svcName}
		if svc.ContainerName != "" {
			names = append([]string{svc.ContainerName}, names...)
		}

		for _, name := range names {
			runShellCommand(ctx, "", "docker", "stop", name)
			runShellCommand(ctx, "", "docker", "rm", "--force", name)
		}
	}
}

// getComposeContainerID returns the first running container ID for a compose project.
func (o *Orchestrator) getComposeContainerID(ctx context.Context, projectName string) (id, name string) {
	out, err := runShellCommand(ctx, "", "docker", "compose",
		"--project-name", projectName,
		"ps", "-q", "--status", "running")
	if err != nil || len(out) == 0 {
		// Fallback — list all containers in the project
		out, _ = runShellCommand(ctx, "", "docker", "compose",
			"--project-name", projectName, "ps", "-q")
	}

	lines := splitLines(out)
	if len(lines) == 0 {
		return projectName, projectName
	}

	id = lines[0]

	// Get the container name from the ID.
	nameOut, err := runShellCommand(ctx, "", "docker",
		"inspect", "--format", "{{.Name}}", id)
	if err == nil {
		name = strings.TrimPrefix(strings.TrimSpace(nameOut), "/")
	} else {
		name = id[:min(12, len(id))]
	}

	return id, name
}

// deployKubernetes handles Kubernetes / K3s engine deployments.
func (o *Orchestrator) deployKubernetes(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	resolved, err := shipfile.Resolve(req.Shipfile, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve failed: %w", err)
	}

	manifestDir := resolved.Build.ManifestDir
	if manifestDir == "" {
		manifestDir = "."
	}

	args := []string{"apply", "--recursive", "-f", manifestDir}
	out, err := runShellCommand(ctx, req.ContextDir, "kubectl", args...)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: kubectl apply failed: %w\noutput: %s", err, out)
	}

	name := sanitize(req.ServiceName)
	return &DeployedService{
		ContainerID:   name,
		ContainerName: name,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Ports:         resolved.ResolvedPorts,
	}, nil
}

// deployNomad handles Nomad engine deployments.
func (o *Orchestrator) deployNomad(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	resolved, err := shipfile.Resolve(req.Shipfile, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve failed: %w", err)
	}

	jobFile := resolved.Build.NomadJob
	if jobFile == "" {
		jobFile = req.ServiceName + ".nomad"
	}

	args := []string{"job", "run", jobFile}
	out, err := runShellCommand(ctx, req.ContextDir, "nomad", args...)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: nomad job run failed: %w\noutput: %s", err, out)
	}

	name := sanitize(req.ServiceName)
	return &DeployedService{
		ContainerID:   name,
		ContainerName: name,
		ServiceName:   req.ServiceName,
		StackName:     req.StackName,
		Mode:          req.Mode,
		Ports:         resolved.ResolvedPorts,
	}, nil
}

// Stop stops a running service container gracefully.
func (o *Orchestrator) Stop(ctx context.Context, containerID string) error {
	return o.lifecycle.Stop(ctx, containerID, 10)
}

// Start starts a stopped service container.
func (o *Orchestrator) Start(ctx context.Context, containerID string) error {
	return o.lifecycle.Start(ctx, containerID)
}

// Restart restarts a service container.
func (o *Orchestrator) Restart(ctx context.Context, containerID string) error {
	return o.lifecycle.Restart(ctx, containerID, 10)
}

// Remove stops and removes a service container.
func (o *Orchestrator) Remove(ctx context.Context, containerID string, force bool) error {
	return o.lifecycle.Remove(ctx, containerID, force)
}

// RemoveWithIDE stops and removes a service container and its IDE sidecar if present.
// serviceName and mode are needed to find the sidecar container name.
func (o *Orchestrator) RemoveWithIDE(ctx context.Context, containerID, serviceName, mode string, force bool) error {
	// Remove main container first.
	if err := o.lifecycle.Remove(ctx, containerID, force); err != nil {
		return err
	}
	// Remove IDE sidecar — non-fatal if it doesn't exist.
	ide := newIDERunner(o)
	_ = ide.Stop(ctx, serviceName, mode)
	return nil
}

// StopWithIDE stops a container and its IDE sidecar.
func (o *Orchestrator) StopWithIDE(ctx context.Context, containerID, serviceName, mode string) error {
	if err := o.lifecycle.Stop(ctx, containerID, 10); err != nil {
		return err
	}
	ide := newIDERunner(o)
	_ = ide.Stop(ctx, serviceName, mode)
	return nil
}

// RemoveWithVNC stops and removes a service container and its noVNC sidecar if present.
func (o *Orchestrator) RemoveWithVNC(ctx context.Context, containerID, serviceName, mode string, force bool) error {
	if err := o.lifecycle.Remove(ctx, containerID, force); err != nil {
		return err
	}
	if launcher, err := vnc.NewLauncher(); err == nil {
		_ = launcher.Stop(ctx, serviceName, mode)
	}
	return nil
}

// StopWithVNC stops a container and its noVNC sidecar.
func (o *Orchestrator) StopWithVNC(ctx context.Context, containerID, serviceName, mode string) error {
	if err := o.lifecycle.Stop(ctx, containerID, 10); err != nil {
		return err
	}
	if launcher, err := vnc.NewLauncher(); err == nil {
		_ = launcher.Stop(ctx, serviceName, mode)
	}
	return nil
}

// Exec runs a command inside a running service container.
func (o *Orchestrator) Exec(ctx context.Context, containerID string, cmd []string) (string, error) {
	return o.lifecycle.Exec(ctx, containerID, cmd)
}

// Status returns the current state of a container.
func (o *Orchestrator) Status(ctx context.Context, containerID string) (string, error) {
	return o.lifecycle.Status(ctx, containerID)
}

// StreamLogs returns a channel of live log lines from a container.
func (o *Orchestrator) StreamLogs(ctx context.Context, containerID string, tail string) (<-chan LogLine, <-chan error) {
	return o.logs.Stream(ctx, containerID, tail)
}

// FetchLogs returns recent log lines from a container without streaming.
func (o *Orchestrator) FetchLogs(ctx context.Context, containerID string, tail string) ([]LogLine, error) {
	return o.logs.Fetch(ctx, containerID, tail)
}

// ScaleUp starts one additional instance of a deployed service.
// It reuses the already-built image rather than rebuilding.
func (o *Orchestrator) ScaleUp(ctx context.Context, serviceName, mode, stackName string, resolved *shipfile.ResolvedMode) (*DeployedService, error) {
	// Find the image already used by existing instances of this service.
	imageTag, err := o.findExistingImage(ctx, serviceName, mode, stackName)
	if err != nil || imageTag == "" {
		return nil, fmt.Errorf("orchestrator: no existing image found for %q — deploy the service first", serviceName)
	}

	networkName := ""
	if stackName != "" {
		networkName = networkNameFor(stackName)
	}

	containerName := containerNameFor(stackName, serviceName, mode)
	result, err := o.runner.Run(ctx, imageTag, containerName, networkName, resolved)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: scale up failed: %w", err)
	}

	return &DeployedService{
		ContainerID:   result.ContainerID,
		ContainerName: result.ContainerName,
		ImageTag:      imageTag,
		ServiceName:   serviceName,
		StackName:     stackName,
		Mode:          mode,
		Ports:         result.Ports,
	}, nil
}

// findExistingImage returns the image tag used by existing containers for a service.
func (o *Orchestrator) findExistingImage(ctx context.Context, serviceName, mode, stackName string) (string, error) {
	f := filters.NewArgs()
	base := fmt.Sprintf("shipyard_%s_%s", sanitize(serviceName), mode)
	if stackName != "" {
		base = fmt.Sprintf("shipyard_%s_%s_%s", sanitize(stackName), sanitize(serviceName), mode)
	}
	f.Add("name", base)

	containers, err := o.docker.ContainerList(ctx, dockercontainertype.ListOptions{All: true, Filters: f})
	if err != nil || len(containers) == 0 {
		return "", fmt.Errorf("no containers found for %q", base)
	}
	return containers[0].Image, nil
}

// ScaleDown stops and removes the most recently started instance of a service.
func (o *Orchestrator) ScaleDown(ctx context.Context, serviceName, mode, stackName string) error {
	f := filters.NewArgs()
	base := fmt.Sprintf("shipyard_%s_%s", sanitize(serviceName), mode)
	if stackName != "" {
		base = fmt.Sprintf("shipyard_%s_%s_%s", sanitize(stackName), sanitize(serviceName), mode)
	}
	f.Add("name", base)

	containers, err := o.docker.ContainerList(ctx, dockercontainertype.ListOptions{All: false, Filters: f})
	if err != nil || len(containers) == 0 {
		return fmt.Errorf("no running containers found for %q", base)
	}

	// Remove the most recently started one (last in list).
	last := containers[len(containers)-1]
	return o.lifecycle.Remove(ctx, last.ID, true)
}

// CountInstances returns the number of running containers for a service.
func (o *Orchestrator) CountInstances(ctx context.Context, serviceName, mode, stackName string) (int, error) {
	f := filters.NewArgs()
	base := fmt.Sprintf("shipyard_%s_%s", sanitize(serviceName), mode)
	if stackName != "" {
		base = fmt.Sprintf("shipyard_%s_%s_%s", sanitize(stackName), sanitize(serviceName), mode)
	}
	f.Add("name", base)
	containers, err := o.docker.ContainerList(ctx, dockercontainertype.ListOptions{All: false, Filters: f})
	if err != nil {
		return 0, err
	}
	return len(containers), nil
}

func (o *Orchestrator) ensureNetwork(ctx context.Context, networkName string) error {
	networks, err := o.docker.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return fmt.Errorf("could not list Docker networks: %w", err)
	}

	for _, n := range networks {
		if n.Name == networkName {
			return nil // already exists
		}
	}

	_, err = o.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"shipyard.network": networkName,
		},
	})
	if err != nil {
		return fmt.Errorf("could not create Docker network %q: %w", networkName, err)
	}

	return nil
}

// ── Naming helpers ────────────────────────────────────────────────────────────

// networkNameFor returns the Docker network name for a stack.
func networkNameFor(stackName string) string {
	return fmt.Sprintf("shipyard_%s", sanitize(stackName))
}

// imageTagFor returns the Docker image tag for a service in a stack.
func imageTagFor(stackName, serviceName, mode string) string {
	if stackName == "" {
		return fmt.Sprintf("shipyard/%s:%s", sanitize(serviceName), mode)
	}
	return fmt.Sprintf("shipyard/%s/%s:%s", sanitize(stackName), sanitize(serviceName), mode)
}

// containerNameFor returns the Docker container name for a service.
// It finds the next available index so multiple instances don't conflict.
// e.g. shipyard_prometheus_production, shipyard_prometheus_production_2, etc.
func containerNameFor(stackName, serviceName, mode string) string {
	base := fmt.Sprintf("shipyard_%s_%s", sanitize(serviceName), mode)
	if stackName != "" {
		base = fmt.Sprintf("shipyard_%s_%s_%s", sanitize(stackName), sanitize(serviceName), mode)
	}
	return nextAvailableContainerName(base)
}

// nextAvailableContainerName returns base if no container with that name exists,
// otherwise appends _2, _3, etc. until a free name is found.
func nextAvailableContainerName(base string) string {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return base
	}
	defer cli.Close()

	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("name", base)
	containers, err := cli.ContainerList(ctx, dockercontainertype.ListOptions{All: true, Filters: f})
	if err != nil || len(containers) == 0 {
		return base
	}

	// Collect existing names.
	existing := make(map[string]bool)
	for _, c := range containers {
		for _, n := range c.Names {
			existing[strings.TrimPrefix(n, "/")] = true
		}
	}

	if !existing[base] {
		return base
	}
	for i := 2; i <= 100; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
	return base
}

// sanitize lowercases and replaces spaces/special chars with underscores.
func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// ── State machine operations ──────────────────────────────────────────────

// Down stops and removes all containers for a stack but keeps volumes
// and the etcd record. The stack can be brought back with Deploy.
// This is different from Destroy which removes everything.
func (o *Orchestrator) Down(ctx context.Context, stackName string, containers []string) error {
	var lastErr error
	for _, id := range containers {
		if err := o.lifecycle.Stop(ctx, id, 10); err != nil {
			fmt.Printf("orchestrator: down: stop %q failed (continuing): %v\n", id, err)
		}
		if err := o.lifecycle.Remove(ctx, id, true); err != nil {
			fmt.Printf("orchestrator: down: remove %q failed: %v\n", id, err)
			lastErr = err
		}
	}
	return lastErr
}

// Destroy removes all containers AND their volumes for a stack.
// This operation is irreversible.
func (o *Orchestrator) Destroy(ctx context.Context, stackName string, containers []string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("orchestrator: destroy: failed to connect to Docker: %w", err)
	}
	defer cli.Close()

	for _, id := range containers {
		if err := o.lifecycle.Stop(ctx, id, 5); err != nil {
			fmt.Printf("orchestrator: destroy: stop %q: %v\n", id, err)
		}
		// Remove with volumes.
		if err := cli.ContainerRemove(ctx, id, dockercontainertype.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		}); err != nil {
			fmt.Printf("orchestrator: destroy: remove %q: %v\n", id, err)
		}
	}
	return nil
}

// Rollback redeploys a stack from a previous ledger entry.
func (o *Orchestrator) Rollback(ctx context.Context, req DeployRequest) (*DeployedService, error) {
	return o.Deploy(ctx, req)
}
