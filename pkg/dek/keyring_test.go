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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// fakeKeyWrapProvider is a same-shaped test double standing in for a real
// go-xkms hardware or cloud-KMS provider -- or, registered under
// keywrap.SoftwareProviderID, for the built-in provider when a test has no
// need to touch a real key file. It "wraps" by copying bytes verbatim, the
// identical shape pkg/keywrap's own fakeHardwareProvider test double uses.
type fakeKeyWrapProvider struct{ id string }

var _ keywrap.Provider = (*fakeKeyWrapProvider)(nil)

func (p *fakeKeyWrapProvider) ProviderID() string { return p.id }

func (p *fakeKeyWrapProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (p *fakeKeyWrapProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// testKeyWrapRegistry returns a Registry whose "software" entry is the fake
// provider above, so a test gets a fully working Keyring with no filesystem
// dependency -- every call site that used to get a zero-config Keyring
// before key wrapping existed uses this instead.
func testKeyWrapRegistry() keywrap.Registry {
	r := keywrap.NewRegistry()
	_ = r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return &fakeKeyWrapProvider{id: keywrap.SoftwareProviderID}, nil
	})
	return r
}

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
	k, err := NewKeyring(stores, WithKeyWrapRegistry(testKeyWrapRegistry()))
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
	v1Key := append([]byte(nil), rec1.Versions[0].WrappedKey...)

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
	first, err := NewKeyring(stores, WithKeyWrapRegistry(testKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	v1 := mustEncrypt(t, first, "acme", []byte("one"), nil)
	if _, err := first.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	v2 := mustEncrypt(t, first, "acme", []byte("two"), nil)

	second, err := NewKeyring(stores, WithKeyWrapRegistry(testKeyWrapRegistry()))
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
	// VersionInfo has no key-material field at all; this guards a
	// regression that adds one. ProviderID/KeyRef are metadata, not key
	// material, and are expected on the status.
	if len(st.Versions) != 1 || st.Versions[0].Version != 1 || st.Versions[0].ProviderID != "software" ||
		st.Versions[0].KeyRef != "acme" || len(rec.Versions[0].WrappedKey) != KeySize {
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

// TestEncryptWrapsTheVersionKeyUnderTheConfiguredProviderAndKeyRef: the
// default composition -- no WithProviderID/WithKeyRef -- wraps under
// "software" and the tenant ID itself, and the stored record carries that
// metadata (never the plaintext key).
func TestEncryptWrapsTheVersionKeyUnderTheConfiguredProviderAndKeyRef(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	store, _ := stores.Resolve(ctx, "acme")
	rec, err := k.load(ctx, "acme", store)
	if err != nil {
		t.Fatal(err)
	}
	v := rec.Versions[0]
	if v.ProviderID != "software" || v.KeyRef != "acme" || len(v.WrappedKey) == 0 {
		t.Fatalf("version record = %+v, want provider=software keyRef=acme with a wrapped key", v)
	}
}

// TestWithProviderIDAndKeyRefAreConfigurable: an embedder can route new
// versions through a different provider ID and a different tenant-scoped
// key reference.
func TestWithProviderIDAndKeyRefAreConfigurable(t *testing.T) {
	ctx := context.Background()
	stores := newTenantStores()
	registry := keywrap.NewRegistry()
	_ = registry.Register("hsm", func() (keywrap.Provider, error) {
		return &fakeKeyWrapProvider{id: "hsm"}, nil
	})
	k, err := NewKeyring(stores,
		WithKeyWrapRegistry(registry),
		WithProviderID("hsm"),
		WithKeyRef(func(tenantID string) string { return "kek-for-" + tenantID }),
	)
	if err != nil {
		t.Fatal(err)
	}
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	store, _ := stores.Resolve(ctx, "acme")
	rec, err := k.load(ctx, "acme", store)
	if err != nil {
		t.Fatal(err)
	}
	v := rec.Versions[0]
	if v.ProviderID != "hsm" || v.KeyRef != "kek-for-acme" {
		t.Fatalf("version record = %+v, want provider=hsm keyRef=kek-for-acme", v)
	}
	// It still round-trips under that provider.
	o, err := k.Decrypt(ctx, "acme", mustEncrypt(t, k, "acme", []byte("y"), nil).Ciphertext, nil)
	if err != nil || string(o.Plaintext) != "y" {
		t.Fatalf("decrypt under hsm provider: %+v, %v", o, err)
	}
}

// TestNewKeyringRefusesAWithKeyWrapProviderCollision mirrors
// service.NewBackupService's identical refusal: installing
// WithKeyWrapProvider under an ID already present in the registry it is
// being installed into is an error, not a silent no-op that discards the
// earlier registration.
func TestNewKeyringRefusesAWithKeyWrapProviderCollision(t *testing.T) {
	registry := testKeyWrapRegistry() // already has "software"
	_, err := NewKeyring(newTenantStores(),
		WithKeyWrapRegistry(registry),
		WithKeyWrapProvider(&fakeKeyWrapProvider{id: keywrap.SoftwareProviderID}),
	)
	var collision *keywrap.ProviderAlreadyRegisteredError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want *keywrap.ProviderAlreadyRegisteredError", err)
	}
}

// TestZeroConfigKeyringRefusesToMintAKeyUntilConfigured: with no
// WithKeyWrapRegistry/WithKeyWrapProvider/WithKeyDir at all, the default
// "software" provider has no key directory to resolve, so minting a key
// (Encrypt, Rotate) is refused with a typed, server-side error rather than
// silently storing a plaintext key or falling back to an implicit
// location -- there is no "no KEK" escape hatch.
func TestZeroConfigKeyringRefusesToMintAKeyUntilConfigured(t *testing.T) {
	k, err := NewKeyring(newTenantStores())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := k.Encrypt(ctx, "acme", []byte("x"), nil); !errors.Is(err, ErrKeyDirNotConfigured) {
		t.Fatalf("Encrypt err = %v, want ErrKeyDirNotConfigured", err)
	}
	if _, err := k.Rotate(ctx, "acme"); !errors.Is(err, ErrKeyDirNotConfigured) {
		t.Fatalf("Rotate err = %v, want ErrKeyDirNotConfigured", err)
	}
}

// TestWithKeyDirUsesARealSoftwareProvider proves the default "software"
// provider -- not merely a test double -- works end to end once WithKeyDir
// names a real key directory, and that Close releases it.
func TestWithKeyDirUsesARealSoftwareProvider(t *testing.T) {
	ctx := context.Background()
	keyDir := t.TempDir()
	// The software provider resolves a bare file NAME inside keyDir; the
	// default key reference is the tenant ID itself, so the operator
	// provisions one 32-byte key file per tenant.
	if err := os.WriteFile(filepath.Join(keyDir, "acme"), bytes.Repeat([]byte{0x42}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := NewKeyring(newTenantStores(), WithKeyDir(keyDir))
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Encrypt(ctx, "acme", []byte("hunter2"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	o, err := k.Decrypt(ctx, "acme", s.Ciphertext, nil)
	if err != nil || string(o.Plaintext) != "hunter2" {
		t.Fatalf("Decrypt = %+v, %v", o, err)
	}
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close on a Keyring that never resolved the software provider is a
	// no-op, not a panic.
	k2, err := NewKeyring(newTenantStores(), WithKeyDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := k2.Close(); err != nil {
		t.Fatalf("Close of an unused provider: %v", err)
	}
}

// TestLegacyFormatRecordIsRefused: a record written before key wrapping
// existed -- format 1, a plaintext key per version -- is refused with
// *LegacyKeyringFormatError on every verb that reads it, never treated as
// corrupt and never migrated.
func TestLegacyFormatRecordIsRefused(t *testing.T) {
	ctx := context.Background()
	k, stores := newTestKeyring(t)
	store, _ := stores.Resolve(ctx, "acme")
	legacy := `{"format":1,"current":1,"versions":[{"version":1,"key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","created_at":"2026-01-01T00:00:00Z"}]}`
	if err := store.Put(ctx, keyringStoreKey, []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	for op, err := range map[string]error{
		"status":  second(k.Status(ctx, "acme")),
		"encrypt": second(k.Encrypt(ctx, "acme", []byte("x"), nil)),
		"decrypt": second(k.Decrypt(ctx, "acme", validLookingCiphertext(), nil)),
		"rewrap":  second(k.Rewrap(ctx, "acme", validLookingCiphertext(), nil)),
		"rotate":  second(k.Rotate(ctx, "acme")),
		"retire":  second(k.Retire(ctx, "acme", 1)),
	} {
		var le *LegacyKeyringFormatError
		if !errors.As(err, &le) || !errors.Is(err, ErrLegacyKeyringFormat) || le.TenantID != "acme" || le.Format != 1 {
			t.Errorf("%s: err = %v, want *LegacyKeyringFormatError for acme format 1", op, err)
		}
		if errors.Is(err, ErrCorruptKeyring) {
			t.Errorf("%s: a legacy-format record was reported as corrupt", op)
		}
	}
}

// TestUnwrapFailureIsAKeyWrapFailure: a provider whose Unwrap refuses (the
// wrong KEK, or a tampered wrapped key) is reported as *KeyWrapFailedError,
// never as a ciphertext authentication failure -- the two are checked at
// different layers and conflating them would mislead an operator debugging
// which one actually broke.
func TestUnwrapFailureIsAKeyWrapFailure(t *testing.T) {
	ctx := context.Background()
	stores := newTenantStores()
	registry := keywrap.NewRegistry()
	_ = registry.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return &refusingUnwrapProvider{}, nil
	})
	k, err := NewKeyring(stores, WithKeyWrapRegistry(registry))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Encrypt(ctx, "acme", []byte("x"), nil); !errors.Is(err, ErrKeyWrapFailed) {
		t.Fatalf("Encrypt under a provider that cannot even Wrap: err = %v, want ErrKeyWrapFailed", err)
	}
}

// TestResolveSoftwareProviderReportsAnUnopenableKeyDir: WithKeyDir naming a
// path the software provider cannot open (missing, or not a directory) is
// a server-side configuration mistake distinct from "never configured" --
// it collapses into *KeyWrapFailedError like any other provider-resolution
// failure, wrapping the software provider's own *keywrap.KeyDirectoryError.
func TestResolveSoftwareProviderReportsAnUnopenableKeyDir(t *testing.T) {
	ctx := context.Background()
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := NewKeyring(newTenantStores(), WithKeyDir(notADir))
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.Encrypt(ctx, "acme", []byte("x"), nil)
	var kwf *KeyWrapFailedError
	if !errors.As(err, &kwf) {
		t.Fatalf("err = %v, want *KeyWrapFailedError", err)
	}
	var dirErr *keywrap.KeyDirectoryError
	if !errors.As(err, &dirErr) {
		t.Fatalf("err = %v, want it to wrap *keywrap.KeyDirectoryError", err)
	}
}

// TestUnregisteredProviderAtDecryptIsAKeyWrapFailure: a version wrapped
// under a provider ID that is no longer registered -- an embedder removed
// a hardware provider, or pointed a second Keyring at the same stores with
// a narrower registry -- is reported the same way as any other provider
// resolution failure at Decrypt/Rewrap time, never a ciphertext
// authentication failure.
func TestUnregisteredProviderAtDecryptIsAKeyWrapFailure(t *testing.T) {
	ctx := context.Background()
	stores := newTenantStores()
	withCustom := keywrap.NewRegistry()
	_ = withCustom.Register("custom", func() (keywrap.Provider, error) {
		return &fakeKeyWrapProvider{id: "custom"}, nil
	})
	writer, err := NewKeyring(stores, WithKeyWrapRegistry(withCustom), WithProviderID("custom"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := writer.Encrypt(ctx, "acme", []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}

	withoutCustom := testKeyWrapRegistry() // only "software"
	reader, err := NewKeyring(stores, WithKeyWrapRegistry(withoutCustom))
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Decrypt(ctx, "acme", sealed.Ciphertext, nil)
	if !errors.Is(err, ErrKeyWrapFailed) {
		t.Fatalf("err = %v, want ErrKeyWrapFailed", err)
	}
	if errors.Is(err, ErrAuthenticationFailed) {
		t.Error("an unregistered provider was reported as a forged ciphertext")
	}
}

// TestProviderUnwrapFailureDuringDecryptIsAKeyWrapFailure: a provider that
// can wrap and unwrap once (Encrypt itself unwraps the just-minted key to
// seal the plaintext) but refuses every unwrap after that is reported as
// *KeyWrapFailedError on a later Decrypt, not as a forged ciphertext.
func TestProviderUnwrapFailureDuringDecryptIsAKeyWrapFailure(t *testing.T) {
	ctx := context.Background()
	stores := newTenantStores()
	provider := &failAfterNUnwraps{n: 1}
	registry := keywrap.NewRegistry()
	_ = registry.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return provider, nil
	})
	k, err := NewKeyring(stores, WithKeyWrapRegistry(registry))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := k.Encrypt(ctx, "acme", []byte("x"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = k.Decrypt(ctx, "acme", sealed.Ciphertext, nil)
	if !errors.Is(err, ErrKeyWrapFailed) {
		t.Fatalf("err = %v, want ErrKeyWrapFailed", err)
	}
}

// failAfterNUnwraps wraps by copying bytes verbatim and unwraps the same
// way for its first n calls, then refuses every call after -- standing in
// for a KEK that becomes unreachable partway through a single request
// (Rewrap unwraps the FROM version to read the plaintext, then the
// CURRENT version to re-seal it; this lets a test fail only the second).
type failAfterNUnwraps struct {
	n   int
	hit atomic.Int64
}

var _ keywrap.Provider = (*failAfterNUnwraps)(nil)

func (p *failAfterNUnwraps) ProviderID() string { return keywrap.SoftwareProviderID }

func (p *failAfterNUnwraps) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (p *failAfterNUnwraps) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	if p.hit.Add(1) > int64(p.n) {
		return nil, errors.New("KEK unreachable")
	}
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// TestRewrapReportsAFailureUnwrappingTheCurrentVersion: Rewrap unwraps TWO
// versions -- the ciphertext's own (to read the plaintext) and the current
// one (to re-seal it). A KEK that fails only on the second is reported as
// *KeyWrapFailedError, and the plaintext is never returned to the caller.
func TestRewrapReportsAFailureUnwrappingTheCurrentVersion(t *testing.T) {
	ctx := context.Background()
	stores := newTenantStores()
	provider := &failAfterNUnwraps{n: 1}
	registry := keywrap.NewRegistry()
	_ = registry.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return provider, nil
	})
	k, err := NewKeyring(stores, WithKeyWrapRegistry(registry))
	if err != nil {
		t.Fatal(err)
	}
	old := mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	_, err = k.Rewrap(ctx, "acme", old.Ciphertext, nil)
	if !errors.Is(err, ErrKeyWrapFailed) {
		t.Fatalf("err = %v, want ErrKeyWrapFailed", err)
	}
}

// refusingUnwrapProvider's Wrap always fails, standing in for a KEK that
// has become unreachable (an HSM unplugged, a cloud KMS call refused).
type refusingUnwrapProvider struct{}

var _ keywrap.Provider = (*refusingUnwrapProvider)(nil)

func (refusingUnwrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (refusingUnwrapProvider) Wrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("KEK unreachable")
}

func (refusingUnwrapProvider) Unwrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("KEK unreachable")
}
