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

package health

import (
	"context"
	"testing"
	"time"
)

func TestNewChecker(t *testing.T) {
	checker := NewChecker()
	if checker == nil {
		t.Fatal("NewChecker returned nil")
	}
	if len(checker.checks) != 0 {
		t.Errorf("expected 0 checks, got %d", len(checker.checks))
	}
	if checker.started {
		t.Error("expected started to be false")
	}
	if time.Since(checker.startTime) > time.Second {
		t.Error("startTime should be recent")
	}
}

func TestNewCheckerIndependence(t *testing.T) {
	a := NewChecker()
	b := NewChecker()

	a.RegisterCheck("only-a", func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusHealthy}
	})
	a.MarkStarted()

	if len(b.GetAllChecks()) != 0 {
		t.Errorf("expected checker b to have 0 checks, got %d", len(b.GetAllChecks()))
	}
	if b.IsStarted() {
		t.Error("expected checker b to remain not started")
	}
}

func TestRegisterCheck(t *testing.T) {
	checker := NewChecker()

	// Register a check
	check := func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:   "test",
			Status: StatusHealthy,
		}
	}
	checker.RegisterCheck("test", check)

	// Verify it was registered
	checks := checker.GetAllChecks()
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0] != "test" {
		t.Errorf("expected check name 'test', got %s", checks[0])
	}

	// Replace existing check
	check2 := func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:   "test2",
			Status: StatusDegraded,
		}
	}
	checker.RegisterCheck("test", check2)
	checks = checker.GetAllChecks()
	if len(checks) != 1 {
		t.Errorf("expected 1 check after replacement, got %d", len(checks))
	}

	// Replaced check is the one that runs
	results := checker.Ready(context.Background())
	if len(results) != 1 || results[0].Status != StatusDegraded {
		t.Errorf("expected replaced check to run with status %s, got %+v", StatusDegraded, results)
	}
}

func TestRegisterCheckNil(t *testing.T) {
	checker := NewChecker()

	// Register nil check (should be ignored)
	checker.RegisterCheck("nil", nil)
	if checks := checker.GetAllChecks(); len(checks) != 0 {
		t.Errorf("expected 0 checks after registering nil, got %d", len(checks))
	}
}

func TestUnregisterCheck(t *testing.T) {
	checker := NewChecker()

	check := func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusHealthy}
	}
	checker.RegisterCheck("test", check)
	checker.RegisterCheck("test2", check)

	// Verify both registered
	checks := checker.GetAllChecks()
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(checks))
	}

	// Unregister one
	checker.UnregisterCheck("test")
	checks = checker.GetAllChecks()
	if len(checks) != 1 {
		t.Fatalf("expected 1 check after unregister, got %d", len(checks))
	}
	if checks[0] != "test2" {
		t.Errorf("expected 'test2' to remain, got %s", checks[0])
	}
}

func TestUnregisterCheckNonexistent(t *testing.T) {
	checker := NewChecker()
	checker.RegisterCheck("keep", func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusHealthy}
	})

	// Unregister non-existent (should not panic)
	checker.UnregisterCheck("nonexistent")
	if checks := checker.GetAllChecks(); len(checks) != 1 {
		t.Errorf("expected 1 check after unregistering nonexistent, got %d", len(checks))
	}
}

func TestMarkStarted(t *testing.T) {
	checker := NewChecker()

	if checker.IsStarted() {
		t.Error("expected IsStarted to be false initially")
	}

	checker.MarkStarted()

	if !checker.IsStarted() {
		t.Error("expected IsStarted to be true after MarkStarted")
	}

	// MarkStarted is idempotent
	checker.MarkStarted()
	if !checker.IsStarted() {
		t.Error("expected IsStarted to remain true after repeated MarkStarted")
	}
}

func TestMarkNotStarted(t *testing.T) {
	checker := NewChecker()

	// MarkNotStarted on a fresh checker is a no-op
	checker.MarkNotStarted()
	if checker.IsStarted() {
		t.Error("expected IsStarted to be false after MarkNotStarted on fresh checker")
	}

	// MarkNotStarted reverts MarkStarted
	checker.MarkStarted()
	checker.MarkNotStarted()
	if checker.IsStarted() {
		t.Error("expected IsStarted to be false after MarkNotStarted")
	}

	// Startup probe reflects the reverted state
	result := checker.Startup(context.Background())
	if result.Status != StatusUnhealthy {
		t.Errorf("expected startup status %s after MarkNotStarted, got %s", StatusUnhealthy, result.Status)
	}
}

func TestLive(t *testing.T) {
	checker := NewChecker()
	ctx := context.Background()

	result := checker.Live(ctx)

	if result.Name != "liveness" {
		t.Errorf("expected name 'liveness', got %s", result.Name)
	}
	if result.Status != StatusHealthy {
		t.Errorf("expected status %s, got %s", StatusHealthy, result.Status)
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
	if result.Latency < 0 {
		t.Error("expected non-negative latency")
	}
}

func TestLiveIgnoresRegisteredChecks(t *testing.T) {
	checker := NewChecker()
	checker.RegisterCheck("failing", func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusUnhealthy, Error: "backend down"}
	})

	// Liveness must remain healthy even when readiness checks fail
	result := checker.Live(context.Background())
	if result.Status != StatusHealthy {
		t.Errorf("expected liveness %s despite failing readiness check, got %s", StatusHealthy, result.Status)
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name           string
		checks         map[string]CheckFunc
		expectedCount  int
		expectedStatus Status
	}{
		{
			name:           "no checks",
			checks:         map[string]CheckFunc{},
			expectedCount:  1, // Default check
			expectedStatus: StatusHealthy,
		},
		{
			name: "single healthy check",
			checks: map[string]CheckFunc{
				"test": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "test",
						Status: StatusHealthy,
					}
				},
			},
			expectedCount:  1,
			expectedStatus: StatusHealthy,
		},
		{
			name: "multiple healthy checks",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "check1",
						Status: StatusHealthy,
					}
				},
				"check2": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "check2",
						Status: StatusHealthy,
					}
				},
			},
			expectedCount:  2,
			expectedStatus: StatusHealthy,
		},
		{
			name: "one unhealthy check",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "check1",
						Status: StatusHealthy,
					}
				},
				"check2": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "check2",
						Status: StatusUnhealthy,
						Error:  "connection failed",
					}
				},
			},
			expectedCount:  2,
			expectedStatus: StatusUnhealthy,
		},
		{
			name: "degraded check",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{
						Name:   "check1",
						Status: StatusDegraded,
					}
				},
			},
			expectedCount:  1,
			expectedStatus: StatusDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewChecker()
			for name, check := range tt.checks {
				checker.RegisterCheck(name, check)
			}

			ctx := context.Background()
			results := checker.Ready(ctx)

			if len(results) != tt.expectedCount {
				t.Errorf("expected %d results, got %d", tt.expectedCount, len(results))
			}

			// Verify all results have required fields
			for _, result := range results {
				if result.Name == "" {
					t.Error("result missing name")
				}
				if result.Status == "" {
					t.Error("result missing status")
				}
				if result.Latency < 0 {
					t.Error("result has negative latency")
				}
			}

			// Verify aggregate status
			status := AggregateStatus(results)
			if status != tt.expectedStatus {
				t.Errorf("expected aggregate status %s, got %s", tt.expectedStatus, status)
			}
		})
	}
}

func TestReadyFillsMissingName(t *testing.T) {
	checker := NewChecker()

	// A check that does not set its own name gets the registration name
	checker.RegisterCheck("anonymous", func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusHealthy}
	})

	results := checker.Ready(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "anonymous" {
		t.Errorf("expected registration name 'anonymous', got %s", results[0].Name)
	}
}

func TestStartup(t *testing.T) {
	checker := NewChecker()
	ctx := context.Background()

	// Before marking started
	result := checker.Startup(ctx)
	if result.Name != "startup" {
		t.Errorf("expected name 'startup', got %s", result.Name)
	}
	if result.Status != StatusUnhealthy {
		t.Errorf("expected status %s before started, got %s", StatusUnhealthy, result.Status)
	}
	if result.Message == "" {
		t.Error("expected non-empty message before started")
	}

	// After marking started
	checker.MarkStarted()
	result = checker.Startup(ctx)
	if result.Status != StatusHealthy {
		t.Errorf("expected status %s after started, got %s", StatusHealthy, result.Status)
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestStartupRevertsWithMarkNotStarted(t *testing.T) {
	checker := NewChecker()
	ctx := context.Background()

	checker.MarkStarted()
	if result := checker.Startup(ctx); result.Status != StatusHealthy {
		t.Fatalf("expected status %s after MarkStarted, got %s", StatusHealthy, result.Status)
	}

	checker.MarkNotStarted()
	if result := checker.Startup(ctx); result.Status != StatusUnhealthy {
		t.Errorf("expected status %s after MarkNotStarted, got %s", StatusUnhealthy, result.Status)
	}
}

func TestGetAllChecksEmpty(t *testing.T) {
	checker := NewChecker()
	checks := checker.GetAllChecks()
	if len(checks) != 0 {
		t.Errorf("expected 0 checks on fresh checker, got %d", len(checks))
	}
}

func TestGetAllChecksMultiple(t *testing.T) {
	checker := NewChecker()
	check := func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusHealthy}
	}
	checker.RegisterCheck("alpha", check)
	checker.RegisterCheck("beta", check)

	names := checker.GetAllChecks()
	if len(names) != 2 {
		t.Fatalf("expected 2 check names, got %d", len(names))
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("expected names alpha and beta, got %v", names)
	}
}

func TestIsHealthy(t *testing.T) {
	tests := []struct {
		name     string
		checks   map[string]CheckFunc
		expected bool
	}{
		{
			name:     "no checks",
			checks:   map[string]CheckFunc{},
			expected: true,
		},
		{
			name: "all healthy",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusHealthy}
				},
				"check2": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusHealthy}
				},
			},
			expected: true,
		},
		{
			name: "one unhealthy",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusHealthy}
				},
				"check2": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusUnhealthy}
				},
			},
			expected: false,
		},
		{
			name: "one degraded",
			checks: map[string]CheckFunc{
				"check1": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusHealthy}
				},
				"check2": func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusDegraded}
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewChecker()
			for name, check := range tt.checks {
				checker.RegisterCheck(name, check)
			}

			ctx := context.Background()
			healthy := checker.IsHealthy(ctx)

			if healthy != tt.expected {
				t.Errorf("expected IsHealthy=%v, got %v", tt.expected, healthy)
			}
		})
	}
}

func TestIsStarted(t *testing.T) {
	checker := NewChecker()

	if checker.IsStarted() {
		t.Error("expected IsStarted false on fresh checker")
	}

	checker.MarkStarted()
	if !checker.IsStarted() {
		t.Error("expected IsStarted true after MarkStarted")
	}
}

func TestUptime(t *testing.T) {
	checker := NewChecker()

	first := checker.Uptime()
	if first < 0 {
		t.Errorf("expected non-negative uptime, got %v", first)
	}

	second := checker.Uptime()
	if second < first {
		t.Errorf("expected uptime to be monotonically non-decreasing: first=%v second=%v", first, second)
	}
	if second > time.Minute {
		t.Errorf("expected uptime < 1m for fresh checker, got %v", second)
	}
}

func TestUptimeMonotonicAcrossStateChanges(t *testing.T) {
	checker := NewChecker()

	before := checker.Uptime()
	checker.MarkStarted()
	checker.MarkNotStarted()
	after := checker.Uptime()

	if after < before {
		t.Errorf("expected uptime unaffected by start markers: before=%v after=%v", before, after)
	}
}

func TestAggregateStatus(t *testing.T) {
	tests := []struct {
		name     string
		results  []CheckResult
		expected Status
	}{
		{
			name:     "empty results",
			results:  []CheckResult{},
			expected: StatusHealthy,
		},
		{
			name: "all healthy",
			results: []CheckResult{
				{Status: StatusHealthy},
				{Status: StatusHealthy},
			},
			expected: StatusHealthy,
		},
		{
			name: "one unhealthy",
			results: []CheckResult{
				{Status: StatusHealthy},
				{Status: StatusUnhealthy},
			},
			expected: StatusUnhealthy,
		},
		{
			name: "one degraded",
			results: []CheckResult{
				{Status: StatusHealthy},
				{Status: StatusDegraded},
			},
			expected: StatusDegraded,
		},
		{
			name: "unhealthy takes precedence over degraded",
			results: []CheckResult{
				{Status: StatusHealthy},
				{Status: StatusDegraded},
				{Status: StatusUnhealthy},
			},
			expected: StatusUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := AggregateStatus(tt.results)
			if status != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, status)
			}
		})
	}
}

func TestCheckResultFields(t *testing.T) {
	checker := NewChecker()

	// Register a check that sets all fields
	checker.RegisterCheck("detailed", func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:    "detailed",
			Status:  StatusDegraded,
			Message: "slow response",
			Error:   "timeout exceeded",
		}
	})

	ctx := context.Background()
	results := checker.Ready(ctx)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Name != "detailed" {
		t.Errorf("expected name 'detailed', got %s", result.Name)
	}
	if result.Status != StatusDegraded {
		t.Errorf("expected status %s, got %s", StatusDegraded, result.Status)
	}
	if result.Message != "slow response" {
		t.Errorf("expected message 'slow response', got %s", result.Message)
	}
	if result.Error != "timeout exceeded" {
		t.Errorf("expected error 'timeout exceeded', got %s", result.Error)
	}
	if result.Latency < 0 {
		t.Error("expected non-negative latency")
	}
}

func TestCheckContext(t *testing.T) {
	checker := NewChecker()

	// Register a check that respects context cancellation
	checker.RegisterCheck("context-aware", func(ctx context.Context) CheckResult {
		select {
		case <-ctx.Done():
			return CheckResult{
				Name:   "context-aware",
				Status: StatusUnhealthy,
				Error:  ctx.Err().Error(),
			}
		case <-time.After(time.Second):
			return CheckResult{
				Name:   "context-aware",
				Status: StatusHealthy,
			}
		}
	})

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := checker.Ready(ctx)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Status != StatusUnhealthy {
		t.Errorf("expected unhealthy with cancelled context, got %s", result.Status)
	}
	if result.Error == "" {
		t.Error("expected error message with cancelled context")
	}
}

func TestCheckWithTimeout(t *testing.T) {
	checker := NewChecker()

	checker.RegisterCheck("slow", func(ctx context.Context) CheckResult {
		// Create a timeout context that fires well before the simulated work completes
		ctx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
		defer cancel()

		select {
		case <-time.After(time.Second):
			return CheckResult{
				Name:   "slow",
				Status: StatusHealthy,
			}
		case <-ctx.Done():
			return CheckResult{
				Name:   "slow",
				Status: StatusUnhealthy,
				Error:  ctx.Err().Error(),
			}
		}
	})

	ctx := context.Background()
	results := checker.Ready(ctx)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Status != StatusUnhealthy {
		t.Errorf("expected unhealthy due to timeout, got %s", result.Status)
	}
	if result.Error == "" {
		t.Error("expected error message for timeout")
	}
}

// TestConcurrency verifies thread safety across register, probe, and
// unregister operations running from multiple goroutines.
func TestConcurrency(t *testing.T) {
	checker := NewChecker()
	ctx := context.Background()

	// Start multiple goroutines registering checks
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			checker.RegisterCheck(
				string(rune('a'+id)),
				func(ctx context.Context) CheckResult {
					return CheckResult{Status: StatusHealthy}
				},
			)
			done <- true
		}(i)
	}

	// Wait for all registrations
	for i := 0; i < 10; i++ {
		<-done
	}

	// Start multiple goroutines running checks
	for i := 0; i < 10; i++ {
		go func() {
			checker.Ready(ctx)
			checker.Live(ctx)
			checker.Startup(ctx)
			checker.IsHealthy(ctx)
			checker.IsStarted()
			checker.Uptime()
			done <- true
		}()
	}

	// Wait for all checks
	for i := 0; i < 10; i++ {
		<-done
	}

	// Start multiple goroutines unregistering checks
	for i := 0; i < 10; i++ {
		go func(id int) {
			checker.UnregisterCheck(string(rune('a' + id)))
			done <- true
		}(i)
	}

	// Wait for all unregistrations
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify no panics occurred and final state is valid
	if len(checker.GetAllChecks()) != 0 {
		t.Errorf("expected 0 checks after unregistering all, got %d", len(checker.GetAllChecks()))
	}
}

// BenchmarkReady measures performance of readiness checks.
func BenchmarkReady(b *testing.B) {
	checker := NewChecker()
	ctx := context.Background()

	// Register 10 checks
	for i := 0; i < 10; i++ {
		checker.RegisterCheck(
			string(rune('a'+i)),
			func(ctx context.Context) CheckResult {
				return CheckResult{
					Name:   "bench",
					Status: StatusHealthy,
				}
			},
		)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		checker.Ready(ctx)
	}
}
