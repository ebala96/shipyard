package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/shipyard/shipyard/pkg/datadir"
	shipper "github.com/shipyard/shipyard/pkg/github"
	"github.com/shipyard/shipyard/pkg/idemanager"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/progress"
	"github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

func execCmd(ctx context.Context, name string, args ...string) *osexec.Cmd {
	return osexec.CommandContext(ctx, name, args...)
}

// ServiceRecord holds a registered service in the in-memory registry.
type ServiceRecord struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ContextDir  string   `json:"contextDir"`
	Modes       []string `json:"modes"`
	Source      string   `json:"source"`  // "github" | "zip" | "local"
	RepoURL     string   `json:"repoURL"` // GitHub URL if source=github
	Branch      string   `json:"branch"`
	Engine      string   `json:"engine"`
}

// OnboardGithubRequest is the JSON body for GitHub URL onboarding.
type OnboardGithubRequest struct {
	URL    string `json:"url" binding:"required"`
	Branch string `json:"branch"`
	Subdir string `json:"subdir"` // optional subfolder inside repo
}

// ServiceHandler handles service onboarding and registry operations.
type ServiceHandler struct {
	orch      *orchestrator.Orchestrator
	mu        sync.RWMutex
	registry  map[string]*ServiceRecord
	progReg   *progress.Registry
	// Optional Phase 1 additions — nil if etcd/NATS not available.
	store     *store.Store
	bus       *telemetry.Bus
}

// NewServiceHandler creates a ServiceHandler and loads any previously
// onboarded services from ~/.shipyard/services/*/record.json.
func NewServiceHandler(orch *orchestrator.Orchestrator, st *store.Store, bus *telemetry.Bus) *ServiceHandler {
	h := &ServiceHandler{
		orch:     orch,
		registry: make(map[string]*ServiceRecord),
		progReg:  progress.NewRegistry(),
		store:    st,
		bus:      bus,
	}
	h.loadAllRecords()
	return h
}

// OnboardGithub handles POST /api/v1/services/github
// Starts an async onboard and returns a session ID for SSE progress tracking.
func (h *ServiceHandler) OnboardGithub(c *gin.Context) {
	var req OnboardGithubRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid request: %v", err)))
		return
	}

	repoSlug := repoNameFromURL(req.URL)
	sessionID := progress.GenerateID(repoSlug)

	ctx, cancel := context.WithCancel(context.Background())
	tracker := progress.NewTracker(cancel)
	h.progReg.Register(sessionID, tracker)

	go h.runOnboardGithub(ctx, tracker, sessionID, repoSlug, req)

	c.JSON(http.StatusAccepted, gin.H{
		"sessionID": sessionID,
		"message":   "onboarding started",
	})
}

// runOnboardGithub does the actual work and reports progress via the tracker.
func (h *ServiceHandler) runOnboardGithub(ctx context.Context, tracker *progress.Tracker, sessionID, repoSlug string, req OnboardGithubRequest) {
	defer h.progReg.Remove(sessionID)

	tracker.Step("prepare", "Preparing workspace", "running", repoSlug)
	if err := datadir.EnsureServiceDir(repoSlug); err != nil {
		tracker.Error(fmt.Errorf("failed to create service directory: %v", err))
		return
	}
	sourceDir := datadir.ServiceSourceDir(repoSlug)
	os.RemoveAll(sourceDir)
	tracker.Step("prepare", "Preparing workspace", "done", "")

	if tracker.IsCancelled() { return }

	tracker.Step("clone", "Cloning repository", "running", req.URL)
	var result *shipper.CloneResult
	var err error
	if req.Subdir != "" {
		result, err = shipper.CloneSubdir(req.URL, req.Branch, req.Subdir, sourceDir)
	} else {
		result, err = shipper.Clone(req.URL, req.Branch, sourceDir)
	}
	if err != nil {
		os.RemoveAll(sourceDir)
		tracker.Error(fmt.Errorf("failed to clone repo: %v", err))
		return
	}
	if tracker.IsCancelled() { os.RemoveAll(sourceDir); return }
	tracker.Step("clone", "Cloning repository", "done", fmt.Sprintf("engine: %s", result.DetectedEngine))

	tracker.Step("shipfile", "Generating shipfile", "running", "")
	var sf *shipfile.Shipfile
	if result.ShipfileFound {
		sf, err = shipfile.Parse(result.ShipfilePath)
		if err != nil { os.RemoveAll(sourceDir); tracker.Error(fmt.Errorf("invalid shipfile.yml: %v", err)); return }
		tracker.Step("shipfile", "Generating shipfile", "done", "found shipfile.yml in repo")
	} else {
		sf, err = generateShipfile(result)
		if err != nil { os.RemoveAll(sourceDir); tracker.Error(fmt.Errorf("could not generate shipfile: %v", err)); return }
		if err := shipfile.SaveToDir(sf, datadir.ServiceDir(repoSlug)); err != nil {
			os.RemoveAll(sourceDir); tracker.Error(fmt.Errorf("failed to save shipfile: %v", err)); return
		}
		tracker.Step("shipfile", "Generating shipfile", "done", fmt.Sprintf("auto-generated for engine: %s", sf.Service.Engine.Type))
	}

	if tracker.IsCancelled() { os.RemoveAll(sourceDir); return }

	tracker.Step("register", "Registering service", "running", sf.Service.Name)
	record := buildRecord(sf, result.ContextDir, "github", req.URL, req.Branch)
	h.mu.Lock()
	h.registry[sf.Service.Name] = record
	h.mu.Unlock()
	h.saveRecord(record)
	tracker.Step("register", "Registering service", "done", "")

	summary := shipper.Summarise(result.ContextDir)
	tracker.Done(fmt.Sprintf(`{"service":%s,"detection":%s}`, mustJSON(record), mustJSON(summary)))
}

// OnboardProgress handles GET /api/v1/services/github/progress/:sessionID
func (h *ServiceHandler) OnboardProgress(c *gin.Context) {
	sessionID := c.Param("sessionID")
	tracker, ok := h.progReg.Get(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse("session not found or already complete"))
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, errorResponse("streaming not supported"))
		return
	}

	for _, step := range tracker.Steps() {
		s := step
		writeOnboardSSE(c, "step", &progress.Event{Type: "step", Step: &s})
		flusher.Flush()
	}

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-tracker.Events():
			if !ok { return }
			writeOnboardSSE(c, event.Type, event)
			flusher.Flush()
			if event.Type == "done" || event.Type == "error" || event.Type == "cancelled" {
				return
			}
		}
	}
}

// OnboardCancel handles DELETE /api/v1/services/github/progress/:sessionID
func (h *ServiceHandler) OnboardCancel(c *gin.Context) {
	sessionID := c.Param("sessionID")
	tracker, ok := h.progReg.Get(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse("session not found"))
		return
	}
	tracker.Cancel()
	h.progReg.Remove(sessionID)
	c.JSON(http.StatusOK, gin.H{"message": "onboarding cancelled"})
}

func writeOnboardSSE(c *gin.Context, eventType string, v interface{}) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, string(data))
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OnboardZip handles POST /api/v1/services/zip
// SECONDARY onboarding path — zip file upload.
func (h *ServiceHandler) OnboardZip(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("missing file upload — send a zip containing your service"))
		return
	}

	if filepath.Ext(file.Filename) != ".zip" {
		c.JSON(http.StatusBadRequest, errorResponse("file must be a .zip archive"))
		return
	}

	// Use a temp dir for the zip itself, extract into ~/.shipyard later.
	tmpDir, err := os.MkdirTemp("", "shipyard-zip-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse("failed to create temp directory"))
		return
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, file.Filename)
	if err := c.SaveUploadedFile(file, zipPath); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse("failed to save uploaded file"))
		return
	}

	// Extract to a temp location first so we can read the service name.
	tempExtract := filepath.Join(tmpDir, "extracted")
	if err := extractZip(zipPath, tempExtract); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("failed to extract zip: %v", err)))
		return
	}

	// Parse or generate shipfile from the temp location to get the service name.
	shipfilePath := filepath.Join(tempExtract, "shipfile.yml")
	var sf *shipfile.Shipfile

	if _, statErr := os.Stat(shipfilePath); statErr == nil {
		sf, err = shipfile.Parse(shipfilePath)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid shipfile.yml: %v", err)))
			return
		}
	} else {
		dockerfilePath := filepath.Join(tempExtract, "Dockerfile")
		if _, statErr := os.Stat(dockerfilePath); statErr != nil {
			c.JSON(http.StatusBadRequest, errorResponse("zip must contain at least a Dockerfile at the root"))
			return
		}
		sf, err = shipfile.GenerateFromDockerfile(dockerfilePath, tempExtract)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to generate shipfile: %v", err)))
			return
		}
	}

	// Now move everything into the proper data directory.
	if err := datadir.EnsureServiceDir(sf.Service.Name); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to create service directory: %v", err)))
		return
	}

	sourceDir := datadir.ServiceSourceDir(sf.Service.Name)
	os.RemoveAll(sourceDir) // clean previous version

	if err := os.Rename(tempExtract, sourceDir); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to move source to data directory: %v", err)))
		return
	}

	// Save shipfile to the service's data directory.
	if err := shipfile.SaveToDir(sf, datadir.ServiceDir(sf.Service.Name)); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to save shipfile: %v", err)))
		return
	}

	record := buildRecord(sf, sourceDir, "zip", "", "")

	h.mu.Lock()
	h.registry[sf.Service.Name] = record
	h.mu.Unlock()

	// Persist so the registry survives server restarts.
	h.saveRecord(record)

	c.JSON(http.StatusCreated, gin.H{
		"message": "service onboarded successfully",
		"service": record,
	})
}

// List handles GET /api/v1/services
func (h *ServiceHandler) List(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	services := make([]*ServiceRecord, 0, len(h.registry))
	for _, svc := range h.registry {
		services = append(services, svc)
	}

	c.JSON(http.StatusOK, gin.H{
		"services": services,
		"count":    len(services),
	})
}

// Get handles GET /api/v1/services/:name
func (h *ServiceHandler) Get(c *gin.Context) {
	name := c.Param("name")

	h.mu.RLock()
	record, ok := h.registry[name]
	h.mu.RUnlock()

	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	c.JSON(http.StatusOK, record)
}

// Delete handles DELETE /api/v1/services/:name
func (h *ServiceHandler) Delete(c *gin.Context) {
	name := c.Param("name")

	h.mu.Lock()
	record, ok := h.registry[name]
	if ok {
		delete(h.registry, name)
	}
	h.mu.Unlock()

	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	// Remove the persisted record file.
	os.Remove(recordPath(name))

	// Remove the entire service directory (source + shipfile).
	// Files written by containers run as root can't be deleted by the host user,
	// so we use a Docker container to force-remove them.
	serviceDir := datadir.ServiceDir(name)
	if err := os.RemoveAll(serviceDir); err != nil {
		// Fallback: use a Docker alpine container to rm -rf as root.
		fmt.Printf("services: standard remove failed for %q, trying Docker cleanup: %v\n", name, err)
		go forceRemoveDir(serviceDir)
	}

	// Remove any Docker images built by Shipyard for this service.
	// Images are tagged shipyard/<name>:<mode> e.g. shipyard/whoami:production
	go h.cleanDockerImages(name, record)

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("service %q removed", name)})
}

// cleanDockerImages removes all Docker images tagged shipyard/<name>:*
func (h *ServiceHandler) cleanDockerImages(name string, record *ServiceRecord) {
	ctx := context.Background()

	if record != nil && record.Engine == "compose" {
		sourceDir := record.ContextDir
		if sourceDir == "" {
			sourceDir = datadir.ServiceSourceDir(name)
		}
		composeFile := detectComposeFileInDir(sourceDir)
		for _, mode := range []string{"production", "dev"} {
			projectName := "shipyard_" + strings.ReplaceAll(name, "-", "_") + "_" + mode
			if composeFile != "" {
				out, err := runCmd(ctx, sourceDir, "docker", "compose",
					"--project-name", projectName,
					"--file", composeFile,
					"down", "--rmi", "all", "--volumes", "--remove-orphans")
				if err == nil {
					fmt.Printf("services: compose cleanup done for %s/%s\n", name, mode)
				} else {
					fmt.Printf("services: compose cleanup: %s\n", out)
				}
			}
		}
		return
	}

	cli, err := dockerClient()
	if err != nil {
		return
	}
	defer cli.Close()

	images, err := cli.ImageList(ctx, dockerImageListOptions())
	if err != nil {
		return
	}

	prefix := "shipyard/" + name + ":"
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, prefix) {
				_, err := cli.ImageRemove(ctx, img.ID, dockerImageRemoveOptions())
				if err != nil {
					fmt.Printf("services: failed to remove image %s: %v\n", tag, err)
				} else {
					fmt.Printf("services: removed image %s\n", tag)
				}
				break
			}
		}
	}
}

// ScanFiles handles GET /api/v1/services/:name/files
// Returns deployable files found in the service source directory.
func (h *ServiceHandler) ScanFiles(c *gin.Context) {
	name := c.Param("name")

	record, ok := h.GetRecord(name)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	sourceDir := record.ContextDir
	if sourceDir == "" {
		sourceDir = datadir.ServiceSourceDir(name)
	}

	files, err := idemanager.ScanDeployableFiles(sourceDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to scan files: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"service": name,
		"files":   files,
	})
}

// GetRecord returns a ServiceRecord by name — used internally by other handlers.
func (h *ServiceHandler) GetRecord(name string) (*ServiceRecord, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	record, ok := h.registry[name]
	return record, ok
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func buildRecord(sf *shipfile.Shipfile, contextDir, source, repoURL, branch string) *ServiceRecord {
	modes := make([]string, 0, len(sf.Service.Modes))
	for m := range sf.Service.Modes {
		modes = append(modes, m)
	}
	engine := string(sf.Service.Engine.Type)
	if engine == "" {
		engine = "docker"
	}
	return &ServiceRecord{
		Name:        sf.Service.Name,
		Description: sf.Service.Description,
		Tags:        sf.Service.Tags,
		ContextDir:  contextDir,
		Modes:       modes,
		Source:      source,
		RepoURL:     repoURL,
		Branch:      branch,
		Engine:      engine,
	}
}

func generateShipfile(result *shipper.CloneResult) (*shipfile.Shipfile, error) {
	dir := result.ContextDir

	// 1. Dockerfile at root
	for _, name := range []string{"Dockerfile", "Containerfile"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return shipfile.GenerateFromDockerfile(p, dir)
		}
	}

	// 2. compose.yaml / docker-compose.yml at root — generate a compose-engine shipfile
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return shipfile.GenerateFromCompose(p, dir)
		}
	}

	// 3. Dockerfile in common subdirectories (docker/, build/, deploy/, etc.)
	for _, sub := range []string{"docker", "build", "ci", ".docker", "deploy", "container", "containers"} {
		for _, name := range []string{"Dockerfile", "dockerfile", "Containerfile"} {
			p := filepath.Join(dir, sub, name)
			if _, err := os.Stat(p); err == nil {
				return shipfile.GenerateFromDockerfile(p, dir)
			}
		}
	}

	return nil, fmt.Errorf("no Dockerfile, docker-compose.yml, compose.yaml, or shipfile.yml found in repo root or common subdirectories (docker/, build/)")
}

// repoNameFromURL extracts the repo name from a GitHub URL.
// e.g. https://github.com/traefik/whoami → whoami
func repoNameFromURL(rawURL string) string {
	// Strip trailing slashes and .git suffix.
	u := strings.TrimRight(rawURL, "/")
	u = strings.TrimSuffix(u, ".git")
	parts := strings.Split(u, "/")
	if len(parts) == 0 {
		return "service"
	}
	name := parts[len(parts)-1]
	if name == "" {
		return "service"
	}
	return strings.ToLower(name)
}

// recordPath returns the path to the persisted record for a service.
func recordPath(name string) string {
	return filepath.Join(datadir.ServiceDir(name), "record.json")
}

// saveRecord writes a ServiceRecord to etcd (if available) and disk (always).
// Dual-write ensures backwards compatibility and graceful etcd-less operation.
func (h *ServiceHandler) saveRecord(record *ServiceRecord) {
	// Write to etcd if available.
	if h.store != nil {
		etcdRecord := &store.ServiceRecord{
			Name:        record.Name,
			Description: record.Description,
			Tags:        record.Tags,
			ContextDir:  record.ContextDir,
			Modes:       record.Modes,
			Source:      record.Source,
			RepoURL:     record.RepoURL,
			Branch:      record.Branch,
			Engine:      record.Engine,
		}
		if err := h.store.PutService(context.Background(), etcdRecord); err != nil {
			fmt.Printf("services: etcd write failed for %q (non-fatal): %v\n", record.Name, err)
		}
	}

	// Always write to disk as fallback.
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		fmt.Printf("services: failed to marshal record for %q: %v\n", record.Name, err)
		return
	}
	if err := os.WriteFile(recordPath(record.Name), data, 0644); err != nil {
		fmt.Printf("services: failed to save record for %q: %v\n", record.Name, err)
	}
}

// loadAllRecords scans ~/.shipyard/services/ and loads every record.json found.
// If no record.json exists but a shipfile.yml does, it reconstructs the record
// automatically so previously onboarded services don't need to be re-onboarded.
func (h *ServiceHandler) loadAllRecords() {
	// Load from etcd first if available.
	if h.store != nil {
		records, err := h.store.ListServices(context.Background())
		if err == nil && len(records) > 0 {
			for _, r := range records {
				if r.ContextDir != "" {
					if _, err := os.Stat(r.ContextDir); err != nil {
						fmt.Printf("services: source missing for %q (etcd) — skipping\n", r.Name)
						continue
					}
				}
				h.registry[r.Name] = &ServiceRecord{
					Name:        r.Name,
					Description: r.Description,
					Tags:        r.Tags,
					ContextDir:  r.ContextDir,
					Modes:       r.Modes,
					Source:      r.Source,
					RepoURL:     r.RepoURL,
					Branch:      r.Branch,
					Engine:      r.Engine,
				}
			}
			fmt.Printf("services: loaded %d service(s) from etcd\n", len(h.registry))
			return // etcd is the source of truth — skip disk scan
		}
	}

	// Fallback to disk scan (no etcd or etcd empty).
	servicesDir := filepath.Join(datadir.Root(), "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return // directory doesn't exist yet — fine on first run
	}

	loaded := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		serviceDir := filepath.Join(servicesDir, entry.Name())
		recordFile := filepath.Join(serviceDir, "record.json")

		// Try loading existing record.json first.
		if data, err := os.ReadFile(recordFile); err == nil {
			var record ServiceRecord
			if err := json.Unmarshal(data, &record); err == nil {
				if record.ContextDir != "" {
					if _, err := os.Stat(record.ContextDir); err != nil {
						fmt.Printf("services: source missing for %q — re-onboard to restore\n", record.Name)
						continue
					}
				}
				h.registry[record.Name] = &record
				loaded++
				continue
			}
		}

		// No record.json — try to reconstruct from shipfile.yml.
		shipfilePath := filepath.Join(serviceDir, "shipfile.yml")
		sourceDir := filepath.Join(serviceDir, "source")

		if _, err := os.Stat(shipfilePath); err != nil {
			continue // no shipfile either — skip
		}
		if _, err := os.Stat(sourceDir); err != nil {
			continue // source dir missing — skip
		}

		sf, err := shipfile.Parse(shipfilePath)
		if err != nil {
			fmt.Printf("services: could not parse shipfile for %q: %v\n", entry.Name(), err)
			continue
		}

		record := buildRecord(sf, sourceDir, "github", "", "")
		h.registry[record.Name] = record

		// Save the reconstructed record so we don't need to do this again.
		h.saveRecord(record)
		loaded++
		fmt.Printf("services: recovered %q from shipfile.yml\n", record.Name)
	}

	if loaded > 0 {
		fmt.Printf("services: loaded %d previously onboarded service(s)\n", loaded)
	}
}

// ── Docker helpers for cleanup ────────────────────────────────────────────────

func dockerClient() (*dockerclient.Client, error) {
	return dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
}

func dockerImageListOptions() dockerimage.ListOptions {
	return dockerimage.ListOptions{}
}

func dockerImageRemoveOptions() dockerimage.RemoveOptions {
	return dockerimage.RemoveOptions{Force: true, PruneChildren: true}
}

// forceRemoveDir removes a directory that may contain root-owned files by
// running a privileged Docker container to delete them.
func forceRemoveDir(dir string) {
	ctx := context.Background()
	cli, err := dockerClient()
	if err != nil {
		fmt.Printf("services: could not connect to Docker for cleanup of %s: %v\n", dir, err)
		return
	}
	defer cli.Close()

	// Run alpine rm -rf inside a container with the directory mounted.
	resp, err := cli.ContainerCreate(ctx,
		&dockercontainer.Config{
			Image: "alpine:latest",
			Cmd:   []string{"rm", "-rf", "/target"},
		},
		&dockercontainer.HostConfig{
			Binds:      []string{dir + ":/target"},
			AutoRemove: true,
		},
		nil, nil, "")
	if err != nil {
		// Try pulling alpine first then retry.
		reader, pullErr := cli.ImagePull(ctx, "alpine:latest", dockerimage.PullOptions{})
		if pullErr == nil {
			io.ReadAll(reader)
			reader.Close()
			resp, err = cli.ContainerCreate(ctx,
				&dockercontainer.Config{Image: "alpine:latest", Cmd: []string{"rm", "-rf", "/target"}},
				&dockercontainer.HostConfig{Binds: []string{dir + ":/target"}, AutoRemove: true},
				nil, nil, "")
		}
		if err != nil {
			fmt.Printf("services: Docker cleanup container create failed for %s: %v\n", dir, err)
			return
		}
	}

	if err := cli.ContainerStart(ctx, resp.ID, dockercontainer.StartOptions{}); err != nil {
		fmt.Printf("services: Docker cleanup container start failed for %s: %v\n", dir, err)
		return
	}

	// Wait for it to finish.
	cli.ContainerWait(ctx, resp.ID, dockercontainer.WaitConditionNotRunning)

	// Now remove the empty parent directory.
	os.Remove(dir)
	fmt.Printf("services: cleaned up directory %s\n", dir)
}

// detectComposeFileInDir returns the compose file name found in a directory.
func detectComposeFileInDir(dir string) string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml", ".shipyard-compose.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

// runCmd runs a shell command and returns combined output.
func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := execCmd(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
