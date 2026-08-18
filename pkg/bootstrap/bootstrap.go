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
	"encoding/pem"
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

	// RoleResolver resolves a system role to the identifier it is persisted
	// under (firstboot.Dependencies.RoleResolver). Optional; see that field
	// for why a store seeded by an earlier release needs it.
	RoleResolver role.NameResolver

	// CA issues certificates via the generate-and-distribute path (Path A).
	// Optional: used when no CSR PEM is supplied.
	CA firstboot.CertProvider

	// Signer signs an admin-supplied CSR (Path B). Used when a CSR PEM is
	// supplied. Optional.
	Signer firstboot.CertSigner

	// Binder pins an issued certificate's SPKI thumbprint to its user record.
	// Optional: when nil, certs are issued but not bound (caller binds).
	Binder firstboot.CertBinder

	// Barriers is the seal barrier registry. Required by BootstrapSystem
	// (system barrier) and InitializeTenantBarrier (tenant barriers). Unused
	// by ProvisionTenant.
	Barriers *seal.BarrierRegistry

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
	Passphrase string

	// SeparationOfDuties selects the first-boot RBAC posture. See
	// firstboot.Dependencies.SeparationOfDuties.
	SeparationOfDuties bool

	// AdminCSRPEM, when non-empty, selects the admin-mTLS CSR-sign path
	// (Path B) for the system admin: the admin's CSR is signed via the
	// Signer seam. When empty, Path A (generate-and-distribute via CA) is used.
	AdminCSRPEM []byte
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
		creds, credErr := credentialsForStrategy(strategy, cfg.Passphrase)
		if credErr != nil {
			return nil, credErr
		}
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
		CA:                 deps.CA,
		Signer:             deps.Signer,
		AdminCSRPEM:        cfg.AdminCSRPEM,
		Binder:             deps.Binder,
		Logger:             deps.Logger,
		SeparationOfDuties: cfg.SeparationOfDuties,
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

	// AdminCSRPEM, when non-empty, selects the CSR-sign path (Path B) for the
	// tenant admin: the admin's CSR is signed via the Signer seam. When empty,
	// Path A (generate-and-distribute via CA) is used. When neither path is
	// configured, no cert is enrolled.
	AdminCSRPEM []byte
}

// TenantProvisionResult reports what ProvisionTenant created.
type TenantProvisionResult struct {
	// TenantID is the provisioned tenant id.
	TenantID string

	// AdminUserID is the created tenant-admin user id.
	AdminUserID uint64

	// AdminUsername is the created tenant-admin username.
	AdminUsername string

	// AdminCertPEM holds the issued tenant-admin certificate (PEM). Nil when
	// no cert path was configured.
	AdminCertPEM []byte

	// AdminKeyPEM holds the tenant-admin private key (PEM). Nil for the
	// CSR-sign path (admin keeps their key) and when no cert was issued.
	AdminKeyPEM []byte

	// AdminCertThumbprint is the SPKI SHA-256 thumbprint of the issued
	// tenant-admin certificate, bound to the admin user. Empty when no cert
	// was issued.
	AdminCertThumbprint string
}

// ProvisionTenant performs MINIMAL control-plane provisioning of a new tenant:
//
//  1. Create the tenant record.
//  2. Seed the tenant-scoped built-in roles (so a tenant-scoped admin role
//     exists for the tenant).
//  3. Create the tenant-admin user bound to that tenant-scoped admin role.
//  4. Enroll the tenant admin's cert via the SAME admin-mTLS seams used by
//     first-boot (Path A generate or Path B CSR-sign) and bind the SPKI
//     thumbprint.
//
// It deliberately does NOT initialize a tenant barrier: the system admin holds
// no tenant key. The tenant admin self-bootstraps their barrier later via
// InitializeTenantBarrier.
//
// Uses: Tenants/Users/Roles (required), Logger (required), CA/Signer/Binder
// (optional, tenant-admin cert enrollment). Barriers is unused.
func ProvisionTenant(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies) (*TenantProvisionResult, error) {
	if deps.Logger == nil {
		return nil, &MissingLoggerError{}
	}
	if cfg.TenantID == "" {
		return nil, &EmptyTenantIDError{}
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

	// Step 1: tenant record. Already-exists is idempotent success.
	if _, err := deps.Tenants.CreateTenant(ctx, cfg.TenantID, displayName, isolation); err != nil {
		if !errors.Is(err, tenant.ErrTenantAlreadyExists) {
			return nil, &TenantRecordCreationError{Cause: err}
		}
		deps.Logger.Debug("bootstrap: tenant already exists",
			slog.String("tenant", cfg.TenantID))
	} else {
		deps.Logger.Info("bootstrap: tenant record created",
			slog.String("tenant", cfg.TenantID),
			slog.String("isolation", string(isolation)))
	}

	// Step 2: tenant-scoped built-in roles (tolerates already-exists).
	if err := seedTenantRoles(ctx, deps, cfg.TenantID); err != nil {
		return nil, err
	}

	// Step 3: tenant-admin user bound to the tenant-scoped admin role.
	// Resolve the grant through the store rather than re-deriving it: step 2
	// tolerates an already-seeded tenant, whose Admin role may still carry
	// the legacy UUIDv5 identifier a pre-FIPS release assigned it. Granting
	// the freshly derived identifier there would bind the admin to a role
	// that does not exist.
	adminRoleID, err := role.ResolveSystemRoleID(ctx, deps.RoleResolver, cfg.TenantID, role.RoleNameAdmin)
	if err != nil {
		return nil, &TenantAdminUserCreationError{Cause: err}
	}
	adminUser, err := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
		Username: adminUsername,
		TenantID: cfg.TenantID,
		RoleIDs:  []string{adminRoleID},
	})
	if err != nil {
		var exists *user.UsernameExistsError
		if !errors.As(err, &exists) {
			return nil, &TenantAdminUserCreationError{Cause: err}
		}
		deps.Logger.Debug("bootstrap: tenant-admin user already exists",
			slog.String("tenant", cfg.TenantID),
			slog.String("username", adminUsername))
	} else {
		deps.Logger.Info("bootstrap: tenant-admin user created",
			slog.String("tenant", cfg.TenantID),
			slog.String("username", adminUsername),
			slog.String("role_id", adminRoleID))
	}

	result := &TenantProvisionResult{
		TenantID:      cfg.TenantID,
		AdminUsername: adminUsername,
	}
	if adminUser != nil {
		result.AdminUserID = adminUser.ID
	}

	// Step 4: tenant-admin cert enrollment via the firstboot seams. Only when
	// the admin was newly created in this run (so a re-run does not rotate the
	// tenant admin's key material).
	if adminUser != nil {
		if certErr := enrollAdminCert(ctx, deps, cfg.TenantID, adminUsername, cfg.AdminCSRPEM, adminUser, result); certErr != nil {
			return nil, certErr
		}
	}

	deps.Logger.Info("bootstrap: tenant provisioned",
		slog.String("tenant", cfg.TenantID),
		slog.String("admin", adminUsername))

	return result, nil
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
// the system barrier. For passphrase, creds.Secret must be non-empty. For
// shamir, creds.Secret carries the M-of-N configuration through the shamir
// strategy already registered on the barrier.
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
	// Passphrase strategy requires a non-empty secret. There is no derived
	// default.
	if seal.IsPassphraseStrategy(strategy) && len(creds.Secret) == 0 {
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
		// a volume pool (see seal.WithVolumePool / WithPerTenantRegistry),
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
func credentialsForStrategy(strategy seal.StrategyID, passphrase string) (seal.Credentials, error) {
	if err := validateStrategy(strategy); err != nil {
		return seal.Credentials{}, err
	}
	if seal.IsPassphraseStrategy(strategy) {
		if passphrase == "" {
			return seal.Credentials{}, &MissingPassphraseError{}
		}
		return seal.Credentials{Secret: []byte(passphrase)}, nil
	}
	return seal.Credentials{}, nil
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
// tenant barrier. Shamir requires a configured threshold/total; since the
// tenant self-service signature carries only Credentials, shamir tenant
// barriers must be registered by the caller (with an explicit ShamirStrategy)
// before InitializeTenantBarrier is invoked. Here we support none and
// passphrase (deferred software) directly.
func buildTenantStrategy(strategy seal.StrategyID) (seal.SealingStrategy, error) {
	switch strategy {
	case StrategyNone:
		return seal.NewNoneStrategy(), nil
	case StrategyPassphrase:
		// Deferred software strategy binds the passphrase at Initialize time
		// from Credentials.Secret.
		return seal.NewDeferredSoftwareStrategy(), nil
	case StrategyShamir:
		// Shamir needs a threshold/total + a share-persistence backend, which
		// are not expressible through the self-service Credentials-only
		// signature. The caller pre-registers the tenant barrier with an
		// explicit ShamirStrategy; reaching here means it was not registered.
		return nil, &MissingShamirConfigError{}
	default:
		return nil, &UnsupportedStrategyError{Strategy: string(strategy)}
	}
}

// seedTenantRoles installs the tenant-scoped built-in roles. Already-exists
// for any individual role is idempotent success.
func seedTenantRoles(ctx context.Context, deps Dependencies, tenantID string) error {
	for _, r := range role.BuiltInRoles(tenantID) {
		_, err := deps.Roles.CreateRole(ctx, &role.CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		if err == nil {
			continue
		}
		var nameExists *role.RoleNameExistsError
		if errors.As(err, &nameExists) {
			continue
		}
		return &TenantAdminRoleCreationError{Cause: err}
	}
	return nil
}

// enrollAdminCert issues, thumbprints, and binds a tenant-admin certificate
// via the firstboot admin-mTLS seams. It selects Path B (CSR-sign) when a CSR
// PEM is supplied and a Signer is configured, else Path A (generate) via the
// CA. When neither path is configured, it is a no-op.
func enrollAdminCert(ctx context.Context, deps Dependencies, tenantID, subject string, csrPEM []byte, adminUser *user.User, result *TenantProvisionResult) error {
	var (
		certPEM []byte
		keyPEM  []byte
	)
	switch {
	case len(csrPEM) > 0 && deps.Signer != nil:
		signed, err := deps.Signer.SignAdminCSR(ctx, tenantID, subject, csrPEM)
		if err != nil {
			return &TenantAdminCertError{Cause: err}
		}
		certPEM = signed
	case deps.CA != nil:
		c, k, err := deps.CA.GenerateAdminCert(ctx, tenantID, subject)
		if err != nil {
			return &TenantAdminCertError{Cause: err}
		}
		certPEM, keyPEM = c, k
	default:
		return nil
	}

	if len(certPEM) == 0 {
		return nil
	}

	thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
	if err != nil {
		return &TenantAdminCertError{Cause: err}
	}
	result.AdminCertPEM = certPEM
	result.AdminKeyPEM = keyPEM
	result.AdminCertThumbprint = thumbprint

	if deps.Binder != nil && adminUser != nil {
		if bindErr := deps.Binder.BindAdminCert(ctx, tenantID, adminUser.ID, thumbprint); bindErr != nil {
			return &TenantAdminCertError{Cause: bindErr}
		}
		deps.Logger.Info("bootstrap: tenant-admin cert bound",
			slog.String("tenant", tenantID),
			slog.Uint64("user_id", adminUser.ID))
	}
	deps.Logger.Info("bootstrap: tenant-admin cert issued",
		slog.String("tenant", tenantID),
		slog.String("subject", subject),
		slog.Bool("csr_signed", len(csrPEM) > 0))

	return nil
}

// spkiThumbprintFromCertPEM parses a PEM certificate and returns its canonical
// SPKI thumbprint via user.ComputeSPKIThumbprint.
func spkiThumbprintFromCertPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("bootstrap: cert PEM has no CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return user.ComputeSPKIThumbprint(cert), nil
}
