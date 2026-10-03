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

package ceremony

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// HolderDeps are the dependencies every holder scope shares: the ceremony CA
// that issues holder certificates, and the registry of authentication factors
// holders may enrol (hardware factors are go-xkms implementations injected
// through WithFactorRegistry). The system ceremony hands the same deps to
// every tenant ceremony.
type HolderDeps struct {
	// CA issues holder certificates.
	CA CeremonyCA

	// Factors resolves factor names. "mtls" is always available: when the
	// registry does not carry it, the built-in pkg/authfactors/mtls factor
	// is used.
	Factors *authfactors.FactorRegistry

	// QuorumLimiter throttles quorum-challenge requests, which the
	// sealed window admits anonymously. Nil applies no throttling; the
	// node's composer always installs one (lifecycle wires the shared
	// pkg/ratelimit engine).
	QuorumLimiter QuorumLimitFunc

	// Audit is the node's compliance audit trail. Every holder scope records
	// its holder events on it -- registration, claim, quorum submission,
	// rekey start and revocation -- keyed by the scope's owning tenant
	// (holder_audit.go). The composer opens it before the sealed window, so
	// the claims and quorums that window serves are recorded too. Nil
	// records nothing.
	Audit audit.AuditLogger

	// AuditPolicy is the node's one audit failure posture
	// (auth.audit_failure_policy), the same the authorization and SO gates
	// follow. A registration, a claim and a rekey opening are recorded
	// before they take effect, and under FailClosed -- the zero value -- a
	// record the trail refuses refuses the operation.
	AuditPolicy audit.FailurePolicy
}

// QuorumLimitFunc decides whether one quorum-challenge request may
// proceed. key names the scope and the caller -- an authenticated identity,
// else the TLS peer certificate, else the scope's one anonymous bucket --
// and anonymous reports the last case, so the composer can apply a separate
// policy to it. A refusal carries the delay before a retry can succeed.
type QuorumLimitFunc func(key string, anonymous bool) (allowed bool, retryAfter time.Duration)

// factor resolves a factor name.
func (d *HolderDeps) factor(name string) (authfactors.Factor, error) {
	if d.Factors != nil {
		if f, err := d.Factors.Lookup(name); err == nil {
			return f, nil
		}
	}
	if name == mtls.Name {
		return mtls.New(), nil
	}
	return nil, &authfactors.FactorNotFoundError{Name: name}
}

// maxPendingQuorumChallenges bounds the quorum challenges a scope holds, so
// an unauthenticated caller naming real holders cannot grow it without limit.
const maxPendingQuorumChallenges = 256

// maxPendingPerHolderOp bounds the unanswered challenges one holder has for
// one operation.
const maxPendingPerHolderOp = 2

// maxPendingDecoys bounds the decoy table: the challenges answered for names
// outside the roster, and the fallback challenges answered to a holder whose
// slots are full of bound ones -- each stored here, and each verified like a
// real one when answered. They are kept apart from the real table so a flood of them can
// never crowd a holder's real challenge out; the oldest is evicted first.
const maxPendingDecoys = 256

// pendingQuorumChallenge is one issued, unanswered quorum challenge.
type pendingQuorumChallenge struct {
	domain    seal.QuorumDomain
	op        seal.QuorumOp
	holder    string
	digest    []byte
	factor    string
	challenge *authfactors.Challenge
	// issued orders the holder's challenges for eviction.
	issued time.Time
	// bound records that the request presented the holder's own enrolled
	// certificate. A request that does not can never evict it.
	bound bool
	// decoy marks a challenge issued for a name outside the roster, or for a
	// factor the policy does not admit. It verifies with verifier -- the
	// factor that issued it from a decoy enrollment -- exactly as a real
	// challenge verifies with its holder's factor, and fails as a wrong
	// answer to a real challenge fails, so a submission against it cannot
	// tell a stranger from a holder.
	decoy    bool
	verifier authfactors.Factor
}

// holderScope is the one share-holder engine. The system, recovery and every
// tenant scope are instances of it: registration, distribution, the claim
// handshake, quorum challenges and holder verification are the same code for
// a Security Officer and a custodian, and for every scope. Callers hold the
// owning service's lock.
type holderScope struct {
	scope    BarrierScope
	domain   seal.QuorumDomain
	tenantID string
	registry *OfficerRegistry
	store    *sealedShareStore
	// staged holds the envelopes of a distribution whose blob is not yet
	// durable, for stagedDistribution. committed swaps it in; until then
	// store, the set in force, is untouched.
	staged             *sealedShareStore
	stagedDistribution string
	claims             map[string]*ChallengeResponse
	quorum             map[string]*pendingQuorumChallenge
	// limitID names this scope instance in the quorum-begin throttle's keys,
	// so two instances of one scope (a test's second node, a re-created
	// tenant authority) never share a bucket.
	limitID string
	// decoys holds the decoy challenges issued and not yet answered (see
	// maxPendingDecoys).
	decoys map[string]*pendingQuorumChallenge
	sealed func() bool
	// roster reads the domain's roster in force from the backing barrier,
	// whose blob it is bound to. A backend that is not a barrier has none.
	roster func(context.Context, seal.QuorumDomain) (*seal.HolderRoster, []byte, error)
	// initialized reports whether the backing barrier has been initialized,
	// reading its persisted metadata so it answers while sealed. A backend
	// that is not a barrier (the tenant service store before the tenant
	// barrier exists) is never sealed, so it is never consulted.
	initialized func(context.Context) (bool, error)
	// capacity reports N, the number of holders the backing barrier's unseal
	// key is split to at initialization, when the scope fixes it before
	// initialization; zero when the initialization chooses it (capacityProbe).
	capacity func() int
	// initializing marks an initialization of the backing barrier in flight
	// (ScopedCeremonyService.InitializeShamir). Unenrolment is refused while
	// it is set: the barrier reads the roster twice, to validate and to
	// split, and a holder removed between the two would leave it initialized
	// without a share set. Guarded by the owning service's lock.
	initializing bool
	// audit records a security event on the backing barrier's audit log. A
	// backend that is not a barrier has none, and the event is only logged.
	audit  func(action, outcome string, details map[string]string)
	logger *slog.Logger
	// synced records that the registry has been reconciled with its
	// backend since the barrier was last seen unsealed.
	synced bool
}

// newHolderScope builds a scope over its own registry and share store.
func newHolderScope(scope BarrierScope, domain seal.QuorumDomain, tenantID string, backend seal.StorageBackend, ttl time.Duration, sealed func() bool, logger *slog.Logger) (*holderScope, error) {
	reg, err := NewScopedOfficerRegistry(backend, scope)
	if err != nil {
		return nil, err
	}
	return &holderScope{
		scope:       scope,
		domain:      domain,
		tenantID:    tenantID,
		registry:    reg,
		store:       newSealedShareStore(ttl),
		claims:      make(map[string]*ChallengeResponse),
		quorum:      make(map[string]*pendingQuorumChallenge),
		limitID:     scope.String() + "#" + randomHex(8),
		decoys:      make(map[string]*pendingQuorumChallenge),
		sealed:      sealed,
		roster:      rosterProbe(backend),
		initialized: initializedProbe(backend),
		capacity:    capacityProbe(scope, backend),
		audit:       auditProbe(backend),
		logger:      logger,
	}, nil
}

// rosterProbe returns backend's roster reader. A backend that is not a
// barrier reports seal.ErrHolderRosterMissing.
func rosterProbe(backend seal.StorageBackend) func(context.Context, seal.QuorumDomain) (*seal.HolderRoster, []byte, error) {
	if b, ok := backend.(interface {
		HolderRoster(context.Context, seal.QuorumDomain) (*seal.HolderRoster, []byte, error)
	}); ok {
		return b.HolderRoster
	}
	return func(context.Context, seal.QuorumDomain) (*seal.HolderRoster, []byte, error) {
		return nil, nil, seal.ErrHolderRosterMissing
	}
}

// auditProbe returns backend's audit sink. A backend that is not a barrier
// has no audit log, and its events are dropped (the caller logs them).
func auditProbe(backend seal.StorageBackend) func(action, outcome string, details map[string]string) {
	if b, ok := backend.(interface {
		Audit(action, outcome string, details map[string]string)
	}); ok {
		return b.Audit
	}
	return func(string, string, map[string]string) {}
}

// capacityProbe returns the number of holders backend's unseal key will be
// split to, when scope fixes it before initialization: a database barrier's
// M-of-N is fixed at its create, in its Shamir configuration, and its
// initialization splits to exactly that N. Every other scope's initialization
// chooses N -- the system barrier's and a tenant's take the threshold and
// total with the request, and a recovery key is split to every recovery
// holder enrolled -- so none is fixed beforehand, and the probe reports zero.
func capacityProbe(scope BarrierScope, backend seal.StorageBackend) func() int {
	b, ok := backend.(interface{ Config() seal.BarrierConfig })
	if !ok || scope.kind != scopeDatabase {
		return func() int { return 0 }
	}
	return func() int {
		if c := b.Config(); c.Shamir != nil {
			return c.Shamir.TotalShares
		}
		return 0
	}
}

// initializedProbe returns backend's initialization probe. A backend that is
// not a barrier has no initialization state and reports false.
func initializedProbe(backend seal.StorageBackend) func(context.Context) (bool, error) {
	if b, ok := backend.(interface {
		Initialized(context.Context) (bool, error)
	}); ok {
		return b.Initialized
	}
	return func(context.Context) (bool, error) { return false, nil }
}

// sync reconciles the registry with its backend the first time the backing
// barrier is seen unsealed: records enrolled while it was sealed (the
// initialization ceremony enrols before the barrier exists) are written, and
// records an earlier process persisted are loaded. Every registry-reading
// operation calls it, so a restarted node's holders are back in view as soon
// as a quorum unseals it.
//
// It never overwrites a persisted holder: a cached record is written only
// when storage has no holder of that name. One naming a persisted holder with
// a different identity is a conflict (*HolderRecordConflictError): the cached
// record is evicted and the conflict audited (evictConflict), and the sync
// carries on. It does not fail the sync, because a failing sync is retried by
// every later holder operation and would wedge the scope -- one registration
// accepted while sealed could otherwise lock every holder out.
func (h *holderScope) sync(ctx context.Context) error {
	if h.sealed() {
		h.synced = false
		return nil
	}
	if h.synced {
		return nil
	}
	for _, rec := range h.registry.Cached() {
		err := h.registry.reconcile(ctx, rec)
		var conflict *HolderRecordConflictError
		if errors.As(err, &conflict) {
			h.evictConflict(rec)
			continue
		}
		if err != nil {
			return err
		}
	}
	if err := h.registry.Reload(ctx); err != nil {
		return err
	}
	h.synced = true
	return nil
}

// evictConflict drops rec, a cached record that conflicts with the persisted
// holder of its name, from the registry cache, and records the conflict on
// the barrier's audit log and the service log. The persisted holder stays,
// and is what the cache holds once the sync reloads it.
func (h *holderScope) evictConflict(rec *PersistentOfficer) {
	h.registry.evict(rec.Username)
	details := map[string]string{
		"scope":       h.scope.String(),
		"username":    rec.Username,
		"cached_cert": rec.CertThumbprint,
		"reason":      "a holder enrolled while the barrier was sealed names a persisted holder with a different identity",
	}
	h.audit("holder-record-conflict", "deny", details)
	h.logger.Warn("share holder record conflict: the record enrolled while sealed was discarded and the persisted holder kept",
		slog.String("scope", h.scope.String()), slog.String("username", rec.Username),
		slog.String("cached_cert", rec.CertThumbprint))
}

// rebind moves the scope to barrier, a new barrier instance over the same
// storage. The registry cache, the held envelopes and the pending challenges
// stay; the registry is reconciled with barrier's storage the next time it is
// seen unsealed.
func (h *holderScope) rebind(barrier *seal.StorageBarrier) {
	h.registry.rebind(barrier)
	h.sealed = barrier.IsSealed
	h.roster = barrier.HolderRoster
	h.initialized = barrier.Initialized
	h.capacity = capacityProbe(h.scope, barrier)
	h.audit = barrier.Audit
	h.synced = false
}

// persist saves rec, or caches it while the backing barrier is sealed.
func (h *holderScope) persist(ctx context.Context, rec *PersistentOfficer) error {
	if h.sealed() {
		h.registry.cacheOnly(rec)
		return nil
	}
	return h.registry.Save(ctx, rec)
}

// register enrols one holder (enrol) and records the enrolment, or its
// refusal, on the compliance trail.
//
// The enrolment is recorded as permitted before it takes effect -- once it
// has been validated, before the CA issues the certificate (a gating record,
// see gate) -- so a registration the trail cannot record under fail-closed is
// refused with *audit.GateError and nothing is issued or stored. Its outcome
// is recorded once it has run (settle): success once the holder is stored, or
// the failure -- a refusal before the gate, or a failure after it.
func (h *holderScope) register(ctx context.Context, d *HolderDeps, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	role := req.Role
	if role == "" {
		role = seal.RoleSecurityOfficer
	}
	ev := holderEvent{
		action: ActionHolderRegister,
		holder: req.Username,
		actor:  req.Enroller,
		attrs:  map[string]any{"role": string(role)},
	}
	res, err := h.enrol(ctx, d, req, func() error { return h.gate(ctx, d, ev) })
	h.settle(ctx, d, ev, err)
	return res, err
}

// enrol enrols one holder: it has the CA issue the holder a certificate
// carrying a CA-assigned identity, records the envelope recipient key and
// every enrolled factor's public material, and persists the public record.
// No share exists at this point; shares come only from a distribution.
//
// admit is called once the request is validated and before the first effect
// (the certificate's issue); its error refuses the enrolment unchanged.
func (h *holderScope) enrol(ctx context.Context, d *HolderDeps, req RegisterOfficerRequest, admit func() error) (*RegisterOfficerResult, error) {
	if req.Role == "" {
		req.Role = seal.RoleSecurityOfficer
	}
	if !req.Role.Valid() {
		return nil, ErrInvalidHolderRole
	}
	if !validPrincipalName(req.Username) {
		if req.Username == "" {
			return nil, ErrMissingUsername
		}
		return nil, &InvalidHolderNameError{Name: req.Username}
	}
	if req.Enroller != "" && req.Enroller == req.Username {
		return nil, ErrSelfEnrolment
	}
	if len(req.CSRPEM) == 0 {
		return nil, ErrMissingCSRPEM
	}
	if d.CA == nil {
		return nil, ErrCANotInitialized
	}
	if err := h.sync(ctx); err != nil {
		return nil, err
	}
	if err := h.nameFree(ctx, req.Username); err != nil {
		return nil, err
	}
	if err := h.refuseOverCapacity(ctx); err != nil {
		return nil, err
	}

	csr, err := parseCSR(req.CSRPEM)
	if err != nil {
		return nil, err
	}
	if _, ok := mtls.PublicKeyKind(csr.PublicKey); !ok {
		return nil, &UnsupportedHolderKeyError{Algorithm: csr.PublicKeyAlgorithm.String()}
	}
	spki, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return nil, &CSRParseError{Err: err}
	}

	encKEM, encKey, err := enrolmentEncryptionKey(req.EncryptionKey, csr)
	if err != nil {
		return nil, err
	}
	// One person holds one share per scope, whatever their role: the
	// certificate key and the encryption key must be new to the scope.
	for _, other := range h.registry.Cached() {
		otherSPKI, perr := pemSPKI(other.PubKeyPEM)
		if perr == nil && bytes.Equal(otherSPKI, spki) {
			return nil, ErrDuplicateHolderKey
		}
		if other.EncryptionKEM == encKEM && bytes.Equal(other.EncryptionKey, encKey) {
			return nil, ErrDuplicateHolderKey
		}
	}

	profile, ok := holderProfiles[req.Role]
	if !ok {
		return nil, ErrInvalidHolderRole
	}
	if !validPrincipalName(req.Username) {
		return nil, &InvalidHolderNameError{Name: req.Username}
	}
	designated, err := designatedFactor(req)
	if err != nil {
		return nil, err
	}
	if err := admit(); err != nil {
		return nil, err
	}
	// The certificate names the registered holder in the scope's tenant and
	// carries no SAN, whatever the CSR asked for (signHolderCSR); the holder
	// profile OU makes it a holder certificate, which authenticates with no
	// roles (auth.HolderProfile).
	cert, _, err := signHolderCSR(ctx, d.CA, req.CSRPEM, &SignCSROptions{
		TenantID:     tenantid.Normalize(h.tenantID),
		Username:     req.Username,
		Profile:      profile,
		ValidityDays: 365,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, err
	}

	factors, err := h.enrolFactors(ctx, d, req, cert)
	if err != nil {
		return nil, err
	}

	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki}))
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	rec := &PersistentOfficer{
		Username:       req.Username,
		Role:           req.Role,
		CertPEM:        string(certPEM),
		CertThumbprint: mtls.Thumbprint(cert.Raw),
		PubKeyPEM:      pubPEM,
		EncryptionKEM:  encKEM,
		EncryptionKey:  encKey,
		Factors:        factors,
		Designated:     designated,
		EnrolledAt:     time.Now().UTC(),
	}
	if err := h.persist(ctx, rec); err != nil {
		return nil, err
	}

	names := make([]string, len(factors))
	for i, f := range factors {
		names[i] = f.Name
	}
	h.logger.Info("share holder registered",
		slog.String("scope", h.scope.String()),
		slog.String("username", req.Username),
		slog.String("role", string(req.Role)),
		slog.String("designated", designated),
		slog.String("kem", encKEM))

	return &RegisterOfficerResult{
		CertPEM:       certPEM,
		Role:          req.Role,
		Designated:    designated,
		EncryptionKEM: encKEM,
		Factors:       names,
	}, nil
}

// refuseOverCapacity refuses an enrolment past N on a scope that fixes N
// (capacityProbe) and whose barrier is not yet initialized: initialization
// needs exactly N holders and refuses any other count, and before it no
// holder can be revoked, so an N+1th holder would leave the scope with no way
// forward. Once the barrier is initialized a new holder joins at the next
// rekey, and nothing is refused here.
func (h *holderScope) refuseOverCapacity(ctx context.Context) error {
	n := h.capacity()
	if n == 0 {
		return nil
	}
	initialized, err := h.initialized(ctx)
	if err != nil {
		return err
	}
	if !initialized && len(h.registry.Cached()) >= n {
		return &HolderLimitReachedError{Total: n}
	}
	return nil
}

// unenrol removes an enrolled holder from a scope whose barrier is not yet
// initialized (removeEnrolled), and records the removal, or its refusal, on
// the compliance trail.
//
// The removal is recorded as permitted before it takes effect (a gating
// record, see gate): under fail-closed one the trail cannot record is refused
// with *audit.GateError and the holder stays enrolled. Its outcome is recorded
// once it has run (settle): success once the record is removed, or the
// failure -- a refusal before the gate, or a failure after it.
func (h *holderScope) unenrol(ctx context.Context, d *HolderDeps, username string) error {
	ev := holderEvent{action: ActionHolderUnenrol, holder: username}
	err := h.removeEnrolled(ctx, username, func() error { return h.gate(ctx, d, ev) })
	h.settle(ctx, d, ev, err)
	return err
}

// removeEnrolled removes username's enrolment before initialization. No share
// exists before it, so the holder holds nothing and the removal needs no
// quorum: it is the way out of an enrolment that cannot be initialized (a
// holder enrolled by mistake, or one whose key is lost before it was ever
// used). On an initialized barrier every holder may hold a share, and only a
// revocation -- a rekey the quorum completes -- removes one
// (ErrUnenrolInitialized). While an initialization is in flight it is
// refused with ErrInitializationInProgress. admit is called once the removal
// is validated and before the record is removed; its error refuses it.
func (h *holderScope) removeEnrolled(ctx context.Context, username string, admit func() error) error {
	if username == "" {
		return ErrMissingUsername
	}
	if h.initializing {
		return ErrInitializationInProgress
	}
	initialized, err := h.initialized(ctx)
	if err != nil {
		return err
	}
	if initialized {
		return ErrUnenrolInitialized
	}
	if err := h.sync(ctx); err != nil {
		return err
	}
	if _, ok := h.registry.cached(username); !ok {
		return ErrOfficerNotFound
	}
	if err := admit(); err != nil {
		return err
	}
	if h.sealed() {
		// Enrolled while sealed, the record is in the cache alone.
		h.registry.evict(username)
	} else if err := h.registry.Delete(ctx, username); err != nil {
		return err
	}
	h.logger.Info("share holder unenrolled before initialization",
		slog.String("scope", h.scope.String()), slog.String("username", username))
	return nil
}

// nameFree reports whether username can be enrolled, failing closed.
//
// While the barrier is sealed the persisted roster cannot be read, so
// enrolment is allowed only before initialization -- the ceremony that
// enrols the first holders runs before the barrier exists -- and is checked
// against the holders cached so far. On an initialized sealed barrier the
// roster already exists behind it and the name cannot be checked, so the
// registration is refused: it would otherwise be cached unchecked and written
// over the persisted holder of the same name at the next unseal.
//
// Unsealed, a read that finds the holder is a duplicate and one that finds
// nothing frees the name; any other read failure is returned, never taken
// as "free".
func (h *holderScope) nameFree(ctx context.Context, username string) error {
	if h.sealed() {
		initialized, err := h.initialized(ctx)
		if err != nil {
			return err
		}
		if initialized {
			return ErrRegistrationWhileSealed
		}
		if _, ok := h.registry.cached(username); ok {
			return ErrDuplicateUsername
		}
		return nil
	}
	_, err := h.registry.Load(ctx, username)
	switch {
	case err == nil:
		return ErrDuplicateUsername
	case errors.Is(err, ErrUserNotFound):
		return nil
	default:
		return err
	}
}

// enrolFactors enrols "mtls" from the issued certificate and every requested
// additional factor through its registered implementation.
func (h *holderScope) enrolFactors(ctx context.Context, d *HolderDeps, req RegisterOfficerRequest, cert *x509.Certificate) ([]seal.RosterFactor, error) {
	me := mtls.EnrollmentFromCertificate(req.Username, h.tenantID, cert)
	out := []seal.RosterFactor{{Name: me.FactorName, EnrollmentID: me.EnrollmentID, PublicKey: me.PublicKey, Metadata: me.Metadata}}
	seen := map[string]bool{mtls.Name: true}
	for _, fe := range req.Factors {
		if fe.Name == "" || seen[fe.Name] {
			return nil, &DuplicateFactorError{Factor: fe.Name}
		}
		seen[fe.Name] = true
		f, err := d.factor(fe.Name)
		if err != nil {
			return nil, err
		}
		e, err := f.Enroll(ctx, &authfactors.EnrollParams{UserID: req.Username, TenantID: h.tenantID, MethodParams: fe.Params})
		if err != nil {
			return nil, &HolderFactorEnrollmentError{Factor: fe.Name, Err: err}
		}
		if e == nil || len(e.PublicKey) == 0 {
			return nil, &HolderFactorEnrollmentError{Factor: fe.Name, Err: &authfactors.EnrollmentInvalidError{Reason: "no public credential"}}
		}
		out = append(out, seal.RosterFactor{Name: fe.Name, EnrollmentID: e.EnrollmentID, PublicKey: e.PublicKey, Metadata: e.Metadata})
	}
	return out, nil
}

// designatedFactor returns the factor req designates -- mtls when it names
// none -- or *FactorNotEnrolledError when req does not enrol it. It reads the
// request alone, so a registration is refused for it before the gate:
// enrolFactors enrols mtls and every factor req names, or fails.
func designatedFactor(req RegisterOfficerRequest) (string, error) {
	if req.Designated == "" || req.Designated == mtls.Name {
		return mtls.Name, nil
	}
	for _, fe := range req.Factors {
		if fe.Name == req.Designated {
			return req.Designated, nil
		}
	}
	return "", &FactorNotEnrolledError{Factor: req.Designated}
}

// enrolmentEncryptionKey resolves the holder's envelope recipient key: the
// separately enrolled key when one is supplied, otherwise the certificate key
// itself when a registered KEM can seal to it.
func enrolmentEncryptionKey(req *EncryptionKeyEnrolment, csr *x509.CertificateRequest) (string, []byte, error) {
	if req != nil {
		suite, err := keydelivery.LookupSuite(req.KEM)
		if err != nil {
			return "", nil, err
		}
		if _, err := suite.PublicKey(req.PublicKey); err != nil {
			return "", nil, err
		}
		return req.KEM, append([]byte(nil), req.PublicKey...), nil
	}
	kem, raw, err := keydelivery.RecipientFromPublicKey(csr.PublicKey)
	if err != nil {
		return "", nil, &EncryptionKeyRequiredError{Err: err}
	}
	return kem, raw, nil
}

// prepareRoster snapshots every active holder except exclude, in enrolment
// order, with share indexes 1..N.
//
// A holder is active unless a distribution removed them. Removal is recorded
// twice: in the roster bound to the blob, durable the moment the blob is,
// and by deleting the holder's record in committed, which runs after it and
// can fail. So a holder whose record says they took part in an earlier
// distribution but who is absent from the roster in force was removed, and
// is never dealt a share again, whatever happened to the record. A holder
// enrolled since the last distribution has no distribution recorded and
// joins.
func (h *holderScope) prepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	if domain != h.domain {
		return nil, &seal.QuorumOpInvalidError{Domain: domain}
	}
	if err := h.sync(ctx); err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	current, _, err := h.roster(ctx, domain)
	switch {
	case err == nil:
		for _, rec := range h.registry.Cached() {
			if _, inForce := current.Holder(rec.Username); rec.Distribution != "" && !inForce {
				skip[rec.Username] = true
			}
		}
	case errors.Is(err, seal.ErrHolderRosterMissing):
		// No distribution is in force yet: nobody has been removed.
	default:
		return nil, err
	}
	roster := seal.NewHolderRoster(domain, policy, threshold)
	for _, rec := range h.registry.Cached() {
		if skip[rec.Username] {
			continue
		}
		roster.Holders = append(roster.Holders, seal.RosterHolder{
			Username:       rec.Username,
			Role:           rec.Role,
			ShareIndex:     len(roster.Holders) + 1,
			CertThumbprint: rec.CertThumbprint,
			Designated:     rec.Designated,
			Factors:        append([]seal.RosterFactor(nil), rec.Factors...),
		})
	}
	roster.Total = len(roster.Holders)
	if threshold < 2 || roster.Total < threshold {
		return nil, ErrInsufficientOfficers
	}
	return roster, nil
}

// distribute splits secret and seals each share into an envelope to its
// holder's encryption key, held in protected memory. The envelopes are
// STAGED: the distribution in force, and every envelope of it nobody has
// claimed yet, stays as it is until committed swaps the staged set in, once
// the blob the new shares belong to is durable. A staged set left by a
// distribution that never committed is destroyed here. secret is borrowed
// only to split it; the caller owns it.
func (h *holderScope) distribute(ctx context.Context, secret *secure.Buffer, roster *seal.HolderRoster, digest []byte) (int, error) {
	if roster.Domain != h.domain {
		return 0, &seal.QuorumOpInvalidError{Domain: roster.Domain}
	}
	if err := h.sync(ctx); err != nil {
		return 0, err
	}
	records := make(map[string]*PersistentOfficer, roster.Total)
	for _, rec := range h.registry.Cached() {
		records[rec.Username] = rec
	}
	for i := range roster.Holders {
		if _, ok := records[roster.Holders[i].Username]; !ok {
			return 0, ErrOfficerNotFound
		}
	}

	var shares []*shamir.ProtectedShare
	//secure:unborrowed allocates-protected SplitProtected seals each share into a go-enclave enclave of its own
	if err := secret.WithBytes(func(k []byte) error {
		var serr error
		shares, serr = shamir.SplitProtected(k, roster.Threshold, roster.Total)
		return serr
	}); err != nil {
		return 0, &ShamirSplitError{Err: err}
	}
	defer func() {
		for _, s := range shares {
			s.Destroy()
		}
	}()

	h.discardStaged()
	staged := newSealedShareStore(h.store.ttl)
	for i := range roster.Holders {
		holder := &roster.Holders[i]
		var envelope []byte
		if err := shares[holder.ShareIndex-1].WithValue(func(v []byte) error {
			var serr error
			envelope, serr = sealShareEnvelope(h.scope, roster, digest, holder, records[holder.Username], v)
			return serr
		}); err != nil {
			staged.DestroyAll()
			return 0, err
		}
		if err := staged.Store(holder.Username, envelope); err != nil {
			staged.DestroyAll()
			return 0, &ShareStoreError{Err: err}
		}
	}
	h.staged, h.stagedDistribution = staged, roster.Distribution
	return roster.Total, nil
}

// promoteStaged swaps in the envelopes staged for distribution: the previous
// set and its pending claims are destroyed, and the staged set is now the one
// holders claim from. It reports whether a staged set for distribution was
// there to promote.
func (h *holderScope) promoteStaged(distribution string) bool {
	if h.staged == nil || h.stagedDistribution != distribution {
		return false
	}
	previous := h.store
	h.store, h.staged, h.stagedDistribution = h.staged, nil, ""
	previous.DestroyAll()
	for username, c := range h.claims {
		secure.Wipe(c.Nonce)
		delete(h.claims, username)
	}
	return true
}

// discardStaged destroys a staged set that was never committed.
func (h *holderScope) discardStaged() {
	if h.staged != nil {
		h.staged.DestroyAll()
	}
	h.staged, h.stagedDistribution = nil, ""
}

// committed swaps in the envelopes staged for roster, now that the blob they
// belong to is durable, then records the durable roster's share indexes and
// drops removed holders. The swap comes first, so the envelopes in force
// always belong to the blob in force; a failure recording the rest leaves the
// removed holders excluded by prepareRoster all the same.
//
// When nothing is staged for roster's distribution -- another distribution
// replaced the staged set before this one committed -- no envelope in hand
// opens the durable blob. That is refused with *StagedDistributionMissingError
// and nothing is changed: recording the roster would advertise a share set no
// holder can claim. The barrier serialises its redistributions per domain, so
// this does not happen through it; the refusal makes any other caller's
// interleaving loud instead of silent.
//
// Each removed holder's revocation is recorded on the compliance trail once
// its record is deleted -- the rekey that revoked it is durable by then -- and
// a deletion that fails is recorded as a failed revocation.
func (h *holderScope) committed(ctx context.Context, d *HolderDeps, roster *seal.HolderRoster, removed []string) error {
	if !h.promoteStaged(roster.Distribution) {
		return &StagedDistributionMissingError{Distribution: roster.Distribution}
	}
	if err := h.sync(ctx); err != nil {
		return err
	}
	for _, name := range removed {
		err := h.registry.Delete(ctx, name)
		h.record(ctx, d, holderEvent{action: ActionHolderRevoked, holder: name, actor: holderActorQuorum, err: err})
		if err != nil {
			return err
		}
		h.logger.Info("share holder revoked", slog.String("scope", h.scope.String()), slog.String("username", name))
	}
	for i := range roster.Holders {
		holder := &roster.Holders[i]
		rec, err := h.registry.Load(ctx, holder.Username)
		if err != nil {
			return err
		}
		rec.ShareIndex = holder.ShareIndex
		rec.Distribution = roster.Distribution
		rec.ClaimedAt = time.Time{}
		if err := h.persist(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// beginClaim issues the claim nonce to a holder presenting its enrolled
// certificate over mTLS, or claiming in process (WithInProcessClaim), where
// the claim is bound to the enrolled certificate and completeClaim demands
// the nonce signature by its key.
func (h *holderScope) beginClaim(ctx context.Context, username, callerThumbprint string) (*ChallengeResponse, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}
	if err := h.sync(ctx); err != nil {
		return nil, err
	}
	rec, err := h.registry.Load(ctx, username)
	if err != nil {
		return nil, ErrOfficerNotFound
	}
	if !thumbprintsEqual(claimCertificate(ctx, callerThumbprint, rec.CertThumbprint), rec.CertThumbprint) {
		return nil, ErrCertThumbprintMismatch
	}
	if !h.store.Exists(username) {
		if h.store.Expired(username) {
			return nil, ErrShareExpired
		}
		return nil, ErrNoPendingShare
	}
	nonce := make([]byte, challengeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrInitFailed
	}
	c := &ChallengeResponse{Nonce: nonce, Username: username, ExpiresAt: time.Now().Add(challengeTTL)}
	h.claims[username] = c
	return c, nil
}

// completeClaim runs the claim (claim) with its record on the compliance
// trail. The record names the holder and the share index; the envelope is
// never part of it. A claim the holder has proved is recorded as permitted
// BEFORE its envelope leaves the store (a gating record, see gate): under
// fail-closed a claim the trail cannot record is refused with
// *audit.GateError and the envelope stays held, intact and claimable. Its
// outcome is recorded once it has run (settle): success once the envelope is
// delivered, a failure after the gate, or -- before it -- a denial, the
// claimant not having proved itself the holder.
func (h *holderScope) completeClaim(ctx context.Context, d *HolderDeps, username, nonceHex string, signature []byte, callerThumbprint string) (*ShareClaimResult, error) {
	ev := holderEvent{action: ActionHolderClaim, holder: username, denied: true}
	res, err := h.claim(ctx, username, nonceHex, signature, callerThumbprint, func(shareIndex int) error {
		// The signature over the nonce, by the enrolled key, proved the
		// caller is the holder.
		ev.actor, ev.denied = username, false
		ev.attrs = map[string]any{"share_index": shareIndex}
		return h.gate(ctx, d, ev)
	})
	h.settle(ctx, d, ev, err)
	return res, err
}

// claim verifies the signature over the claim nonce and delivers the
// holder's envelope exactly once, destroying the protected copy. admit is
// called once the holder is proved and before the envelope is taken from the
// store; its error refuses the claim with the envelope still held.
func (h *holderScope) claim(ctx context.Context, username, nonceHex string, signature []byte, callerThumbprint string, admit func(shareIndex int) error) (*ShareClaimResult, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}
	c, ok := h.claims[username]
	if !ok {
		return nil, ErrChallengeNotFound
	}
	if time.Now().After(c.ExpiresAt) {
		delete(h.claims, username)
		return nil, ErrChallengeExpired
	}
	provided, err := hex.DecodeString(nonceHex)
	if err != nil || !bytes.Equal(provided, c.Nonce) {
		return nil, ErrChallengeVerificationFailed
	}
	delete(h.claims, username)

	rec, err := h.registry.Load(ctx, username)
	if err != nil {
		return nil, ErrOfficerNotFound
	}
	if !thumbprintsEqual(claimCertificate(ctx, callerThumbprint, rec.CertThumbprint), rec.CertThumbprint) {
		return nil, ErrCertThumbprintMismatch
	}
	pub, err := parsePublicKeyPEM(rec.PubKeyPEM)
	if err != nil || !verifyShareClaim(pub, h.scope.ClaimLabel(), username, c.Nonce, signature) {
		return nil, ErrChallengeVerificationFailed
	}
	// The envelope is the share already sealed (HPKE) to this holder's own
	// key: ciphertext, which leaves the node as the claim response. The
	// store's protected copy is destroyed by Claim; this one has to outlive
	// the function to be delivered.
	//secure:unwiped HPKE ciphertext sealed to the holder's key, returned to the transport as the one-time claim response
	if !h.store.Exists(username) {
		if h.store.Expired(username) {
			return nil, ErrShareExpired
		}
		return nil, ErrNoPendingShare
	}
	if err := admit(rec.ShareIndex); err != nil {
		return nil, err
	}
	envelope, err := h.store.Claim(username)
	if err != nil {
		return nil, err
	}
	rec.ClaimedAt = time.Now().UTC()
	if perr := h.persist(ctx, rec); perr != nil {
		h.logger.Warn("failed to record share claim", slog.String("username", username), slog.String("error", perr.Error()))
	}
	return &ShareClaimResult{ShareIndex: rec.ShareIndex, Envelope: envelope}, nil
}

// beginQuorum issues a factor challenge for one holder's submission to op.
// The roster is read from the barrier, so this works while it is sealed.
//
// The sealed window admits this request anonymously, so it is hardened
// against a caller who is not the holder:
//
//   - every request passes the composer's throttle first, keyed by scope and
//     caller (see throttle);
//   - a name outside the roster, or a holder asked for a factor the policy
//     does not admit, gets a decoy challenge (issueDecoy): issued by the
//     requested factor's own Challenge from a decoy enrollment derived from
//     the domain's persisted seed and the name, so it has the shape of a
//     real one and the same public material on every request and across
//     restarts, as a real holder's has. It is STORED like a real challenge,
//     so an answer to it runs the same Verify and fails it the same way. The
//     decoy is derived on every request, real or not, so the two cost the
//     same;
//   - a holder has at most maxPendingPerHolderOp challenges per operation,
//     the oldest evicted first, and a challenge requested over a connection
//     presenting the holder's own certificate is never evicted by one that
//     did not (admitQuorumChallenge). A request that cannot be admitted is
//     answered with the challenge it would have got, stored in the decoy
//     table rather than in the holder's slots, so it verifies when answered,
//     rather than with an error a stranger's name could never produce.
func (h *holderScope) beginQuorum(ctx context.Context, d *HolderDeps, barrier *seal.StorageBarrier, op seal.QuorumOp, username, factorName string) (*QuorumChallenge, error) {
	if !op.ValidIn(h.domain) {
		return nil, &seal.QuorumOpInvalidError{Domain: h.domain, Op: op}
	}
	peerThumb, err := h.throttle(ctx, d, quorumKindBegin)
	if err != nil {
		return nil, err
	}
	roster, digest, err := barrier.HolderRoster(ctx, h.domain)
	if err != nil {
		return nil, err
	}
	decoy, decoyOut, err := h.issueDecoy(ctx, d, barrier, roster, digest, op, username, factorName)
	if err != nil {
		return nil, err
	}
	rf, err := roster.AdmittedFactor(username, factorName)
	if err != nil {
		var notInRoster *seal.HolderNotInRosterError
		var notAdmitted *seal.FactorNotAdmittedError
		if errors.As(err, &notInRoster) || errors.As(err, &notAdmitted) {
			h.storeDecoy(decoy, digest)
			return decoyOut, nil
		}
		return nil, err
	}
	f, err := d.factor(rf.Name)
	if err != nil {
		return nil, err
	}
	holder, _ := roster.Holder(username)
	bound := peerThumb != "" && thumbprintsEqual(peerThumb, holder.CertThumbprint)
	ch, err := f.Challenge(ctx, &authfactors.Enrollment{
		EnrollmentID: rf.EnrollmentID,
		UserID:       username,
		FactorName:   rf.Name,
		TenantID:     h.tenantID,
		PublicKey:    rf.PublicKey,
		Metadata:     rf.Metadata,
	})
	if err != nil {
		return nil, err
	}
	bindQuorumPurpose(ch, h.quorumPurpose(op))
	out := &QuorumChallenge{
		ChallengeID: ch.ChallengeID,
		Domain:      h.domain,
		Op:          op,
		Holder:      username,
		Factor:      rf.Name,
		Nonce:       ch.Nonce,
		MethodData:  stringMethodData(ch.MethodData),
		ExpiresAt:   ch.ExpiresAt,
	}
	pending := &pendingQuorumChallenge{
		domain: h.domain, op: op, holder: username, digest: digest, factor: rf.Name, challenge: ch,
		issued: time.Now(), bound: bound,
	}
	if err := h.admitQuorumChallenge(roster, digest, op, username, bound); err != nil {
		if errors.Is(err, ErrTooManyPendingChallenges) {
			// The holder's slots are full of bound challenges. The request
			// is answered with its challenge all the same, kept in the decoy
			// table, so it neither evicts a bound one nor answers in a way a
			// stranger's name never could.
			h.storeDecoy(pending, digest)
			return out, nil
		}
		return nil, err
	}
	h.quorum[ch.ChallengeID] = pending
	return out, nil
}

// Quorum request kinds, each throttled in a bucket of its own per caller: a
// holder answers every challenge it requests with one submission, so the two
// kinds arrive in equal numbers, and a shared bucket would halve the quorums
// the anonymous bucket can serve without slowing a caller who only does one.
const (
	quorumKindBegin  = "begin"
	quorumKindSubmit = "submit"
)

// throttle passes one quorum request of kind -- a challenge request or a
// share submission -- through the composer's throttle, keyed by this scope
// instance, the kind and the caller (quorumCaller), and returns the
// thumbprint of the certificate the caller's TLS connection presented, if
// any.
func (h *holderScope) throttle(ctx context.Context, d *HolderDeps, kind string) (string, error) {
	caller, peerThumb, anonymous := quorumCaller(ctx)
	if d.QuorumLimiter != nil {
		if ok, retry := d.QuorumLimiter(h.limitID+"\x00"+kind+"\x00"+caller, anonymous); !ok {
			return "", &QuorumRateLimitedError{RetryAfter: retry}
		}
	}
	return peerThumb, nil
}

// pruneQuorumChallenges drops expired challenges.
func (h *holderScope) pruneQuorumChallenges() {
	now := time.Now()
	for id, p := range h.quorum {
		if now.After(p.challenge.ExpiresAt) {
			delete(h.quorum, id)
		}
	}
}

// admitQuorumChallenge makes room for one more challenge for holder's op.
//
// Expired challenges, and those issued against a roster the barrier no longer
// holds (they can never verify), are dropped. The holder may then keep
// maxPendingPerHolderOp challenges for op: beyond that the oldest one not
// bound to the holder's certificate is evicted, and a bound one only to make
// room for another bound request. An unbound request that finds every slot
// bound is refused with ErrTooManyPendingChallenges (beginQuorum answers it
// with a challenge stored in the decoy table, never the error). The scope's whole table
// is capped at what its roster can legitimately use -- every holder, every
// operation of the domain, maxPendingPerHolderOp each -- and never above
// maxPendingQuorumChallenges.
func (h *holderScope) admitQuorumChallenge(roster *seal.HolderRoster, digest []byte, op seal.QuorumOp, holder string, bound bool) error {
	h.pruneQuorumChallenges()
	for id, p := range h.quorum {
		if !bytes.Equal(p.digest, digest) {
			delete(h.quorum, id)
		}
	}
	for {
		var same []string
		for id, p := range h.quorum {
			if p.holder == holder && p.op == op {
				same = append(same, id)
			}
		}
		if len(same) < maxPendingPerHolderOp {
			break
		}
		victim := h.oldestQuorumChallenge(same, false)
		if victim == "" {
			if !bound {
				return ErrTooManyPendingChallenges
			}
			victim = h.oldestQuorumChallenge(same, true)
		}
		delete(h.quorum, victim)
	}
	limit := max(roster.Total, 1) * seal.QuorumOpCount(h.domain) * maxPendingPerHolderOp
	if len(h.quorum) >= min(limit, maxPendingQuorumChallenges) {
		return ErrTooManyPendingChallenges
	}
	return nil
}

// oldestQuorumChallenge returns the earliest-issued of ids whose bound flag
// is bound, or "" when there is none.
func (h *holderScope) oldestQuorumChallenge(ids []string, bound bool) string {
	oldest := ""
	for _, id := range ids {
		p := h.quorum[id]
		if p.bound != bound {
			continue
		}
		if oldest == "" || p.issued.Before(h.quorum[oldest].issued) {
			oldest = id
		}
	}
	return oldest
}

// quorumCaller identifies who is making a quorum request, for the throttle
// key: an authenticated identity, else a certificate the TLS stack VERIFIED,
// else the scope's single anonymous bucket. It also returns the presented
// certificate's thumbprint, verified or not, which binds a challenge to the
// holder it names: the handshake proved possession of that certificate's key,
// and binding compares it to the holder's enrolled one.
//
// An unverified certificate never keys a bucket of its own. Anyone can mint
// one, so keying on it would hand a caller a fresh bucket per request. Every
// anonymous caller of a scope therefore shares one bucket. That is a
// deliberate trade-off: a flood of anonymous requests can starve another
// anonymous caller -- a holder connecting without a verified certificate
// included -- until the bucket refills, while a holder who presents a
// verified certificate, or authenticates, has a bucket of their own.
func quorumCaller(ctx context.Context) (key, peerThumb string, anonymous bool) {
	var verified bool
	if st := auth.TLSStateFromContext(ctx); st != nil && len(st.PeerCertificates) > 0 {
		peerThumb = mtls.Thumbprint(st.PeerCertificates[0].Raw)
		verified = len(st.VerifiedChains) > 0
	}
	if id := auth.IdentityFromContext(ctx); id != nil && id.Subject != "" && !sealedwindow.Anonymous(ctx) {
		return "id\x00" + id.TenantID + "\x00" + id.Subject, peerThumb, false
	}
	if verified {
		return "tls\x00" + peerThumb, peerThumb, false
	}
	return "anonymous", peerThumb, true
}

// issueDecoy builds the decoy challenge for username: the challenge a name
// outside the roster gets, built on every request, real or not, so both cost
// the same. beginQuorum stores it (storeDecoy) only when it is the answer.
//
// The decoy is issued by the requested factor (mtls when none was requested)
// from a decoy enrollment it derives from the domain's seed, persisted with the
// roster by the barrier (seal.StorageBarrier.DecoySeed) so it survives a
// restart, and the name (authfactors.DecoyEnroller), shaped like the factor's enrollments in
// the roster. Its challenge therefore has the factor's own shape and the same
// public material on every request. A factor that cannot derive decoys -- or
// a name no factor is registered under -- is answered with an mtls decoy
// advertised under the requested name; the holders of such a factor are then
// distinguishable from strangers by the challenge's shape, which is why every
// factor should implement authfactors.DecoyEnroller.
func (h *holderScope) issueDecoy(ctx context.Context, d *HolderDeps, barrier *seal.StorageBarrier, roster *seal.HolderRoster, digest []byte, op seal.QuorumOp, username, factorName string) (*pendingQuorumChallenge, *QuorumChallenge, error) {
	seed, err := barrier.DecoySeed(ctx, h.domain)
	if err != nil {
		return nil, nil, err
	}
	advertised := factorName
	if advertised == "" {
		advertised = mtls.Name
	}
	f, enroller := decoyFactor(d, advertised)
	enr, err := enroller.DecoyEnrollment(seed, username, rosterEnrollments(roster, f.Name()))
	if err != nil {
		return nil, nil, err
	}
	enr.TenantID = h.tenantID
	ch, err := f.Challenge(ctx, enr)
	if err != nil {
		return nil, nil, err
	}
	bindQuorumPurpose(ch, h.quorumPurpose(op))
	pending := &pendingQuorumChallenge{
		domain: h.domain, op: op, holder: username, digest: digest, factor: advertised, challenge: ch,
		issued: time.Now(), decoy: true, verifier: f,
	}
	return pending, &QuorumChallenge{
		ChallengeID: ch.ChallengeID,
		Domain:      h.domain,
		Op:          op,
		Holder:      username,
		Factor:      advertised,
		Nonce:       ch.Nonce,
		MethodData:  stringMethodData(ch.MethodData),
		ExpiresAt:   ch.ExpiresAt,
	}, nil
}

// decoyFactor returns the factor that issues name's decoys: name's own when it
// is registered and derives decoys, else the built-in mtls factor.
func decoyFactor(d *HolderDeps, name string) (authfactors.Factor, authfactors.DecoyEnroller) {
	if f, err := d.factor(name); err == nil {
		if e, ok := f.(authfactors.DecoyEnroller); ok {
			return f, e
		}
	}
	m := mtls.New()
	return m, m
}

// rosterEnrollments returns the roster's enrollments of the named factor, the
// form a decoy of that factor takes after.
func rosterEnrollments(roster *seal.HolderRoster, factor string) []*authfactors.Enrollment {
	var out []*authfactors.Enrollment
	for _, holder := range roster.Holders {
		for _, rf := range holder.Factors {
			if rf.Name == factor {
				out = append(out, &authfactors.Enrollment{
					EnrollmentID: rf.EnrollmentID, UserID: holder.Username, FactorName: rf.Name,
					PublicKey: rf.PublicKey, Metadata: rf.Metadata,
				})
			}
		}
	}
	return out
}

// storeDecoy keeps p in the decoy table until it is answered, expires, or its
// roster is replaced, evicting the oldest entry when the table is full.
func (h *holderScope) storeDecoy(p *pendingQuorumChallenge, digest []byte) {
	now := time.Now()
	for id, q := range h.decoys {
		if now.After(q.challenge.ExpiresAt) || !bytes.Equal(q.digest, digest) {
			delete(h.decoys, id)
		}
	}
	for len(h.decoys) >= maxPendingDecoys {
		oldest := ""
		for id, q := range h.decoys {
			if oldest == "" || q.issued.Before(h.decoys[oldest].issued) {
				oldest = id
			}
		}
		delete(h.decoys, oldest)
	}
	h.decoys[p.challenge.ChallengeID] = p
}

// quorumPurpose is the purpose a quorum challenge of this scope for op is
// bound to: "quorum:<claim label>:<domain>:<op>". Neither a domain nor an
// operation contains ':', so distinct (scope, domain, op) never share one.
func (h *holderScope) quorumPurpose(op seal.QuorumOp) string {
	return "quorum:" + h.scope.ClaimLabel() + ":" + string(h.domain) + ":" + string(op)
}

// bindQuorumPurpose records purpose in ch's method data
// (mtls.MethodDataPurpose). The mtls factor signs it into the answer
// (mtls.ChallengeMessage) alongside the challenge ID, so an answer made for
// another scope, domain or operation never verifies; the server-held copy of
// ch is what the answer is verified against, so a client cannot change it.
func bindQuorumPurpose(ch *authfactors.Challenge, purpose string) {
	if ch.MethodData == nil {
		ch.MethodData = make(map[string]any, 1)
	}
	ch.MethodData[mtls.MethodDataPurpose] = purpose
}

// stringMethodData keeps the string-valued entries of a factor's method data.
func stringMethodData(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// randomHex returns n random bytes, hex encoded.
func randomHex(n int) string {
	return hex.EncodeToString(randomBytes(n))
}

// randomBytes returns n random bytes.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+).
	return b
}

// verifyHolder implements seal.HolderVerifier for the scope (authenticate)
// and records the submission's authentication on the compliance trail: the
// holder named, the operation and the outcome. The proof and the share are
// never part of the record.
func (h *holderScope) verifyHolder(ctx context.Context, d *HolderDeps, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	err := h.authenticate(ctx, d, domain, op, holder, roster, digest, proof)
	ev := holderEvent{
		action: ActionHolderQuorumSubmit,
		holder: holder,
		attrs:  map[string]any{"op": string(op)},
		err:    err,
		denied: err != nil,
	}
	if err == nil {
		// The factor response proved the caller is the holder.
		ev.actor = holder
	}
	h.record(ctx, d, ev)
	return err
}

// authenticate checks a submission's proof. The challenge is consumed
// whatever the outcome.
//
// A challenge is looked up in the real table and then the decoy table, so a
// decoy is answered by the same checks and the same Verify as a real one.
func (h *holderScope) authenticate(ctx context.Context, d *HolderDeps, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	p, ok := h.quorum[proof.ChallengeID]
	delete(h.quorum, proof.ChallengeID)
	if !ok {
		p, ok = h.decoys[proof.ChallengeID]
		delete(h.decoys, proof.ChallengeID)
	}
	if !ok {
		return ErrQuorumChallengeNotFound
	}
	if p.domain != domain || p.op != op || p.holder != holder || !bytes.Equal(p.digest, digest) ||
		(proof.Factor != "" && proof.Factor != p.factor) {
		return ErrQuorumChallengeMismatch
	}
	if time.Now().After(p.challenge.ExpiresAt) {
		return ErrChallengeExpired
	}
	f := p.verifier
	if !p.decoy {
		if _, err := roster.AdmittedFactor(holder, p.factor); err != nil {
			return err
		}
		var err error
		if f, err = d.factor(p.factor); err != nil {
			return err
		}
	}
	meta := make(map[string]string, len(proof.ResponseMetadata)+1)
	for k, v := range proof.ResponseMetadata {
		meta[k] = v
	}
	// The peer thumbprint is the server's observation, never the client's.
	delete(meta, authfactors.ResponseMetaPeerCertThumbprint)
	if proof.PeerCertThumbprint != "" {
		meta[authfactors.ResponseMetaPeerCertThumbprint] = proof.PeerCertThumbprint
	}
	if err := f.Verify(ctx, p.challenge, &authfactors.Response{Value: proof.Response, Metadata: meta}); err != nil {
		return &factorRefusedError{err: err}
	}
	return nil
}

// status snapshots every holder's public state.
func (h *holderScope) status(ctx context.Context) ([]*OfficerStatus, error) {
	if err := h.sync(ctx); err != nil {
		return nil, err
	}
	recs := h.registry.Cached()
	out := make([]*OfficerStatus, 0, len(recs))
	for _, r := range recs {
		names := make([]string, len(r.Factors))
		for i, f := range r.Factors {
			names[i] = f.Name
		}
		out = append(out, &OfficerStatus{
			Username:       r.Username,
			Role:           r.Role,
			CertThumbprint: r.CertThumbprint,
			ShareIndex:     r.ShareIndex,
			Designated:     r.Designated,
			Factors:        names,
			EncryptionKEM:  r.EncryptionKEM,
			EnrolledAt:     r.EnrolledAt,
			ClaimedAt:      r.ClaimedAt,
			SharePending:   h.store.Exists(r.Username) && !h.store.Claimed(r.Username),
		})
	}
	return out, nil
}

// allClaimed reports whether a distribution happened and every envelope of
// it has been claimed.
func (h *holderScope) allClaimed() bool {
	recs := h.registry.Cached()
	if len(recs) == 0 {
		return false
	}
	for _, r := range recs {
		if r.ShareIndex == 0 || !h.store.Claimed(r.Username) {
			return false
		}
	}
	return true
}

// cleanup destroys every held envelope and pending challenge.
func (h *holderScope) cleanup() {
	h.store.DestroyAll()
	h.discardStaged()
	for k, c := range h.claims {
		secure.Wipe(c.Nonce)
		delete(h.claims, k)
	}
	for k := range h.quorum {
		delete(h.quorum, k)
	}
	for k := range h.decoys {
		delete(h.decoys, k)
	}
}

// parseCSR decodes a PEM CSR and checks its self-signature.
func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, &CSRParseError{Err: ErrInvalidCSR}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, &CSRParseError{Err: err}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, &CSRParseError{Err: err}
	}
	return csr, nil
}

// pemSPKI returns the DER of a PEM PUBLIC KEY block.
func pemSPKI(p string) ([]byte, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, ErrInvalidPubKeyPEM
	}
	return block.Bytes, nil
}
