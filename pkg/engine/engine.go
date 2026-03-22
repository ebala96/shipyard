package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// DeployRequest is passed to every engine runner when deploying a service.
type DeployRequest struct {
	ServiceName   string
	StackName     string
	Mode          string
	ContextDir    string
	Shipfile      *shipfile.Shipfile
	Resolved      *shipfile.ResolvedMode
	InstanceIndex int
}

// DeployedInstance represents one running instance of a service.
type DeployedInstance struct {
	ID            string
	Name          string
	ServiceName   string
	StackName     string
	Mode          string
	Engine        shipfile.EngineType
	Ports         map[string]int
	InstanceIndex int
}

// ServiceDetail holds full runtime detail for one service — returned by Inspect.
type ServiceDetail struct {
	Name      string
	Image     string
	Platform  string
	Instances []InstanceInfo
	Ports     []PortBinding
	Env       []string
	Mounts    []MountInfo
	Command   string
	Created   string
}

// InstanceInfo is a single running container/pod.
type InstanceInfo struct {
	ID     string
	Status string
	Node   string
}

// PortBinding maps a container port to a host port.
type PortBinding struct {
	Name          string
	ContainerPort int
	HostPort      int
	Protocol      string
}

// MountInfo describes a volume or bind mount.
type MountInfo struct {
	Type        string // "bind" | "volume"
	Source      string
	Destination string
	ReadOnly    bool
}

// DiffResult classifies changes between desired and actual state.
type DiffResult struct {
	Action  string   // "create" | "update" | "destroy" | "unchanged"
	Changes []string // human-readable change descriptions
}

// LogLine is a single line of output from a running instance.
type LogLine struct {
	InstanceID string
	Stream     string // "stdout" or "stderr"
	Text       string
}

// ── Workload archetypes ───────────────────────────────────────────────────

// Archetype classifies what kind of workload a service is.
// This drives lifecycle rules across all platform adapters.
type Archetype string

const (
	ArchetypeLongRunning Archetype = "long-running" // persistent HTTP service (default)
	ArchetypeStateful    Archetype = "stateful"      // ordered startup, bound volumes
	ArchetypeBatch       Archetype = "batch"          // run-to-completion
	ArchetypeScheduled   Archetype = "scheduled"      // cron-triggered
	ArchetypeDaemon      Archetype = "daemon"         // one per node
	ArchetypeComposite   Archetype = "composite"      // multi-service stack
)

// InferArchetype classifies a service based on its shipfile configuration.
// Users can override this by setting service.archetype in the shipfile.
func InferArchetype(sf *shipfile.Shipfile, mode string) Archetype {
	if sf == nil {
		return ArchetypeLongRunning
	}
	name := strings.ToLower(sf.Service.Name)

	// Stateful: has volumes and looks like a database.
	m, ok := sf.Service.Modes[mode]
	if !ok {
		m, ok = sf.Service.Modes["production"]
	}
	if ok && len(m.Runtime.Volumes) > 0 {
		for _, db := range []string{"postgres", "mysql", "mongo", "redis", "mariadb", "sqlite"} {
			if strings.Contains(name, db) {
				return ArchetypeStateful
			}
		}
	}

	// Batch: name suggests a job or migration.
	for _, batch := range []string{"job", "migration", "migrate", "seed", "import", "export"} {
		if strings.Contains(name, batch) {
			return ArchetypeBatch
		}
	}

	// Daemon: name suggests a background agent.
	for _, daemon := range []string{"agent", "daemon", "worker", "collector", "exporter"} {
		if strings.Contains(name, daemon) {
			return ArchetypeDaemon
		}
	}

	// Default: long-running HTTP service.
	return ArchetypeLongRunning
}

// ── PlatformAdapter interface ─────────────────────────────────────────────

// Runner is the interface every platform adapter must implement.
// Adding a new platform (Swarm, Terraform, Fly.io) means implementing
// this interface — zero changes to the orchestrator or pipeline.
type Runner interface {
	// Core lifecycle
	Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error)
	Stop(ctx context.Context, id string) error
	Start(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error

	// Down removes containers but keeps volumes and records (recoverable).
	Down(ctx context.Context, id string) error

	// Destroy removes containers and volumes (irreversible).
	Destroy(ctx context.Context, id string) error

	// Remove stops and deletes an instance.
	Remove(ctx context.Context, id string, force bool) error

	// Observability
	Status(ctx context.Context, id string) (string, error)
	Inspect(ctx context.Context, id string) (*ServiceDetail, error)
	Exec(ctx context.Context, id string, cmd []string) (string, error)
	Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error)

	// GitOps / rollback
	Diff(ctx context.Context, desiredReq DeployRequest, actualID string) (*DiffResult, error)
	Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error)

	// EngineName identifies this adapter.
	EngineName() shipfile.EngineType
}

// Factory creates the correct Runner for a given engine type.
func Factory(cfg shipfile.EngineConfig) (Runner, error) {
	engineType := cfg.Type
	if engineType == "" {
		engineType = shipfile.EngineDocker
	}
	switch engineType {
	case shipfile.EngineDocker:
		return NewDockerRunner(cfg.Connection.Host)
	case shipfile.EnginePodman:
		return NewPodmanRunner(cfg.Connection.Host)
	case shipfile.EngineCompose:
		return NewComposeRunner()
	case shipfile.EngineKubernetes, shipfile.EngineK3s:
		return NewKubernetesRunner(cfg.Connection.Kubeconfig, cfg.Connection.Namespace)
	case shipfile.EngineNomad:
		return NewNomadRunner(cfg.Connection.NomadAddr, cfg.Connection.NomadToken)
	case "swarm":
		return NewSwarmRunner()
	case "terraform":
		return NewTerraformRunner()
	default:
		return nil, fmt.Errorf("engine: unsupported engine type %q", engineType)
	}
}