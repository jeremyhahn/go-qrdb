// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

// Epoch pins: why a StorageBarrier never lets an epoch it wrote under be
// purged.
//
// A StorageBarrier's EpochBarrier is also a Raft host's WAL barrier: the
// system barrier backs the system host, and an isolated tenant's barrier backs
// that tenant's host. go-quicraft retires old epochs on its own schedule. After
// a snapshot it computes a purge floor from the epochs its WAL and snapshots
// still reference, and PurgeEpochsBefore drops every DEK below that floor,
// persisting the shortened KnownEpochs list first so the purge outlives a
// restart.
//
// That floor knows nothing of the ciphertext this type writes. A value stored
// with Put, or a tenant root key wrapped by SystemBarrierStrategy through
// Encrypt, lives under the epoch that was current when it was written and is
// read back for the life of the barrier. Each unseal rotates the epoch, so
// after one restart and one snapshot the floor passes that epoch, the purge
// drops its DEK, and the record can never be decrypted again. The first
// failure seen was an isolated tenant whose root key, wrapped by the system
// barrier at epoch 1, stopped decrypting after a full-cluster restart: the
// tenant barrier stayed sealed and the tenant's home group never started on
// that node.
//
// The fix keeps every such epoch out of the purge's reach through go-quicraft's
// in-flight pin, the mechanism the purge already honours for snapshots in
// transit:
//
//   - every encrypt this type performs pins the epoch it sealed under, in the
//     same step as the seal (EncryptWithEpochPinned), so no Rotate followed by
//     a purge can land between the two;
//   - every unseal pins each epoch the persisted root-key blob still knows,
//     before the EpochBarrier is unsealed and so before any purge can run
//     against it. A record written in an earlier process has no in-memory pin
//     left, and its epoch is not recorded anywhere except in the ciphertext,
//     so every surviving epoch is presumed to hold one.
//
// The cost is that an epoch this barrier has written under or known about is
// never retired, so KnownEpochs grows by one per rotation (every unseal
// rotates). Each entry is a small salt from which one DEK is derived. That
// growth is the price of not losing data. Retiring an epoch safely would mean
// re-encrypting every record under a newer one first, which this type does not
// do.
//
// Pins live in the EpochBarrier's in-flight table. Seal does not clear that
// table, so epochPins records which epochs this StorageBarrier already holds
// one pin for. The set is never pruned: an epoch pinned once stays pinned for
// the life of the process.

// pinEpochs holds one in-flight pin on each epoch for the life of the process.
// An epoch already pinned by this barrier is left as it is. The new pin is
// taken before the set is consulted, so the count never drops to zero while a
// concurrent pin of the same epoch settles.
func (b *StorageBarrier) pinEpochs(epochs ...uint64) {
	for _, epoch := range epochs {
		b.barrier.RegisterInFlightEpoch(epoch)
		b.keepOnePin(epoch)
	}
}

// pinKnownEpochs pins the current epoch and every known epoch a persisted
// root-key blob names. It runs before the EpochBarrier is unsealed so that no
// purge can run first.
func (b *StorageBarrier) pinKnownEpochs(sealed *SealedRootKey) {
	if sealed.Epoch != 0 {
		b.pinEpochs(sealed.Epoch)
	}
	b.pinEpochs(sealed.KnownEpochs...)
}

// encrypt seals plaintext under the current epoch and pins that epoch in the
// same step. It is the only way this type produces ciphertext, so no value it
// writes can outlive the DEK that opens it.
func (b *StorageBarrier) encrypt(plaintext []byte) ([]byte, error) {
	ciphertext, epoch, err := b.barrier.EncryptWithEpochPinned(nil, plaintext)
	if err != nil {
		return nil, err
	}
	b.keepOnePin(epoch)
	return ciphertext, nil
}

// keepOnePin settles a pin just taken on epoch. The first pin of an epoch is
// kept. Any later one is released, which leaves exactly one pin per epoch no
// matter how many writes sealed under it.
func (b *StorageBarrier) keepOnePin(epoch uint64) {
	if _, held := b.epochPins.LoadOrStore(epoch, struct{}{}); held {
		b.barrier.DeregisterInFlightEpoch(epoch)
	}
}
