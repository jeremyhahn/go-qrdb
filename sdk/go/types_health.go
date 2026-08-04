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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/health"
)

// ---------------------------------------------------------------------------
// Health-probe status and check types
//
// Note: the aliases use the HealthCheck* prefix because HealthStatus is
// already taken by the cluster health report (service.HealthStatus) in
// types_service.go.
// ---------------------------------------------------------------------------

// HealthCheckStatus represents the health status of a component.
type HealthCheckStatus = health.Status

const (
	// HealthCheckStatusHealthy indicates the component is operating normally.
	HealthCheckStatusHealthy = health.StatusHealthy
	// HealthCheckStatusUnhealthy indicates the component is not functioning.
	HealthCheckStatusUnhealthy = health.StatusUnhealthy
	// HealthCheckStatusDegraded indicates the component is functioning but with reduced capacity.
	HealthCheckStatusDegraded = health.StatusDegraded
)

// HealthCheckResult represents the result of a single health check.
type HealthCheckResult = health.CheckResult

// HealthCheckFunc is a function that performs a health check.
type HealthCheckFunc = health.CheckFunc

// ---------------------------------------------------------------------------
// Health checker
// ---------------------------------------------------------------------------

// HealthChecker manages health checks following Kubernetes probe semantics
// (liveness, readiness, and startup probes).
type HealthChecker = health.Checker

// NewHealthChecker creates a new health checker.
var NewHealthChecker = health.NewChecker

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

// AggregateHealthCheckStatus returns the overall status based on check
// results: unhealthy if any check is unhealthy, degraded if any check is
// degraded (and none unhealthy), healthy otherwise.
var AggregateHealthCheckStatus = health.AggregateStatus
