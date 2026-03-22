// Package templates provides built-in service templates bundled with Shipyard.
// Templates are pre-configured blueprints for common self-hosted services
// that can be deployed immediately without onboarding a repository.
package templates

import (
	"strings"

	"github.com/shipyard/shipyard/pkg/catalog"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// Category groups related templates.
type Category string

const (
	CategoryDatabase    Category = "database"
	CategoryMonitoring  Category = "monitoring"
	CategoryStorage     Category = "storage"
	CategoryDevTools    Category = "devtools"
	CategoryMessaging   Category = "messaging"
	CategorySecurity    Category = "security"
	CategoryWeb         Category = "web"
)

// Template is a built-in service blueprint.
type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    Category          `json:"category"`
	Tags        []string          `json:"tags"`
	Icon        string            `json:"icon"`   // emoji icon
	DocURL      string            `json:"docURL"`

	// Default power profile for this template.
	DefaultProfile catalog.PowerProfile `json:"defaultProfile"`

	// Parameters the user can configure at deploy time.
	Parameters []TemplateParam `json:"parameters,omitempty"`

	// The shipfile manifest (generated at deploy time).
	buildManifest func(params map[string]string) *shipfile.Shipfile
}

// TemplateParam defines a user-configurable value.
type TemplateParam struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`  // render as password field
}

// BuildManifest generates the shipfile for this template with user params applied.
func (t *Template) BuildManifest(params map[string]string) *shipfile.Shipfile {
	merged := make(map[string]string)
	for _, p := range t.Parameters {
		merged[p.Name] = p.Default
	}
	for k, v := range params {
		merged[k] = v
	}
	return t.buildManifest(merged)
}

// All returns all built-in templates.
func All() []*Template {
	return allTemplates
}

// Get returns a template by ID.
func Get(id string) *Template {
	for _, t := range allTemplates {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// ByCategory returns templates filtered by category.
func ByCategory(cat Category) []*Template {
	var result []*Template
	for _, t := range allTemplates {
		if t.Category == cat {
			result = append(result, t)
		}
	}
	return result
}

// Search returns templates matching a query string.
func Search(query string) []*Template {
	q := strings.ToLower(query)
	var result []*Template
	for _, t := range allTemplates {
		if strings.Contains(strings.ToLower(t.Name), q) ||
			strings.Contains(strings.ToLower(t.Description), q) ||
			strings.Contains(strings.ToLower(string(t.Category)), q) {
			result = append(result, t)
		}
	}
	return result
}

// ── Template definitions ──────────────────────────────────────────────────────

var allTemplates = []*Template{

	// ── Database ──────────────────────────────────────────────────────────────

	{
		ID:             "postgres",
		Name:           "PostgreSQL",
		Description:    "The world's most advanced open source relational database",
		Category:       CategoryDatabase,
		Tags:           []string{"database", "sql", "postgres"},
		Icon:           "🐘",
		DocURL:         "https://hub.docker.com/_/postgres",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "POSTGRES_DB",       Label: "Database name",   Default: "shipyard", Required: true},
			{Name: "POSTGRES_USER",     Label: "Username",        Default: "admin",    Required: true},
			{Name: "POSTGRES_PASSWORD", Label: "Password",        Default: "password", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("postgres", "postgres:16-alpine", 5432, p)
		},
	},

	{
		ID:             "mysql",
		Name:           "MySQL",
		Description:    "The most popular open source SQL database",
		Category:       CategoryDatabase,
		Tags:           []string{"database", "sql", "mysql"},
		Icon:           "🐬",
		DocURL:         "https://hub.docker.com/_/mysql",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "MYSQL_DATABASE",      Label: "Database name", Default: "shipyard", Required: true},
			{Name: "MYSQL_USER",          Label: "Username",      Default: "admin",    Required: true},
			{Name: "MYSQL_PASSWORD",      Label: "Password",      Default: "password", Required: true, Secret: true},
			{Name: "MYSQL_ROOT_PASSWORD", Label: "Root password", Default: "rootpass", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("mysql", "mysql:8.0", 3306, p)
		},
	},

	{
		ID:             "redis",
		Name:           "Redis",
		Description:    "In-memory data structure store — cache, queue, pub/sub",
		Category:       CategoryDatabase,
		Tags:           []string{"cache", "redis", "queue"},
		Icon:           "🔴",
		DocURL:         "https://hub.docker.com/_/redis",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("redis", "redis:7-alpine", 6379, p)
		},
	},

	{
		ID:             "mongodb",
		Name:           "MongoDB",
		Description:    "Document-oriented NoSQL database",
		Category:       CategoryDatabase,
		Tags:           []string{"database", "nosql", "mongodb"},
		Icon:           "🍃",
		DocURL:         "https://hub.docker.com/_/mongo",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "MONGO_INITDB_ROOT_USERNAME", Label: "Root username", Default: "admin",    Required: true},
			{Name: "MONGO_INITDB_ROOT_PASSWORD", Label: "Root password", Default: "password", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("mongodb", "mongo:7", 27017, p)
		},
	},

	// ── Monitoring ────────────────────────────────────────────────────────────

	{
		ID:             "prometheus",
		Name:           "Prometheus",
		Description:    "Open-source monitoring and alerting toolkit",
		Category:       CategoryMonitoring,
		Tags:           []string{"monitoring", "metrics", "prometheus"},
		Icon:           "🔥",
		DocURL:         "https://hub.docker.com/r/prom/prometheus",
		DefaultProfile: catalog.ProfileBalanced,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("prometheus", "prom/prometheus:latest", 9090, p)
		},
	},

	{
		ID:             "grafana",
		Name:           "Grafana",
		Description:    "The open observability platform — dashboards and visualisations",
		Category:       CategoryMonitoring,
		Tags:           []string{"monitoring", "dashboards", "grafana"},
		Icon:           "📊",
		DocURL:         "https://hub.docker.com/r/grafana/grafana",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "GF_SECURITY_ADMIN_PASSWORD", Label: "Admin password", Default: "admin", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("grafana", "grafana/grafana:latest", 3000, p)
		},
	},

	{
		ID:             "cadvisor",
		Name:           "cAdvisor",
		Description:    "Container resource usage and performance analysis",
		Category:       CategoryMonitoring,
		Tags:           []string{"monitoring", "containers", "cadvisor"},
		Icon:           "📡",
		DocURL:         "https://github.com/google/cadvisor",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("cadvisor", "gcr.io/cadvisor/cadvisor:latest", 8080, p)
		},
	},

	// ── Storage ───────────────────────────────────────────────────────────────

	{
		ID:             "minio",
		Name:           "MinIO",
		Description:    "High-performance S3-compatible object storage",
		Category:       CategoryStorage,
		Tags:           []string{"storage", "s3", "minio"},
		Icon:           "🪣",
		DocURL:         "https://hub.docker.com/r/minio/minio",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "MINIO_ROOT_USER",     Label: "Access key", Default: "minioadmin", Required: true},
			{Name: "MINIO_ROOT_PASSWORD", Label: "Secret key", Default: "minioadmin", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("minio", "minio/minio:latest", 9000, p)
		},
	},

	{
		ID:             "nextcloud",
		Name:           "Nextcloud",
		Description:    "Self-hosted productivity platform — files, calendar, contacts",
		Category:       CategoryStorage,
		Tags:           []string{"storage", "cloud", "nextcloud", "files"},
		Icon:           "☁️",
		DocURL:         "https://hub.docker.com/_/nextcloud",
		DefaultProfile: catalog.ProfilePerformance,
		Parameters: []TemplateParam{
			{Name: "NEXTCLOUD_ADMIN_USER",     Label: "Admin username", Default: "admin",    Required: true},
			{Name: "NEXTCLOUD_ADMIN_PASSWORD", Label: "Admin password", Default: "password", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("nextcloud", "nextcloud:latest", 80, p)
		},
	},

	// ── Dev Tools ─────────────────────────────────────────────────────────────

	{
		ID:             "gitea",
		Name:           "Gitea",
		Description:    "Lightweight self-hosted Git service",
		Category:       CategoryDevTools,
		Tags:           []string{"git", "devtools", "gitea"},
		Icon:           "🍵",
		DocURL:         "https://hub.docker.com/r/gitea/gitea",
		DefaultProfile: catalog.ProfileBalanced,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("gitea", "gitea/gitea:latest", 3000, p)
		},
	},

	{
		ID:             "registry",
		Name:           "Docker Registry",
		Description:    "Private Docker image registry",
		Category:       CategoryDevTools,
		Tags:           []string{"docker", "registry", "images"},
		Icon:           "📦",
		DocURL:         "https://hub.docker.com/_/registry",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("registry", "registry:2", 5000, p)
		},
	},

	{
		ID:             "portainer",
		Name:           "Portainer",
		Description:    "Docker management UI",
		Category:       CategoryDevTools,
		Tags:           []string{"docker", "management", "portainer"},
		Icon:           "🐳",
		DocURL:         "https://hub.docker.com/r/portainer/portainer-ce",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("portainer", "portainer/portainer-ce:latest", 9000, p)
		},
	},

	// ── Messaging ─────────────────────────────────────────────────────────────

	{
		ID:             "rabbitmq",
		Name:           "RabbitMQ",
		Description:    "Open source message broker",
		Category:       CategoryMessaging,
		Tags:           []string{"queue", "messaging", "rabbitmq", "amqp"},
		Icon:           "🐇",
		DocURL:         "https://hub.docker.com/_/rabbitmq",
		DefaultProfile: catalog.ProfileBalanced,
		Parameters: []TemplateParam{
			{Name: "RABBITMQ_DEFAULT_USER", Label: "Username", Default: "admin",    Required: true},
			{Name: "RABBITMQ_DEFAULT_PASS", Label: "Password", Default: "password", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("rabbitmq", "rabbitmq:3-management-alpine", 5672, p)
		},
	},

	// ── Security ──────────────────────────────────────────────────────────────

	{
		ID:             "vault",
		Name:           "HashiCorp Vault",
		Description:    "Secrets management and data protection",
		Category:       CategorySecurity,
		Tags:           []string{"secrets", "security", "vault"},
		Icon:           "🔐",
		DocURL:         "https://hub.docker.com/r/hashicorp/vault",
		DefaultProfile: catalog.ProfileBalanced,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("vault", "hashicorp/vault:latest", 8200, p)
		},
	},

	{
		ID:             "keycloak",
		Name:           "Keycloak",
		Description:    "Open source identity and access management",
		Category:       CategorySecurity,
		Tags:           []string{"auth", "sso", "keycloak", "oauth"},
		Icon:           "🔑",
		DocURL:         "https://hub.docker.com/r/keycloak/keycloak",
		DefaultProfile: catalog.ProfilePerformance,
		Parameters: []TemplateParam{
			{Name: "KEYCLOAK_ADMIN",          Label: "Admin username", Default: "admin",    Required: true},
			{Name: "KEYCLOAK_ADMIN_PASSWORD",  Label: "Admin password", Default: "password", Required: true, Secret: true},
		},
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("keycloak", "quay.io/keycloak/keycloak:latest", 8080, p)
		},
	},

	// ── Web ───────────────────────────────────────────────────────────────────

	{
		ID:             "traefik",
		Name:           "Traefik",
		Description:    "Cloud-native application proxy and load balancer",
		Category:       CategoryWeb,
		Tags:           []string{"proxy", "loadbalancer", "traefik"},
		Icon:           "🔀",
		DocURL:         "https://hub.docker.com/_/traefik",
		DefaultProfile: catalog.ProfileBalanced,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("traefik", "traefik:latest", 80, p)
		},
	},

	{
		ID:             "nginx",
		Name:           "NGINX",
		Description:    "High-performance web server and reverse proxy",
		Category:       CategoryWeb,
		Tags:           []string{"web", "proxy", "nginx"},
		Icon:           "🌐",
		DocURL:         "https://hub.docker.com/_/nginx",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("nginx", "nginx:alpine", 80, p)
		},
	},

	{
		ID:             "whoami",
		Name:           "Whoami",
		Description:    "Tiny HTTP server that prints OS information — great for testing",
		Category:       CategoryWeb,
		Tags:           []string{"test", "debug", "whoami"},
		Icon:           "👋",
		DocURL:         "https://hub.docker.com/r/traefik/whoami",
		DefaultProfile: catalog.ProfileEco,
		buildManifest: func(p map[string]string) *shipfile.Shipfile {
			return imageManifest("whoami", "traefik/whoami:latest", 80, p)
		},
	},
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// imageManifest builds a minimal shipfile for a Docker image deployment.
func imageManifest(name, image string, port int, env map[string]string) *shipfile.Shipfile {
	filteredEnv := make(map[string]string)
	for k, v := range env {
		if v != "" {
			filteredEnv[k] = v
		}
	}

	return &shipfile.Shipfile{
		Service: shipfile.Service{
			Name: name,
			Engine: shipfile.EngineConfig{
				Type: shipfile.EngineDocker,
			},
			Modes: map[string]shipfile.Mode{
				"production": {
					Runtime: shipfile.Runtime{
						Image: image, // Runtime.Image = pull this image, skip build
						Ports: []shipfile.Port{
							{Name: "app", Internal: port, External: "auto"},
						},
						Env: filteredEnv,
						Resources: shipfile.Resources{
							CPU:    0.5,
							Memory: "512m",
						},
					},
				},
			},
		},
	}
}
