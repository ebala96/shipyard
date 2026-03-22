package datadir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Root returns the Shipyard data directory (~/.shipyard).
// Can be overridden with the SHIPYARD_DATA env var.
func Root() string {
	if env := os.Getenv("SHIPYARD_DATA"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".shipyard"
	}
	return filepath.Join(home, ".shipyard")
}

// ServiceDir returns the directory for a specific service.
// Structure: ~/.shipyard/services/<name>/
func ServiceDir(name string) string {
	return filepath.Join(Root(), "services", name)
}

// ServiceSourceDir returns where the cloned/extracted source lives.
// Structure: ~/.shipyard/services/<name>/source/
func ServiceSourceDir(name string) string {
	return filepath.Join(ServiceDir(name), "source")
}

// ServiceShipfilePath returns the path to a service's shipfile.yml.
// Structure: ~/.shipyard/services/<name>/shipfile.yml
func ServiceShipfilePath(name string) string {
	return filepath.Join(ServiceDir(name), "shipfile.yml")
}

// StacksDir returns the directory for stack definitions.
// Structure: ~/.shipyard/stacks/
func StacksDir() string {
	return filepath.Join(Root(), "stacks")
}

// EnsureServiceDir creates the full directory tree for a service.
// Safe to call multiple times — does nothing if it already exists.
func EnsureServiceDir(name string) error {
	dirs := []string{
		ServiceDir(name),
		ServiceSourceDir(name),
		StacksDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("datadir: failed to create %q: %w", d, err)
		}
	}
	return nil
}

// EnsureRoot creates the root Shipyard data directory tree.
func EnsureRoot() error {
	dirs := []string{
		Root(),
		filepath.Join(Root(), "services"),
		StacksDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("datadir: failed to create %q: %w", d, err)
		}
	}
	return nil
}
