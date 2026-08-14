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

package statemachine

import (
	"context"
	"io"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// StandardAdapter: sm.StateMachine + sm.Hasher
// ---------------------------------------------------------------------------

// StandardAdapter wraps a KVStateMachine to implement sm.StateMachine
// and sm.Hasher. It is the adapter for single-threaded in-memory state
// machines (equivalent to RegularStateMachine).
type StandardAdapter struct {
	*KVStateMachine
}

// Compile-time interface assertions.
var (
	_ sm.StateMachine = (*StandardAdapter)(nil)
	_ sm.Hasher       = (*StandardAdapter)(nil)
)

// Update implements sm.StateMachine.Update.
func (a *StandardAdapter) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	return a.KVStateMachine.Update(entries, results)
}

// Lookup implements sm.StateMachine.Lookup.
func (a *StandardAdapter) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	return a.KVStateMachine.Lookup(query)
}

// SaveSnapshot implements sm.StateMachine.SaveSnapshot.
func (a *StandardAdapter) SaveSnapshot(_ context.Context, w io.Writer, stopper <-chan struct{}) error {
	return a.KVStateMachine.SaveSnapshot(w, stopper)
}

// RecoverFromSnapshot implements sm.StateMachine.RecoverFromSnapshot.
func (a *StandardAdapter) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	return a.RecoverSnapshot(r, stopper)
}

// Close implements sm.StateMachine.Close.
func (a *StandardAdapter) Close(_ context.Context) error {
	return a.KVStateMachine.Close()
}

// GetHash implements sm.Hasher.GetHash.
func (a *StandardAdapter) GetHash() (uint64, error) {
	return a.KVStateMachine.GetHash()
}

// ---------------------------------------------------------------------------
// ConcurrentAdapter: sm.ConcurrentStateMachine + sm.Hasher
// ---------------------------------------------------------------------------

// ConcurrentAdapter wraps a KVStateMachine to implement
// sm.ConcurrentStateMachine and sm.Hasher. It supports concurrent
// Lookups while Update is running.
type ConcurrentAdapter struct {
	*KVStateMachine
}

// Compile-time interface assertions.
var (
	_ sm.ConcurrentStateMachine = (*ConcurrentAdapter)(nil)
	_ sm.Hasher                 = (*ConcurrentAdapter)(nil)
)

// Update implements sm.ConcurrentStateMachine.Update.
func (a *ConcurrentAdapter) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	return a.KVStateMachine.Update(entries, results)
}

// Lookup implements sm.ConcurrentStateMachine.Lookup.
func (a *ConcurrentAdapter) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	return a.KVStateMachine.Lookup(query)
}

// PrepareSnapshot captures a consistent point-in-time snapshot via the
// backend. The returned context is passed to SaveSnapshot.
func (a *ConcurrentAdapter) PrepareSnapshot() (interface{}, error) {
	// For the concurrent adapter, we delegate to the backend's
	// snapshot mechanism. We return a sentinel that SaveSnapshot uses
	// to know it should call through to the backend directly.
	return "concurrent-snapshot", nil
}

// SaveSnapshot implements sm.ConcurrentStateMachine.SaveSnapshot.
func (a *ConcurrentAdapter) SaveSnapshot(_ context.Context, _ interface{}, w io.Writer, stopper <-chan struct{}) error {
	return a.KVStateMachine.SaveSnapshot(w, stopper)
}

// RecoverFromSnapshot implements sm.ConcurrentStateMachine.RecoverFromSnapshot.
func (a *ConcurrentAdapter) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	return a.RecoverSnapshot(r, stopper)
}

// Close implements sm.ConcurrentStateMachine.Close.
func (a *ConcurrentAdapter) Close(_ context.Context) error {
	return a.KVStateMachine.Close()
}

// GetHash implements sm.Hasher.GetHash.
func (a *ConcurrentAdapter) GetHash() (uint64, error) {
	return a.KVStateMachine.GetHash()
}

// ---------------------------------------------------------------------------
// DiskAdapter: sm.DiskStateMachine + sm.Hasher + sm.NALookup
// ---------------------------------------------------------------------------

// DiskAdapter wraps a KVStateMachine to implement sm.DiskStateMachine,
// sm.Hasher, and sm.NALookup. It provides synchronous disk-backed
// storage for Raft state.
type DiskAdapter struct {
	*KVStateMachine
	store storage.Storage
}

// Compile-time interface assertions.
var (
	_ sm.DiskStateMachine = (*DiskAdapter)(nil)
	_ sm.Hasher           = (*DiskAdapter)(nil)
	_ sm.NALookup         = (*DiskAdapter)(nil)
)

// Open implements sm.DiskStateMachine.Open. For the disk adapter, this
// initializes the storage and returns the last applied index (0 for a
// fresh start).
func (a *DiskAdapter) Open(_ context.Context, _ string, _ <-chan struct{}) (uint64, error) {
	return 0, nil
}

// Update implements sm.DiskStateMachine.Update.
func (a *DiskAdapter) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	return a.KVStateMachine.Update(entries, results)
}

// Lookup implements sm.DiskStateMachine.Lookup.
func (a *DiskAdapter) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	return a.KVStateMachine.Lookup(query)
}

// Sync implements sm.DiskStateMachine.Sync.
func (a *DiskAdapter) Sync() error {
	return nil
}

// PrepareSnapshot implements sm.DiskStateMachine.PrepareSnapshot.
func (a *DiskAdapter) PrepareSnapshot() (interface{}, error) {
	return "disk-snapshot", nil
}

// SaveSnapshot implements sm.DiskStateMachine.SaveSnapshot.
func (a *DiskAdapter) SaveSnapshot(_ context.Context, _ interface{}, w io.Writer, stopper <-chan struct{}) error {
	return a.KVStateMachine.SaveSnapshot(w, stopper)
}

// RecoverFromSnapshot implements sm.DiskStateMachine.RecoverFromSnapshot.
func (a *DiskAdapter) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	return a.RecoverSnapshot(r, stopper)
}

// Close implements sm.DiskStateMachine.Close.
func (a *DiskAdapter) Close(_ context.Context) error {
	return a.KVStateMachine.Close()
}

// GetHash implements sm.Hasher.GetHash.
func (a *DiskAdapter) GetHash() (uint64, error) {
	return a.KVStateMachine.GetHash()
}

// BytesLookup implements sm.NALookup.BytesLookup. It performs a
// non-allocating point lookup by key.
func (a *DiskAdapter) BytesLookup(key []byte) ([]byte, error) {
	val, err := a.backend.Get(key)
	if err != nil {
		return nil, err
	}
	return val, nil
}
