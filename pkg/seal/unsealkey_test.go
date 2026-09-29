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
	"crypto/aes"
	"crypto/cipher"
	"crypto/fips140"
	"encoding/hex"
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
		rootKeyBuf, err := b.resolveDirectSecret(ctx, unsealKey)
		if err != nil {
			t.Fatalf("resolveDirectSecret with valid unseal key: %v", err)
		}
		rootKey := protectedBytes(t, rootKeyBuf)
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

// The wrapped-root-key envelope predates go-quicraft v0.7.0-alpha, which moved
// pkg/seal's AES-GCM to cipher.NewGCMWithRandomNonce: the AEAD now samples its
// own nonce, reports NonceSize() == 0, and prepends the nonce to the
// ciphertext it returns.
//
// The envelope keeps carrying the nonce in its own JSON field, so wrapRootKey
// splits the module-generated prefix back off and resolveDirectSecret
// re-concatenates it. That keeps the persisted bytes IDENTICAL to what the
// pre-v0.7.0 code wrote, which is the whole point: a wrapped root key is the
// one blob whose loss costs the store, and it is written once at initialization
// and read on every unseal for the life of the barrier. A format change here
// would need a migration executed while the barrier is still sealed -- that is,
// while the thing needed to authenticate the migration is unavailable.
//
// legacyWrappedRootKeyBlob was produced by THIS repository at go-quicraft
// v0.6.2-alpha, the pin immediately before the change, and is checked in as
// bytes rather than regenerated so the test cannot drift onto the new format
// and start asserting that the new code reads its own output.
const legacyWrappedRootKeyBlob = `{"version":1,"algorithm":3,"nonce":"ldqVJZV9492DHKT2","ciphertext":"I6vXL9ILPwMKfR67HCO1D6dNgNZ3ood/SC3xzPJROfezBB7oQsEGxNizXHv+qMqa"}`

const (
	// legacyUnsealKeyHex and legacyRootKeyHex are the inputs that produced
	// legacyWrappedRootKeyBlob. Test vectors, never real key material.
	legacyUnsealKeyHex = "2b7e151628aed2a6abf7158809cf4f3c2b7e151628aed2a6abf7158809cf4f3c"
	legacyRootKeyHex   = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	// legacyRootKeyPath is the RootKeyPath the fixture was sealed under. It
	// feeds the AAD, so it is part of the vector.
	legacyRootKeyPath = "core/seal/root-key"

	// legacyNonceLen is the nonce width the fixture actually carries, written
	// as a literal on purpose. Deriving it from AlgAES256GCM.NonceSize() would
	// make these assertions circular -- the implementation reads that same
	// value, so the pair would agree with each other while both drifted off
	// the bytes on disk. 12 is what the format has, measured.
	legacyNonceLen = 12
)

func legacyFixtureBarrier() *StorageBarrier {
	return &StorageBarrier{config: BarrierConfig{RootKeyPath: legacyRootKeyPath}}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex vector: %v", err)
	}
	return b
}

// TestWrappedRootKey_OpensLegacySeparateNonceBlob is the regression that the
// go-quicraft v0.7.0-alpha AEAD change would otherwise have caused silently: a
// barrier initialized before the change must still unseal after it.
func TestWrappedRootKey_OpensLegacySeparateNonceBlob(t *testing.T) {
	var wrapped wrappedRootKey
	if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &wrapped); err != nil {
		t.Fatalf("unmarshal legacy fixture: %v", err)
	}

	// Guard the fixture itself: if these ever stop holding, the constant was
	// regenerated against the new AEAD and the test has stopped testing
	// anything.
	if len(wrapped.Nonce) != legacyNonceLen {
		t.Fatalf("fixture is not in the legacy separate-nonce format: nonce is %d bytes, want %d",
			len(wrapped.Nonce), legacyNonceLen)
	}
	if len(wrapped.Ciphertext) != 48 {
		t.Fatalf("fixture ciphertext is %d bytes, want 48 (32-byte root key + 16-byte tag, no nonce prefix)",
			len(wrapped.Ciphertext))
	}

	b := legacyFixtureBarrier()
	gotBuf, err := b.unwrapRootKey(&wrapped, mustHex(t, legacyUnsealKeyHex))
	if err != nil {
		t.Fatalf("unwrap legacy blob: %v", err)
	}
	got := protectedBytes(t, gotBuf)
	if want := mustHex(t, legacyRootKeyHex); !bytes.Equal(got, want) {
		t.Fatalf("recovered root key = %x, want %x", got, want)
	}
}

// TestWrappedRootKey_WritesLegacyCompatibleFormat asserts the OTHER direction:
// a blob written now has the same shape as the fixture, so a barrier written by
// this code is readable by the field lengths the format has always had. Without
// this, the read path could be fixed while the write path silently started
// emitting zero-length nonces.
func TestWrappedRootKey_WritesLegacyCompatibleFormat(t *testing.T) {
	b := legacyFixtureBarrier()

	blob, err := b.wrapRootKey(mustHex(t, legacyUnsealKeyHex), mustHex(t, legacyRootKeyHex))
	if err != nil {
		t.Fatalf("wrapRootKey: %v", err)
	}

	var wrapped wrappedRootKey
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		t.Fatalf("unmarshal fresh blob: %v", err)
	}
	if len(wrapped.Nonce) != legacyNonceLen {
		t.Fatalf("fresh blob nonce is %d bytes, want %d -- the wire format changed",
			len(wrapped.Nonce), legacyNonceLen)
	}
	if len(wrapped.Ciphertext) != 48 {
		t.Fatalf("fresh blob ciphertext is %d bytes, want 48 -- the nonce was not split off",
			len(wrapped.Ciphertext))
	}

	// A fresh nonce every time: the AEAD samples it, and a fixed one would be
	// catastrophic for GCM. Comparing against the FIXTURE's nonce alone is not
	// enough -- an implementation that emitted any other single constant would
	// pass that -- so two independently sealed blobs are compared to each
	// other as well.
	second, err := b.wrapRootKey(mustHex(t, legacyUnsealKeyHex), mustHex(t, legacyRootKeyHex))
	if err != nil {
		t.Fatalf("wrapRootKey (second): %v", err)
	}
	var other wrappedRootKey
	if err := json.Unmarshal(second, &other); err != nil {
		t.Fatalf("unmarshal second blob: %v", err)
	}
	if bytes.Equal(wrapped.Nonce, other.Nonce) {
		t.Fatal("two sealings produced the same nonce -- the nonce is not being sampled")
	}
	var legacy wrappedRootKey
	if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &legacy); err != nil {
		t.Fatalf("unmarshal legacy fixture: %v", err)
	}
	if bytes.Equal(wrapped.Nonce, legacy.Nonce) {
		t.Fatal("fresh blob reused the fixture's nonce")
	}

	// Open the fresh envelope WITHOUT going through unwrapRootKey. Round-tripping
	// through this package's own reader would pass for a writer and reader that
	// share a same-length transformation -- reversed nonce bytes, say -- while a
	// reader that predates this change fails. The test therefore does its own
	// naive Nonce||Ciphertext rejoin against an independently constructed AEAD,
	// so nothing but the stdlib sits between the stored fields and the
	// plaintext.
	//
	// The AEAD is built here from crypto/aes and crypto/cipher directly rather
	// than through unsealKeyAEAD, so it shares no code with the writer: routing
	// the oracle through the production factory would let a future wrapper
	// applied inside that factory transform both sides identically and keep
	// this test green.
	//
	// The genuinely legacy oracle would be cipher.NewGCM with an explicit
	// nonce, which is what a pre-v0.7.0 reader used. It is not used here: it is
	// the construction GODEBUG=fips140=only rejects, so calling it would add a
	// deliberate FIPS violation to the suite and corrupt the very GCM-rejection
	// count this repository tracks. NewGCMWithRandomNonce reads the nonce from
	// the front of the frame and is otherwise the same computation -- Go's
	// implementation slices off the 12-byte prefix and hands it plus the
	// remainder to the same underlying GCM Open -- so an explicit-nonce reader
	// given these two fields performs identical work.
	block, err := aes.NewCipher(mustHex(t, legacyUnsealKeyHex))
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatalf("cipher.NewGCMWithRandomNonce: %v", err)
	}
	rejoined := append(append([]byte{}, wrapped.Nonce...), wrapped.Ciphertext...)
	// #nosec G407 -- nonce argument MUST be nil: this AEAD reads the nonce from the front of the frame.
	got, err := aead.Open(nil, nil, rejoined, b.wrappedRootKeyAAD())
	if err != nil {
		t.Fatalf("independent open of the fresh envelope's stored fields: %v", err)
	}
	if want := mustHex(t, legacyRootKeyHex); !bytes.Equal(got, want) {
		t.Fatalf("round-trip root key = %x, want %x", got, want)
	}
}

// TestWrappedRootKey_RejectsWrongUnsealKey keeps the authenticated-decrypt
// contract that resolveDirectSecret leans on to detect revoked or garbage
// shares: a wrong unseal key must surface as ErrUnsealKeyMismatch, not as a
// successful open of the wrong bytes.
func TestWrappedRootKey_RejectsWrongUnsealKey(t *testing.T) {
	var wrapped wrappedRootKey
	if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &wrapped); err != nil {
		t.Fatalf("unmarshal legacy fixture: %v", err)
	}

	wrong := mustHex(t, legacyUnsealKeyHex)
	wrong[0] ^= 0xff

	b := legacyFixtureBarrier()
	// Assert the SPECIFIC error, not merely that one occurred: the doc comment
	// on unwrapRootKey promises ErrUnsealKeyMismatch here, and a bare non-nil
	// check would stay green if the mapping drifted to ErrWrappedRootKeyCorrupt
	// -- which would tell a recovery operator the stored blob is damaged when
	// in fact their shares are wrong.
	_, err := b.unwrapRootKey(&wrapped, wrong)
	if err != ErrUnsealKeyMismatch {
		t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
	}
}

// skipIfFIPS skips a test that must construct a caller-nonce AEAD.
//
// `cipher.NewGCM` — GCM with a caller-chosen IV — is exactly what
// GODEBUG=fips140=only refuses, and refusing it is the whole reason go-quicraft
// v0.7.0-alpha moved to NewGCMWithRandomNonce. So a caller-nonce AEAD cannot be
// constructed under fips140=only at all, which means the caller-nonce branch of
// sealWrapped/openWrapped is UNREACHABLE in a FIPS deployment: there is no
// approved way for a provider to return such an AEAD. The branch exists for
// non-FIPS deployments with hardware-backed providers, and that is where it is
// tested.
//
// This is a skip rather than a build tag because the FIPS job runs the same
// package list with no extra tags; a tag keyed on `fips` would not exclude
// anything there. And it is a skip rather than a stub AEAD because the
// assertions that matter most — a caller-nonce provider opening a blob written
// by the module-nonce default, and opening the checked-in pre-bump fixture —
// require REAL AES-GCM on both sides. A fake cipher would still exercise the
// branch while proving nothing about cross-convention compatibility.
func skipIfFIPS(t *testing.T) {
	t.Helper()
	if fips140.Enabled() {
		t.Skip("caller-nonce AEADs cannot be constructed under fips140=only; " +
			"this code path is unreachable in a FIPS deployment")
	}
}

// callerNonceProvider is an AEADProvider returning a classic caller-nonce
// AEAD: NonceSize() == 12, and Seal/Open PANIC on a nil nonce. It stands in for
// the hardware-backed providers BarrierConfig.AEADProvider exists to accept
// (go-qrdb re-exports the interface and go-xkms re-exports it again), which
// go-quicraft's AEADProvider contract explicitly permits alongside the
// module-nonce kind.
type callerNonceProvider struct{}

func (callerNonceProvider) NewAEAD(alg SymmetricAlgorithm, key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block) //nolint:gosec // deliberately the caller-nonce construction under test
}

// TestWrappedRootKey_CallerNonceProvider covers the OTHER half of the
// AEADProvider contract. Adapting to go-quicraft's module-nonce AEAD by
// hard-coding a nil nonce would panic every injected caller-nonce provider at
// initialization, rekey and unseal -- trading one silent break for another, in
// a documented extension point with a downstream consumer.
//
// It also pins the envelope's convention-independence: a blob written through a
// caller-nonce provider carries the same field widths as one written through
// the module-nonce default, so the two are interchangeable on disk.
func TestWrappedRootKey_CallerNonceProvider(t *testing.T) {
	skipIfFIPS(t)

	b := &StorageBarrier{config: BarrierConfig{
		RootKeyPath:  legacyRootKeyPath,
		AEADProvider: callerNonceProvider{},
	}}
	unsealKey := mustHex(t, legacyUnsealKeyHex)

	// Sanity: this provider really does follow the caller-nonce convention.
	aead, err := b.unsealKeyAEAD(unsealKey)
	if err != nil {
		t.Fatalf("unsealKeyAEAD: %v", err)
	}
	if aead.NonceSize() != legacyNonceLen {
		t.Fatalf("fixture provider NonceSize() = %d, want %d -- it is not a caller-nonce AEAD",
			aead.NonceSize(), legacyNonceLen)
	}

	blob, err := b.wrapRootKey(unsealKey, mustHex(t, legacyRootKeyHex))
	if err != nil {
		t.Fatalf("wrapRootKey through a caller-nonce provider: %v", err)
	}
	var wrapped wrappedRootKey
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrapped.Nonce) != legacyNonceLen || len(wrapped.Ciphertext) != 48 {
		t.Fatalf("caller-nonce blob is %d/%d, want %d/48 -- the envelope is not convention-independent",
			len(wrapped.Nonce), len(wrapped.Ciphertext), legacyNonceLen)
	}

	gotBuf, err := b.unwrapRootKey(&wrapped, unsealKey)
	if err != nil {
		t.Fatalf("unwrapRootKey through a caller-nonce provider: %v", err)
	}
	got := protectedBytes(t, gotBuf)
	if want := mustHex(t, legacyRootKeyHex); !bytes.Equal(got, want) {
		t.Fatalf("round-trip root key = %x, want %x", got, want)
	}

	// The decisive one: a caller-nonce provider must also read a blob written
	// by the module-nonce default. If it cannot, the envelope is not really
	// convention-independent and swapping providers would strand a barrier.
	legacy := legacyFixtureBarrier()
	legacyBlob, err := legacy.wrapRootKey(unsealKey, mustHex(t, legacyRootKeyHex))
	if err != nil {
		t.Fatalf("wrapRootKey (module-nonce default): %v", err)
	}
	var moduleWritten wrappedRootKey
	if err := json.Unmarshal(legacyBlob, &moduleWritten); err != nil {
		t.Fatalf("unmarshal module-nonce blob: %v", err)
	}
	gotBuf, err = b.unwrapRootKey(&moduleWritten, unsealKey)
	if err != nil {
		t.Fatalf("caller-nonce provider could not open a module-nonce blob: %v", err)
	}
	got = protectedBytes(t, gotBuf)
	if want := mustHex(t, legacyRootKeyHex); !bytes.Equal(got, want) {
		t.Fatalf("cross-convention root key = %x, want %x", got, want)
	}

	// And the checked-in pre-bump fixture, through the caller-nonce path.
	var fixture wrappedRootKey
	if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &fixture); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if _, err := b.unwrapRootKey(&fixture, unsealKey); err != nil {
		t.Fatalf("caller-nonce provider could not open the legacy fixture: %v", err)
	}
}

// wideNonceProvider returns a caller-nonce AEAD whose nonce width DISAGREES
// with the width the envelope records for AES-256-GCM.
type wideNonceProvider struct{}

func (wideNonceProvider) NewAEAD(alg SymmetricAlgorithm, key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, 16) //nolint:gosec // deliberately a mismatched width
}

// TestWrappedRootKey_RejectsMismatchedProviderNonceWidth covers the guard that
// M10 showed nothing else exercised. The envelope validates its Nonce field
// against the width its ALGORITHM declares, so a provider that draws a
// different width would write a blob that the very next unseal rejects as
// corrupt -- key material persisted in an unreadable envelope. Failing at wrap
// time turns that into a loud error before anything is stored.
func TestWrappedRootKey_RejectsMismatchedProviderNonceWidth(t *testing.T) {
	skipIfFIPS(t)

	b := &StorageBarrier{config: BarrierConfig{
		RootKeyPath:  legacyRootKeyPath,
		AEADProvider: wideNonceProvider{},
	}}

	_, err := b.wrapRootKey(mustHex(t, legacyUnsealKeyHex), mustHex(t, legacyRootKeyHex))
	if err != ErrWrappedRootKeyCorrupt {
		t.Fatalf("err = %v, want ErrWrappedRootKeyCorrupt -- a provider whose nonce width "+
			"disagrees with the algorithm must be refused, not persisted", err)
	}
}

// TestWrappedRootKey_ErrorPrecedence pins which error wins when an input fails
// BOTH the key-length check and the nonce-width check. Extracting unwrapRootKey
// out of resolveDirectSecret made this reorderable, and the two errors route a
// recovery operator differently, so the precedence is part of the contract
// rather than an accident of statement order.
func TestWrappedRootKey_ErrorPrecedence(t *testing.T) {
	var wrapped wrappedRootKey
	if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &wrapped); err != nil {
		t.Fatalf("unmarshal legacy fixture: %v", err)
	}
	wrapped.Nonce = wrapped.Nonce[:len(wrapped.Nonce)-1] // malformed nonce field

	b := legacyFixtureBarrier()

	// Wrong-LENGTH key (not merely wrong bytes) plus a malformed nonce: the key
	// is rejected first, so this is ErrUnsealKeyMismatch.
	shortKey := mustHex(t, legacyUnsealKeyHex)[:16]
	if _, err := b.unwrapRootKey(&wrapped, shortKey); err != ErrUnsealKeyMismatch {
		t.Fatalf("short key + bad nonce: err = %v, want ErrUnsealKeyMismatch", err)
	}

	// Correct-length key with a malformed nonce reaches the nonce check.
	if _, err := b.unwrapRootKey(&wrapped, mustHex(t, legacyUnsealKeyHex)); err != ErrWrappedRootKeyCorrupt {
		t.Fatalf("valid-length key + bad nonce: err = %v, want ErrWrappedRootKeyCorrupt", err)
	}
}

// TestWrappedRootKey_RejectsForeignRootKeyPath proves the AAD still binds the
// blob to its barrier. The nonce moved between the AEAD and the envelope; the
// associated data must not have moved with it.
func TestWrappedRootKey_RejectsForeignRootKeyPath(t *testing.T) {
	// Seal freshly rather than reusing the checked-in fixture: the fixture was
	// sealed under the real AAD, so ANY change to the AAD makes it un-openable
	// and this assertion would pass for the wrong reason. Sealing with one
	// barrier and opening with another is what actually distinguishes an AAD
	// that binds the RootKeyPath from one that does not.
	origin := legacyFixtureBarrier()
	unsealKey := mustHex(t, legacyUnsealKeyHex)

	blob, err := origin.wrapRootKey(unsealKey, mustHex(t, legacyRootKeyHex))
	if err != nil {
		t.Fatalf("wrapRootKey: %v", err)
	}
	var wrapped wrappedRootKey
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Sanity: the originating barrier must be able to open its own blob, or
	// the foreign rejection below proves nothing.
	if _, err := origin.unwrapRootKey(&wrapped, unsealKey); err != nil {
		t.Fatalf("originating barrier could not open its own blob: %v", err)
	}

	foreign := &StorageBarrier{config: BarrierConfig{RootKeyPath: "tenants/other/seal/root-key"}}
	if _, err := foreign.unwrapRootKey(&wrapped, unsealKey); err == nil {
		t.Fatal("unwrapRootKey accepted a blob sealed under a different RootKeyPath")
	}
}

// TestWrappedRootKey_RejectsTruncatedNonce covers the field-length check that
// used to be `len(wrapped.Nonce) != aead.NonceSize()`. That comparison now
// reads 0 from the AEAD and would accept anything, so the check moved onto the
// format constant; this test is what holds it there.
func TestWrappedRootKey_RejectsTruncatedNonce(t *testing.T) {
	for name, mutate := range map[string]func(*wrappedRootKey){
		"short nonce": func(w *wrappedRootKey) { w.Nonce = w.Nonce[:len(w.Nonce)-1] },
		"empty nonce": func(w *wrappedRootKey) { w.Nonce = nil },
		"long nonce":  func(w *wrappedRootKey) { w.Nonce = append(w.Nonce, 0x00) },
	} {
		t.Run(name, func(t *testing.T) {
			var wrapped wrappedRootKey
			if err := json.Unmarshal([]byte(legacyWrappedRootKeyBlob), &wrapped); err != nil {
				t.Fatalf("unmarshal legacy fixture: %v", err)
			}
			mutate(&wrapped)

			b := legacyFixtureBarrier()
			_, err := b.unwrapRootKey(&wrapped, mustHex(t, legacyUnsealKeyHex))
			if err != ErrWrappedRootKeyCorrupt {
				t.Fatalf("err = %v, want ErrWrappedRootKeyCorrupt", err)
			}
		})
	}
}
