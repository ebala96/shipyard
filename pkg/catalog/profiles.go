// Package catalog manages the blueprint catalog — saved, reusable service
// configurations that can be instantiated with a single deploy.
package catalog

import "fmt"

// PowerProfile defines a resource allocation tier.
type PowerProfile string

const (
	ProfileEco         PowerProfile = "eco"         // minimal — dev/testing
	ProfileBalanced    PowerProfile = "balanced"     // default — small production
	ProfilePerformance PowerProfile = "performance"  // high-traffic services
	ProfileMax         PowerProfile = "max"          // resource-intensive workloads
)

// ProfileSpec holds the concrete resource numbers for a power profile.
type ProfileSpec struct {
	Name        PowerProfile `json:"name"`
	Label       string       `json:"label"`
	Description string       `json:"description"`
	CPUMillis   int64        `json:"cpuMillis"`
	MemoryMB    int64        `json:"memoryMB"`
	Replicas    int          `json:"replicas"`
	DiskGB      int          `json:"diskGB"`
}

// Profiles is the ordered list of all power profiles.
var Profiles = []ProfileSpec{
	{
		Name:        ProfileEco,
		Label:       "Eco",
		Description: "Minimal resources — ideal for development and testing",
		CPUMillis:   250,  // 0.25 core
		MemoryMB:    128,
		Replicas:    1,
		DiskGB:      1,
	},
	{
		Name:        ProfileBalanced,
		Label:       "Balanced",
		Description: "Standard allocation — suitable for most production services",
		CPUMillis:   500,  // 0.5 core
		MemoryMB:    512,
		Replicas:    1,
		DiskGB:      10,
	},
	{
		Name:        ProfilePerformance,
		Label:       "Performance",
		Description: "High-throughput — for services under significant load",
		CPUMillis:   1000, // 1 core
		MemoryMB:    1024,
		Replicas:    2,
		DiskGB:      50,
	},
	{
		Name:        ProfileMax,
		Label:       "Max",
		Description: "Maximum resources — for resource-intensive or critical workloads",
		CPUMillis:   2000, // 2 cores
		MemoryMB:    4096,
		Replicas:    3,
		DiskGB:      100,
	},
}

// GetProfile returns the spec for a named profile.
// Defaults to Balanced if not found.
func GetProfile(name PowerProfile) ProfileSpec {
	for _, p := range Profiles {
		if p.Name == name {
			return p
		}
	}
	return Profiles[1] // Balanced
}

// ApplyProfile merges a power profile's resource numbers into a resource map.
// Returns the CPU (as float64 cores) and memory string for use in a shipfile.
func ApplyProfile(profile PowerProfile) (cpu float64, memory string, replicas int) {
	spec := GetProfile(profile)
	cpu = float64(spec.CPUMillis) / 1000.0
	switch {
	case spec.MemoryMB >= 1024:
		memory = fmt.Sprintf("%dg", spec.MemoryMB/1024)
	default:
		memory = fmt.Sprintf("%dm", spec.MemoryMB)
	}
	replicas = spec.Replicas
	return
}