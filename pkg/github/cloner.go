package github

import (
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// CloneResult holds the outcome of cloning a GitHub repository.
type CloneResult struct {
	// ContextDir is the local path where the repo was cloned.
	ContextDir string

	// DetectedEngine is the engine type detected from the repo contents.
	DetectedEngine shipfile.EngineType

	// ShipfileFound is true if a shipfile.yml was found in the repo.
	ShipfileFound bool

	// ShipfilePath is the absolute path to the shipfile.yml if found.
	ShipfilePath string

	// RepoURL is the original GitHub URL that was cloned.
	RepoURL string

	// Branch is the branch that was cloned.
	Branch string
}

// Clone clones a GitHub repository into destDir.
// If destDir is empty a temp directory is used (legacy behaviour).
// If branch is empty the default branch is used.
func Clone(repoURL, branch, destDir string) (*CloneResult, error) {
	var cloneDir string
	var err error

	if destDir != "" {
		if err = os.MkdirAll(destDir, 0755); err != nil {
			return nil, fmt.Errorf("github: failed to create dest dir %q: %w", destDir, err)
		}
		cloneDir = destDir
	} else {
		cloneDir, err = os.MkdirTemp("", "shipyard-clone-*")
		if err != nil {
			return nil, fmt.Errorf("github: failed to create temp dir: %w", err)
		}
	}

	opts := &gogit.CloneOptions{
		URL:          repoURL,
		SingleBranch: branch != "",
		Depth:        1,
		Progress:     nil,
	}

	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}

	_, err = gogit.PlainClone(cloneDir, false, opts)
	if err != nil {
		if destDir == "" {
			os.RemoveAll(cloneDir) // only clean up tmp dirs on failure
		}
		return nil, fmt.Errorf("github: failed to clone %q: %w", repoURL, err)
	}

	result := &CloneResult{
		ContextDir: cloneDir,
		RepoURL:    repoURL,
		Branch:     branch,
	}

	// Detect what kind of service this repo contains.
	result.DetectedEngine = detectEngine(cloneDir)

	// Check for an existing shipfile.yml.
	shipfilePath := filepath.Join(cloneDir, "shipfile.yml")
	if fileExists(shipfilePath) {
		result.ShipfileFound = true
		result.ShipfilePath = shipfilePath
	}

	return result, nil
}

// CloneSubdir clones a repo and returns the path to a specific subdirectory.
// Useful when a monorepo contains multiple services in subdirectories.
func CloneSubdir(repoURL, branch, subdir, destDir string) (*CloneResult, error) {
	result, err := Clone(repoURL, branch, destDir)
	if err != nil {
		return nil, err
	}

	if subdir != "" && subdir != "." {
		subdirPath := filepath.Join(result.ContextDir, subdir)
		if !fileExists(subdirPath) {
			return nil, fmt.Errorf("github: subdirectory %q not found in repo", subdir)
		}
		result.ContextDir = subdirPath
		// Re-detect engine from the subdirectory.
		result.DetectedEngine = detectEngine(subdirPath)

		// Check for shipfile.yml in the subdirectory.
		shipfilePath := filepath.Join(subdirPath, "shipfile.yml")
		if fileExists(shipfilePath) {
			result.ShipfileFound = true
			result.ShipfilePath = shipfilePath
		} else {
			result.ShipfileFound = false
			result.ShipfilePath = ""
		}
	}

	return result, nil
}

// detectEngine scans a directory and returns the most appropriate engine type.
// Detection order: shipfile.yml → docker-compose → kubernetes → nomad → dockerfile → podman
func detectEngine(dir string) shipfile.EngineType {
	// Check for docker-compose files.
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if fileExists(filepath.Join(dir, name)) {
			return shipfile.EngineCompose
		}
	}

	// Check for Kubernetes manifests directory.
	for _, d := range []string{"k8s", "kubernetes", "manifests"} {
		if dirExists(filepath.Join(dir, d)) {
			return shipfile.EngineKubernetes
		}
	}

	// Check for Nomad job files.
	matches, _ := filepath.Glob(filepath.Join(dir, "*.nomad"))
	if len(matches) > 0 {
		return shipfile.EngineNomad
	}
	matches, _ = filepath.Glob(filepath.Join(dir, "*.nomad.hcl"))
	if len(matches) > 0 {
		return shipfile.EngineNomad
	}

	// Default to Docker if a Dockerfile exists.
	if fileExists(filepath.Join(dir, "Dockerfile")) {
		return shipfile.EngineDocker
	}

	// Check for Podman-specific files (Containerfile).
	if fileExists(filepath.Join(dir, "Containerfile")) {
		return shipfile.EnginePodman
	}

	// Fallback — assume Docker even if no Dockerfile found yet.
	return shipfile.EngineDocker
}

// DetectionSummary returns a human-readable summary of what was found in a repo.
// Used to show the user what Shipyard detected before they confirm onboarding.
type DetectionSummary struct {
	Engine        shipfile.EngineType `json:"engine"`
	ShipfileFound bool                `json:"shipfileFound"`
	HasDockerfile bool                `json:"hasDockerfile"`
	HasCompose    bool                `json:"hasCompose"`
	HasK8s        bool                `json:"hasK8s"`
	HasNomad      bool                `json:"hasNomad"`
}

// Summarise returns a DetectionSummary for a cloned repo directory.
func Summarise(dir string) DetectionSummary {
	summary := DetectionSummary{
		Engine:        detectEngine(dir),
		ShipfileFound: fileExists(filepath.Join(dir, "shipfile.yml")),
		HasDockerfile: fileExists(filepath.Join(dir, "Dockerfile")) || fileExists(filepath.Join(dir, "Containerfile")),
	}

	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if fileExists(filepath.Join(dir, name)) {
			summary.HasCompose = true
			break
		}
	}

	for _, d := range []string{"k8s", "kubernetes", "manifests"} {
		if dirExists(filepath.Join(dir, d)) {
			summary.HasK8s = true
			break
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "*.nomad"))
	summary.HasNomad = len(matches) > 0

	return summary
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
