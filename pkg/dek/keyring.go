// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package dek implements the per-tenant data encryption keyring: a versioned
// set of AES-256-GCM keys that encrypt application data on a tenant's behalf
// without the key ever leaving the server.
//
// # Where the keyring lives
//
// The keyring is one record in the tenant's own store, as resolved by
// tenant.StoreResolver — the same store that holds the tenant's identity
// records. That choice gives it every property the tenant's identity already
// has, with no second mechanism:
//
//   - a dedicated-isolation tenant's record is encrypted under the tenant's
//     own barrier and replicated through the system shard;
//   - a prefix tenant's record sits in the replicated shared store, protected
//     at rest by the system barrier;
//   - a sealed tenant's keyring is unreachable, because the resolver refuses a
//     sealed tenant;
//   - a deleted tenant's keyring is unreachable from the teardown fence on,
//     and is destroyed with the tenant's barrier or reclaimed keyspace.
//
// A barrier configured with protection "none" stores data in plaintext, and
// so does it store this record: the keyring follows the barrier's at-rest
// protection, exactly as identity records do. Operators that need at-rest
// protection select a keyed barrier strategy.
//
// # Application keys are not barrier epochs
//
// The keyring's keys are independent random keys, not the barrier's storage
// epochs. Rotating or retiring a keyring version never touches the tenant's
// stored data, and rotating the barrier never invalidates a keyring
// ciphertext.
//
// # Versions
//
// Rotate adds a version and makes it current; earlier versions keep
// decrypting. Rewrap moves a ciphertext to the current version without
// returning its plaintext. Retire destroys an old version's key; the current
// version cannot be retired. Retirement is irreversible, and the keyring
// cannot know where ciphertexts are stored, so a caller rewraps everything
// still under a version before retiring it.
package dek

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/crypto/aesgcm"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	// Algorithm is the AEAD every keyring version uses.
	Algorithm = "AES-256-GCM"

	// KeySize is the size of every keyring key in bytes.
	KeySize = 32

	// keyringStoreKey is the record's key inside the tenant's resolved store.
	// The "__sys:" segment keeps it disjoint from the entity keys the
	// identity stores write into the same namespace.
	keyringStoreKey = "__sys:dek/keyring"

	// recordFormat is the stored record's schema version.
	recordFormat = 1

	// maxUpdateAttempts bounds the compare-and-swap retry loop. Every round
	// has at least one winner, so this only fails under sustained contention
	// far beyond key-management traffic.
	maxUpdateAttempts = 16
)

// Ciphertext layout:
//
//	offset  size  field
//	0       4     magic "QDEK"
//	4       1     format (1)
//	5       8     key version, big-endian
//	13      ...   AES-256-GCM output: nonce(12) || ciphertext || tag(16)
//
// The 13-byte header, the tenant ID and the caller's associated data are all
// bound into the GCM additional data, so neither the version nor the tenant
// can be changed without failing authentication.
const (
	ciphertextMagic     = "QDEK"
	ciphertextFormat    = 1
	headerVersionOffset = 5
	headerLen           = 13
	gcmOverhead         = 12 + 16
)

// StoreResolver maps a tenant to the store that holds its keyring.
// tenant.StoreResolver satisfies it.
type StoreResolver interface {
	Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error)
}

// Sealed is the result of Encrypt.
type Sealed struct {
	// Ciphertext is opaque to callers; it carries the key version internally.
	Ciphertext []byte
	// Version is the key version the ciphertext was sealed under.
	Version uint64
}

// Opened is the result of Decrypt.
type Opened struct {
	Plaintext []byte
	Version   uint64
}

// Rewrapped is the result of Rewrap.
type Rewrapped struct {
	Ciphertext  []byte
	FromVersion uint64
	ToVersion   uint64
}

// Status describes a tenant's keyring. It never carries key material.
type Status struct {
	TenantID  string
	Algorithm string
	// Current is the version new encryptions use; 0 means the tenant has no
	// keyring yet (the first Encrypt or Rotate creates version 1).
	Current  uint64
	Versions []VersionInfo
}

// VersionInfo describes one keyring version.
type VersionInfo struct {
	Version   uint64
	CreatedAt time.Time
	// RetiredAt is set once the version's key has been destroyed.
	RetiredAt *time.Time
}

// keyringRecord is the stored form of a keyring.
type keyringRecord struct {
	Format   int             `json:"format"`
	Current  uint64          `json:"current"`
	Versions []versionRecord `json:"versions"`
}

// versionRecord is one stored version. Key is nil once retired.
type versionRecord struct {
	Version   uint64     `json:"version"`
	Key       []byte     `json:"key,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	RetiredAt *time.Time `json:"retired_at,omitempty"`
}

// Keyring serves every tenant's versioned DEK keyring.
//
// It holds no key material between calls: each operation reads the tenant's
// record, applies the key and wipes it. That keeps every node of a cluster
// consistent with the replicated record — a rotation or retirement on one
// node is in force on all of them at the next call — at the cost of one store
// read per operation, which key-management traffic easily affords.
type Keyring struct {
	resolver StoreResolver
	random   io.Reader
	now      func() time.Time
}

// NewKeyring returns a Keyring over resolver.
func NewKeyring(resolver StoreResolver) (*Keyring, error) {
	if resolver == nil {
		return nil, &InvalidInputError{Op: "NewKeyring", Field: "store resolver"}
	}
	return &Keyring{
		resolver: resolver,
		random:   rand.Reader,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
}

// Encrypt seals plaintext under the tenant's current key version, creating
// the keyring at version 1 on first use. aad is bound into the ciphertext and
// must be presented unchanged to Decrypt and Rewrap.
func (k *Keyring) Encrypt(ctx context.Context, tenantID string, plaintext, aad []byte) (*Sealed, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Encrypt", Field: "tenant ID"}
	}
	store, err := k.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rec, _, err := k.read(ctx, tenantID, store)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		rec, err = k.update(ctx, tenantID, store, k.ensureVersion)
		if err != nil {
			return nil, err
		}
	}
	defer rec.wipe()
	ct, err := seal(tenantID, rec.Current, rec.version(rec.Current), plaintext, aad)
	if err != nil {
		return nil, err
	}
	return &Sealed{Ciphertext: ct, Version: rec.Current}, nil
}

// Decrypt opens a ciphertext produced by Encrypt or Rewrap for the same
// tenant and aad.
func (k *Keyring) Decrypt(ctx context.Context, tenantID string, ciphertext, aad []byte) (*Opened, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Decrypt", Field: "tenant ID"}
	}
	version, err := parseVersion(ciphertext)
	if err != nil {
		return nil, err
	}
	rec, err := k.loadForUse(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer rec.wipe()
	plaintext, err := rec.open(tenantID, version, ciphertext, aad)
	if err != nil {
		return nil, err
	}
	return &Opened{Plaintext: plaintext, Version: version}, nil
}

// Rewrap re-seals a ciphertext under the tenant's current key version. The
// plaintext never leaves this call. A ciphertext already under the current
// version is authenticated and returned unchanged.
func (k *Keyring) Rewrap(ctx context.Context, tenantID string, ciphertext, aad []byte) (*Rewrapped, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Rewrap", Field: "tenant ID"}
	}
	from, err := parseVersion(ciphertext)
	if err != nil {
		return nil, err
	}
	rec, err := k.loadForUse(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer rec.wipe()
	plaintext, err := rec.open(tenantID, from, ciphertext, aad)
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(plaintext)
	if from == rec.Current {
		return &Rewrapped{Ciphertext: ciphertext, FromVersion: from, ToVersion: from}, nil
	}
	ct, err := seal(tenantID, rec.Current, rec.version(rec.Current), plaintext, aad)
	if err != nil {
		return nil, err
	}
	return &Rewrapped{Ciphertext: ct, FromVersion: from, ToVersion: rec.Current}, nil
}

// Rotate adds a new key version and makes it current. Earlier versions keep
// decrypting until retired. A tenant with no keyring gets version 1.
func (k *Keyring) Rotate(ctx context.Context, tenantID string) (*Status, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Rotate", Field: "tenant ID"}
	}
	store, err := k.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rec, err := k.update(ctx, tenantID, store, func(rec *keyringRecord) (bool, error) {
		if len(rec.Versions) == 0 {
			return k.ensureVersion(rec)
		}
		return true, k.addVersion(rec)
	})
	if err != nil {
		return nil, err
	}
	defer rec.wipe()
	return rec.status(tenantID), nil
}

// Retire destroys the key of an old version. Ciphertexts under it can no
// longer be decrypted or rewrapped, so rewrap them first. The current
// version is refused; retiring an already retired version succeeds.
func (k *Keyring) Retire(ctx context.Context, tenantID string, version uint64) (*Status, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Retire", Field: "tenant ID"}
	}
	if version == 0 {
		return nil, &InvalidInputError{Op: "Retire", Field: "version"}
	}
	store, err := k.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rec, err := k.update(ctx, tenantID, store, func(rec *keyringRecord) (bool, error) {
		v := rec.find(version)
		switch {
		case v == nil:
			return false, &UnknownVersionError{TenantID: tenantID, Version: version}
		case version == rec.Current:
			return false, &RetireCurrentVersionError{TenantID: tenantID, Version: version}
		case v.RetiredAt != nil:
			return false, nil
		}
		secure.Wipe(v.Key)
		v.Key = nil
		at := k.now()
		v.RetiredAt = &at
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	defer rec.wipe()
	return rec.status(tenantID), nil
}

// Status reports the tenant's keyring versions. A tenant with no keyring
// reports Current 0 and no versions.
func (k *Keyring) Status(ctx context.Context, tenantID string) (*Status, error) {
	if tenantID == "" {
		return nil, &InvalidInputError{Op: "Status", Field: "tenant ID"}
	}
	store, err := k.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rec, _, err := k.read(ctx, tenantID, store)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return &Status{TenantID: tenantID, Algorithm: Algorithm}, nil
	}
	defer rec.wipe()
	return rec.status(tenantID), nil
}

// store resolves the tenant's store and requires the atomic verbs.
func (k *Keyring) store(ctx context.Context, tenantID string) (kvstore.AtomicKVStore, error) {
	s, err := k.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, &KeyringUnavailableError{TenantID: tenantID, Err: err}
	}
	atomic, ok := s.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &KeyringUnavailableError{TenantID: tenantID, Err: &AtomicStoreRequiredError{}}
	}
	return atomic, nil
}

// loadForUse resolves and reads the tenant's record for Decrypt and Rewrap. A
// missing record is reported as an unknown version by the caller's open.
func (k *Keyring) loadForUse(ctx context.Context, tenantID string) (*keyringRecord, error) {
	store, err := k.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rec, _, err := k.read(ctx, tenantID, store)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return &keyringRecord{Format: recordFormat}, nil
	}
	return rec, nil
}

// load reads and decodes the record; nil means the tenant has none.
func (k *Keyring) load(ctx context.Context, tenantID string, store kvstore.KVStore) (*keyringRecord, error) {
	rec, _, err := k.read(ctx, tenantID, store)
	return rec, err
}

// read returns the decoded record and the raw bytes it decoded from; both
// are nil when the tenant has no keyring.
func (k *Keyring) read(ctx context.Context, tenantID string, store kvstore.KVStore) (*keyringRecord, []byte, error) {
	raw, err := store.Get(ctx, keyringStoreKey)
	if err != nil {
		return nil, nil, &StoreError{TenantID: tenantID, Op: "read", Err: err}
	}
	if raw == nil {
		return nil, nil, nil
	}
	var rec keyringRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, nil, &CorruptKeyringError{TenantID: tenantID, Err: err}
	}
	if err := rec.validate(); err != nil {
		return nil, nil, &CorruptKeyringError{TenantID: tenantID, Err: err}
	}
	return &rec, raw, nil
}

// update applies mutate to the current record and writes it back with a
// compare-and-swap (or a create-if-absent when there is no record), retrying
// on a lost race. mutate reports whether it changed the record; an unchanged
// record is returned without a write.
func (k *Keyring) update(ctx context.Context, tenantID string, store kvstore.AtomicKVStore, mutate func(*keyringRecord) (bool, error)) (*keyringRecord, error) {
	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		rec, raw, err := k.read(ctx, tenantID, store)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			rec = &keyringRecord{Format: recordFormat}
		}
		changed, err := mutate(rec)
		if err != nil {
			rec.wipe()
			return nil, err
		}
		if !changed {
			return rec, nil
		}
		next, err := json.Marshal(rec)
		if err != nil {
			rec.wipe()
			return nil, &CorruptKeyringError{TenantID: tenantID, Err: err}
		}
		var written bool
		if raw == nil {
			written, err = store.PutIfAbsent(ctx, keyringStoreKey, next)
		} else {
			written, err = store.CompareAndSwap(ctx, keyringStoreKey, raw, next)
		}
		secure.Wipe(raw)
		secure.Wipe(next)
		if err != nil {
			rec.wipe()
			return nil, &StoreError{TenantID: tenantID, Op: "write", Err: err}
		}
		if written {
			return rec, nil
		}
		rec.wipe()
	}
	return nil, &ConcurrentUpdateError{TenantID: tenantID, Attempts: maxUpdateAttempts}
}

// ensureVersion gives an empty record its first version.
func (k *Keyring) ensureVersion(rec *keyringRecord) (bool, error) {
	if len(rec.Versions) > 0 {
		return false, nil
	}
	return true, k.addVersion(rec)
}

// addVersion appends a fresh random key as the new current version.
func (k *Keyring) addVersion(rec *keyringRecord) error {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(k.random, key); err != nil {
		return &CryptoError{Op: "generate key", Err: err}
	}
	next := rec.Current + 1
	rec.Versions = append(rec.Versions, versionRecord{Version: next, Key: key, CreatedAt: k.now()})
	rec.Current = next
	return nil
}

// recordShapeError explains why a decoded record is unusable.
type recordShapeError struct {
	reason string
}

func (e *recordShapeError) Error() string { return e.reason }

// validate checks the invariants every stored record must hold.
func (r *keyringRecord) validate() error {
	if r.Format != recordFormat {
		return &recordShapeError{reason: "unsupported record format"}
	}
	if len(r.Versions) == 0 {
		return &recordShapeError{reason: "record has no versions"}
	}
	cur := r.find(r.Current)
	if cur == nil || cur.RetiredAt != nil || len(cur.Key) != KeySize {
		return &recordShapeError{reason: "current version is missing or unusable"}
	}
	return nil
}

// find returns the version record, or nil.
func (r *keyringRecord) find(version uint64) *versionRecord {
	for i := range r.Versions {
		if r.Versions[i].Version == version {
			return &r.Versions[i]
		}
	}
	return nil
}

// version returns the key of a version known to exist.
func (r *keyringRecord) version(v uint64) []byte {
	return r.find(v).Key
}

// open authenticates and decrypts a ciphertext under the named version.
func (r *keyringRecord) open(tenantID string, version uint64, ciphertext, aad []byte) ([]byte, error) {
	v := r.find(version)
	if v == nil {
		return nil, &UnknownVersionError{TenantID: tenantID, Version: version}
	}
	if v.RetiredAt != nil {
		return nil, &VersionRetiredError{TenantID: tenantID, Version: version}
	}
	plaintext, err := aesgcm.DecryptWithAAD(v.Key, ciphertext[headerLen:], boundAAD(ciphertext[:headerLen], tenantID, aad))
	if err != nil {
		var de *aesgcm.DecryptionError
		if errors.As(err, &de) {
			return nil, &AuthenticationFailedError{TenantID: tenantID, Version: version, Err: err}
		}
		return nil, &CryptoError{Op: "decrypt", Err: err}
	}
	return plaintext, nil
}

// status projects the record without key material.
func (r *keyringRecord) status(tenantID string) *Status {
	st := &Status{TenantID: tenantID, Algorithm: Algorithm, Current: r.Current}
	st.Versions = make([]VersionInfo, 0, len(r.Versions))
	for _, v := range r.Versions {
		st.Versions = append(st.Versions, VersionInfo{Version: v.Version, CreatedAt: v.CreatedAt, RetiredAt: v.RetiredAt})
	}
	return st
}

// wipe zeroes every key the record holds in memory.
func (r *keyringRecord) wipe() {
	if r == nil {
		return
	}
	for i := range r.Versions {
		secure.Wipe(r.Versions[i].Key)
	}
}

// seal encrypts plaintext under key and returns header || AEAD output, with
// the header naming version.
func seal(tenantID string, version uint64, key, plaintext, aad []byte) ([]byte, error) {
	header := make([]byte, headerLen)
	copy(header, ciphertextMagic)
	header[4] = ciphertextFormat
	binary.BigEndian.PutUint64(header[headerVersionOffset:], version)
	out, err := aesgcm.EncryptWithAAD(key, plaintext, boundAAD(header, tenantID, aad))
	if err != nil {
		return nil, &CryptoError{Op: "encrypt", Err: err}
	}
	return append(header, out...), nil
}

// boundAAD is the additional data the AEAD authenticates: the ciphertext
// header, the length-prefixed tenant ID and the caller's associated data.
func boundAAD(header []byte, tenantID string, aad []byte) []byte {
	out := make([]byte, 0, len(header)+2+len(tenantID)+len(aad))
	out = append(out, header...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(tenantID)))
	out = append(out, tenantID...)
	return append(out, aad...)
}

// parseVersion validates the ciphertext header and returns its key version.
func parseVersion(ciphertext []byte) (uint64, error) {
	if len(ciphertext) < headerLen+gcmOverhead {
		return 0, &MalformedCiphertextError{Reason: "too short"}
	}
	if string(ciphertext[:4]) != ciphertextMagic {
		return 0, &MalformedCiphertextError{Reason: "not a tenant DEK ciphertext"}
	}
	if ciphertext[4] != ciphertextFormat {
		return 0, &MalformedCiphertextError{Reason: "unsupported ciphertext format"}
	}
	version := binary.BigEndian.Uint64(ciphertext[headerVersionOffset:headerLen])
	if version == 0 {
		return 0, &MalformedCiphertextError{Reason: "zero key version"}
	}
	return version, nil
}
