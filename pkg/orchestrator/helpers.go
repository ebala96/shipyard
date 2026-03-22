package orchestrator

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/filters"
	imagetypes "github.com/docker/docker/api/types/image"
)

// fileExists returns true if path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// getFreePort asks the OS for an available TCP port.
func getFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// dockerImageListOptions wraps filters into an ImageListOptions struct.
func dockerImageListOptions(f filters.Args) imagetypes.ListOptions {
	return imagetypes.ListOptions{Filters: f}
}

// dockerImagePullOptions returns default ImagePullOptions.
func dockerImagePullOptions() imagetypes.PullOptions {
	return imagetypes.PullOptions{}
}

// composeProjectName builds a stable docker compose project name including mode.
func composeProjectName(stackName, serviceName string) string {
	if stackName != "" {
		return sanitize(stackName + "_" + serviceName)
	}
	return sanitize("shipyard_" + serviceName)
}

// composeProjectNameWithMode includes the mode so dev and production don't conflict.
func composeProjectNameWithMode(stackName, serviceName, mode string) string {
	if stackName != "" {
		return sanitize(stackName + "_" + serviceName + "_" + mode)
	}
	return sanitize("shipyard_" + serviceName + "_" + mode)
}

// detectComposeFile finds the compose file in a directory.
func detectComposeFile(dir string) string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return "docker-compose.yml"
}

// runShellCommand runs a shell command in a directory and returns output.
func runShellCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// splitLines splits a string into non-empty trimmed lines.
func splitLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
