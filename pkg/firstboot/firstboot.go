// Copyright (c) 2026 Jeremy Hahn
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

// Package firstboot implements the always-runs first-boot bootstrap path
// for qrdb-style daemons. It is the canonical place where the system
// tenant, built-in roles, and the platform admin user are created.
//
// Initialize is independent of the storage barrier strategy. The Shamir
// ceremony state machine is layered on top of barrier init only when
// barrier.strategy == "shamir"; first-boot itself never engages it.
//
// This package is reusable across qrdb, xkms, quicbaas, and any future
// binary in the stack. Dependencies are passed by interface; nothing in
// this package imports raft, pebble, or any daemon-specific machinery.
package firstboot

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// adminUsername is the canonical platform-admin username created during
// first-boot. The admin user is granted exactly one role: Admin@system,
// which the system-tenant rule in pkg/auth/rbac promotes to cross-tenant
// authority.
const adminUsername = user.BootstrapAdminUsername

// systemTenantDisplayName is the human-readable name persisted alongside
// the reserved system tenant id.
const systemTenantDisplayName = "System"

// Result describes what Initialize did during this run. The *Created
// flags reflect this run only, not the overall presence of the resource:
// when a resource already existed (idempotent re-run), the corresponding
// flag is false even though the resource is present.
type Result struct {
	// SystemTenantCreated is true when this run created the system tenant.
	// False when the tenant already existed.
	SystemTenantCreated bool

	// RolesCreated is true when this run created at least one built-in
	// role. False when every built-in role already existed.
	RolesCreated bool

	// AdminCreated is true when this run created the admin user. False
	// when the admin user already existed.
	AdminCreated bool

	// AdminCertPEM holds the newly-generated admin certificate (PEM). Nil
	// when no CertProvider was supplied or the admin user already existed
	// and so the cert was not regenerated.
	AdminCertPEM []byte

	// AdminKeyPEM holds the newly-generated admin private key (PEM). Nil
	// under the same conditions as AdminCertPEM, and ALWAYS nil for the
	// CSR-sign path (Path B) where the admin keeps their own key.
	AdminKeyPEM []byte

	// AdminCertThumbprint is the SPKI SHA-256 thumbprint of the issued admin
	// certificate, bound to the admin user. Empty when no cert was issued.
	AdminCertThumbprint string

	// SOCreated is true when this run created the system SO (Dependencies.SO).
	SOCreated bool

	// SOUserID is the system SO's user id. Zero when no SO was given.
	SOUserID uint64

	// SOCertPEM is the system SO's certificate this run bound: signed from
	// its CSR or adopted. Nil when no SO was given or it was already bound.
	SOCertPEM []byte

	// SOCertThumbprint is SOCertPEM's SPKI SHA-256 thumbprint.
	SOCertThumbprint string
}

// CertProvider is the minimal cert-issuance contract first-boot needs (Path
// A, generate-and-distribute).
//
// go-qrdb ships no production implementation of its own: go-xkms's CAAdapter
// (sdk/go NewCAAdapter) satisfies it for xkms-composed binaries. go-qrdb's own
// compositions (qrdbd, sdk/go/node) do not call this interface at all --
// pkg/ceremony.CeremonyService issues the bootstrap admin's certificate
// itself, synchronously during Initialize (go-qrdb#548), using whichever of
// the CSR-sign, BYOC or generate paths the operator selected, and hands first
// boot the already-issued certificate through Dependencies.AdminCertPEM (see
// AdoptAdminCert) rather than asking first boot to issue one. This interface
// remains for a composer whose admin certificate comes from a CA first boot
// itself must call, which is not how go-qrdb's own daemon works.
type CertProvider interface {
	// GenerateAdminCert issues a client certificate for the platform
	// admin user. tenantID identifies the OU; subject is the CN
	// (typically "admin"). Returns PEM-encoded cert and key bytes.
	GenerateAdminCert(ctx context.Context, tenantID, subject string) (certPEM, keyPEM []byte, err error)
}

// CertSigner is the admin-mTLS Path B seam: the admin (or tenant admin) supplies
// a CSR and the CA signs it, so the private key never leaves the admin.
// go-xkms's CAAdapter (sdk/go NewCAAdapter) satisfies this (CSR signing). Like
// CertProvider, go-qrdb's own compositions do not call this: the ceremony
// signs the CSR itself during Initialize (go-qrdb#548).
type CertSigner interface {
	// SignAdminCSR signs an admin-supplied CSR and returns the PEM cert.
	SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) (certPEM []byte, err error)
}

// CertBinder is the store seam that pins an admin certificate's SPKI thumbprint
// to the admin user record so subsequent mTLS handshakes authenticate as the
// admin. UserCertBinder is the canonical implementation over qrdb's own user
// store; a foreign IAM stack's adapter implements this directly.
type CertBinder interface {
	// BindAdminCert binds the SPKI thumbprint to the admin user.
	BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error
}

// UserCertBinder is the canonical CertBinder over qrdb's own user store
// (go-qrdb#548): every composition in this module (qrdbd, sdk/go/node,
// embedded) wires one of these rather than reimplementing the one-line
// adapter. notAfterUnix is always 0 (never expires): the binding tracks the
// key, not the certificate's validity window, consistent with every other
// CertBinder this package has ever been called with -- a certificate rotation
// enrols the new key additively rather than updating an expiry on the old
// entry.
type UserCertBinder struct {
	Users interface {
		AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	}
}

// BindAdminCert implements CertBinder.
func (b UserCertBinder) BindAdminCert(ctx context.Context, _ string, userID uint64, thumbprint string) error {
	_, err := b.Users.AddCertBinding(ctx, userID, thumbprint, 0)
	return err
}

// TenantCreator is the narrow tenant-creation surface first-boot needs.
// qrdb's own tenant.TenantServicer satisfies it; higher-level IAM stacks
// (go-quicbaas, go-xkms) ship adapters that satisfy it over their own
// tenant stores so the same bootstrap path serves every binary.
type TenantCreator interface {
	CreateTenant(ctx context.Context, id, displayName string, isolation tenant.IsolationType, opts ...tenant.CreateTenantOption) (*tenant.TenantInfo, error)
}

// RoleCreator is the narrow role-creation surface first-boot needs. Same
// adapter rationale as TenantCreator.
type RoleCreator interface {
	CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
}

// UserCreator is the narrow user-creation surface first-boot needs. Same
// adapter rationale as TenantCreator.
type UserCreator interface {
	CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
}

// UserResolver looks a user up by its tenant-scoped username. qrdb's own
// user.UserServicer satisfies it; it exists so createAdminUser can verify a
// create-time collision on the admin's fixed, well-known username actually
// names the admin, the same way role.NameResolver lets RegisterBuiltInRoles
// verify a built-in role collision (go-qrdb#555).
type UserResolver interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
}

// Dependencies is the small set of services the bootstrap needs. Pass
// by interface so the same package works for any binary in the stack.
// qrdb's own tenant/user/role Servicer interfaces satisfy these narrower
// types automatically; downstream consumers (quicbaas, xkms) implement
// adapters from their IAM stacks to these three minimal interfaces.
type Dependencies struct {
	// Tenants is the tenant-creation surface. Required.
	Tenants TenantCreator

	// Users is the user-creation surface. Required.
	Users UserCreator

	// Roles is the role-creation surface. Required.
	Roles RoleCreator

	// RoleResolver looks a role up by name so RegisterBuiltInRoles can verify
	// that a create-time collision on a built-in role's reserved name (or
	// generated id) actually names that exact canonical role — same tenant,
	// name, System flag and permission set — before treating it as this
	// run's own idempotent re-attempt rather than a genuine, unexplained
	// conflict (see RoleMatchesSeed). Leaving it nil is safe when
	// Roles itself implements role.NameResolver, which qrdb's own
	// role.RoleServicer does and which is then used automatically.
	RoleResolver role.NameResolver

	// UserResolver looks the admin user up by username so createAdminUser can
	// verify a create-time collision — on either the admin's generated id or
	// its fixed username — actually names the admin before treating it as
	// this run's own idempotent re-attempt. Leaving it nil is safe when Users
	// itself implements UserResolver — qrdb's own user.UserServicer does, and
	// it is then used automatically.
	UserResolver UserResolver

	// CA issues the platform admin certificate (Path A: generate-and-distribute).
	// Optional: used when no AdminCSRPEM is supplied.
	CA CertProvider

	// Signer signs an admin-supplied CSR (Path B: CSR-sign, key never leaves the
	// admin). Used when AdminCSRPEM is set. Optional.
	Signer CertSigner

	// AdminCSRPEM, when non-empty, selects Path B: the admin's CSR is signed via
	// Signer instead of generating a key+cert via CA. The admin keeps their key.
	AdminCSRPEM []byte
	// AdminCertPEM, when non-empty, selects Path C: the admin presents a
	// certificate they already hold — typically one already resident on a PIV
	// card or a security key — and first-boot adopts it rather than issuing
	// anything. No CA and no Signer are consulted; the private key never exists
	// outside the operator's token, so AdminKeyPEM stays nil as it does for
	// Path B.
	//
	// The certificate is validated against AdminCertPool before adoption, and
	// only its SPKI thumbprint is bound, which is the same identifier every
	// other path binds. Mutually exclusive with AdminCSRPEM.
	//
	AdminCertPEM []byte
	// AdminCertPool holds the trust anchors a presented AdminCertPEM must chain
	// to. Required whenever AdminCertPEM is set, and unused otherwise.
	//
	// It is separate from the platform CA on purpose: a certificate the operator
	// already holds was, by definition, issued by something else — a corporate
	// PKI or a card vendor — so validating it against the CA this deployment is
	// about to create would reject every legitimate input.
	AdminCertPool *x509.CertPool

	// TrustDomain is the SPIFFE trust domain the node's mTLS authenticator
	// pins. A presented AdminCertPEM is held to the authenticator's own
	// tenant rule, auth.TenantFromClientCert under this domain, and must name
	// the system tenant, so first boot never binds a certificate no login can
	// use. Empty pins no domain, exactly as an authenticator built without
	// one; a composer pins its domain (Node.FirstBoot pins the node's).
	TrustDomain string

	// Binder pins the issued admin certificate's SPKI thumbprint to the admin
	// user so mTLS handshakes authenticate as the admin. Optional: when nil, the
	// cert is issued but not bound (caller binds separately).
	Binder CertBinder

	// Logger receives structured progress events. Required.
	Logger *slog.Logger

	// SO is the system tenant's Security Officer, enrolled the way tenant
	// create enrols a tenant SO (bootstrap.TenantProvisionConfig.SO): a
	// separate account holding the system tenant's so role and nothing
	// else, its certificate signed through Signer (CSRPEM) or verified
	// against AdminCertPool (CertPEM) and bound through Binder, and its PIN
	// committed through SOPINs.
	//
	// Under SeparationOfDuties it is required: the strict gates run role
	// create and the built-in reconcile only as a separate SO, so a
	// deployment without one could never manage its roles, and Initialize
	// refuses with *SystemSORequiredError before any write. Optional under
	// the standard posture. The SO never shares the admin's username or key
	// (*SOMatchesAdminError), in either posture: first boot never folds the
	// SO into the immutable system admin.
	//
	// Initialize opens SO.PIN only to commit it and never destroys it, so a
	// caller that retries Initialize keeps it; the caller destroys it.
	SO *SOConfig

	// SOEnrolled reports that an earlier first boot already enrolled the
	// system SO -- the composer read the ceremony's pending SO record as
	// consumed (SystemSOConsumed). A strict boot with no SO is then a
	// re-run, not a deployment without one, and is not refused.
	SOEnrolled bool

	// SOPINs commits the system SO's PIN. Required when SO is set.
	SOPINs SOPINSetter

	// TrustAnchors persists the system tenant's client-trust anchors, and
	// ClientTrust refreshes the node's runtime client trust from them. Both
	// are required when SO presents a certificate with its own trust pool
	// (SO.PoolPEM, carried from the ceremony's AdminCertPoolPEM): that
	// pool's issuers are trusted nowhere else on the node, so first boot
	// installs them as system trust anchors and refreshes client trust
	// before it declares the SO enrolled, or the SO could never log in.
	TrustAnchors TrustAnchorAdder
	ClientTrust  ClientTrustRefresher

	// SeparationOfDuties selects the RBAC posture for the bootstrap admin.
	// When false (default, "standard") the admin is also granted the Owner
	// role (full operational authority) so one identity can run the whole
	// database. When true ("strict") only the canonical Admin@system grant
	// is issued and the Owner role is neither seeded nor assigned, so the
	// FIPS 140-3 / PCI-DSS least-privilege separation is preserved.
	//
	// The caller derives this from crypto/fips140.Enabled() ||
	// config.Auth.SeparationOfDuties — see cmd/qrdbd.
	SeparationOfDuties bool

	// MTLSBindingPolicy is the fleet's auth.mtls_binding_policy. The ZERO
	// VALUE is auth.MTLSBindingRequired (go-qrdb#548), under which Initialize
	// refuses — before writing anything — a configuration that would leave
	// the admin unable to log in by certificate: creating the admin needs a
	// Binder and an admin certificate path, and an existing admin must
	// already hold a live binding (which needs Users to implement
	// AdminLookup). The error is *AdminCertBindingRequiredError.
	// auth.MTLSBindingEnrolled is the explicit opt-out that restores the
	// pre-go-qrdb#517 behaviour (an empty binding set admits).
	MTLSBindingPolicy auth.MTLSBindingPolicy
}

// Initialize runs the always-runs first-boot path. Idempotent: when the
// system tenant + built-in roles + admin user already exist, returns a
// Result with the *Created flags false and no error.
//
// Sequence:
//
//  1. Create the system tenant via WithInternalCall(ctx) so the tenant
//     service's reserved-id rejection is bypassed for this trusted path.
//     IsolationPrefix is used for the system tenant so its IAM data
//     lives in the shared store and is reachable during bootstrap.
//  2. Register the five built-in roles (SO/Admin/Operator/User/Auditor)
//     under the system tenant.
//  3. Create the admin user inside the system tenant with a single
//     role grant: Admin@system. The system-tenant rule in
//     pkg/auth/rbac promotes this to cross-tenant authority.
//  4. Generate the platform admin certificate via deps.CA when supplied.
//
// Initialize is NEVER engaged with the Shamir ceremony state machine:
// for non-shamir barrier strategies the daemon transitions to
// operational immediately after Initialize returns. The shamir-only
// state machine is layered on top by the daemon at a higher level.
func Initialize(ctx context.Context, deps Dependencies) (*Result, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	if err := checkBindingPosture(ctx, deps); err != nil {
		return nil, err
	}
	if err := checkSystemSO(deps); err != nil {
		return nil, err
	}

	logger := deps.Logger
	tenantID := tenantid.System
	result := &Result{}

	// Step 1: system tenant. Internal-call context bypasses the
	// reserved-id rejection that protects the public CreateTenant API.
	if err := createSystemTenant(ctx, deps, result); err != nil {
		return nil, err
	}

	// Step 2: built-in roles. Tolerates already-exists for idempotency.
	_, err := RegisterBuiltInRoles(ctx, deps, tenantID, result)
	if err != nil {
		return nil, err
	}

	// Step 3: admin user with Admin@system grant. Tolerates
	// already-exists for idempotency. Cert generation only runs on the
	// initial create so a re-run does not rotate the platform key
	// material.
	adminUser, created, err := createAdminUser(ctx, deps, tenantID)
	if err != nil {
		return nil, err
	}
	result.AdminCreated = created

	// needsBinding covers BOTH the brand-new admin (created, never bound)
	// and the create-but-never-bound gap a crash between createAdminUser and
	// deps.Binder.BindAdminCert leaves on an EARLIER run (go-qrdb#548
	// review: B6) -- an admin that already exists but, per the SAME
	// verifyAdminBindings predicate checkBindingPosture and
	// verifyRequiredOutcome apply, holds no live certificate binding right
	// now. Gating step 4 on this instead of only `created` means a retry
	// repairs the gap rather than skipping binding forever because the
	// admin "already existed" -- the account converges to fully bound
	// exactly as the ceremony's own admin-credential issuance converges on
	// a retry after a crash (ReissueAdminCredential).
	//
	// adminUser is nil when reconcileExistingAdmin's UserCreator does not
	// implement systemPromoter (a documented, legitimate shape for a
	// downstream composer's own adapter): there is then no ID to bind a
	// certificate to at all, so this is never treated as "needs binding" --
	// the same no-op `created`-only gating already applied here.
	needsBinding := created || (adminUser != nil && verifyAdminBindings(adminUser, time.Now().UTC()) != nil)

	// Step 4: platform admin cert + SPKI binding, for a brand-new admin or
	// an existing, still-unbound one. Two cert-issuance paths, both ending
	// in the SPKI thumbprint being bound to the admin user:
	//   Path B (CSR-sign): AdminCSRPEM supplied -> Signer signs it; the admin
	//     keeps their private key (AdminKeyPEM stays nil).
	//   Path A (generate-and-distribute): else CA generates the key+cert bundle,
	//     which must be securely distributed to the admin.
	// There is no invite/activation token — enrollment is cert-based.
	if needsBinding {
		certPEM, keyPEM, issueErr := issueAdminCert(ctx, deps, tenantID)
		if issueErr != nil {
			return nil, issueErr
		}
		if len(certPEM) > 0 {
			result.AdminCertPEM = certPEM
			result.AdminKeyPEM = keyPEM
			thumbprint, tpErr := spkiThumbprintFromCertPEM(certPEM)
			if tpErr != nil {
				return nil, &AdminCertGenerationError{Cause: tpErr}
			}
			result.AdminCertThumbprint = thumbprint
			if deps.Binder != nil && adminUser != nil {
				if bindErr := deps.Binder.BindAdminCert(ctx, tenantID, adminUser.ID, thumbprint); bindErr != nil {
					return nil, &AdminCertBindError{Cause: bindErr}
				}
				logger.Info("first-boot: admin cert bound",
					slog.String("tenant", tenantID),
					slog.Uint64("user_id", adminUser.ID),
				)
			}
			logger.Info("first-boot: admin cert issued",
				slog.String("tenant", tenantID),
				slog.String("subject", adminUsername),
				slog.Bool("csr_signed", len(deps.AdminCSRPEM) > 0),
			)
		}
	}

	// Step 5: the system SO (Dependencies.SO), a separate principal with the
	// so role alone: under strict separation the only one that can create
	// roles and run the built-in reconcile.
	if err := enrollSystemSO(ctx, deps, tenantID, result); err != nil {
		return nil, err
	}

	// Under "required", verify the OUTCOME, not only the inputs the
	// pre-flight checked: a CA that returned an empty certificate, a user
	// surface that created the admin without returning it, or a Binder that
	// reported success without binding would each leave an admin nobody can
	// log in as. The record is read back through the same lookup the
	// pre-flight used (checkBindingPosture guaranteed it exists).
	if err := verifyRequiredOutcome(ctx, deps); err != nil {
		return nil, err
	}

	logger.Info("first-boot complete",
		slog.Bool("system_tenant_created", result.SystemTenantCreated),
		slog.Bool("roles_created", result.RolesCreated),
		slog.Bool("admin_created", result.AdminCreated),
		slog.String("tenant", tenantID),
	)

	return result, nil
}

// validateDependencies ensures every required dependency is present.
// Each missing field returns its own typed error so callers can
// distinguish them in test assertions.
func validateDependencies(deps Dependencies) error {
	if deps.Tenants == nil {
		return &MissingTenantsError{}
	}
	if deps.Users == nil {
		return &MissingUsersError{}
	}
	if deps.Roles == nil {
		return &MissingRolesError{}
	}
	if deps.Logger == nil {
		return &MissingLoggerError{}
	}
	return nil
}

// createSystemTenant creates the structurally-privileged system tenant
// using the internal-call context flag to bypass reserved-id rejection.
// Already-exists is treated as idempotent success.
func createSystemTenant(ctx context.Context, deps Dependencies, result *Result) error {
	internalCtx := tenant.WithInternalCall(ctx)
	_, err := deps.Tenants.CreateTenant(internalCtx, tenantid.System, systemTenantDisplayName, tenant.IsolationPrefix)
	if err == nil {
		result.SystemTenantCreated = true
		deps.Logger.Info("first-boot: system tenant created",
			slog.String("tenant", tenantid.System),
		)
		return nil
	}
	if errors.Is(err, tenant.ErrTenantAlreadyExists) {
		deps.Logger.Debug("first-boot: system tenant already exists",
			slog.String("tenant", tenantid.System),
		)
		return nil
	}
	return &SystemTenantCreationError{Cause: err}
}

// roleResolver returns the name resolver first-boot should use: the explicit
// dependency when supplied, otherwise the Roles surface itself when it
// implements role.NameResolver — which qrdb's own role.RoleServicer does.
// Falling back this way means a caller that wires only Roles still binds
// grants to persisted identifiers instead of silently deriving them; the
// explicit field exists for adapters whose role-creation surface genuinely
// cannot answer lookups by name.
func roleResolver(deps Dependencies) role.NameResolver {
	if deps.RoleResolver != nil {
		return deps.RoleResolver
	}
	if r, ok := deps.Roles.(role.NameResolver); ok {
		return r
	}
	return nil
}

// userResolver returns the seam createAdminUser uses to verify a create-time
// collision names the admin, preferring an explicit Dependencies.UserResolver
// and falling back to Users itself when it happens to implement UserResolver
// — mirroring roleResolver's fallback for the same reason.
func userResolver(deps Dependencies) UserResolver {
	if deps.UserResolver != nil {
		return deps.UserResolver
	}
	if r, ok := deps.Users.(UserResolver); ok {
		return r
	}
	return nil
}

// adminHolder classifies what a single read finds behind the admin username
// after a create collided on it.
type adminHolder int

const (
	// adminHolderUnverifiable: there is no resolver to read the record with,
	// so nothing about the collision can ever be verified.
	adminHolderUnverifiable adminHolder = iota

	// adminHolderUnresolved: the lookup failed or found nothing. The username
	// is claimed by a write this read cannot see yet -- a concurrent first
	// boot whose record is still being written or replicated -- or by one
	// that has since rolled back and released the claim. Either resolves
	// with time.
	adminHolderUnresolved

	// adminHolderAdmin: the record is the admin this first boot would have
	// created.
	adminHolderAdmin

	// adminHolderOther: the record is some other account holding the
	// username, which no amount of waiting turns into the admin.
	adminHolderOther
)

// adminUsernameHolder reports what tenantID's admin username resolves to,
// verified by reading the record back rather than assumed from the create
// error's type alone. Only adminHolderAdmin is the admin.
//
// "The admin" means every property first boot itself gives the account, not
// only its username: the persisted record must name adminUsername in
// tenantID, hold adminRoleID (the tenant-scoped Admin role), and be in a
// defined, non-terminal lifecycle. System is deliberately NOT pinned: first
// boot creates the admin as the System user (go-qrdb#106), and an admin
// created by a build that predates the flag carries System == false until
// reconcileExistingAdmin promotes it, so both are the admin. The lifecycle is deliberately NOT pinned to
// active: an operator may since have suspended, locked or disabled the admin,
// and a re-run must stay idempotent across that. What it must never adopt is a
// record that is no account at all — deleting, or a state this build does not
// know — nor an unrelated user that merely holds the username.
//
// The classification comes from ONE read, so a record that becomes visible
// between two reads can never be mistaken for a squatter. Nothing but
// adminHolderAdmin is ever adopted: this function fails closed, and the
// caller refuses a collision it cannot verify.
func adminUsernameHolder(ctx context.Context, resolver UserResolver, tenantID, adminRoleID string) adminHolder {
	if resolver == nil {
		return adminHolderUnverifiable
	}
	existing, err := resolver.GetUserByUsername(ctx, tenantID, adminUsername)
	if err != nil || existing == nil {
		return adminHolderUnresolved
	}
	if existing.TenantID == tenantID &&
		existing.Username == adminUsername &&
		slices.Contains(existing.RoleIDs, adminRoleID) &&
		existing.Lifecycle.Valid() && !existing.Lifecycle.Terminal() {
		return adminHolderAdmin
	}
	return adminHolderOther
}

// RegisterBuiltInRoles installs every role returned by role.BuiltInRoles
// under the supplied tenant. Already-exists for any individual role is
// treated as idempotent success; only structural errors propagate.
//
// It reports whether any role was found to already exist.
//
// Both "already exists" outcomes (a RoleNameExistsError and a
// RoleIDExistsError) are verified the SAME way, by reading the persisted
// record back and requiring an EXACT match — id included — against the
// built-in definition this run would have created; see
// RoleMatchesSeed. Neither collision is answered from the error
// type alone:
//
//   - A name collision only proves SOME record already claims the reserved
//     name; it does not by itself prove that record IS this run's own
//     built-in. requireCanonicalSystemRole (pkg/role) refuses to let
//     anything else be created under that name with a non-canonical id, so
//     an id mismatch here is never a false refusal of a legitimate
//     idempotent re-attempt — it is always the genuine conflict this
//     function exists to catch. Every OTHER field — tenant, name, System,
//     and the permission set — is checked too: an earlier release persisted
//     every built-in role with System: false (CreateRoleRequest had no such
//     field then), and accepting a record like that here would leave a
//     reserved-name role mutable and deletable through the ordinary role
//     APIs.
//   - An id collision (RoleIDExistsError) does not answer "is this already
//     seeded" from the error alone either — see go-qrdb#555. CreateRole's
//     atomic path guards the id claim and the name together and diagnoses
//     the id first, so two concurrent first-boot attempts for the SAME
//     built-in role (the common case an id collision means here) are
//     indistinguishable, by error type alone, from an id chosen by an
//     unrelated caller that happens to collide. This function is the one
//     place trusted to tell them apart, because it — and only it — knows
//     the FULL canonical definition an id collision might legitimately
//     mean: it reads the persisted record back by name and refuses unless
//     every field, id included, matches exactly. CreateRole itself is never
//     asked to make this judgment, on purpose: a general create endpoint
//     accepting "same name and a guessed id means treat it as mine" would
//     let any caller holding roles:create adopt another actor's in-flight
//     or malformed role.
func RegisterBuiltInRoles(ctx context.Context, deps Dependencies, tenantID string, result *Result) (preexisting bool, err error) {
	roles := role.BuiltInRoles(tenantID)
	// Under the standard (non-strict) posture also seed the Owner
	// convenience role. In strict mode it is omitted entirely so a
	// compliant deployment's role store never contains an all-permission
	// role definition.
	if !deps.SeparationOfDuties {
		roles = append(roles, role.OwnerRole(tenantID))
	}
	resolver := roleResolver(deps)
	for _, r := range roles {
		_, createErr := deps.Roles.CreateRole(ctx, &role.CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		if createErr == nil {
			result.RolesCreated = true
			deps.Logger.Info("first-boot: role created",
				slog.String("name", r.Name),
				slog.String("id", r.ID),
				slog.String("tenant", tenantID),
			)
			continue
		}
		var nameExists *role.RoleNameExistsError
		if errors.As(createErr, &nameExists) {
			// A reserved built-in name can only ever denote its one
			// canonical role (pkg/role's requireCanonicalSystemRole pins the
			// id a canonical name may be created with), but this function
			// verifies anyway rather than trusting the error alone: a
			// record reached by name with an unexpected id, tenant,
			// permission set, or a System flag that does not match the
			// canonical definition is refused rather than silently accepted
			// as this run's own seed.
			if !BuiltInRoleAlreadySeededWithin(ctx, resolver, tenantID, r) {
				return false, &RolesRegistrationError{Role: r.Name, Cause: createErr}
			}
			deps.Logger.Debug("first-boot: role already exists (verified identical)",
				slog.String("name", r.Name),
				slog.String("tenant", tenantID),
			)
			preexisting = true
			continue
		}
		var idExists *role.RoleIDExistsError
		if errors.As(createErr, &idExists) {
			if !BuiltInRoleAlreadySeededWithin(ctx, resolver, tenantID, r) {
				return false, &RolesRegistrationError{Role: r.Name, Cause: createErr}
			}
			deps.Logger.Debug("first-boot: role already exists (verified identical)",
				slog.String("name", r.Name),
				slog.String("id", r.ID),
				slog.String("tenant", tenantID),
			)
			preexisting = true
			continue
		}
		return false, &RolesRegistrationError{Role: r.Name, Cause: createErr}
	}
	return preexisting, nil
}

// RoleMatchesSeed reports whether existing, the role found by want.Name, has a
// persisted id, tenant, name, System flag, and permission set that are ALL
// exactly want's — the only condition under which a collision on a built-in
// role's create is safe to treat as "this run's own idempotent re-attempt"
// rather than a genuine, unexplained conflict. It is the field-by-field
// verdict of BuiltInRoleAlreadySeededWithin, which owns the lookup, and
// RegisterBuiltInRoles' two call sites (a RoleNameExistsError and a
// RoleIDExistsError) reach it through that function the same way.
//
// The id is checked unconditionally, on BOTH collision kinds. For a reserved
// built-in name this is never a false refusal of a legitimate re-attempt:
// pkg/role's requireCanonicalSystemRole refuses any CreateRole for a reserved
// name whose requested id is not that name's one canonical
// role.SystemRoleID, so the only way a persisted "admin" record (for example)
// could carry a different id is a record written by a release that predates
// that guard, or under an identifier scheme this build no longer derives —
// both out of scope; there is no supported path by which two first-boot
// attempts for the SAME built-in role, run by this build, could ever
// disagree on its id. An id mismatch is therefore always the genuine,
// unexplained conflict this function exists to catch, never an idempotent
// re-attempt it would otherwise wrongly refuse.
//
// On an ID collision specifically, want.ID already names SOME record, but
// the record found by NAME could be a different one entirely — an unrelated
// custom role might coincidentally claim the canonical name while a wholly
// separate record blocks the id. Checking the id here confirms the record
// this function reads back is the SAME one causing the id collision before
// trusting it, closing the provenance gap go-qrdb#555's review raised: a
// general CreateRole caller must not be able to adopt another actor's role
// by supplying its name and a guessed id.
//
// Every field want.System distinguishes is checked independently, not
// inferred from the others matching: a persisted record whose id, tenant,
// and permissions all match while System is false was possible under an
// older release that predates deriving System from the canonical name
// (rather than accepting it on the request), and accepting such a record
// here would leave a role with a reserved name mutable and deletable
// through the ordinary role APIs — silently defeating the immutability a
// built-in role is supposed to have. Name is checked for the same reason on
// the id-collision path, where the record was reached by id rather than by
// the name this comparison assumes.
//
// Any field that does not match reports false, and so does a nil resolver, a
// lookup error or a nil result in BuiltInRoleAlreadySeededWithin: the pair
// fails closed, since the caller's only alternative on a false is to refuse
// the whole first-boot run rather than silently accept a role it cannot
// verify.
func RoleMatchesSeed(existing, want *role.Role) bool {
	if existing.ID != want.ID {
		return false
	}
	if existing.TenantID != want.TenantID {
		return false
	}
	if existing.Name != want.Name || existing.System != want.System {
		return false
	}
	return permissionSetsEqual(existing.Permissions, want.Permissions)
}

// BuiltInRoleSeedRetryInterval and BuiltInRoleSeedRetryWindow bound
// BuiltInRoleAlreadySeededWithin's retry. Package-level variables, not
// constants, so a test can shrink them rather than waiting out the
// production window; see TestRegisterBuiltInRoles_ConcurrentSeedersConverge.
var (
	BuiltInRoleSeedRetryInterval = 10 * time.Millisecond
	BuiltInRoleSeedRetryWindow   = 5 * time.Second
)

// BuiltInRoleAlreadySeededWithin looks the role up by name and reports
// RoleMatchesSeed's verdict on it, retrying with a bounded backoff while the verification lookup itself comes back not-found
// or erroring. Exported because it is the ONE place that answers "is this
// create collision my own idempotent re-attempt?" for every seeder of a
// built-in role, not only this package's own RegisterBuiltInRoles:
// pkg/bootstrap's seedTenantRoles (ProvisionTenant's tenant-scoped seeding)
// calls it too, rather than re-deriving the same verification a third time.
//
// A create collision proves SOME write already claimed the id or the name;
// it does not by itself prove that write has become visible to THIS read
// yet. Several concurrent seeders can legitimately produce exactly that
// lag:
//
//   - Two nodes' own first-boot runs, racing to seed the SAME built-in role
//     under the SAME deterministic id (role.SystemRoleID). Whichever loses
//     the create is told so by the LEADER, authoritatively, but its own
//     local StaleRead may not yet have applied the winner's entry — the
//     identical replication-visibility window role.Store's own
//     diagnoseAtomicGuardFailure already tolerates on the CREATE side of
//     this exact collision (see that function's doc).
//   - A tenant's CreateTenant-triggered RoleSeeder (pkg/server/lifecycle's
//     roleSeederAdapter) seeding the SAME tenant's built-ins concurrently
//     with first-boot's own RegisterBuiltInRoles loop. Even with both sides
//     writing atomically, the SAME replication lag applies across the two
//     callers' stores.
//   - Two concurrent ProvisionTenant calls for the SAME new tenant, racing
//     each other's seedTenantRoles the identical way.
//
// A role that DOES resolve but disagrees with want is a different thing
// entirely — a definitive, unexplained conflict — and returns false at
// once: no amount of waiting makes a mismatched record match, and retrying
// that case would only turn a correct, immediate refusal into a slow one.
//
// The whole wait is bounded by BuiltInRoleSeedRetryWindow so a GENUINE
// conflict (the id or name claimed by something that will never resolve to
// this canonical definition) still fails, just not before giving a
// legitimate concurrent winner a chance to replicate.
func BuiltInRoleAlreadySeededWithin(ctx context.Context, resolver role.NameResolver, tenantID string, want *role.Role) bool {
	if resolver == nil {
		return false
	}
	deadline := time.Now().Add(BuiltInRoleSeedRetryWindow)
	for {
		existing, err := resolver.GetRoleByName(ctx, tenantID, want.Name)
		if err == nil && existing != nil {
			return RoleMatchesSeed(existing, want)
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(BuiltInRoleSeedRetryInterval):
		}
	}
}

// permissionSetsEqual compares two permission lists as SETS: a grant's
// meaning does not depend on the order permissions were listed in, and a
// repeated permission grants nothing beyond one occurrence of it.
func permissionSetsEqual(a, b []permission.Permission) bool {
	as := make(map[permission.Permission]struct{}, len(a))
	for _, p := range a {
		as[p] = struct{}{}
	}
	bs := make(map[permission.Permission]struct{}, len(b))
	for _, p := range b {
		bs[p] = struct{}{}
	}
	if len(as) != len(bs) {
		return false
	}
	for p := range as {
		if _, ok := bs[p]; !ok {
			return false
		}
	}
	return true
}

// adminCreateAttempts bounds createAdminUser's retries of a generated-id
// collision with some other user. Each attempt draws a fresh id, so a second
// collision is already vanishingly unlikely; the bound exists so a store that
// reports the collision for every id cannot loop first boot forever.
const adminCreateAttempts = 3

// adminConvergeInterval and adminConvergeWindow bound how long
// createAdminUser waits for an unresolved admin username to resolve; see
// adminHolderUnresolved. Variables, not constants, so a test can shrink
// them rather than waiting out the production window.
var (
	adminConvergeInterval = 10 * time.Millisecond
	adminConvergeWindow   = 5 * time.Second
)

// createAdminUser creates the platform admin user inside the supplied
// tenant with a single role grant: Admin in that tenant. When tenantID
// is the system tenant, this single grant — Admin@system — confers
// cross-tenant authority via the system-tenant rule in pkg/auth/rbac.
//
// Returns true when the admin user was newly created in this run, false
// when the user already existed.
// issueAdminCert issues the admin certificate via the configured path. Path B
// (CSR-sign) is selected when AdminCSRPEM is set and Signer is provided; else
// Path A (generate-and-distribute) via CA. Returns (nil, nil, nil) when neither
// path is configured — cert issuance is optional.
func issueAdminCert(ctx context.Context, deps Dependencies, tenantID string) (certPEM, keyPEM []byte, err error) {
	if len(deps.AdminCSRPEM) > 0 && len(deps.AdminCertPEM) > 0 {
		return nil, nil, &ConflictingAdminCertInputsError{}
	}

	switch {
	// Path C is tested first because it is the only path that issues nothing.
	// A deployment that presents a certificate has already decided which
	// identity the admin authenticates as, so falling through to a CA that
	// happens to be configured would mint a second, unrelated credential.
	case len(deps.AdminCertPEM) > 0:
		adopted, adoptErr := adoptPresentedCert(deps.AdminCertPEM, nil, deps.AdminCertPool, deps.TrustDomain, tenantID)
		if adoptErr != nil {
			return nil, nil, adoptErr
		}
		return adopted, nil, nil
	case len(deps.AdminCSRPEM) > 0 && deps.Signer != nil:
		signed, signErr := deps.Signer.SignAdminCSR(ctx, tenantID, adminUsername, deps.AdminCSRPEM)
		if signErr != nil {
			return nil, nil, &AdminCertGenerationError{Cause: signErr}
		}
		return signed, nil, nil
	case deps.CA != nil:
		c, k, genErr := deps.CA.GenerateAdminCert(ctx, tenantID, adminUsername)
		if genErr != nil {
			return nil, nil, &AdminCertGenerationError{Cause: genErr}
		}
		return c, k, nil
	default:
		return nil, nil, nil
	}
}

// adoptPresentedCert validates a certificate the admin (or the system SO,
// whose chainPEM may carry intermediates) already holds and
// returns it unchanged for binding (Path C).
//
// "Adopt" rather than "issue": nothing is signed and no key is generated. The
// checks are the ones that decide whether this certificate may stand for the
// platform administrator — it parses, it is inside its validity window, and it
// chains to a trust anchor the deployment was configured with. That is the same
// bar ceremony.parseAndValidateCert applies to an externally enrolled officer,
// which is deliberate: the admin and the SO enrol against one rule, so an
// operator who can bring their own credential to one role can bring it to the
// other.
//
// It must also pass the mTLS authenticator's own tenant rule,
// auth.TenantFromClientCert under trustDomain (go-qrdb#777) -- a SPIFFE ID SAN
// naming the tenant its first OU names -- and that tenant must be tenantID, the
// one the admin is bound in. A certificate the rule refuses, or one naming
// another tenant, could never log in as this admin, so adopting it would leave
// the deployment with an administrator nobody can be. Each refusal is an
// *AdminCertRejectedError, returned before anything is bound.
//
// A nil pool is refused rather than treated as "trust anything"; see
// MissingAdminCertPoolError.
func adoptPresentedCert(certPEM, chainPEM []byte, pool *x509.CertPool, trustDomain, tenantID string) ([]byte, error) {
	if pool == nil {
		return nil, &MissingAdminCertPoolError{}
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, &AdminCertRejectedError{Cause: errors.New("no CERTIFICATE block in PEM input")}
	}
	cert, parseErr := x509.ParseCertificate(block.Bytes)
	if parseErr != nil {
		return nil, &AdminCertRejectedError{Cause: parseErr}
	}

	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, &AdminCertRejectedError{Cause: errors.New("certificate is outside its validity window")}
	}

	// ExtKeyUsageAny, matching the ceremony's officer check: a certificate
	// already resident on a PIV card carries whatever EKU its issuer chose, and
	// demanding ClientAuth here would reject cards this deployment can still
	// authenticate. Whether the certificate may be used for a given operation is
	// an authorization question answered at login, not an adoption-time one.
	var intermediates *x509.CertPool
	if len(chainPEM) > 0 {
		intermediates = x509.NewCertPool()
		intermediates.AppendCertsFromPEM(chainPEM)
	}
	if _, verifyErr := cert.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); verifyErr != nil {
		return nil, &AdminCertRejectedError{Cause: verifyErr}
	}

	certTenant, tenantErr := auth.TenantFromClientCert(cert, trustDomain)
	if tenantErr != nil {
		return nil, &AdminCertRejectedError{Cause: tenantErr}
	}
	if certTenant != tenantID {
		return nil, &AdminCertRejectedError{Cause: &AdminCertTenantError{CertTenant: certTenant, AdminTenant: tenantID}}
	}

	adopted := make([]byte, len(certPEM))
	copy(adopted, certPEM)
	return adopted, nil
}

// spkiThumbprintFromCertPEM parses a PEM certificate and returns its
// canonical SPKI thumbprint. Delegates to the one stack-wide implementation,
// user.ComputeSPKIThumbprintFromCertPEM.
func spkiThumbprintFromCertPEM(certPEM []byte) (string, error) {
	return user.ComputeSPKIThumbprintFromCertPEM(certPEM)
}

// createAdminUser creates the bootstrap admin. It returns the created user
// (nil when the admin already existed), a created flag, and an error.
func createAdminUser(ctx context.Context, deps Dependencies, tenantID string) (*user.User, bool, error) {
	adminRoleID := role.SystemRoleID(tenantID, role.RoleNameAdmin)
	roleIDs := []string{adminRoleID}

	// Under the standard (non-strict) posture, also grant the Owner role so
	// the bootstrap admin can run the entire database as a single identity.
	// In strict mode (FIPS 140-3 / auth.separation_of_duties) the admin
	// keeps only the canonical Admin@system grant and data-plane/governance
	// duties require the operator/SO identities.
	if !deps.SeparationOfDuties {
		roleIDs = append(roleIDs, role.SystemRoleID(tenantID, role.RoleNameOwner))
	}

	// A concurrent bootstrap attempt racing for the SAME fixed admin username
	// can lose on EITHER guard the atomic create batch holds: UsernameExistsError
	// when the username collided, or UserIDExistsError when the attempt's
	// generated id collided (go-qrdb#555's own failure shape — independently
	// seeded nodes racing first-boot). Either is first checked against the
	// persisted admin (adminUsernameHolder); a verified admin is this run's
	// idempotent success.
	//
	// A collision on the username can also come back before the winner's
	// record is visible to this read: a user store claims the username
	// before it writes the record, and a replica may not have applied the
	// winner's entry yet. That lookup finds nothing (adminHolderUnresolved),
	// which is a race still settling, not a refusal. The create is retried
	// after adminConvergeInterval, within adminConvergeWindow: it converges
	// on the winner's admin once the record lands, or creates the admin
	// itself if the winner rolled back and released the claim.
	//
	// An id collision that is NOT the admin — the generated id repeated some
	// other user's — says nothing about the admin at all: the username was
	// never the problem. The store generates a fresh id on every call, so it
	// is retried, a bounded number of times. A username that resolves to
	// anyone but the admin is never retried: that is a squatter, and first
	// boot refuses.
	users := userResolver(deps)
	var convergeDeadline time.Time
	idCollisions := 0
	for {
		adminUser, createErr := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
			// The firstboot admin authenticates by certificate, not by an email
			// login key, so the omission is declared rather than defaulted (#190).
			WithoutEmailLoginKey: true,
			Username:             adminUsername,
			TenantID:             tenantID,
			RoleIDs:              roleIDs,
			// System marks THE single system-tenant bootstrap admin as
			// immutable (go-qrdb#106): DeleteUser refuses it with
			// SystemUserImmutableError. No other identity in the cluster --
			// including a per-tenant admin minted by
			// pkg/bootstrap.ProvisionTenant -- carries this flag, so tenant
			// deprovisioning is never blocked by it.
			System: true,
		})
		if createErr == nil {
			deps.Logger.Info("first-boot: admin user created",
				slog.String("username", adminUsername),
				slog.String("tenant", tenantID),
				slog.Any("role_ids", roleIDs),
				slog.Bool("separation_of_duties", deps.SeparationOfDuties),
			)
			return adminUser, true, nil
		}
		var usernameExists *user.UsernameExistsError
		var idExists *user.UserIDExistsError
		usernameTaken := errors.As(createErr, &usernameExists)
		idTaken := errors.As(createErr, &idExists)
		if !usernameTaken && !idTaken {
			return nil, false, &AdminUserCreationError{Cause: createErr}
		}
		holder := adminUsernameHolder(ctx, users, tenantID, adminRoleID)
		if holder == adminHolderAdmin {
			deps.Logger.Debug("first-boot: admin user already exists (verified)",
				slog.String("username", adminUsername),
				slog.String("tenant", tenantID),
			)
			return reconcileExistingAdmin(ctx, deps, tenantID), false, nil
		}
		if usernameTaken && holder == adminHolderUnresolved {
			if convergeDeadline.IsZero() {
				convergeDeadline = time.Now().Add(adminConvergeWindow)
			}
			if !time.Now().Before(convergeDeadline) {
				return nil, false, &AdminUserCreationError{Cause: createErr}
			}
			deps.Logger.Debug("first-boot: admin username claimed by a write not yet visible; retrying",
				slog.String("username", adminUsername),
				slog.String("tenant", tenantID),
			)
			select {
			case <-ctx.Done():
				return nil, false, &AdminUserCreationError{Cause: ctx.Err()}
			case <-time.After(adminConvergeInterval):
			}
			continue
		}
		idCollisions++
		if usernameTaken || idCollisions >= adminCreateAttempts {
			return nil, false, &AdminUserCreationError{Cause: createErr}
		}
		deps.Logger.Info("first-boot: generated admin id collided with another user; retrying with a fresh id",
			slog.Int("attempt", idCollisions),
			slog.String("tenant", tenantID),
		)
	}
}

// systemPromoter is an OPTIONAL capability a concrete UserCreator may
// implement (pkg/user's store does). It is reached by type assertion rather
// than added to UserCreator itself, because a downstream composer's adapter
// (quicbaas, xkms) implementing only CreateUser against its own IAM store has
// no notion of qrdb's System flag -- skipping the reconciliation for such an
// adapter is correct, not a regression.
type systemPromoter interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
	PromoteToSystemUser(ctx context.Context, id uint64) (*user.User, error)
}

// reconcileExistingAdmin heals a bootstrap admin created before go-qrdb#106
// existed -- or one whose System flag did not persist for any other reason.
// createAdminUser's own CreateUser call cannot do this: an already-exists
// response tells it the admin is there, not what flag it carries. Every
// start of every node reaches here once the admin already exists, so a
// restarted node ends up with a protected admin even though its ONE create
// attempt, at whatever version first minted the record, may not have set the
// flag.
//
// Best-effort by design: a Users implementation that cannot reconcile (an
// adapter without the capability, or a transient store error) logs and
// leaves the existing record exactly as it was, rather than failing an
// otherwise-successful first-boot over a repair it was never guaranteed to
// be able to make.
func reconcileExistingAdmin(ctx context.Context, deps Dependencies, tenantID string) *user.User {
	reconciler, ok := deps.Users.(systemPromoter)
	if !ok {
		return nil
	}
	existing, err := reconciler.GetUserByUsername(ctx, tenantID, adminUsername)
	if err != nil || existing == nil {
		deps.Logger.Warn("first-boot: could not resolve the existing admin to check its System flag",
			slog.String("tenant", tenantID), slog.Any("error", err))
		return nil
	}
	if existing.System {
		return existing
	}
	promoted, perr := reconciler.PromoteToSystemUser(ctx, existing.ID)
	if perr != nil {
		deps.Logger.Warn("first-boot: could not promote the pre-existing admin to System",
			slog.String("tenant", tenantID), slog.Uint64("user_id", existing.ID), slog.Any("error", perr))
		return existing
	}
	deps.Logger.Info("first-boot: promoted a pre-existing admin to System (go-qrdb#106)",
		slog.String("tenant", tenantID), slog.Uint64("user_id", promoted.ID))
	return promoted
}
