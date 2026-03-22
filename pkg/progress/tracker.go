package progress

import (
	"fmt"
	"sync"
	"time"
)

// Step represents a single onboarding step.
type Step struct {
	ID      string    `json:"id"`
	Label   string    `json:"label"`
	Status  string    `json:"status"` // pending | running | done | error
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

// Event is sent over the SSE stream.
type Event struct {
	Type    string `json:"type"`   // step | done | error | cancelled
	Step    *Step  `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// Tracker manages progress events for a single onboard operation.
type Tracker struct {
	mu        sync.Mutex
	ch        chan Event
	steps     []Step
	cancelled bool
	cancelFn  func() // called to cancel the underlying context
}

// NewTracker creates a Tracker with a buffered event channel.
func NewTracker(cancelFn func()) *Tracker {
	return &Tracker{
		ch:       make(chan Event, 32),
		cancelFn: cancelFn,
	}
}

// Step emits a step update.
func (t *Tracker) Step(id, label, status, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := Step{ID: id, Label: label, Status: status, Detail: detail, At: time.Now()}

	// Update existing step or append new one.
	found := false
	for i, existing := range t.steps {
		if existing.ID == id {
			t.steps[i] = s
			found = true
			break
		}
	}
	if !found {
		t.steps = append(t.steps, s)
	}

	t.ch <- Event{Type: "step", Step: &s}
}

// Done signals successful completion.
func (t *Tracker) Done(message string) {
	t.ch <- Event{Type: "done", Message: message}
	close(t.ch)
}

// Error signals a failure.
func (t *Tracker) Error(err error) {
	t.ch <- Event{Type: "error", Message: err.Error()}
	close(t.ch)
}

// Cancel cancels the operation and cleans up.
func (t *Tracker) Cancel() {
	t.mu.Lock()
	t.cancelled = true
	t.mu.Unlock()

	if t.cancelFn != nil {
		t.cancelFn()
	}
	t.ch <- Event{Type: "cancelled", Message: "onboarding cancelled by user"}
	close(t.ch)
}

// IsCancelled returns true if the user cancelled.
func (t *Tracker) IsCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}

// Events returns the event channel for SSE streaming.
func (t *Tracker) Events() <-chan Event {
	return t.ch
}

// Steps returns a snapshot of all steps so far.
func (t *Tracker) Steps() []Step {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]Step, len(t.steps))
	copy(result, t.steps)
	return result
}

// Registry holds active onboard trackers keyed by a session ID.
type Registry struct {
	mu       sync.RWMutex
	trackers map[string]*Tracker
}

// NewRegistry creates a Registry.
func NewRegistry() *Registry {
	return &Registry{trackers: make(map[string]*Tracker)}
}

// Register adds a tracker for a session.
func (r *Registry) Register(sessionID string, t *Tracker) {
	r.mu.Lock()
	r.trackers[sessionID] = t
	r.mu.Unlock()
}

// Get returns a tracker by session ID.
func (r *Registry) Get(sessionID string) (*Tracker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.trackers[sessionID]
	return t, ok
}

// Remove removes a tracker.
func (r *Registry) Remove(sessionID string) {
	r.mu.Lock()
	delete(r.trackers, sessionID)
	r.mu.Unlock()
}

// GenerateID creates a simple session ID from service name + timestamp.
func GenerateID(name string) string {
	return fmt.Sprintf("%s_%d", name, time.Now().UnixMilli())
}