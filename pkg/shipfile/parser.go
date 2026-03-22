package shipfile

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Parse reads and parses a shipfile.yml from the given file path.
// Returns a validated Shipfile or an error describing what is missing or invalid.
func Parse(path string) (*Shipfile, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("shipfile: cannot resolve path %q: %w", path, err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("shipfile: cannot read file %q: %w", absPath, err)
	}

	return ParseBytes(data)
}

// ParseBytes parses a shipfile.yml from raw bytes.
// Useful for parsing files received as uploads or fetched from GitHub.
func ParseBytes(data []byte) (*Shipfile, error) {
	var sf Shipfile

	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("shipfile: invalid yaml: %w", err)
	}

	if err := validate(&sf); err != nil {
		return nil, err
	}

	return &sf, nil
}

// validate checks that all required fields are present and values are valid.
func validate(sf *Shipfile) error {
	if sf.Version == "" {
		return fmt.Errorf("shipfile: missing required field: version")
	}

	if sf.Service.Name == "" {
		return fmt.Errorf("shipfile: missing required field: service.name")
	}

	if err := validateEngine(sf.Service.Engine); err != nil {
		return err
	}

	if err := validateSource(sf.Service.Source); err != nil {
		return err
	}

	if err := validateScale(sf.Service.Scale); err != nil {
		return err
	}

	if len(sf.Service.Modes) == 0 {
		return fmt.Errorf("shipfile: service %q must define at least one mode", sf.Service.Name)
	}

	engine := sf.Service.Engine.Type
	for modeName, mode := range sf.Service.Modes {
		if err := validateMode(sf.Service.Name, modeName, mode, engine); err != nil {
			return err
		}
	}

	for i, dep := range sf.Service.Dependencies {
		if dep.Name == "" {
			return fmt.Errorf("shipfile: dependency at index %d is missing a name", i)
		}
		if dep.WaitFor != "" && dep.WaitFor != "health" && dep.WaitFor != "start" {
			return fmt.Errorf("shipfile: dependency %q has invalid waitFor value %q (must be 'health' or 'start')", dep.Name, dep.WaitFor)
		}
	}

	return nil
}

// validateEngine checks the engine config is valid.
func validateEngine(e EngineConfig) error {
	if e.Type == "" {
		return nil // defaults to docker, validated downstream
	}
	valid := map[EngineType]bool{
		EngineDocker:     true,
		EngineCompose:    true,
		EngineK3s:        true,
		EngineKubernetes: true,
		EngineNomad:      true,
		EnginePodman:     true,
	}
	if !valid[e.Type] {
		return fmt.Errorf("shipfile: unknown engine type %q (supported: docker, compose, k3s, kubernetes, nomad, podman)", e.Type)
	}

	// Kubernetes/k3s need at least kubeconfig or in-cluster.
	if (e.Type == EngineKubernetes || e.Type == EngineK3s) && e.Connection.Namespace == "" {
		// namespace defaults to "default" — not an error, just note
	}

	// Nomad needs an address.
	if e.Type == EngineNomad && e.Connection.NomadAddr == "" {
		return fmt.Errorf("shipfile: nomad engine requires connection.nomadAddr (e.g. http://localhost:4646)")
	}

	return nil
}

// validateSource checks source config is consistent.
func validateSource(s Source) error {
	if s.Type == "" {
		return nil // local is the implicit default
	}
	if s.Type == SourceGitHub && s.URL == "" {
		return fmt.Errorf("shipfile: source type 'github' requires a url")
	}
	if s.Type == SourceZip && s.URL == "" && s.Context == "" {
		return fmt.Errorf("shipfile: source type 'zip' requires a url or context path")
	}
	return nil
}

// validateScale checks scale config bounds are sensible.
func validateScale(s ScaleConfig) error {
	if s.Instances < 0 {
		return fmt.Errorf("shipfile: scale.instances cannot be negative")
	}
	if s.Autoscale.Enabled {
		if s.Autoscale.Min < 1 {
			return fmt.Errorf("shipfile: scale.autoscale.min must be at least 1")
		}
		if s.Autoscale.Max < s.Autoscale.Min {
			return fmt.Errorf("shipfile: scale.autoscale.max (%d) must be >= min (%d)", s.Autoscale.Max, s.Autoscale.Min)
		}
		if s.Autoscale.TargetCPU < 1 || s.Autoscale.TargetCPU > 100 {
			return fmt.Errorf("shipfile: scale.autoscale.targetCPU must be between 1 and 100")
		}
		if s.Autoscale.TargetMemory < 1 || s.Autoscale.TargetMemory > 100 {
			return fmt.Errorf("shipfile: scale.autoscale.targetMemory must be between 1 and 100")
		}
	}
	if s.LoadBalancer.Enabled {
		valid := map[LBStrategy]bool{
			LBRoundRobin: true,
			LBLeastConn:  true,
			LBIPHash:     true,
		}
		if s.LoadBalancer.Strategy != "" && !valid[s.LoadBalancer.Strategy] {
			return fmt.Errorf("shipfile: unknown load balancer strategy %q (supported: round-robin, least-conn, ip-hash)", s.LoadBalancer.Strategy)
		}
	}
	return nil
}

// validateMode checks that a single mode config is valid for the given engine type.
func validateMode(serviceName, modeName string, mode Mode, engine EngineType) error {
	prefix := fmt.Sprintf("shipfile: service %q mode %q", serviceName, modeName)

	// Determine effective engine — if unset, infer from build config.
	effectiveEngine := engine
	if effectiveEngine == "" {
		switch {
		case mode.Build.ComposeFile != "":
			effectiveEngine = EngineCompose
		case mode.Build.ManifestDir != "":
			effectiveEngine = EngineKubernetes
		case mode.Build.NomadJob != "":
			effectiveEngine = EngineNomad
		default:
			effectiveEngine = EngineDocker
		}
	}

	// Engine-specific build config requirements.
	switch effectiveEngine {
	case EngineCompose:
		// compose file is optional — defaults to docker-compose.yml
	case EngineKubernetes, EngineK3s:
		// manifest dir is optional — defaults to "."
	case EngineNomad:
		// nomad job file is optional
	default:
		// docker / podman — dockerfile required only if no compose file present
		if mode.Build.Dockerfile == "" && mode.Build.ComposeFile == "" {
			return fmt.Errorf("%s: missing required field: build.dockerfile", prefix)
		}
	}

	for i, port := range mode.Runtime.Ports {
		if port.Name == "" {
			return fmt.Errorf("%s: port at index %d is missing a name", prefix, i)
		}
		if port.Internal == 0 {
			return fmt.Errorf("%s: port %q is missing internal port number", prefix, port.Name)
		}
		if port.External == "" {
			return fmt.Errorf("%s: port %q is missing external value (use a number or 'auto')", prefix, port.Name)
		}
	}

	for i, vol := range mode.Runtime.Volumes {
		if vol.Type != "bind" && vol.Type != "volume" {
			return fmt.Errorf("%s: volume at index %d has invalid type %q (must be 'bind' or 'volume')", prefix, i, vol.Type)
		}
		if vol.From == "" {
			return fmt.Errorf("%s: volume at index %d is missing 'from'", prefix, i)
		}
		if vol.To == "" {
			return fmt.Errorf("%s: volume at index %d is missing 'to'", prefix, i)
		}
	}

	if mode.Runtime.Health.Path != "" && mode.Runtime.Health.Port == "" {
		return fmt.Errorf("%s: health check has a path but is missing a port reference", prefix)
	}

	return nil
}

// HasMode returns true if the shipfile defines the given mode name.
func (sf *Shipfile) HasMode(mode string) bool {
	_, ok := sf.Service.Modes[mode]
	return ok
}

// GetMode returns the Mode config for the given mode name.
func (sf *Shipfile) GetMode(mode string) (Mode, error) {
	m, ok := sf.Service.Modes[mode]
	if !ok {
		return Mode{}, fmt.Errorf("shipfile: service %q does not have a mode %q", sf.Service.Name, mode)
	}
	return m, nil
}