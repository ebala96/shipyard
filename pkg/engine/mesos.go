package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// MesosRunner implements Runner using the Marathon REST API.
// Marathon is the long-running service framework for Apache Mesos.
type MesosRunner struct {
	marathonURL string // e.g. "http://marathon.mesos:8080"
	httpClient  *http.Client
}

// NewMesosRunner creates a MesosRunner.
func NewMesosRunner(marathonURL string) (*MesosRunner, error) {
	if marathonURL == "" {
		marathonURL = "http://localhost:8080"
	}
	return &MesosRunner{
		marathonURL: strings.TrimRight(marathonURL, "/"),
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (r *MesosRunner) EngineName() shipfile.EngineType { return "mesos" }

// Deploy creates or updates a Marathon app definition.
func (r *MesosRunner) Deploy(ctx context.Context, req DeployRequest) (*DeployedInstance, error) {
	appID := marathonAppID(req.StackName, req.ServiceName)

	// Build Marathon app definition.
	app := marathonApp{
		ID:        appID,
		Instances: 1,
	}

	if req.Resolved != nil {
		// Set image and ports from resolved mode.
		if req.Resolved.Build.Dockerfile != "" {
			app.Container = &marathonContainer{
				Type: "DOCKER",
				Docker: &marathonDocker{
					Image:          "shipyard/" + req.ServiceName + ":" + req.Mode,
					Network:        "BRIDGE",
					PortMappings:   buildMarathonPorts(req.Resolved),
					ForcePullImage: false,
				},
			}
		}

		// Environment variables.
		app.Env = req.Resolved.ResolvedEnv

		// Resource limits.
		if req.Resolved.Runtime.Resources.CPU > 0 {
			app.CPUs = req.Resolved.Runtime.Resources.CPU
		}
		if req.Resolved.Runtime.Resources.Memory != "" {
			app.Mem = parseMemoryMB(req.Resolved.Runtime.Resources.Memory)
		}
	}

	if app.CPUs == 0 {
		app.CPUs = 0.1
	}
	if app.Mem == 0 {
		app.Mem = 128
	}

	// Check if app already exists.
	existing := r.getApp(ctx, appID)
	var err error
	if existing != nil {
		// Update existing app.
		err = r.putApp(ctx, appID, app)
	} else {
		// Create new app.
		err = r.postApp(ctx, app)
	}
	if err != nil {
		return nil, fmt.Errorf("mesos: failed to deploy app %q: %w", appID, err)
	}

	fmt.Printf("mesos: deployed app %q to Marathon\n", appID)
	return &DeployedInstance{
		ID:          appID,
		Name:        appID,
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
		Engine:      "mesos",
	}, nil
}

func (r *MesosRunner) Stop(ctx context.Context, id string) error {
	// Scale to 0 instances — keeps the app definition.
	return r.scaleApp(ctx, id, 0)
}

func (r *MesosRunner) Start(ctx context.Context, id string) error {
	return r.scaleApp(ctx, id, 1)
}

func (r *MesosRunner) Restart(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/v2/apps/%s/restart", r.marathonURL, id)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mesos: restart failed for %q: %w", id, err)
	}
	resp.Body.Close()
	return nil
}

func (r *MesosRunner) Remove(ctx context.Context, id string, force bool) error {
	return r.deleteApp(ctx, id)
}

func (r *MesosRunner) Down(ctx context.Context, id string) error {
	// Down = scale to 0 (keeps definition, recoverable).
	return r.scaleApp(ctx, id, 0)
}

func (r *MesosRunner) Destroy(ctx context.Context, id string) error {
	// Destroy = delete app definition entirely.
	return r.deleteApp(ctx, id)
}

func (r *MesosRunner) Status(ctx context.Context, id string) (string, error) {
	app := r.getApp(ctx, id)
	if app == nil {
		return "unknown", nil
	}
	if app.Instances == 0 {
		return "stopped", nil
	}
	if app.TasksRunning > 0 {
		return "running", nil
	}
	return "deploying", nil
}

func (r *MesosRunner) Inspect(ctx context.Context, id string) (*ServiceDetail, error) {
	app := r.getApp(ctx, id)
	if app == nil {
		return nil, fmt.Errorf("mesos: app %q not found", id)
	}
	detail := &ServiceDetail{
		Name:     id,
		Platform: "mesos/marathon",
		Instances: []InstanceInfo{{
			ID:     id,
			Status: "running",
		}},
	}
	if app.Container != nil && app.Container.Docker != nil {
		detail.Image = app.Container.Docker.Image
	}
	return detail, nil
}

func (r *MesosRunner) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", fmt.Errorf("mesos: exec not supported via Marathon API")
}

func (r *MesosRunner) Logs(ctx context.Context, id string, tail string) (<-chan LogLine, <-chan error) {
	lines := make(chan LogLine)
	errs := make(chan error, 1)
	errs <- fmt.Errorf("mesos: log streaming not supported via Marathon API")
	close(lines)
	return lines, errs
}

func (r *MesosRunner) Diff(ctx context.Context, req DeployRequest, actualID string) (*DiffResult, error) {
	app := r.getApp(ctx, actualID)
	if app == nil {
		return &DiffResult{Action: "create"}, nil
	}
	return &DiffResult{Action: "update"}, nil
}

func (r *MesosRunner) Rollback(ctx context.Context, id string, req DeployRequest) (*DeployedInstance, error) {
	return r.Deploy(ctx, req)
}

// ── Marathon API helpers ──────────────────────────────────────────────────

type marathonApp struct {
	ID           string             `json:"id"`
	Instances    int                `json:"instances"`
	CPUs         float64            `json:"cpus"`
	Mem          float64            `json:"mem"`
	Container    *marathonContainer `json:"container,omitempty"`
	Env          map[string]string  `json:"env,omitempty"`
	TasksRunning int                `json:"tasksRunning,omitempty"`
}

type marathonContainer struct {
	Type   string           `json:"type"`
	Docker *marathonDocker  `json:"docker,omitempty"`
}

type marathonDocker struct {
	Image          string                  `json:"image"`
	Network        string                  `json:"network"`
	PortMappings   []marathonPortMapping   `json:"portMappings,omitempty"`
	ForcePullImage bool                    `json:"forcePullImage"`
}

type marathonPortMapping struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Protocol      string `json:"protocol"`
}

func (r *MesosRunner) getApp(ctx context.Context, id string) *marathonApp {
	url := fmt.Sprintf("%s/v2/apps/%s", r.marathonURL, id)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := r.httpClient.Do(req)
	if err != nil || resp.StatusCode == 404 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		App marathonApp `json:"app"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return &result.App
}

func (r *MesosRunner) postApp(ctx context.Context, app marathonApp) error {
	return r.doJSON(ctx, "POST", r.marathonURL+"/v2/apps", app)
}

func (r *MesosRunner) putApp(ctx context.Context, id string, app marathonApp) error {
	return r.doJSON(ctx, "PUT", fmt.Sprintf("%s/v2/apps/%s", r.marathonURL, id), app)
}

func (r *MesosRunner) deleteApp(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/v2/apps/%s", r.marathonURL, id)
	req, _ := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (r *MesosRunner) scaleApp(ctx context.Context, id string, instances int) error {
	return r.doJSON(ctx, "PATCH",
		fmt.Sprintf("%s/v2/apps/%s", r.marathonURL, id),
		map[string]int{"instances": instances},
	)
}

func (r *MesosRunner) doJSON(ctx context.Context, method, url string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("marathon API error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func marathonAppID(stackName, serviceName string) string {
	if stackName != "" {
		return "/" + stackName + "/" + serviceName
	}
	return "/" + serviceName
}

func buildMarathonPorts(resolved *shipfile.ResolvedMode) []marathonPortMapping {
	var mappings []marathonPortMapping
	for _, p := range resolved.Runtime.Ports {
		if p.Internal > 0 {
			hostPort := 0
			if rp, ok := resolved.ResolvedPorts[p.Name]; ok {
				hostPort = rp
			}
			mappings = append(mappings, marathonPortMapping{
				ContainerPort: p.Internal,
				HostPort:      hostPort,
				Protocol:      "tcp",
			})
		}
	}
	return mappings
}

func parseMemoryMB(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	var val float64
	fmt.Sscanf(s, "%f", &val)
	switch {
	case strings.HasSuffix(s, "g"):
		return val * 1024
	case strings.HasSuffix(s, "m"):
		return val
	case strings.HasSuffix(s, "k"):
		return val / 1024
	}
	return val
}
