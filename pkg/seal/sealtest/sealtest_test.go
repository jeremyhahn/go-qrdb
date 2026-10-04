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

package sealtest

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestPlaintext_RoundTrip(t *testing.T) {
	creds := seal.MustCredentials(context.Background(), []byte("correct horse battery staple"))
	defer creds.Secret.Destroy()

	got := Plaintext(t, creds)
	if string(got) != "correct horse battery staple" {
		t.Fatalf("Plaintext = %q", got)
	}
	// The credential is untouched: a second read answers the same, and the
	// copy is the caller's — mutating it changes nothing behind it.
	got[0] = 'X'
	if again := Plaintext(t, creds); string(again) != "correct horse battery staple" {
		t.Fatalf("second Plaintext = %q; the first read altered or consumed the credential", again)
	}
	if !creds.Secret.IsAlive() {
		t.Fatal("Plaintext destroyed the credential; it is the owner's to destroy")
	}
}

func TestPlaintext_NoSecretIsNil(t *testing.T) {
	if got := Plaintext(t, seal.Credentials{}); got != nil {
		t.Fatalf("Plaintext of the zero credential = %v, want nil", got)
	}
}

func TestSnapshot_RoundTrip(t *testing.T) {
	creds := seal.MustCredentials(context.Background(), []byte("snap"))
	got := Snapshot(creds)
	// The snapshot survives the owner destroying the credential — which is
	// the reason a double takes one during the call.
	creds.Secret.Destroy()
	if !bytes.Equal(got, []byte("snap")) {
		t.Fatalf("Snapshot = %q, want snap", got)
	}
}

func TestSnapshot_NoSecretIsNil(t *testing.T) {
	if got := Snapshot(seal.Credentials{}); got != nil {
		t.Fatalf("Snapshot of the zero credential = %v, want nil", got)
	}
}

func TestSnapshot_DestroyedCredentialPanics(t *testing.T) {
	creds := seal.MustCredentials(context.Background(), []byte("gone"))
	creds.Secret.Destroy()
	defer func() {
		if recover() == nil {
			t.Fatal("Snapshot of a destroyed credential must panic, not answer")
		}
	}()
	_ = Snapshot(creds)
}

func TestUnsealedBarrier_IsUnsealed(t *testing.T) {
	if UnsealedBarrier(t).IsSealed() {
		t.Fatal("UnsealedBarrier returned a sealed barrier")
	}
}
