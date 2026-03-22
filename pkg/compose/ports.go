package compose

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// PortMapping holds one port mapping from a compose service.
type PortMapping struct {
	ServiceName   string
	ContainerPort int
	Protocol      string // "tcp" or "udp"
	OriginalHost  string // what was in the compose file
	AssignedHost  int    // the dynamic port we allocated
}

// OverrideResult holds the generated override file content and resolved ports.
type OverrideResult struct {
	// OverrideFilePath is the path to the generated override file.
	OverrideFilePath string
	// Ports maps "service/containerPort" → assignedHostPort.
	Ports map[string]int
}

// GenerateDynamicPortOverride reads a docker-compose file, replaces ALL
// hardcoded host port bindings with free OS ports, and writes a fully
// merged temporary compose file. This avoids compose override caching issues.
func GenerateDynamicPortOverride(composeFilePath, contextDir string) (*OverrideResult, error) {
	data, err := os.ReadFile(composeFilePath)
	if err != nil {
		return nil, fmt.Errorf("compose: cannot read %s: %w", composeFilePath, err)
	}

	// Parse into a generic map so we preserve all fields.
	var cf map[string]interface{}
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("compose: cannot parse %s: %w", composeFilePath, err)
	}

	result := &OverrideResult{Ports: make(map[string]int)}

	services, ok := cf["services"].(map[string]interface{})
	if !ok {
		return result, nil
	}

	for svcName, svcRaw := range services {
		svc, ok := svcRaw.(map[string]interface{})
		if !ok {
			continue
		}

		ports, ok := svc["ports"].([]interface{})
		if !ok || len(ports) == 0 {
			continue
		}

		var newPorts []interface{}
		for _, p := range ports {
			mapping, err := parsePortEntry(p)
			if err != nil {
				// Keep original entry if we can't parse it.
				newPorts = append(newPorts, p)
				continue
			}

			freePort, err := getFreePort()
			if err != nil {
				return nil, fmt.Errorf("compose: no free port for %s:%d: %w", svcName, mapping.ContainerPort, err)
			}

			mapping.AssignedHost = freePort
			key := fmt.Sprintf("%s/%d", svcName, mapping.ContainerPort)
			result.Ports[key] = freePort

			// Write the new binding as "hostPort:containerPort/proto".
			proto := mapping.Protocol
			if proto == "" || proto == "tcp" {
				newPorts = append(newPorts, fmt.Sprintf("0.0.0.0:%d:%d", freePort, mapping.ContainerPort))
			} else {
				newPorts = append(newPorts, fmt.Sprintf("0.0.0.0:%d:%d/%s", freePort, mapping.ContainerPort, proto))
			}
		}

		svc["ports"] = newPorts
		services[svcName] = svc
	}

	cf["services"] = services

	// Marshal the fully merged compose file.
	mergedData, err := yaml.Marshal(cf)
	if err != nil {
		return nil, fmt.Errorf("compose: failed to marshal merged compose: %w", err)
	}

	// Write as a separate file — never overwrite the original.
	mergedPath := filepath.Join(contextDir, ".shipyard-compose.yml")
	if err := os.WriteFile(mergedPath, mergedData, 0644); err != nil {
		return nil, fmt.Errorf("compose: failed to write merged compose file: %w", err)
	}

	result.OverrideFilePath = mergedPath
	return result, nil
}

// CleanOverride removes the generated merged compose file if it exists.
func CleanOverride(contextDir string) {
	os.Remove(filepath.Join(contextDir, ".shipyard-compose.yml"))
	os.Remove(filepath.Join(contextDir, "docker-compose.shipyard-override.yml"))
}

// ── Internal types ────────────────────────────────────────────────────────────

// parsePortEntry handles short string, long map, and bare integer port entries.
// Compose files use three formats:
//   string:  "8080:80" or "8080:80/tcp"
//   integer: 8080  (container port only, no host binding)
//   map:     {target: 80, published: 8080, protocol: tcp}
func parsePortEntry(entry interface{}) (*PortMapping, error) {
	switch v := entry.(type) {
	case string:
		return parseShortPort(v)
	case int:
		// Bare integer — just a container port with no host binding.
		// We still allocate a host port so it's accessible.
		return &PortMapping{ContainerPort: v, Protocol: "tcp"}, nil
	case map[string]interface{}:
		return parseLongPort(v)
	default:
		// Try converting to string as a last resort.
		s := fmt.Sprintf("%v", v)
		return parseShortPort(s)
	}
}

// parseShortPort parses strings like:
//   "8080:80", "8080:80/tcp", "0.0.0.0:8080:80", "80" (no host binding)
func parseShortPort(s string) (*PortMapping, error) {
	proto := "tcp"
	if idx := strings.LastIndex(s, "/"); idx != -1 {
		proto = s[idx+1:]
		s = s[:idx]
	}

	parts := strings.Split(s, ":")
	var containerStr string

	switch len(parts) {
	case 1:
		// Just a container port — no host binding to override.
		containerStr = parts[0]
	case 2:
		// host:container
		containerStr = parts[1]
	case 3:
		// ip:host:container
		containerStr = parts[2]
	default:
		return nil, fmt.Errorf("cannot parse port %q", s)
	}

	containerPort, err := strconv.Atoi(containerStr)
	if err != nil {
		return nil, fmt.Errorf("cannot parse container port %q", containerStr)
	}

	return &PortMapping{
		ContainerPort: containerPort,
		Protocol:      proto,
	}, nil
}

// parseLongPort handles the long-form compose port spec:
//
//	ports:
//	  - target: 80
//	    published: 8080
//	    protocol: tcp
func parseLongPort(m map[string]interface{}) (*PortMapping, error) {
	target, _ := m["target"].(int)
	proto, _ := m["protocol"].(string)
	if proto == "" {
		proto = "tcp"
	}
	if target == 0 {
		return nil, fmt.Errorf("long port entry missing target")
	}
	return &PortMapping{
		ContainerPort: target,
		Protocol:      proto,
	}, nil
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}