package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
)

// ContainerSummary is a JSON-serialisable snapshot of a running container.
type ContainerSummary struct {
	ContainerID   string         `json:"containerID"`
	ContainerName string         `json:"containerName"`
	ServiceName   string         `json:"serviceName"`
	Mode          string         `json:"mode"`
	Status        string         `json:"status"`
	Image         string         `json:"image"`
	Ports         map[string]int `json:"ports"`
}

// ListContainers handles GET /api/v1/containers
// Returns all containers that were started by Shipyard (name prefix shipyard_).
func ListContainers(c *gin.Context) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to connect to Docker: %v", err)))
		return
	}
	defer cli.Close()

	// Filter containers whose name starts with "shipyard_".
	filters := dockerfilters.NewArgs()
	filters.Add("name", "shipyard_")

	containers, err := cli.ContainerList(context.Background(), container.ListOptions{
		All:     true, // include stopped containers
		Filters: filters,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to list containers: %v", err)))
		return
	}

	result := make([]ContainerSummary, 0, len(containers))
	for _, ctr := range containers {
		name := ""
		if len(ctr.Names) > 0 {
			name = strings.TrimPrefix(ctr.Names[0], "/")
		}

		serviceName, mode := parseContainerName(name)
		ports := extractPorts(ctr.Ports)

		result = append(result, ContainerSummary{
			ContainerID:   ctr.ID,
			ContainerName: name,
			ServiceName:   serviceName,
			Mode:          mode,
			Status:        ctr.State, // "running", "exited", etc.
			Image:         ctr.Image,
			Ports:         ports,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"containers": result,
		"count":      len(result),
	})
}

// ContainerStats handles GET /api/v1/containers/stats
// Returns CPU and memory stats for all running shipyard containers.
func ContainerStats(c *gin.Context) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to connect to Docker: %v", err)))
		return
	}
	defer cli.Close()

	ctx := context.Background()
	filters := dockerfilters.NewArgs()
	filters.Add("name", "shipyard_")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     false, // only running
		Filters: filters,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to list containers: %v", err)))
		return
	}

	type StatResult struct {
		ContainerID   string  `json:"containerID"`
		ContainerName string  `json:"containerName"`
		ServiceName   string  `json:"serviceName"`
		CPUPercent    float64 `json:"cpuPercent"`
		MemUsageMB    float64 `json:"memUsageMB"`
		MemLimitMB    float64 `json:"memLimitMB"`
		MemPercent    float64 `json:"memPercent"`
		NetRxMB       float64 `json:"netRxMB"`
		NetTxMB       float64 `json:"netTxMB"`
	}

	results := make([]StatResult, 0, len(containers))

	for _, ctr := range containers {
		name := ""
		if len(ctr.Names) > 0 {
			name = strings.TrimPrefix(ctr.Names[0], "/")
		}

		resp, err := cli.ContainerStats(ctx, ctr.ID, false)
		if err != nil {
			continue
		}

		var raw container.StatsResponse
		if err := decodeJSON(resp.Body, &raw); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		cpuPercent := calcCPU(&raw)
		memUsage := float64(raw.MemoryStats.Usage) / 1024 / 1024
		memLimit := float64(raw.MemoryStats.Limit) / 1024 / 1024
		memPercent := 0.0
		if raw.MemoryStats.Limit > 0 {
			memPercent = float64(raw.MemoryStats.Usage) / float64(raw.MemoryStats.Limit) * 100
		}

		netRx, netTx := 0.0, 0.0
		for _, n := range raw.Networks {
			netRx += float64(n.RxBytes) / 1024 / 1024
			netTx += float64(n.TxBytes) / 1024 / 1024
		}

		serviceName, _ := parseContainerName(name)

		results = append(results, StatResult{
			ContainerID:   ctr.ID,
			ContainerName: name,
			ServiceName:   serviceName,
			CPUPercent:    cpuPercent,
			MemUsageMB:    memUsage,
			MemLimitMB:    memLimit,
			MemPercent:    memPercent,
			NetRxMB:       netRx,
			NetTxMB:       netTx,
		})
	}

	c.JSON(http.StatusOK, gin.H{"stats": results})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parseContainerName extracts service name and mode from "shipyard_whoami_production".
func parseContainerName(name string) (serviceName, mode string) {
	name = strings.TrimPrefix(name, "shipyard_")
	// Modes we look for at the end.
	for _, m := range []string{"production", "dev", "staging"} {
		if strings.HasSuffix(name, "_"+m) {
			return strings.TrimSuffix(name, "_"+m), m
		}
	}
	// IDE containers: shipyard_ide_whoami
	if strings.HasPrefix(name, "ide_") {
		return strings.TrimPrefix(name, "ide_"), "ide"
	}
	return name, "production"
}

// extractPorts converts Docker port bindings to a simple map.
func extractPorts(ports []types.Port) map[string]int {
	result := make(map[string]int)
	for _, p := range ports {
		if p.PublicPort > 0 {
			key := strconv.Itoa(int(p.PrivatePort))
			result[key] = int(p.PublicPort)
		}
	}
	return result
}

// calcCPU computes CPU percentage from a Docker stats response.
func calcCPU(stats *container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) -
		float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(stats.CPUStats.SystemUsage) -
		float64(stats.PreCPUStats.SystemUsage)
	numCPU := float64(stats.CPUStats.OnlineCPUs)
	if numCPU == 0 {
		numCPU = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if numCPU == 0 || sysDelta == 0 {
		return 0
	}
	return (cpuDelta / sysDelta) * numCPU * 100
}
