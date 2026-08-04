// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package health provides a generic Kubernetes-style health-probe framework
// with liveness, readiness, and startup probes. Components register named
// CheckFunc callbacks with a Checker, which aggregates their results into
// an overall Status suitable for exposure through HTTP probe endpoints.
package health

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Status represents the health status of a component.
type Status string

const (
	// StatusHealthy indicates the component is operating normally.
	StatusHealthy Status = "healthy"
	// StatusUnhealthy indicates the component is not functioning.
	StatusUnhealthy Status = "unhealthy"
	// StatusDegraded indicates the component is functioning but with reduced capacity.
	StatusDegraded Status = "degraded"
)

// CheckResult represents the result of a single health check.
type CheckResult struct {
	// Name is the identifier for this health check.
	Name string `json:"name"`
	// Status is the health status of the component.
	Status Status `json:"status"`
	// Message provides additional context about the status.
	Message string `json:"message,omitempty"`
	// Latency is how long the check took to execute.
	Latency time.Duration `json:"latency"`
	// Error contains error details if the check failed.
	Error string `json:"error,omitempty"`
}

// CheckFunc is a function that performs a health check.
// It should return quickly (ideally < 1 second) and indicate component health.
type CheckFunc func(ctx context.Context) CheckResult

// Checker manages health checks following Kubernetes probe semantics.
//
// It supports three types of probes:
//   - Liveness: Is the service alive? (should it be restarted?)
//   - Readiness: Can the service accept requests? (should it receive traffic?)
//   - Startup: Has initialization completed? (delay liveness/readiness until ready)
//
// See: https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
type Checker struct {
	mu        sync.RWMutex
	started   bool
	startTime time.Time
	checks    map[string]CheckFunc
}

// NewChecker creates a new health checker.
func NewChecker() *Checker {
	return &Checker{
		checks:    make(map[string]CheckFunc),
		startTime: time.Now(),
	}
}

// RegisterCheck adds a health check with the given name.
// If a check with this name already exists, it will be replaced.
// A nil check is ignored.
func (c *Checker) RegisterCheck(name string, check CheckFunc) {
	if check == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = check
}

// UnregisterCheck removes a health check.
func (c *Checker) UnregisterCheck(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.checks, name)
}

// MarkStarted marks the service as fully started and ready.
// This should be called after all initialization is complete.
func (c *Checker) MarkStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = true
}

// MarkNotStarted marks the service as not started.
// This is useful for testing or graceful shutdown scenarios.
func (c *Checker) MarkNotStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = false
}

// Live performs a liveness check.
//
// Liveness probes determine if the service is alive. If a liveness probe
// fails, Kubernetes will restart the container.
//
// The liveness check should only fail if the service is in an unrecoverable
// state and needs to be restarted. Temporary failures (network issues, etc.)
// should NOT cause liveness to fail.
//
// Liveness simply reports that the process is running; it does not run
// registered checks.
func (c *Checker) Live(ctx context.Context) CheckResult {
	start := time.Now()
	return CheckResult{
		Name:    "liveness",
		Status:  StatusHealthy,
		Message: "Service is alive",
		Latency: time.Since(start),
	}
}

// Ready performs a readiness check by running all registered health checks.
//
// Readiness probes determine if the service can accept traffic. If a readiness
// probe fails, Kubernetes will remove the pod from service endpoints.
//
// The readiness check should fail if the service cannot handle requests due to:
//   - Backend connectivity issues
//   - Resource exhaustion
//   - Dependencies being unavailable
//
// Unlike liveness, readiness failures are expected to be temporary and recoverable.
func (c *Checker) Ready(ctx context.Context) []CheckResult {
	c.mu.RLock()
	checks := make(map[string]CheckFunc, len(c.checks))
	for name, check := range c.checks {
		checks[name] = check
	}
	c.mu.RUnlock()

	results := make([]CheckResult, 0, len(checks))
	for name, check := range checks {
		start := time.Now()
		result := check(ctx)
		result.Latency = time.Since(start)
		// Ensure name is set even if check doesn't set it
		if result.Name == "" {
			result.Name = name
		}
		results = append(results, result)
	}

	// If no checks registered, return healthy by default
	if len(results) == 0 {
		return []CheckResult{{
			Name:    "default",
			Status:  StatusHealthy,
			Message: "No readiness checks configured",
			Latency: 0,
		}}
	}

	return results
}

// Startup performs a startup check.
//
// Startup probes determine if the application has finished starting up.
// Kubernetes will not check liveness or readiness until startup succeeds.
//
// This is useful for services that have long initialization times.
// The startup check fails until MarkStarted() is called.
func (c *Checker) Startup(ctx context.Context) CheckResult {
	start := time.Now()

	c.mu.RLock()
	started := c.started
	startTime := c.startTime
	c.mu.RUnlock()

	if !started {
		return CheckResult{
			Name:    "startup",
			Status:  StatusUnhealthy,
			Message: "Service initialization not complete",
			Latency: time.Since(start),
		}
	}

	return CheckResult{
		Name:    "startup",
		Status:  StatusHealthy,
		Message: fmt.Sprintf("Service fully initialized (uptime: %s)", time.Since(startTime).Round(time.Second)),
		Latency: time.Since(start),
	}
}

// GetAllChecks returns the names of all registered checks.
func (c *Checker) GetAllChecks() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.checks))
	for name := range c.checks {
		names = append(names, name)
	}
	return names
}

// IsHealthy returns true if all readiness checks pass.
func (c *Checker) IsHealthy(ctx context.Context) bool {
	results := c.Ready(ctx)
	for _, result := range results {
		if result.Status != StatusHealthy {
			return false
		}
	}
	return true
}

// IsStarted returns true if the service has been marked as started.
func (c *Checker) IsStarted() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.started
}

// Uptime returns how long the service has been running.
func (c *Checker) Uptime() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.startTime)
}

// AggregateStatus returns the overall status based on check results:
//   - If all checks are healthy, returns StatusHealthy
//   - If any check is unhealthy, returns StatusUnhealthy
//   - If any check is degraded (and none unhealthy), returns StatusDegraded
func AggregateStatus(results []CheckResult) Status {
	hasUnhealthy := false
	hasDegraded := false

	for _, result := range results {
		switch result.Status {
		case StatusUnhealthy:
			hasUnhealthy = true
		case StatusDegraded:
			hasDegraded = true
		}
	}

	if hasUnhealthy {
		return StatusUnhealthy
	}
	if hasDegraded {
		return StatusDegraded
	}
	return StatusHealthy
}
