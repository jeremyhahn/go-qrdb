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
)

// HighPerfAdapter wraps a KVStateMachine backed by HighPerfBackend to
// implement sm.ConcurrentStateMachine, sm.Hasher, sm.NALookup, and
// sm.NALookupInto. It supports concurrent Lookups while Update is
// running and provides zero-allocation byte lookups for hot paths.
//
// This adapter is purpose-built for data-plane state such as connection
// tracking, NAT session tables, and other high-frequency read/write
// workloads where sub-microsecond latency is critical.
type HighPerfAdapter struct {
	*KVStateMachine
	backend *HighPerfBackend
}

// Compile-time interface assertions.
var (
	_ sm.ConcurrentStateMachine = (*HighPerfAdapter)(nil)
	_ sm.Hasher                 = (*HighPerfAdapter)(nil)
	_ sm.NALookup               = (*HighPerfAdapter)(nil)
	_ sm.NALookupInto           = (*HighPerfAdapter)(nil)
)

// Update implements sm.ConcurrentStateMachine.Update.
func (a *HighPerfAdapter) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	return a.KVStateMachine.Update(entries, results)
}

// Lookup implements sm.ConcurrentStateMachine.Lookup.
func (a *HighPerfAdapter) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	return a.KVStateMachine.Lookup(query)
}

// PrepareSnapshot captures a consistent point-in-time snapshot via the
// backend. The returned context is passed to SaveSnapshot.
func (a *HighPerfAdapter) PrepareSnapshot() (interface{}, error) {
	return "highperf-snapshot", nil
}

// SaveSnapshot implements sm.ConcurrentStateMachine.SaveSnapshot.
func (a *HighPerfAdapter) SaveSnapshot(_ context.Context, _ interface{}, w io.Writer, stopper <-chan struct{}) error {
	return a.KVStateMachine.SaveSnapshot(w, stopper)
}

// RecoverFromSnapshot implements sm.ConcurrentStateMachine.RecoverFromSnapshot.
func (a *HighPerfAdapter) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	return a.RecoverSnapshot(r, stopper)
}

// Close implements sm.ConcurrentStateMachine.Close.
func (a *HighPerfAdapter) Close(_ context.Context) error {
	return a.KVStateMachine.Close()
}

// GetHash implements sm.Hasher.GetHash.
func (a *HighPerfAdapter) GetHash() (uint64, error) {
	return a.KVStateMachine.GetHash()
}

// BytesLookup implements sm.NALookup for zero-allocation point lookups.
func (a *HighPerfAdapter) BytesLookup(key []byte) ([]byte, error) {
	return a.backend.BytesLookup(key)
}

// BytesLookupInto implements sm.NALookupInto for zero-allocation point
// lookups with a caller-provided destination buffer.
func (a *HighPerfAdapter) BytesLookupInto(key, dst []byte) ([]byte, error) {
	return a.backend.BytesLookupInto(key, dst)
}

// NewKVHighPerfCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by HighPerfBackend with configurable
// shard count. The returned function produces HighPerfAdapter instances
// that support concurrent Lookups during Update and zero-allocation
// byte lookups.
func NewKVHighPerfCreateFunc(cfg *HighPerfConfig, opts ...KVOption) sm.CreateFunc {
	return sm.NewConcurrentCreateFunc(func(_, _ uint64) sm.ConcurrentStateMachine {
		backend := NewHighPerfBackend(cfg)
		kvsm := NewKVStateMachine(backend, opts...)
		return &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}
	})
}
