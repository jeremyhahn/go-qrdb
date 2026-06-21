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
	"testing"
)

// newSoftwareBarrierWithPass builds an initialized software (passphrase) barrier
// over its own in-memory backend, sealed under the given passphrase. Each call
// generates an independent random root key + HKDF salt — modeling a per-node
// barrier.
func newSoftwareBarrierWithPass(t *testing.T, pass string) *StorageBarrier {
	t.Helper()
	strat, err := NewSoftwareStrategy([]byte(pass))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "node/root-key"}, strat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{Secret: []byte(pass)}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// TestPerNodeSaltIsolation_SamePassphraseDistinctDEKs proves the per-node
// foundation: two barriers initialized with the SAME passphrase derive DIFFERENT
// data-encryption keys (each Initialize mints an independent random root key +
// HKDF salt), so one node's at-rest ciphertext is undecryptable by another even
// though the operator-supplied secret is identical. This is what makes the
// per-node model safe without a shared, resident cluster key.
func TestPerNodeSaltIsolation_SamePassphraseDistinctDEKs(t *testing.T) {
	const pass = "shared-operator-passphrase"
	a := newSoftwareBarrierWithPass(t, pass)
	b := newSoftwareBarrierWithPass(t, pass)

	// Master (root) keys are independent per node.
	ak, err := a.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey(a): %v", err)
	}
	bk, err := b.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey(b): %v", err)
	}
	if bytes.Equal(ak, bk) {
		t.Fatal("two barriers with the same passphrase produced identical root keys; expected per-node keys")
	}

	// Node A's ciphertext is NOT decryptable by node B: distinct DEKs.
	pt := []byte("per-node-secret-payload")
	ct, err := a.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt(a): %v", err)
	}
	if _, err := b.Decrypt(ct); err == nil {
		t.Fatal("node B decrypted node A's ciphertext despite distinct per-node DEKs (data-key isolation broken)")
	}
	// Sanity: A can decrypt its own.
	if got, err := a.Decrypt(ct); err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("self-decrypt: got %q err %v, want %q", got, err, pt)
	}
}

// TestShamirOption3_SharedUnsealKeyPerNodeRootKeys proves the Shamir "Option 3"
// cluster property at the seal layer: a SINGLE unseal key U (the secret that is
// Shamir-split to officers — one quorum, one share set) can unwrap a DIFFERENT
// per-node root key on each node, and those per-node root keys yield DISTINCT
// DEKs. Thus one quorum unlocks every replica while each replica keeps its own
// data key — no shared resident encryption key.
func TestShamirOption3_SharedUnsealKeyPerNodeRootKeys(t *testing.T) {
	ctx := context.Background()

	// Two independent per-node barriers (distinct random root keys + salts).
	a := newSoftwareBarrierWithPass(t, "node-a-pass")
	b := newSoftwareBarrierWithPass(t, "node-b-pass")

	// ONE shared unseal key U — in a cluster this is the secret split via Shamir
	// to the officers (one quorum). Here we model it directly.
	u, err := generateUnsealKey()
	if err != nil {
		t.Fatalf("generateUnsealKey: %v", err)
	}

	// Each node wraps ITS OWN root key under the shared U and persists the blob,
	// so resolveDirectSecret(U) unwraps that node's own root key.
	wrapUnder := func(bar *StorageBarrier) []byte {
		rk, err := bar.GetMasterKey()
		if err != nil {
			t.Fatalf("GetMasterKey: %v", err)
		}
		blob, err := bar.wrapRootKey(u, rk)
		if err != nil {
			t.Fatalf("wrapRootKey: %v", err)
		}
		if err := bar.base.Put(ctx, bar.wrappedRootKeyPath(), blob); err != nil {
			t.Fatalf("persist wrapped blob: %v", err)
		}
		return rk
	}
	rkA := wrapUnder(a)
	rkB := wrapUnder(b)

	// The single U unwraps each node's OWN distinct root key.
	gotA, err := a.resolveDirectSecret(ctx, u)
	if err != nil {
		t.Fatalf("resolveDirectSecret(a): %v", err)
	}
	gotB, err := b.resolveDirectSecret(ctx, u)
	if err != nil {
		t.Fatalf("resolveDirectSecret(b): %v", err)
	}
	if !bytes.Equal(gotA, rkA) || !bytes.Equal(gotB, rkB) {
		t.Fatal("shared U did not unwrap each node's own root key")
	}
	if bytes.Equal(rkA, rkB) {
		t.Fatal("per-node root keys are identical; Option 3 must keep them distinct")
	}

	// Distinct root keys ⇒ distinct DEKs: A's ciphertext is undecryptable by B,
	// even though both are unlocked by the same quorum secret U.
	ct, err := a.Encrypt([]byte("tenant-replica-A-data"))
	if err != nil {
		t.Fatalf("Encrypt(a): %v", err)
	}
	if _, err := b.Decrypt(ct); err == nil {
		t.Fatal("replica B decrypted replica A's data under a shared quorum; per-node DEK isolation broken")
	}
}
