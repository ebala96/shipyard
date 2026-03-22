package shipfile_test

import (
	"strings"
	"testing"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// ── Parser tests ──────────────────────────────────────────────────────────────

func TestParse_ValidShipfile(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if sf.Service.Name != "myapi" {
		t.Errorf("expected service name 'myapi', got %q", sf.Service.Name)
	}

	if !sf.HasMode("dev") {
		t.Error("expected 'dev' mode to be present")
	}

	if !sf.HasMode("production") {
		t.Error("expected 'production' mode to be present")
	}

	if len(sf.Service.Dependencies) != 2 {
		t.Errorf("expected 2 dependencies, got %d", len(sf.Service.Dependencies))
	}
}

func TestParse_MissingVersion(t *testing.T) {
	yaml := `
service:
  name: myapi
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "version")
}

func TestParse_MissingServiceName(t *testing.T) {
	yaml := `
version: '1'
service:
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "service.name")
}

func TestParse_NoModes(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "mode")
}

func TestParse_MissingDockerfile(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
  modes:
    dev:
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "dockerfile")
}

func TestParse_MissingPortName(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - internal: 3000
            external: auto
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "name")
}

func TestParse_InvalidVolumeType(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
        volumes:
          - type: nfs
            from: '.'
            to: '/app'
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "type")
}

func TestParse_InvalidDependencyWaitFor(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
  dependencies:
    - name: postgres
      required: true
      waitFor: ready
`
	_, err := shipfile.ParseBytes([]byte(yaml))
	assertError(t, err, "waitFor")
}

func TestGetMode_NotFound(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	_, err = sf.GetMode("staging")
	if err == nil {
		t.Error("expected error for missing mode, got nil")
	}
}

// ── Resolver tests ────────────────────────────────────────────────────────────

func TestResolve_DevMode_AutoPorts(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	resolved, err := shipfile.Resolve(sf, "dev")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	appPort, ok := resolved.ResolvedPorts["app"]
	if !ok {
		t.Error("expected 'app' port to be resolved")
	}
	if appPort <= 0 || appPort > 65535 {
		t.Errorf("expected valid port number, got %d", appPort)
	}

	idePort, ok := resolved.ResolvedPorts["ide"]
	if !ok {
		t.Error("expected 'ide' port to be resolved")
	}
	if idePort <= 0 || idePort > 65535 {
		t.Errorf("expected valid port number, got %d", idePort)
	}

	if appPort == idePort {
		t.Errorf("expected app and ide ports to be different, both got %d", appPort)
	}
}

func TestResolve_EnvVarSubstitution(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	resolved, err := shipfile.Resolve(sf, "dev")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	appPortStr := resolved.ResolvedEnv["APP_PORT"]
	if appPortStr == "" {
		t.Error("expected APP_PORT env var to be resolved")
	}
	if strings.Contains(appPortStr, "${") {
		t.Errorf("APP_PORT still contains unresolved variable: %q", appPortStr)
	}
}

func TestResolve_ProductionMode(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	resolved, err := shipfile.Resolve(sf, "production")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if _, ok := resolved.ResolvedPorts["app"]; !ok {
		t.Error("expected 'app' port to be resolved in production mode")
	}
}

func TestResolve_UnknownMode(t *testing.T) {
	sf, err := shipfile.Parse("testdata/valid.shipfile.yml")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	_, err = shipfile.Resolve(sf, "staging")
	if err == nil {
		t.Error("expected error for unknown mode, got nil")
	}
}

func TestResolve_UnknownPortVariable(t *testing.T) {
	yaml := `
version: '1'
service:
  name: myapi
  modes:
    dev:
      build:
        dockerfile: Dockerfile.dev
      runtime:
        ports:
          - name: app
            internal: 3000
            external: auto
        env:
          BAD_VAR: '${ports.nonexistent}'
`
	sf, err := shipfile.ParseBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	_, err = shipfile.Resolve(sf, "dev")
	if err == nil {
		t.Error("expected error for unknown port variable, got nil")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func assertError(t *testing.T, err error, containing string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", containing)
	}
	if !strings.Contains(err.Error(), containing) {
		t.Errorf("expected error to contain %q, got: %v", containing, err)
	}
}