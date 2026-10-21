package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TenantStatusReader reads the tenant record whose Status the auth chain
// admits a principal against. tenant.TenantServicer satisfies it; the narrow
// interface keeps the middleware's dependency to the one call the gate needs.
type TenantStatusReader interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// WithTenantStatusReader gives the auth chain the tenant records it refuses a
// principal of a non-active tenant against (checkTenantActive). The status is
// read at every authentication and every authorization, so a status change
// takes effect on the next operation of every credential and every open
// session the tenant's principals hold, a token issued before it included --
// as fresh as r answers it: wrap r in NewTenantStatusCache to bound the reads.
// Without one no tenant status is enforced.
func WithTenantStatusReader(r TenantStatusReader) Option {
	return func(m *AuthMiddleware) { m.tenants = r }
}

// AdmitIdentity refuses an identity already authenticated -- the principal of
// an open SQL session -- whose tenant is no longer active, with
// *TenantNotActiveError, the refusal its next authentication would get. A
// front end whose operations do not each pass the chain's authorization calls
// it before each one. The system tenant is never refused.
func (m *AuthMiddleware) AdmitIdentity(ctx context.Context, identity *auth.Identity) error {
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	return m.AdmitTenant(ctx, identity.TenantID)
}

// AdmitStream is AdmitIdentity for a stream that holds deliveries while its
// consumer is not reading, and it also answers how long the admission holds:
// no longer than the tenant status it was decided against is served -- what
// is left of the status cache's entry, or TenantStatusTTL after a fresh read.
// A delivery still held when that runs out is admitted again, so none is
// handed over later than TenantStatusTTL after the status read that refuses
// it could first have seen a change.
func (m *AuthMiddleware) AdmitStream(ctx context.Context, identity *auth.Identity) (time.Duration, error) {
	if identity == nil {
		return 0, auth.ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, admitIdentityTimeout)
	defer cancel()
	return m.tenantActiveFor(ctx, identity.TenantID)
}

// AdmitTenant is AdmitIdentity for a holder that keeps only the tenant of the
// sessions it admits: a connection guard asking, between statements, whether
// its tenant's open sessions may keep serving.
func (m *AuthMiddleware) AdmitTenant(ctx context.Context, tenantID string) error {
	ctx, cancel := context.WithTimeout(ctx, admitIdentityTimeout)
	defer cancel()
	return m.checkTenantActive(ctx, tenantID)
}

// admitIdentityTimeout bounds AdmitIdentity's tenant record read, which a
// caller with no request context of its own -- a SQL command -- cannot bound:
// a store that does not answer refuses the operation (StoreUnavailableError)
// rather than stall it. It is the authentication fallback's budget.
const admitIdentityTimeout = authFallbackTimeout

// checkTenantActive refuses an identity whose tenant is not active, with
// *TenantNotActiveError. It runs in finalizeIdentity and finalizeDelegated,
// beside the account's own lifecycle gate, and again in decide, at every
// authorization, so every transport running the chain applies it to an
// identity authenticated before the change as well.
//
// The system tenant is never gated: it has no tenant record, and its
// administrators are the principals who suspend, disable and reactivate
// every other tenant, so their control-plane calls on a non-active tenant
// pass. A tenant with no record was never registered and is served the way
// the store resolver serves it, as a logical-prefix tenant; a deleted one is
// refused by the resolver itself. A record with no known status fails closed.
func (m *AuthMiddleware) checkTenantActive(ctx context.Context, tenantID string) error {
	_, err := m.tenantActiveFor(ctx, tenantID)
	return err
}

// tenantActiveFor is checkTenantActive that also reports how long its answer
// holds: as long as the tenant status it was decided against is served (see
// leaseTenantStatus), and TenantStatusTTL where no status is read at all.
func (m *AuthMiddleware) tenantActiveFor(ctx context.Context, tenantID string) (time.Duration, error) {
	if m.tenants == nil || tenantid.IsSystem(tenantID) {
		return TenantStatusTTL, nil
	}
	info, holds, err := leaseTenantStatus(ctx, m.tenants, tenantID)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			return holds, nil
		}
		if errors.Is(err, seal.ErrBarrierSealed) || errors.Is(err, seal.ErrSealed) {
			return 0, &TenantStatusUnavailableError{TenantID: tenantID, Err: &BarrierSealedError{Err: err}}
		}
		return 0, &TenantStatusUnavailableError{TenantID: tenantID, Err: &StoreUnavailableError{Err: err}}
	}
	if !info.Status.IsActive() {
		return 0, &TenantNotActiveError{TenantID: tenantID, Status: string(info.Status)}
	}
	return holds, nil
}

// leaseTenantStatus reads tenantID's record through reader, with how much
// longer the answer is served: what is left of the status cache's entry it
// came from, or TenantStatusTTL for a reader that does not cache and so
// answered from the store just now.
func leaseTenantStatus(ctx context.Context, reader TenantStatusReader, tenantID string) (*tenant.TenantInfo, time.Duration, error) {
	if l, ok := reader.(tenantStatusLeaser); ok {
		return l.leaseTenant(ctx, tenantID)
	}
	info, err := reader.GetTenant(ctx, tenantID)
	return info, TenantStatusTTL, err
}
