// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// AuditEntitlementLookup answers whether a tenant's replicated-audit
// entitlement is on. *tenant.TenantService.ReplicatedAuditEnabled satisfies
// it directly.
type AuditEntitlementLookup interface {
	ReplicatedAuditEnabled(ctx context.Context, tenantID string) (bool, error)
}

// OptInAuditLogger wraps an audit.AuditLogger (in production, a
// *ReplicatedLogger) behind a per-tenant entitlement check. Per
// quota.Entitlements.AllowReplicatedAudit
// (docs/audit/architecture.md#cluster-wide-replicated-trail), replicated
// audit is opt-in: when a tenant's entitlement is off, Emit and Query are
// complete no-ops for that tenant -- never reaching inner, and so never
// proposing to Raft -- indistinguishable from "no audit logger configured"
// at all. This is a REAL runtime gate checked on every call, not a property
// of what happened to be wired at boot: a tenant whose entitlement is
// toggled takes effect on its very next request, with no restart.
type OptInAuditLogger struct {
	inner       audit.AuditLogger
	entitlement AuditEntitlementLookup

	// platformEnabled governs the system tenant's own platform-event trail.
	// The system tenant is never provisioned through the per-tenant
	// entitlements path (ReplicatedAuditEnabled always answers false for
	// it, deliberately), so its posture needs its own switch, resolved once
	// at boot by the composer.
	platformEnabled bool
}

// Compile-time interface check.
var _ audit.AuditLogger = (*OptInAuditLogger)(nil)

// NewOptInAuditLogger returns an OptInAuditLogger wrapping inner, gated by
// entitlement for every ordinary tenant and by platformEnabled for the
// system tenant and untenanted platform events.
func NewOptInAuditLogger(inner audit.AuditLogger, entitlement AuditEntitlementLookup, platformEnabled bool) *OptInAuditLogger {
	return &OptInAuditLogger{inner: inner, entitlement: entitlement, platformEnabled: platformEnabled}
}

// allowed reports whether target's events may reach inner: platformEnabled
// for the system tenant, the entitlement lookup for everyone else.
func (l *OptInAuditLogger) allowed(ctx context.Context, target string) (bool, error) {
	if tenantid.IsSystem(target) {
		return l.platformEnabled, nil
	}
	return l.entitlement.ReplicatedAuditEnabled(ctx, target)
}

// Emit implements audit.AuditLogger. An entitlement-check failure (a store
// read error, not an off answer) is propagated rather than swallowed as
// "off": the caller cannot tell the difference between "this tenant may not
// use replicated audit" and "replicated audit is broken" if both answer
// nil, and under fail-closed only the former may safely let a gated
// operation through unrecorded.
func (l *OptInAuditLogger) Emit(ctx context.Context, ev audit.AuditEvent) error {
	on, err := l.allowed(ctx, routingTenant(ev.TenantID))
	if err != nil {
		return err
	}
	if !on {
		return nil
	}
	return l.inner.Emit(ctx, ev)
}

// Query implements audit.AuditLogger. A tenant whose entitlement is off
// answers Total: -1, "not answerable here" -- the same convention
// NoopLogger and SlogSink already use -- rather than an empty Total: 0
// page, which would read as "no events" rather than "no trail".
func (l *OptInAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	on, err := l.allowed(ctx, routingTenant(f.TenantID))
	if err != nil {
		return nil, err
	}
	if !on {
		return &audit.Page{Total: -1}, nil
	}
	return l.inner.Query(ctx, f)
}

// Close implements audit.AuditLogger, delegating to inner.
func (l *OptInAuditLogger) Close() error { return l.inner.Close() }

// TenantChainVerifier is implemented by an audit.AuditLogger that can also
// verify one tenant's replicated hash-chain continuity. *ReplicatedLogger
// satisfies it directly.
type TenantChainVerifier interface {
	VerifyTenantChain(ctx context.Context, tenantID string) (*audit.ChainContinuityReport, error)
}

// VerifyTenantChain reports tenantID's replicated hash-chain continuity,
// gated by the same per-tenant entitlement as Emit/Query: a tenant whose
// entitlement is off is refused with *ReplicatedAuditNotEntitledError
// rather than a misleading "verified" report for a chain that was never
// written to in the first place.
func (l *OptInAuditLogger) VerifyTenantChain(ctx context.Context, tenantID string) (*audit.ChainContinuityReport, error) {
	target := routingTenant(tenantID)
	on, err := l.allowed(ctx, target)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, &ReplicatedAuditNotEntitledError{TenantID: target}
	}
	verifier, ok := l.inner.(TenantChainVerifier)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return verifier.VerifyTenantChain(ctx, target)
}
