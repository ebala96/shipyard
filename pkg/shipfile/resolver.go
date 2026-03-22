package shipfile

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Resolve takes a parsed Shipfile and a mode name, assigns real port numbers
// to any "auto" ports, and substitutes all ${variable} expressions in env vars.
// Returns a ResolvedMode ready to be handed to the orchestration engine.
func Resolve(sf *Shipfile, modeName string) (*ResolvedMode, error) {
	mode, err := sf.GetMode(modeName)
	if err != nil {
		return nil, err
	}

	resolved := &ResolvedMode{
		Mode:          mode,
		ResolvedPorts: make(map[string]int),
		ResolvedEnv:   make(map[string]string),
	}

	// Step 1: assign real port numbers to all ports in this mode.
	if err := resolvePorts(mode, resolved); err != nil {
		return nil, fmt.Errorf("resolver: %w", err)
	}

	// Step 2: substitute ${ports.name} and other variables in env map.
	if err := resolveEnv(mode, resolved); err != nil {
		return nil, fmt.Errorf("resolver: %w", err)
	}

	return resolved, nil
}

// resolvePorts iterates over all ports in the mode.
// "auto" ports get a free OS port assigned. Numeric strings are parsed directly.
func resolvePorts(mode Mode, resolved *ResolvedMode) error {
	for _, port := range mode.Runtime.Ports {
		var assignedPort int
		var err error

		if strings.ToLower(port.External) == "auto" {
			assignedPort, err = getFreePort()
			if err != nil {
				return fmt.Errorf("could not assign free port for %q: %w", port.Name, err)
			}
		} else {
			assignedPort, err = strconv.Atoi(port.External)
			if err != nil {
				return fmt.Errorf("port %q has invalid external value %q: must be a number or 'auto'", port.Name, port.External)
			}
		}

		resolved.ResolvedPorts[port.Name] = assignedPort
	}
	return nil
}

// resolveEnv iterates over the env map and substitutes ${ports.name} expressions.
// Supports: ${ports.<portName>} → the resolved port number as a string.
func resolveEnv(mode Mode, resolved *ResolvedMode) error {
	for key, value := range mode.Runtime.Env {
		substituted, err := substituteVariables(value, resolved.ResolvedPorts)
		if err != nil {
			return fmt.Errorf("env var %q: %w", key, err)
		}
		resolved.ResolvedEnv[key] = substituted
	}
	return nil
}

// substituteVariables replaces all ${ports.<name>} occurrences in a string.
// Example: "${ports.app}" → "3000" if port "app" resolved to 3000.
func substituteVariables(value string, ports map[string]int) (string, error) {
	result := value

	for strings.Contains(result, "${") {
		start := strings.Index(result, "${")
		end := strings.Index(result, "}")
		if end == -1 || end < start {
			return "", fmt.Errorf("unclosed variable expression in %q", value)
		}

		expr := result[start+2 : end] // e.g. "ports.app"
		replacement, err := evaluateExpression(expr, ports)
		if err != nil {
			return "", err
		}

		result = result[:start] + replacement + result[end+1:]
	}

	return result, nil
}

// evaluateExpression resolves a single variable expression like "ports.app".
func evaluateExpression(expr string, ports map[string]int) (string, error) {
	parts := strings.SplitN(expr, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unrecognised variable expression ${%s} — expected format: ${ports.<name>}", expr)
	}

	namespace := parts[0]
	key := parts[1]

	switch namespace {
	case "ports":
		port, ok := ports[key]
		if !ok {
			return "", fmt.Errorf("variable ${ports.%s} references unknown port name %q", key, key)
		}
		return strconv.Itoa(port), nil
	default:
		return "${"+expr+"}", nil // pass unknown namespaces through unchanged
	}
}

// getFreePort asks the OS for an available TCP port by binding to :0.
func getFreePort() (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, fmt.Errorf("could not find a free port: %w", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
}