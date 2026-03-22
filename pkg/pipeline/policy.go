package pipeline

import (
	"fmt"
	"strings"

	"github.com/shipyard/shipyard/pkg/shipfile"
)

// PolicyReport is the output of Stage 5.
type PolicyReport struct {
	Passed   bool
	Failures []PolicyFailure
	Warnings []PolicyWarning
}

// PolicyFailure is a hard violation that blocks the deploy.
type PolicyFailure struct {
	Rule    string
	Message string
}

// PolicyWarning is a soft violation that logs but does not block.
type PolicyWarning struct {
	Rule    string
	Message string
}

// PolicyRule is a function that checks one rule.
// It returns a failure message if the rule is violated, or "" if it passes.
type PolicyRule func(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (failure string, warning string)

// PolicyEngine evaluates all registered rules.
type PolicyEngine struct {
	rules []PolicyRule
}

// NewPolicyEngine creates a PolicyEngine with the built-in rules pre-loaded.
func NewPolicyEngine() *PolicyEngine {
	e := &PolicyEngine{}
	e.AddRule(ruleNoPrivilegedPorts)
	e.AddRule(ruleMaxReplicas)
	e.AddRule(ruleNoEmptyServiceName)
	e.AddRule(ruleNoSensitiveEnvKeys)
	e.AddRule(ruleResourceLimitsRecommended)
	return e
}

// AddRule registers a custom policy rule.
func (e *PolicyEngine) AddRule(rule PolicyRule) {
	e.rules = append(e.rules, rule)
}

// Evaluate runs all rules and returns the report.
// Returns an error only if a hard failure is found.
func (e *PolicyEngine) Evaluate(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (*PolicyReport, error) {
	report := &PolicyReport{Passed: true}

	for _, rule := range e.rules {
		failure, warning := rule(req, resolved, plan)
		if failure != "" {
			report.Failures = append(report.Failures, PolicyFailure{
				Rule:    funcName(rule),
				Message: failure,
			})
			report.Passed = false
		}
		if warning != "" {
			report.Warnings = append(report.Warnings, PolicyWarning{
				Rule:    funcName(rule),
				Message: warning,
			})
		}
	}

	if !report.Passed {
		msgs := make([]string, 0, len(report.Failures))
		for _, f := range report.Failures {
			msgs = append(msgs, f.Message)
		}
		return report, fmt.Errorf("policy gate: %s", strings.Join(msgs, "; "))
	}

	return report, nil
}

// ── Built-in rules ────────────────────────────────────────────────────────

// ruleNoPrivilegedPorts blocks exposing ports below 1024 as host ports.
func ruleNoPrivilegedPorts(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (string, string) {
	for name, port := range resolved.ResolvedPorts {
		if port > 0 && port < 1024 {
			return fmt.Sprintf("port %q maps to privileged host port %d (< 1024)", name, port), ""
		}
	}
	return "", ""
}

// ruleMaxReplicas blocks deploying more than 20 instances.
func ruleMaxReplicas(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (string, string) {
	if req.Shipfile == nil {
		return "", ""
	}
	instances := req.Shipfile.Service.Scale.Instances
	if instances > 20 {
		return fmt.Sprintf("requested %d instances exceeds maximum of 20", instances), ""
	}
	return "", ""
}

// ruleNoEmptyServiceName blocks deploys with a blank service name.
func ruleNoEmptyServiceName(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (string, string) {
	if req.ServiceName == "" {
		return "service name cannot be empty", ""
	}
	return "", ""
}

// ruleNoSensitiveEnvKeys warns if env vars look like they contain raw secrets.
func ruleNoSensitiveEnvKeys(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (string, string) {
	sensitive := []string{"PASSWORD", "SECRET", "TOKEN", "PRIVATE_KEY", "API_KEY"}
	for key, val := range resolved.ResolvedEnv {
		for _, s := range sensitive {
			if strings.Contains(strings.ToUpper(key), s) && len(val) > 0 && !strings.HasPrefix(val, "vault:") {
				return "", fmt.Sprintf("env var %q looks like a raw secret — consider using vault: references", key)
			}
		}
	}
	return "", ""
}

// ruleResourceLimitsRecommended warns when no resource limits are set.
func ruleResourceLimitsRecommended(req Request, resolved *shipfile.ResolvedMode, plan *DiffPlan) (string, string) {
	if plan.Action == ActionCreate {
		if resolved.Runtime.Resources.Memory == "" && resolved.Runtime.Resources.CPU == 0 {
			return "", "no resource limits set — consider adding memory and CPU limits"
		}
	}
	return "", ""
}

// funcName returns a stable name for a rule function (used in reports).
func funcName(f PolicyRule) string {
	// Derive name from the rule function's signature via a type assertion.
	// In practice we'd use reflect but keep it simple for now.
	return "built-in"
}
