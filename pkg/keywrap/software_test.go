// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package keywrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeKeyFile writes n random-looking (but deterministic, test-only)
// bytes to a fresh file under t.TempDir() and returns its path.
func writeKeyFile(t *testing.T, n int, fill byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.key")
	data := make([]byte, n)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestSoftwareProvider_ProviderID(t *testing.T) {
	p := NewSoftwareProvider()
	if p.ProviderID() != "software" {
		t.Fatalf("ProviderID() = %q, want %q", p.ProviderID(), "software")
	}
	if p.ProviderID() != SoftwareProviderID {
		t.Fatalf("ProviderID() = %q, want constant %q", p.ProviderID(), SoftwareProviderID)
	}
}

func TestSoftwareProvider_WrapUnwrapRoundTrip(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x01)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyPath, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if len(wrapped) <= len(dek) {
		t.Fatalf("wrapped length %d, want > plaintext length %d (nonce + tag overhead)", len(wrapped), len(dek))
	}

	got, err := p.Unwrap(context.Background(), keyPath, wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("Unwrap() = %q, want %q", got, dek)
	}
}

func TestSoftwareProvider_WrapProducesDistinctCiphertextEachCall(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x02)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	a, err := p.Wrap(context.Background(), keyPath, dek)
	if err != nil {
		t.Fatalf("Wrap (1): %v", err)
	}
	b, err := p.Wrap(context.Background(), keyPath, dek)
	if err != nil {
		t.Fatalf("Wrap (2): %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two Wrap calls over the same dek produced identical ciphertext -- nonce reuse")
	}
}

func TestSoftwareProvider_RefusesShortKeyFile(t *testing.T) {
	keyPath := writeKeyFile(t, 31, 0x03)
	p := NewSoftwareProvider()

	_, err := p.Wrap(context.Background(), keyPath, make([]byte, 32))
	var lenErr *InvalidKeyLengthError
	if !errors.As(err, &lenErr) {
		t.Fatalf("Wrap err = %v, want *InvalidKeyLengthError", err)
	}
	if lenErr.Length != 31 {
		t.Fatalf("lenErr.Length = %d, want 31", lenErr.Length)
	}
}

func TestSoftwareProvider_RefusesLongKeyFile(t *testing.T) {
	keyPath := writeKeyFile(t, 33, 0x04)
	p := NewSoftwareProvider()

	_, err := p.Unwrap(context.Background(), keyPath, make([]byte, 28))
	var lenErr *InvalidKeyLengthError
	if !errors.As(err, &lenErr) {
		t.Fatalf("Unwrap err = %v, want *InvalidKeyLengthError", err)
	}
	if lenErr.Length != 33 {
		t.Fatalf("lenErr.Length = %d, want 33", lenErr.Length)
	}
}

func TestSoftwareProvider_UnwrapWithDifferentKeyFails(t *testing.T) {
	keyA := writeKeyFile(t, 32, 0x10)
	keyB := writeKeyFile(t, 32, 0x20)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyA, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	_, err = p.Unwrap(context.Background(), keyB, wrapped)
	var unwrapErr *UnwrapFailedError
	if !errors.As(err, &unwrapErr) {
		t.Fatalf("Unwrap with wrong key err = %v, want *UnwrapFailedError", err)
	}
}

func TestSoftwareProvider_UnwrapWithTamperedCiphertextFails(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x30)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyPath, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the authentication tag

	_, err = p.Unwrap(context.Background(), keyPath, tampered)
	var unwrapErr *UnwrapFailedError
	if !errors.As(err, &unwrapErr) {
		t.Fatalf("Unwrap with tampered ciphertext err = %v, want *UnwrapFailedError", err)
	}
}

func TestSoftwareProvider_UnwrapWithTruncatedPayloadFails(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x40)
	p := NewSoftwareProvider()

	_, err := p.Unwrap(context.Background(), keyPath, []byte{0x01, 0x02})
	var unwrapErr *UnwrapFailedError
	if !errors.As(err, &unwrapErr) {
		t.Fatalf("Unwrap with truncated payload err = %v, want *UnwrapFailedError", err)
	}
}

func TestSoftwareProvider_MissingKeyFile(t *testing.T) {
	p := NewSoftwareProvider()
	missing := filepath.Join(t.TempDir(), "does-not-exist.key")

	_, err := p.Wrap(context.Background(), missing, make([]byte, 32))
	var keyFileErr *KeyFileError
	if !errors.As(err, &keyFileErr) {
		t.Fatalf("Wrap with missing key file err = %v, want *KeyFileError", err)
	}
	if keyFileErr.Path != missing {
		t.Fatalf("keyFileErr.Path = %q, want %q", keyFileErr.Path, missing)
	}
}

// swapWipeFn installs fn as wipeFn for the duration of the calling test,
// restoring the original on cleanup -- the same indirection
// sdk/go/transport/embedded's own wipeFn swap uses, because the wiped
// buffer is a local no caller retains: the swap is the only way to observe
// that the wipe happened at all.
func swapWipeFn(t *testing.T, fn func([]byte)) {
	t.Helper()
	prev := wipeFn
	wipeFn = fn
	t.Cleanup(func() { wipeFn = prev })
}

func TestSoftwareProvider_WrapWipesItsKeyCopy(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x70)
	var wiped [][]byte
	swapWipeFn(t, func(b []byte) {
		cp := append([]byte(nil), b...)
		wiped = append(wiped, cp)
	})

	p := NewSoftwareProvider()
	if _, err := p.Wrap(context.Background(), keyPath, make([]byte, 32)); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if len(wiped) != 1 {
		t.Fatalf("wipeFn called %d times, want 1", len(wiped))
	}
	if len(wiped[0]) != 32 {
		t.Fatalf("wiped buffer length = %d, want 32 (the key, not the dek or wrapped output)", len(wiped[0]))
	}
}

func TestSoftwareProvider_UnwrapWipesItsKeyCopy(t *testing.T) {
	keyPath := writeKeyFile(t, 32, 0x80)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]
	wrapped, err := p.Wrap(context.Background(), keyPath, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	var wipeCalls int
	swapWipeFn(t, func(b []byte) { wipeCalls++ })

	if _, err := p.Unwrap(context.Background(), keyPath, wrapped); err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if wipeCalls != 1 {
		t.Fatalf("wipeFn called %d times, want 1", wipeCalls)
	}
}

func TestSoftwareProvider_KeyFileNeverCachedAcrossCalls(t *testing.T) {
	// Two sequential Wrap calls against two DIFFERENT key files must each
	// use their OWN file's key -- proving nothing is cached from the
	// first call into the second.
	keyA := writeKeyFile(t, 32, 0x50)
	keyB := writeKeyFile(t, 32, 0x60)
	p := NewSoftwareProvider()
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrappedA, err := p.Wrap(context.Background(), keyA, dek)
	if err != nil {
		t.Fatalf("Wrap(keyA): %v", err)
	}
	// keyB cannot unwrap what keyA wrapped.
	if _, err := p.Unwrap(context.Background(), keyB, wrappedA); err == nil {
		t.Fatal("Unwrap(keyB, wrappedA) succeeded, want failure (keys must not be shared/cached across calls)")
	}
	// keyA can.
	got, err := p.Unwrap(context.Background(), keyA, wrappedA)
	if err != nil {
		t.Fatalf("Unwrap(keyA, wrappedA): %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("Unwrap(keyA, wrappedA) = %q, want %q", got, dek)
	}
}
