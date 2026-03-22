package idemanager

import (
	"os"
	"path/filepath"
)

// DeployableFiles holds files found in a service source directory
// that are relevant to each platform.
type DeployableFiles struct {
	Dockerfiles  []string `json:"dockerfiles"`
	ComposeFiles []string `json:"composeFiles"`
	K8sDirs      []string `json:"k8sDirs"`
	NomadFiles   []string `json:"nomadFiles"`
	AutoDetected string   `json:"autoDetected"` // best guess platform
}

// ScanDeployableFiles scans a directory and returns files usable for deployment.
func ScanDeployableFiles(dir string) (*DeployableFiles, error) {
	result := &DeployableFiles{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		name := e.Name()
		fullPath := filepath.Join(dir, name)

		if e.IsDir() {
			// Check for k8s manifest directories.
			for _, k8sDir := range []string{"k8s", "kubernetes", "manifests", "deploy", "helm"} {
				if name == k8sDir {
					result.K8sDirs = append(result.K8sDirs, name)
				}
			}
			continue
		}

		// Dockerfiles.
		if name == "Dockerfile" || name == "Containerfile" ||
			startsWith(name, "Dockerfile.") || startsWith(name, "dockerfile") {
			result.Dockerfiles = append(result.Dockerfiles, name)
		}

		// Compose files.
		if name == "docker-compose.yml" || name == "docker-compose.yaml" ||
			name == "compose.yml" || name == "compose.yaml" {
			result.ComposeFiles = append(result.ComposeFiles, name)
		}

		// Nomad job files.
		if hasSuffix(name, ".nomad") || hasSuffix(name, ".nomad.hcl") {
			result.NomadFiles = append(result.NomadFiles, name)
		}

		_ = fullPath
	}

	// Auto-detect the best platform.
	result.AutoDetected = autoDetect(result)

	return result, nil
}

func autoDetect(f *DeployableFiles) string {
	if len(f.ComposeFiles) > 0 {
		return "compose"
	}
	if len(f.K8sDirs) > 0 {
		return "kubernetes"
	}
	if len(f.NomadFiles) > 0 {
		return "nomad"
	}
	if len(f.Dockerfiles) > 0 {
		return "docker"
	}
	return "docker"
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
