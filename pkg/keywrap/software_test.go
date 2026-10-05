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
	"crypto/cipher"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestProvider returns a SoftwareProvider confined to a fresh directory
// under t.TempDir().
func newTestProvider(t *testing.T) (*SoftwareProvider, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := NewSoftwareProvider(dir)
	if err != nil {
		t.Fatalf("NewSoftwareProvider: %v", err)
	}
	return p, dir
}

// writeKeyFile writes n random-looking (but deterministic, test-only)
// bytes to a file named name inside dir and returns name -- the bare file
// name every keyRef must now be (go-qrdb review #B4).
func writeKeyFile(t *testing.T, dir, name string, n int, fill byte) string {
	t.Helper()
	data := make([]byte, n)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return name
}

func TestSoftwareProvider_ProviderID(t *testing.T) {
	p, _ := newTestProvider(t)
	if p.ProviderID() != "software" {
		t.Fatalf("ProviderID() = %q, want %q", p.ProviderID(), "software")
	}
	if p.ProviderID() != SoftwareProviderID {
		t.Fatalf("ProviderID() = %q, want constant %q", p.ProviderID(), SoftwareProviderID)
	}
}

func TestNewSoftwareProvider_RefusesUnopenableDir(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := NewSoftwareProvider(filepath.Join(notADir, "sub"))
	var dirErr *KeyDirectoryError
	if !errors.As(err, &dirErr) {
		t.Fatalf("NewSoftwareProvider err = %v, want *KeyDirectoryError", err)
	}
	if dirErr.Dir == "" {
		t.Fatal("KeyDirectoryError.Dir is empty")
	}
}

func TestSoftwareProvider_WrapUnwrapRoundTrip(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x01)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if len(wrapped) <= len(dek) {
		t.Fatalf("wrapped length %d, want > plaintext length %d (nonce + tag overhead)", len(wrapped), len(dek))
	}

	got, err := p.Unwrap(context.Background(), keyName, wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("Unwrap() = %q, want %q", got, dek)
	}
}

func TestSoftwareProvider_WrapProducesDistinctCiphertextEachCall(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x02)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	a, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap (1): %v", err)
	}
	b, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap (2): %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two Wrap calls over the same dek produced identical ciphertext -- nonce reuse")
	}
}

func TestSoftwareProvider_RefusesShortKeyFile(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 31, 0x03)

	_, err := p.Wrap(context.Background(), keyName, make([]byte, 32))
	var accessErr *KeyAccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("Wrap err = %v, want *KeyAccessError", err)
	}
}

func TestSoftwareProvider_RefusesLongKeyFile(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 33, 0x04)

	_, err := p.Unwrap(context.Background(), keyName, make([]byte, 28))
	var accessErr *KeyAccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("Unwrap err = %v, want *KeyAccessError", err)
	}
}

// TestSoftwareProvider_RefusesArbitrarilyLargeKeyFile proves a key
// reference naming a file far larger than 32 bytes is refused without
// reading the whole thing -- the DoS this provider must not be a lever
// for (go-qrdb review #B4). 1 MiB stands in for "unbounded"; the fix
// reads at most 33 bytes regardless.
func TestSoftwareProvider_RefusesArbitrarilyLargeKeyFile(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 1<<20, 0x05)

	_, err := p.Wrap(context.Background(), keyName, make([]byte, 32))
	var accessErr *KeyAccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("Wrap err = %v, want *KeyAccessError", err)
	}
}

func TestSoftwareProvider_UnwrapWithDifferentKeyFails(t *testing.T) {
	p, dir := newTestProvider(t)
	keyA := writeKeyFile(t, dir, "key-a", 32, 0x10)
	keyB := writeKeyFile(t, dir, "key-b", 32, 0x20)
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
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x30)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the authentication tag

	_, err = p.Unwrap(context.Background(), keyName, tampered)
	var unwrapErr *UnwrapFailedError
	if !errors.As(err, &unwrapErr) {
		t.Fatalf("Unwrap with tampered ciphertext err = %v, want *UnwrapFailedError", err)
	}
}

func TestSoftwareProvider_UnwrapWithTruncatedPayloadFails(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x40)

	_, err := p.Unwrap(context.Background(), keyName, []byte{0x01, 0x02})
	var unwrapErr *UnwrapFailedError
	if !errors.As(err, &unwrapErr) {
		t.Fatalf("Unwrap with truncated payload err = %v, want *UnwrapFailedError", err)
	}
}

func TestSoftwareProvider_MissingKeyFile(t *testing.T) {
	p, _ := newTestProvider(t)

	_, err := p.Wrap(context.Background(), "does-not-exist.key", make([]byte, 32))
	var accessErr *KeyAccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("Wrap with missing key file err = %v, want *KeyAccessError", err)
	}
}

// TestSoftwareProvider_RefusesPathSeparator proves a keyRef naming
// anything other than a bare file name inside the configured directory --
// here, an attempt to traverse into a parent directory -- is refused with
// the SAME detail-free KeyAccessError as every other key-access failure,
// never a filesystem error that would confirm or deny what exists outside
// the confined directory (go-qrdb review #B4).
func TestSoftwareProvider_RefusesPathSeparator(t *testing.T) {
	p, _ := newTestProvider(t)

	for _, ref := range []string{
		"../escape.key",
		"sub/backup.key",
		"sub\\backup.key",
		"",
		".",
		"..",
	} {
		_, err := p.Wrap(context.Background(), ref, make([]byte, 32))
		var accessErr *KeyAccessError
		if !errors.As(err, &accessErr) {
			t.Fatalf("Wrap(%q) err = %v, want *KeyAccessError", ref, err)
		}
	}
}

// TestSoftwareProvider_RefusesDirectoryAsKeyRef proves a keyRef naming a
// subdirectory of the confined key directory -- rather than a regular
// file -- is refused the same way, never opened as if it were a key.
func TestSoftwareProvider_RefusesDirectoryAsKeyRef(t *testing.T) {
	p, dir := newTestProvider(t)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	_, err := p.Wrap(context.Background(), "subdir", make([]byte, 32))
	var accessErr *KeyAccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("Wrap(subdir) err = %v, want *KeyAccessError", err)
	}
}

// failingReader always fails Read -- used to force Wrap's nonce
// generation branch, otherwise unreachable against the real entropy
// source.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy source failed") }

func TestSoftwareProvider_WrapNonceGenerationFailureIsWrapped(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x91)

	prev := randReader
	randReader = failingReader{}
	t.Cleanup(func() { randReader = prev })

	_, err := p.Wrap(context.Background(), keyName, make([]byte, 32))
	var wrapErr *WrapFailedError
	if !errors.As(err, &wrapErr) {
		t.Fatalf("Wrap err = %v, want *WrapFailedError", err)
	}
}

func TestSoftwareProvider_GCMConstructionFailureIsWrapped(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x92)

	prev := newGCM
	newGCM = func(key []byte) (cipher.AEAD, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { newGCM = prev })

	_, err := p.Wrap(context.Background(), keyName, make([]byte, 32))
	var wrapErr *WrapFailedError
	if !errors.As(err, &wrapErr) {
		t.Fatalf("Wrap err = %v, want *WrapFailedError", err)
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
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x70)
	var wiped [][]byte
	swapWipeFn(t, func(b []byte) {
		cp := append([]byte(nil), b...)
		wiped = append(wiped, cp)
	})

	if _, err := p.Wrap(context.Background(), keyName, make([]byte, 32)); err != nil {
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
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "backup.key", 32, 0x80)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]
	wrapped, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	var wipeCalls int
	swapWipeFn(t, func(b []byte) { wipeCalls++ })

	if _, err := p.Unwrap(context.Background(), keyName, wrapped); err != nil {
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
	p, dir := newTestProvider(t)
	keyA := writeKeyFile(t, dir, "key-a", 32, 0x50)
	keyB := writeKeyFile(t, dir, "key-b", 32, 0x60)
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

// TestSoftwareProvider_WrapUnwrapAfterCloseRefused proves a Wrap/Unwrap
// call reaching an already-Closed provider gets a typed
// *ProviderClosedError, not a silent reopen and not the generic
// *KeyAccessError every unrelated key-lookup failure folds into -- the
// lifecycle mistake (something called Wrap/Unwrap after the owner's own
// Close) reports as itself (go-qrdb security review,
// backup-encrypt-default round 4).
func TestSoftwareProvider_WrapUnwrapAfterCloseRefused(t *testing.T) {
	p, dir := newTestProvider(t)
	keyName := writeKeyFile(t, dir, "k", 32, 0x70)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), keyName, dek)
	if err != nil {
		t.Fatalf("Wrap before Close: %v", err)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = p.Wrap(context.Background(), keyName, dek)
	var closedErr *ProviderClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("Wrap after Close: err = %T, want *ProviderClosedError: %v", err, err)
	}

	_, err = p.Unwrap(context.Background(), keyName, wrapped)
	if !errors.As(err, &closedErr) {
		t.Fatalf("Unwrap after Close: err = %T, want *ProviderClosedError: %v", err, err)
	}
}

// TestSoftwareProvider_RotationNewVersionIsANewRefAndOldStillWorks proves
// the documented rotation discipline: a new key version is a NEW file and
// a new ref, never an in-place overwrite of the old one, and a wrapped
// DEK produced under the old ref keeps unwrapping after the new ref
// exists alongside it (go-qrdb review #B4).
func TestSoftwareProvider_RotationNewVersionIsANewRefAndOldStillWorks(t *testing.T) {
	p, dir := newTestProvider(t)
	v1 := writeKeyFile(t, dir, "backup-v1.key", 32, 0xC0)
	dek := []byte("0123456789abcdef0123456789abcdef")[:32]

	wrapped, err := p.Wrap(context.Background(), v1, dek)
	if err != nil {
		t.Fatalf("Wrap under v1: %v", err)
	}

	// Rotation: v2 is a NEW file/ref, added alongside v1, never an
	// overwrite of it.
	v2 := writeKeyFile(t, dir, "backup-v2.key", 32, 0xD0)
	if v1 == v2 {
		t.Fatal("v1 and v2 refs must differ")
	}

	// The backup made under v1 still restores: v1's file and ref are
	// untouched by v2's arrival.
	got, err := p.Unwrap(context.Background(), v1, wrapped)
	if err != nil {
		t.Fatalf("Unwrap under v1 after v2 exists: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("Unwrap(v1) = %q, want %q", got, dek)
	}

	// v2 is usable independently for new backups.
	if _, err := p.Wrap(context.Background(), v2, dek); err != nil {
		t.Fatalf("Wrap under v2: %v", err)
	}
}
