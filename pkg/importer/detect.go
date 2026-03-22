package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// DetectAndGenerate detects the repo format and generates a shipfile.
// Priority: shipfile.yml → docker-compose → dockerfile → error
func DetectAndGenerate(repoDir, serviceName string) (*shipfile.Shipfile, error) {
	// 1. Existing shipfile.yml.
	if sf := tryLoadShipfile(repoDir); sf != nil {
		return sf, nil
	}

	// 2. Docker Compose.
	if composeFile := findComposeFile(repoDir); composeFile != "" {
		return generateFromCompose(composeFile, serviceName), nil
	}

	// 3. Dockerfile.
	if dockerfilePath := findDockerfile(repoDir); dockerfilePath != "" {
		return shipfile.GenerateFromDockerfile(dockerfilePath, repoDir)
	}

	return nil, fmt.Errorf("no Dockerfile, docker-compose.yml, or shipfile.yml found in %q", repoDir)
}

func tryLoadShipfile(dir string) *shipfile.Shipfile {
	for _, name := range []string{"shipfile.yml", "shipfile.yaml"} {
		p := filepath.Join(dir, name)
		if sf, err := shipfile.Parse(p); err == nil {
			return sf
		}
	}
	return nil
}

func findComposeFile(dir string) string {
	for _, name := range []string{
		"compose.yaml", "compose.yml",
		"docker-compose.yml", "docker-compose.yaml",
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Deep scan one level.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
			p := filepath.Join(dir, e.Name(), name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func findDockerfile(dir string) string {
	for _, name := range []string{"Dockerfile", "dockerfile"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Check common subdirs.
	for _, sub := range []string{"docker", "build", "deploy", "ci"} {
		for _, name := range []string{"Dockerfile", "dockerfile"} {
			p := filepath.Join(dir, sub, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func generateFromCompose(composeFile, serviceName string) *shipfile.Shipfile {
	name := serviceName
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(filepath.Dir(composeFile)), "/")
	}
	composeRel := filepath.Base(composeFile)
	return &shipfile.Shipfile{
		Service: shipfile.Service{
			Name:        name,
			Description: "Auto-generated config for " + name,
			Engine:      shipfile.EngineConfig{Type: shipfile.EngineCompose},
			Modes: map[string]shipfile.Mode{
				"production": {
					Build: shipfile.Build{ComposeFile: composeRel},
				},
			},
		},
	}
}
