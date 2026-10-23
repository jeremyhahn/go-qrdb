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

package embedded

import (
	"context"
	"crypto/rand"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MissingLoginResolverError is returned when LoginClient is invoked
// without an injected auth.LoginResolver.
type MissingLoginResolverError struct{}

// Error returns a human-readable description.
func (e *MissingLoginResolverError) Error() string {
	return "embedded: login resolver is not configured"
}

// MissingSessionIssuerError is returned when LoginClient.FinishLogin or
// SessionClient is invoked without an injected SessionIssuer.
type MissingSessionIssuerError struct{}

// Error returns a human-readable description.
func (e *MissingSessionIssuerError) Error() string {
	return "embedded: session issuer is not configured"
}

// HydratorTenantOwnerMismatchError refuses a login whose configured
// IdentityHydrator set auth.Identity.TenantOwner to a value role.IsTenantOwner
// does not decide for the identity it returned. Either the hydrator breaks the
// IdentityHydrator contract, or the principal's roles changed between its read
// and FinishLogin's; a new login is decided afresh.
type HydratorTenantOwnerMismatchError struct {
	TenantID string
	Hydrated bool
}

// Error returns a human-readable description.
func (e *HydratorTenantOwnerMismatchError) Error() string {
	return "embedded: identity hydrator set TenantOwner " + strconv.FormatBool(e.Hydrated) +
		" for tenant " + strconv.Quote(e.TenantID) + "; the role store decides " + strconv.FormatBool(!e.Hydrated)
}

// SessionIssuer mints, resolves, and revokes opaque bearer tokens. The
// embedded transport delegates session-state ownership to the caller —
// qrdb itself has no native session store; the issuer typically lives
// in the surrounding daemon (quicbaas in production, an in-mem stub in
// tests).
//
// Implementations MUST be safe for concurrent use and MUST treat
// missing / expired bearers as InvalidBearerError.
type SessionIssuer interface {
	// Issue produces a fresh bearer for the given identity. Returns the
	// bearer and its expiry time.
	Issue(ctx context.Context, identity *auth.Identity) (bearer string, expiresAt time.Time, err error)

	// Resolve returns the identity bound to the given bearer or
	// transport.InvalidBearerError when the bearer is unknown / expired
	// / revoked.
	Resolve(ctx context.Context, bearer string) (*auth.Identity, error)

	// Revoke invalidates the bearer.
	Revoke(ctx context.Context, bearer string) error
}

// IdentityHydrator translates a resolved auth.Principal + auth.AuthFactor
// pair into a full auth.Identity ready to be issued as a bearer. The
// hydrator is responsible for assembling the role set and any
// implementation-specific metadata. It does not name the account: once it
// returns, FinishLogin binds the identity to the resolved principal with
// auth.Identity.BindUser, so the Subject is always the immutable user id
// whatever the hydrator set. A default implementation (the principal's
// Roles and factor metadata) is supplied when none is configured — see
// defaultIdentityHydrator.
//
// Every implementation sets auth.Identity.TenantOwner, and sets it only
// from role.IsTenantOwner over the identity it returns, after its role set
// and TenantID are final; it returns IsTenantOwner's error rather than
// guess. FinishLogin enforces this for a hydrator given by
// WithIdentityHydrator: it decides role.IsTenantOwner again over the
// transport's role service (WithRoleService) and refuses the login, with
// that call's error or a *HydratorTenantOwnerMismatchError, unless the two
// agree. The value reaches the caller as transport.Identity.TenantOwner. It
// must never be derived from Metadata, a role name, or anything the caller
// supplied.
type IdentityHydrator interface {
	// Hydrate produces an *auth.Identity ready to be wrapped in a
	// bearer. Implementations may return a typed error to abort login
	// (e.g. principal lacks any roles).
	Hydrate(ctx context.Context, principal *auth.Principal, factor *auth.AuthFactor) (*auth.Identity, error)
}

// defaultIdentityHydrator is the trivial fallback that builds an
// Identity directly from the principal's fields. It is used when the
// embedded transport is constructed without WithIdentityHydrator.
//
// roles is the role store TenantOwner is resolved against (the transport's
// WithRoleService). Without one the hydrator refuses rather than issue an
// identity whose TenantOwner it could not decide.
type defaultIdentityHydrator struct {
	roles role.NameResolver
}

// Hydrate implements IdentityHydrator using the principal's own fields,
// deciding TenantOwner through role.IsTenantOwner.
func (d *defaultIdentityHydrator) Hydrate(ctx context.Context, principal *auth.Principal, factor *auth.AuthFactor) (*auth.Identity, error) {
	if principal == nil {
		return nil, &auth.InvalidFactorError{Reason: "principal is nil"}
	}
	roles := append([]string(nil), principal.Roles...)
	meta := map[string]string{}
	if factor != nil {
		meta["factor_id"] = factor.ID
		meta["factor_type"] = string(factor.Type)
	}
	identity := &auth.Identity{
		TenantID: principal.TenantID,
		Roles:    roles,
		Metadata: meta,
	}
	owner, err := role.IsTenantOwner(ctx, d.roles, identity)
	if err != nil {
		return nil, err
	}
	identity.TenantOwner = owner
	return identity, nil
}

// loginSessionStore tracks open BeginLogin attempts so FinishLogin can
// validate the SessionID + Nonce pair. The store is process-local and
// in-memory — login sessions are short-lived (default 5 min) and don't
// need cross-process replication.
// checkHydratedTenantOwner enforces the IdentityHydrator contract on the
// identity a configured hydrator returned: its TenantOwner must be what
// role.IsTenantOwner decides over the transport's role service. A role-store
// error, a missing role service included, is returned as is; a mismatch is a
// *HydratorTenantOwnerMismatchError. Either refuses the login, so no bearer
// carries an ownership the role store does not grant, or lacks one it does.
func checkHydratedTenantOwner(ctx context.Context, roles role.NameResolver, identity *auth.Identity) error {
	owner, err := role.IsTenantOwner(ctx, roles, identity)
	if err != nil {
		return err
	}
	if owner != identity.TenantOwner {
		return &HydratorTenantOwnerMismatchError{TenantID: identity.TenantID, Hydrated: identity.TenantOwner}
	}
	return nil
}

type loginSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*loginSessionEntry
}

// loginSessionEntry is one open login attempt.
type loginSessionEntry struct {
	id        string
	factorTyp transport.FactorType
	nonce     []byte
	hint      string
	tenantID  string
	metadata  map[string]string
	expiresAt time.Time
}

func newLoginSessionStore() *loginSessionStore {
	return &loginSessionStore{sessions: make(map[string]*loginSessionEntry)}
}

func (s *loginSessionStore) put(entry *loginSessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[entry.id] = entry
}

// take removes and returns the entry; returns nil when absent.
func (s *loginSessionStore) take(id string) *loginSessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[id]
	if !ok {
		return nil
	}
	delete(s.sessions, id)
	return e
}

// loginNonceLen is the length of the BeginLogin server nonce. 32 bytes
// matches the canonical lookup key length for mtls/piv factors and
// gives 256 bits of randomness — adequate for replay defence.
const loginNonceLen = 32

// loginSessionTTL is the default validity window for an open login
// attempt. Five minutes is the convention used elsewhere in qrdb's
// challenge / verify dance.
const loginSessionTTL = 5 * time.Minute

// loginClient is the embedded implementation of transport.LoginClient.
type loginClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.LoginClient = (*loginClient)(nil)

// identityToTransport projects the domain Identity into the SDK shape.
func identityToTransport(id *auth.Identity) *transport.Identity {
	if id == nil {
		return nil
	}
	roles := append([]string(nil), id.Roles...)
	var meta map[string]string
	if len(id.Metadata) > 0 {
		meta = make(map[string]string, len(id.Metadata))
		for k, v := range id.Metadata {
			meta[k] = v
		}
	}
	return &transport.Identity{
		Subject:     id.Subject,
		TenantID:    id.TenantID,
		Roles:       roles,
		TenantOwner: id.TenantOwner,
		Metadata:    meta,
	}
}

// mergeLoginMetadata combines the challenge metadata stored at BeginLogin
// with the per-request metadata supplied at FinishLogin. Request values win
// on key conflicts so the client supplies the live proof data (assertion,
// client data) while the server-issued challenge context (e.g. a WebAuthn
// session id) is preserved.
func mergeLoginMetadata(stored, req map[string]string) map[string]string {
	if len(stored) == 0 && len(req) == 0 {
		return nil
	}
	out := make(map[string]string, len(stored)+len(req))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range req {
		out[k] = v
	}
	return out
}

// ResolveIdentity returns the identity bound to the given session
// token. Delegates to the configured SessionIssuer.
func (c *loginClient) ResolveIdentity(ctx context.Context, sessionToken string) (*transport.Identity, error) {
	ctx, err := c.transport.admit(ctx, "LoginClient.ResolveIdentity")
	if err != nil {
		return nil, err
	}
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.sessionIssuer == nil {
		return nil, &MissingSessionIssuerError{}
	}
	if sessionToken == "" {
		return nil, &transport.InvalidBearerError{Reason: "empty bearer"}
	}
	id, err := c.transport.sessionIssuer.Resolve(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	return identityToTransport(id), nil
}

// BeginLogin opens a login attempt and returns a server-issued nonce.
func (c *loginClient) BeginLogin(ctx context.Context, req transport.BeginLoginRequest) (*transport.BeginLoginResponse, error) {
	ctx, err := c.transport.admit(ctx, "LoginClient.BeginLogin")
	if err != nil {
		return nil, err
	}
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !req.Type.IsValid() {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(req.Type)}
	}
	// A TENANT-WIDE throttle, not a per-hint one: req.Hint is
	// caller-supplied and unverified at this point (BeginLogin runs before
	// any proof is checked), so keying on it would let an attacker pick a
	// fresh, unthrottled bucket for every nonce request by varying the
	// hint. "begin-login" is a fixed principal shared by every attempt in
	// the tenant, which is what makes this a defense against RAW
	// volumetric nonce/challenge-issuance spam -- FinishLogin's own check,
	// keyed by the RESOLVED principal, remains the defense against
	// repeated guesses against one account.
	//
	// permission.LoginBeginAttempt, not UserAuthFactorsLogin: the two
	// throttles are different threat models (volumetric challenge-issuance
	// spam here vs. per-account brute force at FinishLogin) that need
	// independently tunable ratelimit.Policy values. Sharing one Permission
	// key would force one Policy to govern both bucket shapes; see
	// permission.LoginBeginAttempt's doc comment for the collision that
	// surfaced when this used UserAuthFactorsLogin.
	if c.transport.authMW != nil {
		if err := c.transport.authMW.CheckLoginRateLimit(ctx, req.TenantID, "begin-login", permission.LoginBeginAttempt); err != nil {
			return nil, err
		}
	}
	now := c.transport.nowFunc()

	var (
		challenge []byte
		metadata  map[string]string
		expiresAt = now.Add(loginSessionTTL)
	)
	// When the registered authenticator can issue a method-specific,
	// tenant-scoped challenge (e.g. a WebAuthn RP-bound options blob), use
	// it; otherwise fall back to a generic random server nonce.
	if v := c.transport.factorVerifier(req.Type); v != nil {
		if ch, ok := v.(FactorChallenger); ok {
			c, meta, exp, err := ch.BeginChallenge(ctx, req.TenantID, req.Type, req.Hint)
			if err != nil {
				return nil, err
			}
			challenge, metadata = c, meta
			if !exp.IsZero() {
				expiresAt = exp
			}
		}
	}
	if challenge == nil {
		challenge = make([]byte, loginNonceLen)
		if _, err := rand.Read(challenge); err != nil {
			return nil, err
		}
	}

	entry := &loginSessionEntry{
		id:        uuid.NewString(),
		factorTyp: req.Type,
		nonce:     challenge,
		hint:      req.Hint,
		tenantID:  req.TenantID,
		metadata:  metadata,
		expiresAt: expiresAt,
	}
	c.transport.loginSessions.put(entry)
	return &transport.BeginLoginResponse{
		SessionID:         entry.id,
		Nonce:             append([]byte(nil), challenge...),
		ChallengeMetadata: metadata,
		ExpiresAt:         entry.expiresAt,
	}, nil
}

// FinishLogin consumes the open login attempt, resolves the (Type,
// LookupKey) tuple to a user, mints a bearer, and returns the resolved
// identity.
func (c *loginClient) FinishLogin(ctx context.Context, req transport.FinishLoginRequest) (*transport.FinishLoginResponse, error) {
	ctx, err := c.transport.admit(ctx, "LoginClient.FinishLogin")
	if err != nil {
		return nil, err
	}
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.loginResolver == nil {
		return nil, &MissingLoginResolverError{}
	}
	if c.transport.sessionIssuer == nil {
		return nil, &MissingSessionIssuerError{}
	}
	if !req.Type.IsValid() {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(req.Type)}
	}

	entry := c.transport.loginSessions.take(req.SessionID)
	if entry == nil {
		return nil, &transport.LoginSessionNotFoundError{SessionID: req.SessionID}
	}
	if c.transport.nowFunc().After(entry.expiresAt) {
		return nil, &transport.LoginSessionExpiredError{SessionID: req.SessionID}
	}
	if entry.factorTyp != req.Type {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(req.Type)}
	}

	domainType := factorTypeToDomain(req.Type)
	// Resolve LookupKey first, then any ordered fallback keys. The
	// fallback must happen here — the login session was consumed above,
	// so a client-side retry with a different key is impossible. The
	// canonical pairing is SPKI key first (renewal-stable), legacy
	// SHA-256(cert) key second (pre-SPKI enrolments).
	lookupKeys := append([][]byte{req.LookupKey}, req.AltLookupKeys...)
	// Resolve on the login SESSION's tenant (entry.tenantID, captured from
	// BeginLoginRequest.TenantID above), never the caller's own ctx: this
	// endpoint is unauthenticated, so ctx carries no tenant of its own, and
	// resolving on it let a credential enrolled for tenant A complete a
	// ceremony begun as tenant B (go-qrdb#303). An empty entry.tenantID
	// binds the system tenant, matching BeginLogin's own default. This
	// mirrors, at the resolve step, the explicit tenant the verifier below
	// has always received.
	resolveCtx := sdk.ContextWithTenant(ctx, entry.tenantID)
	principal, factor, err := c.transport.loginResolver.ResolveFirst(resolveCtx, domainType, lookupKeys...)
	if err != nil {
		return nil, mapFactorError(err)
	}
	// The issued Subject is the principal's immutable user id, so a
	// principal without one names no account and cannot log in.
	if principal == nil || principal.ID == 0 {
		return nil, &auth.InvalidFactorError{Reason: "resolved principal has no user id"}
	}

	// Charged to the RESOLVED principal, never the caller-supplied
	// LookupKey/hint: the lookup key is attacker-controlled before any
	// proof is checked, so throttling by it would let repeated guesses
	// against the SAME account each pick a fresh bucket by varying an
	// irrelevant byte of the key. permission.UserAuthFactorsLogin is the
	// same permission the network transports' login-factor verb carries,
	// so this remains the per-account brute-force defense; BeginLogin's
	// own throttle above is deliberately a separate Permission
	// (permission.LoginBeginAttempt) so the two bucket shapes can be
	// tuned independently. The bucket is keyed by the user id, the Subject
	// the network transports charge, so a rename never resets it. A nil
	// t.authMW or no declared policy is a no-op.
	if c.transport.authMW != nil {
		if err := c.transport.authMW.CheckLoginRateLimit(ctx, principal.TenantID, auth.UserSubject(principal.ID), permission.UserAuthFactorsLogin); err != nil {
			return nil, err
		}
	}

	// Verify the proof-of-possession BEFORE issuing a bearer. The
	// resolution step above only maps the (type, lookupKey) to a user; it
	// does NOT prove the caller holds the credential's private key. When a
	// verifier is registered for this factor type, it MUST verify the
	// signature over the BeginLogin challenge. When a proof is supplied but
	// no verifier is registered, fail closed rather than issue a bearer on
	// an unverified proof. When neither a verifier nor a proof is present,
	// possession was established out-of-band (e.g. the mTLS handshake) and
	// the ceremony proceeds.
	if verifier := c.transport.factorVerifier(req.Type); verifier != nil {
		tenantID := entry.tenantID
		if tenantID == "" && principal != nil {
			tenantID = principal.TenantID
		}
		vc := transport.VerifyChallenge{
			Type:           req.Type,
			Nonce:          entry.nonce,
			Signature:      req.Signature,
			MethodMetadata: mergeLoginMetadata(entry.metadata, req.MethodMetadata),
		}
		if factor != nil {
			vc.FactorID = factor.ID
		}
		if vErr := verifyFactor(ctx, tenantID, verifier, factor, vc); vErr != nil {
			fid := ""
			if factor != nil {
				fid = factor.ID
			}
			return nil, &transport.FactorVerificationError{FactorID: fid, Reason: vErr.Error()}
		}
	} else if len(req.Signature) > 0 {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(req.Type)}
	}

	hydrator := c.transport.identityHydrator
	if hydrator == nil {
		hydrator = &defaultIdentityHydrator{roles: c.transport.roleSvc}
	}
	identity, err := hydrator.Hydrate(ctx, principal, factor)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, &auth.InvalidFactorError{Reason: "identity hydrator returned no identity"}
	}
	identity.BindUser(principal.ID, principal.Username)
	// A configured hydrator is held to the IdentityHydrator contract here,
	// before a bearer exists; the default hydrator decides TenantOwner itself.
	if c.transport.identityHydrator != nil {
		if err := checkHydratedTenantOwner(ctx, c.transport.roleSvc, identity); err != nil {
			return nil, err
		}
	}

	bearer, expiresAt, err := c.transport.sessionIssuer.Issue(ctx, identity)
	if err != nil {
		return nil, err
	}

	resp := &transport.FinishLoginResponse{
		Bearer:    bearer,
		Identity:  identityToTransport(identity),
		ExpiresAt: expiresAt,
	}
	if factor != nil {
		resp.FactorID = factor.ID
	}
	return resp, nil
}

// Logout invalidates the given bearer.
func (c *loginClient) Logout(ctx context.Context, sessionToken string) error {
	ctx, err := c.transport.admit(ctx, "LoginClient.Logout")
	if err != nil {
		return err
	}
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.sessionIssuer == nil {
		return &MissingSessionIssuerError{}
	}
	if sessionToken == "" {
		return &transport.InvalidBearerError{Reason: "empty bearer"}
	}
	return c.transport.sessionIssuer.Revoke(ctx, sessionToken)
}

// LoginClient returns the embedded login client projection.
func (t *Transport) LoginClient() transport.LoginClient {
	return &loginClient{transport: t}
}

// WithLoginResolver injects the qrdb auth.LoginResolver into the
// embedded transport.
func WithLoginResolver(r *auth.LoginResolver) Option {
	return func(t *Transport) {
		t.loginResolver = r
	}
}

// WithSessionIssuer injects the SessionIssuer into the embedded
// transport.
func WithSessionIssuer(s SessionIssuer) Option {
	return func(t *Transport) {
		t.sessionIssuer = s
	}
}

// WithIdentityHydrator injects a custom IdentityHydrator into the
// embedded transport. When unset, a default hydrator that maps the
// principal's fields directly is used. Either way the issued identity's
// Subject is the principal's immutable user id.
func WithIdentityHydrator(h IdentityHydrator) Option {
	return func(t *Transport) {
		t.identityHydrator = h
	}
}
