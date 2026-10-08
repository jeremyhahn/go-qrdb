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

package seal

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-enclave"
)

// ---------------------------------------------------------------------------
// Credentials constructors (go-qrdb#540)
// ---------------------------------------------------------------------------

func TestNewCredentials_ConsumesTheSource(t *testing.T) {
	src := []byte("correct horse battery staple")
	creds, err := NewCredentials(context.Background(), src)
	if err != nil {
		t.Fatalf("NewCredentials: %v", err)
	}
	defer DestroyCredentials(creds)
	for i, c := range src {
		if c != 0 {
			t.Fatalf("source byte %d survived NewCredentials: %q", i, src)
		}
	}
	if !creds.Secret.IsAlive() {
		t.Fatal("credential is not alive after construction")
	}
	if got := credentialPlaintext(t, creds); got != "correct horse battery staple" {
		t.Fatalf("credential holds %q", got)
	}
}

func TestNewCredentials_RefusesEmpty(t *testing.T) {
	creds, err := NewCredentials(context.Background(), nil)
	if err == nil {
		t.Fatalf("NewCredentials(context.Background(), nil) = %+v, nil; want an error", creds)
	}
	if creds.Secret != nil {
		t.Fatal("a refused credential must carry no secret")
	}
}

func TestCredentialsFor_BorrowsTheSource(t *testing.T) {
	src := []byte("borrowed")
	creds, err := CredentialsFor(context.Background(), src)
	if err != nil {
		t.Fatalf("CredentialsFor: %v", err)
	}
	defer DestroyCredentials(creds)
	if string(src) != "borrowed" {
		t.Fatalf("CredentialsFor altered the caller's slice: %q", src)
	}
	// And the credential is a COPY: wiping the caller's slice afterwards
	// changes nothing behind it.
	for i := range src {
		src[i] = 0
	}
	if got := credentialPlaintext(t, creds); got != "borrowed" {
		t.Fatalf("credential aliased the caller's slice: %q", got)
	}
}

func TestCredentialsFor_EmptyIsNoCredentials(t *testing.T) {
	for _, src := range [][]byte{nil, {}} {
		creds, err := CredentialsFor(context.Background(), src)
		if err != nil {
			t.Fatalf("CredentialsFor(context.Background(), %v): %v", src, err)
		}
		if creds.Secret != nil {
			t.Fatalf("CredentialsFor(context.Background(), %v) built a secret from nothing", src)
		}
		// The zero credential is refused by a strategy that authenticates:
		// "no credentials supplied" is not a passphrase.
		strategy := NewDeferredSoftwareStrategy()
		if _, err := strategy.SealRootKey(context.Background(), make([]byte, 32), creds); err == nil {
			t.Fatal("a deferred passphrase strategy accepted the zero credential")
		}
	}
}

func TestMustCredentials_PanicsWhereNewCredentialsErrors(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustCredentials(context.Background(), nil) must panic")
		}
	}()
	_ = MustCredentials(context.Background(), nil)
}

func TestMustCredentials_RoundTrip(t *testing.T) {
	creds := MustCredentials(context.Background(), []byte("fixture"))
	defer DestroyCredentials(creds)
	if got := credentialPlaintext(t, creds); got != "fixture" {
		t.Fatalf("MustCredentials holds %q", got)
	}
}

func TestDestroyCredentials_ReleasesTheSecret(t *testing.T) {
	creds, err := NewCredentials(context.Background(), []byte("released"))
	if err != nil {
		t.Fatalf("NewCredentials: %v", err)
	}
	DestroyCredentials(creds)
	if creds.Secret.IsAlive() {
		t.Fatal("credential is still alive after DestroyCredentials")
	}
	if _, err := creds.Secret.Open(t.Context()); !errors.Is(err, enclave.ErrEnclaveDestroyed) {
		t.Fatalf("Open after DestroyCredentials = %v, want ErrEnclaveDestroyed", err)
	}
	// Idempotent, like the Destroy it wraps: a second release is a no-op.
	DestroyCredentials(creds)
}

func TestDestroyCredentials_ZeroIsNoOp(t *testing.T) {
	// The zero value means "no credentials supplied"; releasing it must
	// neither panic nor touch anything.
	DestroyCredentials(Credentials{})
}

// credentialPlaintext copies the credential's secret out under the enclave
// borrow. sealtest.Plaintext is the exported form; pkg/seal cannot import it
// without a cycle.
func credentialPlaintext(t *testing.T, creds Credentials) string {
	t.Helper()
	opened, err := creds.Secret.Open(t.Context())
	if err != nil {
		t.Fatalf("open credential: %v", err)
	}
	defer func() {
		if err := opened.Destroy(); err != nil {
			t.Errorf("release opened credential: %v", err)
		}
	}()
	var out []byte
	if err := opened.WithBytes(func(view []byte) error {
		out = append([]byte(nil), view...)
		return nil
	}); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	return string(out)
}
