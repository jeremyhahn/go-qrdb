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
}

// CertProvider is the minimal cert-issuance contract first-boot needs.
// qrdb's pkg/cert/DefaultCertProvider satisfies this, and go-xkms's
// CAAdapter (sdk/go NewCAAdapter) satisfies it, wiring the xkms CA into
// first-boot admin enrollment. Defined narrow here so firstboot doesn't
// pull in either package as a dependency.
type CertProvider interface {
	// GenerateAdminCert issues a client certificate for the platform
	// admin user. tenantID identifies the OU; subject is the CN
	// (typically "admin"). Returns PEM-encoded cert and key bytes.
	GenerateAdminCert(ctx context.Context, tenantID, subject string) (certPEM, keyPEM []byte, err error)
}

// CertSigner is the admin-mTLS Path B seam: the admin (or tenant admin) supplies
// a CSR and the CA signs it, so the private key never leaves the admin.
// go-xkms's CAAdapter (sdk/go NewCAAdapter) satisfies this (CSR signing).
type CertSigner interface {
	// SignAdminCSR signs an admin-supplied CSR and returns the PEM cert.
	SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) (certPEM []byte, err error)
}

// CertBinder is the store seam that pins an admin certificate's SPKI thumbprint
// to the admin user record so subsequent mTLS handshakes authenticate as the
// admin. The consumer's user store implements it (e.g. via
// user.UpdateUserRequest{CertBindings: ...}).
type CertBinder interface {
	// BindAdminCert binds the SPKI thumbprint to the admin user.
	BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error
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
// names the admin, the same way role.NameResolver lets registerBuiltInRoles
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

	// RoleResolver resolves a system role to the identifier it is actually
	// persisted under, so grants bind to that record rather than to a
	// freshly derived identifier. On a cluster seeded before system role
	// IDs moved off UUIDv5/SHA-1, the seeded roles keep their legacy
	// identifiers (seeding is name-unique and tolerates already-exists),
	// and a grant written against the derived identifier would name no
	// role at all.
	//
	// Leaving it nil is safe in two cases, and refused otherwise. It is
	// safe when Roles itself implements role.NameResolver — qrdb's own
	// role.RoleServicer does, and it is then used automatically — and when
	// this run actually seeds the roles, where the derived identifier is
	// the one being written. If the roles already existed and nothing can
	// resolve them, first-boot fails with UnresolvableSystemRoleError
	// rather than writing a guessed grant.
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
	// This mirrors what the ceremony already offers a security officer through
	// EnrollExternalOfficer: the admin and the SO enrol the same way.
	AdminCertPEM []byte
	// AdminCertPool holds the trust anchors a presented AdminCertPEM must chain
	// to. Required whenever AdminCertPEM is set, and unused otherwise.
	//
	// It is separate from the platform CA on purpose: a certificate the operator
	// already holds was, by definition, issued by something else — a corporate
	// PKI or a card vendor — so validating it against the CA this deployment is
	// about to create would reject every legitimate input.
	AdminCertPool *x509.CertPool

	// Binder pins the issued admin certificate's SPKI thumbprint to the admin
	// user so mTLS handshakes authenticate as the admin. Optional: when nil, the
	// cert is issued but not bound (caller binds separately).
	Binder CertBinder

	// Logger receives structured progress events. Required.
	Logger *slog.Logger

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

	// MTLSBindingPolicy is the fleet's auth.mtls_binding_policy. Under
	// auth.MTLSBindingRequired, Initialize refuses — before writing anything —
	// a configuration that would leave the admin unable to log in by
	// certificate: creating the admin needs a Binder and an admin certificate
	// path, and an existing admin must already hold a live binding (which
	// needs Users to implement AdminLookup). The error is
	// *AdminCertBindingRequiredError. The zero value, MTLSBindingEnrolled,
	// changes nothing.
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
//  2. Register the six built-in roles (SO/Admin/Operator/User/Auditor/
//     Custodian) under the system tenant.
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

	logger := deps.Logger
	tenantID := tenant.SystemTenantID
	result := &Result{}

	// Step 1: system tenant. Internal-call context bypasses the
	// reserved-id rejection that protects the public CreateTenant API.
	if err := createSystemTenant(ctx, deps, result); err != nil {
		return nil, err
	}

	// Step 2: built-in roles. Tolerates already-exists for idempotency.
	rolesPreexisted, err := registerBuiltInRoles(ctx, deps, tenantID, result)
	if err != nil {
		return nil, err
	}

	// Step 3: admin user with Admin@system grant. Tolerates
	// already-exists for idempotency. Cert generation only runs on the
	// initial create so a re-run does not rotate the platform key
	// material.
	adminUser, created, err := createAdminUser(ctx, deps, tenantID, rolesPreexisted)
	if err != nil {
		return nil, err
	}
	result.AdminCreated = created

	// Step 4: platform admin cert + SPKI binding. Only when the admin user was
	// newly created in this run. Two cert-issuance paths, both ending in the
	// SPKI thumbprint being bound to the admin user:
	//   Path B (CSR-sign): AdminCSRPEM supplied -> Signer signs it; the admin
	//     keeps their private key (AdminKeyPEM stays nil).
	//   Path A (generate-and-distribute): else CA generates the key+cert bundle,
	//     which must be securely distributed to the admin.
	// There is no invite/activation token — enrollment is cert-based.
	if created {
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
	_, err := deps.Tenants.CreateTenant(internalCtx, tenant.SystemTenantID, systemTenantDisplayName, tenant.IsolationPrefix)
	if err == nil {
		result.SystemTenantCreated = true
		deps.Logger.Info("first-boot: system tenant created",
			slog.String("tenant", tenant.SystemTenantID),
		)
		return nil
	}
	if errors.Is(err, tenant.ErrTenantAlreadyExists) {
		deps.Logger.Debug("first-boot: system tenant already exists",
			slog.String("tenant", tenant.SystemTenantID),
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

// adminUserAlreadyExists reports whether tenantID already holds the admin
// this first boot would have created, verified by reading the record back
// rather than assumed from the create error's type alone.
//
// "The admin" means every property first boot itself gives the account, not
// only its username: the persisted record must name adminUsername in
// tenantID, hold adminRoleID (the tenant-scoped Admin role), carry
// System == false (first boot never creates a system user), and be in a
// defined, non-terminal lifecycle. The lifecycle is deliberately NOT pinned to
// active: an operator may since have suspended, locked or disabled the admin,
// and a re-run must stay idempotent across that. What it must never adopt is a
// record that is no account at all — deleting, or a state this build does not
// know — nor an unrelated user that merely holds the username.
//
// A nil resolver, a lookup error, or a nil result all report false: this
// function fails closed, since the caller's only alternative on a false is
// to refuse the whole first-boot run rather than silently accept a
// collision it cannot verify.
func adminUserAlreadyExists(ctx context.Context, resolver UserResolver, tenantID, adminRoleID string) bool {
	if resolver == nil {
		return false
	}
	existing, err := resolver.GetUserByUsername(ctx, tenantID, adminUsername)
	if err != nil || existing == nil {
		return false
	}
	return existing.TenantID == tenantID &&
		existing.Username == adminUsername &&
		slices.Contains(existing.RoleIDs, adminRoleID) &&
		!existing.System &&
		existing.Lifecycle.Valid() && !existing.Lifecycle.Terminal()
}

// registerBuiltInRoles installs every role returned by role.BuiltInRoles
// under the supplied tenant. Already-exists for any individual role is
// treated as idempotent success; only structural errors propagate.
//
// It reports whether any role was found to already exist. That distinguishes
// a store this run seeded — where deriving a system role ID is exactly right,
// because it is the identifier just written — from one seeded earlier, whose
// identifiers this code may be unable to derive at all.
//
// Two DIFFERENT "already exists" outcomes are tolerated, and BOTH are
// verified by reading the persisted record back — see
// builtInRoleAlreadySeeded, which every branch below calls with the checkID
// value matching its collision kind:
//
//   - A name collision (RoleNameExistsError) does not need the persisted id
//     to equal the freshly derived one: role.SystemRoleID's derivation has
//     changed hash algorithm before (FIPS 140-3's move off SHA-1), and a
//     cluster seeded under the retired scheme keeps its roles under their
//     legacy identifiers rather than this run treating the mismatch as a
//     reason to refuse. Every OTHER field — tenant, name, System, and the
//     permission set — must still match exactly: an earlier release
//     persisted every built-in role with System: false (CreateRoleRequest
//     had no such field then), and accepting a record like that here would
//     leave a reserved-name role mutable and deletable through the ordinary
//     role APIs.
//   - An id collision (RoleIDExistsError) does not answer "is this already
//     seeded" from the error alone — see go-qrdb#555. CreateRole's atomic
//     path guards the id claim and the name together and diagnoses the id
//     first, so two concurrent first-boot attempts for the SAME built-in
//     role (the common case an id collision means here) are
//     indistinguishable, by error type alone, from an id chosen by an
//     unrelated caller that happens to collide.
//     This function is the one place trusted to tell them apart, because it
//     — and only it — knows the FULL canonical definition an id collision
//     might legitimately mean: it reads the persisted record back by name
//     and refuses unless every field, id included, matches exactly.
//     CreateRole itself is never asked to make this judgment, on purpose: a
//     general create endpoint accepting "same name and a guessed id means
//     treat it as mine" would let any caller holding roles:create adopt
//     another actor's in-flight or malformed role.
func registerBuiltInRoles(ctx context.Context, deps Dependencies, tenantID string, result *Result) (preexisting bool, err error) {
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
			// verifies anyway rather than trusting the error alone: the same
			// provenance check the id-collision branch below performs is run
			// here too, so a record reached by name with an unexpected id,
			// tenant, permission set, or a System flag that does not match
			// the canonical definition is refused rather than silently
			// accepted as this run's own seed.
			if !builtInRoleAlreadySeeded(ctx, resolver, tenantID, r, false) {
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
			if !builtInRoleAlreadySeeded(ctx, resolver, tenantID, r, true) {
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

// builtInRoleAlreadySeeded reports whether the tenant already holds a role
// named want.Name whose persisted tenant, name, System flag, and permission
// set are EXACTLY want's — the only condition under which a collision on a
// built-in role's create is safe to treat as "this run's own idempotent
// re-attempt" rather than a genuine, unexplained conflict. checkID selects
// whether the persisted id must ALSO equal want.ID exactly.
//
// checkID must be false for a RoleNameExistsError and true for a
// RoleIDExistsError; registerBuiltInRoles' two call sites pass the matching
// value and this is not a caller-tunable safety knob.
//
//   - On a NAME collision the store's own name-uniqueness rule already
//     proves the record this function reads back IS the one blocking the
//     create — there is exactly one way to be blocked on a canonical name.
//     Its id is deliberately NOT required to equal want.ID:
//     role.SystemRoleID's derivation has changed hash algorithm before
//     (UUIDv5/SHA-1 to UUIDv8/SHA-256, for FIPS 140-3), and a cluster seeded
//     under the retired scheme keeps its roles under their legacy
//     identifiers rather than this run silently trying to mint a second
//     "admin" role under the current one.
//   - On an ID collision, want.ID already names SOME record, but the record
//     found by NAME could be a different one entirely — an unrelated custom
//     role might coincidentally claim the canonical name while a wholly
//     separate record blocks the id. checkID=true confirms the record this
//     function reads back is the SAME one causing the id collision before
//     trusting it, closing the provenance gap go-qrdb#555's review raised:
//     a general CreateRole caller must not be able to adopt another actor's
//     role by supplying its name and a guessed id.
//
// Every field want.System distinguishes is checked independently in BOTH
// cases, not inferred from the others matching: a persisted record whose id,
// tenant, and permissions all match while System is false was possible under
// an older release that predates deriving System from the canonical name
// (rather than accepting it on the request), and accepting such a record here
// would leave a role with a reserved name mutable and deletable through the
// ordinary role APIs — silently defeating the immutability a built-in role
// is supposed to have. Name is checked for the same reason on the
// id-collision path, where the record was reached by id rather than by the
// name this comparison assumes.
//
// A nil resolver, a lookup error, a nil result, or any field that does not
// match all report false: this function fails closed, since the caller's
// only alternative on a false is to refuse the whole first-boot run rather
// than silently accept a role it cannot verify.
func builtInRoleAlreadySeeded(ctx context.Context, resolver role.NameResolver, tenantID string, want *role.Role, checkID bool) bool {
	if resolver == nil {
		return false
	}
	existing, err := resolver.GetRoleByName(ctx, tenantID, want.Name)
	if err != nil || existing == nil {
		return false
	}
	if checkID && existing.ID != want.ID {
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
		adopted, adoptErr := adoptPresentedAdminCert(deps.AdminCertPEM, deps.AdminCertPool)
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

// adoptPresentedAdminCert validates a certificate the admin already holds and
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
// A nil pool is refused rather than treated as "trust anything"; see
// MissingAdminCertPoolError.
func adoptPresentedAdminCert(certPEM []byte, pool *x509.CertPool) ([]byte, error) {
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
	if _, verifyErr := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); verifyErr != nil {
		return nil, &AdminCertRejectedError{Cause: verifyErr}
	}

	adopted := make([]byte, len(certPEM))
	copy(adopted, certPEM)
	return adopted, nil
}

// spkiThumbprintFromCertPEM parses a PEM certificate and returns its canonical
// SPKI thumbprint via user.ComputeSPKIThumbprint.
func spkiThumbprintFromCertPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("firstboot: admin cert PEM has no CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return user.ComputeSPKIThumbprint(cert), nil
}

// createAdminUser creates the bootstrap admin. It returns the created user
// (nil when the admin already existed), a created flag, and an error.
func createAdminUser(ctx context.Context, deps Dependencies, tenantID string, rolesPreexisted bool) (*user.User, bool, error) {
	resolver := roleResolver(deps)
	// Deriving a system role ID is correct only when this run seeded the
	// store, because the derived value is then exactly what was written. If
	// the roles were already there and nothing can tell us which identifiers
	// they carry, the derived value is a guess — and on a cluster seeded
	// before the UUIDv5 migration it is the wrong guess, producing an admin
	// bound to no role at all while bootstrap reports success.
	if resolver == nil && rolesPreexisted {
		return nil, false, &UnresolvableSystemRoleError{Role: role.RoleNameAdmin, TenantID: tenantID}
	}

	adminRoleID, err := role.ResolveSystemRoleID(ctx, resolver, tenantID, role.RoleNameAdmin)
	if err != nil {
		return nil, false, err
	}
	roleIDs := []string{adminRoleID}

	// Under the standard (non-strict) posture, also grant the Owner role so
	// the bootstrap admin can run the entire database as a single identity.
	// In strict mode (FIPS 140-3 / auth.separation_of_duties) the admin
	// keeps only the canonical Admin@system grant and data-plane/governance
	// duties require the operator/SO identities.
	if !deps.SeparationOfDuties {
		ownerRoleID, ownerErr := role.ResolveSystemRoleID(ctx, resolver, tenantID, role.RoleNameOwner)
		if ownerErr != nil {
			return nil, false, ownerErr
		}
		roleIDs = append(roleIDs, ownerRoleID)
	}

	// A concurrent bootstrap attempt racing for the SAME fixed admin username
	// can lose on EITHER guard the atomic create batch holds: UsernameExistsError
	// when the username collided, or UserIDExistsError when the attempt's
	// generated id collided (go-qrdb#555's own failure shape — independently
	// seeded nodes racing first-boot). Either is first checked against the
	// persisted admin (adminUserAlreadyExists); a verified admin is this run's
	// idempotent success.
	//
	// An id collision that is NOT the admin — the generated id repeated some
	// other user's — says nothing about the admin at all: the username was
	// never the problem. The store generates a fresh id on every call, so it
	// is retried, a bounded number of times. A username held by anyone but
	// the admin is never retried: that is a squatter, and first boot refuses.
	users := userResolver(deps)
	for attempt := 1; ; attempt++ {
		adminUser, createErr := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
			// The firstboot admin authenticates by certificate, not by an email
			// login key, so the omission is declared rather than defaulted (#190).
			WithoutEmailLoginKey: true,
			Username:             adminUsername,
			TenantID:             tenantID,
			RoleIDs:              roleIDs,
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
		if adminUserAlreadyExists(ctx, users, tenantID, adminRoleID) {
			deps.Logger.Debug("first-boot: admin user already exists (verified)",
				slog.String("username", adminUsername),
				slog.String("tenant", tenantID),
			)
			return nil, false, nil
		}
		if usernameTaken || attempt >= adminCreateAttempts {
			return nil, false, &AdminUserCreationError{Cause: createErr}
		}
		deps.Logger.Info("first-boot: generated admin id collided with another user; retrying with a fresh id",
			slog.Int("attempt", attempt),
			slog.String("tenant", tenantID),
		)
	}
}
