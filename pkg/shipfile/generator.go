package shipfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// GenerateFromDockerfile creates a minimal Shipfile by scanning a Dockerfile
// for EXPOSE, ENV, and ENTRYPOINT directives.
// Used when a user uploads a zip without a shipfile.yml.
func GenerateFromDockerfile(dockerfilePath, contextDir string) (*Shipfile, error) {
	file, err := os.Open(dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("generator: cannot open Dockerfile: %w", err)
	}
	defer file.Close()

	var exposedPorts []int
	var envVars []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch {
		case strings.HasPrefix(line, "EXPOSE"):
			ports := parseExposeDirective(line)
			exposedPorts = append(exposedPorts, ports...)

		case strings.HasPrefix(line, "ENV"):
			envVars = append(envVars, parseEnvDirective(line)...)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("generator: error reading Dockerfile: %w", err)
	}

	// Derive service name from the context directory.
	// If the context dir ends in "source" (our standard layout), use the
	// parent directory name instead — e.g. ~/.shipyard/services/whoami/source → whoami.
	serviceName := filepath.Base(contextDir)
	if serviceName == "source" || serviceName == "." || serviceName == "" {
		serviceName = filepath.Base(filepath.Dir(contextDir))
	}
	if serviceName == "." || serviceName == "" {
		serviceName = "service"
	}

	// Build port definitions — one named port per exposed port.
	ports := make([]Port, 0, len(exposedPorts))
	for i, p := range exposedPorts {
		name := "app"
		if i > 0 {
			name = fmt.Sprintf("port%d", i+1)
		}
		ports = append(ports, Port{
			Name:     name,
			Internal: p,
			External: "auto",
		})
	}

	// If no ports were found, add a default app port.
	if len(ports) == 0 {
		ports = append(ports, Port{
			Name:     "app",
			Internal: 8080,
			External: "auto",
		})
	}

	// Build env map from parsed ENV directives.
	envMap := make(map[string]string)
	for _, e := range envVars {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Always include APP_PORT pointing at the first port.
	envMap["APP_PORT"] = "${ports.app}"

	sf := &Shipfile{
		Version: "1",
		Service: Service{
			Name:        serviceName,
			Description: fmt.Sprintf("Auto-generated config for %s", serviceName),
			Tags:        []string{"auto-generated"},
			Source: Source{
				Context: ".",
			},
			Modes: map[string]Mode{
				"production": {
					Build: Build{
						Dockerfile: "Dockerfile",
					},
					Runtime: Runtime{
						Ports: ports,
						Env:   envMap,
						Health: HealthCheck{
							Path: "/healthz",
							Port: "app",
						},
					},
					IDE: IDE{Enabled: false},
				},
				"dev": {
					Build: Build{
						Dockerfile: "Dockerfile",
					},
					Runtime: Runtime{
						Ports: ports,
						Env:   envMap,
						Volumes: []Volume{
							{
								Type: "bind",
								From: ".",
								To:   "/app",
							},
						},
						Health: HealthCheck{
							Path: "/healthz",
							Port: "app",
						},
					},
					IDE: IDE{Enabled: true},
				},
			},
		},
	}

	return sf, nil
}

// SaveToDir serialises a Shipfile to shipfile.yml in the given directory.
// Used after auto-generation to persist the shipfile so deploy can read it.
func SaveToDir(sf *Shipfile, dir string) error {
	path := filepath.Join(dir, "shipfile.yml")

	data, err := marshalShipfile(sf)
	if err != nil {
		return fmt.Errorf("generator: failed to marshal shipfile: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("generator: failed to write shipfile to %q: %w", path, err)
	}

	return nil
}

// marshalShipfile converts a Shipfile struct back to YAML bytes.
func marshalShipfile(sf *Shipfile) ([]byte, error) {
	// Build a clean map representation for yaml output.
	out := map[string]interface{}{
		"version": sf.Version,
		"service": map[string]interface{}{
			"name":        sf.Service.Name,
			"description": sf.Service.Description,
			"tags":        sf.Service.Tags,
			"source": map[string]interface{}{
				"type":    string(sf.Service.Source.Type),
				"context": sf.Service.Source.Context,
			},
			"engine": map[string]interface{}{
				"type": string(sf.Service.Engine.Type),
			},
			"modes": buildModesMap(sf.Service.Modes),
			"scale": map[string]interface{}{
				"instances": sf.Service.Scale.Instances,
				"resources": map[string]interface{}{
					"cpu":    sf.Service.Scale.Resources.CPU,
					"memory": sf.Service.Scale.Resources.Memory,
				},
				"autoscale": map[string]interface{}{
					"enabled": sf.Service.Scale.Autoscale.Enabled,
					"min":     sf.Service.Scale.Autoscale.Min,
					"max":     sf.Service.Scale.Autoscale.Max,
				},
				"loadBalancer": map[string]interface{}{
					"enabled":  sf.Service.Scale.LoadBalancer.Enabled,
					"strategy": string(sf.Service.Scale.LoadBalancer.Strategy),
				},
			},
		},
	}

	return yaml.Marshal(out)
}

// buildModesMap converts the modes map to a serialisable form.
func buildModesMap(modes map[string]Mode) map[string]interface{} {
	out := make(map[string]interface{}, len(modes))
	for name, mode := range modes {
		ports := make([]map[string]interface{}, 0, len(mode.Runtime.Ports))
		for _, p := range mode.Runtime.Ports {
			ports = append(ports, map[string]interface{}{
				"name":     p.Name,
				"internal": p.Internal,
				"external": p.External,
			})
		}
		out[name] = map[string]interface{}{
			"build": map[string]interface{}{
				"dockerfile": mode.Build.Dockerfile,
			},
			"ide": map[string]interface{}{
				"enabled": mode.IDE.Enabled,
			},
			"runtime": map[string]interface{}{
				"ports": ports,
				"env":   mode.Runtime.Env,
				"health": map[string]interface{}{
					"path": mode.Runtime.Health.Path,
					"port": mode.Runtime.Health.Port,
				},
			},
		}
	}
	return out
}

// GenerateFromCompose creates a Shipfile from a docker-compose file.
// Used when a repo has no Dockerfile but has a compose file at root.
func GenerateFromCompose(composePath, contextDir string) (*Shipfile, error) {
	serviceName := filepath.Base(contextDir)
	if serviceName == "source" || serviceName == "." || serviceName == "" {
		serviceName = filepath.Base(filepath.Dir(contextDir))
	}
	if serviceName == "." || serviceName == "" {
		serviceName = "service"
	}

	composeFile := filepath.Base(composePath)

	return &Shipfile{
		Version: "1",
		Service: Service{
			Name:        serviceName,
			Description: fmt.Sprintf("Auto-generated config for %s (compose)", serviceName),
			Tags:        []string{"auto-generated", "compose"},
			Source:      Source{Type: SourceLocal, Context: "."},
			Engine:      EngineConfig{Type: EngineCompose},
			Modes: map[string]Mode{
				"production": {
					Build:   Build{ComposeFile: composeFile},
					Runtime: Runtime{Ports: []Port{{Name: "app", Internal: 3001, External: "auto"}}, Env: map[string]string{}},
					IDE:     IDE{Enabled: false},
				},
				"dev": {
					Build:   Build{ComposeFile: composeFile},
					Runtime: Runtime{Ports: []Port{{Name: "app", Internal: 3001, External: "auto"}}, Env: map[string]string{}},
					IDE:     IDE{Enabled: true},
				},
			},
			Scale: ScaleConfig{
				Instances: 1,
				Resources: Resources{CPU: 0.5, Memory: "256m"},
				Autoscale: AutoscaleConfig{Enabled: false, Min: 1, Max: 5},
			},
		},
	}, nil
}

// Handles: EXPOSE 3000, EXPOSE 3000/tcp, EXPOSE 3000 8080
func parseExposeDirective(line string) []int {
	parts := strings.Fields(line)
	var ports []int
	for _, part := range parts[1:] { // skip "EXPOSE"
		// Strip protocol suffix like /tcp or /udp
		portStr := strings.Split(part, "/")[0]
		p, err := strconv.Atoi(portStr)
		if err == nil && p > 0 && p <= 65535 {
			ports = append(ports, p)
		}
	}
	return ports
}

// parseEnvDirective extracts KEY=VALUE pairs from an ENV directive.
// Handles: ENV KEY=VALUE, ENV KEY VALUE, ENV KEY1=VAL1 KEY2=VAL2
func parseEnvDirective(line string) []string {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return nil
	}

	var result []string
	args := parts[1:] // skip "ENV"

	// Check if it's the legacy form: ENV KEY VALUE (no =)
	if len(args) == 2 && !strings.Contains(args[0], "=") {
		result = append(result, fmt.Sprintf("%s=%s", args[0], args[1]))
		return result
	}

	// Modern form: ENV KEY=VALUE ...
	for _, arg := range args {
		if strings.Contains(arg, "=") {
			result = append(result, arg)
		}
	}

	return result
}