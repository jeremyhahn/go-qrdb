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
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// RegisterOfficer validates and signs an officer CSR, persists the public
// enrollment record, and returns the signed certificate PEM and assigned
// share index. No share bytes are produced at registration time; splitting
// happens at SealAndDistribute time.
//
// Preconditions: CSR parses and CheckSignature passes; username unique in
// registry and pendingCerts; AuthenticatorType valid.
func (s *CeremonyService) RegisterOfficer(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ca == nil {
		return nil, ErrCANotInitialized
	}
	if req.Username == "" {
		return nil, ErrMissingUsername
	}
	if len(req.CSRPEM) == 0 {
		return nil, ErrMissingCSRPEM
	}
	if err := req.AuthenticatorType.Validate(); err != nil {
		return nil, err
	}

	// Ensure username uniqueness across both persistent registry and pending cache.
	if s.officerRegistry != nil {
		if _, err := s.officerRegistry.Load(ctx, req.Username); err == nil {
			return nil, ErrDuplicateUsername
		}
	}
	if _, exists := s.pendingCerts[req.Username]; exists {
		return nil, ErrDuplicateUsername
	}

	// Validate and sign the CSR.
	cert, csr, certPEM, err := signOfficerCSR(ctx, s.ca, req.CSRPEM)
	if err != nil {
		return nil, err
	}

	// Determine the share index as the next sequential slot.
	shareIndex := s.nextShareIndex()

	// Build the persistent record (no share material).
	thumb := certThumbprint(cert.Raw)
	pubPEM, err := marshalPublicKeyPEM(csr.PublicKey)
	if err != nil {
		return nil, &CertEncodeError{Err: err}
	}

	persisted := &PersistentOfficer{
		Username:       req.Username,
		CertThumbprint: thumb,
		PubKeyPEM:      pubPEM,
		ShareIndex:     shareIndex,
		AuthFactor:     authFactorFromAuthenticator(req.AuthenticatorType.Normalized()),
		EnrolledAt:     time.Now().UTC(),
	}

	// Lazy-initialize officer registry if needed.
	if err := s.ensureOfficerRegistry(); err != nil {
		return nil, err
	}

	registryWritable := !s.barrier.IsSealed()
	if registryWritable {
		if err := s.officerRegistry.Save(ctx, persisted); err != nil {
			return nil, err
		}
	} else {
		s.officerRegistry.mu.Lock()
		s.officerRegistry.cache[persisted.Username] = persisted
		s.officerRegistry.mu.Unlock()
	}

	// Stash the public key so BeginClaimShare/CompleteClaimShare can verify
	// the challenge signature without re-parsing the PEM each time.
	s.pendingCerts[req.Username] = &PendingCert{
		Username:          req.Username,
		CertPEM:           certPEM,
		CSRPubKey:         csr.PublicKey,
		AuthenticatorType: req.AuthenticatorType.Normalized(),
		AuthenticatorRef:  req.AuthenticatorRef,
		Claimed:           false,
	}

	s.logger.Info("officer registered",
		slog.String("username", req.Username),
		slog.Int("share_index", shareIndex))

	return &RegisterOfficerResult{
		CertPEM:    certPEM,
		ShareIndex: shareIndex,
	}, nil
}

// SealAndDistribute satisfies seal.CeremonyDistributor for the system barrier.
// It splits shareSecret into total shares with the given threshold and seals
// one share to each enrolled officer's enclave. shareSecret is wiped before
// this function returns regardless of outcome.
//
// Returns ErrInsufficientOfficers when fewer than threshold officers are enrolled.
// Returns ErrOfficerCountMismatch when the enrolled officer count != total.
func (s *CeremonyService) SealAndDistribute(ctx context.Context, shareSecret []byte, threshold, total int) (int, error) {
	defer secure.Wipe(shareSecret)

	s.mu.Lock()
	defer s.mu.Unlock()

	return sealSharesToOfficers(ctx, shareSecret, threshold, total, s.officerRegistry, s.shareStore)
}

// RekeyAndDistribute satisfies seal.CeremonyDistributor for the system barrier.
// It destroys all unclaimed share enclaves from the prior round, resets claim
// state, then seals new shares. newShareSecret is wiped before return.
func (s *CeremonyService) RekeyAndDistribute(ctx context.Context, newShareSecret []byte, newThreshold, newTotal int) (int, error) {
	defer secure.Wipe(newShareSecret)

	s.mu.Lock()
	defer s.mu.Unlock()

	// INV-10: destroy prior round's enclaves and clear claim history so
	// officers who claimed in the prior round can claim their new shares.
	s.shareStore.Reset()

	if s.officerRegistry != nil {
		if err := s.officerRegistry.ResetClaimedAt(ctx); err != nil {
			s.logger.Warn("failed to reset claimed-at timestamps during rekey",
				slog.String("error", err.Error()))
		}
	}

	return sealSharesToOfficers(ctx, newShareSecret, newThreshold, newTotal, s.officerRegistry, s.shareStore)
}

// BeginClaimShare issues a challenge nonce to the named officer for share claim.
// The officer must be enrolled, their cert thumbprint must match, and an
// unclaimed share must exist for them.
func (s *CeremonyService) BeginClaimShare(ctx context.Context, username, callerCertThumbprint string) (*ChallengeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return beginClaimShareLocked(ctx, username, callerCertThumbprint,
		s.officerRegistry, s.shareStore, s.challenges, "share:")
}

// CompleteClaimShare verifies the officer's nonce signature and returns the
// share bytes exactly once, then destroys the enclave.
func (s *CeremonyService) CompleteClaimShare(
	ctx context.Context,
	username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
) (*ShareClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := completeClaimShareLocked(ctx, username, nonceHex, signature, callerCertThumbprint,
		s.officerRegistry, s.shareStore, s.pendingCerts, s.challenges, "share:")
	if err != nil {
		return nil, err
	}

	// Best-effort: persist the claim timestamp.
	if s.officerRegistry != nil {
		if markErr := s.officerRegistry.MarkClaimed(ctx, username, time.Now().UTC()); markErr != nil {
			s.logger.Warn("failed to record share claim in persistent registry",
				slog.String("username", username),
				slog.String("error", markErr.Error()))
		}
	}

	if s.allClaimsComplete() {
		s.becomeOperationalLocked(ctx)
		s.logger.Info("all claims complete, ceremony operational")
	}

	return result, nil
}

// OfficerClaimStatus returns a per-officer claim status snapshot. Never
// includes share bytes.
func (s *CeremonyService) OfficerClaimStatus(_ context.Context) ([]*OfficerStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.officerRegistry == nil {
		return nil, nil
	}
	return officerStatusFromRegistry(s.officerRegistry, s.shareStore), nil
}

// -------------------------------------------------------------------------
// Shared unexported helpers used by both CeremonyService and
// TenantCeremonyService (and the recovery scope adapter).
// -------------------------------------------------------------------------

// signOfficerCSR validates the CSR, signs it with the custodian profile, and
// returns the certificate, the parsed CSR, and the PEM-encoded certificate.
func signOfficerCSR(ctx context.Context, ca CeremonyCA, csrPEM []byte) (*x509.Certificate, *x509.CertificateRequest, []byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, nil, nil, &CSRParseError{Err: ErrInvalidCSR}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, nil, &CSRParseError{Err: err}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, nil, &CSRParseError{Err: err}
	}

	cert, err := ca.SignCSR(ctx, csrPEM, &SignCSROptions{
		Profile:      "custodian",
		ValidityDays: 365,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, nil, nil, &CertEncodeError{Err: err}
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	return cert, csr, certPEM, nil
}

// sealSharesToOfficers splits shareSecret and seals one share per officer.
// shareSecret must NOT be wiped by this function (caller owns the defer-wipe).
// Officers are taken from registry.Cached(); share index determines which
// share each officer receives.
func sealSharesToOfficers(
	_ context.Context,
	shareSecret []byte,
	threshold, total int,
	registry *OfficerRegistry,
	store *sealedShareStore,
) (int, error) {
	if registry == nil {
		return 0, ErrInsufficientOfficers
	}

	officers := registry.Cached()
	if len(officers) < threshold {
		return 0, ErrInsufficientOfficers
	}
	if len(officers) != total {
		return 0, ErrOfficerCountMismatch
	}

	shares, err := shamir.SplitProtected(shareSecret, threshold, total)
	if err != nil {
		return 0, &ShamirSplitError{Err: err}
	}
	// Each share is one mlock'd allocation against RLIMIT_MEMLOCK. Destroy the
	// set on every path out, including the error returns inside the loop
	// below.
	defer func() {
		for _, share := range shares {
			share.Destroy()
		}
	}()

	for _, officer := range officers {
		idx := officer.ShareIndex - 1 // 0-based into shares slice
		if idx < 0 || idx >= len(shares) {
			return 0, ErrInvalidShareIndex
		}
		shareJSON, marshalErr := json.Marshal(shares[idx])
		if marshalErr != nil {
			return 0, &ShareStoreError{Err: marshalErr}
		}
		if storeErr := store.Store(officer.Username, shareJSON); storeErr != nil {
			return 0, &ShareStoreError{Err: storeErr}
		}
	}

	// The share values are wiped by the deferred Destroy above. What stood
	// here was a loop of wipeString(&shares[i].Value), which did not wipe
	// anything: it converted the string to []byte — allocating a COPY —
	// zeroed that copy, and dropped the reference. The original backing array
	// was never touched. See wipeString's removal in this change.

	return len(officers), nil
}

// beginClaimShareLocked implements the BeginClaimShare handshake under the
// caller's lock. challengePrefix is prepended to the username key so that
// cert-claim and share-claim challenges in the same map cannot collide.
func beginClaimShareLocked(
	ctx context.Context,
	username, callerCertThumbprint string,
	registry *OfficerRegistry,
	store *sealedShareStore,
	challenges map[string]*ChallengeResponse,
	challengePrefix string,
) (*ChallengeResponse, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}

	// The officer must be enrolled.
	if registry == nil {
		return nil, ErrOfficerNotFound
	}
	officer, err := registry.Load(ctx, username)
	if err != nil {
		return nil, ErrOfficerNotFound
	}

	// Thumbprint must match.
	if callerCertThumbprint != officer.CertThumbprint {
		return nil, ErrCertThumbprintMismatch
	}

	// A share must exist and be unclaimed.
	if !store.Exists(username) {
		return nil, ErrNoPendingShare
	}

	nonce := make([]byte, challengeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrInitFailed
	}

	challenge := &ChallengeResponse{
		Nonce:     nonce,
		Username:  username,
		ExpiresAt: time.Now().Add(challengeTTL),
	}
	challenges[challengePrefix+username] = challenge
	return challenge, nil
}

// completeClaimShareLocked implements the CompleteClaimShare handshake under
// the caller's lock.
func completeClaimShareLocked(
	ctx context.Context,
	username, nonceHex string,
	signature []byte,
	callerCertThumbprint string,
	registry *OfficerRegistry,
	store *sealedShareStore,
	pendingCerts map[string]*PendingCert,
	challenges map[string]*ChallengeResponse,
	challengePrefix string,
) (*ShareClaimResult, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}

	challengeKey := challengePrefix + username
	challenge, ok := challenges[challengeKey]
	if !ok {
		return nil, ErrChallengeNotFound
	}

	if time.Now().After(challenge.ExpiresAt) {
		delete(challenges, challengeKey)
		return nil, ErrChallengeExpired
	}

	// Decode and compare nonce.
	providedNonce, err := hex.DecodeString(nonceHex)
	if err != nil || len(providedNonce) != len(challenge.Nonce) {
		return nil, ErrChallengeVerificationFailed
	}
	for i := range providedNonce {
		if providedNonce[i] != challenge.Nonce[i] {
			return nil, ErrChallengeVerificationFailed
		}
	}

	// Thumbprint check.
	if registry == nil {
		return nil, ErrOfficerNotFound
	}
	officer, err := registry.Load(ctx, username)
	if err != nil {
		return nil, ErrOfficerNotFound
	}
	if callerCertThumbprint != officer.CertThumbprint {
		delete(challenges, challengeKey)
		return nil, ErrCertThumbprintMismatch
	}

	// Verify signature using the officer's registered public key.
	pubKey, parseErr := parsePublicKeyPEM(officer.PubKeyPEM)
	if parseErr != nil {
		delete(challenges, challengeKey)
		return nil, ErrChallengeVerificationFailed
	}
	if sigErr := verifyNonceSignature(pubKey, challenge.Nonce, signature); sigErr != nil {
		delete(challenges, challengeKey)
		return nil, sigErr
	}

	// Claim the share exactly once.
	//secure:unwiped the claimed share is handed to the officer in ShareClaimResult, and the transport response owns it
	shareBytes, claimErr := store.Claim(username)
	if claimErr != nil {
		delete(challenges, challengeKey)
		return nil, claimErr
	}

	delete(challenges, challengeKey)

	// Mark the pendingCert as claimed if it exists (system ceremony parity).
	if pc, exists := pendingCerts[username]; exists {
		pc.Claimed = true
	}

	return &ShareClaimResult{
		ShareIndex: officer.ShareIndex,
		ShareBytes: shareBytes,
	}, nil
}

// officerStatusFromRegistry builds an OfficerStatus slice from the registry
// cache, with SharePending set from the share store.
func officerStatusFromRegistry(registry *OfficerRegistry, store *sealedShareStore) []*OfficerStatus {
	officers := registry.Cached()
	out := make([]*OfficerStatus, 0, len(officers))
	for _, o := range officers {
		out = append(out, &OfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     o.ShareIndex,
			AuthFactor:     o.AuthFactor,
			EnrolledAt:     o.EnrolledAt,
			ClaimedAt:      o.ClaimedAt,
			SharePending:   store.Exists(o.Username) && !store.Claimed(o.Username),
		})
	}
	return out
}

// nextShareIndex returns the next sequential 1-based share index by inspecting
// the officer registry cache and pendingCerts. Must be called under s.mu.
func (s *CeremonyService) nextShareIndex() int {
	max := 0
	if s.officerRegistry != nil {
		for _, o := range s.officerRegistry.Cached() {
			if o.ShareIndex > max {
				max = o.ShareIndex
			}
		}
	}
	// Also count any in pendingCerts not yet in registry (first-boot race).
	for range s.pendingCerts {
		// pendingCerts doesn't carry ShareIndex, so we rely solely on the registry.
	}
	return max + 1
}

// ensureOfficerRegistry lazily initialises the officer registry if it has not
// yet been created. Must be called under s.mu.
func (s *CeremonyService) ensureOfficerRegistry() error {
	if s.officerRegistry != nil {
		return nil
	}
	reg, err := NewOfficerRegistry(s.barrier)
	if err != nil {
		return err
	}
	s.officerRegistry = reg
	return nil
}

// wipeString was removed here (go-qrdb#46). It read:
//
//	func wipeString(s *string) {
//		b := []byte(*s)
//		secure.Wipe(b)
//		*s = ""
//	}
//
// and it was documented as "best-effort ... clears the most likely copy". It
// cleared no copy that existed before the call: []byte(*s) ALLOCATES a new
// array and copies into it, so secure.Wipe zeroed a buffer the function had
// just created, and the string's own backing bytes were left intact for the
// collector. Assigning "" only drops a reference.
//
// The share values it was meant to protect now live in ProtectedShare
// enclaves, which Destroy genuinely wipes. There is no correct wipeString to
// write for a Go string, which is precisely why go-quicraft deprecated the
// string-valued share API.
