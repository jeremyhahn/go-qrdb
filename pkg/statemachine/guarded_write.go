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
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file holds every implementation of protocol.OpGuarded in this package,
// together, for the reason guarded_ops.go gives for keeping 66 and 67
// together: an opcode registered in some handler tables and not others is a
// defect nothing catches at build time (go-qrdb#86, #175). The conformance
// test beside this file drives the same guarded write through all four.
//
// A guarded write is any ordinary write command wrapped with compares that
// must hold at the entry's own log position (see protocol.OpGuarded). Each
// state machine evaluates the guard through the SAME read its own
// CompareAndSwap uses, and then dispatches the inner command through its own
// handler table at the revision the outer entry was given. The guard and the
// write are therefore one entry: go-quicraft runs Update single-threaded, so
// nothing can apply between the read and the write.
//
// A guard that does not hold is a successful application that changes
// nothing, reported in Result.Data (protocol.EncodeGuardRefusal); returning a
// Go error instead would halt lastApplied and stall the shard. A malformed
// payload or a read fault IS returned as an error, classified exactly like
// any other decode or storage failure.

// applyGuardedWrite is the body every state machine's OpGuarded handler shares.
// read is the state machine's own point read; handlers is its dispatch table.
func applyGuardedWrite(payload []byte, revision uint64, read protocol.GuardReader, handlers map[byte]updateHandler) (sm.Result, error) {
	inner, refusal, err := protocol.OpenGuarded(payload, read)
	if err != nil {
		return sm.Result{}, classifyGuardError(err)
	}
	if refusal != nil {
		return sm.Result{Data: refusal}, nil
	}
	handler, ok := handlers[inner[0]]
	if !ok {
		return sm.Result{}, &InvalidOpcodeError{Op: inner[0]}
	}
	return handler(inner[1:], revision)
}

// classifyGuardError maps an OpenGuarded failure onto this package's error
// types: a read fault is a StorageError, anything else is malformed data.
func classifyGuardError(err error) error {
	var readErr *protocol.GuardReadError
	if errors.As(err, &readErr) {
		return &StorageError{Op: "guard_get", Inner: readErr.Err}
	}
	return &IncompleteDataError{Reason: err.Error()}
}

// lookupReader adapts a Lookup-style point read -- (value or nil, error) --
// to a protocol.GuardReader.
func lookupReader(lookup func(key []byte) (interface{}, error)) protocol.GuardReader {
	return func(key []byte) ([]byte, error) {
		v, err := lookup(key)
		if err != nil || v == nil {
			return nil, err
		}
		b, _ := v.([]byte)
		return b, nil
	}
}

// handleGuarded applies an OpGuarded entry, reading the guard through the
// backend -- the read handleCompareAndSwap uses.
func (k *KVStateMachine) handleGuarded(payload []byte, revision uint64) (sm.Result, error) {
	return applyGuardedWrite(payload, revision, k.backend.Get, k.handlers)
}

// handleGuarded applies an OpGuarded entry, reading the guard through the
// map under r.mu -- the read handleCompareAndSwap uses.
func (r *RegularStateMachine) handleGuarded(payload []byte, revision uint64) (sm.Result, error) {
	return applyGuardedWrite(payload, revision, lookupReader(r.lookupKey), r.handlers)
}

// handleGuarded applies an OpGuarded entry, reading the guard through the
// shard cache with storage fallback -- the read handleCompareAndSwap uses --
// through guardRead, which remembers a miss.
func (f *FastStateMachine) handleGuarded(payload []byte, revision uint64) (sm.Result, error) {
	return applyGuardedWrite(payload, revision, f.guardRead, f.handlers)
}

// guardRead is lookupKey for a guard, with one addition: a key storage does
// not hold is remembered in the shard cache as a tombstone.
//
// A guard reads the same key on every write to a database, and for every
// database whose fence marker is ABSENT while it is live -- one with no
// incarnation of its own -- that read misses the cache and goes to storage
// every time. A tombstone is exactly what the cache already holds for a
// deleted key: SaveSnapshot, lookupPrefix and listKeys all skip it, and any
// later write to the key replaces it. It is only recorded when the cache
// still has no entry for the key, so it never shadows a write.
func (f *FastStateMachine) guardRead(key []byte) ([]byte, error) {
	k := string(key)
	s := f.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()
	if found {
		return val, nil
	}

	stored, err := f.storage.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	if stored == nil {
		s.mu.Lock()
		if _, raced := s.data[k]; !raced {
			s.data[k] = nil
		}
		s.mu.Unlock()
	}
	return stored, nil
}

// handleGuarded applies an OpGuarded entry, reading the guard through the
// shard cache with storage fallback -- the read handleCompareAndSwap uses.
func (c *ConcurrentStateMachine) handleGuarded(payload []byte, revision uint64) (sm.Result, error) {
	return applyGuardedWrite(payload, revision, lookupReader(c.lookupConcurrentKey), c.handlers)
}
