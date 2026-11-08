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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

// fixtureUnsealKeyHex and fixtureRootKeyHex are AES-256 keys drawn fresh for
// each test run, so no key material lives in the source. Every test that
// uses them is a round trip or a tamper check, never a known-answer vector.
var (
	fixtureUnsealKeyHex = freshKeyHex()
	fixtureRootKeyHex   = freshKeyHex()
)

// freshKeyHex returns 32 bytes from crypto/rand, hex-encoded.
func freshKeyHex() string {
	k := make([]byte, 32)
	_, _ = rand.Read(k) // crypto/rand.Read never returns an error; it crashes the program instead
	return hex.EncodeToString(k)
}

const (
	// fixtureRootKeyPath feeds the AAD.
	fixtureRootKeyPath = "core/seal/root-key"

	// wireNonceLen is the nonce width the envelope carries, written as a
	// literal on purpose: deriving it from AlgAES256GCM.NonceSize() would make
	// the assertions circular.
	wireNonceLen = 12

	// v1WrappedBlob is a wrapped-root-key blob in the format that predates
	// holder rosters (version 1, no roster, AAD without a digest).
	v1WrappedBlob = `{"version":1,"algorithm":3,"nonce":"ldqVJZV9492DHKT2","ciphertext":"I6vXL9ILPwMKfR67HCO1D6dNgNZ3ood/SC3xzPJROfezBB7oQsEGxNizXHv+qMqa"}`
)

func fixtureBarrier() *StorageBarrier {
	return &StorageBarrier{config: BarrierConfig{RootKeyPath: fixtureRootKeyPath}}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex vector: %v", err)
	}
	return b
}

// fixtureRoster returns a valid 2-of-3 roster for domain.
func fixtureRoster(t *testing.T, domain QuorumDomain) *HolderRoster {
	t.Helper()
	d := newCaptureDistributor()
	d.setHolders(domain, "h1", "h2", "h3")
	r, err := d.PrepareRoster(context.Background(), domain, 2, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// wrapFixture wraps the fixture root key under the fixture key for domain and
// decodes the result.
func wrapFixture(t *testing.T, b *StorageBarrier, domain QuorumDomain) (*wrappedRootKey, []byte) {
	t.Helper()
	blob, digest, err := b.wrapRootKey(domain, mustHex(t, fixtureUnsealKeyHex), mustHex(t, fixtureRootKeyHex), fixtureRoster(t, domain))
	if err != nil {
		t.Fatalf("wrapRootKey: %v", err)
	}
	var w wrappedRootKey
	if err := json.Unmarshal(blob, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &w, digest
}

// TestWrappedRootKey_WritesSeparateNonceFormat pins the envelope's wire shape:
// the nonce in its own field, a fresh one on every sealing, and a ciphertext
// an independently built AEAD opens under the documented AAD.
func TestWrappedRootKey_WritesSeparateNonceFormat(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	if w.Version != wrappedRootKeyVersion || len(w.Roster) == 0 {
		t.Fatalf("blob version %d, roster %d bytes", w.Version, len(w.Roster))
	}
	if len(w.Nonce) != wireNonceLen || len(w.Ciphertext) != 48 {
		t.Fatalf("nonce/ciphertext = %d/%d, want %d/48", len(w.Nonce), len(w.Ciphertext), wireNonceLen)
	}
	other, _ := wrapFixture(t, b, DomainUnseal)
	if bytes.Equal(w.Nonce, other.Nonce) {
		t.Fatal("two sealings produced the same nonce -- the nonce is not being sampled")
	}

	// Open with an AEAD built from the stdlib, sharing no code with the writer.
	block, err := aes.NewCipher(mustHex(t, fixtureUnsealKeyHex))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("go-qrdb/seal/wrapped-root-key/v2|" + fixtureRootKeyPath + "|unseal|" + hex.EncodeToString(digest))
	rejoined := append(append([]byte{}, w.Nonce...), w.Ciphertext...)
	// #nosec G407 -- nonce argument MUST be nil: this AEAD reads the nonce from the front of the frame.
	got, err := aead.Open(nil, nil, rejoined, aad)
	if err != nil {
		t.Fatalf("independent open under the documented AAD: %v", err)
	}
	if !bytes.Equal(got, mustHex(t, fixtureRootKeyHex)) {
		t.Fatalf("round-trip root key = %x", got)
	}
}

// TestWrappedRootKey_RoundTrip opens what wrapRootKey wrote.
func TestWrappedRootKey_RoundTrip(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	buf, err := b.unwrapRootKey(DomainUnseal, w, digest, mustHex(t, fixtureUnsealKeyHex))
	if err != nil {
		t.Fatal(err)
	}
	if got := protectedBytes(t, buf); !bytes.Equal(got, mustHex(t, fixtureRootKeyHex)) {
		t.Fatalf("root key = %x", got)
	}
}

// TestWrappedRootKey_RejectsWrongUnsealKey asserts the SPECIFIC error: a
// recovery operator must be told their shares are wrong, not that the blob is
// damaged.
func TestWrappedRootKey_RejectsWrongUnsealKey(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	wrong := mustHex(t, fixtureUnsealKeyHex)
	wrong[0] ^= 0xff
	if _, err := b.unwrapRootKey(DomainUnseal, w, digest, wrong); err != ErrUnsealKeyMismatch {
		t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
	}
}

// TestWrappedRootKey_RosterDigestIsAuthenticated is the tamper property the
// roster binding exists for: the right key over a different roster digest
// does not open.
func TestWrappedRootKey_RosterDigestIsAuthenticated(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	forged := append([]byte(nil), digest...)
	forged[0] ^= 0x01
	if _, err := b.unwrapRootKey(DomainUnseal, w, forged, mustHex(t, fixtureUnsealKeyHex)); err != ErrUnsealKeyMismatch {
		t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
	}
}

// TestWrappedRootKey_DomainIsAuthenticated pins that an unseal-domain blob
// cannot be opened as the recovery domain (or vice versa).
func TestWrappedRootKey_DomainIsAuthenticated(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	if _, err := b.unwrapRootKey(DomainRecovery, w, digest, mustHex(t, fixtureUnsealKeyHex)); err != ErrUnsealKeyMismatch {
		t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
	}
	var bad *InvalidRosterError
	if _, _, err := b.wrapRootKey(DomainRecovery, mustHex(t, fixtureUnsealKeyHex), mustHex(t, fixtureRootKeyHex), fixtureRoster(t, DomainUnseal)); !errors.As(err, &bad) {
		t.Fatalf("wrapping a roster under another domain = %v", err)
	}
	if _, _, err := b.wrapRootKey(DomainUnseal, mustHex(t, fixtureUnsealKeyHex), mustHex(t, fixtureRootKeyHex), nil); !errors.As(err, &bad) {
		t.Fatalf("wrapping with no roster = %v", err)
	}
}

// TestWrappedRootKey_RejectsForeignRootKeyPath proves the AAD binds the blob
// to its barrier.
func TestWrappedRootKey_RejectsForeignRootKeyPath(t *testing.T) {
	origin := fixtureBarrier()
	w, digest := wrapFixture(t, origin, DomainUnseal)
	if _, err := origin.unwrapRootKey(DomainUnseal, w, digest, mustHex(t, fixtureUnsealKeyHex)); err != nil {
		t.Fatalf("originating barrier could not open its own blob: %v", err)
	}
	foreign := &StorageBarrier{config: BarrierConfig{RootKeyPath: "tenants/other/seal/root-key"}}
	if _, err := foreign.unwrapRootKey(DomainUnseal, w, digest, mustHex(t, fixtureUnsealKeyHex)); err == nil {
		t.Fatal("unwrapRootKey accepted a blob sealed under a different RootKeyPath")
	}
}

// TestWrappedRootKey_ErrorPrecedence pins which error wins when an input fails
// BOTH the key-length check and the nonce-width check.
func TestWrappedRootKey_ErrorPrecedence(t *testing.T) {
	b := fixtureBarrier()
	w, digest := wrapFixture(t, b, DomainUnseal)
	w.Nonce = w.Nonce[:len(w.Nonce)-1]
	shortKey := mustHex(t, fixtureUnsealKeyHex)[:16]
	if _, err := b.unwrapRootKey(DomainUnseal, w, digest, shortKey); err != ErrUnsealKeyMismatch {
		t.Fatalf("short key + bad nonce: err = %v, want ErrUnsealKeyMismatch", err)
	}
	if _, err := b.unwrapRootKey(DomainUnseal, w, digest, mustHex(t, fixtureUnsealKeyHex)); err != ErrWrappedRootKeyCorrupt {
		t.Fatalf("valid-length key + bad nonce: err = %v, want ErrWrappedRootKeyCorrupt", err)
	}
}

// TestWrappedRootKey_RejectsTruncatedNonce holds the nonce-width check on the
// format constant.
func TestWrappedRootKey_RejectsTruncatedNonce(t *testing.T) {
	for name, mutate := range map[string]func(*wrappedRootKey){
		"short nonce": func(w *wrappedRootKey) { w.Nonce = w.Nonce[:len(w.Nonce)-1] },
		"empty nonce": func(w *wrappedRootKey) { w.Nonce = nil },
		"long nonce":  func(w *wrappedRootKey) { w.Nonce = append(w.Nonce, 0x00) },
		"version":     func(w *wrappedRootKey) { w.Version = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := fixtureBarrier()
			w, digest := wrapFixture(t, b, DomainUnseal)
			mutate(w)
			if _, err := b.unwrapRootKey(DomainUnseal, w, digest, mustHex(t, fixtureUnsealKeyHex)); err != ErrWrappedRootKeyCorrupt {
				t.Fatalf("err = %v, want ErrWrappedRootKeyCorrupt", err)
			}
		})
	}
}

// TestLoadWrapped_PreRosterBlobIsRosterMissing pins the breaking-change
// behaviour: a blob written before holder rosters cannot be read as a
// roster-bound key, and says so.
func TestLoadWrapped_PreRosterBlobIsRosterMissing(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	b := &StorageBarrier{base: backend, config: BarrierConfig{RootKeyPath: fixtureRootKeyPath}}

	if _, _, err := b.HolderRoster(ctx, DomainUnseal); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("no blob: err = %v, want ErrHolderRosterMissing", err)
	}
	if err := backend.Put(ctx, b.wrappedKeyPath(DomainUnseal), []byte(v1WrappedBlob)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.HolderRoster(ctx, DomainUnseal); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("v1 blob: err = %v, want ErrHolderRosterMissing", err)
	}
	if err := backend.Put(ctx, b.wrappedKeyPath(DomainUnseal), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.HolderRoster(ctx, DomainUnseal); !errors.Is(err, ErrWrappedRootKeyCorrupt) {
		t.Fatalf("corrupt blob: err = %v, want ErrWrappedRootKeyCorrupt", err)
	}
	var invalid *QuorumOpInvalidError
	if _, _, err := b.HolderRoster(ctx, QuorumDomain("elsewhere")); !errors.As(err, &invalid) {
		t.Fatalf("unknown domain: err = %v", err)
	}
}

// TestLoadWrapped_RefusesARosterFromAnotherDomain pins that a blob moved from
// the recovery path onto the unseal path is refused before any key is tried.
func TestLoadWrapped_RefusesARosterFromAnotherDomain(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	b := &StorageBarrier{base: backend, config: BarrierConfig{RootKeyPath: fixtureRootKeyPath}}
	blob, _, err := b.wrapRootKey(DomainRecovery, mustHex(t, fixtureUnsealKeyHex), mustHex(t, fixtureRootKeyHex), fixtureRoster(t, DomainRecovery))
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, b.wrappedKeyPath(DomainUnseal), blob); err != nil {
		t.Fatal(err)
	}
	var bad *InvalidRosterError
	if _, _, err := b.HolderRoster(ctx, DomainUnseal); !errors.As(err, &bad) {
		t.Fatalf("err = %v, want InvalidRosterError", err)
	}
}

// skipIfFIPS skips a test that must construct a caller-nonce AEAD, which
// GODEBUG=fips140=only refuses to construct at all; the caller-nonce branch is
// unreachable in a FIPS deployment.
func skipIfFIPS(t *testing.T) {
	t.Helper()
	if fips140.Enabled() {
		t.Skip("caller-nonce AEADs cannot be constructed under fips140=only; " +
			"this code path is unreachable in a FIPS deployment")
	}
}

// callerNonceProvider is an AEADProvider returning a classic caller-nonce
// AEAD, standing in for the hardware-backed providers BarrierConfig
// .AEADProvider accepts.
type callerNonceProvider struct{}

func (callerNonceProvider) NewAEAD(_ SymmetricAlgorithm, key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block) //nolint:gosec // deliberately the caller-nonce construction under test
}

// TestWrappedRootKey_CallerNonceProvider covers the caller-nonce half of the
// AEADProvider contract and the envelope's convention-independence.
func TestWrappedRootKey_CallerNonceProvider(t *testing.T) {
	skipIfFIPS(t)
	b := &StorageBarrier{config: BarrierConfig{RootKeyPath: fixtureRootKeyPath, AEADProvider: callerNonceProvider{}}}
	key := mustHex(t, fixtureUnsealKeyHex)
	w, digest := wrapFixture(t, b, DomainUnseal)
	if len(w.Nonce) != wireNonceLen || len(w.Ciphertext) != 48 {
		t.Fatalf("caller-nonce blob is %d/%d", len(w.Nonce), len(w.Ciphertext))
	}
	if _, err := b.unwrapRootKey(DomainUnseal, w, digest, key); err != nil {
		t.Fatalf("caller-nonce round trip: %v", err)
	}
	// A caller-nonce provider reads a blob the module-nonce default wrote.
	mw, mdigest := wrapFixture(t, fixtureBarrier(), DomainUnseal)
	if _, err := b.unwrapRootKey(DomainUnseal, mw, mdigest, key); err != nil {
		t.Fatalf("caller-nonce provider could not open a module-nonce blob: %v", err)
	}
}

// wideNonceProvider returns a caller-nonce AEAD whose nonce width disagrees
// with the width the envelope records for AES-256-GCM.
type wideNonceProvider struct{}

func (wideNonceProvider) NewAEAD(_ SymmetricAlgorithm, key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, 16) //nolint:gosec // deliberately a mismatched width
}

// TestWrappedRootKey_RejectsMismatchedProviderNonceWidth refuses, at wrap
// time, a provider whose blob the next unseal would reject.
func TestWrappedRootKey_RejectsMismatchedProviderNonceWidth(t *testing.T) {
	skipIfFIPS(t)
	b := &StorageBarrier{config: BarrierConfig{RootKeyPath: fixtureRootKeyPath, AEADProvider: wideNonceProvider{}}}
	_, _, err := b.wrapRootKey(DomainUnseal, mustHex(t, fixtureUnsealKeyHex), mustHex(t, fixtureRootKeyHex), fixtureRoster(t, DomainUnseal))
	if err != ErrWrappedRootKeyCorrupt {
		t.Fatalf("err = %v, want ErrWrappedRootKeyCorrupt", err)
	}
}
