package healthcheck

import (
	"context"
	"sync"
	"time"
)

// defaultCheckTimeout bounds each individual health check so one slow/hung
// component can't stall the whole probe endpoint. Checks must honor ctx cancellation.
const defaultCheckTimeout = 5 * time.Second

// Status represents component health status.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

// ComponentResult is the result of a single component check.
type ComponentResult struct {
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Result is the aggregate result of all health checks.
type Result struct {
	Components map[string]ComponentResult `json:"components"`
	Status     Status                     `json:"status"`
}

// Checker performs a health check for a component.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Registry holds checkers and runs them.
type Registry interface {
	Register(checker Checker)
	CheckAll(ctx context.Context) *Result
}

type registry struct {
	checkers []Checker
	mu       sync.RWMutex
}

// NewRegistry returns a new Registry.
func NewRegistry() Registry {
	return &registry{checkers: make([]Checker, 0)}
}

// Register adds a checker to the registry.
func (r *registry) Register(checker Checker) {
	if checker == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkers = append(r.checkers, checker)
}

// CheckAll runs all registered checkers and returns the aggregate result.
func (r *registry) CheckAll(ctx context.Context) *Result {
	r.mu.RLock()
	checkers := make([]Checker, len(r.checkers))
	copy(checkers, r.checkers)
	r.mu.RUnlock()

	result := &Result{
		Status:     StatusUp,
		Components: make(map[string]ComponentResult),
	}

	// Run the checks concurrently, each under its own timeout, so a single slow check
	// bounds only itself instead of serially stalling the probe.
	type outcome struct {
		name string
		res  ComponentResult
	}
	outcomes := make([]outcome, len(checkers))

	var wg sync.WaitGroup
	for i, c := range checkers {
		wg.Add(1)
		go func(i int, c Checker) {
			defer wg.Done()

			checkCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
			defer cancel()

			if err := c.Check(checkCtx); err != nil {
				outcomes[i] = outcome{name: c.Name(), res: ComponentResult{Status: StatusDown, Message: err.Error()}}
			} else {
				outcomes[i] = outcome{name: c.Name(), res: ComponentResult{Status: StatusUp}}
			}
		}(i, c)
	}
	wg.Wait()

	for i := range outcomes {
		o := &outcomes[i]
		result.Components[o.name] = o.res
		if o.res.Status == StatusDown {
			result.Status = StatusDown
		}
	}

	return result
}
