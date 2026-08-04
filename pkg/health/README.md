# Health Check Package

The `health` package provides a generic health-probe framework following
Kubernetes probe semantics. It is the stack's canonical home for health
probes; downstream composers (go-xkms, go-quicbaas) consume it directly or
through the SDK re-exports in `sdk/go/types_health.go`.

## Probes

1. **Liveness** (`Live`) — is the service alive? Failing liveness triggers a
   restart. Never include dependency checks here.
2. **Readiness** (`Ready`) — can the service accept traffic? Runs all
   registered checks; failures are expected to be temporary and recoverable.
3. **Startup** (`Startup`) — has initialization completed? Fails until
   `MarkStarted()` is called.

## Usage

```go
import "github.com/jeremyhahn/go-qrdb/pkg/health"

checker := health.NewChecker()

checker.RegisterCheck("storage", func(ctx context.Context) health.CheckResult {
    if err := store.Ping(ctx); err != nil {
        return health.CheckResult{
            Name:    "storage",
            Status:  health.StatusUnhealthy,
            Message: "storage backend unreachable",
            Error:   err.Error(),
        }
    }
    return health.CheckResult{
        Name:    "storage",
        Status:  health.StatusHealthy,
        Message: "storage backend responding",
    }
})

// After all initialization is complete:
checker.MarkStarted()

// Probe handlers:
live := checker.Live(ctx)                       // always healthy while running
results := checker.Ready(ctx)                   // runs all registered checks
overall := health.AggregateStatus(results)      // healthy | degraded | unhealthy
startup := checker.Startup(ctx)                 // unhealthy until MarkStarted
```

## Status Types

```go
const (
    StatusHealthy   Status = "healthy"   // operating normally
    StatusUnhealthy Status = "unhealthy" // not functioning
    StatusDegraded  Status = "degraded"  // functioning with reduced capacity
)
```

`AggregateStatus` rules: any unhealthy → `StatusUnhealthy`; else any
degraded → `StatusDegraded`; else `StatusHealthy`.

## API Reference

- `NewChecker() *Checker` — creates a new health checker
- `RegisterCheck(name string, check CheckFunc)` — registers a check (nil ignored, same name replaces)
- `UnregisterCheck(name string)` — removes a check
- `MarkStarted()` / `MarkNotStarted()` — toggle startup completion
- `Live(ctx) CheckResult` — liveness probe
- `Ready(ctx) []CheckResult` — readiness probe (healthy default when no checks registered)
- `Startup(ctx) CheckResult` — startup probe
- `IsHealthy(ctx) bool` — true if all readiness checks pass
- `IsStarted() bool` — true after `MarkStarted`
- `Uptime() time.Duration` — time since checker creation
- `GetAllChecks() []string` — names of registered checks
- `AggregateStatus(results []CheckResult) Status` — combines results

## Thread Safety

All `Checker` methods are safe for concurrent use.

## References

- [Kubernetes Liveness, Readiness, and Startup Probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)
- [Health Check Response Format for HTTP APIs](https://datatracker.ietf.org/doc/html/draft-inadarei-api-health-check)
