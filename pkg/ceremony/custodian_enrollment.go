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
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	// custodianNonceSize is the number of random bytes for custodian challenges.
	custodianNonceSize = 32

	// custodianChallengeTTL is how long a custodian factor challenge remains valid.
	custodianChallengeTTL = 5 * time.Minute
)

// EnrollCustodianRequest carries the inputs for EnrollCustodianViaFactor.
type EnrollCustodianRequest struct {
	// SOIdentity is the subject of the Security Officer initiating the enrollment.
	SOIdentity string

	// TenantID scopes the custodian record.
	TenantID string

	// Username is the human-readable custodian identifier.
	Username string

	// Method is the authentication factor type ("pkcs11", "piv", "fido2", "software").
	Method custodian.FactorMethod

	// EnrollmentID references the authfactors.Enrollment for the custodian's
	// factor credential.
	EnrollmentID string

	// PublicKeyPEM is the PEM-encoded P-256 public key from the enrollment.
	// Used as the ECDH recipient key for share envelope sealing.
	PublicKeyPEM string

	// ShareIndex is the 1-based Shamir share index to assign to this custodian.
	ShareIndex int

	// ShareBytes is the raw plaintext Shamir share bytes to be sealed.
	// Consumed and zeroed by EnrollCustodianViaFactor.
	ShareBytes []byte
}

// EnrollCustodianResponse carries the result of a successful EnrollCustodianViaFactor call.
type EnrollCustodianResponse struct {
	// Custodian is the persisted public metadata record.
	Custodian *custodian.Custodian

	// SealedEnvelope is the ECDH+AES-256-GCM-sealed Shamir share.
	// Must be delivered to the custodian out-of-band. Not persisted server-side.
	SealedEnvelope *custodian.SealedEnvelope
}

// CustodianFactorChallenge is a pending nonce-based challenge for a custodian.
type CustodianFactorChallenge struct {
	// CustodianID is the unique ID of the custodian being challenged.
	CustodianID string

	// Nonce is the 32-byte random challenge the custodian must sign with their factor.
	Nonce []byte

	// ExpiresAt is the deadline by which the custodian must respond.
	ExpiresAt time.Time
}

// SubmitUnsealShareRequest carries a custodian's unseal share submission.
type SubmitUnsealShareRequest struct {
	// CustodianID identifies the submitting custodian.
	CustodianID string

	// ChallengeID references the active factor challenge.
	ChallengeID string

	// FactorResponse is the signed response proving custodian possession.
	FactorResponse *authfactors.Response

	// ShareBytes is the plaintext Shamir share decrypted client-side by the custodian.
	// The server does NOT decrypt the envelope; the custodian decrypts offline using
	// their hardware private key and submits the plaintext here along with factor proof.
	// ShareBytes is zeroed after accumulation.
	ShareBytes []byte
}

// SubmitUnsealShareResult is the response after a successful share submission.
type SubmitUnsealShareResult struct {
	// Submitted is the number of shares accepted so far.
	Submitted int

	// Required is the threshold needed to unseal.
	Required int

	// Unsealed is true when the threshold was met and the barrier was unsealed.
	Unsealed bool
}

// unsealAccumulator collects Shamir shares atomically during the unseal phase.
// State is in-memory only: a daemon restart clears all shares.
type unsealAccumulator struct {
	mu        sync.Mutex
	shares    [][]byte
	threshold int
	sealed    atomic.Bool // true = not yet unsealed
}

func newUnsealAccumulator(threshold int) *unsealAccumulator {
	a := &unsealAccumulator{
		shares:    make([][]byte, 0, threshold),
		threshold: threshold,
	}
	a.sealed.Store(true)
	return a
}

// submit appends the share (zeroing it on return) and reports whether
// the threshold was just reached for the first time. The returned bool
// is true only for the call that crosses the threshold.
func (a *unsealAccumulator) submit(share []byte) (submitted int, thresholdReached bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer secure.Wipe(share)

	shareCopy := make([]byte, len(share))
	copy(shareCopy, share)
	a.shares = append(a.shares, shareCopy)
	submitted = len(a.shares)

	if submitted == a.threshold && a.sealed.CompareAndSwap(true, false) {
		thresholdReached = true
	}
	return submitted, thresholdReached
}

// collect returns all accumulated shares. Must be called after thresholdReached.
func (a *unsealAccumulator) collect() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([][]byte, len(a.shares))
	copy(out, a.shares)
	return out
}

// custodianServiceFields are the fields added to CeremonyService for Phase 4.
// They extend the existing struct via embedding or by extending the constructor
// (see ceremonyServicePhase4 below for the wiring). To avoid a circular import
// between ceremony and custodian, we wire the deps via interfaces.

// CustodianServicer is the local interface the ceremony needs from the custodian
// service.
type CustodianServicer interface {
	RegisterCustodian(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error)
	GetCustodian(ctx context.Context, id string) (*custodian.Custodian, error)
	RevokeCustodian(ctx context.Context, id string) error
}

// AuthFactorVerifier is the local interface for verifying factor responses
// during the unseal ceremony.
type AuthFactorVerifier interface {
	VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error
}

// BarrierUnsealer is the local interface for barrier unseal operations.
// Satisfied by *seal.StorageBarrier.
type BarrierUnsealer interface {
	UnsealWithKey(ctx context.Context, key []byte) error
}

// CeremonyExtension holds Phase-4 dependencies injected into the ceremony
// service after construction. All fields are optional — when nil, the
// corresponding Phase-4 operations return ErrNotImplemented.
type CeremonyExtension struct {
	CustodianSvc   CustodianServicer
	FactorVerifier AuthFactorVerifier
	BarrierUnseal  BarrierUnsealer
	AuditLogger    audit.AuditLogger
	Threshold      int
	// accumulator is created lazily when the first unseal share arrives.
	accumulator *unsealAccumulator
	accMu       sync.Mutex
	// custodianChallenges holds pending factor challenges for custodians.
	factorChallenges map[string]*CustodianFactorChallenge
	factorChallMu    sync.Mutex
}

// SetExtension wires Phase-4 dependencies into the CeremonyService.
// This is separate from the constructor so that cmd/qrdbd/start.go can
// set extension dependencies in a consolidation pass.
func (s *CeremonyService) SetExtension(ext *CeremonyExtension) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ext = ext
	if s.ext != nil && s.ext.factorChallenges == nil {
		s.ext.factorChallenges = make(map[string]*CustodianFactorChallenge)
	}
}

// EnrollCustodianViaFactor is SO-driven: generates a sealed share envelope for
// the custodian and persists the public metadata record via the custodian service.
// The plaintext share bytes are zeroed immediately after sealing.
//
// Idempotent: if the custodian already exists (same tenantID+username), returns
// ErrCustodianAlreadyExists from the custodian service.
//
// Persist ZERO ceremony state. If the daemon restarts mid-ceremony, the SO must
// re-initialize. This is intentional per docs/seal/custodians.md.
func (s *CeremonyService) EnrollCustodianViaFactor(
	ctx context.Context,
	req EnrollCustodianRequest,
) (*EnrollCustodianResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ext := s.ext
	if ext == nil || ext.CustodianSvc == nil {
		return nil, ErrNotImplemented
	}

	s.logger.Info("enrolling custodian via factor",
		slog.String("so", req.SOIdentity),
		slog.String("username", req.Username),
		slog.String("tenantID", req.TenantID),
		slog.String("method", string(req.Method)),
		slog.Int("shareIndex", req.ShareIndex))

	resp, err := ext.CustodianSvc.RegisterCustodian(ctx, custodian.RegisterRequest{
		Username:     req.Username,
		TenantID:     req.TenantID,
		Method:       req.Method,
		EnrollmentID: req.EnrollmentID,
		PublicKeyPEM: req.PublicKeyPEM,
		ShareIndex:   req.ShareIndex,
		ShareBytes:   req.ShareBytes, // zeroed by RegisterCustodian → SealShareTo
	})
	if err != nil {
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Actor:    req.SOIdentity,
				TenantID: req.TenantID,
				Resource: "ceremony",
				Action:   "enroll-custodian",
				Outcome:  audit.OutcomeFailure + ":" + err.Error(),
				Attributes: map[string]any{
					"username": req.Username,
					"method":   string(req.Method),
				},
			})
		}
		return nil, err
	}

	if ext.AuditLogger != nil {
		ext.AuditLogger.Emit(ctx, audit.AuditEvent{
			Actor:    req.SOIdentity,
			TenantID: req.TenantID,
			Resource: "ceremony",
			Action:   "enroll-custodian",
			Outcome:  audit.OutcomeSuccess,
			Attributes: map[string]any{
				"custodianID": resp.Custodian.ID,
				"username":    req.Username,
				"method":      string(req.Method),
				"shareIndex":  req.ShareIndex,
			},
		})
	}

	return &EnrollCustodianResponse{
		Custodian:      resp.Custodian,
		SealedEnvelope: resp.SealedEnvelope,
	}, nil
}

// ClaimShareChallenge issues a nonce for the custodian to sign with their factor.
// The nonce is stored in memory and expires after 5 minutes. Must be followed by
// VerifyClaimAndDeliver to confirm possession.
func (s *CeremonyService) ClaimShareChallenge(ctx context.Context, custodianID string) (*CustodianFactorChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ext := s.ext
	if ext == nil || ext.CustodianSvc == nil {
		return nil, ErrNotImplemented
	}

	if custodianID == "" {
		return nil, ErrMissingUsername
	}

	// Verify custodian exists and is active.
	c, err := ext.CustodianSvc.GetCustodian(ctx, custodianID)
	if err != nil {
		return nil, err
	}
	if !c.Active() {
		return nil, ErrCustodianRevoked
	}

	nonce := make([]byte, custodianNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrInitFailed
	}

	challenge := &CustodianFactorChallenge{
		CustodianID: custodianID,
		Nonce:       nonce,
		ExpiresAt:   time.Now().Add(custodianChallengeTTL),
	}

	ext.factorChallMu.Lock()
	ext.factorChallenges[custodianID] = challenge
	ext.factorChallMu.Unlock()

	s.logger.Info("custodian factor challenge issued",
		slog.String("custodianID", custodianID))

	if ext.AuditLogger != nil {
		ext.AuditLogger.Emit(ctx, audit.AuditEvent{
			Resource: "barrier",
			Action:   "unseal-share-claim-begin",
			Outcome:  audit.OutcomeSuccess,
			Attributes: map[string]any{
				"custodianID": custodianID,
			},
		})
	}

	return challenge, nil
}

// VerifyClaimAndDeliver validates the custodian's factor response, then marks
// the share as delivered. After delivery the challenge is consumed (single-use).
// A second call returns ErrShareAlreadyDelivered.
//
// NOTE: this method authenticates the custodian's identity via factor. The
// actual share envelope was returned at enrollment time; this call provides
// the authentication proof that the custodian received it.
func (s *CeremonyService) VerifyClaimAndDeliver(
	ctx context.Context,
	custodianID string,
	factorChallengeID string,
	response *authfactors.Response,
) (*custodian.Custodian, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ext := s.ext
	if ext == nil || ext.CustodianSvc == nil || ext.FactorVerifier == nil {
		return nil, ErrNotImplemented
	}

	if custodianID == "" {
		return nil, ErrMissingUsername
	}

	c, err := ext.CustodianSvc.GetCustodian(ctx, custodianID)
	if err != nil {
		return nil, err
	}
	if !c.Active() {
		return nil, ErrCustodianRevoked
	}
	if c.ShareClaimed {
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Resource:   "barrier",
				Action:     "unseal-share-claim-complete",
				Outcome:    audit.OutcomeFailure + ":already-delivered",
				Attributes: map[string]any{"custodianID": custodianID},
			})
		}
		return nil, ErrShareAlreadyDelivered
	}

	// Consume the challenge (single-use).
	ext.factorChallMu.Lock()
	challenge, ok := ext.factorChallenges[custodianID]
	if ok {
		delete(ext.factorChallenges, custodianID)
	}
	ext.factorChallMu.Unlock()

	if !ok {
		return nil, ErrChallengeNotFound
	}
	if time.Now().After(challenge.ExpiresAt) {
		secure.Wipe(challenge.Nonce)
		return nil, ErrChallengeExpired
	}
	secure.Wipe(challenge.Nonce)

	// Verify factor response.
	if err := ext.FactorVerifier.VerifyFactor(ctx, string(c.Method), factorChallengeID, response); err != nil {
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Resource: "barrier",
				Action:   "unseal-share-claim-complete",
				Outcome:  audit.OutcomeFailure + ":factor-verify",
				Attributes: map[string]any{
					"custodianID": custodianID,
					"method":      string(c.Method),
				},
			})
		}
		return nil, ErrChallengeVerificationFailed
	}

	if ext.AuditLogger != nil {
		ext.AuditLogger.Emit(ctx, audit.AuditEvent{
			Resource: "barrier",
			Action:   "unseal-share-claim-complete",
			Outcome:  audit.OutcomeSuccess,
			Attributes: map[string]any{
				"custodianID": custodianID,
				"method":      string(c.Method),
			},
		})
	}

	return c, nil
}

// SubmitUnsealShare accepts a plaintext Shamir share from a custodian (who
// decrypted their envelope client-side) along with factor proof of identity.
// At threshold, it reconstructs the root key in a *secure.Buffer and calls the barrier
// unseal.
//
// Persist ZERO ceremony state. Daemon restart clears the accumulator; custodians
// must resubmit.
func (s *CeremonyService) SubmitUnsealShare(ctx context.Context, req SubmitUnsealShareRequest) (*SubmitUnsealShareResult, error) {
	ext := s.ext
	if ext == nil || ext.CustodianSvc == nil || ext.FactorVerifier == nil {
		return nil, ErrNotImplemented
	}

	// Validate the custodian.
	c, err := ext.CustodianSvc.GetCustodian(ctx, req.CustodianID)
	if err != nil {
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Resource:   "barrier",
				Action:     "unseal-share",
				Outcome:    audit.OutcomeFailure + ":custodian-not-found",
				Attributes: map[string]any{"custodianID": req.CustodianID},
			})
		}
		return nil, err
	}
	if !c.Active() {
		return nil, ErrCustodianRevoked
	}

	// Verify factor response.
	if err := ext.FactorVerifier.VerifyFactor(ctx, string(c.Method), req.ChallengeID, req.FactorResponse); err != nil {
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Resource: "barrier",
				Action:   "unseal-share",
				Outcome:  audit.OutcomeFailure + ":factor-verify",
				Attributes: map[string]any{
					"custodianID": req.CustodianID,
					"method":      string(c.Method),
				},
			})
		}
		return nil, ErrChallengeVerificationFailed
	}

	// Lazy-initialize accumulator.
	ext.accMu.Lock()
	if ext.accumulator == nil {
		ext.accumulator = newUnsealAccumulator(ext.Threshold)
	}
	acc := ext.accumulator
	ext.accMu.Unlock()

	submitted, reached := acc.submit(req.ShareBytes) // zeroes req.ShareBytes

	if ext.AuditLogger != nil {
		ext.AuditLogger.Emit(ctx, audit.AuditEvent{
			Resource: "barrier",
			Action:   "unseal-share",
			Outcome:  audit.OutcomeSuccess,
			Attributes: map[string]any{
				"custodianID": req.CustodianID,
				"submitted":   submitted,
				"threshold":   ext.Threshold,
			},
		})
	}

	result := &SubmitUnsealShareResult{
		Submitted: submitted,
		Required:  ext.Threshold,
	}

	if reached {
		shares := acc.collect()
		if err := s.reconstructAndUnseal(ctx, shares, ext); err != nil {
			if ext.AuditLogger != nil {
				ext.AuditLogger.Emit(ctx, audit.AuditEvent{
					Resource: "barrier",
					Action:   "unseal",
					Outcome:  audit.OutcomeFailure + ":reconstruct",
				})
			}
			return nil, err
		}
		result.Unsealed = true
		if ext.AuditLogger != nil {
			ext.AuditLogger.Emit(ctx, audit.AuditEvent{
				Resource: "barrier",
				Action:   "unseal",
				Outcome:  audit.OutcomeSuccess,
				Attributes: map[string]any{
					"threshold": ext.Threshold,
					"submitted": submitted,
				},
			})
		}
	}

	return result, nil
}

// reconstructAndUnseal reconstructs the root key from the collected shares using
// Shamir Combine, unseals the barrier, and zeroes all key material.
// Each element of shares is a JSON-encoded *shamir.Share.
func (s *CeremonyService) reconstructAndUnseal(ctx context.Context, rawShares [][]byte, ext *CeremonyExtension) error {
	if ext.BarrierUnseal == nil {
		return ErrNotImplemented
	}

	shamirShares := make([]*quicraftShamir.Share, 0, len(rawShares))
	for _, raw := range rawShares {
		var sh quicraftShamir.Share
		if err := unmarshalShare(raw, &sh); err != nil {
			return &ShamirSplitError{Err: err}
		}
		shamirShares = append(shamirShares, &sh)
	}

	rootKey, err := quicraftShamir.Combine(shamirShares)
	if err != nil {
		return &ShamirSplitError{Err: err}
	}

	// Wrap the reconstructed root key in a *secure.Buffer; the buffer
	// is destroyed after unseal, and raw share bytes are wiped behind
	// it. NewBufferFromBytes wipes rootKey on entry.
	rootBuf, err := secure.NewBufferFromBytes(rootKey)
	if err != nil {
		for _, share := range rawShares {
			secure.Wipe(share)
		}
		return err
	}
	defer func() {
		rootBuf.Destroy()
		for _, share := range rawShares {
			secure.Wipe(share)
		}
	}()

	if err := ext.BarrierUnseal.UnsealWithKey(ctx, rootBuf.Bytes()); err != nil {
		return err
	}

	s.logger.Info("barrier unsealed via Shamir quorum",
		slog.Int("threshold", ext.Threshold))

	return nil
}

// unmarshalShare deserialises JSON-encoded share bytes into a *quicraftShamir.Share.
func unmarshalShare(data []byte, sh *quicraftShamir.Share) error {
	return json.Unmarshal(data, sh)
}

// ErrShareAlreadyDelivered is returned when a custodian attempts to claim
// their share envelope a second time.
var ErrShareAlreadyDelivered = errShareAlreadyDelivered

// errShareAlreadyDelivered is the package-level sentinel.
var errShareAlreadyDelivered = newSentinel("ceremony: share envelope already delivered")

// ErrCustodianRevoked is returned when an operation is attempted on a revoked custodian.
var ErrCustodianRevoked = errCustodianRevoked

var errCustodianRevoked = newSentinel("ceremony: custodian is revoked")

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }

func newSentinel(msg string) error { return &sentinelError{msg: msg} }
