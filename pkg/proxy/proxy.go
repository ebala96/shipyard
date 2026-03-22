package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

const DefaultProxyPort = 9090

// Route maps a path prefix to an upstream URL.
type Route struct {
	// Prefix is the path prefix e.g. "/p/abc123/"
	Prefix string
	// Target is the upstream URL e.g. "http://localhost:39201"
	Target string
	// Label is a human-readable name for the route.
	Label string
}

// RouteInfo is a JSON-serialisable snapshot of a route.
type RouteInfo struct {
	Prefix    string `json:"prefix"`
	Target    string `json:"target"`
	Label     string `json:"label"`
	ProxyURL  string `json:"proxyURL"`
}

// Proxy is a single-port reverse proxy that routes by URL path prefix.
// All containers and IDEs are accessible through one port via:
//
//	/p/<containerID>/  → service container
//	/ide/<serviceName>/ → code-server IDE
type Proxy struct {
	mu     sync.RWMutex
	routes map[string]*route // prefix → route
	port   int
}

type route struct {
	label   string
	proxy   *httputil.ReverseProxy
	target  string
}

// New creates a Proxy listening on the given port.
func New(port int) *Proxy {
	if port == 0 {
		port = DefaultProxyPort
	}
	return &Proxy{
		routes: make(map[string]*route),
		port:   port,
	}
}

// Register adds a route from pathPrefix to targetURL.
// pathPrefix should be like "/p/abc123" (no trailing slash needed).
// targetURL should be like "http://localhost:39201".
func (p *Proxy) Register(pathPrefix, targetURL, label string) error {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("proxy: invalid target URL %q: %w", targetURL, err)
	}

	// Normalise prefix — always starts with / and has no trailing slash.
	prefix := "/" + strings.Trim(pathPrefix, "/")

	rp := httputil.NewSingleHostReverseProxy(parsed)

	// Rewrite the path — strip the prefix before forwarding.
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		// Strip prefix so the upstream sees /  not /p/abc123/...
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefix)
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("upstream %q unavailable: %v", label, err), http.StatusBadGateway)
	}

	p.mu.Lock()
	p.routes[prefix] = &route{label: label, proxy: rp, target: targetURL}
	p.mu.Unlock()

	return nil
}

// Deregister removes a route by prefix.
func (p *Proxy) Deregister(pathPrefix string) {
	prefix := "/" + strings.Trim(pathPrefix, "/")
	p.mu.Lock()
	delete(p.routes, prefix)
	p.mu.Unlock()
}

// Routes returns a snapshot of all registered routes.
func (p *Proxy) Routes() []RouteInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	info := make([]RouteInfo, 0, len(p.routes))
	for prefix, r := range p.routes {
		info = append(info, RouteInfo{
			Prefix:   prefix,
			Target:   r.target,
			Label:    r.label,
			ProxyURL: fmt.Sprintf("http://localhost:%d%s/", p.port, prefix),
		})
	}
	return info
}

// ServiceURL returns the proxy URL for a given prefix.
func (p *Proxy) ServiceURL(pathPrefix string) string {
	prefix := "/" + strings.Trim(pathPrefix, "/")
	return fmt.Sprintf("http://localhost:%d%s/", p.port, prefix)
}

// ContainerPrefix returns the standard path prefix for a container ID.
func ContainerPrefix(containerID string) string {
	id := containerID
	if len(id) > 12 {
		id = id[:12]
	}
	return "/p/" + id
}

// IDEPrefix returns the standard path prefix for a service IDE.
func IDEPrefix(serviceName string) string {
	return "/ide/" + strings.ToLower(strings.ReplaceAll(serviceName, " ", "-"))
}

// VNCPrefix returns the standard path prefix for a service VNC session.
func VNCPrefix(serviceName string) string {
	return "/vnc/" + strings.ToLower(strings.ReplaceAll(serviceName, " ", "-"))
}

// ServeHTTP implements http.Handler — matches the longest prefix and proxies.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	matched := p.matchRoute(r.URL.Path)
	p.mu.RUnlock()

	if matched == nil {
		http.Error(w, fmt.Sprintf("no route for path %q — use /p/<containerID>/ or /ide/<service>/", r.URL.Path), http.StatusNotFound)
		return
	}

	// Add CORS headers so the React dashboard can embed IDEs.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	matched.proxy.ServeHTTP(w, r)
}

// matchRoute finds the longest matching prefix for a request path.
func (p *Proxy) matchRoute(path string) *route {
	var best *route
	bestLen := 0

	for prefix, r := range p.routes {
		if strings.HasPrefix(path, prefix) && len(prefix) > bestLen {
			best = r
			bestLen = len(prefix)
		}
	}
	return best
}

// Start begins serving the proxy in a background goroutine.
func (p *Proxy) Start() error {
	addr := fmt.Sprintf(":%d", p.port)
	server := &http.Server{
		Addr:    addr,
		Handler: p,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("proxy: server error: %v\n", err)
		}
	}()
	fmt.Printf("Shipyard proxy listening on http://localhost:%d\n", p.port)
	return nil
}
