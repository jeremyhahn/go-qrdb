// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// volumeKeyInfoPrefix domain-separates this package's HKDF info string from
// every other deriver that reads the same root key (per-tenant DEKs, auth
// factor decoys). It is a fixed label, not a credential.
//
// #nosec G101 -- domain-separation label, not a credential.
const volumeKeyInfoPrefix = "qrdb/volume/v1"

// volumeKeyLen is the derived key length in bytes (AES-256).
const volumeKeyLen = 32

// Lockable is implemented by a Volume whose provider can lock and unlock the
// underlying device in step with the node's system barrier (decision D6,
// ruled 2026-10-04: docs/volume-pools/architecture.md#decisions-ruled-2026-10-04).
// The "dir" provider does not implement it; a sealed node has nothing to
// unlock for a plain directory.
//
// A Lockable's Unlock/Lock are called by the composing layer (pkg/seal's
// VolumeLifecycle), never by the Provider itself, and never concurrently
// with each other for the same Volume.
type Lockable interface {
	// Unlock makes the volume's data readable, given the per-volume key the
	// caller derived from the node's now-unsealed system barrier with
	// DeriveVolumeKey. Unlock of an already-unlocked volume returns nil.
	// The caller wipes key immediately after this call returns, so an
	// implementation that needs the key beyond the call must copy it.
	Unlock(ctx context.Context, key VolumeKey) error
	// Lock makes the volume's data unreadable again. Lock of an
	// already-locked volume returns nil. Lock takes no key: it is the
	// inverse of mapping the device available, not of decrypting it.
	Lock(ctx context.Context) error
}

// VolumeKey is a per-volume, per-node symmetric key used by a Lockable's
// Unlock. It is derived fresh by DeriveVolumeKey at every system-barrier
// unseal and is never persisted anywhere: a sealed node's system barrier has
// no root key to derive it from, so a sealed node cannot unlock its
// volumes. The caller wipes it with Wipe immediately after the Unlock call
// returns.
type VolumeKey [volumeKeyLen]byte

// Wipe zeroes the key in place. Safe to call more than once.
func (k *VolumeKey) Wipe() {
	secure.Wipe(k[:])
}

// DeriveVolumeKey derives the symmetric key for the volume identified by
// nodeID and pool, from the system barrier's unwrapped root key, with
// HKDF-SHA256 -- the same primitive go-quicraft's EpochBarrier uses to
// derive its own per-epoch data keys, so this introduces no new
// cryptographic primitive, only a new, domain-separated info string.
//
// The info string binds nodeID and pool so that no two (node, pool) pairs
// ever derive the same key, and so a node never derives another node's key
// for the same pool. Under decision D1's one-volume-per-pool-per-node
// granularity, (node, pool) already identifies the volume; a future
// per-tenant extension of this hook would add a third, tenant-id component
// to the info string rather than changing this function's contract.
//
// masterKey must be the system barrier's unwrapped root key (StorageBarrier
// .GetMasterKey()); it is never retained by this function. Nothing this
// function returns is persisted: callers re-derive it at every unseal and
// wipe it after use.
func DeriveVolumeKey(masterKey []byte, nodeID, pool string) (VolumeKey, error) {
	var key VolumeKey
	if len(masterKey) == 0 {
		return key, &InvalidMasterKeyError{}
	}
	if nodeID == "" || pool == "" {
		return key, &InvalidVolumeIdentityError{NodeID: nodeID, Pool: pool}
	}
	info := volumeKeyInfoPrefix + "\x00" + nodeID + "\x00" + pool
	derived, err := hkdf.Key(sha256.New, masterKey, nil, info, volumeKeyLen)
	if err != nil {
		return key, &VolumeKeyDerivationError{NodeID: nodeID, Pool: pool, Err: err}
	}
	copy(key[:], derived)
	secure.Wipe(derived)
	return key, nil
}
