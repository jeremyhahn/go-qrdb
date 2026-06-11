// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// errRekeyDistributor wraps a captureDistributor but fails RekeyAndDistribute,
// simulating a ceremony distribution failure mid-rekey.
type errRekeyDistributor struct {
	*captureDistributor
	rekeyErr error
}

func (e *errRekeyDistributor) RekeyAndDistribute(_ context.Context, _ []byte, _, _ int) (int, error) {
	return 0, e.rekeyErr
}

// newUnsealKeyBarrier creates an initialized 3-of-5 Shamir direct-mode
// barrier with a captureDistributor and returns it with the backend,
// distributor, and the initial distributed share set.
func newUnsealKeyBarrier(t *testing.T) (*Barrier, *MemoryBackend, *captureDistributor, []string) {
	t.Helper()
	b, backend, dist := newRekeyBarrier(t)
	if _, err := b.InitializeShamir(context.Background(), Credentials{}); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	shares := dist.Shares()
	if len(shares) != 5 {
		t.Fatalf("distributed shares: got %d, want 5", len(shares))
	}
	return b, backend, dist, shares
}

func TestUnsealKeyInit(t *testing.T) {
	t.Run("WrappedBlobPersistedAtSiblingPath", func(t *testing.T) {
		b, backend, _, _ := newUnsealKeyBarrier(t)
		ctx := context.Background()

		wantPath := b.config.RootKeyPath + wrappedRootKeyPathSuffix
		if got := b.wrappedRootKeyPath(); got != wantPath {
			t.Fatalf("wrappedRootKeyPath: got %q, want %q", got, wantPath)
		}
		exists, err := backend.Exists(ctx, wantPath)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatal("wrapped root key blob not persisted after InitializeShamir")
		}

		blob, err := backend.Get(ctx, wantPath)
		if err != nil {
			t.Fatal(err)
		}
		var wrapped wrappedRootKey
		if err := json.Unmarshal(blob, &wrapped); err != nil {
			t.Fatalf("wrapped blob is not valid JSON: %v", err)
		}
		if wrapped.Version != wrappedRootKeyVersion {
			t.Errorf("Version: got %d, want %d", wrapped.Version, wrappedRootKeyVersion)
		}
		if SymmetricAlgorithm(wrapped.Algorithm) != AlgAES256GCM {
			t.Errorf("Algorithm: got %d, want %d", wrapped.Algorithm, AlgAES256GCM)
		}
		if len(wrapped.Ciphertext) == 0 {
			t.Error("Ciphertext is empty")
		}
	})

	t.Run("DistributedSecretIsUnsealKeyNotRootKey", func(t *testing.T) {
		b, _, _, shares := newUnsealKeyBarrier(t)

		unsealKey := combineShareValues(t, shares[:3], 3, 5)
		if len(unsealKey) != unsealKeySize {
			t.Fatalf("reconstructed unseal key size: got %d, want %d", len(unsealKey), unsealKeySize)
		}

		rootKey, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(unsealKey, rootKey) {
			t.Fatal("distributed secret equals the root key; expected an independent unseal key")
		}
	})

	t.Run("UnsealKeyDecryptsWrappedBlobToRootKey", func(t *testing.T) {
		b, _, _, shares := newUnsealKeyBarrier(t)
		ctx := context.Background()

		unsealKey := combineShareValues(t, shares[:3], 3, 5)
		rootKey, err := b.resolveDirectSecret(ctx, unsealKey)
		if err != nil {
			t.Fatalf("resolveDirectSecret with valid unseal key: %v", err)
		}
		masterKey, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rootKey, masterKey) {
			t.Fatal("unwrapped root key does not match the barrier master key")
		}
	})
}

func TestUnsealKeyRekeyRevocation(t *testing.T) {
	t.Run("PriorRoundSharesRevokedAfterRekey", func(t *testing.T) {
		b, _, dist, round1 := newUnsealKeyBarrier(t)
		ctx := context.Background()

		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatalf("Rekey: %v", err)
		}
		round2 := dist.Shares()

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Round-1 shares reconstruct the old unseal key, which no longer
		// decrypts the re-wrapped root key.
		err := b.UnsealWithShares(ctx, round1[:3])
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("UnsealWithShares with revoked shares: got %v, want ErrUnsealKeyMismatch", err)
		}
		if !b.IsSealed() {
			t.Fatal("barrier unsealed with revoked shares")
		}

		// Round-2 shares unseal successfully.
		if err := b.UnsealWithShares(ctx, round2[:3]); err != nil {
			t.Fatalf("UnsealWithShares with current shares: %v", err)
		}
		if b.IsSealed() {
			t.Fatal("barrier still sealed after valid quorum")
		}
	})

	t.Run("RootKeyUnchangedAcrossRekey", func(t *testing.T) {
		b, _, _, _ := newUnsealKeyBarrier(t)
		ctx := context.Background()

		before, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		after, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("root key rotated during Rekey; it must remain stable (epoch KDF)")
		}
	})

	t.Run("DistributionFailureLeavesOldSharesValid", func(t *testing.T) {
		b, backend, dist, round1 := newUnsealKeyBarrier(t)
		ctx := context.Background()

		blobBefore, err := backend.Get(ctx, b.wrappedRootKeyPath())
		if err != nil {
			t.Fatal(err)
		}

		wantErr := errors.New("ceremony distribution unavailable")
		b.SetCeremonyDistributor(&errRekeyDistributor{captureDistributor: dist, rekeyErr: wantErr})

		if _, err := b.Rekey(ctx, 3, 5); !errors.Is(err, wantErr) {
			t.Fatalf("Rekey with failing distributor: got %v, want %v", err, wantErr)
		}

		// The wrapped blob must be untouched: distribute-first / persist-last.
		blobAfter, err := backend.Get(ctx, b.wrappedRootKeyPath())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(blobBefore, blobAfter) {
			t.Fatal("wrapped blob replaced despite distribution failure; risks lockout")
		}

		// Old shares still unseal.
		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if err := b.UnsealWithShares(ctx, round1[:3]); err != nil {
			t.Fatalf("UnsealWithShares after failed rekey: %v", err)
		}
	})
}

func TestUnsealKeyLegacyMigration(t *testing.T) {
	// Simulate a barrier initialized before unseal-key indirection: the
	// distributed shares carry the root key itself and no wrapped blob exists.
	newLegacyBarrier := func(t *testing.T) (*Barrier, *MemoryBackend, *captureDistributor, []string) {
		t.Helper()
		b, backend, dist, _ := newUnsealKeyBarrier(t)
		ctx := context.Background()

		if err := backend.Delete(ctx, b.wrappedRootKeyPath()); err != nil {
			t.Fatal(err)
		}
		rootKey, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := quicraftShamir.Split(rootKey, 3, 5)
		if err != nil {
			t.Fatal(err)
		}
		legacy := make([]string, len(raw))
		for i, s := range raw {
			legacy[i] = s.Value
		}
		return b, backend, dist, legacy
	}

	t.Run("LegacyRootKeySharesStillUnseal", func(t *testing.T) {
		b, _, _, legacy := newLegacyBarrier(t)
		ctx := context.Background()

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}
		if err := b.UnsealWithShares(ctx, legacy[:3]); err != nil {
			t.Fatalf("UnsealWithShares on legacy barrier: %v", err)
		}
		if b.IsSealed() {
			t.Fatal("legacy barrier still sealed after valid root-key quorum")
		}
	})

	t.Run("FirstRekeyUpgradesAndRevokesLegacyShares", func(t *testing.T) {
		b, backend, dist, legacy := newLegacyBarrier(t)
		ctx := context.Background()

		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatalf("Rekey on legacy barrier: %v", err)
		}

		// Upgrade: the first wrapped blob must now exist.
		exists, err := backend.Exists(ctx, b.wrappedRootKeyPath())
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatal("first Rekey did not write the wrapped root key blob")
		}

		if err := b.Seal(); err != nil {
			t.Fatal(err)
		}

		// Legacy root-key shares are dead: the 64-byte hex-decoded root key
		// is not a valid AES-256 unseal key (or fails the AEAD open).
		err = b.UnsealWithShares(ctx, legacy[:3])
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("legacy shares after upgrade: got %v, want ErrUnsealKeyMismatch", err)
		}

		// The fresh unseal-key shares work.
		if err := b.UnsealWithShares(ctx, dist.Shares()[:3]); err != nil {
			t.Fatalf("UnsealWithShares with post-upgrade shares: %v", err)
		}
	})
}

func TestResolveDirectSecretCorruption(t *testing.T) {
	corrupt := func(t *testing.T, mutate func(w *wrappedRootKey) []byte) error {
		t.Helper()
		b, backend, _, shares := newUnsealKeyBarrier(t)
		ctx := context.Background()

		blob, err := backend.Get(ctx, b.wrappedRootKeyPath())
		if err != nil {
			t.Fatal(err)
		}
		var wrapped wrappedRootKey
		if err := json.Unmarshal(blob, &wrapped); err != nil {
			t.Fatal(err)
		}
		if err := backend.Put(ctx, b.wrappedRootKeyPath(), mutate(&wrapped)); err != nil {
			t.Fatal(err)
		}

		unsealKey := combineShareValues(t, shares[:3], 3, 5)
		_, resolveErr := b.resolveDirectSecret(ctx, unsealKey)
		return resolveErr
	}

	remarshal := func(t *testing.T, w *wrappedRootKey) []byte {
		t.Helper()
		out, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("InvalidJSON", func(t *testing.T) {
		err := corrupt(t, func(_ *wrappedRootKey) []byte {
			return []byte("{not json")
		})
		if !errors.Is(err, ErrWrappedRootKeyCorrupt) {
			t.Fatalf("got %v, want ErrWrappedRootKeyCorrupt", err)
		}
	})

	t.Run("WrongVersion", func(t *testing.T) {
		err := corrupt(t, func(w *wrappedRootKey) []byte {
			w.Version = 99
			return remarshal(t, w)
		})
		if !errors.Is(err, ErrWrappedRootKeyCorrupt) {
			t.Fatalf("got %v, want ErrWrappedRootKeyCorrupt", err)
		}
	})

	t.Run("WrongAlgorithm", func(t *testing.T) {
		err := corrupt(t, func(w *wrappedRootKey) []byte {
			w.Algorithm = 0x7f
			return remarshal(t, w)
		})
		if !errors.Is(err, ErrWrappedRootKeyCorrupt) {
			t.Fatalf("got %v, want ErrWrappedRootKeyCorrupt", err)
		}
	})

	t.Run("WrongNonceLength", func(t *testing.T) {
		err := corrupt(t, func(w *wrappedRootKey) []byte {
			w.Nonce = w.Nonce[:len(w.Nonce)-1]
			return remarshal(t, w)
		})
		if !errors.Is(err, ErrWrappedRootKeyCorrupt) {
			t.Fatalf("got %v, want ErrWrappedRootKeyCorrupt", err)
		}
	})

	t.Run("TamperedCiphertext", func(t *testing.T) {
		err := corrupt(t, func(w *wrappedRootKey) []byte {
			w.Ciphertext[0] ^= 0xff
			return remarshal(t, w)
		})
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("got %v, want ErrUnsealKeyMismatch", err)
		}
	})
}

func TestResolveDirectSecretMismatch(t *testing.T) {
	t.Run("WrongLengthSecret", func(t *testing.T) {
		b, _, _, _ := newUnsealKeyBarrier(t)

		_, err := b.resolveDirectSecret(context.Background(), []byte("short"))
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("got %v, want ErrUnsealKeyMismatch", err)
		}
	})

	t.Run("WrongKeyRightLength", func(t *testing.T) {
		b, _, _, _ := newUnsealKeyBarrier(t)

		wrong := make([]byte, unsealKeySize)
		_, err := b.resolveDirectSecret(context.Background(), wrong)
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("got %v, want ErrUnsealKeyMismatch", err)
		}
	})

	t.Run("AADBindsBlobToRootKeyPath", func(t *testing.T) {
		b, backend, _, shares := newUnsealKeyBarrier(t)
		ctx := context.Background()

		// Transplant the wrapped blob to a barrier with a different
		// RootKeyPath sharing the same backend. The AAD path binding must
		// reject the decrypt even with the correct unseal key.
		blob, err := backend.Get(ctx, b.wrappedRootKeyPath())
		if err != nil {
			t.Fatal(err)
		}

		shamirStrat, err := NewShamirStrategy(3, 5)
		if err != nil {
			t.Fatal(err)
		}
		other, err := NewBarrier(testLogger(), backend, BarrierConfig{
			RootKeyPath:     "other/root-key",
			Shamir:          &ShamirConfig{Threshold: 3, TotalShares: 5},
			PreferenceOrder: []StrategyID{StrategyShamir},
		}, testSoftwareStrategy(t), shamirStrat)
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.Put(ctx, other.wrappedRootKeyPath(), blob); err != nil {
			t.Fatal(err)
		}

		unsealKey := combineShareValues(t, shares[:3], 3, 5)
		_, err = other.resolveDirectSecret(ctx, unsealKey)
		if !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("transplanted blob: got %v, want ErrUnsealKeyMismatch", err)
		}
	})
}

func TestUnsealKeySealVoteAndRootToken(t *testing.T) {
	t.Run("SealVoteRejectsRevokedShares", func(t *testing.T) {
		b, _, _, round1 := newUnsealKeyBarrier(t)
		ctx := context.Background()

		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}

		// Submit a quorum of revoked round-1 shares as seal votes; the
		// final vote triggers verification, which must fail.
		var lastErr error
		for _, s := range round1[:3] {
			_, lastErr = b.SubmitSealVote(ctx, s)
			if lastErr != nil {
				break
			}
		}
		if !errors.Is(lastErr, ErrSealVoteVerificationFailed) {
			t.Fatalf("seal vote with revoked shares: got %v, want ErrSealVoteVerificationFailed", lastErr)
		}
		if b.IsSealed() {
			t.Fatal("barrier sealed by revoked shares")
		}
	})

	t.Run("SealVoteAcceptsCurrentShares", func(t *testing.T) {
		b, _, dist, _ := newUnsealKeyBarrier(t)
		ctx := context.Background()

		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}
		round2 := dist.Shares()

		var prog *QuorumProgress
		var err error
		for _, s := range round2[:3] {
			prog, err = b.SubmitSealVote(ctx, s)
			if err != nil {
				t.Fatalf("SubmitSealVote: %v", err)
			}
		}
		if prog == nil || !prog.Complete {
			t.Fatalf("seal-vote quorum not complete: %+v", prog)
		}
		if !b.IsSealed() {
			t.Fatal("barrier not sealed after valid seal-vote quorum")
		}
	})

	t.Run("RootTokenRejectsRevokedSharesAcceptsCurrent", func(t *testing.T) {
		b, _, dist, round1 := newUnsealKeyBarrier(t)
		ctx := context.Background()

		if _, err := b.Rekey(ctx, 3, 5); err != nil {
			t.Fatal(err)
		}

		if _, err := b.GenerateRootToken(ctx, round1[:3]); !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Fatalf("GenerateRootToken with revoked shares: got %v, want ErrRootTokenVerificationFailed", err)
		}

		token, err := b.GenerateRootToken(ctx, dist.Shares()[:3])
		if err != nil {
			t.Fatalf("GenerateRootToken with current shares: %v", err)
		}
		if token == nil || token.Token == "" {
			t.Fatal("GenerateRootToken returned an empty token")
		}
	})
}
