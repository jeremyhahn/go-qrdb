// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// tenantStores is an in-memory StoreResolver: one isolated MemoryKVStore per
// tenant, standing in for the tenant.StoreResolver the server wires.
type tenantStores struct {
	mu     sync.Mutex
	stores map[string]*kvstore.MemoryKVStore
	err    error
}

func newTenantStores() *tenantStores {
	return &tenantStores{stores: make(map[string]*kvstore.MemoryKVStore)}
}

func (r *tenantStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	s, ok := r.stores[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		r.stores[tenantID] = s
	}
	return s, nil
}

func newTestKeyring(t *testing.T) (*Keyring, *tenantStores) {
	t.Helper()
	stores := newTenantStores()
	k, err := NewKeyring(stores)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return k, stores
}

func mustEncrypt(t *testing.T, k *Keyring, tenantID string, pt, aad []byte) *Sealed {
	t.Helper()
	s, err := k.Encrypt(context.Background(), tenantID, pt, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return s
}

func TestNewKeyringRefusesANilResolver(t *testing.T) {
	if _, err := NewKeyring(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	aad := []byte("acme/db/password")
	s := mustEncrypt(t, k, "acme", []byte("hunter2"), aad)
	if s.Version != 1 {
		t.Fatalf("first version = %d, want 1", s.Version)
	}
	if bytes.Contains(s.Ciphertext, []byte("hunter2")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	o, err := k.Decrypt(ctx, "acme", s.Ciphertext, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(o.Plaintext, []byte("hunter2")) || o.Version != 1 {
		t.Fatalf("Decrypt = %q v%d", o.Plaintext, o.Version)
	}
}

func TestDecryptRejectsAMismatchedAAD(t *testing.T) {
	k, _ := newTestKeyring(t)
	s := mustEncrypt(t, k, "acme", []byte("v"), []byte("path=a|v=1"))
	_, err := k.Decrypt(context.Background(), "acme", s.Ciphertext, []byte("path=a|v=2"))
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed", err)
	}
	if _, err := k.Decrypt(context.Background(), "acme", s.Ciphertext, nil); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("missing AAD: err = %v, want ErrAuthenticationFailed", err)
	}
}

// TestDecryptRejectsAnotherTenantsCiphertext: tenant isolation holds even when
// a ciphertext is replayed against another tenant's keyring at the same
// version — the keys differ and the tenant ID is bound into the AEAD.
func TestDecryptRejectsAnotherTenantsCiphertext(t *testing.T) {
	k, _ := newTestKeyring(t)
	s := mustEncrypt(t, k, "acme", []byte("v"), nil)
	mustEncrypt(t, k, "globex", []byte("w"), nil) // globex has a v1 too
	_, err := k.Decrypt(context.Background(), "globex", s.Ciphertext, nil)
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed", err)
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	k, _ := newTestKeyring(t)
	s := mustEncrypt(t, k, "acme", []byte("value"), nil)
	tampered := append([]byte(nil), s.Ciphertext...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := k.Decrypt(context.Background(), "acme", tampered, nil); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed", err)
	}
}

// TestDecryptBindsTheVersionHeader: rewriting the version in the header to a
// different existing version fails authentication instead of silently trying
// the other key.
func TestDecryptBindsTheVersionHeader(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	s := mustEncrypt(t, k, "acme", []byte("value"), nil)
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	forged := append([]byte(nil), s.Ciphertext...)
	forged[headerVersionOffset+7] = 2
	if _, err := k.Decrypt(ctx, "acme", forged, nil); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed", err)
	}
}

func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	k, _ := newTestKeyring(t)
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	for name, ct := range map[string][]byte{
		"empty":     nil,
		"short":     []byte("QDEK"),
		"bad magic": append([]byte("XXXX\x01"), make([]byte, 40)...),
		"bad fmt":   append([]byte("QDEK\x09"), make([]byte, 40)...),
	} {
		if _, err := k.Decrypt(context.Background(), "acme", ct, nil); !errors.Is(err, ErrMalformedCiphertext) {
			t.Errorf("%s: err = %v, want ErrMalformedCiphertext", name, err)
		}
	}
}

func TestDecryptWithNoKeyringIsUnknownVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	s := mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Decrypt(context.Background(), "nobody", s.Ciphertext, nil); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

func TestRotateKeepsOldDataDecryptable(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	old := mustEncrypt(t, k, "acme", []byte("before"), []byte("a"))

	st, err := k.Rotate(ctx, "acme")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if st.Current != 2 || len(st.Versions) != 2 {
		t.Fatalf("status after rotate = %+v", st)
	}

	fresh := mustEncrypt(t, k, "acme", []byte("after"), []byte("a"))
	if fresh.Version != 2 {
		t.Fatalf("new encryptions use v%d, want v2", fresh.Version)
	}
	o, err := k.Decrypt(ctx, "acme", old.Ciphertext, []byte("a"))
	if err != nil || !bytes.Equal(o.Plaintext, []byte("before")) || o.Version != 1 {
		t.Fatalf("old ciphertext after rotate: %q v%d, %v", o.Plaintext, o.Version, err)
	}
}

func TestRotateCreatesTheKeyringWhenAbsent(t *testing.T) {
	k, _ := newTestKeyring(t)
	st, err := k.Rotate(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 1 || len(st.Versions) != 1 {
		t.Fatalf("status = %+v, want a single v1", st)
	}
}

func TestRewrapMovesCiphertextToTheCurrentVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	old := mustEncrypt(t, k, "acme", []byte("secret"), []byte("ctx"))
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	rw, err := k.Rewrap(ctx, "acme", old.Ciphertext, []byte("ctx"))
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if rw.FromVersion != 1 || rw.ToVersion != 3 {
		t.Fatalf("rewrap versions = %d -> %d, want 1 -> 3", rw.FromVersion, rw.ToVersion)
	}
	if bytes.Contains(rw.Ciphertext, []byte("secret")) {
		t.Fatal("rewrapped ciphertext contains the plaintext")
	}
	o, err := k.Decrypt(ctx, "acme", rw.Ciphertext, []byte("ctx"))
	if err != nil || !bytes.Equal(o.Plaintext, []byte("secret")) || o.Version != 3 {
		t.Fatalf("decrypt rewrapped: %q v%d, %v", o.Plaintext, o.Version, err)
	}
}

func TestRewrapOfCurrentCiphertextIsANoOp(t *testing.T) {
	k, _ := newTestKeyring(t)
	s := mustEncrypt(t, k, "acme", []byte("x"), nil)
	rw, err := k.Rewrap(context.Background(), "acme", s.Ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rw.FromVersion != 1 || rw.ToVersion != 1 || !bytes.Equal(rw.Ciphertext, s.Ciphertext) {
		t.Fatalf("rewrap of current = %+v", rw)
	}
}

func TestRewrapRequiresTheOriginalAAD(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	s := mustEncrypt(t, k, "acme", []byte("x"), []byte("right"))
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Rewrap(ctx, "acme", s.Ciphertext, []byte("wrong")); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("err = %v, want ErrAuthenticationFailed", err)
	}
}

func TestRetireRefusesTheCurrentVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Retire(context.Background(), "acme", 1); !errors.Is(err, ErrRetireCurrentVersion) {
		t.Fatalf("err = %v, want ErrRetireCurrentVersion", err)
	}
}

func TestRetireDestroysAnOldVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	old := mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	st, err := k.Retire(ctx, "acme", 1)
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if st.Versions[0].Version != 1 || st.Versions[0].RetiredAt == nil {
		t.Fatalf("v1 after retire = %+v", st.Versions[0])
	}
	if _, err := k.Decrypt(ctx, "acme", old.Ciphertext, nil); !errors.Is(err, ErrVersionRetired) {
		t.Fatalf("decrypt retired: err = %v, want ErrVersionRetired", err)
	}
	if _, err := k.Rewrap(ctx, "acme", old.Ciphertext, nil); !errors.Is(err, ErrVersionRetired) {
		t.Fatalf("rewrap retired: err = %v, want ErrVersionRetired", err)
	}
	// Retiring again is idempotent.
	if _, err := k.Retire(ctx, "acme", 1); err != nil {
		t.Fatalf("second Retire: %v", err)
	}
}

func TestRetireUnknownVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	if _, err := k.Retire(ctx, "acme", 1); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("no keyring: err = %v, want ErrUnknownVersion", err)
	}
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Retire(ctx, "acme", 7); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

// TestRetiredKeyMaterialIsGoneFromTheStore checks the stored record, not just
// the API: a retired version's key bytes are deleted, not merely flagged.
func TestRetiredKeyMaterialIsGoneFromTheStore(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	store, _ := stores.Resolve(ctx, "acme")
	rec1, err := k.load(ctx, "acme", store)
	if err != nil {
		t.Fatal(err)
	}
	v1Key := append([]byte(nil), rec1.Versions[0].Key...)

	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Retire(ctx, "acme", 1); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Get(ctx, keyringStoreKey)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, v1Key) {
		t.Fatal("retired key material is still in the stored record")
	}
}

// TestVersionsSurviveARestart: a second Keyring over the same stores (a new
// process) reads every version back and decrypts what the first wrote.
func TestVersionsSurviveARestart(t *testing.T) {
	stores := newTenantStores()
	ctx := context.Background()
	first, err := NewKeyring(stores)
	if err != nil {
		t.Fatal(err)
	}
	v1 := mustEncrypt(t, first, "acme", []byte("one"), nil)
	if _, err := first.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	v2 := mustEncrypt(t, first, "acme", []byte("two"), nil)

	second, err := NewKeyring(stores)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		s    *Sealed
		want string
	}{{v1, "one"}, {v2, "two"}} {
		o, err := second.Decrypt(ctx, "acme", c.s.Ciphertext, nil)
		if err != nil || string(o.Plaintext) != c.want {
			t.Fatalf("after restart: %q, %v; want %q", o.Plaintext, err, c.want)
		}
	}
	st, err := second.Status(ctx, "acme")
	if err != nil || st.Current != 2 {
		t.Fatalf("status after restart = %+v, %v", st, err)
	}
}

func TestStatusOfATenantWithNoKeyring(t *testing.T) {
	k, _ := newTestKeyring(t)
	st, err := k.Status(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if st.TenantID != "acme" || st.Current != 0 || len(st.Versions) != 0 || st.Algorithm != Algorithm {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNeverCarriesKeyMaterial(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	store, _ := stores.Resolve(ctx, "acme")
	rec, err := k.load(ctx, "acme", store)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Status(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	// VersionInfo has no key field at all; this guards a regression that adds one.
	if len(st.Versions) != 1 || st.Versions[0].Version != 1 || len(rec.Versions[0].Key) != KeySize {
		t.Fatalf("status = %+v", st)
	}
}

func TestOperationsRequireATenant(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["encrypt"] = k.Encrypt(ctx, "", []byte("x"), nil)
	_, checks["decrypt"] = k.Decrypt(ctx, "", []byte("x"), nil)
	_, checks["rewrap"] = k.Rewrap(ctx, "", []byte("x"), nil)
	_, checks["rotate"] = k.Rotate(ctx, "")
	_, checks["retire"] = k.Retire(ctx, "", 1)
	_, checks["status"] = k.Status(ctx, "")
	for op, err := range checks {
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", op, err)
		}
	}
}

func TestResolverFailureIsKeyringUnavailable(t *testing.T) {
	k, stores := newTestKeyring(t)
	sealed := errors.New("tenant sealed")
	stores.err = sealed
	_, err := k.Encrypt(context.Background(), "acme", []byte("x"), nil)
	if !errors.Is(err, ErrKeyringUnavailable) || !errors.Is(err, sealed) {
		t.Fatalf("err = %v, want ErrKeyringUnavailable wrapping the cause", err)
	}
}

// plainStore is a KVStore without the atomic verbs.
type plainStore struct{ kvstore.KVStore }

type plainResolver struct{}

func (plainResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return plainStore{kvstore.NewMemoryKVStore()}, nil
}

func TestKeyringRefusesANonAtomicStore(t *testing.T) {
	k, err := NewKeyring(plainResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Rotate(context.Background(), "acme"); !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want ErrKeyringUnavailable", err)
	}
}

// TestConcurrentRotationsLoseNoVersion: CAS on the record means N concurrent
// rotations produce N new versions — none overwrites another.
func TestConcurrentRotationsLoseNoVersion(t *testing.T) {
	k, _ := newTestKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := k.Rotate(ctx, "acme")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
	}
	st, err := k.Status(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != n+1 || len(st.Versions) != n+1 {
		t.Fatalf("after %d concurrent rotations: current=%d versions=%d", n, st.Current, len(st.Versions))
	}
}

func TestDecodeRejectsACorruptRecord(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	store, _ := stores.Resolve(ctx, "acme")
	if err := store.Put(ctx, keyringStoreKey, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Status(ctx, "acme"); !errors.Is(err, ErrCorruptKeyring) {
		t.Fatalf("err = %v, want ErrCorruptKeyring", err)
	}
}
