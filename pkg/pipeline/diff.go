package pipeline

// DiffAction classifies what a deploy will do.
type DiffAction string

const (
	ActionCreate    DiffAction = "create"
	ActionUpdate    DiffAction = "update"
	ActionUnchanged DiffAction = "unchanged"
	ActionDestroy   DiffAction = "destroy"
)

// DiffPlan is the output of Stage 4 — what will change.
type DiffPlan struct {
	ServiceName string
	Mode        string
	Action      DiffAction
	Changes     []Change
}

// Change describes a single field that will be modified.
type Change struct {
	Field string
	Old   string
	New   string
}

// NoChange returns true when the deploy would be a no-op.
func (p *DiffPlan) NoChange() bool {
	return p.Action == ActionUnchanged
}

// Summary returns a human-readable summary of the plan.
func (p *DiffPlan) Summary() string {
	switch p.Action {
	case ActionCreate:
		return "create new stack"
	case ActionUpdate:
		return "update existing stack"
	case ActionUnchanged:
		return "no changes"
	case ActionDestroy:
		return "destroy stack"
	default:
		return string(p.Action)
	}
}
