// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantServicer is the subset of tenant.TenantServicer that
// BootstrapSystemTenant requires. Using a minimal interface keeps this
// package from depending on the full tenant service dependency graph.
type TenantServicer interface {
	CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType) (*tenant.TenantInfo, error)
}

// BootstrapState describes the outcome of a BootstrapSystemTenant call.
type BootstrapState int

const (
	// BootstrapCreated means the system tenant was created successfully on
	// this call.
	BootstrapCreated BootstrapState = iota

	// BootstrapAlreadyExists means the system tenant was already present.
	// This is the normal state on every restart after the first boot.
	BootstrapAlreadyExists

	// BootstrapDeferred means the storage barrier is sealed and tenant
	// creation must be retried after the operator initializes the barrier.
	// The daemon should continue starting; this is not a fatal error.
	BootstrapDeferred

	// BootstrapNonFatal means an unexpected error occurred but the daemon
	// was allowed to continue. The Err field carries the underlying error for
	// logging.
	BootstrapNonFatal
)

// BootstrapResult is the result of a BootstrapSystemTenant call.
type BootstrapResult struct {
	// State is the outcome of the bootstrap attempt.
	State BootstrapState

	// Err is non-nil when State is BootstrapNonFatal. It is always nil for
	// BootstrapCreated, BootstrapAlreadyExists, and BootstrapDeferred.
	Err error
}

// BootstrapOption configures a BootstrapSystemTenant call.
type BootstrapOption func(*bootstrapConfig)

type bootstrapConfig struct {
	timeout    time.Duration
	logger     *slog.Logger
	tenantID   string
	tenantName string
}

func defaultBootstrapConfig() *bootstrapConfig {
	return &bootstrapConfig{
		timeout:    5 * time.Second,
		tenantID:   tenant.SystemTenantID,
		tenantName: "System",
	}
}

// WithBootstrapTimeout sets the context deadline for the CreateTenant call.
// Default: 5 seconds.
func WithBootstrapTimeout(d time.Duration) BootstrapOption {
	return func(c *bootstrapConfig) { c.timeout = d }
}

// WithBootstrapLogger provides a logger for informational messages emitted
// during bootstrap. When nil, slog.Default() is used.
func WithBootstrapLogger(l *slog.Logger) BootstrapOption {
	return func(c *bootstrapConfig) { c.logger = l }
}

// BootstrapSystemTenant ensures the structurally privileged system tenant
// exists in svc.
//
// This is the SINGLE canonical implementation of system-tenant bootstrap.
// It mirrors the control flow in go-qrdb/cmd/qrdbd/start.go so that all
// callers (go-xkms, go-quicbaas, etc.) behave identically. Any caller that
// re-implements this logic instead of calling this function is wrong.
//
// The call is annotated with tenant.WithInternalCall so the reserved-id
// check on TenantServicer.CreateTenant lets the platform create its own
// system tenant. External callers cannot fabricate this annotation because
// the underlying context key is package-private to pkg/tenant.
//
// Control flow:
//  1. Call svc.CreateTenant with tenant.SystemTenantID and IsolationBarrier.
//  2. ErrTenantAlreadyExists → BootstrapAlreadyExists (expected on restart).
//  3. seal.ErrSealed or "barrier is sealed" → BootstrapDeferred (the
//     operator must initialize the barrier before the first write succeeds).
//  4. Any other error → BootstrapNonFatal (logged; daemon continues).
//
// The function never returns a non-nil error; all outcomes are encoded in
// BootstrapResult so callers do not need to handle the error path.
func BootstrapSystemTenant(ctx context.Context, svc TenantServicer, opts ...BootstrapOption) *BootstrapResult {
	cfg := defaultBootstrapConfig()
	for _, o := range opts {
		o(cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}

	tctx, cancel := context.WithTimeout(tenant.WithInternalCall(ctx), cfg.timeout)
	defer cancel()

	_, err := svc.CreateTenant(tctx, cfg.tenantID, cfg.tenantName, tenant.IsolationBarrier)
	if err == nil {
		cfg.logger.Info("system tenant created",
			slog.String("tenant", cfg.tenantID))
		return &BootstrapResult{State: BootstrapCreated}
	}

	switch {
	case errors.Is(err, tenant.ErrTenantAlreadyExists):
		cfg.logger.Debug("system tenant already exists — idempotent",
			slog.String("tenant", cfg.tenantID))
		return &BootstrapResult{State: BootstrapAlreadyExists}

	case errors.Is(err, seal.ErrSealed) || strings.Contains(err.Error(), "barrier is sealed"):
		cfg.logger.Info("system tenant creation deferred until barrier is unsealed",
			slog.String("tenant", cfg.tenantID),
			slog.String("error", err.Error()))
		return &BootstrapResult{State: BootstrapDeferred}

	default:
		// Any other error (including context timeout) is non-fatal during
		// bootstrap: the system tenant is an idempotent artifact. Log and
		// proceed so the server comes up long enough for the operator to
		// intervene or for a retry on the next restart.
		cfg.logger.Info("system tenant bootstrap could not complete; proceeding",
			slog.String("tenant", cfg.tenantID),
			slog.String("error", err.Error()))
		return &BootstrapResult{State: BootstrapNonFatal, Err: err}
	}
}
