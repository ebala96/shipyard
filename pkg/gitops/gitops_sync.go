// Package gitops implements GitOps-style continuous deployment for Shipyard.
// Services can be configured to automatically redeploy when their git branch changes.
//
// Flow:
//
//	GitHub pushes → POST /api/v1/gitops/:name/webhook
//	             → Shipyard pulls latest commit
//	             → runs 7-stage IaC pipeline
//	             → redeploys the service
//	             → publishes NATS event
package gitops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/shipyard/shipyard/pkg/store"
)

const prefixGitOps = "/shipyard/gitops/"

// SyncConfig holds the GitOps configuration for one service.
type SyncConfig struct {
	ServiceName   string    `json:"serviceName"`
	RepoURL       string    `json:"repoURL"`
	Branch        string    `json:"branch"`         // branch to track (default: main)
	WebhookSecret string    `json:"webhookSecret"`  // HMAC secret for validating GitHub webhooks
	AutoDeploy    bool      `json:"autoDeploy"`     // true = deploy on push
	LastSyncedSHA string    `json:"lastSyncedSHA"`  // last deployed commit
	LastSyncedAt  time.Time `json:"lastSyncedAt"`
	CreatedAt     time.Time `json:"createdAt"`
	Enabled       bool      `json:"enabled"`
}

// SyncStatus is returned after a sync attempt.
type SyncStatus struct {
	ServiceName string    `json:"serviceName"`
	PreviousSHA string    `json:"previousSHA"`
	CurrentSHA  string    `json:"currentSHA"`
	Changed     bool      `json:"changed"`
	Deployed    bool      `json:"deployed"`
	Error       string    `json:"error,omitempty"`
	SyncedAt    time.Time `json:"syncedAt"`
}

// Manager manages GitOps configs in etcd and triggers syncs.
type Manager struct {
	store *store.Store
}

// NewManager creates a GitOps manager.
func NewManager(st *store.Store) *Manager {
	return &Manager{store: st}
}

// Configure saves or updates a GitOps config for a service.
func (m *Manager) Configure(ctx context.Context, cfg *SyncConfig) error {
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = time.Now()
	}
	cfg.Enabled = true

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return m.store.RawPut(ctx, prefixGitOps+cfg.ServiceName, string(data))
}

// Get retrieves the GitOps config for a service.
func (m *Manager) Get(ctx context.Context, serviceName string) (*SyncConfig, error) {
	var cfg SyncConfig
	found, err := m.store.RawGet(ctx, prefixGitOps+serviceName, &cfg)
	if err != nil || !found {
		return nil, err
	}
	return &cfg, nil
}

// List returns all GitOps configs.
func (m *Manager) List(ctx context.Context) ([]*SyncConfig, error) {
	var configs []*SyncConfig
	err := m.store.RawList(ctx, prefixGitOps, func(key string, raw []byte) error {
		var cfg SyncConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil
		}
		configs = append(configs, &cfg)
		return nil
	})
	return configs, err
}

// Delete removes a GitOps config.
func (m *Manager) Delete(ctx context.Context, serviceName string) error {
	return m.store.RawDelete(ctx, prefixGitOps+serviceName)
}

// Sync pulls the latest commit for a service and returns whether it changed.
// The caller is responsible for triggering the actual deploy.
func (m *Manager) Sync(ctx context.Context, serviceName, contextDir string) (*SyncStatus, error) {
	cfg, err := m.Get(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("gitops: config not found for %q: %w", serviceName, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("gitops: no config for %q — set up GitOps first", serviceName)
	}
	if !cfg.Enabled {
		return &SyncStatus{ServiceName: serviceName, Error: "gitops is disabled for this service"}, nil
	}

	status := &SyncStatus{
		ServiceName: serviceName,
		PreviousSHA: cfg.LastSyncedSHA,
		SyncedAt:    time.Now(),
	}

	// Open existing repo and pull latest.
	repo, err := gogit.PlainOpen(contextDir)
	if err != nil {
		return nil, fmt.Errorf("gitops: failed to open repo at %q: %w", contextDir, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("gitops: failed to get worktree: %w", err)
	}

	pullErr := wt.PullContext(ctx, &gogit.PullOptions{
		RemoteName:    "origin",
		ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
		Force:         true,
	})
	if pullErr != nil && pullErr != gogit.NoErrAlreadyUpToDate {
		return nil, fmt.Errorf("gitops: pull failed: %w", pullErr)
	}

	// Get current HEAD SHA.
	ref, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("gitops: failed to read HEAD: %w", err)
	}
	currentSHA := ref.Hash().String()
	status.CurrentSHA = currentSHA
	status.Changed = currentSHA != cfg.LastSyncedSHA

	log.Printf("gitops: %q — SHA %s (changed=%v)", serviceName, currentSHA[:8], status.Changed)

	// Update last synced SHA in etcd.
	cfg.LastSyncedSHA = currentSHA
	cfg.LastSyncedAt = time.Now()
	if err := m.Configure(ctx, cfg); err != nil {
		log.Printf("gitops: failed to update sync record: %v", err)
	}

	return status, nil
}

// ValidateWebhookSignature verifies a GitHub webhook HMAC-SHA256 signature.
// GitHub sends X-Hub-Signature-256: sha256=<hex>
func ValidateWebhookSignature(secret, signature string, body []byte) bool {
	if secret == "" {
		return true // no secret configured — allow all
	}
	expected := "sha256=" + computeHMAC(secret, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func computeHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ExtractPushBranch extracts the branch name from a GitHub push webhook payload.
// The ref field looks like "refs/heads/main".
func ExtractPushBranch(payload []byte) string {
	var p struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Ref, "refs/heads/")
}

// ExtractHeadSHA extracts the HEAD commit SHA from a GitHub push webhook payload.
func ExtractHeadSHA(payload []byte) string {
	var p struct {
		HeadCommit struct {
			ID string `json:"id"`
		} `json:"head_commit"`
		After string `json:"after"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	if p.After != "" {
		return p.After
	}
	return p.HeadCommit.ID
}
