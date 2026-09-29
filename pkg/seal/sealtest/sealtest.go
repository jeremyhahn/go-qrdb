// Copyright 2026 Jeremy Hahn
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

// Package sealtest holds helpers for tests that exercise seal.Credentials and
// the storage barrier.
//
// A credential's secret is protected memory (a *enclave.SealedEnclave), and
// production code never materializes it as a []byte: the strategy that needs
// the passphrase opens the enclave itself for exactly as long as the KDF runs.
// A test asserting what a handler or service PASSED DOWN has no such path — it
// captured a credential in a double and wants to compare its content — and
// that is the one reading this package exists for.
//
// It also holds in-memory custody doubles (Distributor, Verifier, Custody) for
// tests that need a working Shamir share-holder quorum; see custody.go. It is
// imported by tests only; a production import of it is a defect.
package sealtest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Plaintext opens the credential's secret and returns a heap copy of the
// material, or nil for a credential with no secret. The opened enclave is
// destroyed before returning; the copy is the test's to compare and forget.
// The credential itself is untouched and remains its owner's to destroy.
func Plaintext(tb testing.TB, creds seal.Credentials) []byte {
	tb.Helper()
	if creds.Secret == nil {
		return nil
	}
	opened, err := creds.Secret.Open()
	if err != nil {
		tb.Fatalf("sealtest: open credential secret: %v", err)
	}
	defer opened.Destroy()
	// A COPY, taken under the borrow: MemoryEnclave.Open returns a view of
	// the locked pages, and the Destroy above unmaps them on return.
	var b []byte
	if err := opened.WithBytes(func(view []byte) error {
		b = append([]byte(nil), view...)
		return nil
	}); err != nil {
		tb.Fatalf("sealtest: read credential secret: %v", err)
	}
	return b
}

// Snapshot is Plaintext for a test double that has no testing.TB in hand — a
// fake service or migrator recording what it was passed. A double must take
// the snapshot DURING the call: the caller owns the credential and destroys it
// when the call returns, so a captured seal.Credentials read afterwards is a
// dead enclave. It panics if the secret cannot be opened, which in a test is
// the right answer.
func Snapshot(creds seal.Credentials) []byte {
	if creds.Secret == nil {
		return nil
	}
	opened, err := creds.Secret.Open()
	if err != nil {
		panic("sealtest: open credential secret: " + err.Error())
	}
	defer opened.Destroy()
	var b []byte
	if err := opened.WithBytes(func(view []byte) error {
		b = append([]byte(nil), view...)
		return nil
	}); err != nil {
		panic("sealtest: read credential secret: " + err.Error())
	}
	return b
}

// UnsealedBarrier returns an initialized, unsealed *seal.StorageBarrier over an
// in-memory backend with a software strategy: a working key-encryption key
// for a test that needs at-rest encryption actually engaged, as
// atrest.New(barrier).Enabled() reports it.
func UnsealedBarrier(tb testing.TB) *seal.StorageBarrier {
	tb.Helper()
	const passphrase = "sealtest-unsealed-barrier"
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		tb.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "sealtest-root-key", DeploymentID: 1}, strategy)
	if err != nil {
		tb.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials([]byte(passphrase))
	if err := barrier.Initialize(context.Background(), creds); err != nil {
		tb.Fatalf("barrier.Initialize: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(context.Background(), creds); err != nil {
			tb.Fatalf("barrier.Unseal: %v", err)
		}
	}
	return barrier
}
