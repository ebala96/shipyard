# Shipyard — useful open source services

A curated list of open source services worth self-hosting via Shipyard.
All repos listed have Docker support (Dockerfile or docker-compose) at the root and can be onboarded directly.

> **Note:** Many projects separate their source code from their Docker config into different repos.
> Always use the Docker-specific repo URL below, not the main source repo, unless noted.

---

## Communication & collaboration

| Service | Onboard URL | What it is |
|---|---|---|
| Jitsi Meet | https://github.com/jitsi/docker-jitsi-meet | Self-hosted video conferencing |
| Rocket.Chat | https://github.com/RocketChat/Rocket.Chat | Slack alternative |
| Matrix Synapse | https://github.com/element-hq/synapse | Decentralised chat server |
| Mattermost | https://github.com/mattermost/mattermost | Team messaging |

---

## Development tools

| Service | Onboard URL | What it is |
|---|---|---|
| Gitea | https://github.com/go-gitea/gitea | Self-hosted GitHub |
| Drone CI | https://github.com/harness/drone | CI/CD pipelines |
| Verdaccio | https://github.com/verdaccio/verdaccio | Private npm registry |
| SonarQube | https://github.com/SonarSource/sonarqube | Code quality scanner |
| Nexus | https://github.com/sonatype/nexus-public | Artifact repository |

---

## Monitoring & observability

| Service | Onboard URL | What it is |
|---|---|---|
| Grafana | https://github.com/grafana/grafana | Metrics dashboards |
| Prometheus | https://github.com/prometheus/prometheus | Metrics collection |
| Loki | https://github.com/grafana/loki | Log aggregation |
| Uptime Kuma | https://github.com/louislam/uptime-kuma | Service uptime monitor |
| Netdata | https://github.com/netdata/netdata | Real-time system monitoring |

---

## Databases & storage

| Service | Onboard URL | What it is |
|---|---|---|
| MinIO | https://github.com/minio/minio | S3-compatible object storage |
| Redis Stack | https://github.com/redis-stack/redis-stack | Redis with search and JSON |
| PocketBase | https://github.com/pocketbase/pocketbase | Backend in a single file |
| Supabase | https://github.com/supabase/supabase | Open source Firebase |

---

## API & backend tools

| Service | Onboard URL | What it is |
|---|---|---|
| Kong | https://github.com/Kong/kong | API gateway |
| Traefik | https://github.com/traefik/traefik | Reverse proxy / ingress |
| n8n | https://github.com/n8n-io/n8n | Workflow automation |
| Hasura | https://github.com/hasura/graphql-engine | GraphQL over Postgres |
| Directus | https://github.com/directus/directus | Headless CMS / data API |

---

## Security & auth

| Service | Onboard URL | What it is |
|---|---|---|
| Keycloak | https://github.com/keycloak/keycloak | Identity and SSO |
| Vault | https://github.com/hashicorp/vault | Secrets management |
| Authentik | https://github.com/goauthentik/authentik | Auth provider |

---

## Productivity

| Service | Onboard URL | Note |
|---|---|---|
| Nextcloud | https://github.com/nextcloud/all-in-one | Use `all-in-one` repo — `nextcloud/server` is PHP source only |
| Outline | https://github.com/outline/outline | Team wiki / docs |
| AppFlowy | https://github.com/AppFlowy-IO/AppFlowy | Notion alternative |
| Plane | https://github.com/makeplane/plane | Project management |

---

## Best ones to start with in Shipyard

Recommended for initial testing — lightweight, start quickly, exercise different engine paths.

| Service | Onboard URL | Why start here |
|---|---|---|
| Traefik whoami | https://github.com/traefik/whoami | Tiny Go binary, builds in seconds — best smoke test |
| PocketBase | https://github.com/pocketbase/pocketbase | Single binary Dockerfile, zero dependencies — Docker engine test |
| n8n | https://github.com/n8n-io/n8n | Clean compose file, single service — compose engine test |
| Gitea | https://github.com/go-gitea/gitea | Has both Dockerfile and compose — good for engine switching test |
| MinIO | https://github.com/minio/minio | S3 storage, clean Dockerfile, genuinely useful locally |
| Uptime Kuma | https://github.com/louislam/uptime-kuma | compose.yaml at root, healthy community |

---

## Common mistakes — wrong repo vs right repo

Some projects keep source and Docker config in different repos.
Always use the Docker repo for Shipyard onboarding.

| Service | Wrong (source only) | Right (has Docker) |
|---|---|---|
| Nextcloud | `nextcloud/server` | `nextcloud/all-in-one` |
| Jitsi | `jitsi/jitsi-meet` | `jitsi/docker-jitsi-meet` |
| cAdvisor | `google/cadvisor` | onboard `google/cadvisor` with subdir `deploy` — or just deploy direct, Shipyard pulls `ghcr.io/google/cadvisor:latest` automatically |

---

## Notes on onboarding

- Paste any URL above into the **Services → Onboard** dialog.
- Shipyard auto-detects the engine (`docker`, `compose`, `kubernetes`, etc).
- Switch engines or files in the **Deploy** tab before deploying.
- `docker-compose.yml` / `compose.yaml` at root → detected as `compose` engine.
- `k8s/` or `kubernetes/` directory at root → detected as `kubernetes` engine.
- If onboarding fails with "no Dockerfile found", the repo likely keeps Docker config elsewhere — check the table above for the correct URL.


---

## Communication & collaboration

| Service | GitHub | What it is |
|---|---|---|
| Jitsi Meet | https://github.com/jitsi/docker-jitsi-meet | Self-hosted video conferencing |
| Rocket.Chat | https://github.com/RocketChat/Rocket.Chat | Slack alternative |
| Matrix Synapse | https://github.com/element-hq/synapse | Decentralised chat server |
| Mattermost | https://github.com/mattermost/mattermost | Team messaging |

---

## Development tools

| Service | GitHub | What it is |
|---|---|---|
| Gitea | https://github.com/go-gitea/gitea | Self-hosted GitHub |
| Drone CI | https://github.com/harness/drone | CI/CD pipelines |
| Verdaccio | https://github.com/verdaccio/verdaccio | Private npm registry |
| SonarQube | https://github.com/SonarSource/sonarqube | Code quality scanner |
| Nexus | https://github.com/sonatype/nexus-public | Artifact repository |

---

## Monitoring & observability

| Service | GitHub | What it is |
|---|---|---|
| Grafana | https://github.com/grafana/grafana | Metrics dashboards |
| Prometheus | https://github.com/prometheus/prometheus | Metrics collection |
| Loki | https://github.com/grafana/loki | Log aggregation |
| Uptime Kuma | https://github.com/louislam/uptime-kuma | Service uptime monitor |
| Netdata | https://github.com/netdata/netdata | Real-time system monitoring |

---

## Databases & storage

| Service | GitHub | What it is |
|---|---|---|
| MinIO | https://github.com/minio/minio | S3-compatible object storage |
| Redis Stack | https://github.com/redis-stack/redis-stack | Redis with search and JSON |
| PocketBase | https://github.com/pocketbase/pocketbase | Backend in a single file |
| Supabase | https://github.com/supabase/supabase | Open source Firebase |

---

## API & backend tools

| Service | GitHub | What it is |
|---|---|---|
| Kong | https://github.com/Kong/kong | API gateway |
| Traefik | https://github.com/traefik/traefik | Reverse proxy / ingress |
| n8n | https://github.com/n8n-io/n8n | Workflow automation |
| Hasura | https://github.com/hasura/graphql-engine | GraphQL over Postgres |
| Directus | https://github.com/directus/directus | Headless CMS / data API |

---

## Security & auth

| Service | GitHub | What it is |
|---|---|---|
| Keycloak | https://github.com/keycloak/keycloak | Identity and SSO |
| Vault | https://github.com/hashicorp/vault | Secrets management |
| Authentik | https://github.com/goauthentik/authentik | Auth provider |

---

## Productivity

| Service | GitHub | What it is |
|---|---|---|
| Nextcloud | https://github.com/nextcloud/server | Self-hosted Google Drive |
| Outline | https://github.com/outline/outline | Team wiki / docs |
| AppFlowy | https://github.com/AppFlowy-IO/AppFlowy | Notion alternative |
| Plane | https://github.com/makeplane/plane | Project management |

---

## Best ones to start with in Shipyard

These are recommended for initial testing because they are lightweight,
start quickly, and exercise different Shipyard engine paths.

| Service | GitHub | Why start here |
|---|---|---|
| n8n | https://github.com/n8n-io/n8n | Clean compose file, single service, starts fast — good compose engine test |
| PocketBase | https://github.com/pocketbase/pocketbase | Single binary with Dockerfile, zero dependencies — good Docker engine test |
| Gitea | https://github.com/go-gitea/gitea | Has both Dockerfile and compose — good for testing engine switching in Deploy tab |
| MinIO | https://github.com/minio/minio | S3 storage, clean Dockerfile, genuinely useful locally |
| Traefik whoami | https://github.com/traefik/whoami | Tiny Go service, builds in seconds — good for smoke testing |

---

## Notes on onboarding

- Paste any of the GitHub URLs above into the **Services → Onboard** dialog.
- Shipyard auto-detects the engine (`docker`, `compose`, `kubernetes`, etc).
- Switch engines or files in the **Deploy** tab before deploying.
- Services with a `docker-compose.yml` or `compose.yaml` at the root are detected as `compose` engine automatically.
- Services with a `k8s/` or `kubernetes/` directory are detected as `kubernetes` engine.