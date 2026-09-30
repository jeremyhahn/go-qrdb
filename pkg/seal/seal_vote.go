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

package seal

import (
	"context"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SubmitSealVote submits a Shamir share as a seal vote toward a
// quorum-gated seal. When the threshold is reached the barrier is sealed
// and all vote enclaves are destroyed.
//
// Returns ErrShamirNotConfigured if the barrier is not in Shamir mode.
// Returns ErrSealed if the barrier is already sealed.
// Returns ErrShamirDuplicateShare when the same share is submitted twice.
// Returns ErrShamirQuorumExpired when the vote TTL has elapsed.
// Returns ErrSealVoteVerificationFailed when the reconstructed secret does
// not verify against the barrier's root key; the accumulator is reset so
// the seal round may be retried.
func (b *StorageBarrier) SubmitSealVote(ctx context.Context, shareValue string) (*QuorumProgress, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	if b.state.Load() == stateSealed {
		return nil, ErrSealed
	}

	threshold := b.config.Shamir.Threshold
	total := b.config.Shamir.TotalShares

	ttl := DefaultShareTTL
	if b.config.Shamir.QuorumTTL > 0 {
		ttl = b.config.Shamir.QuorumTTL
	}

	acc := b.loadOrCreateSealVoteAccumulator(threshold, total, ttl)

	// Wrap the operator's share string into protected memory before it enters
	// the accumulator, which then owns and destroys it. Index is metadata
	// CombineProtected ignores (it reads the x-coordinate from the value), so a
	// positional label within [1, total] is sufficient.
	raw := []byte(shareValue)
	share, err := shamir.NewProtectedShare(acc.GetProgress().Submitted+1, threshold, total, raw)
	// Wipe raw unconditionally: NewProtectedShare wipes it on success but leaves
	// it untouched on a validation failure, which would otherwise strand a
	// plaintext copy on the heap.
	secure.Wipe(raw)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "protect share", Cause: err}
	}

	prog, err := acc.AddProtectedShare(ctx, share)
	if err != nil {
		return nil, err
	}

	if !prog.Complete {
		emitAudit(b.config.AuditLogger, "seal-vote", "barrier", b.config.RootKeyPath,
			"allow", map[string]string{
				"submitted": itoa(prog.Submitted),
				"required":  itoa(prog.Required),
			})
		return prog, nil
	}

	// Threshold met: reconstruct the secret into protected memory and verify
	// it against the sealed root key before sealing. This ensures SOs cannot
	// cause a seal with random/garbage shares.
	secretEnclave, err := acc.ReconstructProtected(ctx)
	if err != nil {
		acc.Reset()
		b.clearSealVoteAccumulator()
		return nil, ErrSealVoteVerificationFailed
	}
	defer secretEnclave.Destroy()

	// Copy the reconstructed secret into a protected buffer and verify OUTSIDE
	// the enclave borrow: verifySecretAndGetRootKey performs an AEAD unwrap that
	// allocates protected memory, which deadlocks if run inside WithBytes (see
	// copyEnclaveToBuffer in shamir.go). Reset and reject on failure so the round
	// can be retried with correct shares. The buffer is destroyed and the
	// returned root key wiped immediately — only the verification outcome is used.
	secretBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		acc.Reset()
		b.clearSealVoteAccumulator()
		return nil, ErrSealVoteVerificationFailed
	}
	defer secretBuf.Destroy()

	//secure:unborrowed injected-interface verifySecretAndGetRootKey dispatches to a SealingStrategy (go-qrdb#428)
	rootKey, verifyErr := b.verifySecretAndGetRootKey(ctx, secretBuf.Bytes())
	if verifyErr != nil {
		acc.Reset()
		b.clearSealVoteAccumulator()
		return nil, ErrSealVoteVerificationFailed
	}
	secure.Wipe(rootKey)

	acc.Reset()
	b.clearSealVoteAccumulator()

	emitAudit(b.config.AuditLogger, "seal-vote-quorum-met", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{
			"submitted": itoa(prog.Submitted),
			"required":  itoa(prog.Required),
		})

	b.logger.Info("seal-vote quorum met, sealing barrier",
		slog.Int("required", prog.Required),
		slog.Int("submitted", prog.Submitted))

	if sealErr := b.Seal(); sealErr != nil {
		return nil, sealErr
	}

	return &QuorumProgress{
		Required:  prog.Required,
		Submitted: prog.Submitted,
		Complete:  true,
	}, nil
}

// clearSealVoteAccumulator replaces the current seal-vote accumulator with a
// new expired one, effectively clearing the vote round without storing a nil
// interface value (which would panic in atomic.Value.Store).
// Must NOT hold sealVoteMu when called.
func (b *StorageBarrier) clearSealVoteAccumulator() {
	b.sealVoteMu.Lock()
	defer b.sealVoteMu.Unlock()
	// Store a fresh accumulator whose TTL is zero so it is considered expired
	// immediately, preventing stale vote state from carrying forward.
	empty := NewShamirShareAccumulator(1, 1, 0)
	b.sealVoteAcc.Store(empty)
}

// loadOrCreateSealVoteAccumulator returns the current seal-vote accumulator
// or creates a new one. If the existing accumulator has expired it is
// replaced, mirroring loadOrCreateAccumulator for the unseal path.
func (b *StorageBarrier) loadOrCreateSealVoteAccumulator(threshold, total int, ttl time.Duration) ShareAccumulator {
	b.sealVoteMu.Lock()
	defer b.sealVoteMu.Unlock()

	if acc, ok := b.sealVoteAcc.Load().(ShareAccumulator); ok && acc != nil {
		if !acc.Expired() {
			return acc
		}
		acc.Reset()
	}

	fresh := NewShamirShareAccumulator(threshold, total, ttl)
	b.sealVoteAcc.Store(fresh)
	return fresh
}
