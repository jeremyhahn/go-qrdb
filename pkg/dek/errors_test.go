// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestErrorsMatchTheirClassAndNameTheirSubject pins the contract transports
// rely on: each typed error matches exactly its sentinel class, unwraps to its
// cause, and says what it is about.
func TestErrorsMatchTheirClassAndNameTheirSubject(t *testing.T) {
	cause := errors.New("cause")
	cases := []struct {
		err      error
		class    error
		contains string
		unwraps  bool
	}{
		{&InvalidInputError{Op: "Encrypt", Field: "tenant ID"}, ErrInvalidInput, "tenant ID", false},
		{&CrossTenantError{CallerTenant: "a", RequestedTenant: "b"}, ErrCrossTenant, `"b"`, false},
		{&MalformedCiphertextError{Reason: "too short"}, ErrMalformedCiphertext, "too short", false},
		{&AuthenticationFailedError{TenantID: "a", Version: 3, Err: cause}, ErrAuthenticationFailed, "version 3", true},
		{&UnknownVersionError{TenantID: "a", Version: 9}, ErrUnknownVersion, "9", false},
		{&VersionRetiredError{TenantID: "a", Version: 2}, ErrVersionRetired, "retired", false},
		{&RetireCurrentVersionError{TenantID: "a", Version: 4}, ErrRetireCurrentVersion, "rotate first", false},
		{&KeyringUnavailableError{TenantID: "a", Err: cause}, ErrKeyringUnavailable, "cause", true},
		{&CorruptKeyringError{TenantID: "a", Err: cause}, ErrCorruptKeyring, "corrupt", true},
		{&ConcurrentUpdateError{TenantID: "a", Attempts: 16}, ErrConcurrentUpdate, "16", false},
		{&CryptoError{Op: "encrypt", Err: cause}, ErrCrypto, "encrypt", true},
	}
	all := []error{
		ErrInvalidInput, ErrCrossTenant, ErrMalformedCiphertext, ErrAuthenticationFailed,
		ErrUnknownVersion, ErrVersionRetired, ErrRetireCurrentVersion, ErrKeyringUnavailable,
		ErrCorruptKeyring, ErrConcurrentUpdate, ErrCrypto,
	}
	for _, c := range cases {
		if !strings.Contains(c.err.Error(), c.contains) {
			t.Errorf("%T: %q does not mention %q", c.err, c.err.Error(), c.contains)
		}
		for _, s := range all {
			if got := errors.Is(c.err, s); got != (s == c.class) {
				t.Errorf("%T: errors.Is(%v) = %v", c.err, s, got)
			}
		}
		if got := errors.Is(c.err, cause); got != c.unwraps {
			t.Errorf("%T: unwraps to cause = %v, want %v", c.err, got, c.unwraps)
		}
	}
	se := &StoreError{TenantID: "a", Op: "read", Err: cause}
	if !errors.Is(se, cause) || !strings.Contains(se.Error(), "read") {
		t.Errorf("StoreError = %q", se.Error())
	}
	if (&AtomicStoreRequiredError{}).Error() == "" {
		t.Error("AtomicStoreRequiredError has no message")
	}
	if (&CorruptKeyringError{TenantID: "a"}).Error() == "" {
		t.Error("CorruptKeyringError without cause has no message")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestKeyGenerationFailureIsACryptoError(t *testing.T) {
	k, _ := newTestKeyring(t)
	k.random = failingReader{}
	if _, err := k.Encrypt(context.Background(), "acme", []byte("x"), nil); !errors.Is(err, ErrCrypto) {
		t.Fatalf("err = %v, want ErrCrypto", err)
	}
}

// faultStore fails the chosen verb.
type faultStore struct {
	*kvstore.MemoryKVStore
	getErr, writeErr error
	casLoses         bool
}

func (f *faultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.MemoryKVStore.Get(ctx, key)
}

func (f *faultStore) PutIfAbsent(ctx context.Context, key string, v []byte) (bool, error) {
	if f.writeErr != nil {
		return false, f.writeErr
	}
	return f.MemoryKVStore.PutIfAbsent(ctx, key, v)
}

func (f *faultStore) CompareAndSwap(ctx context.Context, key string, old, next []byte) (bool, error) {
	if f.writeErr != nil {
		return false, f.writeErr
	}
	if f.casLoses {
		return false, nil
	}
	return f.MemoryKVStore.CompareAndSwap(ctx, key, old, next)
}

type fixedResolver struct{ s kvstore.KVStore }

func (r fixedResolver) Resolve(context.Context, string) (kvstore.KVStore, error) { return r.s, nil }

func newFaultKeyring(t *testing.T) (*Keyring, *faultStore) {
	t.Helper()
	fs := &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	k, err := NewKeyring(fixedResolver{fs})
	if err != nil {
		t.Fatal(err)
	}
	return k, fs
}

func TestStoreReadFailureIsReported(t *testing.T) {
	k, fs := newFaultKeyring(t)
	boom := errors.New("raft unavailable")
	fs.getErr = boom
	ctx := context.Background()
	for op, err := range map[string]error{
		"status":  second(k.Status(ctx, "acme")),
		"encrypt": second(k.Encrypt(ctx, "acme", []byte("x"), nil)),
		"rotate":  second(k.Rotate(ctx, "acme")),
		"retire":  second(k.Retire(ctx, "acme", 1)),
		"decrypt": second(k.Decrypt(ctx, "acme", validLookingCiphertext(), nil)),
	} {
		var se *StoreError
		if !errors.As(err, &se) || !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want *StoreError wrapping the cause", op, err)
		}
	}
}

func TestStoreWriteFailureIsReported(t *testing.T) {
	k, fs := newFaultKeyring(t)
	boom := errors.New("not leader")
	fs.writeErr = boom
	if _, err := k.Encrypt(context.Background(), "acme", []byte("x"), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write cause", err)
	}
}

func TestSustainedCASLossIsConcurrentUpdate(t *testing.T) {
	k, fs := newFaultKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	fs.casLoses = true
	if _, err := k.Rotate(ctx, "acme"); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("err = %v, want ErrConcurrentUpdate", err)
	}
}

func TestRetireRejectsVersionZero(t *testing.T) {
	k, _ := newTestKeyring(t)
	if _, err := k.Retire(context.Background(), "acme", 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseVersionRejectsZero(t *testing.T) {
	ct := validLookingCiphertext()
	for i := headerVersionOffset; i < headerLen; i++ {
		ct[i] = 0
	}
	if _, err := parseVersion(ct); !errors.Is(err, ErrMalformedCiphertext) {
		t.Fatalf("err = %v, want ErrMalformedCiphertext", err)
	}
}

// TestRecordsViolatingInvariantsAreCorrupt: a record that decodes but breaks
// an invariant (wrong format, no versions, unusable current key) is refused
// rather than used.
func TestRecordsViolatingInvariantsAreCorrupt(t *testing.T) {
	for name, raw := range map[string]string{
		"format":      `{"format":7,"current":1,"versions":[]}`,
		"no versions": `{"format":1,"current":1,"versions":[]}`,
		"no current":  `{"format":1,"current":2,"versions":[{"version":1,"key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}]}`,
		"short key":   `{"format":1,"current":1,"versions":[{"version":1,"key":"AAAA"}]}`,
	} {
		k, stores := newTestKeyring(t)
		ctx := context.Background()
		store, _ := stores.Resolve(ctx, "acme")
		if err := store.Put(ctx, keyringStoreKey, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Status(ctx, "acme"); !errors.Is(err, ErrCorruptKeyring) {
			t.Errorf("%s: err = %v, want ErrCorruptKeyring", name, err)
		}
	}
}

// validLookingCiphertext is a structurally valid v1 ciphertext header with a
// body that will not authenticate under any key.
func validLookingCiphertext() []byte {
	ct := make([]byte, headerLen+gcmOverhead+4)
	copy(ct, ciphertextMagic)
	ct[4] = ciphertextFormat
	ct[headerLen-1] = 1
	return ct
}

func second[T any](_ T, err error) error { return err }
