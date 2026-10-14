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

// Package bootstrap is qrdb's canonical bootstrap orchestrator. It composes
// the existing qrdb primitives — pkg/seal (StorageBarrier + BarrierRegistry),
// pkg/firstboot (system tenant + roles + admin + admin-mTLS), pkg/role, and
// pkg/tenant — into three entry points that model the control-plane /
// data-plane split:
//
//   - BootstrapSystem    — daemon init: bring up the SYSTEM barrier per the
//     chosen strategy (none / passphrase / shamir) and run first-boot
//     (system tenant + roles + system admin + admin-mTLS).
//   - ProvisionTenant    — system-admin action: minimal control-plane tenant
//     provisioning (tenant record + tenant-admin user with a tenant-scoped
//     admin role + tenant-admin cert enrollment). It does NOT initialize a
//     tenant barrier; the system admin holds no tenant key.
//   - InitializeTenantBarrier — tenant-admin self-service: register and
//     initialize THAT tenant's own barrier with the tenant's chosen strategy
//     and credentials.
//
// This package introduces no new cryptography. Every cryptographic operation
// is delegated to the composed primitives. Authorization (who may invoke each
// entry point) is enforced at the API / transport layer, NOT here.
package bootstrap

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Strategy identifiers accepted by the bootstrap orchestrator. They are the
// canonical operator-facing names from the identity/barrier unification plan.
// "passphrase" maps to the existing software (deferred-passphrase) strategy;
// "none" and "shamir" map to their seal package equivalents. Hardware-backed
// strategies are never selected here — they are injected into the barrier
// registry by xkms at composition time.
const (
	// StrategyNone disables barrier encryption (passthrough).
	StrategyNone = seal.StrategyNone

	// StrategyPassphrase protects the root key with an operator passphrase
	// (Argon2id / PBKDF2-FIPS via the deferred software strategy). It is the
	// canonical operator-facing identifier.
	StrategyPassphrase = seal.StrategyPassphrase

	// StrategyShamir splits the root key into M-of-N Shamir shares.
	StrategyShamir = seal.StrategyShamir
)

// FirstAdminPinner pins the SPKI thumbprint of a barrier's first admin as
// the signer of its epoch-1 init statement. The pin is written once: pinning
// the same thumbprint again succeeds, any other is refused.
// *barrierinit.Pins implements it.
type FirstAdminPinner interface {
	PinFirstAdmin(ctx context.Context, b clusterseal.Barrier, thumbprint string) error
}

// Dependencies carries the seams every entry point composes. The same struct
// is shared by all three entry points; an individual call uses only the fields
// it needs (documented per entry point). Pass by interface so the orchestrator
// works for any binary in the stack.
type Dependencies struct {
	// Tenants is the tenant-creation seam (firstboot.TenantCreator).
	Tenants firstboot.TenantCreator

	// Users is the user-creation seam (firstboot.UserCreator).
	Users firstboot.UserCreator

	// Roles is the role-creation seam (firstboot.RoleCreator).
	Roles firstboot.RoleCreator

	// RoleResolver is forwarded to firstboot.Dependencies.RoleResolver; see
	// that field's doc comment for what it verifies. Leaving it nil is safe
	// when Roles itself implements role.NameResolver, which qrdb's own
	// role.RoleServicer does and which is then used automatically.
	RoleResolver role.NameResolver

	// UserResolver looks the admin user up by username so createAdminUser can
	// verify a create-time collision — on either the admin's generated id or
	// its fixed username — actually names the admin before treating it as
	// this run's own idempotent re-attempt (firstboot.Dependencies.UserResolver,
	// go-qrdb#555). Leaving it nil is safe when Users itself implements
	// firstboot.UserResolver — qrdb's own user.UserServicer does, and it is
	// then used automatically.
	UserResolver firstboot.UserResolver

	// CA issues the SYSTEM bootstrap admin's certificate through first boot
	// (BootstrapSystem). ProvisionTenant never uses it: a tenant admin's key
	// is never generated for it.
	CA firstboot.CertProvider

	// Signer signs an admin-supplied CSR. ProvisionTenant requires it for
	// TenantProvisionConfig.AdminCSRPEM; qrdb's own transports pass
	// KeyBSigner (the tenant's own Key B CA). When it also implements
	// RevokeCert, an exclusive provisioning that is undone revokes what it
	// signed.
	Signer firstboot.CertSigner

	// Binder pins a certificate's SPKI thumbprint to its user record. A
	// tenant admin's certificate is bound through Users' own AddCertBinding
	// when Users provides it (the binding then carries the certificate's
	// expiry); Binder is the fallback for a Users seam that does not.
	Binder firstboot.CertBinder

	// AdminFactors is the canonical auth-factor query seam
	// (AdminFactorChecker, fail-closed). REQUIRED by ProvisionTenant in
	// deferred enrollment mode, where it proves the admin holds no active
	// credential before an invitation target is handed back; unused
	// otherwise. qrdb's production user auth-factor service implements it
	// (service.ActiveFactorChecker); assert the servicer to that interface
	// when wiring.
	AdminFactors AdminFactorChecker

	// Barriers is the seal barrier registry. Required by BootstrapSystem
	// (system barrier) and InitializeTenantBarrier (tenant barriers). Unused
	// by ProvisionTenant.
	Barriers *seal.BarrierRegistry

	// TrustAnchors registers a tenant's trust anchors
	// (TenantProvisionConfig.TrustAnchorsPEM). Required by ProvisionTenant
	// when anchors are supplied.
	TrustAnchors trustanchor.Registry

	// PendingAdmins records the first admin of a tenant whose identity
	// records live behind its own barrier (tenant.IAMBehindTenantBarrier)
	// until that barrier is initialized; CompletePendingTenantAdmin then
	// creates the account. Required by ProvisionTenant in cert mode for such
	// a tenant.
	PendingAdmins tenantadmin.Store

	// BarrierAnchors pins a pending first admin's SPKI thumbprint as its
	// tenant barrier's epoch-1 anchor, the signer every node verifies that
	// barrier's init statement against (docs/barrier-seal/architecture.md
	// "Per-node barrier init"). The pending record is consumed when the admin's
	// account is created, the pin is not: a node that initializes its key later
	// still needs it. Nil pins nothing, which is right only for a composition
	// that claims no init statements (tenant.WithBarrierInitBook).
	BarrierAnchors FirstAdminPinner

	// TrustDomain is the SPIFFE trust domain the node's mTLS authenticator
	// pins, so a bring-your-own admin certificate's identity is read exactly
	// as a login will read it. Empty accepts any, as the authenticator does.
	TrustDomain string

	// Now is ProvisionTenant's clock for certificate and anchor validity.
	// Nil is time.Now.
	Now func() time.Time

	// Logger receives structured progress events. Required.
	Logger *slog.Logger
}

// ---------------------------------------------------------------------------
// BootstrapSystem
// ---------------------------------------------------------------------------

// SystemConfig configures system bootstrap: the system barrier strategy and
// the first-boot RBAC posture.
type SystemConfig struct {
	// Strategy selects the system barrier sealing strategy: none, passphrase,
	// or shamir. Empty defaults to none.
	Strategy seal.StrategyID

	// Passphrase is REQUIRED when Strategy is passphrase. There is no derived
	// default. Ignored for none and shamir.
	//
	// It is a *secure.Buffer and not a string or a []byte. This struct is
	// the caller's own choice of representation — nothing decodes it, no
	// generator writes it, no flag parser fills it — so it is a HOLDER, and
	// pkg/secure's rule 3 applies with its remedy available (go-qrdb#429).
	// The material is copied out under a borrow exactly once, at the point
	// it becomes the seal.Credentials the barrier demands, and the caller
	// keeps ownership of the buffer: BootstrapSystem does not destroy it.
	Passphrase *secure.Buffer

	// SeparationOfDuties selects the first-boot RBAC posture. See
	// firstboot.Dependencies.SeparationOfDuties.
	SeparationOfDuties bool

	// MTLSBindingPolicy is the fleet's auth.mtls_binding_policy, forwarded to
	// first-boot. See firstboot.Dependencies.MTLSBindingPolicy.
	MTLSBindingPolicy auth.MTLSBindingPolicy

	// AdminCSRPEM, when non-empty, selects the admin-mTLS CSR-sign path
	// (Path B) for the system admin: the admin's CSR is signed via the
	// Signer seam. When empty, Path A (generate-and-distribute via CA) is
	// used.
	//
	// go-qrdb's own compositions (qrdbd, sdk/go/node) no longer set this:
	// the ceremony already issued the admin's certificate during
	// Initialize, and AdminCertPEM carries the result. It remains the
	// correct field for a composer that drives CA/Signer directly instead
	// of going through the ceremony.
	AdminCSRPEM []byte

	// AdminCertPEM is the bootstrap admin's certificate, already issued or
	// adopted by pkg/ceremony.CeremonyService.Initialize (go-qrdb#548): the
	// ceremony resolves the operator's CSR-sign / BYOC / generate choice and
	// signs or validates synchronously, where the CA is available, before the
	// admin account exists. BootstrapSystem passes this straight to
	// firstboot.Dependencies.AdminCertPEM, which only needs to BIND it, not
	// issue it -- so CA/Signer stay nil for this path.
	AdminCertPEM []byte

	// AdminCertPool is the trust anchor AdminCertPEM is (re-)validated against
	// before binding -- normally a pool containing the cluster's own CA
	// (ceremony.CeremonyService.CACertPool()), since AdminCertPEM was issued
	// by that same CA. Required whenever AdminCertPEM is set.
	AdminCertPool *x509.CertPool
}

// SystemResult reports what BootstrapSystem did: the system barrier strategy
// it initialized under and the first-boot result.
type SystemResult struct {
	// BarrierStrategy is the strategy the system barrier was initialized
	// under. Empty when the barrier was already initialized on this run.
	BarrierStrategy seal.StrategyID

	// BarrierAlreadyInitialized is true when the system barrier was already
	// initialized before this run (idempotent re-run).
	BarrierAlreadyInitialized bool

	// FirstBoot is the first-boot result (system tenant + roles + admin +
	// admin-mTLS). Never nil on success.
	FirstBoot *firstboot.Result
}

// BootstrapSystem initializes the system barrier per cfg.Strategy and then
// runs first-boot. It composes deps.Barriers.SystemBarrier() (already
// configured with the registered strategies) and firstboot.Initialize.
//
// Uses: Barriers (required), Logger (required), Tenants/Users/Roles (required
// by first-boot), CA/Signer/Binder (optional, admin-mTLS).
//
// The system barrier is initialized idempotently: an already-initialized
// barrier is treated as success with BarrierAlreadyInitialized set.
func BootstrapSystem(ctx context.Context, cfg SystemConfig, deps Dependencies) (*SystemResult, error) {
	if deps.Logger == nil {
		return nil, &MissingLoggerError{}
	}
	if deps.Barriers == nil {
		return nil, &MissingBarriersError{}
	}

	strategy := cfg.Strategy
	if strategy == "" {
		strategy = StrategyNone
	}
	if err := validateStrategy(strategy); err != nil {
		return nil, err
	}

	result := &SystemResult{}

	// Honour the locked deferred-init architecture: for passphrase/shamir the
	// system barrier comes up SEALED and is initialized+unsealed out-of-band
	// (admin barrier API for passphrase, ceremony for shamir) BEFORE
	// BootstrapSystem runs. By the time first-boot is reachable the barrier is
	// therefore already unsealed. StorageBarrier exposes no IsInitialized();
	// !IsSealed() is the canonical "already initialized and live" signal (an
	// unsealed barrier has necessarily been initialized). When the barrier is
	// already unsealed we MUST NOT demand a passphrase or re-initialize — we
	// skip both credentialsForStrategy and Initialize and proceed to first-boot.
	system := deps.Barriers.SystemBarrier()
	switch {
	case !system.IsSealed():
		result.BarrierAlreadyInitialized = true
		deps.Logger.Info("bootstrap: system barrier already initialized")
	case strategy == StrategyShamir:
		// Shamir barrier initialization is the ceremony's job (multiple SOs),
		// never inline here. A still-sealed shamir barrier means the ceremony
		// has not completed; first-boot below would fail against a sealed data
		// plane, so the operator must drive the ceremony first. We do NOT
		// initialize it and we do NOT demand a single passphrase. Proceed so
		// first-boot runs once the ceremony has unsealed the barrier.
		deps.Logger.Info("bootstrap: shamir system barrier sealed; deferring to ceremony for initialization")
	default:
		// Sealed AND uninitialized for none/passphrase: compute credentials
		// (passphrase strategy demands a non-empty passphrase) and initialize.
		creds, credErr := credentialsForStrategy(ctx, strategy, cfg.Passphrase)
		if credErr != nil {
			return nil, credErr
		}
		// creds.Secret is protected memory that this function owns — the
		// credential is built here and its lifetime ends here, so this is
		// where it is destroyed. It used to be an ORDINARY HEAP COPY of the
		// passphrase, wiped at this same point (go-qrdb#429); the copy no
		// longer exists, because credentialsForStrategy moves the material
		// straight from the mlocked source into a sealed enclave, and the
		// strategy opens that enclave itself for exactly as long as it needs
		// the passphrase.
		//
		// DEFERRED TO FUNCTION EXIT RATHER THAN DESTROYED INLINE AFTER
		// Initialize, for the same reason the wipe was: it then runs on EVERY
		// path, including the early return that a failed Initialize takes,
		// and a ciphertext held a few hundred microseconds longer costs
		// nothing. Destroy is nil-safe, so the none strategy's empty
		// Credentials is fine.
		defer seal.DestroyCredentials(creds)
		if initErr := system.Initialize(ctx, creds); initErr != nil {
			if errors.Is(initErr, seal.ErrAlreadyInitialized) {
				result.BarrierAlreadyInitialized = true
				deps.Logger.Info("bootstrap: system barrier already initialized")
			} else {
				return nil, &SystemBarrierInitError{Cause: initErr}
			}
		} else {
			result.BarrierStrategy = strategy
			deps.Logger.Info("bootstrap: system barrier initialized",
				slog.String("strategy", string(strategy)))
		}
	}

	fbResult, fbErr := firstboot.Initialize(ctx, firstboot.Dependencies{
		Tenants:            deps.Tenants,
		Users:              deps.Users,
		Roles:              deps.Roles,
		RoleResolver:       deps.RoleResolver,
		UserResolver:       deps.UserResolver,
		CA:                 deps.CA,
		Signer:             deps.Signer,
		AdminCSRPEM:        cfg.AdminCSRPEM,
		AdminCertPEM:       cfg.AdminCertPEM,
		AdminCertPool:      cfg.AdminCertPool,
		Binder:             deps.Binder,
		Logger:             deps.Logger,
		SeparationOfDuties: cfg.SeparationOfDuties,
		MTLSBindingPolicy:  cfg.MTLSBindingPolicy,
	})
	if fbErr != nil {
		return nil, &FirstBootError{Cause: fbErr}
	}
	result.FirstBoot = fbResult

	deps.Logger.Info("bootstrap: system bootstrap complete",
		slog.String("strategy", string(strategy)),
		slog.Bool("barrier_already_initialized", result.BarrierAlreadyInitialized))

	return result, nil
}

// ---------------------------------------------------------------------------
// ProvisionTenant
// ---------------------------------------------------------------------------

// AdminEnrollmentMode selects how the initial tenant admin's credential is
// established during ProvisionTenant.
//
// This seam exists so every consumer composes tenant-admin onboarding the
// same way (owner ruling 2026-09-01, company doc 08 §"Admin enrollment"):
// certificate enrollment stays in qrdb, while the invitation path — issuing
// a single-use token via pkg/invite's TenantInviteStore and binding the
// first auth factor at redemption — is layered by the composing host
// (go-quicbaas) on top of the deferred mode. System-level bootstrap remains
// cert-only and has no such mode.
type AdminEnrollmentMode string

const (
	// AdminEnrollCert enrolls the tenant admin's mTLS certificate during
	// provisioning: the admin's CSR, signed via Signer, or the admin's own
	// certificate, verified against the tenant's trust anchors -- exactly
	// one, and bound to the admin's account. No key is ever generated for
	// the admin. This is the default; an empty AdminEnrollmentMode selects
	// it.
	AdminEnrollCert AdminEnrollmentMode = "cert"

	// AdminEnrollDeferred creates the tenant record, seeds the tenant
	// roles, and creates the tenant-admin user PENDING (disabled, no
	// credential bound). The consumer attaches an invitation to the
	// returned user; redemption binds the admin's first auth factor and
	// activates the account. No certificate is issued and no key material
	// exists at provisioning time.
	AdminEnrollDeferred AdminEnrollmentMode = "deferred"
)

// TenantProvisionConfig configures minimal control-plane tenant provisioning.
type TenantProvisionConfig struct {
	// TenantID is the new tenant's id. Required.
	TenantID string

	// DisplayName is the human-readable tenant name. Empty defaults to the
	// tenant id.
	DisplayName string

	// Isolation selects the tenant isolation type. Empty defaults to
	// IsolationBarrier (per-tenant barrier).
	Isolation tenant.IsolationType

	// AdminUsername is the tenant-admin login name. Empty defaults to "admin".
	AdminUsername string

	// AdminCSRPEM is the tenant admin's certificate signing request: the
	// admin keeps its key, and the CSR is signed via the Signer seam (qrdb's
	// own transports: the tenant's Key B CA, KeyBSigner). In cert mode exactly
	// one of AdminCSRPEM and AdminCertPEM is required; combining either with
	// AdminEnrollDeferred is a ConflictingAdminEnrollmentError.
	AdminCSRPEM []byte

	// AdminCertPEM is the tenant admin's own (bring-your-own) certificate,
	// leaf first. It must chain to one of TrustAnchorsPEM for client
	// authentication and authenticate as AdminUsername in TenantID.
	AdminCertPEM []byte

	// AdminChainPEM holds intermediates for AdminCertPEM's chain. Optional.
	AdminChainPEM []byte

	// TrustAnchorsPEM registers the tenant's trust anchors (pkg/trustanchor)
	// as part of its creation. Optional, and required for AdminCertPEM.
	TrustAnchorsPEM []byte

	// CreateOptions are applied to the tenant record's creation.
	CreateOptions []tenant.CreateTenantOption

	// Exclusive makes provisioning a creation rather than a convergence: an
	// existing tenant is refused (TenantRecordCreationError wrapping
	// tenant.ErrTenantAlreadyExists), and a failure after the tenant record
	// is created undoes everything this call created, the tenant record
	// last (ProvisioningUndoneError). Undoing a tenant deletes it, which
	// retires its id. Every input that can be refused is judged before the
	// record is created, so only an infrastructure failure gets that far.
	// qrdb's transports provision exclusively; a composer re-running
	// first-boot style provisioning does not.
	Exclusive bool

	// AdminEnrollment selects the tenant-admin enrollment mode. Empty
	// defaults to AdminEnrollCert (existing behavior); any other value than
	// the declared modes is an InvalidAdminEnrollmentModeError.
	AdminEnrollment AdminEnrollmentMode
}

// TenantProvisionResult reports what ProvisionTenant created.
type TenantProvisionResult struct {
	// TenantID is the provisioned tenant id.
	TenantID string

	// AdminUserID is the created tenant-admin user id.
	AdminUserID uint64

	// AdminUsername is the created tenant-admin username.
	AdminUsername string

	// AdminCertPEM is the tenant admin's certificate: the one signed from
	// AdminCSRPEM, or the bring-your-own one as enrolled. Nil in deferred
	// mode and on an idempotent re-run that found the admin already present.
	// There is never a private key: the admin keeps its own.
	AdminCertPEM []byte

	// AdminCertSerial is the signed certificate's serial; empty for a
	// bring-your-own certificate, which this cluster did not issue.
	AdminCertSerial string

	// AdminCertThumbprint is the SPKI SHA-256 thumbprint of the admin's
	// certificate, bound to the admin's account.
	AdminCertThumbprint string

	// AdminAwaitsBarrierInit reports that the tenant keeps its identity
	// records behind its own barrier (tenant.IAMBehindTenantBarrier), so the
	// admin's account does not exist yet: the certificate is bound in a
	// pending record (pkg/tenantadmin) that admits only it to initialize the
	// tenant barrier, and the account is created when it is
	// (CompletePendingTenantAdmin). AdminUserID is then zero.
	AdminAwaitsBarrierInit bool

	// AdminRoleID is the tenant-scoped Admin role identifier the tenant-admin
	// user is bound to. In deferred mode it is, with AdminUserID, what the
	// consumer needs to attach an invitation to the pending admin.
	AdminRoleID string

	// AdminPending reports that the tenant admin awaits invitation
	// redemption to bind its first auth factor and activate. It is true
	// exactly when AdminEnrollDeferred succeeded: deferred provisioning
	// resolves only a truly pending admin (disabled, no credential bound)
	// and fails closed on any other preexisting state.
	AdminPending bool
}

// ProvisionTenant performs MINIMAL control-plane provisioning of a new tenant:
//
//  1. Create the tenant record, and register its trust anchors when
//     cfg.TrustAnchorsPEM supplies them.
//  2. Seed the tenant-scoped built-in roles (so a tenant-scoped admin role
//     exists for the tenant).
//  3. Create the tenant-admin user bound to that tenant-scoped admin role.
//  4. Establish the admin credential per cfg.AdminEnrollment. Cert mode
//     (default) takes exactly one of the admin's CSR -- signed via Signer --
//     or the admin's own certificate -- verified against the trust anchors --
//     and binds its SPKI thumbprint to the admin; no key is ever generated
//     for the admin. Deferred mode creates the admin PENDING (disabled,
//     nothing issued) so the consumer can attach an invitation whose
//     redemption binds the first auth factor and activates the account.
//
// For a tenant that keeps its identity records behind its own barrier
// (tenant.IAMBehindTenantBarrier), steps 2 and 3 cannot run until that
// barrier exists. In cert mode the admin's certificate is issued or verified
// all the same and recorded as the tenant's pending admin
// (Dependencies.PendingAdmins); the barrier's trust-on-first-init window then
// admits only that certificate, and the account is created from the record
// when the barrier is initialized (CompletePendingTenantAdmin). The result
// reports AdminAwaitsBarrierInit.
//
// Every input that can be refused -- the anchors, the CSR, the bring-your-own
// certificate's chain and identity -- is judged before the tenant record is
// created. With cfg.Exclusive a failure after that undoes what this call
// created.
//
// It deliberately does NOT initialize a tenant barrier: the system admin holds
// no tenant key. The tenant admin self-bootstraps their barrier later via
// InitializeTenantBarrier.
//
// Uses: Tenants/Users/Roles (required), Logger (required); Signer for a CSR;
// TrustAnchors for anchors; Users with AddCertBinding, or Binder, for the
// binding; PendingAdmins for a tenant whose identity records are behind its
// barrier. Barriers and CA are unused.
func ProvisionTenant(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies) (*TenantProvisionResult, error) {
	if deps.Logger == nil {
		return nil, &MissingLoggerError{}
	}
	if cfg.TenantID == "" {
		return nil, &EmptyTenantIDError{}
	}
	// Every write below that goes through a routed IAM service (roles,
	// users, cert bindings) acts on THIS tenant's own initial records, not
	// on an existing tenant's data: tenant.EffectiveTenant has no
	// system-tenant lift (owner ruling, 2026-10-01), so the caller's own
	// identity tenant no longer carries this call across to a brand-new
	// tenant on its own. Provisioning is legitimately privileged here --
	// ProvisionTenant itself is reached only through the ScopeCross
	// tenants:create gate -- so the context is rebound to the tenant being
	// created for the rest of this call. This does not touch the
	// authenticated IDENTITY (a separate context key), so audit trails still
	// name the real caller as the actor.
	ctx = tenant.WithTenantID(ctx, cfg.TenantID)
	mode := cfg.AdminEnrollment
	if mode == "" {
		mode = AdminEnrollCert
	}
	switch mode {
	case AdminEnrollCert:
	case AdminEnrollDeferred:
		// System-level enrollment is cert-only, always (company doc 08):
		// a deferred, invitation-activated identity is not an acceptable
		// bootstrap anchor for the tenant that guards barrier material.
		if cfg.TenantID == tenantid.System {
			return nil, &SystemTenantDeferredEnrollmentError{}
		}
		// A credential alongside deferred mode is refused rather than
		// silently discarded: honoring it would contradict the requested
		// deferral, and dropping it would strand the admin's keypair.
		if len(cfg.AdminCSRPEM) > 0 || len(cfg.AdminCertPEM) > 0 {
			return nil, &ConflictingAdminEnrollmentError{}
		}
		// The canonical factor store is consulted before any invitation
		// target is handed back, so the seam is required up front — before
		// any write — rather than discovered missing mid-provisioning.
		if deps.AdminFactors == nil {
			return nil, &MissingAdminFactorsError{}
		}
	default:
		return nil, &InvalidAdminEnrollmentModeError{Mode: string(cfg.AdminEnrollment)}
	}
	if deps.Tenants == nil {
		return nil, &firstboot.MissingTenantsError{}
	}
	if deps.Users == nil {
		return nil, &firstboot.MissingUsersError{}
	}
	if deps.Roles == nil {
		return nil, &firstboot.MissingRolesError{}
	}

	displayName := cfg.DisplayName
	if displayName == "" {
		displayName = cfg.TenantID
	}
	isolation := cfg.Isolation
	if isolation == "" {
		isolation = tenant.IsolationBarrier
	}
	adminUsername := cfg.AdminUsername
	if adminUsername == "" {
		adminUsername = "admin"
	}
	behindBarrier := tenant.IAMBehindTenantBarrier(isolation)

	plan, err := planAdmin(cfg, deps, mode, adminUsername)
	if err != nil {
		return nil, err
	}
	if mode == AdminEnrollCert {
		if behindBarrier && deps.PendingAdmins == nil {
			return nil, &MissingProvisioningSeamError{Seam: "Dependencies.PendingAdmins for a tenant whose identity records are behind its own barrier"}
		}
		if _, ok := deps.Users.(certBindingAdder); !ok && deps.Binder == nil {
			return nil, &MissingProvisioningSeamError{Seam: "a certificate binder (Dependencies.Users with AddCertBinding, or Dependencies.Binder)"}
		}
	}
	var undo undoStack
	if cfg.Exclusive {
		if _, ok := deps.Tenants.(tenantDeleter); !ok {
			return nil, &MissingProvisioningSeamError{Seam: "Dependencies.Tenants with DeleteTenant, to undo an exclusive provisioning"}
		}
		if _, ok := deps.Users.(userDeleter); !ok {
			return nil, &MissingProvisioningSeamError{Seam: "Dependencies.Users with DeleteUser, to undo an exclusive provisioning"}
		}
	}
	fail := func(cause error) (*TenantProvisionResult, error) {
		if !cfg.Exclusive || len(undo.steps) == 0 {
			return nil, cause
		}
		return nil, &ProvisioningUndoneError{
			TenantID:     cfg.TenantID,
			Cause:        cause,
			UndoFailures: undo.run(ctx, deps.Logger, cfg.TenantID),
		}
	}

	// Step 1: tenant record. Already-exists is idempotent success, unless
	// this provisioning is exclusive.
	var createdInfo *tenant.TenantInfo
	if info, err := deps.Tenants.CreateTenant(ctx, cfg.TenantID, displayName, isolation, cfg.CreateOptions...); err != nil {
		if cfg.Exclusive || !errors.Is(err, tenant.ErrTenantAlreadyExists) {
			return nil, &TenantRecordCreationError{Cause: err}
		}
		deps.Logger.Debug("bootstrap: tenant already exists",
			slog.String("tenant", cfg.TenantID))
	} else {
		createdInfo = info
		deps.Logger.Info("bootstrap: tenant record created",
			slog.String("tenant", cfg.TenantID),
			slog.String("isolation", string(isolation)))
		undo.push("delete tenant", func(ctx context.Context) error {
			// planAdmin refused the request up front when Exclusive and
			// the seam was missing; this is the same check, not a new one.
			d, ok := deps.Tenants.(tenantDeleter)
			if !ok {
				return &MissingProvisioningSeamError{Seam: "Tenants.DeleteTenant"}
			}
			return d.DeleteTenant(ctx, cfg.TenantID)
		})
	}

	// Step 1b: trust anchors, before any credential that may depend on them.
	if len(plan.anchors) > 0 {
		added, err := deps.TrustAnchors.Add(ctx, cfg.TenantID, cfg.TrustAnchorsPEM)
		if err != nil {
			return fail(&TrustAnchorRegistrationError{Cause: err})
		}
		for _, a := range added {
			fp := a.Fingerprint
			undo.push("remove trust anchor "+fp, func(ctx context.Context) error {
				return deps.TrustAnchors.Remove(ctx, cfg.TenantID, fp)
			})
		}
	}

	if behindBarrier && mode == AdminEnrollCert {
		result, err := provisionPendingAdmin(ctx, cfg, deps, plan, adminUsername, &undo, fail)
		if err != nil {
			return result, err
		}
		// A system-managed (auto-unseal) barrier is already initialized and
		// serving by the moment Step 1's CreateTenant above returned -- unlike
		// a self-managed barrier, whose own admin later calls
		// barrier-init/barrier-unseal to raise tenant.BarrierReadyHook, NOTHING
		// else will ever fire that hook for an auto-unseal tenant, so the
		// pending admin recorded above would never complete. Finish it now,
		// gated on createdInfo so a re-run against an already-existing tenant
		// (createdInfo nil) and a self-managed barrier (BarrierAutoUnseal
		// false) are both left exactly as before: deferred to the admin's own
		// barrier-init/unseal/status call. completeErr ==
		// tenant.ErrTenantSealed is not expected here (the barrier this
		// gate admits is never sealed) but is tolerated as the same
		// non-fatal deferred case the hook itself allows.
		if createdInfo != nil && createdInfo.BarrierAutoUnseal {
			switch done, completeErr := CompletePendingTenantAdmin(ctx, cfg.TenantID, deps); {
			case completeErr == nil && done:
				result.AdminAwaitsBarrierInit = false
				deps.Logger.Info("bootstrap: tenant-admin account completed immediately (barrier already ready)",
					slog.String("tenant", cfg.TenantID), slog.String("admin", adminUsername))
			case completeErr == nil:
			case errors.Is(completeErr, tenant.ErrTenantSealed):
			default:
				deps.Logger.Error("tenant admin completion failed; retried on the next unseal or status read",
					slog.String("tenant", cfg.TenantID), slog.Any("error", completeErr))
			}
		}
		return result, nil
	}

	// Step 2: tenant-scoped built-in roles (tolerates already-exists).
	if _, err := seedTenantRoles(ctx, deps, cfg.TenantID); err != nil {
		return fail(err)
	}

	// Step 3: tenant-admin user bound to the tenant-scoped admin role.
	// System roles are undeletable and name-immutable (no-compat-cleanup
	// deleted the UUIDv5->UUIDv8 dual-read compatibility), so the role id is
	// always its canonical derivation -- no resolver, no store lookup, no
	// legacy fallback.
	adminRoleID := role.SystemRoleID(cfg.TenantID, role.RoleNameAdmin)
	adminUser, err := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
		// The bootstrap admin authenticates by certificate, not by an email login
		// key, so the omission is declared rather than defaulted (#190).
		WithoutEmailLoginKey: true,
		Username:             adminUsername,
		TenantID:             cfg.TenantID,
		RoleIDs:              []string{adminRoleID},
		// System is deliberately left false (zero value) here (go-qrdb#106).
		// This is a PER-TENANT admin, one of potentially many created by
		// ProvisionTenant, and must stay deletable so a tenant can be fully
		// deprovisioned. Immutability is reserved for the single system-tenant
		// admin that pkg/firstboot.Initialize mints.
		// Deferred enrollment (#193) landed on main during this branch. Both
		// sides of the conflict are load-bearing and neither supersedes the
		// other: the admin has no email login key AND, under deferred mode,
		// must be created disabled until it enrolls.
		Pending: mode == AdminEnrollDeferred,
	})
	if err != nil {
		var exists *user.UsernameExistsError
		if cfg.Exclusive || !errors.As(err, &exists) {
			return fail(&TenantAdminUserCreationError{Cause: err})
		}
		deps.Logger.Debug("bootstrap: tenant-admin user already exists",
			slog.String("tenant", cfg.TenantID),
			slog.String("username", adminUsername))
	} else {
		deps.Logger.Info("bootstrap: tenant-admin user created",
			slog.String("tenant", cfg.TenantID),
			slog.String("username", adminUsername),
			slog.String("role_id", adminRoleID))
		createdID := adminUser.ID
		undo.push("delete tenant admin", func(ctx context.Context) error {
			d, ok := deps.Users.(userDeleter)
			if !ok {
				return &MissingProvisioningSeamError{Seam: "Users.DeleteUser"}
			}
			return d.DeleteUser(ctx, createdID)
		})
	}

	// Deferred mode must hand the consumer the admin's user id even on an
	// idempotent re-run (the invitation attaches to that id), so a
	// preexisting admin is resolved by lookup rather than left unreported.
	// EVERY record deferred mode returns — freshly created included, since
	// Users is an adapter seam that may have ignored Pending — passes the
	// same fail-closed truly-pending validation, and the canonical factor
	// store must agree the admin holds no active credential.
	if mode == AdminEnrollDeferred {
		if adminUser == nil {
			resolved, resolveErr := resolveExistingTenantAdmin(ctx, deps, cfg.TenantID, adminUsername, adminRoleID)
			if resolveErr != nil {
				return fail(resolveErr)
			}
			adminUser = resolved
		} else if validateErr := validatePendingAdmin(adminUser, cfg.TenantID, adminUsername, adminRoleID); validateErr != nil {
			return fail(validateErr)
		}
		if factorErr := ensureNoActiveFactors(ctx, deps, cfg.TenantID, adminUsername, adminUser.ID); factorErr != nil {
			return fail(factorErr)
		}
	}

	result := &TenantProvisionResult{
		TenantID:      cfg.TenantID,
		AdminUsername: adminUsername,
		AdminRoleID:   adminRoleID,
		AdminPending:  mode == AdminEnrollDeferred,
	}
	if adminUser != nil {
		result.AdminUserID = adminUser.ID
	}

	// Step 4: tenant-admin certificate. Only in cert mode, and only when the
	// admin was newly created in this run (so a re-run does not rotate the
	// tenant admin's credential). Deferred mode issues nothing: the pending
	// admin's first credential is bound by the consumer's
	// invitation-redemption flow.
	if adminUser != nil && mode == AdminEnrollCert {
		c, err := plan.issue(ctx, deps, cfg.TenantID, adminUsername)
		if err != nil {
			return fail(err)
		}
		pushRevoke(&undo, deps, c)
		if err := bindAdmin(ctx, deps, cfg.TenantID, adminUser, c); err != nil {
			return fail(err)
		}
		result.AdminCertPEM = c.certPEM
		result.AdminCertSerial = c.serial
		result.AdminCertThumbprint = c.thumbprint
		deps.Logger.Info("bootstrap: tenant-admin cert bound",
			slog.String("tenant", cfg.TenantID),
			slog.Uint64("user_id", adminUser.ID),
			slog.Bool("csr_signed", len(plan.csrPEM) > 0))
	}

	deps.Logger.Info("bootstrap: tenant provisioned",
		slog.String("tenant", cfg.TenantID),
		slog.String("admin", adminUsername))

	return result, nil
}

// provisionPendingAdmin finishes cert-mode provisioning of a tenant whose
// identity records are behind its own barrier: the admin's certificate is
// issued (or the bring-your-own one taken) now and recorded as the tenant's
// pending admin; the account comes when the barrier is initialized.
func provisionPendingAdmin(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies, plan *adminPlan, adminUsername string, undo *undoStack, fail func(error) (*TenantProvisionResult, error)) (*TenantProvisionResult, error) {
	// Validated HERE, before anything is signed: this path's username goes
	// straight into a certificate's Subject CN and a PendingAdmins record,
	// with no later user-store validation to catch a malformed one -- the
	// non-pending path's CreateUser runs in the same call, this one does
	// not run until the tenant's barrier is initialized, possibly long
	// after an unforgeable certificate for the bad name already exists.
	if !validAdminUsername(adminUsername) {
		return fail(&InvalidAdminUsernameError{Username: adminUsername})
	}
	existing, err := deps.PendingAdmins.Get(ctx, cfg.TenantID)
	if err != nil {
		return fail(&PendingTenantAdminError{Op: "read", Cause: err})
	}
	if existing != nil && !cfg.Exclusive {
		// A re-run of a provisioning whose admin is already pending: keep the
		// recorded credential rather than rotating it, and pin it, which
		// completes a run that recorded the admin and stopped before its pin.
		if err := pinFirstAdmin(ctx, deps, cfg.TenantID, existing.Thumbprint); err != nil {
			return fail(err)
		}
		return &TenantProvisionResult{
			TenantID:               cfg.TenantID,
			AdminUsername:          existing.Username,
			AdminCertThumbprint:    existing.Thumbprint,
			AdminCertSerial:        existing.Serial,
			AdminAwaitsBarrierInit: true,
		}, nil
	}
	c, err := plan.issue(ctx, deps, cfg.TenantID, adminUsername)
	if err != nil {
		return fail(err)
	}
	pushRevoke(undo, deps, c)
	if err := deps.PendingAdmins.Create(ctx, cfg.TenantID, pendingFrom(adminUsername, c)); err != nil {
		return fail(&PendingTenantAdminError{Op: "record", Cause: err})
	}
	undo.push("delete pending admin", func(ctx context.Context) error {
		return deps.PendingAdmins.Delete(ctx, cfg.TenantID)
	})
	// The pin has no undo: it is written once, and an undone provisioning
	// deletes the tenant, whose id is then retired and never reused.
	if err := pinFirstAdmin(ctx, deps, cfg.TenantID, c.thumbprint); err != nil {
		return fail(err)
	}
	deps.Logger.Info("bootstrap: tenant provisioned; admin awaits the tenant barrier",
		slog.String("tenant", cfg.TenantID),
		slog.String("admin", adminUsername))
	return &TenantProvisionResult{
		TenantID:               cfg.TenantID,
		AdminUsername:          adminUsername,
		AdminCertPEM:           c.certPEM,
		AdminCertSerial:        c.serial,
		AdminCertThumbprint:    c.thumbprint,
		AdminAwaitsBarrierInit: true,
	}, nil
}

// pinFirstAdmin pins thumbprint, the pending first admin's, as the epoch-1
// anchor of tenantID's barrier through deps.BarrierAnchors; nil pins nothing.
func pinFirstAdmin(ctx context.Context, deps Dependencies, tenantID, thumbprint string) error {
	if deps.BarrierAnchors == nil {
		return nil
	}
	if err := deps.BarrierAnchors.PinFirstAdmin(ctx, clusterseal.TenantBarrier(tenantID), thumbprint); err != nil {
		return &PendingTenantAdminError{Op: "pin barrier anchor", Cause: err}
	}
	return nil
}

// maxAdminUsernameLen bounds an admin username signed into a certificate's
// Subject CN on the pending-admin path.
const maxAdminUsernameLen = 64

// validAdminUsername reports whether name is safe to sign into a
// certificate's Subject CN: 1..64 characters from [A-Za-z0-9._@-].
func validAdminUsername(name string) bool {
	if name == "" || len(name) > maxAdminUsernameLen {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '@', r == '-':
		default:
			return false
		}
	}
	return true
}

// pushRevoke records the revocation of a certificate this provisioning
// signed, when the Signer can revoke it.
func pushRevoke(undo *undoStack, deps Dependencies, c *adminCredential) {
	if c.serial == "" {
		return
	}
	revoker, ok := deps.Signer.(certRevoker)
	if !ok {
		return
	}
	serial := c.serial
	undo.push("revoke admin certificate "+serial, func(ctx context.Context) error {
		_, err := revoker.RevokeCert(ctx, serial)
		return err
	})
}

// ---------------------------------------------------------------------------
// InitializeTenantBarrier
// ---------------------------------------------------------------------------

// InitializeTenantBarrier is the tenant-admin self-service call: it registers
// (if not already registered) and initializes THAT tenant's own barrier with
// the chosen strategy and credentials, scoped to the tenant.
//
// The barrier is registered with explicit strategies (NOT
// RegisterTenantDefault) so the tenant chooses its own strategy independent of
// the system barrier. For passphrase, creds.Secret must be non-empty. A Shamir
// tenant barrier cannot be initialized here: it is initialized over the
// tenant's enrolled share holders (tenant.TenantService's holder path), and a
// pre-registered Shamir barrier is refused with
// seal.ErrShamirInitializeRequiresHolders.
//
// Authorization — who may call this (the tenant-admin role for THIS tenant) —
// is enforced at the API / transport layer, NOT here. This entry point assumes
// the caller is already authorized.
//
// Uses: Barriers (required), Logger (required).
func InitializeTenantBarrier(ctx context.Context, tenantID string, strategy seal.StrategyID, creds seal.Credentials, deps Dependencies) error {
	if deps.Logger == nil {
		return &MissingLoggerError{}
	}
	if deps.Barriers == nil {
		return &MissingBarriersError{}
	}
	if tenantID == "" {
		return &EmptyTenantIDError{}
	}

	if strategy == "" {
		strategy = StrategyNone
	}
	// Passphrase strategy requires a secret. There is no derived default. A
	// destroyed credential is one the caller has already released, and it is
	// refused the same way — nothing below could open it.
	if seal.IsPassphraseStrategy(strategy) && (creds.Secret == nil || !creds.Secret.IsAlive()) {
		return &MissingPassphraseError{}
	}
	if err := validateStrategy(strategy); err != nil {
		return err
	}

	// Register the tenant barrier if it isn't already, with the tenant's
	// chosen strategy. An already-registered tenant is reused.
	if _, err := deps.Barriers.GetTenant(tenantID); err != nil {
		strategyImpl, buildErr := buildTenantStrategy(strategy)
		if buildErr != nil {
			return buildErr
		}
		config := seal.BarrierConfig{
			RootKeyPath: "tenant/" + tenantID + "/root-key",
		}
		// base is intentionally nil, NOT seal.NewMemoryBackend(): a memory
		// backend loses the tenant's wrapped-root-key/epoch bookkeeping on
		// restart, making the tenant's barrier-encrypted IAM unrecoverable.
		// Passing nil defers to BarrierRegistry's configured durable
		// volume-pool / per-tenant Pebble allocation — the same mechanism
		// RegisterTenantPooledWithStrategy uses — so embedders get
		// persistence by default. If the registry was not constructed with
		// a volume allocator (see seal.WithVolumeAllocator / WithPerTenantRegistry),
		// RegisterTenant fails closed with ErrPoolNotConfigured /
		// ErrPerTenantRegistryNotConfigured rather than silently degrading
		// to a non-durable backend.
		if _, regErr := deps.Barriers.RegisterTenant(ctx, tenantID, nil, config, strategyImpl); regErr != nil {
			return &TenantBarrierRegistrationError{Cause: regErr}
		}
		deps.Logger.Info("bootstrap: tenant barrier registered",
			slog.String("tenant", tenantID),
			slog.String("strategy", string(strategy)))
	}

	if err := deps.Barriers.InitializeTenant(ctx, tenantID, creds); err != nil {
		return &TenantBarrierInitError{Cause: err}
	}

	deps.Logger.Info("bootstrap: tenant barrier initialized",
		slog.String("tenant", tenantID),
		slog.String("strategy", string(strategy)))

	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// credentialsForStrategy validates the passphrase requirement and returns the
// seal credentials for the chosen system strategy. The passphrase strategy
// requires a non-empty passphrase with no derived default; none and shamir
// carry no passphrase.
func credentialsForStrategy(ctx context.Context, strategy seal.StrategyID, passphrase *secure.Buffer) (seal.Credentials, error) {
	if err := validateStrategy(strategy); err != nil {
		return seal.Credentials{}, err
	}
	if !seal.IsPassphraseStrategy(strategy) {
		return seal.Credentials{}, nil
	}
	// The copy-out is held INSIDE the borrow, and the copy lives only until
	// seal.NewCredentials has moved it into a sealed enclave — that call
	// WIPES its source, and it runs OUTSIDE the borrow because it allocates
	// locked memory of its own (pkg/secure rule 6: no allocation under a
	// borrow). So the material crosses pageable memory for the width of one
	// statement, under the passphrase's own borrow going in and wiped coming
	// out, rather than living there as a []byte for the rest of the bootstrap
	// as it did when go-quicraft's Credentials.Secret was a plain slice.
	var plain []byte
	if err := passphrase.WithBytes(func(b []byte) error {
		plain = append([]byte(nil), b...)
		return nil
	}); err != nil {
		// WithBytes fails for exactly three shapes — nil, destroyed, and the
		// zero value — and all three mean the same thing at this call site:
		// the passphrase strategy was selected and no passphrase came with
		// it. That is MissingPassphraseError, the error this branch returned
		// when the field was a string, and it stays the error callers match
		// on.
		//
		// THE EMBEDDED TRANSPORT DELIBERATELY DOES NOT DO THIS, and the
		// divergence is a decision rather than an inconsistency to tidy
		// (go-qrdb#429). There, collapsing nil into destroyed selects
		// seal.StrategyNone and a fresh data dir comes up with a barrier
		// sealed by nothing — an unusable credential silently WEAKENS the
		// posture, so a non-nil-but-unreadable buffer is a hard configuration
		// error. Here, every shape FAILS CLOSED: this function's only outputs
		// are a credential or an error, nothing downstream substitutes a
		// weaker strategy on the strength of the error, and the caller
		// already matches on MissingPassphraseError. Do not "align" the two
		// without re-reading which one can lose a barrier.
		return seal.Credentials{}, &MissingPassphraseError{}
	}
	return seal.NewCredentials(ctx, plain)
}

// validateStrategy rejects strategies the bootstrap orchestrator does not
// select directly. Only none, passphrase (software), and shamir are accepted.
func validateStrategy(strategy seal.StrategyID) error {
	switch strategy {
	case StrategyNone, StrategyPassphrase, StrategyShamir:
		return nil
	default:
		return &UnsupportedStrategyError{Strategy: string(strategy)}
	}
}

// buildTenantStrategy constructs the sealing strategy implementation for a
// tenant barrier: none and passphrase (deferred software). Shamir is never
// built here — a Shamir tenant barrier is initialized over its enrolled
// share holders, not from Credentials.
func buildTenantStrategy(strategy seal.StrategyID) (seal.SealingStrategy, error) {
	switch strategy {
	case StrategyNone:
		return seal.NewNoneStrategy(), nil
	case StrategyPassphrase:
		// Deferred software strategy binds the passphrase at Initialize time
		// from Credentials.Secret.
		return seal.NewDeferredSoftwareStrategy(), nil
	case StrategyShamir:
		// A Shamir barrier needs its holders and a threshold, neither of
		// which the Credentials-only signature carries.
		return nil, &MissingShamirConfigError{}
	default:
		return nil, &UnsupportedStrategyError{Strategy: string(strategy)}
	}
}

// AdminFactorChecker is the canonical auth-factor query deferred tenant
// provisioning consults before handing back an invitation target. The
// canonical factor records live OUTSIDE the user record — qrdb's user
// auth-factor service persists them under __sys:auth/... — so
// user.User.Factors alone cannot prove an admin is credential-less.
//
// The contract is FAIL-CLOSED (service.ActiveFactorChecker): an
// implementation MUST error whenever the answer cannot be computed from a
// complete read of the user's factor records — never report false over a
// partial factor set. The lenient List (which skips unreadable entries)
// deliberately cannot satisfy this seam; qrdb's production user
// auth-factor service implements it directly.
type AdminFactorChecker interface {
	// HasActiveFactor reports whether the user owns at least one active
	// (non-revoked) auth factor.
	HasActiveFactor(ctx context.Context, userID uint64) (bool, error)
}

// AdminUserLookup is the optional read seam ProvisionTenant uses to resolve
// a preexisting tenant-admin user in deferred mode. qrdb's user.UserServicer
// satisfies it. An adapter that implements no lookup makes a deferred re-run
// fail with PendingAdminUnresolvableError instead of silently reporting a
// zero user id the consumer cannot attach an invitation to.
type AdminUserLookup interface {
	// GetUserByUsername retrieves a user by tenant-scoped username.
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
}

// validatePendingAdmin guards every user record deferred mode would hand
// back as an invitation target. It fails closed unless the record is TRULY
// the pending admin an invitation may attach to: a persisted id, the exact
// tenant and username requested, the tenant-scoped Admin role, a lifecycle
// of exactly pending, and no bound certificate. Enrolled auth factors are
// checked separately by ensureNoActiveFactors against the canonical
// auth-factor store — the record itself carries no factor field to check
// here. Anything else — an unrelated squatter under the admin username,
// an admin already activated, suspended, locked, disabled or being deleted,
// a record with an absent or unrecognized lifecycle, or an adapter that
// ignored CreateUserRequest.Pending — is refused rather than converted into
// an invitation target.
func validatePendingAdmin(u *user.User, tenantID, username, adminRoleID string) error {
	var reason string
	switch {
	case u == nil || u.ID == 0:
		reason = "record carries no persisted user id"
	case u.TenantID != tenantID:
		reason = "record belongs to a different tenant"
	case u.Username != username:
		reason = "record names a different user"
	case !slices.Contains(u.RoleIDs, adminRoleID):
		reason = "record does not hold the tenant-scoped admin role"
	case u.Lifecycle != user.LifecyclePending:
		reason = "admin lifecycle is " + strconv.Quote(string(u.Lifecycle)) + ", not pending"
	case len(u.CertBindings) > 0:
		reason = "admin already holds a bound certificate"
	default:
		return nil
	}
	return &PendingAdminUnresolvableError{
		TenantID: tenantID,
		Username: username,
		Reason:   reason,
	}
}

// ensureNoActiveFactors consults the canonical auth-factor store: a record
// that looks pending can still own active factors persisted outside the
// user record (an enrolled-then-disabled admin, or a redemption that bound
// a factor and failed before activation). An active factor fails closed,
// and so does any store error — the seam's fail-closed contract means an
// error is "the answer is unknown", which is never treated as
// credential-less. Revoked factors do not block, so re-inviting an admin
// whose credentials were revoked remains possible.
func ensureNoActiveFactors(ctx context.Context, deps Dependencies, tenantID, username string, userID uint64) error {
	hasActive, err := deps.AdminFactors.HasActiveFactor(ctx, userID)
	if err != nil {
		return &TenantAdminUserCreationError{Cause: err}
	}
	if hasActive {
		return &PendingAdminUnresolvableError{
			TenantID: tenantID,
			Username: username,
			Reason:   "admin already holds an active auth factor in the canonical factor store",
		}
	}
	return nil
}

// resolveExistingTenantAdmin recovers the tenant-admin user a deferred
// re-run found already present, applying validatePendingAdmin's fail-closed
// bar to the record the lookup returns.
func resolveExistingTenantAdmin(ctx context.Context, deps Dependencies, tenantID, username, adminRoleID string) (*user.User, error) {
	lookup, ok := deps.Users.(AdminUserLookup)
	if !ok {
		return nil, &PendingAdminUnresolvableError{
			TenantID: tenantID,
			Username: username,
			Reason:   "user already exists and the Users seam supports no lookup",
		}
	}
	existing, err := lookup.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		return nil, &TenantAdminUserCreationError{Cause: err}
	}
	if err := validatePendingAdmin(existing, tenantID, username, adminRoleID); err != nil {
		return nil, err
	}
	return existing, nil
}

// seedTenantRoles installs the tenant-scoped built-in roles. Already-exists
// for any individual role is idempotent success — verified, not assumed, on
// EITHER collision CreateRole can report, by the SAME bounded-retry check
// pkg/firstboot's registerBuiltInRoles uses for the identical reason
// (go-qrdb's firstboot role race, 61d7cb049): two concurrent ProvisionTenant
// calls for the SAME new tenant race each other's seedTenantRoles under the
// SAME deterministic role.SystemRoleID, and the losing side can be told
// "already exists" before the winner's write has replicated to its own
// read. Reusing firstboot.BuiltInRoleAlreadySeededWithin rather than
// re-deriving the verification a third time (pkg/role's own
// ReconcileBuiltInRoles/seedMissingRole is the second).
//
// A record that DOES resolve but disagrees with the canonical definition —
// a genuine conflict, not a racing idempotent re-attempt — still fails:
// BuiltInRoleAlreadySeededWithin only returns true on an exact match (id,
// tenant, name, System flag, and permission set).
func seedTenantRoles(ctx context.Context, deps Dependencies, tenantID string) (preexisting bool, err error) {
	resolver := tenantRoleResolver(deps)
	for _, r := range role.BuiltInRoles(tenantID) {
		_, createErr := deps.Roles.CreateRole(ctx, &role.CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		if createErr == nil {
			continue
		}
		var nameExists *role.RoleNameExistsError
		var idExists *role.RoleIDExistsError
		if errors.As(createErr, &nameExists) || errors.As(createErr, &idExists) {
			if firstboot.BuiltInRoleAlreadySeededWithin(ctx, resolver, tenantID, r) {
				preexisting = true
				continue
			}
		}
		return false, &TenantAdminRoleCreationError{Cause: createErr}
	}
	return preexisting, nil
}

// tenantRoleResolver returns the seam seedTenantRoles verifies a create
// collision through, preferring an explicit Dependencies.RoleResolver and
// falling back to Roles itself when it happens to implement
// role.NameResolver — mirroring pkg/firstboot's own roleResolver fallback
// for the identical reason: a caller that wires only Roles still gets
// verified idempotency instead of a silently unverifiable collision.
func tenantRoleResolver(deps Dependencies) role.NameResolver {
	if deps.RoleResolver != nil {
		return deps.RoleResolver
	}
	if r, ok := deps.Roles.(role.NameResolver); ok {
		return r
	}
	return nil
}
