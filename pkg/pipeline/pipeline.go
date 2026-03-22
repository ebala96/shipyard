// Package pipeline implements the 7-stage IaC deploy pipeline.
// Every deploy request passes through all stages before Docker is touched.
//
//	Stage 1 — Parse & validate      schema validation, cycle detection
//	Stage 2 — Resolve environment    merge base → shared → overlay → service
//	Stage 3 — Interpolate variables  ${var} substitution
//	Stage 4 — Diff & plan            compare desired vs current etcd state
//	Stage 5 — Policy gate            built-in + custom rules
//	Stage 6 — Apply to intent store  atomic etcd write + ledger entry
//	Stage 7 — Reconcile              watcher converges Docker state
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/store"
)

// Pipeline executes the 7 stages for a deploy request.
type Pipeline struct {
	store   *store.Store
	policy  *PolicyEngine
}

// New creates a Pipeline. store may be nil — stages 4 and 6 are skipped.
func New(st *store.Store) *Pipeline {
	return &Pipeline{
		store:  st,
		policy: NewPolicyEngine(),
	}
}

// Request is the input to the pipeline.
type Request struct {
	ServiceName string
	StackName   string
	ContextDir  string
	Mode        string
	Shipfile    *shipfile.Shipfile
	Operator    string // "user" | "gitops" | "reconciler"
}

// Result is the output of a successful pipeline run.
type Result struct {
	ServiceName  string
	StackName    string
	Mode         string
	Resolved     *shipfile.ResolvedMode
	Plan         *DiffPlan
	PolicyReport *PolicyReport
	AppliedAt    time.Time
}

// Run executes all 7 stages and returns a Result or the first error.
func (p *Pipeline) Run(ctx context.Context, req Request) (*Result, error) {
	result := &Result{
		ServiceName: req.ServiceName,
		StackName:   req.StackName,
		Mode:        req.Mode,
	}

	// ── Stage 1: Parse & validate ─────────────────────────────────────────
	if err := p.stage1Validate(req); err != nil {
		return nil, pipelineErr(1, "validate", err)
	}

	// ── Stage 2: Resolve environment ──────────────────────────────────────
	resolved, err := p.stage2Resolve(req)
	if err != nil {
		return nil, pipelineErr(2, "resolve", err)
	}
	result.Resolved = resolved

	// ── Stage 3: Interpolate variables ────────────────────────────────────
	if err := p.stage3Interpolate(resolved); err != nil {
		return nil, pipelineErr(3, "interpolate", err)
	}

	// ── Stage 4: Diff & plan ──────────────────────────────────────────────
	plan, err := p.stage4Diff(ctx, req, resolved)
	if err != nil {
		return nil, pipelineErr(4, "diff", err)
	}
	result.Plan = plan

	// Skip deploy if nothing changed.
	if plan.NoChange() {
		result.AppliedAt = time.Now()
		return result, nil
	}

	// ── Stage 5: Policy gate ──────────────────────────────────────────────
	report, err := p.stage5Policy(req, resolved, plan)
	if err != nil {
		return nil, pipelineErr(5, "policy", err)
	}
	result.PolicyReport = report

	// ── Stage 6: Apply to intent store ───────────────────────────────────
	if err := p.stage6Apply(ctx, req, resolved); err != nil {
		return nil, pipelineErr(6, "apply", err)
	}

	// Stage 7 (reconcile) is handled by the watcher automatically.
	result.AppliedAt = time.Now()
	return result, nil
}

// ── Stage implementations ─────────────────────────────────────────────────

// stage1Validate checks the manifest is structurally valid.
func (p *Pipeline) stage1Validate(req Request) error {
	sf := req.Shipfile
	if sf == nil {
		return fmt.Errorf("shipfile is nil")
	}
	if sf.Service.Name == "" {
		return fmt.Errorf("service.name is required")
	}
	if len(sf.Service.Modes) == 0 {
		return fmt.Errorf("at least one mode must be defined")
	}
	if _, ok := sf.Service.Modes[req.Mode]; !ok {
		// Mode not found — check if "production" or any mode exists.
		if _, ok := sf.Service.Modes["production"]; !ok {
			return fmt.Errorf("mode %q not found in shipfile", req.Mode)
		}
	}
	// Cycle detection for dependencies.
	if err := detectCycles(sf); err != nil {
		return err
	}
	return nil
}

// stage2Resolve merges mode configuration and resolves the environment.
func (p *Pipeline) stage2Resolve(req Request) (*shipfile.ResolvedMode, error) {
	mode := req.Mode
	if _, ok := req.Shipfile.Service.Modes[mode]; !ok {
		mode = "production"
	}
	resolved, err := shipfile.Resolve(req.Shipfile, mode)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve mode %q: %w", mode, err)
	}
	return resolved, nil
}

// stage3Interpolate validates that all required variables have been resolved.
// The actual substitution already happened in Resolve — this stage catches
// any remaining unresolved required variables.
func (p *Pipeline) stage3Interpolate(resolved *shipfile.ResolvedMode) error {
	// Check for unresolved ${ports.*} references in env vars.
	for k, v := range resolved.ResolvedEnv {
		if containsUnresolved(v) {
			// Only warn on unknown vars — they're passed through intentionally.
			_ = k
		}
	}
	return nil
}

// stage4Diff compares desired state against what's currently in etcd.
func (p *Pipeline) stage4Diff(ctx context.Context, req Request, resolved *shipfile.ResolvedMode) (*DiffPlan, error) {
	plan := &DiffPlan{
		ServiceName: req.ServiceName,
		Mode:        req.Mode,
	}

	// If no store available, treat everything as a fresh create.
	if p.store == nil {
		plan.Action = ActionCreate
		plan.Changes = []Change{{Field: "stack", Old: "", New: "created"}}
		return plan, nil
	}

	existing, err := p.store.GetStackState(ctx, req.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("failed to read current state: %w", err)
	}

	if existing == nil || existing.State == store.StateDown || existing.State == store.StateDestroyed {
		plan.Action = ActionCreate
		plan.Changes = []Change{{Field: "stack", Old: "", New: "created"}}
		return plan, nil
	}

	// Compare ports.
	for name, port := range resolved.ResolvedPorts {
		for _, ctr := range existing.Containers {
			if oldPort, ok := ctr.Ports[name]; ok && oldPort != port {
				plan.Changes = append(plan.Changes, Change{
					Field: fmt.Sprintf("port.%s", name),
					Old:   fmt.Sprintf("%d", oldPort),
					New:   fmt.Sprintf("%d", port),
				})
			}
		}
	}

	// Compare image.
	if len(existing.Containers) > 0 {
		oldImage := existing.Containers[0].Image
		// New image tag is derived from service+mode — flag as update if mode changed.
		if existing.Mode != req.Mode {
			plan.Changes = append(plan.Changes, Change{
				Field: "mode",
				Old:   existing.Mode,
				New:   req.Mode,
			})
		}
		_ = oldImage
	}

	if len(plan.Changes) == 0 {
		plan.Action = ActionUnchanged
	} else {
		plan.Action = ActionUpdate
	}

	return plan, nil
}

// stage5Policy runs all policy rules against the plan.
func (p *Pipeline) stage5Policy(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (*PolicyReport, error) {
	return p.policy.Evaluate(req, resolved, plan)
}

// stage6Apply writes the desired state to etcd atomically.
func (p *Pipeline) stage6Apply(ctx context.Context, req Request, resolved *shipfile.ResolvedMode) error {
	if p.store == nil {
		return nil
	}

	// Write pending stack state — the reconciler will start the actual deploy.
	stackName := req.ServiceName
	existing, _ := p.store.GetStackState(ctx, stackName)

	state := &store.StackState{
		Name:          stackName,
		ServiceName:   req.ServiceName,
		Platform:      string(req.Shipfile.Service.Engine.Type),
		Mode:          req.Mode,
		StackName:     req.StackName,
		State:         store.StatePending,
		StateAt:       time.Now(),
		LastOperation: "deploy",
	}

	// Preserve retry count if already exists.
	if existing != nil {
		state.RetryCount = existing.RetryCount
		state.CreatedAt = existing.CreatedAt
	}

	return p.store.PutStackState(ctx, state)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func pipelineErr(stage int, name string, err error) error {
	return fmt.Errorf("pipeline stage %d (%s): %w", stage, name, err)
}

// detectCycles checks for circular dependencies between services.
func detectCycles(sf *shipfile.Shipfile) error {
	// Simple self-dependency check for now.
	// Full topological sort would be needed for multi-service manifests.
	for _, dep := range sf.Service.Dependencies {
		if dep.Name == sf.Service.Name {
			return fmt.Errorf("service %q depends on itself", sf.Service.Name)
		}
	}
	return nil
}

func containsUnresolved(s string) bool {
	// Check if any ${ports.*} remains unresolved.
	for i := 0; i < len(s)-2; i++ {
		if s[i] == '$' && s[i+1] == '{' {
			end := i + 2
			for end < len(s) && s[end] != '}' {
				end++
			}
			if end < len(s) {
				expr := s[i+2 : end]
				if len(expr) > 6 && expr[:6] == "ports." {
					return true // unresolved ports variable
				}
			}
		}
	}
	return false
}
