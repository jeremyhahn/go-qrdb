package middleware

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// OperatorIdentityAccessAction is the audit action of an operator's access to
// another tenant's identity records. The record is written twice, once to the
// platform trail and once to the target tenant's trail.
const OperatorIdentityAccessAction = "auth.operator_identity_access"

// OperatorAccessTargetMetadataKey names the target tenant in the platform
// trail's record of an operator identity access.
const OperatorAccessTargetMetadataKey = "target_tenant"

// IsolationPolicyReader is what AuthorizeOperatorIdentityAccess reads a
// target tenant's isolation policy from: the tenant record (its isolation
// type and its own policy) and the cluster floor. It is read uncached, per
// access: a tenant that has just tightened to strict refuses the next one.
type IsolationPolicyReader interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
	ClusterIsolationPolicy(ctx context.Context) (tenant.ClusterIsolationPolicy, error)
}

// WithIsolationPolicyReader gives the middleware the isolation policy
// AuthorizeOperatorIdentityAccess decides against. Without one every operator
// access to another tenant's identity records is refused
// (*OperatorAccessUnresolvedError).
func WithIsolationPolicyReader(r IsolationPolicyReader) Option {
	return func(m *AuthMiddleware) { m.isolationPolicy = r }
}

// tenantTrailRef holds the operator access tenant trail behind an atomic
// pointer, so the daemon can bind it once the services that own it exist.
type tenantTrailRef struct{ trail audit.AuditLogger }

// WithOperatorAccessTenantTrail gives the middleware the trail each target
// tenant's record of an operator identity access is written to: in
// production the cluster's replicated, per-tenant audit chain, which an
// isolated tenant keeps in its home group. Without one every operator
// access to another tenant's identity records is refused
// (*OperatorAccessUnauditedError, trail "tenant").
func WithOperatorAccessTenantTrail(t audit.AuditLogger) Option {
	return func(m *AuthMiddleware) { m.BindOperatorAccessTenantTrail(t) }
}

// BindOperatorAccessTenantTrail is WithOperatorAccessTenantTrail for a
// middleware already built: the daemon builds the middleware before the
// services owning the replicated trail.
func (m *AuthMiddleware) BindOperatorAccessTenantTrail(t audit.AuditLogger) {
	if m == nil {
		return
	}
	if t == nil {
		m.operatorTenantTrail.Store(nil)
		return
	}
	m.operatorTenantTrail.Store(&tenantTrailRef{trail: t})
}

// OperatorAccessRefusedError is the refusal of an operator's access to the
// identity records of a tenant whose effective isolation policy is strict.
type OperatorAccessRefusedError struct {
	TenantID string
	Policy   tenant.ClusterIsolationPolicy
}

func (e *OperatorAccessRefusedError) Error() string {
	return "auth/middleware: tenant " + strconv.Quote(e.TenantID) + " isolation policy is " +
		strconv.Quote(string(e.Policy)) + "; operator access to its identity records is refused"
}

// Unwrap classifies the refusal as an authorization failure.
func (e *OperatorAccessRefusedError) Unwrap() error { return auth.ErrUnauthorized }

// OperatorAccessUnresolvedError is the refusal of an operator's access to a
// tenant's identity records when the tenant's isolation policy cannot be
// read: no policy reader is wired, the tenant record is absent from the
// reader's answer, or the read failed. ClassifyAuthError reports it as
// AuthFailureUnavailable (HTTP 503, gRPC Unavailable), so a client retries a
// transient store outage; a wrapped context cancellation or deadline keeps
// its own class, and a wrapped tenant.ErrTenantNotFound keeps NotFound: a
// missing tenant answers NotFound (HTTP 404, gRPC NotFound) on REST, gRPC,
// MCP and QUIC alike.
type OperatorAccessUnresolvedError struct {
	TenantID string
	Err      error
}

func (e *OperatorAccessUnresolvedError) Error() string {
	msg := "auth/middleware: isolation policy of tenant " + strconv.Quote(e.TenantID) + " is unresolved"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *OperatorAccessUnresolvedError) Unwrap() error { return e.Err }

// OperatorAccessUnauditedError is the refusal of an operator's access to a
// tenant's identity records that could not be recorded in both trails. An
// access to a tenant's records on the operator's own authority is never
// admitted unrecorded, whatever auth.audit_failure_policy says: that policy is
// the operator's choice about the operator's own trail.
type OperatorAccessUnauditedError struct {
	TenantID string
	Trail    string
	Err      error
}

func (e *OperatorAccessUnauditedError) Error() string {
	msg := "auth/middleware: operator access to tenant " + strconv.Quote(e.TenantID) +
		" identity records could not be recorded in the " + e.Trail + " trail"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *OperatorAccessUnauditedError) Unwrap() error { return e.Err }

// AuthorizeOperatorIdentityAccess decides the platform operator's access to
// the identity records (users, roles, credentials) of tenant tenantID, and
// returns the context the access runs under: bound to tenantID, so the
// identity services resolve that tenant's store.
//
// A caller acting on its own tenant (tenantID empty or its own) is not an
// operator access: ctx is returned unchanged and nothing is decided here --
// the route's own permission governs it.
//
// Otherwise the caller must hold perm (one of the ScopeCross
// permission.UsersReadTenant family) and the target tenant must allow
// operator access: a shared (prefix) tenant always does; an isolated tenant
// does when its effective policy (tenant.EffectiveIsolationPolicy of the
// cluster floor and its own) is operator-access, and refuses it under
// strict. Every decision, admitted or refused, is recorded in the platform
// trail and in the target tenant's trail; an access that cannot be recorded
// in both is refused.
func (m *AuthMiddleware) AuthorizeOperatorIdentityAccess(ctx context.Context, tenantID string, perm permission.Permission) (context.Context, error) {
	if m == nil {
		if tenantID == "" {
			return ctx, nil
		}
		return tenant.WithTenantID(ctx, tenantID), nil
	}
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return nil, auth.ErrUnauthenticated
	}
	target := tenantid.Normalize(tenantID)
	if tenantID == "" || target == tenantid.Normalize(identity.TenantID) {
		return ctx, nil
	}
	decision := m.decideOperatorIdentityAccess(ctx, identity, target, perm)
	auditErr := m.auditOperatorIdentityAccess(ctx, identity, target, perm, decision)
	// A refused access stays refused with its own reason; an unrecordable
	// record only turns an admitted access into a refusal.
	if decision != nil {
		return nil, decision
	}
	if auditErr != nil {
		return nil, auditErr
	}
	ctx = context.WithValue(ctx, operatorAdmittedKey{}, operatorAdmission{tenant: target, perm: perm})
	if perm == permission.RolesWriteTenant || perm == permission.UsersWriteTenant {
		// The grant checks of the write (role.ValidateGrantable, through
		// ValidateAssignable and the role service's grant guard) judge the
		// operator by what it holds in its own tenant.
		ctx = auth.WithOperatorIdentityWrite(ctx, target, identity.TenantID)
	}
	return tenant.WithTenantID(ctx, target), nil
}

// OperatorAccessGate returns AuthorizeOperatorIdentityAccess for perm as a
// per-target-tenant gate (role.TenantGate): a pass over every tenant, such as
// role.ReconcileAllTenants, decides and records the operator access to each
// target before touching its identity records.
func (m *AuthMiddleware) OperatorAccessGate(perm permission.Permission) func(context.Context, string) (context.Context, error) {
	return func(ctx context.Context, tenantID string) (context.Context, error) {
		return m.AuthorizeOperatorIdentityAccess(ctx, tenantID, perm)
	}
}

// operatorAdmittedKey carries the operator access AuthorizeOperatorIdentityAccess
// admitted (and recorded) for the request, so the secondary checks of the
// same write (roles:assign in the target tenant) honour the admitted
// tenant-qualified permission instead of comparing the operator's own
// tenant against the target.
type operatorAdmittedKey struct{}

type operatorAdmission struct {
	tenant string
	perm   permission.Permission
}

// operatorAdmittedWrite reports whether ctx carries an admitted operator
// access to target for a tenant-qualified identity write (roles or users),
// the permission that stands in for roles:assign there.
func operatorAdmittedWrite(ctx context.Context, target string) bool {
	a, ok := ctx.Value(operatorAdmittedKey{}).(operatorAdmission)
	if !ok || a.tenant != tenantid.Normalize(target) {
		return false
	}
	return a.perm == permission.RolesWriteTenant || a.perm == permission.UsersWriteTenant
}

func (m *AuthMiddleware) decideOperatorIdentityAccess(ctx context.Context, identity *auth.Identity, target string, perm permission.Permission) error {
	if err := m.decide(tenant.WithTenantID(ctx, tenantid.System), identity, perm); err != nil {
		return err
	}
	if m.isolationPolicy == nil {
		return &OperatorAccessUnresolvedError{TenantID: target}
	}
	info, err := m.isolationPolicy.GetTenant(ctx, target)
	if err != nil {
		return &OperatorAccessUnresolvedError{TenantID: target, Err: err}
	}
	if info == nil {
		return &OperatorAccessUnresolvedError{TenantID: target}
	}
	// The policy governs isolated tenants only; a shared tenant is never
	// refused.
	if info.IsolationType == "" || info.IsolationType == tenant.IsolationPrefix {
		return nil
	}
	floor, err := m.isolationPolicy.ClusterIsolationPolicy(ctx)
	if err != nil {
		return &OperatorAccessUnresolvedError{TenantID: target, Err: err}
	}
	if eff := tenant.EffectiveIsolationPolicy(floor, info.IsolationPolicy); eff == tenant.ClusterIsolationPolicyStrict {
		return &OperatorAccessRefusedError{TenantID: target, Policy: eff}
	}
	return nil
}

// auditOperatorIdentityAccess writes the decision to the platform trail (the
// operator's tenant) and to the target tenant's trail. Both writes are
// attempted; the first failure refuses the access.
func (m *AuthMiddleware) auditOperatorIdentityAccess(ctx context.Context, identity *auth.Identity, target string, perm permission.Permission, decision error) error {
	result := audit.ResultSuccess
	if decision != nil {
		result = audit.ResultDenied
	}
	now := time.Now().UTC()
	operatorTenant := tenantid.Normalize(identity.TenantID)
	fail := func(trail string, err error) error {
		auditWriteFailures().With(auditFailureOutcomes[audit.FailClosed]).Inc()
		slog.Error("auth/middleware: operator identity access audit failed",
			"error", err, "trail", trail, "subject", identity.Subject,
			"target_tenant", target, "permission", perm.String())
		return &audit.GateError{Component: authorizeComponent, Err: &OperatorAccessUnauditedError{TenantID: target, Trail: trail, Err: err}}
	}
	var first error
	// The platform trail: the operator's own decision log.
	if m.audit == nil {
		first = fail("platform", nil)
	} else if err := m.audit.Log(ctx, &audit.Entry{
		Timestamp: now,
		Subject:   identity.Subject,
		TenantID:  operatorTenant,
		Action:    OperatorIdentityAccessAction,
		Resource:  perm.String(),
		Result:    result,
		Metadata: map[string]string{
			OperatorAccessTargetMetadataKey: target,
			"operator_tenant":               operatorTenant,
		},
	}); err != nil {
		first = fail("platform", err)
	}
	// The tenant trail: the target tenant's own replicated chain, which a
	// sealed, unavailable or unsupported home refuses.
	ref := m.operatorTenantTrail.Load()
	var err error
	if ref == nil {
		err = errNoOperatorTenantTrail
	} else {
		err = ref.trail.Emit(ctx, audit.AuditEvent{
			Timestamp: now,
			Actor:     identity.Subject,
			TenantID:  target,
			Resource:  perm.String(),
			Action:    OperatorIdentityAccessAction,
			Outcome:   result,
			Attributes: map[string]any{
				OperatorAccessTargetMetadataKey: target,
				"operator_tenant":               operatorTenant,
			},
		})
	}
	if err != nil {
		if tErr := fail("tenant", err); first == nil {
			first = tErr
		}
	}
	return first
}

// errNoOperatorTenantTrail is the tenant-trail failure of a middleware
// given no tenant trail to record operator access in.
var errNoOperatorTenantTrail = errors.New("auth/middleware: no tenant trail is wired for operator access")
