# Shipyard

Self-hosted Docker service management platform. A full-stack system for orchestrating containerised services via Docker, Compose, Kubernetes, Nomad, and Podman — with a React dashboard, etcd-backed state machine, and MCP server for Claude integration.

## Environment
- Machine: lenovo @ BK-2505, Windows 11, WSL2 Ubuntu 24.04
- Project root: `~/shipyard/`
- Go module: `github.com/shipyard/shipyard`

## Stack
- **Backend:** Go 1.22, Gin, Docker SDK v27
- **Frontend:** React + Vite (JavaScript, NOT TypeScript), Tailwind v3, Zustand
- **Infrastructure:** etcd :2379, NATS :4222
- **Ports:** Shipyard API :8888, Proxy :9090, React dev :5173, Agent metrics :9091

## Running the project
```bash
# Terminal 1 — backend (auto-starts etcd + NATS via Docker)
cd ~/shipyard && go run cmd/shipyard/main.go

# Terminal 2 — frontend
cd ~/shipyard/web && npm run dev

# Skip infra auto-start (if etcd/NATS already running)
SHIPYARD_NO_INFRA=1 go run cmd/shipyard/main.go
```

## Key conventions
- `deploy-files.sh` — extracts `files.zip` from Downloads, routes each file to the correct directory by filename, then runs `go build ./...` automatically
- Renamed files use a prefix pattern to avoid conflicts: `gitops_handler.go` → `internal/api/handlers/gitops.go`
- All renamed files MUST have a matching `case` label in the `deploy-files.sh` rename block
- No TypeScript — plain JS only throughout the frontend
- Power profiles: `eco` / `balanced` / `performance` / `max` (never S/M/L/XL)
- Service names in Shiplink DNS: `<name>.shipyard.local`

## Architecture overview
```
React Dashboard (8 tabs)
    ↓ REST API
Go/Gin Server :8888
    ├── pkg/orchestrator    Docker SDK deploys
    ├── pkg/statemachine    State transitions (pending→running→stopped→destroyed)
    ├── pkg/pipeline        7-stage IaC pipeline (validate→resolve→interpolate→diff→policy→apply→reconcile)
    ├── pkg/store           etcd CRUD (services, stacks, nodes, gitops, catalog)
    ├── pkg/scheduler       4-phase placement (Filter→Score→Bind→Verify)
    ├── pkg/shiplink        Service mesh (registry, DNS resolver, canary router)
    ├── pkg/agent           Embedded node agent (CPU/RAM heartbeat every 15s)
    ├── pkg/catalog         Blueprint catalog with power profiles
    ├── pkg/templates       16 built-in service templates
    ├── pkg/gitops          GitOps sync (webhook + manual pull + auto-redeploy)
    ├── pkg/mcp             MCP server (Streamable HTTP, 12 tools for Claude)
    └── pkg/telemetry       NATS JetStream event bus
```

## Key etcd key layout
```
/shipyard/services/{name}           ServiceRecord (onboarded service)
/shipyard/stacks/{name}/state       StackState (lifecycle + node placement)
/shipyard/stacks/{name}/ledger/{ts} LedgerEntry (deploy history for rollback)
/shipyard/nodes/{id}                NodeInfo (30s TTL, written by agent)
/shipyard/catalog/{name}            Blueprint
/shipyard/gitops/{name}             SyncConfig
/shipyard/shiplink/services/{svc}/{id}  Endpoint (60s TTL)
```

## Dashboard tabs
| Tab | Purpose |
|-----|---------|
| Services | Onboard repos (GitHub/ZIP), edit config, save to catalog |
| Catalog | Blueprint CRUD, AI import, deploy with power profile |
| Templates | 16 built-in templates (one-click deploy, no onboarding) |
| Deploy | Deploy onboarded services, pick platform + file |
| Scale | Autoscaler config and manual scaling |
| Monitor | Live container list, stop/start/restart/logs |
| Observe | Metrics charts (recharts), log viewer, event timeline |
| Nodes | Registered nodes with per-node service placement |

## MCP server (Claude integration)
- Endpoint: `http://localhost:8888/mcp` (Streamable HTTP, spec 2025-03-26)
- 12 tools: `list_services`, `deploy_service`, `stop_service`, `start_service`, `restart_service`, `get_logs`, `get_metrics`, `scale_service`, `list_blueprints`, `deploy_blueprint`, `resolve_service`, `get_nodes`
- Claude Desktop config (`%APPDATA%\Claude\claude_desktop_config.json`):
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

## Important file locations
```
cmd/shipyard/main.go              Entry point (starts etcd+NATS+agent+API)
cmd/shipyard-agent/agent_main.go  Standalone agent binary (renamed to avoid conflict)
internal/api/server.go            HTTP server, all route wiring
internal/api/handlers/            All HTTP handlers
pkg/engine/engine.go              PlatformAdapter Runner interface + Factory
pkg/shipfile/types.go             Shipfile schema (Runtime.Image added for templates)
deploy-files.sh                   Dev workflow: extract zip → copy → build
docs/tasks.md                     Full task list with completion status
docs/useful-services.md           Correct Docker image URLs for common services
```

## Known gotchas
- `helpers.go` exists in both `pkg/engine` and `pkg/orchestrator` — ambiguous resolver reads package declaration to route correctly
- `catalog.go` exists in both `pkg/catalog` and `internal/api/handlers` — use prefixed names (`pkg_catalog.go`, `handler_catalog.go`) in the zip
- `main.go` conflict between `cmd/shipyard` and `cmd/shipyard-agent` — agent file is named `agent_main.go`
- deploy-files.sh must use ASCII only — unicode box-drawing characters break bash
- The Go build target is WSL2 Ubuntu, not Windows
- `Runtime.Image` in shipfile types skips the Docker build step entirely (used by templates)
- Scheduler always runs but falls back to localhost when no nodes are registered