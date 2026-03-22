package shipfile

// EngineType defines which container platform runs this service.
type EngineType string

const (
	EngineDocker     EngineType = "docker"
	EngineCompose    EngineType = "compose"
	EngineK3s        EngineType = "k3s"
	EngineKubernetes EngineType = "kubernetes"
	EngineNomad      EngineType = "nomad"
	EnginePodman     EngineType = "podman"
)

// SourceType defines where the service source code comes from.
type SourceType string

const (
	SourceGitHub SourceType = "github"
	SourceZip    SourceType = "zip"
	SourceLocal  SourceType = "local"
)

// LBStrategy defines the load balancing algorithm.
type LBStrategy string

const (
	LBRoundRobin  LBStrategy = "round-robin"
	LBLeastConn   LBStrategy = "least-conn"
	LBIPHash      LBStrategy = "ip-hash"
)

// ── Top-level ─────────────────────────────────────────────────────────────────

// Shipfile is the top-level structure of a shipfile.yml manifest.
type Shipfile struct {
	Version string  `yaml:"version"`
	Service Service `yaml:"service"`
}

// Service describes a single deployable service.
type Service struct {
	Name         string          `yaml:"name"`
	Description  string          `yaml:"description"`
	Tags         []string        `yaml:"tags"`
	Source       Source          `yaml:"source"`
	Engine       EngineConfig    `yaml:"engine"`
	Modes        map[string]Mode `yaml:"modes"`
	Scale        ScaleConfig     `yaml:"scale"`
	Dependencies []Dependency    `yaml:"dependencies"`
}

// ── Source ────────────────────────────────────────────────────────────────────

// Source defines where the service source code comes from.
type Source struct {
	Type    SourceType `yaml:"type"`    // github | zip | local
	URL     string     `yaml:"url"`     // GitHub repo URL e.g. https://github.com/user/repo
	Branch  string     `yaml:"branch"`  // defaults to repo default branch
	Context string     `yaml:"context"` // subfolder inside repo, defaults to "."
}

// ── Engine ────────────────────────────────────────────────────────────────────

// EngineConfig defines which platform runs this service and how to connect to it.
type EngineConfig struct {
	Type       EngineType       `yaml:"type"`       // docker | compose | k3s | kubernetes | nomad | podman
	Connection EngineConnection `yaml:"connection"` // how to connect to the engine
}

// EngineConnection holds the connection details for a remote or local engine.
type EngineConnection struct {
	// Docker / Podman
	Host string `yaml:"host"` // empty = local socket. e.g. "tcp://remote-host:2376"

	// Kubernetes / K3s
	Kubeconfig string `yaml:"kubeconfig"` // path to kubeconfig file. empty = in-cluster or ~/.kube/config
	Namespace  string `yaml:"namespace"`  // k8s namespace. defaults to "default"

	// Nomad
	NomadAddr  string `yaml:"nomadAddr"`  // e.g. "http://nomad-server:4646"
	NomadToken string `yaml:"nomadToken"` // ACL token if Nomad auth is enabled
}

// ── Modes ─────────────────────────────────────────────────────────────────────

// Mode defines how a service is built and run in a specific mode (dev or production).
type Mode struct {
	Build   Build   `yaml:"build"`
	Runtime Runtime `yaml:"runtime"`
	IDE     IDE     `yaml:"ide"`
	VNC     VNC     `yaml:"vnc"`
}

// Build holds image/artifact build configuration for a mode.
type Build struct {
	Dockerfile  string            `yaml:"dockerfile"`  // for docker/podman/k8s
	ComposeFile string            `yaml:"composeFile"` // for compose engine, defaults to "docker-compose.yml"
	ManifestDir string            `yaml:"manifestDir"` // for k8s/k3s, directory of yaml manifests
	NomadJob    string            `yaml:"nomadJob"`    // for nomad, path to .nomad job file
	Args        map[string]string `yaml:"args"`        // build args passed to docker build
}

// Runtime holds the container runtime configuration for a mode.
type Runtime struct {
	Image     string            `yaml:"image"`     // pre-built image to pull (skips build step)
	Ports     []Port            `yaml:"ports"`
	Env       map[string]string `yaml:"env"`
	Volumes   []Volume          `yaml:"volumes"`
	Resources Resources         `yaml:"resources"`
	Health    HealthCheck       `yaml:"health"`
}

// Port defines a named port mapping for a service.
type Port struct {
	Name     string `yaml:"name"`
	Internal int    `yaml:"internal"`
	External string `yaml:"external"` // "auto" or a specific port number as string
}

// Volume defines a mount for a service container.
type Volume struct {
	Type     string `yaml:"type"`     // "bind" or "volume"
	From     string `yaml:"from"`     // host path or named volume
	To       string `yaml:"to"`       // container path
	ReadOnly bool   `yaml:"readonly"`
}

// Resources defines CPU and memory limits per container instance.
type Resources struct {
	CPU    float64 `yaml:"cpu"`    // cores e.g. 0.5 = half a core
	Memory string  `yaml:"memory"` // e.g. "256m", "1g"
}

// HealthCheck defines how to verify a container is healthy.
type HealthCheck struct {
	Path     string `yaml:"path"`     // HTTP path e.g. "/healthz"
	Port     string `yaml:"port"`     // references a named port e.g. "app"
	Interval int    `yaml:"interval"` // seconds between checks, default 10
	Timeout  int    `yaml:"timeout"`  // seconds before check fails, default 5
	Retries  int    `yaml:"retries"`  // failures before marked unhealthy, default 3
}

// IDE controls whether a code-server sidecar is attached in this mode.
type IDE struct {
	Enabled bool `yaml:"enabled"`
}

// VNC controls whether a noVNC sidecar is attached to expose a GUI display.
type VNC struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"` // VNC server port on the main container, default 5900
}

// ── Scale ─────────────────────────────────────────────────────────────────────

// ScaleConfig defines instance count, resource limits, autoscaling and load balancing.
// Users edit this section in the dashboard — Shipyard reconciles the running state.
type ScaleConfig struct {
	Instances    int              `yaml:"instances"`    // desired instance count, default 1
	Resources    Resources        `yaml:"resources"`    // per-instance CPU and memory
	Autoscale    AutoscaleConfig  `yaml:"autoscale"`
	LoadBalancer LoadBalancerConfig `yaml:"loadBalancer"`
}

// AutoscaleConfig defines thresholds for automatic scale-up and scale-down.
type AutoscaleConfig struct {
	Enabled      bool `yaml:"enabled"`
	Min          int  `yaml:"min"`          // minimum instances when scaling down
	Max          int  `yaml:"max"`          // maximum instances when scaling up
	TargetCPU    int  `yaml:"targetCPU"`    // scale up when avg CPU% exceeds this
	TargetMemory int  `yaml:"targetMemory"` // scale up when avg memory% exceeds this
	CooldownSecs int  `yaml:"cooldownSecs"` // seconds to wait between scale events, default 60
}

// LoadBalancerConfig defines how traffic is distributed across instances.
type LoadBalancerConfig struct {
	Enabled  bool       `yaml:"enabled"`
	Strategy LBStrategy `yaml:"strategy"` // round-robin | least-conn | ip-hash
	// StickySession keeps a client routed to the same instance.
	StickySession bool `yaml:"stickySession"`
}

// ── Dependencies ──────────────────────────────────────────────────────────────

// Dependency declares a service that this service depends on.
type Dependency struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
	WaitFor  string `yaml:"waitFor"` // "health" or "start"
}

// ── Resolved ─────────────────────────────────────────────────────────────────

// ResolvedMode is a Mode with all ${variable} expressions substituted
// and all "auto" ports replaced with actual assigned port numbers.
type ResolvedMode struct {
	Mode
	ResolvedPorts map[string]int    // port name -> actual host port
	ResolvedEnv   map[string]string // env vars with variables substituted
}

