package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/internal/api/handlers"
	"github.com/shipyard/shipyard/pkg/agent"
	"github.com/shipyard/shipyard/pkg/catalog"
	"github.com/shipyard/shipyard/pkg/datadir"
	"github.com/shipyard/shipyard/pkg/idemanager"
	"github.com/shipyard/shipyard/pkg/mcp"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/proxy"
	"github.com/shipyard/shipyard/pkg/relay"
	"github.com/shipyard/shipyard/pkg/scheduler"
	"github.com/shipyard/shipyard/pkg/shiplink"
	"github.com/shipyard/shipyard/pkg/statemachine"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
	"github.com/shipyard/shipyard/pkg/vnc"
)

// Server holds the HTTP server and all its dependencies.
type Server struct {
	httpServer  *http.Server
	orch        *orchestrator.Orchestrator
	proxy       *proxy.Proxy
	ideManager  *idemanager.Manager
	vncRegistry *vnc.Registry
	store       *store.Store
	bus         *telemetry.Bus
	scheduler   *scheduler.Scheduler
	cancelWatch context.CancelFunc
}

// New creates a fully wired Gin server with all routes registered.
func New(port int) (*Server, error) {
	if err := datadir.EnsureRoot(); err != nil {
		return nil, fmt.Errorf("api: failed to initialise data directory: %w", err)
	}
	log.Printf("Shipyard data directory: %s", datadir.Root())

	// ── etcd store ────────────────────────────────────────────────────────
	etcdEndpoints := []string{envOr("ETCD_ENDPOINTS", "localhost:2379")}
	st, err := store.New(etcdEndpoints)
	if err != nil {
		// etcd is optional for now — log and continue with file-based fallback.
		log.Printf("WARNING: etcd not available at %v (%v) — using file-based persistence", etcdEndpoints, err)
		st = nil
	} else {
		log.Printf("etcd connected at %v", etcdEndpoints)
	}

	// ── NATS telemetry ────────────────────────────────────────────────────
	natsURL := envOr("NATS_URL", "nats://localhost:4222")
	bus, err := telemetry.New(natsURL)
	if err != nil {
		// NATS is optional for now — log and continue without telemetry.
		log.Printf("WARNING: NATS not available at %q (%v) — telemetry disabled", natsURL, err)
		bus = nil
	} else {
		log.Printf("NATS connected at %q", natsURL)
	}

	// ── Core dependencies ─────────────────────────────────────────────────
	orch, err := orchestrator.New()
	if err != nil {
		return nil, fmt.Errorf("api: failed to initialise orchestrator: %w", err)
	}

	prx := proxy.New(proxy.DefaultProxyPort)
	if err := prx.Start(); err != nil {
		return nil, fmt.Errorf("api: failed to start proxy: %w", err)
	}

	ideMgr, err := idemanager.New()
	if err != nil {
		return nil, fmt.Errorf("api: failed to initialise IDE manager: %w", err)
	}

	// ── Reconciler watch loop (only if etcd is available) ─────────────────
	var cancelWatch context.CancelFunc
	if st != nil {
		watcher := store.NewWatcher(st, makeReconcileFunc(orch, st, bus), 30*time.Second)
		cancelWatch = watcher.Start(context.Background())
	}

	// ── Shiplink (service mesh) ───────────────────────────────────────────
	var shipRouter *shiplink.Router
	if st != nil {
		registry := shiplink.NewRegistry(st.Client())
		shipRouter = shiplink.NewRouter(registry)
		shipRouter.StartHealthChecker(context.Background(), 10*time.Second)

		// Auto-register all deployed containers into the registry.
		autoReg := shiplink.NewAutoRegistrar(registry, st)
		autoReg.Start(context.Background())

		log.Printf("shiplink: service mesh started")
	}
	var sched *scheduler.Scheduler
	if st != nil {
		sched = scheduler.New(st)
		log.Printf("scheduler: ready")
	}

	// ── Node agent (embedded) ─────────────────────────────────────────────
	// Registers this machine in etcd and sends live CPU/mem heartbeats
	// every 15s so the scheduler can see available resources.
	// Multi-node setups run cmd/shipyard-agent on each worker instead.
	if st != nil {
		localAgent := agent.New(st, bus)
		localAgent.Start(context.Background())
	}

	// ── HTTP handlers ─────────────────────────────────────────────────────
	router := gin.Default()
	router.Use(corsMiddleware())

	svcHandler := handlers.NewServiceHandler(orch, st, bus)
	deployHandler := handlers.NewDeployHandler(orch, st, bus, sched)
	deployHandler.SetServiceHandler(svcHandler)
	lifecycleHandler := handlers.NewLifecycleHandler(orch)
	logsHandler := handlers.NewLogsHandler(orch)
	ideHandler := handlers.NewIDEHandler(ideMgr, prx, svcHandler)
	manifestHandler := handlers.NewManifestHandler(svcHandler)

	vncRegistry := vnc.NewRegistry()
	var vncHandler *handlers.VNCHandler
	if vncLauncher, err := vnc.NewLauncher(); err == nil {
		vncHandler = handlers.NewVNCHandler(vncLauncher, vncRegistry, st, prx, svcHandler)
	} else {
		log.Printf("WARNING: VNC launcher unavailable (%v) — VNC endpoints disabled", err)
	}

	relayManager := relay.NewManager()
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	relayHandler := handlers.NewRelayHandler(relayManager, vncRegistry, baseURL)

	// Catalog handler (only if etcd available).
	var catalogHandler *handlers.CatalogHandler
	if st != nil {
		cat := catalog.New(st)
		catalogHandler = handlers.NewCatalogHandler(cat, svcHandler, orch, st, bus)
	}

	v1 := router.Group("/api/v1")
	{
		services := v1.Group("/services")
		{
			services.POST("/github", svcHandler.OnboardGithub)
			services.GET("/github/progress/:sessionID", svcHandler.OnboardProgress)
			services.DELETE("/github/progress/:sessionID", svcHandler.OnboardCancel)
			services.POST("/zip", svcHandler.OnboardZip)
			services.GET("", svcHandler.List)
			services.GET("/:name", svcHandler.Get)
			services.DELETE("/:name", svcHandler.Delete)
			services.GET("/:name/files", svcHandler.ScanFiles)
			services.GET("/:name/manifest", manifestHandler.Get)
			services.PUT("/:name/manifest", manifestHandler.Put)
		}

		v1.POST("/services/:name/deploy", deployHandler.Deploy)
		v1.POST("/services/:name/redeploy", deployHandler.Redeploy)
		v1.POST("/services/:name/scale", deployHandler.Scale)

		v1.POST("/ide/:name", ideHandler.Spawn)
		v1.DELETE("/ide/:name", ideHandler.Stop)
		v1.GET("/ide", ideHandler.List)

		if vncHandler != nil {
			v1.GET("/services/:name/vnc", vncHandler.Get)
			v1.POST("/services/:name/vnc/start", vncHandler.Start)
			v1.POST("/services/:name/vnc/stop", vncHandler.Stop)
			v1.GET("/vnc", vncHandler.List)
			v1.POST("/services/:name/vnc/share", relayHandler.Share)
		}

		// ── Relay sessions ────────────────────────────────────────────────
		v1.GET("/relay", relayHandler.Sessions)
		v1.DELETE("/relay/:token", relayHandler.Delete)

		v1.GET("/containers", handlers.ListContainers)
		v1.GET("/containers/stats", handlers.ContainerStats)
		v1.GET("/containers/:id/inspect", func(c *gin.Context) {
			detail, err := orch.Inspect(c.Request.Context(), c.Param("id"))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, detail)
		})

		containers := v1.Group("/containers/:id")
		{
			containers.POST("/start", lifecycleHandler.Start)
			containers.POST("/stop", lifecycleHandler.Stop)
			containers.POST("/restart", lifecycleHandler.Restart)
			containers.DELETE("", lifecycleHandler.Remove)
			containers.GET("/status", lifecycleHandler.Status)
			containers.POST("/exec", lifecycleHandler.Exec)
		}

		v1.GET("/containers/:id/logs", logsHandler.Stream)
		v1.GET("/containers/:id/logs/fetch", logsHandler.Fetch)

		v1.GET("/proxy/routes", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"routes": prx.Routes()})
		})

		// ── Shiplink ──────────────────────────────────────────────────────
		if shipRouter != nil {
			// GET /shiplink/services — all registered services + endpoints
			v1.GET("/shiplink/services", func(c *gin.Context) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				routes, err := shipRouter.Routes(ctx)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"services": routes, "count": len(routes)})
			})

			// GET /shiplink/resolve/:name — resolve a service name to its URL
			v1.GET("/shiplink/resolve/:name", func(c *gin.Context) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				registry := shiplink.NewRegistry(st.Client())
				resolver := shiplink.NewResolver(registry)
				url, err := resolver.Resolve(ctx, c.Param("name"))
				if err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{
					"service": c.Param("name"),
					"url":     url,
					"dns":     shiplink.DNSName(c.Param("name")),
				})
			})

			// POST /shiplink/canary/:name — set canary traffic split
			v1.POST("/shiplink/canary/:name", func(c *gin.Context) {
				var req struct {
					CanaryContainerID string `json:"canaryContainerID"`
					CanaryWeight      int    `json:"canaryWeight"` // 0-100
					CanaryHeader      string `json:"canaryHeader"`
				}
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				shipRouter.SetCanary(shiplink.CanaryRule{
					ServiceName:       c.Param("name"),
					StableWeight:      100 - req.CanaryWeight,
					CanaryWeight:      req.CanaryWeight,
					CanaryContainerID: req.CanaryContainerID,
					CanaryHeader:      req.CanaryHeader,
				})
				c.JSON(http.StatusOK, gin.H{
					"message":      fmt.Sprintf("canary set: %d%% to %s", req.CanaryWeight, req.CanaryContainerID[:12]),
					"stableWeight": 100 - req.CanaryWeight,
					"canaryWeight": req.CanaryWeight,
				})
			})

			// DELETE /shiplink/canary/:name — remove canary rule
			v1.DELETE("/shiplink/canary/:name", func(c *gin.Context) {
				shipRouter.RemoveCanary(c.Param("name"))
				c.JSON(http.StatusOK, gin.H{"message": "canary rule removed"})
			})
		}

		// -- Templates (built-in service catalog) ---------------------------------
		templatesHandler := handlers.NewTemplatesHandler(orch, st, bus)
		v1.GET("/templates", templatesHandler.List)
		v1.GET("/templates/:id", templatesHandler.Get)
		v1.POST("/templates/:id/deploy", templatesHandler.Deploy)

		// ── GitOps ────────────────────────────────────────────────────────
		if st != nil {
			gitopsHandler := handlers.NewGitOpsHandler(st, orch, bus)
			v1.PUT("/gitops/:name", gitopsHandler.Configure)
			v1.GET("/gitops", gitopsHandler.List)
			v1.GET("/gitops/:name", gitopsHandler.Get)
			v1.DELETE("/gitops/:name", gitopsHandler.Delete)
			v1.POST("/gitops/:name/webhook", gitopsHandler.Webhook)
			v1.POST("/gitops/:name/sync", gitopsHandler.Sync)
		}
		if catalogHandler != nil {
			v1.GET("/catalog", catalogHandler.List)
			v1.GET("/catalog/profiles", catalogHandler.Profiles)
			v1.POST("/catalog/import", catalogHandler.Import)
			v1.POST("/catalog/save/:name", catalogHandler.SaveFromService)
			v1.GET("/catalog/:name", catalogHandler.Get)
			v1.DELETE("/catalog/:name", catalogHandler.Delete)
			v1.POST("/catalog/:name/deploy", catalogHandler.Deploy)
		}

		// ── Nodes ─────────────────────────────────────────────────────────
		v1.GET("/nodes", func(c *gin.Context) {
			if st == nil {
				c.JSON(http.StatusOK, gin.H{"nodes": []interface{}{}, "count": 0})
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			nodes, err := st.ListNodes(ctx)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if nodes == nil {
				nodes = []*store.NodeInfo{}
			}
			c.JSON(http.StatusOK, gin.H{"nodes": nodes, "count": len(nodes)})
		})

		// ── State + ledger endpoints ──────────────────────────────────────
		if st != nil {
			exec := statemachine.NewExecutor(st, orch, bus)

			v1.GET("/stacks", func(c *gin.Context) {
				states, err := st.ListStackStates(c.Request.Context())
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"stacks": states, "count": len(states)})
			})
			v1.GET("/stacks/:name", func(c *gin.Context) {
				state, err := st.GetStackState(c.Request.Context(), c.Param("name"))
				if err != nil || state == nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
					return
				}
				c.JSON(http.StatusOK, state)
			})
			v1.GET("/stacks/:name/ledger", func(c *gin.Context) {
				entries, err := st.ListLedgerEntries(c.Request.Context(), c.Param("name"))
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"entries": entries, "count": len(entries)})
			})

			// POST /stacks/:name/stop    — stop containers, keep record
			// POST /stacks/:name/start   — start stopped containers
			// POST /stacks/:name/restart — restart containers
			// POST /stacks/:name/down    — remove containers, keep volumes + record
			// DELETE /stacks/:name       — destroy everything (irreversible)
			// POST /stacks/:name/rollback?version=<ts> — redeploy from ledger
			for _, op := range []string{"stop", "start", "restart", "down"} {
				opCopy := statemachine.Operation(op)
				v1.POST("/stacks/:name/"+op, func(c *gin.Context) {
					if err := exec.Apply(c.Request.Context(), c.Param("name"), opCopy); err != nil {
						c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
						return
					}
					c.JSON(http.StatusOK, gin.H{"message": string(opCopy) + " applied"})
				})
			}

			v1.DELETE("/stacks/:name", func(c *gin.Context) {
				if err := exec.Apply(c.Request.Context(), c.Param("name"), statemachine.OpDestroy); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "stack destroyed"})
			})

			v1.POST("/stacks/:name/rollback", func(c *gin.Context) {
				name := c.Param("name")
				version := c.Query("version")

				state, err := st.GetStackState(c.Request.Context(), name)
				if err != nil || state == nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "stack not found"})
					return
				}

				// Load ledger entry for this version.
				var entry *store.LedgerEntry
				if version != "" {
					entry, err = st.GetLedgerEntry(c.Request.Context(), name, version)
					if err != nil || entry == nil {
						c.JSON(http.StatusNotFound, gin.H{"error": "version not found"})
						return
					}
				} else {
					// Use most recent ledger entry before current.
					entries, err := st.ListLedgerEntries(c.Request.Context(), name)
					if err != nil || len(entries) < 2 {
						c.JSON(http.StatusBadRequest, gin.H{"error": "no previous version to roll back to"})
						return
					}
					entry = entries[1] // entries[0] is current
				}

				log.Printf("statemachine: rolling back %q to version %s", name, entry.Version)
				_ = st.TransitionState(c.Request.Context(), name, store.StateRollingBack, "")
				c.JSON(http.StatusAccepted, gin.H{
					"message": "rollback initiated",
					"version": entry.Version,
				})
			})
		}
	}

	// ── VNC relay (root-level — WebSocket + viewer page) ─────────────────────
	// GET /relay/:token       — WebSocket endpoint for viewers
	// GET /relay/:token/view  — HTML viewer page (shareable link)
	router.GET("/relay/:token", relayHandler.Connect)
	router.GET("/relay/:token/view", relayHandler.View)

	router.GET("/healthz", func(c *gin.Context) {
		etcdOK := st != nil
		natsOK := bus != nil
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"etcd":   etcdOK,
			"nats":   natsOK,
		})
	})

	// ── MCP server (Claude integration — Streamable HTTP 2025-03-26) ───────
	mcpServer := mcp.NewMCPServer(fmt.Sprintf("http://localhost:%d", port))
	mux := http.NewServeMux()
	mcpServer.Mount(mux)
	// Bridge Gin → stdlib mux for all /mcp/* paths.
	router.Any("/mcp", func(c *gin.Context) { mux.ServeHTTP(c.Writer, c.Request) })
	router.Any("/mcp/*path", func(c *gin.Context) { mux.ServeHTTP(c.Writer, c.Request) })
	log.Printf("mcp: Streamable HTTP at http://localhost:%d/mcp", port)

	return &Server{
		httpServer: &http.Server{
			Addr:         fmt.Sprintf(":%d", port),
			Handler:      router,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 0,
			IdleTimeout:  60 * time.Second,
		},
		orch:        orch,
		proxy:       prx,
		ideManager:  ideMgr,
		vncRegistry: vncRegistry,
		store:       st,
		bus:         bus,
		scheduler:   sched,
		cancelWatch: cancelWatch,
	}, nil
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	fmt.Printf("Shipyard API   → http://localhost%s\n", s.httpServer.Addr)
	fmt.Printf("Shipyard proxy → http://localhost:%d\n", proxy.DefaultProxyPort)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancelWatch != nil {
		s.cancelWatch()
	}
	if s.store != nil {
		s.store.Close()
	}
	if s.bus != nil {
		s.bus.Close()
	}
	return s.httpServer.Shutdown(ctx)
}

// makeReconcileFunc returns a ReconcileFunc that converges actual state
// toward desired state. Called by the watcher on every etcd change + 30s sweep.
func makeReconcileFunc(orch *orchestrator.Orchestrator, st *store.Store, bus *telemetry.Bus) store.ReconcileFunc {
	return func(ctx context.Context, state *store.StackState) error {
		switch state.State {

		case store.StateRunning:
			// Only verify containers if the last operation was a deploy.
			// Don't interfere with stop/restart operations in progress.
			if state.LastOperation != "" && state.LastOperation != "deploy" {
				return nil
			}
			for _, ctr := range state.Containers {
				if ctr.ContainerID == "" {
					continue
				}
				actual, err := orch.Status(ctx, ctr.ContainerID)
				if err != nil {
					// Container not found — could be a race, skip this sweep.
					return nil
				}
				if actual == "exited" || actual == "dead" {
					log.Printf("reconciler: container %q for %q is %q — marking failed",
						ctr.ContainerID[:12], state.Name, actual)
					return st.TransitionState(ctx, state.Name, store.StateFailed,
						fmt.Sprintf("container %s found in state %q", ctr.ContainerID[:12], actual))
				}
			}

		case store.StateFailed:
			// Only auto-retry deploy failures, not stop/start/down failures.
			if state.LastOperation != "" && state.LastOperation != "deploy" {
				log.Printf("reconciler: stack %q failed during %q — not retrying (user must fix)",
					state.Name, state.LastOperation)
				return nil
			}
			if state.RetryCount >= 3 {
				log.Printf("reconciler: stack %q has failed %d times — parked",
					state.Name, state.RetryCount)
				return nil
			}
			log.Printf("reconciler: stack %q deploy failed — scheduling retry %d/3",
				state.Name, state.RetryCount+1)
			return st.TransitionState(ctx, state.Name, store.StatePending,
				fmt.Sprintf("auto-retry %d", state.RetryCount+1))
		}

		return nil
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
