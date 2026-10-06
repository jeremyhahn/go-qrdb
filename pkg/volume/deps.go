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

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// ---------------------------------------------------------------------------
// Narrow service interfaces used by the catalog.
//
// These are minimal subsets of pkg/service's fuller KVServicer and
// TransactionServicer, following the same pattern pkg/lock/deps.go
// documents: pkg/service's concrete types satisfy these interfaces
// structurally (service.Compare, service.Op and service.TransactionResponse
// are themselves aliases of the pkg/lock types used below), so no adapter
// and no explicit declaration is required on the service side.
//
// Depending on pkg/lock rather than pkg/service is what keeps this package a
// leaf: pkg/service already imports pkg/seal, and pkg/seal's VolumeLifecycle
// imports this package (decision D6), so a pkg/volume -> pkg/service import
// would cycle back through pkg/seal. pkg/lock carries none of that weight.
// ---------------------------------------------------------------------------

// KVServicer is the minimal replicated KV interface the catalog needs: Get
// and Scan for reads, Put for a record with no cross-key atomicity
// requirement (a node's own health report, owned by that node alone), and
// the three native single-key atomic primitives every KVServicer
// implementation already offers for the catalog's single-key operations
// (CreatePool, UpdatePool, a plain DeletePool with no assignments to
// guard against).
type KVServicer interface {
	// Get retrieves a value by key. Returns (nil, nil) when the key does
	// not exist, matching kvstore.KVStore's contract rather than
	// service.KVServicer's own (which returns *service.KeyNotFoundError on
	// a miss): this package cannot name that type without importing
	// pkg/service and cycling back through pkg/seal, so the composition
	// layer that wires a real service.KVService in here is the one that
	// adapts it to this contract -- exactly as
	// lifecycle.kvStoreAdapter.Get already does for kvstore.KVStore.
	Get(ctx context.Context, key string) ([]byte, error)

	// Put stores a key-value pair unconditionally.
	Put(ctx context.Context, key string, value []byte) error

	// PutIfAbsent atomically stores value under key only if key is absent.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)

	// CompareAndSwap atomically replaces key's value with next only when
	// its stored value still equals expected.
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)

	// CompareAndDelete atomically removes key only when its stored value
	// still equals expected.
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)

	// Scan returns every key-value pair matching prefix.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// TransactionServicer is the minimal atomic-transaction interface the
// catalog needs for Reserve and Release: a single Raft transaction that
// updates a Pool's ReservedBytes and writes (or deletes) an Assignment
// together (invariant 4), exactly as engineering.md specifies: "through the
// replicated system KV's service.TransactionServicer... the same
// transaction lifecycle.NewReplicatedSealBackend already uses for
// put-if-absent".
type TransactionServicer interface {
	Execute(ctx context.Context, compares []lock.Compare, successOps, failureOps []lock.Op) (*lock.TransactionResponse, error)
}

// MVCCServicer is KVServicer plus the lease-attached write Reporter needs to
// publish a node's own NodeReport the SAME way cluster.Advertiser publishes
// a cluster.Member: PutWithLease attaches the record to a lease so the
// underlying engine deletes it automatically when the lease expires --
// "when the lease expires, the key is automatically deleted" is
// service.MVCCKVServicer.PutWithLease's own contract, which this mirrors.
// kv.KeyValue (like lock.Compare/lock.Op) is pkg/kv's canonical,
// dependency-free type; service.KeyValue is an alias of it, so
// service.MVCCKVService satisfies this interface structurally.
type MVCCServicer interface {
	KVServicer
	PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error)
}
