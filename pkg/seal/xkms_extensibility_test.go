// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"log/slog"
	"testing"
)

// mockHSMStrategy is a stand-in for an xkms-injected hardware sealing strategy
// (TPM2 / PKCS#11 / cloud-KMS). It models the REQUIRED envelope discipline:
//   - SealRootKey / UnsealRootKey wrap/unwrap the root key under a non-exportable
//     device key (here a fixed in-test AES-GCM KEK standing in for the HSM).
//   - BarrierEncryptor returns (nil, nil) so the barrier derives a RESIDENT
//     software DEK via HKDF and does the bulk encryption with AES-NI — the
//     hardware is touched only once at unseal, never on the per-op hot path.
//
// This is exactly the seam go-xkms plugs into via WithStrategyRegistry, and the
// per-node-keys property holds because the device key never leaves the node.
type mockHSMStrategy struct {
	id  StrategyID
	kek []byte // 32-byte "device" key (never leaves this strategy)
}

func newMockHSMStrategy(t *testing.T, id string) *mockHSMStrategy {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, kek); err != nil {
		t.Fatalf("gen kek: %v", err)
	}
	return &mockHSMStrategy{id: StrategyID(id), kek: kek}
}

func (m *mockHSMStrategy) ID() StrategyID       { return m.id }
func (m *mockHSMStrategy) Available() bool      { return true }
func (m *mockHSMStrategy) HardwareBacked() bool { return true }
func (m *mockHSMStrategy) Close() error         { return nil }

// BarrierEncryptor returns (nil, nil): the envelope invariant. The barrier uses
// epoch-HKDF resident DEKs for bulk crypto; the HSM only wraps the root key.
func (m *mockHSMStrategy) BarrierEncryptor(context.Context, []byte) (SymmetricEncrypter, error) {
	return nil, nil
}

func (m *mockHSMStrategy) SealRootKey(_ context.Context, rootKey []byte, _ Credentials) (*SealedRootKey, error) {
	g := m.aeadNoT()
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := g.Seal(nonce, nonce, rootKey, nil)
	return &SealedRootKey{Strategy: m.id, Version: 1, Ciphertext: ct}, nil
}

func (m *mockHSMStrategy) UnsealRootKey(_ context.Context, sealed *SealedRootKey, _ Credentials) ([]byte, error) {
	g := m.aeadNoT()
	ns := g.NonceSize()
	nonce, ct := sealed.Ciphertext[:ns], sealed.Ciphertext[ns:]
	return g.Open(nil, nonce, ct, nil)
}

// aeadNoT builds the AEAD without *testing.T (used on the interface methods).
func (m *mockHSMStrategy) aeadNoT() cipher.AEAD {
	blk, _ := aes.NewCipher(m.kek)
	g, _ := cipher.NewGCM(blk)
	return g
}

var _ SealingStrategy = (*mockHSMStrategy)(nil)

func xkmsTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestXKMS_HardwareStrategyThroughBarrier proves an xkms-style hardware strategy
// slots into the StorageBarrier end-to-end: a barrier built with the mock HSM
// strategy initializes, encrypts/decrypts a round-trip, seals, and re-unseals —
// reporting HardwareBacked while the bulk crypto uses a resident DEK (the
// strategy's BarrierEncryptor returns nil).
func TestXKMS_HardwareStrategyThroughBarrier(t *testing.T) {
	ctx := context.Background()
	strat := newMockHSMStrategy(t, "mock-hsm")

	// Envelope invariant: the hardware strategy does not supply a per-op
	// encrypter; the barrier uses resident HKDF DEKs.
	if enc, err := strat.BarrierEncryptor(ctx, nil); err != nil || enc != nil {
		t.Fatalf("BarrierEncryptor = (%v,%v), want (nil,nil) for the envelope invariant", enc, err)
	}

	b, err := NewStorageBarrier(xkmsTestLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "hsm/root-key", PreferenceOrder: []StrategyID{"mock-hsm"}}, strat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize (hardware): %v", err)
	}
	if st := b.Status(); !st.HardwareBacked {
		t.Fatalf("Status().HardwareBacked = false, want true for an HSM strategy")
	}

	// Data round-trip through the barrier (resident-DEK AES-GCM).
	if err := b.Put(ctx, "k", []byte("secret-value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := b.Get(ctx, "k"); err != nil || string(got) != "secret-value" {
		t.Fatalf("Get = %q, %v; want secret-value", got, err)
	}

	// Seal (drops the DEK) then re-unseal via the HSM (unwraps the root key once).
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal (hardware): %v", err)
	}
	if got, err := b.Get(ctx, "k"); err != nil || string(got) != "secret-value" {
		t.Fatalf("post-unseal Get = %q, %v; want secret-value", got, err)
	}
}

// TestXKMS_HardwareStrategyViaRegistry proves the StrategyRegistry seam: an
// xkms strategy registered by id is resolvable, exactly how go-xkms publishes a
// hardware strategy for tenants/system to select.
func TestXKMS_HardwareStrategyViaRegistry(t *testing.T) {
	reg := NewDefaultStrategyRegistry()
	strat := newMockHSMStrategy(t, "mock-hsm-reg")
	if err := reg.Register("mock-hsm-reg", func() (SealingStrategy, error) { return strat, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	factory, err := reg.LookupFactory("mock-hsm-reg")
	if err != nil {
		t.Fatalf("LookupFactory registered strategy: %v", err)
	}
	got, err := factory()
	if err != nil {
		t.Fatalf("factory(): %v", err)
	}
	if got.ID() != "mock-hsm-reg" || !got.HardwareBacked() {
		t.Fatalf("resolved strategy id=%q hw=%v, want mock-hsm-reg/true", got.ID(), got.HardwareBacked())
	}
}

// TestXKMS_PerNodeHardwareKeysAreDistinct proves the per-node property holds for
// hardware strategies: two nodes each with their OWN device key produce distinct
// at-rest DEKs, so one node's ciphertext is not decryptable by the other — the
// reason hardware crypto requires (and naturally yields) per-node keys.
func TestXKMS_PerNodeHardwareKeysAreDistinct(t *testing.T) {
	ctx := context.Background()
	mk := func(id string) *StorageBarrier {
		b, err := NewStorageBarrier(xkmsTestLogger(), NewMemoryBackend(),
			BarrierConfig{RootKeyPath: "hsm/root-key", PreferenceOrder: []StrategyID{StrategyID(id)}},
			newMockHSMStrategy(t, id))
		if err != nil {
			t.Fatalf("NewStorageBarrier(%s): %v", id, err)
		}
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatalf("Initialize(%s): %v", id, err)
		}
		return b
	}
	nodeA := mk("hsm-a")
	nodeB := mk("hsm-b")

	if err := nodeA.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("nodeA.Put: %v", err)
	}
	// Each node decrypts its own.
	if got, err := nodeA.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("nodeA.Get = %q, %v; want v", got, err)
	}
	// Node B (own device key, own root key, own salt) cannot read node A's value.
	if got, err := nodeB.Get(ctx, "k"); err == nil && string(got) == "v" {
		t.Fatal("nodeB decrypted nodeA's data — per-node hardware keys are NOT distinct")
	}
}
