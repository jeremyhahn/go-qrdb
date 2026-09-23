// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// AuthFactorRoutingConfigError is returned when a routed auth-factor service
// is constructed without a dependency the routing cannot do without.
type AuthFactorRoutingConfigError struct {
	Reason string
}

// Error returns a human-readable description of the missing dependency.
func (e *AuthFactorRoutingConfigError) Error() string {
	return "service: auth-factor routing config: " + e.Reason
}

// AuthFactorTenantMismatchError is returned when EnrollFactor is called with
// an authfactors.EnrollParams.TenantID that names a different tenant from the
// one the call's context resolves to.
//
// EnrollParams.TenantID is caller-supplied data on the wire — every transport
// hands EnrollFactorRequest.TenantID through with no validation of its own —
// not routing input: routing is decided entirely by the context tenant (see
// routingAuthFactorService's type doc). Forwarding a mismatched value
// unchanged would let an acme-context caller enrol a record that CLAIMS to be
// globex's while it physically lands in acme's store, and silently
// overwriting it would hide that the caller asked for the wrong tenant rather
// than surfacing the bug. A caller that supplies no TenantID at all gets the
// context tenant filled in; a caller that supplies the context tenant
// verbatim passes; anything else is refused.
type AuthFactorTenantMismatchError struct {
	// Requested is the tenant EnrollParams.TenantID named.
	Requested string
	// Context is the tenant the call's context resolved to.
	Context string
}

// Error returns a human-readable description of the mismatch.
func (e *AuthFactorTenantMismatchError) Error() string {
	return "service: enroll factor: requested tenant " + e.Requested + " does not match context tenant " + e.Context
}

// RoutingAuthFactorServiceConfig holds the dependencies for the tenant-routed
// ceremony/custodian auth-factor service.
type RoutingAuthFactorServiceConfig struct {
	// Factors is the list of factor implementations shared by every
	// tenant's per-tenant service.
	Factors []authfactors.Factor

	// Resolver maps a tenant ID to the KVStore backing that tenant's
	// custodian enrolment records. Required.
	Resolver tenant.StoreResolver
}

// routingAuthFactorService is an AuthFactorServicer decorator that routes
// every call to the per-tenant authFactorService backed by the KVStore the
// StoreResolver returns for the CONTEXT tenant.
//
// There is no owner-index lookup here, unlike routingUserAuthFactorService's
// Add/List/HasActiveFactor. A user id is a globally unique handle that can be
// enrolled from a different tenant context than the one it lives in, which is
// why that router resolves the OWNING tenant through tenant.IdentityIndex. A
// custodian enrolment carries no such handle: EnrollFactor's subject is a
// caller-supplied label with no registry of its own, and ChallengeFactor,
// VerifyFactor, ListFactors and RevokeFactor all key on an enrollment id
// issued by THIS call, inside the enrolling tenant. That is exactly the
// "factor id" family routingUserAuthFactorService already documents for
// Get/Remove/Rotate/Relabel/MarkUsed: the id is only ever presented back to
// the tenant that issued it, there is nothing to index, and routing on the
// context is the whole boundary. A caller presenting another tenant's
// enrollment id gets authfactors.EnrollmentNotFoundError from its own store —
// the answer that preserves the boundary — rather than a cross-tenant read.
//
// A fresh authFactorService is built on EVERY call, deliberately uncached.
// Three review rounds tried to cache one per tenant — keyed on the resolved
// store's identity, then on the tenant ID, then a hybrid of both — and each
// scheme broke a real case a production tenant.StoreResolver can produce:
// identity-keying loses track of a tenant whose resolved store is not
// comparable (a bare interface value with no pointer underneath); tenant-ID
// keying permanently pins a tenant to the first store it ever resolved to,
// so a later isolation transition (a prefix tenant migrated to a dedicated
// barrier, resolving to a genuinely different store from then on) is
// silently ignored; and neither scheme is even sound in principle, because
// StoreResolver's contract never promised Resolve returns the SAME Go value
// twice for one tenant — a resolver that legitimately builds a fresh
// comparable wrapper per call defeats an identity-keyed cache too, just less
// visibly, by handing ChallengeFactor and VerifyFactor two "identical"
// instances that do not actually share anything.
//
// The caching was solving the wrong problem. What actually broke
// verification when two calls landed on different instances was that
// authFactorService cached a pending CHALLENGE in its own memory between
// ChallengeFactor and VerifyFactor — the one piece of state this type held
// beyond the KV store both calls already share. Moving that state into the
// KV store itself (see sysChallengePrefix in authfactor_service.go) makes
// authFactorService fully stateless, which removes the requirement a cache
// existed to satisfy rather than trading one failure mode for another:
// construct it once per call, and Resolve's own live seal/isolation check
// runs on every request with nothing sitting in front of it to go stale.
//
// MIGRATION. There is deliberately none. Records enrolled through the flat,
// unrouted service before this type existed live at the bare key
// "__sys:authfactor/{id}"; a resolved store confines every key under
// "{tenant}/", the reserved "__sys:" prefix included (see
// tenant.StoreResolver), so those records are simply not reachable through
// this type — not for the system tenant either. go-qrdb is pre-release alpha
// and carries no migration guarantee across this cutover (owner ruling,
// go-qrdb#464): the records are left exactly where they are, inert, and an
// operator who needs them re-runs the custodian enrolment ceremony. See
// docs/auth-factors/engineering.md for the same decision recorded for
// consumers of the SDK surface.
type routingAuthFactorService struct {
	resolver tenant.StoreResolver
	factors  []authfactors.Factor
}

// Compile-time assertion.
var _ AuthFactorServicer = (*routingAuthFactorService)(nil)

// NewRoutingAuthFactorService constructs the tenant-routed ceremony/custodian
// auth-factor service. Resolver must be non-nil: there is no unrouted
// fallback, because a service that cannot say which tenant a call belongs to
// cannot serve a surface whose whole isolation boundary is that answer.
func NewRoutingAuthFactorService(cfg RoutingAuthFactorServiceConfig) (AuthFactorServicer, error) {
	if cfg.Resolver == nil {
		return nil, &AuthFactorRoutingConfigError{Reason: "store resolver is required"}
	}
	return &routingAuthFactorService{
		resolver: cfg.Resolver,
		factors:  cfg.Factors,
	}, nil
}

// serviceFor resolves the caller's (context) tenant store and builds the
// authFactorService bound to it, fresh, on every call. See the type doc for
// why there is no cache: authFactorService is stateless, so building one is
// cheap and there is nothing a cache would need to keep coherent.
func (r *routingAuthFactorService) serviceFor(ctx context.Context) (AuthFactorServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return NewAuthFactorService(AuthFactorServiceConfig{Factors: r.factors, KV: kv})
}

// EnrollFactor routes to the caller's (context) tenant: enrolment always
// lands in the tenant that performed it.
//
// params.TenantID is CHECKED against the context tenant, not merely
// forwarded: an empty value is filled in from the context, a value matching
// the context passes unchanged, and anything else is refused with
// *AuthFactorTenantMismatchError. A caller whose context resolves to one
// tenant but who names a different one in the request is exactly the spoof
// shape this refusal exists to catch — the record would otherwise physically
// land in the context tenant while the caller believes, and any downstream
// reader of the persisted TenantID field would be told, that it landed
// somewhere else. Overwriting the field instead of refusing was considered
// and rejected: it would silently place the enrollment in the context tenant
// while leaving the caller's mistaken belief about where it went uncorrected,
// which hides the caller's bug rather than surfacing it.
func (r *routingAuthFactorService) EnrollFactor(ctx context.Context, factorName string, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	ctxTenant := tenant.FromContext(ctx)
	switch params.TenantID {
	case "":
		params.TenantID = ctxTenant
	case ctxTenant:
		// Matches; nothing to do.
	default:
		return nil, &AuthFactorTenantMismatchError{Requested: params.TenantID, Context: ctxTenant}
	}
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.EnrollFactor(ctx, factorName, subject, params)
}

// ChallengeFactor routes to the caller's tenant. An enrollment id from
// another tenant is not found here, which is the boundary rather than a
// shortcoming.
func (r *routingAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.ChallengeFactor(ctx, enrollmentID)
}

// VerifyFactor routes to the caller's tenant, matching ChallengeFactor: the
// challenge it verifies was persisted to that tenant's store, not to any
// particular *authFactorService instance, so it is found regardless of
// whether this call's instance is the same one ChallengeFactor built.
func (r *routingAuthFactorService) VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.VerifyFactor(ctx, factorName, challengeID, response)
}

// ListFactors routes to the caller's tenant. An empty subject returns every
// enrollment in that tenant, never across tenants.
func (r *routingAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.ListFactors(ctx, subject)
}

// RevokeFactor routes to the caller's tenant.
func (r *routingAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.RevokeFactor(ctx, enrollmentID)
}
