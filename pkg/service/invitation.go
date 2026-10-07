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
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// InvitationServicer is the one service every transport's invite and redeem
// verbs delegate to. It is how a user gets a first certificate for a key
// nobody else ever held: the administrator creates the account pending and
// hands the user a one-time token, and the user enrols THEIR OWN key with it.
type InvitationServicer interface {
	// InviteUser creates a pending account in the caller's tenant -- or
	// re-invites one that is still pending -- and returns a single-use
	// enrolment token that expires after the configured TTL. The token is
	// returned exactly once and stored only as a digest. The caller is
	// authorized for users:invite (and roles:assign for any roles) by the
	// transport; the account must hold no ceremony-only role and may be an
	// Owner only when the caller is -- the same rule certs:issue applies
	// (certTargetGuard), since direct issuance and invitation both decide
	// who may hold a certificate for the account.
	InviteUser(ctx context.Context, req *InviteUserRequest) (*Invitation, error)

	// RedeemInvitation is UNAUTHENTICATED: the token is the authority. The
	// redeemer presents the token and a CSR for their own key (the CSR's
	// self-signature is the proof of possession) and gets a certificate for
	// the invited account signed by the tenant's Key B CA -- or, with
	// CertPEM, binds their own certificate over that key after verifying it
	// chains to the tenant's trust anchors. The token is consumed and the
	// account activated with the binding; a token that is unknown, for
	// another tenant, expired, already used, or for an account that is no
	// longer pending is refused with one opaque *InvitationInvalidError. A
	// genuine storage or transport fault behind the lookup or the consuming
	// write is NOT folded into that answer -- it is returned as itself, so
	// it surfaces as retryable/unavailable rather than as a forged token.
	RedeemInvitation(ctx context.Context, req *RedeemInvitationRequest) (*EnrolledCertificate, error)
}

// InviteUserRequest invites Username into TenantID, the caller's own tenant.
// Email, Name and RoleIDs describe a new account; re-inviting an existing
// pending account takes none of them.
type InviteUserRequest struct {
	TenantID string
	Username string
	Email    string
	Name     string
	RoleIDs  []string
	// KeyPinFingerprint, when set, pins the invitation to one key: the SPKI
	// thumbprint (base64url SHA-256 of the SubjectPublicKeyInfo) the invited
	// user sent out of band. Only a CSR or certificate over exactly that key
	// can redeem it, so a stolen token is useless without the user's private
	// key. Required for every invitation when auth.invitation_require_key_pin
	// is on.
	KeyPinFingerprint string
}

// Invitation is an issued invitation. Token is the plaintext bearer secret:
// it is returned once, here, and must reach the invited user out of band.
type Invitation struct {
	ID        uint64
	Token     string
	ExpiresAt time.Time
	UserID    uint64
	Username  string
	TenantID  string
	// Created reports that the account was created by this invitation
	// rather than re-invited.
	Created bool
	// KeyPinFingerprint is the key the invitation is pinned to; empty for
	// an unpinned invitation.
	KeyPinFingerprint string
}

// RedeemInvitationRequest redeems Token in TenantID. CSRPEM is always
// required: it carries the proof that the redeemer holds the key. CertPEM
// (leaf first) and ChainPEM, when given, are a bring-your-own certificate
// over that same key.
type RedeemInvitationRequest struct {
	TenantID string
	Token    string
	CSRPEM   []byte
	CertPEM  []byte
	ChainPEM []byte
}

// InvitationStore is the invitation record store: *invite.TenantStore.
type InvitationStore interface {
	Create(ctx context.Context, userID, tenantID string, ttl time.Duration, opts ...invite.CreateOption) (string, *invite.Entity, error)
	Lookup(ctx context.Context, tenantID, token string) (*invite.Entity, error)
	Redeem(ctx context.Context, tenantID, token string) (*invite.Entity, error)
}

// InvitationUsers is the user service an InvitationService needs: the
// tenant-routed user service, which also activates a pending account with
// its first binding.
type InvitationUsers interface {
	user.UserServicer
	user.CertActivator
}

// TenantGetter reads a tenant's record; tenant.TenantServicer implements it.
// A tenant that does not exist is an error (tenant.ErrTenantNotFound).
type TenantGetter interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// InvitationDeps are the collaborators of an InvitationService. Invitations,
// Users and TTL are required; Certs is required to redeem with a CSR alone,
// Anchors to redeem with a bring-your-own certificate, and Tenants is
// required to redeem at all (an unauthenticated caller names the tenant).
type InvitationDeps struct {
	Invitations InvitationStore
	Users       InvitationUsers
	Certs       cert.CertProvider
	Anchors     trustanchor.Source
	Tenants     TenantGetter
	// TTL is how long an invitation stays redeemable (auth.invitation_ttl).
	TTL time.Duration
	// MaxCertTTL is the validity of a certificate issued at redemption
	// (auth.max_client_cert_ttl); zero is the issuer's own default.
	MaxCertTTL time.Duration
	// TrustDomain is the SPIFFE trust domain the mTLS authenticator pins.
	TrustDomain string
	// AuditLogger records user.invite and cert.enroll. Nil disables them.
	AuditLogger audit.AuditLogger
	// AuditPolicy is the node's audit failure posture (auth.audit_failure_policy)
	// for redemption's gating record, exactly as CertEnrollmentService applies
	// it to SignCertificate and EnrollCertificate. The zero value, FailClosed,
	// is the default.
	AuditPolicy audit.FailurePolicy
	// Now is the clock for bring-your-own certificates; nil is time.Now.
	Now func() time.Time
	// RequireKeyPin refuses an invitation that carries no key pin
	// (auth.invitation_require_key_pin), for compliance profiles that must
	// never issue a bearer-only token.
	RequireKeyPin bool
	// RedeemLimiter throttles redemption -- the one verb any caller may make
	// with no credential -- per source address (RedeemPerSource) and per
	// tenant (RedeemPerTenant), on the shared rate-limit engine
	// (pkg/ratelimit). A throttled redemption gets the same opaque refusal as
	// a bad token. Nil disables throttling.
	RedeemLimiter   ratelimit.Limiter
	RedeemPerSource ratelimit.Policy
	RedeemPerTenant ratelimit.Policy
}

// InvitationService is the production InvitationServicer.
type InvitationService struct {
	invites     InvitationStore
	users       InvitationUsers
	certs       cert.CertProvider
	anchors     trustanchor.Source
	tenants     TenantGetter
	ttl         time.Duration
	maxCertTTL  time.Duration
	trustDomain string
	audit       audit.AuditLogger
	auditPolicy audit.FailurePolicy
	now         func() time.Time
	requirePin  bool
	limiter     ratelimit.Limiter
	perSource   ratelimit.Policy
	perTenant   ratelimit.Policy
}

// Compile-time interface check.
var _ InvitationServicer = (*InvitationService)(nil)

// NewInvitationService returns the production InvitationServicer.
func NewInvitationService(deps InvitationDeps) *InvitationService {
	s := &InvitationService{
		invites:     deps.Invitations,
		users:       deps.Users,
		certs:       deps.Certs,
		anchors:     deps.Anchors,
		tenants:     deps.Tenants,
		ttl:         deps.TTL,
		maxCertTTL:  deps.MaxCertTTL,
		trustDomain: deps.TrustDomain,
		audit:       deps.AuditLogger,
		auditPolicy: deps.AuditPolicy,
		now:         deps.Now,
		requirePin:  deps.RequireKeyPin,
		limiter:     deps.RedeemLimiter,
		perSource:   deps.RedeemPerSource,
		perTenant:   deps.RedeemPerTenant,
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// configured reports the first required collaborator that is missing.
func (s *InvitationService) configured() error {
	switch {
	case s.invites == nil:
		return &CertEnrollmentNotConfiguredError{Capability: "invitations"}
	case s.users == nil:
		return &CertEnrollmentNotConfiguredError{Capability: "user accounts"}
	case s.ttl <= 0:
		return &CertEnrollmentNotConfiguredError{Capability: "invitation lifetime"}
	}
	return nil
}

// InviteUser implements InvitationServicer.
func (s *InvitationService) InviteUser(ctx context.Context, req *InviteUserRequest) (*Invitation, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if req == nil || req.Username == "" {
		return nil, &InvitationFieldError{Field: "username", Reason: "is required"}
	}
	if req.TenantID == "" {
		return nil, &InvitationFieldError{Field: "tenant_id", Reason: "is required"}
	}
	pin, err := s.keyPin(req.KeyPinFingerprint)
	if err != nil {
		return nil, err
	}

	existing, err := s.users.GetUserByUsername(ctx, req.TenantID, req.Username)
	var notFound *user.UserNotFoundError
	switch {
	case err == nil:
		return s.reinvite(ctx, req, existing, pin)
	case !errors.As(err, &notFound):
		return nil, err
	}

	if err := certTargetGuard(ctx, req.TenantID, &user.User{Username: req.Username, RoleIDs: req.RoleIDs}); err != nil {
		return nil, err
	}
	created, err := s.users.CreateUser(ctx, &user.CreateUserRequest{
		Username: req.Username,
		Email:    req.Email,
		Name:     req.Name,
		TenantID: req.TenantID,
		RoleIDs:  req.RoleIDs,
		// An invited account authenticates by the certificate it enrols; an
		// email login key is kept only when one is given.
		WithoutEmailLoginKey: req.Email == "",
		Pending:              true,
	})
	if err != nil {
		return nil, err
	}
	inv, err := s.issue(ctx, req.TenantID, created, true, pin)
	if err != nil {
		// The account exists only for this invitation; without one it is an
		// account nobody can ever activate. Detached from the request, so a
		// caller that gave up does not leave it behind.
		if derr := s.users.DeleteUser(context.WithoutCancel(ctx), created.ID); derr != nil {
			return nil, &InvitationUndoFailedError{UserID: created.ID, Err: err, UndoErr: derr}
		}
		return nil, err
	}
	return inv, nil
}

// reinvite issues a fresh token for an account that is still awaiting its
// first credential. Every earlier token for it stays valid until it expires,
// but only one can ever be redeemed: activation is single-use per account.
func (s *InvitationService) reinvite(ctx context.Context, req *InviteUserRequest, u *user.User, pin string) (*Invitation, error) {
	if req.Email != "" || req.Name != "" || len(req.RoleIDs) > 0 {
		return nil, &InvitationFieldError{Field: "username", Reason: "names an existing account; re-inviting it takes no email, name or roles"}
	}
	if u.Lifecycle != user.LifecyclePending || len(u.CertBindings) > 0 {
		return nil, &InvitationAccountActiveError{Username: u.Username}
	}
	if err := certTargetGuard(ctx, req.TenantID, u); err != nil {
		return nil, err
	}
	return s.issue(ctx, req.TenantID, u, false, pin)
}

// keyPin validates an invitation's key pin: empty is allowed unless the
// node requires one, and anything else must be a canonical SPKI thumbprint.
func (s *InvitationService) keyPin(fingerprint string) (string, error) {
	if fingerprint == "" {
		if s.requirePin {
			return "", &InvitationFieldError{Field: "key_pin_fingerprint", Reason: "is required: this node requires every invitation to be pinned to the invited user's key (auth.invitation_require_key_pin)"}
		}
		return "", nil
	}
	if !user.IsValidSPKIThumbprint(fingerprint) {
		return "", &InvitationFieldError{Field: "key_pin_fingerprint", Reason: "must be the base64url SHA-256 of the key's SubjectPublicKeyInfo"}
	}
	return fingerprint, nil
}

// issue creates the invitation record for u and audits it.
func (s *InvitationService) issue(ctx context.Context, tenantID string, u *user.User, created bool, pin string) (*Invitation, error) {
	token, entity, err := s.invites.Create(ctx, strconv.FormatUint(u.ID, 10), tenantID, s.ttl, invite.WithKeyPin(pin))
	if err != nil {
		return nil, err
	}
	s.emitInvited(ctx, tenantID, u, entity, created)
	return &Invitation{
		ID:        entity.ID,
		Token:     token,
		ExpiresAt: entity.ExpiresAt,
		UserID:    u.ID,
		Username:  u.Username,
		TenantID:  tenantID,
		Created:   created,

		KeyPinFingerprint: entity.KeyPin,
	}, nil
}

// emitInvited records user.invite. The token is never recorded.
func (s *InvitationService) emitInvited(ctx context.Context, tenantID string, u *user.User, entity *invite.Entity, created bool) {
	if s.audit == nil {
		return
	}
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	audit.EmitBestEffort(ctx, s.audit, "invitation", audit.AuditEvent{
		Actor:    actor,
		TenantID: tenantID,
		Resource: "user:" + strconv.FormatUint(u.ID, 10),
		Action:   "user.invite",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"tenant_id":       tenantID,
			"username":        u.Username,
			"user_id":         u.ID,
			"invitation_id":   entity.ID,
			"expires_at":      entity.ExpiresAt,
			"account_created": created,
			"key_pinned":      entity.KeyPin != "",
		},
	})
}

// RedeemInvitation implements InvitationServicer.
//
// Everything that can be refused without spending the token is judged
// first: the request, the CSR's proof of possession, the invitation, the
// account, the key's freedom, and a bring-your-own certificate's chain. Only
// then is the token consumed, and only by the one caller whose consumption
// commits. The certificate is issued after that and bound in the same commit
// that activates the account; a failure there revokes what was issued.
func (s *InvitationService) RedeemInvitation(ctx context.Context, req *RedeemInvitationRequest) (*EnrolledCertificate, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if req == nil || req.TenantID == "" {
		return nil, &InvitationFieldError{Field: "tenant_id", Reason: "is required"}
	}
	if req.Token == "" {
		return nil, &InvitationFieldError{Field: "token", Reason: "is required"}
	}
	if len(req.CSRPEM) == 0 {
		return nil, &InvitationFieldError{Field: "csr_pem", Reason: "is required: it proves the redeemer holds the key"}
	}
	if s.tenants == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "tenants"}
	}
	// The tenant id is the caller's choice and nothing has vouched for it, so
	// it is judged before anything is keyed by it: a malformed or overlong id,
	// a tenant that does not exist, and a throttled source or tenant all get
	// the opaque answer -- no CSR parse, no store resolved or cached, no
	// bucket created for an id no tenant has. Telling a guesser it is being
	// throttled, or that a tenant exists, helps only the guesser.
	if tenant.ValidateTenantIDShape(req.TenantID) != nil {
		return nil, &InvitationInvalidError{}
	}
	if !s.allowSource(ctx) {
		return nil, &InvitationInvalidError{}
	}
	if !s.tenantExists(ctx, req.TenantID) {
		return nil, &InvitationInvalidError{}
	}
	if !s.allowTenant(req.TenantID) {
		return nil, &InvitationInvalidError{}
	}
	byoc := len(req.CertPEM) > 0
	if !byoc && s.certs == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "certificate issuance"}
	}
	if byoc && s.anchors == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "trust anchors"}
	}
	pub, err := cert.ParseCSR(req.CSRPEM)
	if err != nil {
		return nil, err
	}
	thumbprint, err := user.PublicKeySPKIThumbprint(pub)
	if err != nil {
		return nil, err
	}

	// Every lookup below is confined to the invitation's tenant. The redeemer
	// is not authenticated, so nothing else names one -- and an unbound
	// context would otherwise resolve as the system tenant.
	ctx = tenant.WithTenantID(ctx, req.TenantID)

	// From here until the token is spent, a bad token -- unknown, for
	// another tenant, expired, already used -- a sealed or deleted tenant,
	// and an account that may not be activated all get the identical opaque
	// refusal: the redeemer is unauthenticated, so none of those may be
	// told apart from each other (and none may name the account). A genuine
	// storage or transport fault behind the lookup is NOT one of those: it
	// says nothing about any particular token or tenant, so it is reported
	// as itself -- retryable or unavailable, logged and metered as the
	// fault it is -- rather than folded into the opaque answer, which would
	// otherwise hide an outage as "bad token" for every caller it touches.
	entity, err := s.invites.Lookup(ctx, req.TenantID, req.Token)
	if err != nil {
		var storeErr *invite.StoreError
		if errors.As(err, &storeErr) {
			return nil, err
		}
		return nil, &InvitationInvalidError{}
	}
	// A pinned invitation enrols exactly one key. Any other is refused with
	// the same answer as a bad token -- telling the presenter that the token
	// is genuine but the key is wrong would confirm a stolen token -- and the
	// token is NOT spent, so the invited user can still redeem it.
	if entity.KeyPin != "" && entity.KeyPin != thumbprint {
		return nil, &InvitationInvalidError{}
	}
	u, err := s.pendingAccount(ctx, req.TenantID, entity)
	if err != nil {
		return nil, &InvitationInvalidError{}
	}
	if err := s.keyIsFree(ctx, thumbprint); err != nil {
		return nil, err
	}
	var verified *certenroll.BYOC
	if byoc {
		verified, err = certenroll.VerifyBYOC(ctx, s.anchors, req.TenantID, u.Username, req.CertPEM, req.ChainPEM, s.trustDomain, s.now())
		if err != nil {
			return nil, err
		}
		if verified.Thumbprint != thumbprint {
			return nil, &InvitationFieldError{Field: "csr_pem", Reason: "is not over the certificate's key"}
		}
	}

	ev := certEnrollmentEvent(ctx, req.TenantID, ActionCertEnroll, map[string]any{
		"username":      u.Username,
		"via":           "invitation",
		"invitation_id": entity.ID,
	})
	if err := gateCertAuditOn(ctx, s.audit, s.auditPolicy, ev); err != nil {
		return nil, err
	}

	if _, err := s.invites.Redeem(ctx, req.TenantID, req.Token); err != nil {
		settleCertAuditOn(ctx, s.audit, ev, err)
		var storeErr *invite.StoreError
		if errors.As(err, &storeErr) {
			return nil, err
		}
		return nil, &InvitationInvalidError{}
	}

	if verified != nil {
		if _, err := s.users.ActivateWithCertBinding(ctx, u.ID, thumbprint, verified.NotAfter.Unix()); err != nil {
			bindErr := &CertBindingFailedError{Err: err}
			settleCertAuditOn(ctx, s.audit, ev, bindErr)
			return nil, bindErr
		}
		settleCertAuditOn(ctx, s.audit, ev, nil)
		return &EnrolledCertificate{
			UserID:     u.ID,
			Username:   u.Username,
			TenantID:   req.TenantID,
			CertPEM:    trustanchor.EncodePEM(verified.Certificate),
			Thumbprint: thumbprint,
			NotAfter:   verified.NotAfter,
		}, nil
	}

	issued, err := certenroll.IssueFromCSR(ctx, s.certs, req.TenantID, u.Username, req.CSRPEM, s.maxCertTTL)
	if err != nil {
		settleCertAuditOn(ctx, s.audit, ev, err)
		return nil, err
	}
	if _, err := s.users.ActivateWithCertBinding(ctx, u.ID, issued.Thumbprint, issued.NotAfter.Unix()); err != nil {
		bindErr := revokeUnbound(ctx, s.certs, issued.Serial, err)
		settleCertAuditOn(ctx, s.audit, ev, bindErr)
		return nil, bindErr
	}
	settleCertAuditOn(ctx, s.audit, ev, nil)
	return &EnrolledCertificate{
		UserID:     u.ID,
		Username:   u.Username,
		TenantID:   req.TenantID,
		CertPEM:    issued.CertPEM,
		Serial:     issued.Serial,
		Thumbprint: issued.Thumbprint,
		NotAfter:   issued.NotAfter,
	}, nil
}

// redeemLimitKeyPrefix scopes the redemption buckets in the limiter they
// share with nothing else; NUL separates fields so no source or tenant can
// collide two keys.
const redeemLimitKeyPrefix = "invitation.redeem\x00"

// allowSource takes one token from the caller's source bucket. It runs
// before the tenant's (allowTenant), so a source that is refused never
// drains the tenant's budget for everyone else. With no limiter it allows.
func (s *InvitationService) allowSource(ctx context.Context) bool {
	if s.limiter == nil {
		return true
	}
	ok, _ := s.limiter.Allow(redeemLimitKeyPrefix+"source\x00"+redeemSourceKey(sourceHost(ctx)), s.perSource)
	return ok
}

// sourceHost returns the host part of the caller's remote address
// (auth.WithRemoteAddr, which every transport that can establish one injects
// before any gate that might bypass authentication entirely -- the same
// context value CallerPrincipal's anonymous-caller lockout already uses), or
// "" when none was recorded. An address without a port is returned whole.
func sourceHost(ctx context.Context) string {
	addr, _ := auth.RemoteAddrFromContext(ctx)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// allowTenant draws on the per-tenant redemption bucket of tenantID, a
// tenant known to exist.
func (s *InvitationService) allowTenant(tenantID string) bool {
	if s.limiter == nil {
		return true
	}
	ok, _ := s.limiter.Allow(redeemLimitKeyPrefix+"tenant\x00"+tenantID, s.perTenant)
	return ok
}

// tenantExists reports whether tenantID names a live tenant: the system
// tenant always, any other only when its record reads. Any failure to read
// it is "no" -- the redeemer gets the opaque answer either way.
func (s *InvitationService) tenantExists(ctx context.Context, tenantID string) bool {
	if tenantid.IsSystem(tenantID) {
		return true
	}
	info, err := s.tenants.GetTenant(ctx, tenantID)
	return err == nil && info != nil
}

// redeemSourceKey is the per-source bucket a redemption from host draws on.
// An IPv6 address is keyed on its /64, the smallest block a single
// subscriber is routinely given, so one host cannot spread guesses across
// the addresses of its own prefix. An IPv4 address (an IPv4-mapped IPv6 one
// included) is keyed whole. Anything that is not an IP -- a unix-domain
// socket's peer, which has no network address -- shares one "local" bucket:
// the socket's file permissions decide who may reach it at all, and every
// caller that may is a local process of this host.
func redeemSourceKey(host string) string {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "local"
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.WithZone("").Prefix(64)
	if err != nil {
		return "local"
	}
	return prefix.String()
}

// pendingAccount resolves the account an invitation activates, and refuses
// one that can no longer be activated with the same opaque error as a bad
// token: to the redeemer, a spent account and a spent token are one thing.
// An account holding a ceremony-only role or the Owner role (unless the
// caller itself holds it -- impossible here, since redemption is
// unauthenticated) is refused by the same certTargetGuard certs:issue
// applies, so an invitation can never seat an Owner or Security Officer that
// direct issuance would also refuse.
func (s *InvitationService) pendingAccount(ctx context.Context, tenantID string, entity *invite.Entity) (*user.User, error) {
	id, err := strconv.ParseUint(entity.UserID, 10, 64)
	if err != nil {
		return nil, &InvitationInvalidError{}
	}
	u, err := s.users.GetUser(ctx, id)
	if err != nil {
		var nf *user.UserNotFoundError
		if errors.As(err, &nf) {
			return nil, &InvitationInvalidError{}
		}
		return nil, err
	}
	if u.TenantID != tenantID || u.Lifecycle != user.LifecyclePending || len(u.CertBindings) > 0 {
		return nil, &InvitationInvalidError{}
	}
	if err := certTargetGuard(ctx, tenantID, u); err != nil {
		return nil, err
	}
	return u, nil
}

// keyIsFree refuses a key already bound to an account of the tenant -- the
// inviter's own, for one -- before the token is spent. The activation
// commit checks again; this answer just comes before anything is consumed.
func (s *InvitationService) keyIsFree(ctx context.Context, thumbprint string) error {
	_, err := s.users.UserByCertThumbprint(ctx, thumbprint)
	if err == nil {
		return &InvitationKeyInUseError{}
	}
	var nf *user.UserNotFoundError
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

// revokeUnbound revokes a certificate that could not be bound to its account
// and reports the binding failure. A certificate bound to nobody must not
// outlive the request; the revocation is detached from it, so a caller that
// gave up does not leave the certificate valid.
func revokeUnbound(ctx context.Context, certs cert.CertProvider, serial string, bindErr error) error {
	if _, rerr := certs.RevokeCert(context.WithoutCancel(ctx), serial); rerr != nil {
		return &CertBindingFailedError{Serial: serial, Err: bindErr, RevokeErr: rerr}
	}
	return &CertBindingFailedError{Serial: serial, Err: bindErr}
}
