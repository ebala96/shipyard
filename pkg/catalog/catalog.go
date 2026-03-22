// Package catalog manages the Shipyard blueprint catalog.
// Blueprints are versioned, parameterised service templates stored in etcd.
// Users can instantiate them with size profiles and custom parameters.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/store"
)

const prefixCatalog = "/shipyard/catalog/"

// ImportMode describes how the blueprint was created.
type ImportMode string

const (
	ImportModeCatalog ImportMode = "catalog" // parsed from repo metadata
	ImportModeRepo    ImportMode = "repo"    // cloned + kept on disk
	ImportModeAI      ImportMode = "ai"      // Claude-generated manifest
)

// Blueprint is a versioned, parameterised service template.
type Blueprint struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Tags        []string   `json:"tags"`
	ImportMode  ImportMode `json:"importMode"`
	LocalPath   string     `json:"localPath,omitempty"` // on-disk clone for repo/ai modes
	GitURL      string     `json:"gitURL"`
	GitBranch   string     `json:"gitBranch"`
	GitSHA      string     `json:"gitSHA,omitempty"`

	// The full resolved shipfile manifest.
	Manifest *shipfile.Shipfile `json:"manifest"`

	// Parameter definitions for instantiation.
	Parameters []Parameter `json:"parameters,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Parameter defines a user-configurable value in a blueprint.
type Parameter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
}

// PowerProfile alias — defined in profiles.go.
// Eco / Balanced / Performance / Max

// InstantiateRequest is the input to Instantiate.
type InstantiateRequest struct {
	BlueprintName string
	Parameters    map[string]string
	Profile       PowerProfile
	Mode          string // "production" | "dev"
}

// Catalog manages blueprints in etcd.
type Catalog struct {
	store *store.Store
}

// New creates a Catalog.
func New(st *store.Store) *Catalog {
	return &Catalog{store: st}
}

// Put saves a blueprint to etcd.
func (c *Catalog) Put(ctx context.Context, bp *Blueprint) error {
	if bp.CreatedAt.IsZero() {
		bp.CreatedAt = time.Now()
	}
	bp.UpdatedAt = time.Now()
	if bp.Version == "" {
		bp.Version = fmt.Sprintf("%d", time.Now().Unix())
	}

	data, err := json.Marshal(bp)
	if err != nil {
		return fmt.Errorf("catalog: marshal failed for %q: %w", bp.Name, err)
	}

	key := prefixCatalog + bp.Name
	return c.store.RawPut(ctx, key, string(data))
}

// Get retrieves a blueprint by name.
func (c *Catalog) Get(ctx context.Context, name string) (*Blueprint, error) {
	var bp Blueprint
	found, err := c.store.RawGet(ctx, prefixCatalog+name, &bp)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &bp, nil
}

// List returns all blueprints in the catalog.
func (c *Catalog) List(ctx context.Context) ([]*Blueprint, error) {
	var blueprints []*Blueprint
	err := c.store.RawList(ctx, prefixCatalog, func(key string, raw []byte) error {
		var bp Blueprint
		if err := json.Unmarshal(raw, &bp); err != nil {
			fmt.Printf("catalog: skipping malformed blueprint at %q\n", key)
			return nil
		}
		blueprints = append(blueprints, &bp)
		return nil
	})
	return blueprints, err
}

// Delete removes a blueprint from the catalog.
func (c *Catalog) Delete(ctx context.Context, name string) error {
	return c.store.RawDelete(ctx, prefixCatalog+name)
}

// Instantiate resolves a blueprint into a ready-to-deploy shipfile.
// It applies parameter substitutions and merges the selected size profile.
func (c *Catalog) Instantiate(ctx context.Context, req InstantiateRequest) (*shipfile.Shipfile, error) {
	bp, err := c.Get(ctx, req.BlueprintName)
	if err != nil {
		return nil, err
	}
	if bp == nil {
		return nil, fmt.Errorf("catalog: blueprint %q not found", req.BlueprintName)
	}
	if bp.Manifest == nil {
		return nil, fmt.Errorf("catalog: blueprint %q has no manifest", req.BlueprintName)
	}

	// Deep copy the manifest.
	data, _ := json.Marshal(bp.Manifest)
	var sf shipfile.Shipfile
	json.Unmarshal(data, &sf)

	// Apply parameter substitutions — replace {{param}} and ${param}.
	if len(req.Parameters) > 0 {
		raw, _ := json.Marshal(sf)
		s := string(raw)
		for k, v := range req.Parameters {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
			s = strings.ReplaceAll(s, "${"+k+"}", v)
		}
		json.Unmarshal([]byte(s), &sf)
	}

	// Apply power profile — Eco / Balanced / Performance / Max.
	if req.Profile != "" {
		cpu, memory, replicas := ApplyProfile(req.Profile)
		sf.Service.Scale.Instances = replicas
		sf.Service.Scale.Resources = shipfile.Resources{
			CPU:    cpu,
			Memory: memory,
		}
	}

	return &sf, nil
}

// GetProfiles returns all available power profiles.
func GetProfiles() []ProfileSpec {
	return Profiles
}
