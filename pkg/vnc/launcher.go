package vnc

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	// noVNCImage is the Docker image used for the noVNC + websockify sidecar.
	// When VNC_SERVER is set, theasp/novnc runs only websockify (no Xvfb/x11vnc).
	noVNCImage = "theasp/novnc:latest"

	// noVNCPort is the port noVNC + websockify listens on inside the sidecar.
	noVNCPort = 8080
)

// Instance holds the runtime details of a running noVNC sidecar container.
type Instance struct {
	ServiceName   string `json:"serviceName"`
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	HostPort      int    `json:"hostPort"`
	// URL is the direct localhost address to open in the browser / iframe.
	URL string `json:"url"`
}

// Launcher manages noVNC sidecar containers.
type Launcher struct {
	docker *client.Client
}

// NewLauncher creates a Launcher connected to the local Docker daemon.
func NewLauncher() (*Launcher, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("vnc: failed to connect to Docker: %w", err)
	}
	return &Launcher{docker: cli}, nil
}

// Launch pulls the noVNC image (if needed) and starts a sidecar container that
// proxies the VNC server running inside mainContainerName via websockify.
// The sidecar joins the same Docker network as the main container.
// vncPort is the VNC port on the main container (default 5900).
func (l *Launcher) Launch(ctx context.Context, serviceName, mode, networkName, mainContainerName string, vncPort int) (*Instance, error) {
	if vncPort == 0 {
		vncPort = 5900
	}

	if err := l.ensureImage(ctx); err != nil {
		return nil, fmt.Errorf("vnc: could not pull noVNC image: %w", err)
	}

	hostPort, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("vnc: no free port available: %w", err)
	}

	containerName := SidecarName(serviceName, mode)
	l.removeStale(ctx, containerName)

	cp, _ := nat.NewPort("tcp", strconv.Itoa(noVNCPort))

	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			cp: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)}},
		},
	}

	// Resolve the VNC server address.
	// On a user-defined network, container names resolve via Docker DNS.
	// On the default bridge (standalone deploy, no stackName), only IPs work —
	// so we inspect the main container and use its bridge IP directly.
	vncHost := mainContainerName
	if networkName == "" {
		if ip, err := l.containerIP(ctx, mainContainerName); err == nil && ip != "" {
			vncHost = ip
		}
	}
	vncServer := fmt.Sprintf("%s:%d", vncHost, vncPort)

	containerCfg := &container.Config{
		Image:        noVNCImage,
		ExposedPorts: nat.PortSet{cp: struct{}{}},
		Env: []string{
			fmt.Sprintf("VNC_SERVER=%s", vncServer),
			"DISPLAY_WIDTH=1280",
			"DISPLAY_HEIGHT=720",
		},
	}

	var netCfg *network.NetworkingConfig
	if networkName != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		}
	}

	resp, err := l.docker.ContainerCreate(ctx, containerCfg, hostCfg, netCfg, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("vnc: failed to create noVNC container: %w", err)
	}

	if err := l.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("vnc: failed to start noVNC container: %w", err)
	}

	return &Instance{
		ServiceName:   serviceName,
		ContainerID:   resp.ID,
		ContainerName: containerName,
		HostPort:      hostPort,
		URL:           fmt.Sprintf("http://localhost:%d", hostPort),
	}, nil
}

// Stop stops and removes the noVNC sidecar for a service.
func (l *Launcher) Stop(ctx context.Context, serviceName, mode string) error {
	return l.removeStale(ctx, SidecarName(serviceName, mode))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// SidecarName returns the Docker container name for a noVNC sidecar.
func SidecarName(serviceName, mode string) string {
	name := strings.ToLower(serviceName)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return fmt.Sprintf("shipyard_%s_%s_vnc", name, mode)
}

func (l *Launcher) ensureImage(ctx context.Context) error {
	f := filters.NewArgs()
	f.Add("reference", noVNCImage)
	images, err := l.docker.ImageList(ctx, imagetypes.ListOptions{Filters: f})
	if err != nil {
		return err
	}
	if len(images) > 0 {
		return nil
	}
	reader, err := l.docker.ImagePull(ctx, noVNCImage, imagetypes.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	io.ReadAll(reader)
	return nil
}

func (l *Launcher) removeStale(ctx context.Context, name string) error {
	_ = l.docker.ContainerStop(ctx, name, container.StopOptions{})
	_ = l.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	return nil
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// containerIP returns the IP address of a container on any attached network.
// Used for standalone (non-stacked) deploys where Docker DNS is not available.
func (l *Launcher) containerIP(ctx context.Context, containerName string) (string, error) {
	info, err := l.docker.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", err
	}
	// Prefer the default bridge IP, then fall back to any network.
	if bridge, ok := info.NetworkSettings.Networks["bridge"]; ok && bridge.IPAddress != "" {
		return bridge.IPAddress, nil
	}
	for _, n := range info.NetworkSettings.Networks {
		if n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	return "", fmt.Errorf("no IP found for container %q", containerName)
}
