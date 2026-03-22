# Shipyard — Architecture & Developer Guide

Shipyard is a self-hosted Docker service management platform. It orchestrates containerised services across Docker, Compose, Kubernetes, Nomad, and Podman with a React dashboard, etcd-backed state machine, and an MCP server for Claude integration.

---

## Table of Contents

1. [Quick Start](#quick-start)
2. [Project Structure](#project-structure)
3. [Configuration](#configuration)
4. [Architecture Overview](#architecture-overview)
5. [Backend Packages](#backend-packages)
6. [API Reference](#api-reference)
7. [Frontend](#frontend)
8. [State & Data Flow](#state--data-flow)
9. [Key Modules Added](#key-modules-added)
10. [Multi-Node Setup](#multi-node-setup)
11. [Claude (MCP) Integration](#claude-mcp-integration)
12. [VNC & Screen Sharing](#vnc--screen-sharing)
13. [Service Mesh (Shiplink)](#service-mesh-shiplink)
14. [Dependency Wiring](#dependency-wiring)

---

## Quick Start

**Requirements:** Go 1.22+, Node 18+, Docker Engine running.

```bash
# Clone the repo
git clone <repo-url> ~/shipyard
cd ~/shipyard

# Terminal 1 — backend (auto-starts etcd + NATS via Docker)
go run cmd/shipyard/main.go

# Terminal 2 — frontend
cd web && npm install && npm run dev
```

| Service        | URL                          |
|----------------|------------------------------|
| API server     | http://localhost:8888        |
| React dashboard| http://localhost:5173        |
| Reverse proxy  | http://localhost:9090        |
| etcd           | localhost:2379               |
| NATS           | localhost:4222               |
| Agent metrics  | http://localhost:9091/metrics|

Skip infra auto-start when etcd/NATS are already running:

```bash
SHIPYARD_NO_INFRA=1 go run cmd/shipyard/main.go
```

---

## Project Structure

```
shipyard/
├── cmd/
│   ├── shipyard/main.go              # Main API server entry point
│   └── shipyard-agent/agent_main.go  # Standalone node agent binary
├── internal/
│   └── api/
│       ├── server.go                 # HTTP server + all route wiring
│       └── handlers/                 # Gin request handlers (one file per domain)
├── pkg/
│   ├── agent/         Embedded node heartbeat agent
│   ├── catalog/       Blueprint CRUD + power-profile deployment
│   ├── compose/       Docker Compose file parser
│   ├── datadir/       ~/.shipyard data directory helpers
│   ├── engine/        Platform adapter factory (docker/compose/k8s/nomad/podman)
│   ├── gitops/        GitHub webhook + manual sync
│   ├── github/        Repo clone + shipfile detection
│   ├── idemanager/    code-server IDE sidecar lifecycle
│   ├── importer/      Blueprint import from external URL
│   ├── infra/         Auto-start etcd + NATS as Docker containers
│   ├── lb/            Load-balancing algorithms
│   ├── mcp/           MCP server (12 tools, Streamable HTTP)
│   ├── orchestrator/  Docker SDK wrapper (deploy/stop/start/logs)
│   ├── pipeline/      7-stage IaC deploy pipeline
│   ├── progress/      GitHub onboard progress tracker
│   ├── proxy/         Single-port reverse proxy (:9090)
│   ├── relay/         WebSocket relay for VNC sharing
│   ├── scaler/        Autoscaler policy engine
│   ├── scheduler/     4-phase node placement engine
│   ├── shipfile/      shipfile.yml schema types
│   ├── shiplink/      Service mesh: discovery, canary, health check
│   ├── statemachine/  Stack lifecycle FSM
│   ├── store/         etcd-backed state store
│   ├── telemetry/     NATS JetStream event bus
│   ├── templates/     16 built-in service templates
│   └── vnc/           VNC sidecar launcher + session registry
├── web/
│   └── src/
│       ├── App.jsx              8-tab main navigation
│       ├── lib/api.js           Axios HTTP client (all endpoints)
│       ├── store/shipyard.js    Zustand global state
│       ├── pages/               Tab components (8 files)
│       └── components/ui.jsx    Shared UI component library
├── docs/
│   ├── tasks.md                 Phase-by-phase task list
│   └── useful-services.md       Verified Docker image URLs
├── deploy-files.sh              Dev workflow: zip → copy → go build
├── docker-compose.dev.yml       Local infra (etcd + NATS)
├── go.mod
└── CLAUDE.md                    Claude Code guidance
```

---

## Configuration

### Backend environment variables

| Variable             | Default               | Description                              |
|----------------------|-----------------------|------------------------------------------|
| `PORT`               | `8888`                | API server port                          |
| `SHIPYARD_NO_INFRA`  | —                     | Set to `1` to skip etcd + NATS startup   |
| `ETCD_ENDPOINTS`     | `localhost:2379`      | Comma-separated etcd addresses           |
| `NATS_URL`           | `nats://localhost:4222` | NATS connection URL                    |

### Frontend environment variables

| Variable        | Default                    | Description          |
|-----------------|----------------------------|----------------------|
| `VITE_API_URL`  | `http://localhost:8888`    | Backend API base URL |

### Node agent environment variables

| Variable          | Default      | Description                         |
|-------------------|--------------|-------------------------------------|
| `AGENT_NODE_ID`   | hostname     | Unique node identifier              |
| `AGENT_NODE_NAME` | hostname     | Display name in dashboard           |
| `AGENT_REGION`    | `local`      | Geographic region label             |
| `AGENT_PROVIDER`  | `docker`     | Container platform type             |
| `METRICS_PORT`    | `9091`       | Prometheus metrics HTTP port        |

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│  React Dashboard (5173)   ←→   Shipyard API (8888)           │
│  8 tabs, Zustand store         Gin + Docker SDK              │
└──────────────────────────────┬───────────────────────────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
   ┌──────▼──────┐    ┌───────▼───────┐   ┌───────▼───────┐
   │    etcd     │    │     NATS      │   │    Docker     │
   │  :2379      │    │   :4222       │   │   Engine      │
   │ State store │    │  Event bus    │   │ Containers    │
   └─────────────┘    └───────────────┘   └───────────────┘
          │                    │
   ┌──────▼──────────────────────────────────────────────┐
   │  Reconciler                                          │
   │  Watches etcd key changes + 30s periodic sweep      │
   │  Converges Docker state toward desired state         │
   └──────────────────────────────────────────────────────┘
```

### Request path for a deploy

```
User clicks Deploy
      │
      ▼
DeployHandler
  ├── 1. Load ServiceRecord from ~/.shipyard
  ├── 2. Parse shipfile.yml
  ├── 3. Run 7-stage Pipeline
  │     (validate → resolve → interpolate → diff → policy → apply → reconcile)
  ├── 4. Schedule node placement (Filter → Score → Bind → Verify)
  ├── 5. Orchestrator.Deploy() → Docker SDK creates container
  ├── 6. Register in Shiplink (service mesh)
  ├── 7. Save StackState to etcd
  └── 8. Publish deploy event to NATS
```

---

## Backend Packages

### `pkg/orchestrator` — Docker SDK Wrapper

Central package for all container runtime operations. Abstracts the Docker SDK behind clean interfaces.

**Key types:**
- `DeployRequest` — service name, mode, shipfile config, resolved variables
- `DeployedService` — running container metadata (ID, name, ports, network, VNC instance)

**Key methods:**
```go
Deploy(ctx, req)          → DeployedService, error
Start/Stop/Restart(ctx, id)
Remove(ctx, id, force)
Logs(ctx, id, tail)       → []LogLine
ListContainers(ctx)       → []Container
Exec(ctx, id, cmd)        → output string
```

---

### `pkg/pipeline` — 7-Stage Deploy Pipeline

Every deploy passes through all seven stages in order:

| Stage | Name | Description |
|-------|------|-------------|
| 1 | **Validate** | Schema validation, cycle detection in dependencies |
| 2 | **Resolve** | Merge env configs: base → shared → overlay → service |
| 3 | **Interpolate** | Substitute `${variable}` placeholders |
| 4 | **Diff & Plan** | Compare desired vs current etcd state |
| 5 | **Policy Gate** | Built-in + custom policy rules |
| 6 | **Apply** | Atomic etcd write + create LedgerEntry snapshot |
| 7 | **Reconcile** | Watcher converges Docker containers toward intent |

---

### `pkg/statemachine` — Stack Lifecycle FSM

```
pending ──► running ──► stopped ──► destroying ──► destroyed
               │                        │
               └────────────────────────┘ (stop → restart → running)
```

Operations: `start`, `stop`, `restart`, `down`, `destroy`, `rollback`.

Each operation validates the transition, executes Docker ops, persists to etcd, and publishes a NATS event.

---

### `pkg/scheduler` — 4-Phase Placement Engine

Used for multi-node deployments when worker nodes are registered.

| Phase | Name | Description |
|-------|------|-------------|
| 1 | **Filter** | Hard constraints: CPU/memory fit, provider match, selectors |
| 2 | **Score** | Soft preferences: bin-packing, spread, locality |
| 3 | **Bind** | Reserve resources on selected node via etcd |
| 4 | **Verify** | Pre-flight: confirm node agent is responsive |

Falls back to localhost if no nodes are registered.

---

### `pkg/store` — etcd-backed State Store

All persistent state (beyond the running containers) lives in etcd.

**Key types:**
```go
StackState     // desired + actual lifecycle state
ServiceRecord  // onboarded service metadata
NodeInfo       // node heartbeat (30-second TTL)
LedgerEntry    // timestamped snapshot for rollback
Blueprint      // catalog blueprint
ContainerRecord // running container metadata + VNC info
```

**etcd key layout:**
```
/shipyard/services/{name}              ServiceRecord
/shipyard/stacks/{name}/state          StackState
/shipyard/stacks/{name}/ledger/{ts}    LedgerEntry (version history)
/shipyard/nodes/{id}                   NodeInfo  (TTL: 30s)
/shipyard/catalog/{name}               Blueprint
/shipyard/gitops/{name}                SyncConfig
/shipyard/shiplink/services/{svc}/{id} Endpoint  (TTL: 60s)
```

---

### `pkg/shipfile` — Service Manifest Schema

Defines the `shipfile.yml` schema parsed for every service.

```yaml
service:
  name: my-service
  engine: docker        # docker | compose | kubernetes | nomad | podman

  modes:
    production:
      build:
        dockerfile: Dockerfile
      runtime:
        image: myapp:latest
        ports:
          - name: http
            container: 8080
            host: 0       # 0 = auto-assign
        env:
          DB_URL: postgres://...
        resources:
          cpu: 0.5        # cores
          memory: 512     # MB
        health:
          path: /health
          port: 8080
          interval: 10
        vnc:
          enabled: true
          port: 5900      # VNC port inside container
      ide:
        enabled: true     # spawn code-server sidecar
      scale:
        instances: 1
        autoscale:
          min: 1
          max: 5
          targetCPU: 70
          targetMemory: 80
          cooldown: 60
        loadBalancer:
          algorithm: round-robin
```

**Power profiles** (applied at deploy from catalog):

| Profile | CPU | Memory |
|---------|-----|--------|
| `eco` | ×0.25 | ×0.25 |
| `balanced` | ×0.5 | ×0.5 |
| `performance` | ×1.0 | ×1.0 |
| `max` | ×2.0 | ×2.0 |

---

### `pkg/catalog` — Blueprint Management

Versioned, parameterised service templates stored in etcd.

- **Save** a running service as a blueprint
- **Import** a blueprint from a GitHub URL
- **Deploy** with a power profile (scales resources up/down)
- **List/Delete** blueprints

---

### `pkg/templates` — 16 Built-in Templates

One-click deploy for common infrastructure services. No onboarding required.

| Category | Templates |
|----------|-----------|
| Database | PostgreSQL, MySQL, MongoDB, Redis |
| Monitoring | Prometheus, Grafana |
| Messaging | RabbitMQ |
| Storage | MinIO |
| DevTools | code-server (VS Code), Jupyter |
| Security | Keycloak |
| Web | Nginx |

Each template defines default power profiles and user-configurable parameters.

---

### `pkg/gitops` — GitOps Sync

Automatic redeployment on git push.

1. Configure a service with a GitHub URL, branch, and optional webhook secret
2. Register the webhook in GitHub (`/api/v1/gitops/:name/webhook`)
3. On push event: pull latest, redeploy, publish NATS event
4. Manual sync: `POST /api/v1/gitops/:name/sync`

---

### `pkg/mcp` — Claude Integration

Exposes Shipyard as an MCP server using the Streamable HTTP spec (2025-03-26).

**Endpoint:** `http://localhost:8888/mcp`

**12 available tools:**

| Tool | Description |
|------|-------------|
| `list_services` | All services with status |
| `deploy_service` | Deploy a service by name |
| `stop_service` | Stop a running service |
| `start_service` | Start a stopped service |
| `restart_service` | Restart a service |
| `get_logs` | Fetch recent container logs |
| `get_metrics` | CPU and memory for a service |
| `scale_service` | Change replica count |
| `list_blueprints` | All catalog blueprints |
| `deploy_blueprint` | Deploy blueprint with power profile |
| `resolve_service` | Get Shiplink URL for a service |
| `get_nodes` | Registered nodes with resource stats |

**Claude Desktop config** (`%APPDATA%\Claude\claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "shipyard": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8888/mcp"]
    }
  }
}
```

---

### `pkg/telemetry` — NATS JetStream Event Bus

Publishes structured events for lifecycle changes and metrics.

**NATS subjects:**
- `shipyard.events.*` — deploy, stop, start, scale, destroy, rollback events
- `shipyard.metrics.containers` — CPU/memory samples consumed by the autoscaler

---

### `pkg/infra` — Infrastructure Manager

Auto-starts etcd and NATS as Docker containers at server startup.

```
etcd  → :2379 (client) :2380 (peer)
NATS  → :4222 (client) :8222 (HTTP metrics)
```

Disable with `SHIPYARD_NO_INFRA=1` if you manage infra yourself.

---

## API Reference

All routes are prefixed with `/api/v1` unless noted.

### Services
```
POST   /services/github                         Onboard GitHub repo (starts SSE session)
GET    /services/github/progress/:sessionID     Stream onboard progress (Server-Sent Events)
DELETE /services/github/progress/:sessionID     Cancel onboard
POST   /services/zip                            Onboard ZIP upload
GET    /services                                List all services
GET    /services/:name                          Get service details
DELETE /services/:name                          Delete service
GET    /services/:name/files                    Scan files (Dockerfile, compose, manifests)
GET    /services/:name/manifest                 Get shipfile.yml content
PUT    /services/:name/manifest                 Update shipfile.yml
```

### Deployment
```
POST   /services/:name/deploy                   Deploy service (creates new stack)
POST   /services/:name/redeploy                 Redeploy (replace running instance)
POST   /services/:name/scale                    Scale to N replicas
```

### Container Lifecycle
```
GET    /containers                              List all Shipyard containers
GET    /containers/stats                        CPU/memory stats for each container
GET    /containers/:id/inspect                  Full container inspection
POST   /containers/:id/start                    Start container
POST   /containers/:id/stop                     Stop container
POST   /containers/:id/restart                  Restart container
DELETE /containers/:id                          Remove container (force=true to kill first)
GET    /containers/:id/status                   Get status string
POST   /containers/:id/exec                     Execute command { cmd: ["sh","-c","..."] }
```

### Logs
```
GET    /containers/:id/logs                     Stream logs (Server-Sent Events)
GET    /containers/:id/logs/fetch?tail=100      Fetch log snapshot
```

### Stacks
```
GET    /stacks                                  List all stack states
GET    /stacks/:name                            Get stack state + containers
GET    /stacks/:name/ledger                     Version history (for rollback)
POST   /stacks/:name/stop                       Stop (preserve record)
POST   /stacks/:name/start                      Start previously stopped stack
POST   /stacks/:name/restart                    Restart
POST   /stacks/:name/down                       Remove containers (keep volumes)
DELETE /stacks/:name                            Destroy (irreversible)
POST   /stacks/:name/rollback?version=<ts>      Rollback to a ledger snapshot
```

### IDE (code-server)
```
POST   /ide/:name                               Spawn code-server sidecar
DELETE /ide/:name                               Stop IDE
GET    /ide                                     List running IDE sessions
```

### VNC
```
GET    /services/:name/vnc                      VNC session status
POST   /services/:name/vnc/start                Launch VNC sidecar (noVNC on port 8080)
POST   /services/:name/vnc/stop                 Stop VNC
GET    /vnc                                     List all active VNC sessions
POST   /services/:name/vnc/share                Create shareable relay link → { token, viewURL }
```

### Relay (root-level, no /api/v1 prefix)
```
GET    /api/v1/relay                            List active relay sessions
DELETE /api/v1/relay/:token                     Revoke relay token
GET    /relay/:token                            WebSocket endpoint for viewers
GET    /relay/:token/view                       Self-contained HTML viewer page
```

### Catalog & Templates
```
GET    /catalog                                 List blueprints
GET    /catalog/sizes                           Power profiles
GET    /catalog/:name                           Blueprint details
DELETE /catalog/:name                           Delete blueprint
POST   /catalog/:name/instantiate              Deploy with power profile
GET    /templates                              List 16 built-in templates
GET    /templates/:id                          Template details + parameters
POST   /templates/:id/deploy                   One-click deploy
```

### Service Mesh (Shiplink)
```
GET    /shiplink/services                       All registered services + endpoints
GET    /shiplink/resolve/:name                  Resolve service name → URL
POST   /shiplink/canary/:name                   Set canary split { weight: 20 }
DELETE /shiplink/canary/:name                   Remove canary rule
```

### GitOps
```
PUT    /gitops/:name                            Configure GitOps (URL, branch, secret)
GET    /gitops                                  List all GitOps configs
GET    /gitops/:name                            Get config for service
DELETE /gitops/:name                            Disable GitOps
POST   /gitops/:name/webhook                    GitHub push webhook receiver
POST   /gitops/:name/sync                       Trigger manual sync + redeploy
```

### Infrastructure
```
GET    /nodes                                   Registered nodes + resource usage
GET    /proxy/routes                            Reverse proxy route table
GET    /healthz                                 Health check (etcd + NATS status)
POST   /mcp                                     MCP Streamable HTTP (Claude tools)
```

---

## Frontend

### Technology

- **React 18** with Vite (JavaScript, no TypeScript)
- **Tailwind CSS v3** — utility-first styling
- **Zustand** — lightweight global state
- **Axios** — HTTP client (`web/src/lib/api.js`)
- **Recharts** — metrics charts in Observe tab

### 8 Dashboard Tabs

| Tab | Page File | Purpose |
|-----|-----------|---------|
| Services | `Services.jsx` | Onboard repos (GitHub/ZIP), edit manifests, spawn IDE |
| Catalog | `Catalog.jsx` | Blueprint CRUD, power-profile deployment |
| Deploy | `Deploy.jsx` | Deploy onboarded services, pick platform + file |
| Scale | `Scale.jsx` | Autoscaler config + manual scaling |
| Monitor | `Monitor.jsx` | Live container list, logs, exec, VNC panel |
| Templates | `Templates.jsx` | 16 built-in services, one-click deploy |
| Observe | `Observe.jsx` | CPU/memory charts, log viewer, event timeline |
| Nodes | `Nodes.jsx` | Registered nodes, resource gauges, placement |

### Zustand Store (`store/shipyard.js`)

```js
// Sections:
services       []  ← fetched from /api/v1/services
containers     []  ← synced from /api/v1/containers
activeTab      string

// Actions:
fetchServices()
syncContainers()
addContainer(c)
updateContainerStatus(id, status)
refreshContainerStatus(id)       ← polls /containers/:id/status
removeContainerFromStore(id)
```

### UI Components (`components/ui.jsx`)

`Button`, `Card`, `Badge`, `Spinner`, `PageHeader`, `EmptyState`

`statusColor(status)` maps container status strings to badge color variants.

---

## State & Data Flow

### Layered persistence

```
Disk  (~/.shipyard/services/)
  └── Onboarded source code + shipfile.yml

etcd
  └── StackState, LedgerEntry, NodeInfo, Blueprints, GitOps configs

NATS
  └── Lifecycle events + metrics samples (consumed by autoscaler)

Zustand (browser session)
  └── Service list, container list, active tab

Docker Engine (runtime)
  └── Running containers, images, volumes, networks
```

### Reconciler

The reconciler runs inside the API server whenever etcd is available:

1. Watches `/shipyard/stacks/*` for key changes
2. Runs a 30-second periodic sweep
3. For each StackState where `desired != actual`, issues Docker operations
4. Converges actual Docker state toward desired without user intervention

---

## Key Modules Added

This section documents the phases of new functionality added beyond the base deploy/monitor core.

### Phase 8d — VNC App Sharing

Allows any container with a VNC server (x11vnc + Xvfb on port 5900) to be viewed in a browser and shared with remote users.

**New packages:**

#### `pkg/vnc`
- `launcher.go` — Starts a `theasp/novnc` sidecar container that proxies VNC to WebSocket
- `session.go` — In-memory registry of active VNC sessions (`sync.RWMutex`-protected map)

`Launch(ctx, serviceName, mode, networkName, mainContainerName, vncPort)` inspects the target container's bridge IP (for standalone deploys with no user-defined network) and sets `VNC_SERVER=<ip>:<port>` in the noVNC container.

#### `pkg/relay`
- `relay.go` — WebSocket relay manager; each viewer gets its own upstream connection to the noVNC websockify endpoint, so x11vnc's `-shared` flag serves all viewers independently
- `token.go` — Cryptographically random 8-byte hex token generator

**New handlers:**

#### `internal/api/handlers/vnc.go`
- `Get / Start / Stop / List` — VNC lifecycle via the launcher + registry
- `resolveContainerInfo` — looks up the running container from etcd stack states

#### `internal/api/handlers/relay.go`
- `Share` — Creates a relay room and returns a `viewURL`
- `Connect` — WebSocket upgrade; relays binary RFB frames between viewer and upstream noVNC
- `View` — Serves a self-contained noVNC HTML page (inline Go template, CDN-loaded noVNC)
- `Sessions / Delete` — Relay room management

**VNC sidecar setup (shipfile.yml):**

```yaml
modes:
  production:
    runtime:
      vnc:
        enabled: true
        port: 5900        # the port x11vnc listens on inside the container
```

Your container must run x11vnc with the `-shared` flag so multiple relay viewers can connect simultaneously:

```dockerfile
# Example entrypoint
CMD Xvfb :0 -screen 0 1920x1080x24 & \
    x11vnc -display :0 -nopw -shared -forever -bg && \
    exec your-gui-app
```

**Share flow:**
1. User clicks **Connect** in Monitor tab → `POST /api/v1/services/:name/vnc/start` → noVNC sidecar starts
2. noVNC iframe appears in Monitor panel
3. User clicks **Share** → `POST /api/v1/services/:name/vnc/share` → relay room created, `viewURL` returned
4. Share recipient opens `http://<server>:8888/relay/<token>/view` in any browser
5. Relay server proxies RFB frames to the noVNC sidecar; each viewer gets an independent upstream connection

---

### Service Mesh (Shiplink) — Phase 7

Every deployed container is auto-registered in the Shiplink registry.

- DNS names resolve as `<service>.shipyard.local` internally
- `GET /api/v1/shiplink/resolve/:name` → returns the container's proxy URL
- Canary rules split traffic by percentage: `POST /api/v1/shiplink/canary/:name { "weight": 20 }`
- Health checker removes unhealthy backends with TTL expiry (60-second etcd lease)

---

### GitOps — Phase 6

1. `PUT /api/v1/gitops/:name` — configure with `{ url, branch, secret }`
2. Register the generated webhook URL in GitHub repository settings
3. On `push` event: server pulls latest, runs pipeline, redeploys
4. `POST /api/v1/gitops/:name/sync` — manual trigger without a push

---

### Rollback / Ledger — Phase 5

Every deploy writes a `LedgerEntry` to etcd at `/shipyard/stacks/{name}/ledger/{timestamp}`.

```
GET  /api/v1/stacks/:name/ledger              List all versions (most recent first)
POST /api/v1/stacks/:name/rollback?version=ts Redeploy from a specific snapshot
```

---

## Multi-Node Setup

### Embedded agent (single-machine)

The main server embeds a node agent that registers `localhost` in etcd and sends a heartbeat every 15 seconds. The scheduler uses this for single-machine deploys.

### Standalone agent (worker nodes)

On each additional machine:

```bash
# Build the agent binary
go build -o shipyard-agent ./cmd/shipyard-agent

# Run with your node's configuration
AGENT_NODE_ID=worker-1 \
AGENT_REGION=us-east \
ETCD_ENDPOINTS=<master-ip>:2379 \
NATS_URL=nats://<master-ip>:4222 \
./shipyard-agent
```

The scheduler will automatically include the worker in the Filter → Score → Bind → Verify placement cycle.

---

## Claude (MCP) Integration

Shipyard exposes a Model Context Protocol server at `http://localhost:8888/mcp`.

Once configured, you can ask Claude things like:

> "Deploy the postgres blueprint with balanced resources"
> "Show me the logs for my api-service"
> "Scale web-frontend to 3 instances"
> "Which nodes have the most free memory?"

See the [MCP tools list](#pkg-mcp) above for the full set of 12 tools.

---

## Service Mesh (Shiplink)

Every deployed container is registered in Shiplink with a 60-second etcd TTL, renewed by the embedded health checker.

```
Deploy completes
      │
      ▼
AutoRegistrar.Register(serviceName, containerID, proxyURL)
      │
      ▼
etcd: /shipyard/shiplink/services/{name}/{id}  (TTL: 60s)
      │
      ▼
Shiplink router picks up new endpoint on next request
```

**Canary deployments:**

```bash
# Split 20% of traffic to the new version
curl -X POST http://localhost:8888/api/v1/shiplink/canary/my-service \
  -H 'Content-Type: application/json' \
  -d '{"weight": 20}'

# Remove split when confident
curl -X DELETE http://localhost:8888/api/v1/shiplink/canary/my-service
```

---

## Dependency Wiring

`internal/api/server.go` is the single wiring point. Simplified:

```go
// Infrastructure
st  := store.New(etcdEndpoints)         // etcd (optional)
bus := telemetry.New(natsURL)           // NATS (optional)

// Core
orch    := orchestrator.New()
prx     := proxy.New(9090)
ideMgr  := idemanager.New()
sched   := scheduler.New(st)           // falls back to localhost
cat     := catalog.New(st)
vncReg  := &vnc.Registry{}
relayMgr:= relay.NewManager()

// Handlers
svcH    := handlers.NewServiceHandler(orch, st, bus)
depH    := handlers.NewDeployHandler(orch, st, bus, sched, pipeline, prx)
vncH    := handlers.NewVNCHandler(vnc.NewLauncher(), vncReg, st)
relayH  := handlers.NewRelayHandler(relayMgr, vncReg, baseURL)

// Background loops (when etcd available)
reconciler.Start(ctx)     // converge Docker → etcd intent
localAgent.Start(ctx)     // heartbeat every 15s
```

All routes are registered in `server.go` under `/api/v1` except:
- `/relay/:token` and `/relay/:token/view` — root-level for WebSocket upgrade compatibility
- `/mcp` — root-level for MCP Streamable HTTP
- `/healthz` — root-level health check

---

*Generated from codebase as of 2026-03-22.*
