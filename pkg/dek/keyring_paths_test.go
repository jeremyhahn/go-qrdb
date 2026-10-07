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
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestRewrapRejectsMalformedCiphertextBeforeTouchingTheStore: the header is
// validated first, so a malformed ciphertext is reported as such even when
// the tenant's store is unreachable.
func TestRewrapRejectsMalformedCiphertextBeforeTouchingTheStore(t *testing.T) {
	k, stores := newTestKeyring(t)
	stores.err = errors.New("tenant sealed")
	for name, ct := range map[string][]byte{
		"empty":     nil,
		"short":     []byte("QDEK\x01"),
		"bad magic": append([]byte("XXXX\x01"), make([]byte, 40)...),
		"bad fmt":   append([]byte("QDEK\x02"), make([]byte, 40)...),
	} {
		_, err := k.Rewrap(context.Background(), "acme", ct, nil)
		var me *MalformedCiphertextError
		if !errors.As(err, &me) || !errors.Is(err, ErrMalformedCiphertext) {
			t.Errorf("%s: err = %v, want *MalformedCiphertextError", name, err)
		}
		if errors.Is(err, ErrKeyringUnavailable) {
			t.Errorf("%s: store was consulted for a malformed ciphertext", name)
		}
	}
}

// TestUnresolvableTenantRefusesEveryOperation: a tenant the resolver refuses,
// such as a sealed or deleted one, has no usable keyring on any verb, and the
// cause is preserved for the caller.
func TestUnresolvableTenantRefusesEveryOperation(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	pt := []byte("x")
	ct := mustEncrypt(t, k, "acme", pt, nil).Ciphertext
	sealed := errors.New("tenant sealed")
	stores.err = sealed
	for op, err := range map[string]error{
		"encrypt": second(k.Encrypt(ctx, "acme", pt, nil)),
		"decrypt": second(k.Decrypt(ctx, "acme", ct, nil)),
		"rewrap":  second(k.Rewrap(ctx, "acme", ct, nil)),
		"rotate":  second(k.Rotate(ctx, "acme")),
		"retire":  second(k.Retire(ctx, "acme", 1)),
		"status":  second(k.Status(ctx, "acme")),
	} {
		var ue *KeyringUnavailableError
		if !errors.As(err, &ue) || ue.TenantID != "acme" || !errors.Is(err, sealed) {
			t.Errorf("%s: err = %v, want *KeyringUnavailableError for acme wrapping the cause", op, err)
		}
	}
}

// TestCorruptRecordRefusesUseAndNamesTheReason: Decrypt and Rewrap refuse a
// record that fails validation rather than trying its keys, and the error
// says which invariant broke.
func TestCorruptRecordRefusesUseAndNamesTheReason(t *testing.T) {
	ctx := context.Background()
	for raw, reason := range map[string]string{
		`{"format":7,"current":1,"versions":[]}`:                                                  "unsupported record format",
		`{"format":2,"current":1,"versions":[]}`:                                                  "record has no versions",
		`{"format":2,"current":1,"versions":[{"version":1,"wrapped_key":"AAAA"}]}`:                "current version is missing or unusable",
		`{"format":2,"current":1,"versions":[{"version":1,"retired_at":"2026-01-01T00:00:00Z"}]}`: "current version is missing or unusable",
	} {
		k, stores := newTestKeyring(t)
		store, _ := stores.Resolve(ctx, "acme")
		if err := store.Put(ctx, keyringStoreKey, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		ct := validLookingCiphertext()
		for op, err := range map[string]error{
			"decrypt": second(k.Decrypt(ctx, "acme", ct, nil)),
			"rewrap":  second(k.Rewrap(ctx, "acme", ct, nil)),
			"status":  second(k.Status(ctx, "acme")),
		} {
			var ce *CorruptKeyringError
			if !errors.As(err, &ce) || ce.TenantID != "acme" {
				t.Errorf("%s %s: err = %v, want *CorruptKeyringError", op, raw, err)
				continue
			}
			if !strings.Contains(err.Error(), reason) {
				t.Errorf("%s %s: err = %q, want reason %q", op, raw, err, reason)
			}
		}
	}
}

// TestDamagedOldVersionKeyIsAKeyWrapFailure: validation guarantees only the
// current version's key; a non-current version whose stored wrapped key
// unwraps to the wrong length is reported as a KEK-layer failure, never as
// a forged ciphertext (checked at a different layer entirely), and never
// decrypts.
func TestDamagedOldVersionKeyIsAKeyWrapFailure(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	old := mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	store, _ := stores.Resolve(ctx, "acme")
	rec, err := k.load(ctx, "acme", store)
	if err != nil {
		t.Fatal(err)
	}
	// testKeyWrapRegistry's fake provider unwraps by copying bytes
	// verbatim, so truncating the stored wrapped key truncates what
	// Unwrap returns -- standing in for a provider that returns the
	// wrong-length key for any reason.
	rec.Versions[0].WrappedKey = rec.Versions[0].WrappedKey[:16]
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, keyringStoreKey, raw); err != nil {
		t.Fatal(err)
	}

	for op, err := range map[string]error{
		"decrypt": second(k.Decrypt(ctx, "acme", old.Ciphertext, nil)),
		"rewrap":  second(k.Rewrap(ctx, "acme", old.Ciphertext, nil)),
	} {
		var ke *KeyWrapFailedError
		if !errors.Is(err, ErrKeyWrapFailed) || !errors.As(err, &ke) {
			t.Errorf("%s: err = %v, want ErrKeyWrapFailed wrapping *KeyWrapFailedError", op, err)
		}
		if errors.Is(err, ErrAuthenticationFailed) {
			t.Errorf("%s: a damaged key was reported as a forged ciphertext", op)
		}
	}
	// The current version is untouched and keeps working.
	fresh := mustEncrypt(t, k, "acme", []byte("y"), nil)
	o, err := k.Decrypt(ctx, "acme", fresh.Ciphertext, nil)
	if err != nil || string(o.Plaintext) != "y" || o.Version != 2 {
		t.Fatalf("current version after damage: %+v, %v", o, err)
	}
}

// racingStore is a MemoryKVStore that lets a rival writer run at a chosen
// point, exactly as a second node lands a write between two of this node
// store calls. The rival writes to the same underlying store, so the record
// the keyring later reads or CASes against is the real one.
type racingStore struct {
	*kvstore.MemoryKVStore
	gets        atomic.Int32
	raceOnGet   int32 // run rival after this 1-based Get returns; 0 is never
	raceOnWrite bool  // run rival just before the first PutIfAbsent
	raced       atomic.Bool
	rival       func()
}

var _ kvstore.AtomicKVStore = (*racingStore)(nil)

func (s *racingStore) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := s.MemoryKVStore.Get(ctx, key)
	if s.gets.Add(1) == s.raceOnGet && s.raced.CompareAndSwap(false, true) {
		s.rival()
	}
	return v, err
}

func (s *racingStore) PutIfAbsent(ctx context.Context, key string, v []byte) (bool, error) {
	if s.raceOnWrite && s.raced.CompareAndSwap(false, true) {
		s.rival()
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, v)
}

// TestFirstEncryptAdoptsARivalsKeyring: two nodes encrypt for a tenant with
// no keyring at the same moment. The one that loses the create must adopt the
// winning version 1 -- never overwrite it, never mint a second v1 -- so both
// nodes decrypt each other under the one stored key.
func TestFirstEncryptAdoptsARivalsKeyring(t *testing.T) {
	for name, race := range map[string]func(*racingStore){
		// The rival lands after Encrypt saw no record but before update re-reads.
		"before update read": func(s *racingStore) { s.raceOnGet = 1 },
		// The rival lands between the update read and its create-if-absent.
		"before create": func(s *racingStore) { s.raceOnWrite = true },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mem := kvstore.NewMemoryKVStore()
			rival, err := NewKeyring(fixedResolver{mem}, WithKeyWrapRegistry(testKeyWrapRegistry()))
			if err != nil {
				t.Fatal(err)
			}
			var rivalCT []byte
			rs := &racingStore{MemoryKVStore: mem}
			rs.rival = func() {
				s, err := rival.Encrypt(ctx, "acme", []byte("rival"), nil)
				if err != nil {
					t.Errorf("rival Encrypt: %v", err)
					return
				}
				rivalCT = s.Ciphertext
			}
			race(rs)
			k, err := NewKeyring(fixedResolver{rs}, WithKeyWrapRegistry(testKeyWrapRegistry()))
			if err != nil {
				t.Fatal(err)
			}

			s, err := k.Encrypt(ctx, "acme", []byte("mine"), nil)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if !rs.raced.Load() {
				t.Fatal("the rival never ran; the race was not exercised")
			}
			if s.Version != 1 {
				t.Fatalf("loser encrypted under v%d, want the winning v1", s.Version)
			}
			st, err := k.Status(ctx, "acme")
			if err != nil || st.Current != 1 || len(st.Versions) != 1 {
				t.Fatalf("status = %+v, %v; want a single v1", st, err)
			}
			// Each side decrypts the other: one key, not two.
			o, err := rival.Decrypt(ctx, "acme", s.Ciphertext, nil)
			if err != nil || string(o.Plaintext) != "mine" {
				t.Fatalf("winner decrypting loser ciphertext: %+v, %v", o, err)
			}
			o, err = k.Decrypt(ctx, "acme", rivalCT, nil)
			if err != nil || string(o.Plaintext) != "rival" {
				t.Fatalf("loser decrypting winner ciphertext: %+v, %v", o, err)
			}
		})
	}
}

// TestRotationAndRetirementOnOneNodeAreInForceOnAnother: the keyring caches
// nothing, so a second Keyring over the same replicated store -- another
// node -- selects the new current version and refuses the retired one on its
// very next call.
func TestRotationAndRetirementOnOneNodeAreInForceOnAnother(t *testing.T) {
	stores := newTenantStores()
	ctx := context.Background()
	aad := []byte("aad")
	nodeA, err := NewKeyring(stores, WithKeyWrapRegistry(testKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := NewKeyring(stores, WithKeyWrapRegistry(testKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	v1 := mustEncrypt(t, nodeB, "acme", []byte("one"), aad)

	if _, err := nodeA.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	v2 := mustEncrypt(t, nodeB, "acme", []byte("two"), aad)
	if v2.Version != 2 {
		t.Fatalf("node B encrypted under v%d after node A rotated, want v2", v2.Version)
	}
	rw, err := nodeB.Rewrap(ctx, "acme", v1.Ciphertext, aad)
	if err != nil || rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("node B rewrap = %+v, %v; want 1 -> 2", rw, err)
	}

	if _, err := nodeA.Retire(ctx, "acme", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeB.Decrypt(ctx, "acme", v1.Ciphertext, aad); !errors.Is(err, ErrVersionRetired) {
		t.Fatalf("node B decrypting retired v1: err = %v, want ErrVersionRetired", err)
	}
	// The rewrapped copy survives the retirement of its source version.
	o, err := nodeB.Decrypt(ctx, "acme", rw.Ciphertext, aad)
	if err != nil || string(o.Plaintext) != "one" || o.Version != 2 {
		t.Fatalf("rewrapped ciphertext after retire: %+v, %v", o, err)
	}
	st, err := nodeB.Status(ctx, "acme")
	if err != nil || st.Current != 2 || st.Versions[0].RetiredAt == nil || st.Versions[1].RetiredAt != nil {
		t.Fatalf("node B status = %+v, %v", st, err)
	}
}

// TestRetireOfAnAlreadyRetiredVersionDoesNotWrite: the idempotent second
// Retire leaves the stored record byte-for-byte unchanged, keeping the
// original retirement time.
func TestRetireOfAnAlreadyRetiredVersionDoesNotWrite(t *testing.T) {
	k, stores := newTestKeyring(t)
	ctx := context.Background()
	mustEncrypt(t, k, "acme", []byte("x"), nil)
	if _, err := k.Rotate(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	first, err := k.Retire(ctx, "acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := stores.Resolve(ctx, "acme")
	before, err := store.Get(ctx, keyringStoreKey)
	if err != nil {
		t.Fatal(err)
	}
	before = append([]byte(nil), before...)
	again, err := k.Retire(ctx, "acme", 1)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.Get(ctx, keyringStoreKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("second Retire rewrote the record")
	}
	if !again.Versions[0].RetiredAt.Equal(*first.Versions[0].RetiredAt) {
		t.Fatalf("retirement time moved: %v -> %v", first.Versions[0].RetiredAt, again.Versions[0].RetiredAt)
	}
}
