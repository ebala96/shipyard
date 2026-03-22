// Package importer provides AI-assisted import of GitHub repositories.
// It builds a snapshot of the repo contents and sends it to Claude,
// which returns a shipfile.yml manifest automatically.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shipyard/shipyard/pkg/shipfile"
	"gopkg.in/yaml.v3"
)

const (
	claudeAPIURL  = "https://api.anthropic.com/v1/messages"
	claudeModel   = "claude-haiku-4-5-20251001"
	maxFileChars  = 3000
	maxFiles      = 15
	requestTimeout = 60 * time.Second
)

// AIImportResult holds the result of an AI-assisted import.
type AIImportResult struct {
	Shipfile    *shipfile.Shipfile
	RawYAML     string
	UsedAI      bool   // false if fell back to format detection
	FallbackReason string
}

// AIImporter generates shipfiles from repos using Claude.
type AIImporter struct {
	apiKey string
}

// NewAIImporter creates an AIImporter.
// apiKey is read from ANTHROPIC_API_KEY env var if empty.
func NewAIImporter(apiKey string) *AIImporter {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	return &AIImporter{apiKey: apiKey}
}

// Import analyses a cloned repository and generates a shipfile.
// Falls back to format detection if AI is unavailable or fails.
func (a *AIImporter) Import(ctx context.Context, repoDir, repoURL, serviceName string) (*AIImportResult, error) {
	if a.apiKey == "" {
		return a.fallback(repoDir, serviceName, "no ANTHROPIC_API_KEY set")
	}

	// Build repo snapshot for the prompt.
	snapshot, err := buildRepoSnapshot(repoDir)
	if err != nil {
		return a.fallback(repoDir, serviceName, fmt.Sprintf("snapshot failed: %v", err))
	}

	// Call Claude.
	rawYAML, err := a.callClaude(ctx, serviceName, repoURL, snapshot)
	if err != nil {
		return a.fallback(repoDir, serviceName, fmt.Sprintf("Claude API error: %v", err))
	}

	// Parse the returned YAML.
	var sf shipfile.Shipfile
	if err := yaml.Unmarshal([]byte(rawYAML), &sf); err != nil {
		return a.fallback(repoDir, serviceName, fmt.Sprintf("Claude returned invalid YAML: %v", err))
	}
	if sf.Service.Name == "" {
		sf.Service.Name = serviceName
	}

	return &AIImportResult{
		Shipfile: &sf,
		RawYAML:  rawYAML,
		UsedAI:   true,
	}, nil
}

// callClaude sends the repo snapshot to Claude and returns the generated YAML.
func (a *AIImporter) callClaude(ctx context.Context, serviceName, repoURL, snapshot string) (string, error) {
	prompt := buildPrompt(serviceName, repoURL, snapshot)

	reqBody := map[string]interface{}{
		"model":      claudeModel,
		"max_tokens": 2048,
		"system":     systemPrompt(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, "POST", claudeAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("Claude API returned %d: %v", resp.StatusCode, errBody)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			// Strip markdown code fences if present.
			text := block.Text
			text = strings.TrimPrefix(text, "```yaml")
			text = strings.TrimPrefix(text, "```")
			text = strings.TrimSuffix(text, "```")
			return strings.TrimSpace(text), nil
		}
	}

	return "", fmt.Errorf("no text content in Claude response")
}

// fallback attempts format detection when AI is unavailable.
func (a *AIImporter) fallback(repoDir, serviceName, reason string) (*AIImportResult, error) {
	fmt.Printf("importer: AI unavailable (%s) — using format detection\n", reason)

	sf, err := DetectAndGenerate(repoDir, serviceName)
	if err != nil {
		return nil, fmt.Errorf("importer: fallback format detection failed: %w", err)
	}

	raw, _ := yaml.Marshal(sf)
	return &AIImportResult{
		Shipfile:       sf,
		RawYAML:        string(raw),
		UsedAI:         false,
		FallbackReason: reason,
	}, nil
}

// ── Repo snapshot ─────────────────────────────────────────────────────────

// buildRepoSnapshot creates a text summary of the repo for the AI prompt.
func buildRepoSnapshot(repoDir string) (string, error) {
	var sb strings.Builder

	// File tree (top level + one level deep).
	sb.WriteString("## File tree\n")
	entries, _ := os.ReadDir(repoDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sb.WriteString("  " + e.Name() + "\n")
		if e.IsDir() {
			sub, _ := os.ReadDir(filepath.Join(repoDir, e.Name()))
			for _, s := range sub {
				if !strings.HasPrefix(s.Name(), ".") {
					sb.WriteString("    " + s.Name() + "\n")
				}
			}
		}
	}

	// Key files.
	keyFiles := []string{
		"README.md", "readme.md", "README",
		"Dockerfile", "dockerfile",
		"docker-compose.yml", "docker-compose.yaml",
		"compose.yml", "compose.yaml",
		"package.json", "go.mod", "requirements.txt",
		"Makefile", ".env.example",
	}

	fileCount := 0
	for _, name := range keyFiles {
		if fileCount >= maxFiles {
			break
		}
		path := filepath.Join(repoDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxFileChars {
			content = content[:maxFileChars] + "\n... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("\n## %s\n```\n%s\n```\n", name, content))
		fileCount++
	}

	return sb.String(), nil
}

// ── Prompts ───────────────────────────────────────────────────────────────

func systemPrompt() string {
	return `You are a Shipyard configuration generator. Shipyard is a self-hosted container platform.

Given a repository's file tree and key files, generate a shipfile.yml that describes how to build and run the service.

IMPORTANT: Return ONLY valid YAML — no markdown, no explanation, no code fences. Just the raw YAML.

The shipfile.yml format is:
service:
  name: <service-name>
  description: <brief description>
  engine:
    type: docker  # or: compose, kubernetes, nomad
  modes:
    production:
      build:
        dockerfile: Dockerfile  # or composeFile: docker-compose.yml
      runtime:
        ports:
          - name: app
            internal: <port>
            external: auto
        env:
          KEY: value

Rules:
- Use engine.type "compose" if docker-compose.yml exists
- Use engine.type "docker" if only a Dockerfile exists
- Leave image empty for source-built services
- Infer ports from Dockerfile EXPOSE or docker-compose ports
- Keep env vars to non-secret ones only
- Default to production mode only`
}

func buildPrompt(serviceName, repoURL, snapshot string) string {
	return fmt.Sprintf(`Generate a shipfile.yml for the service named "%s" from this repository: %s

%s

Return only the shipfile.yml YAML content.`, serviceName, repoURL, snapshot)
}
