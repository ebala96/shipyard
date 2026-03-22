# Shipyard — Task List

Status: ✅ Done | 🔧 In progress | ⏳ Not started

---

## Phase 1 — Foundation ✅

### 1.1 etcd state store ✅
- `pkg/store/store.go` — generic put/get/list/delete + Raw helpers
- `pkg/store/service_store.go` — ServiceRecord CRUD at `/shipyard/services/{name}`
- `pkg/store/state_store.go` — StackState, ContainerRecord, LedgerEntry, IDERecord, Node field
- `pkg/store/node_store.go` — NodeInfo with 30s TTL leases at `/shipyard/nodes/{id}`
- `pkg/store/reconciler.go` — watch loop + 30s periodic sweep

### 1.2 State machine ✅
- `pkg/statemachine/transitions.go` — valid transition table + guards
- `pkg/statemachine/executor.go` — Apply(), RetryFailed(), Docker+etcd+NATS execution
- States: pending → deploying → running → stopping → stopped → down → destroyed | failed → rolling-back
- Auto-retry deploy failures up to 3×; stop/start NOT retried (uses LastOperation field)

### 1.3 NATS JetStream event bus ✅
- `pkg/telemetry/telemetry.go` — SHIPYARD_EVENTS (24h) + SHIPYARD_METRICS (4h)
- Infra auto-starts via `pkg/infra/infra.go` using Docker-based etcd + NATS containers

---

## Phase 2 — IaC Pipeline ✅

### 2.1 Scheduler ✅
- `pkg/scheduler/scheduler.go` — Filter → Score → Bind → Verify
- Scoring weights: bin-packing 35%, spread 25%, resource availability 30%, locality 10%
- `pkg/agent/agent.go` — embedded, heartbeat every 15s, real CPU/RAM from /proc/stat
- Falls back to localhost when no nodes registered

### 2.2 IaC pipeline ✅
- `pkg/pipeline/pipeline.go` — 7 stages: validate→resolve→interpolate→diff→policy→apply→reconcile
- `pkg/pipeline/diff.go` — DiffPlan types
- `pkg/pipeline/policy.go` — no privileged ports, max 20 replicas, secret warnings

### 2.3 Config editor ✅
- `GET/PUT /api/v1/services/:name/manifest`
- "Edit config" button in Services tab, YAML editor modal, auto-backup

---

## Phase 3 — Platform Adapters ✅

### 3.1 PlatformAdapter interface ✅
- `pkg/engine/engine.go` — Runner interface, 6 archetypes, engine.Factory()

### 3.2 All adapters ✅
- Docker, Compose, Kubernetes, Nomad, Podman, Swarm, Terraform, Mesos (Marathon REST API)
- `pkg/shipfile/types.go` — `Runtime.Image` field added — skips build, goes straight to pull

### 3.3 Port allocator ✅
- `pkg/portalloc/portalloc.go` — FindAvailable() + FindInRange()

---

## Phase 4 — Catalog + AI Import ✅

### 4.1 AI importer ✅
- `pkg/importer/ai.go` — Claude API (claude-haiku-4-5-20251001), repo snapshot → shipfile YAML
- `pkg/importer/detect.go` — fallback: Dockerfile/compose/k8s detection

### 4.2 Blueprint catalog ✅
- `pkg/catalog/catalog.go` — etcd-backed CRUD, parameter substitution, profile application
- `pkg/catalog/profiles.go` — Eco (0.25c/128MB) / Balanced (0.5c/512MB) / Performance (1c/1GB) / Max (2c/4GB)
- Catalog deploy wired to orchestrator — actually runs containers
- `syncContainers()` called after deploy so Monitor tab updates immediately

### 4.3 Catalog UI ✅
- `web/src/pages/Catalog.jsx` — blueprint cards, profile picker, AI import modal
- "Save to catalog" button on every service card

---

## Phase 5 — Shiplink ✅

- `pkg/shiplink/registry.go` — endpoints in etcd with 60s TTL
- `pkg/shiplink/dns.go` — `<service>.shipyard.local` resolver, 5s cache
- `pkg/shiplink/router.go` — canary traffic splits (weight + header-forced), health checks every 10s
- `pkg/shiplink/autoregister.go` — watches etcd, registers containers automatically on deploy
- API: `/shiplink/services`, `/shiplink/resolve/:name`, `/shiplink/canary/:name`

---

## Phase 6 — Observe tab ✅

- `web/src/pages/Observe.jsx` — 3 panels: Metrics / Logs / Events
- Metrics: recharts LineChart (CPU) + AreaChart (RAM), 5s polling, last 30 points
- Logs: SSE stream per container, filter, level coloring
- Events: stack health badges + ledger event feed, 10s polling

---

## Phase 7 — MCP server ✅

- `pkg/mcp/transport.go` — Streamable HTTP (spec 2025-03-26), single `/mcp` endpoint
- `pkg/mcp/server.go` — 12 tools via Shipyard REST API
- Legacy aliases `/mcp/sse` and `/mcp/messages` kept for mcp-remote compatibility
- Claude Desktop config: `npx mcp-remote http://localhost:8888/mcp`

---

## Phase 8a — GitOps sync ✅

- `pkg/gitops/sync.go` — SyncConfig in etcd, git pull via go-git, HMAC webhook validation
- Auto-redeploy on push to tracked branch (runs full 7-stage pipeline)
- `PUT /api/v1/gitops/:name`, `POST /api/v1/gitops/:name/webhook`, `POST .../sync`
- **Known issue:** manual sync returns null message (getServiceRecord vs store path mismatch)

---

## Phase 8b — Multi-node deploy ✅

- Scheduler `Place()` called in DeployHandler before Stage 7
- `TargetNode` in `orchestrator.DeployRequest`, `Node` in `store.StackState`
- `web/src/pages/Nodes.jsx` — registered nodes + per-node service list + container stats

---

## Phase 8c — Service templates ✅

- `pkg/templates/templates.go` — 16 templates: PostgreSQL, MySQL, Redis, MongoDB, Prometheus, Grafana, cAdvisor, MinIO, Nextcloud, Gitea, Docker Registry, Portainer, RabbitMQ, Vault, Keycloak, Traefik, NGINX, Whoami
- `GET /api/v1/templates`, `GET /api/v1/templates/:id`, `POST /api/v1/templates/:id/deploy`
- `web/src/pages/Templates.jsx` — grouped by category, search, inline param forms, one-click deploy

---

## Phase 8d — VNC app sharing ⏳ ← NEXT

### Goal
Embed a live screen of a running GUI container directly in the Shipyard dashboard.

### Planned architecture
```
Container (GUI app + Xvfb virtual display + x11vnc)
    ↓ VNC port 5900
noVNC + websockify (sidecar container)
    ↓ WebSocket
Shipyard proxy (/vnc/:name → ws://novnc-container)
    ↓ iframe src
Monitor tab VNC panel
```

### Tasks
- [ ] `pkg/vnc/launcher.go` — launch noVNC sidecar alongside app container
- [ ] `pkg/vnc/session.go` — VNC session registry in etcd
- [ ] Add `vnc` field to `pkg/shipfile/types.go` — `vnc: { enabled: true, port: 5900 }`
- [ ] Modify orchestrator deploy — detect `vnc.enabled`, inject sidecar
- [ ] `GET /api/v1/services/:name/vnc` — return ws URL
- [ ] `POST /api/v1/services/:name/vnc/start` / `.../stop`
- [ ] VNC viewer panel in Monitor tab — noVNC iframe, connect/disconnect button
- [ ] Proxy route `/vnc/:name` → WebSocket upstream

---

## Known issues / tech debt

| Issue | File | Priority |
|-------|------|----------|
| GitOps manual sync returns null message | `handlers/gitops.go` | High |
| deploy-files.sh unicode chars break bash on WSL | `deploy-files.sh` | High |
| MCP mcp-remote connection still flaky | `pkg/mcp/transport.go` | High |
| Catalog deploy doesn't appear in Services tab | `handlers/catalog.go` | Medium |
| Observe logs reconnect on every tab switch | `pages/Observe.jsx` | Low |