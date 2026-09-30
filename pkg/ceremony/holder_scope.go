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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// HolderDeps are the dependencies every holder scope shares: the ceremony CA
// that issues holder certificates, the trust domain those certificates'
// SPIFFE identities live in, and the registry of authentication factors
// holders may enrol (hardware factors are go-xkms implementations injected
// through WithFactorRegistry). The system ceremony hands the same deps to
// every tenant ceremony.
type HolderDeps struct {
	// CA issues holder certificates.
	CA CeremonyCA

	// TrustDomain is the SPIFFE trust domain of holder identities.
	TrustDomain string

	// Factors resolves factor names. "mtls" is always available: when the
	// registry does not carry it, the built-in pkg/authfactors/mtls factor
	// is used.
	Factors *authfactors.FactorRegistry
}

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

// pendingQuorumChallenge is one issued, unanswered quorum challenge.
type pendingQuorumChallenge struct {
	domain    seal.QuorumDomain
	op        seal.QuorumOp
	holder    string
	digest    []byte
	factor    string
	challenge *authfactors.Challenge
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
	claims   map[string]*ChallengeResponse
	quorum   map[string]*pendingQuorumChallenge
	sealed   func() bool
	logger   *slog.Logger
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
		scope:    scope,
		domain:   domain,
		tenantID: tenantID,
		registry: reg,
		store:    newSealedShareStore(ttl),
		claims:   make(map[string]*ChallengeResponse),
		quorum:   make(map[string]*pendingQuorumChallenge),
		sealed:   sealed,
		logger:   logger,
	}, nil
}

// sync reconciles the registry with its backend the first time the backing
// barrier is seen unsealed: records enrolled while it was sealed (the
// initialization ceremony enrols before the barrier exists) are written, and
// records an earlier process persisted are loaded. Every registry-reading
// operation calls it, so a restarted node's holders are back in view as soon
// as a quorum unseals it.
func (h *holderScope) sync(ctx context.Context) error {
	if h.sealed() {
		h.synced = false
		return nil
	}
	if h.synced {
		return nil
	}
	for _, rec := range h.registry.Cached() {
		if err := h.registry.Save(ctx, rec); err != nil {
			return err
		}
	}
	if err := h.registry.Reload(ctx); err != nil {
		return err
	}
	h.synced = true
	return nil
}

// rebind moves the scope to barrier, a new barrier instance over the same
// storage. The registry cache, the held envelopes and the pending challenges
// stay; the registry is reconciled with barrier's storage the next time it is
// seen unsealed.
func (h *holderScope) rebind(barrier *seal.StorageBarrier) {
	h.registry.rebind(barrier)
	h.sealed = barrier.IsSealed
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

// register enrols one holder: it has the CA issue the holder a certificate
// carrying a CA-assigned identity, records the envelope recipient key and
// every enrolled factor's public material, and persists the public record.
// No share exists at this point; shares come only from a distribution.
func (h *holderScope) register(ctx context.Context, d *HolderDeps, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
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
	if _, err := h.registry.Load(ctx, req.Username); err == nil {
		return nil, ErrDuplicateUsername
	}

	csr, err := parseCSR(req.CSRPEM)
	if err != nil {
		return nil, err
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

	ident, err := holderIdentity(d.TrustDomain, h.tenantID, req.Username, req.Role)
	if err != nil {
		return nil, err
	}
	cert, err := d.CA.SignCSR(ctx, req.CSRPEM, &SignCSROptions{
		Subject:      ident.subject,
		URIs:         ident.uris,
		Profile:      holderProfiles[req.Role],
		ValidityDays: 365,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	factors, err := h.enrolFactors(ctx, d, req, cert)
	if err != nil {
		return nil, err
	}
	designated := req.Designated
	if designated == "" {
		designated = mtls.Name
	}
	enrolled := false
	for _, f := range factors {
		enrolled = enrolled || f.Name == designated
	}
	if !enrolled {
		return nil, &FactorNotEnrolledError{Factor: designated}
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
// holder's encryption key, held in protected memory until claimed. Every
// share and envelope of the previous distribution is destroyed first.
func (h *holderScope) distribute(ctx context.Context, secret []byte, roster *seal.HolderRoster, digest []byte) (int, error) {
	defer secure.Wipe(secret)
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

	shares, err := shamir.SplitProtected(secret, roster.Threshold, roster.Total)
	if err != nil {
		return 0, &ShamirSplitError{Err: err}
	}
	defer func() {
		for _, s := range shares {
			s.Destroy()
		}
	}()

	h.store.Reset()
	for username := range h.claims {
		delete(h.claims, username)
	}
	for i := range roster.Holders {
		holder := &roster.Holders[i]
		var envelope []byte
		if err := shares[holder.ShareIndex-1].WithValue(func(v []byte) error {
			var serr error
			envelope, serr = sealShareEnvelope(h.scope, roster, digest, holder, records[holder.Username], v)
			return serr
		}); err != nil {
			h.store.Reset()
			return 0, err
		}
		if err := h.store.Store(holder.Username, envelope); err != nil {
			h.store.Reset()
			return 0, &ShareStoreError{Err: err}
		}
	}
	return roster.Total, nil
}

// committed records the durable roster's share indexes and drops removed
// holders.
func (h *holderScope) committed(ctx context.Context, roster *seal.HolderRoster, removed []string) error {
	if err := h.sync(ctx); err != nil {
		return err
	}
	for _, name := range removed {
		if err := h.registry.Delete(ctx, name); err != nil {
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
// certificate over mTLS.
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
	if !thumbprintsEqual(callerThumbprint, rec.CertThumbprint) {
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

// completeClaim verifies the signature over the claim nonce and delivers the
// holder's envelope exactly once, destroying the protected copy.
func (h *holderScope) completeClaim(ctx context.Context, username, nonceHex string, signature []byte, callerThumbprint string) (*ShareClaimResult, error) {
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
	if !thumbprintsEqual(callerThumbprint, rec.CertThumbprint) {
		return nil, ErrCertThumbprintMismatch
	}
	pub, err := parsePublicKeyPEM(rec.PubKeyPEM)
	if err != nil || !mtls.VerifyNonceSignature(pub, c.Nonce, signature) {
		return nil, ErrChallengeVerificationFailed
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
func (h *holderScope) beginQuorum(ctx context.Context, d *HolderDeps, barrier *seal.StorageBarrier, op seal.QuorumOp, username, factorName string) (*QuorumChallenge, error) {
	if !op.ValidIn(h.domain) {
		return nil, &seal.QuorumOpInvalidError{Domain: h.domain, Op: op}
	}
	roster, digest, err := barrier.HolderRoster(ctx, h.domain)
	if err != nil {
		return nil, err
	}
	rf, err := roster.AdmittedFactor(username, factorName)
	if err != nil {
		return nil, err
	}
	f, err := d.factor(rf.Name)
	if err != nil {
		return nil, err
	}
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
	h.pruneQuorumChallenges()
	if len(h.quorum) >= maxPendingQuorumChallenges {
		return nil, ErrTooManyPendingChallenges
	}
	h.quorum[ch.ChallengeID] = &pendingQuorumChallenge{
		domain: h.domain, op: op, holder: username, digest: digest, factor: rf.Name, challenge: ch,
	}
	methodData := make(map[string]string, len(ch.MethodData))
	for k, v := range ch.MethodData {
		if s, ok := v.(string); ok {
			methodData[k] = s
		}
	}
	return &QuorumChallenge{
		ChallengeID: ch.ChallengeID,
		Domain:      h.domain,
		Op:          op,
		Holder:      username,
		Factor:      rf.Name,
		Nonce:       ch.Nonce,
		MethodData:  methodData,
		ExpiresAt:   ch.ExpiresAt,
	}, nil
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

// verifyHolder implements seal.HolderVerifier for the scope. The challenge is
// consumed whatever the outcome.
func (h *holderScope) verifyHolder(ctx context.Context, d *HolderDeps, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	p, ok := h.quorum[proof.ChallengeID]
	delete(h.quorum, proof.ChallengeID)
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
	if _, err := roster.AdmittedFactor(holder, p.factor); err != nil {
		return err
	}
	f, err := d.factor(p.factor)
	if err != nil {
		return err
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
	return f.Verify(ctx, p.challenge, &authfactors.Response{Value: proof.Response, Metadata: meta})
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
	for k, c := range h.claims {
		secure.Wipe(c.Nonce)
		delete(h.claims, k)
	}
	for k := range h.quorum {
		delete(h.quorum, k)
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
