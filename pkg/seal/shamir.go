// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// InitializeShamir initializes the barrier under Shamir custody and
// distributes the unseal key to the enrolled share holders.
//
// The EpochBarrier generates the root key during Initialize; the root key is
// then wrapped under a fresh random unseal key bound to the holder roster (see
// unsealkey.go and holders.go), and the UNSEAL KEY — never the root key — is
// split M-of-N and sealed to the holders through the CeremonyDistributor. No
// share material is returned. The indirection is what lets a rekey revoke
// every earlier share, and the roster binding is what lets a sealed barrier
// authenticate the holders who submit.
//
// A ShamirStrategy must be registered: the old credential-mode split (sharing
// a passphrase instead of a wrapped key) is gone, because it had no wrapped
// blob to bind a roster into.
//
// The threshold and share count come from b.config.Shamir (set through
// SetShamirConfig); the share count must equal the number of enrolled holders.
// policy is the factor policy for the share set; empty selects
// FactorPolicyDesignated.
//
// Initialization is all or nothing. If the distribution fails at any step,
// the root key is discarded and deleted, so storage and status are sealed and
// uninitialized again; a barrier left initialized with a key no holder has a
// share of could never be unsealed. The in-memory epoch barrier cannot leave
// the initialized state within the process, so a retry needs a node restart:
// until then every initialization of this barrier answers
// ErrRestartRequired. Should the rollback itself fail, the error is a
// *ShamirInitRollbackError carrying both failures.
func (b *StorageBarrier) InitializeShamir(ctx context.Context, policy FactorPolicy) (*ShamirInitParams, error) {
	cfg := b.shamirConfig()
	if cfg == nil || b.ShamirStrategy() == nil {
		return nil, ErrShamirNotConfigured
	}
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}
	threshold := cfg.Threshold
	total := cfg.TotalShares
	if threshold < 2 || threshold > total {
		return nil, ErrShamirThresholdInvalid
	}

	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrCorruptRootKey, Cause: err}
	}
	if exists {
		return nil, ErrAlreadyInitialized
	}

	// Validate the holder set BEFORE any key exists, so a short enrolment
	// fails without leaving an initialized-but-undistributed barrier behind.
	roster, err := b.distributor.PrepareRoster(ctx, DomainUnseal, threshold, policy.OrDefault(), nil)
	if err != nil {
		return nil, err
	}
	if roster.Total != total {
		return nil, &HolderCountMismatchError{Want: total, Enrolled: roster.Total}
	}

	// Generate the root key, derive the initial DEK, persist the
	// (key-material-free) Shamir envelope, and leave the barrier unsealed.
	if err := b.initializeShamirKey(ctx); err != nil {
		return nil, err
	}
	rootKey, err := b.masterKeyBuffer(ctx)
	if err != nil {
		return nil, err
	}
	defer rootKey.Destroy()

	params, err := b.redistribute(ctx, DomainUnseal, rootKey, threshold, policy, nil, total)
	if err != nil {
		if rbErr := b.rollbackInitialization(ctx); rbErr != nil {
			return nil, &ShamirInitRollbackError{Distribution: err, Rollback: rbErr}
		}
		return nil, err
	}
	return params, nil
}

// ShamirInitRollbackError is returned by InitializeShamir when the share
// distribution failed and discarding the new root key failed too. The barrier
// may then be left initialized without distributed shares; an operator must
// clear it before initializing again.
type ShamirInitRollbackError struct {
	// Distribution is the error that failed the distribution.
	Distribution error
	// Rollback is the error that failed discarding the root key.
	Rollback error
}

// Error returns a human-readable description.
func (e *ShamirInitRollbackError) Error() string {
	return "qrdb/seal: share distribution failed (" + e.Distribution.Error() +
		") and the new root key could not be discarded: " + e.Rollback.Error()
}

// Unwrap returns both failures, so errors.Is and errors.As match either.
func (e *ShamirInitRollbackError) Unwrap() []error {
	return []error{e.Distribution, e.Rollback}
}

// rollbackInitialization returns a barrier whose initialization did not
// complete to the uninitialized state: it zeroes the in-memory keys, seals,
// forgets the strategy and initialization time, and deletes the persisted
// root key, wrapped unseal key and metadata. It marks the barrier as needing a
// restart before it is initialized again (see ErrRestartRequired).
func (b *StorageBarrier) rollbackInitialization(ctx context.Context) error {
	b.rolledBack.Store(true)
	_ = b.Seal()
	b.activeStrategy.Store(StrategyID(""))
	b.persistedStrategyCache.Store(StrategyID(""))
	b.initializedAt.Store(time.Time{})
	if err := b.DeleteRootKey(ctx); err != nil {
		return err
	}
	if err := b.base.Delete(ctx, b.metadataPath()); err != nil && !isSecretNotFound(err) {
		return err
	}
	b.uninitializedRead.Store(true)
	emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
		"deny", map[string]string{"strategy": string(StrategyShamir), "rolled_back": "true"})
	return nil
}

// masterKeyBuffer copies the in-memory root key of an unsealed barrier into
// protected memory the caller must destroy.
func (b *StorageBarrier) masterKeyBuffer(ctx context.Context) (*secure.Buffer, error) {
	raw, err := b.GetMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	// NewBufferFromBytes wipes raw.
	return secure.NewBufferFromBytes(raw)
}

// combineProtected indirects shamir.CombineProtected so a test can record the
// address of the enclave a quorum reconstructs its key into, and prove that the
// AEADProvider is handed those locked pages rather than a copy of them
// (go-qrdb#145). Production behavior is unchanged — this is a direct copy of the
// function value.
var combineProtected = shamir.CombineProtected

// Compile-time interface check.
var _ ShareSplitter = (*shamirShareSplitter)(nil)

// shamirShareSplitter implements ShareSplitter using go-quicraft's Shamir
// library.
type shamirShareSplitter struct{}

// NewShamirShareSplitter creates a ShareSplitter.
func NewShamirShareSplitter() ShareSplitter {
	return &shamirShareSplitter{}
}

// SplitProtected divides the secret into threshold-of-total Shamir shares whose
// values live in protected memory. The caller owns the returned shares and MUST
// Destroy every one of them, including on its own error paths — each is one
// mlock'd allocation charged against the kernel's RLIMIT_MEMLOCK ceiling, but
// NOT against server.secure_memory_budget_mib: the same BOUNDED EXCEPTION
// documented on SubmitHolderShare's NewProtectedShare call applies here, and
// this call site is reached only from barrier initialization, an SO/operator-
// driven, one-time-per-ceremony path with total bounded by the M-of-N the
// operator configures.
//
// go-quicraft v0.9.0-alpha removed the string-returning Split interface this
// once had to satisfy: splitting into locked memory only to copy each value
// back into an immortal heap string defeated the protection, which is why the
// SA1019 residue this replaces existed (go-quicraft #35/#37).
func (s *shamirShareSplitter) SplitProtected(secret []byte, threshold, total int) ([]*shamir.ProtectedShare, error) {
	shares, err := shamir.SplitProtected(secret, threshold, total)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirSplitFailed, Cause: err}
	}
	return shares, nil
}
