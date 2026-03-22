package scaler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// InstanceStats holds a single sample of resource usage for a container.
type InstanceStats struct {
	ContainerID string
	CPUPercent  float64
	MemPercent  float64
	MemUsageMB  float64
	SampledAt   time.Time
}

// StatsCollector polls Docker container stats.
type StatsCollector struct {
	docker *client.Client
}

// NewStatsCollector creates a StatsCollector connected to the local Docker daemon.
func NewStatsCollector() (*StatsCollector, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("scaler: failed to connect to Docker: %w", err)
	}
	return &StatsCollector{docker: cli}, nil
}

// Sample fetches a single stats snapshot for a container.
// Docker stats are streamed — we read one frame and close.
func (sc *StatsCollector) Sample(ctx context.Context, containerID string) (*InstanceStats, error) {
	resp, err := sc.docker.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, fmt.Errorf("scaler: stats for %q failed: %w", containerID, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("scaler: reading stats for %q: %w", containerID, err)
	}

	var raw container.StatsResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("scaler: parsing stats for %q: %w", containerID, err)
	}

	return &InstanceStats{
		ContainerID: containerID,
		CPUPercent:  calcCPUPercent(&raw),
		MemPercent:  calcMemPercent(&raw),
		MemUsageMB:  float64(raw.MemoryStats.Usage) / 1024 / 1024,
		SampledAt:   time.Now(),
	}, nil
}

// SampleAll samples stats for multiple containers and returns averages.
// Returns avg CPU%, avg mem%, and the individual samples.
func (sc *StatsCollector) SampleAll(ctx context.Context, containerIDs []string) (avgCPU, avgMem float64, samples []*InstanceStats) {
	if len(containerIDs) == 0 {
		return 0, 0, nil
	}

	for _, id := range containerIDs {
		s, err := sc.Sample(ctx, id)
		if err != nil {
			continue // skip unhealthy containers
		}
		samples = append(samples, s)
		avgCPU += s.CPUPercent
		avgMem += s.MemPercent
	}

	if len(samples) > 0 {
		avgCPU /= float64(len(samples))
		avgMem /= float64(len(samples))
	}

	return avgCPU, avgMem, samples
}

// calcCPUPercent computes CPU usage percentage from a Docker stats response.
// Docker gives raw CPU ticks — we convert to a percentage of total system CPU.
func calcCPUPercent(stats *container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) -
		float64(stats.PreCPUStats.CPUUsage.TotalUsage)

	systemDelta := float64(stats.CPUStats.SystemUsage) -
		float64(stats.PreCPUStats.SystemUsage)

	numCPUs := float64(stats.CPUStats.OnlineCPUs)
	if numCPUs == 0 {
		numCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if numCPUs == 0 {
		numCPUs = 1
	}

	if systemDelta == 0 {
		return 0
	}

	return (cpuDelta / systemDelta) * numCPUs * 100.0
}

// calcMemPercent computes memory usage as a percentage of the container's limit.
func calcMemPercent(stats *container.StatsResponse) float64 {
	limit := float64(stats.MemoryStats.Limit)
	if limit == 0 {
		return 0
	}
	return float64(stats.MemoryStats.Usage) / limit * 100.0
}
