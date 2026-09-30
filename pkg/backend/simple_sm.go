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

package backend

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	qkv "github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Compile-time interface check.
var _ SMAdapter = (*SimpleStateMachine)(nil)

// SimpleStateMachine wraps a storage.Storage and decodes the same kv wire
// protocol used by PebbleStateMachine. It supports only basic Put and Delete
// operations. Extended opcodes (MVCC, leases, indexes) return
// UnsupportedOperationError.
type SimpleStateMachine struct {
	store    storage.Storage
	hub      *watch.Hub
	handlers map[byte]simpleHandler
}

// simpleHandler processes a single decoded kv.Command and returns the
// result.
type simpleHandler func(cmd kv.Command) (sm.Result, error)

// NewSimpleStateMachine creates a SimpleStateMachine backed by the given
// storage. An optional watch.Hub enables watch notifications on
// Put and Delete operations. Pass nil to disable notifications.
func NewSimpleStateMachine(store storage.Storage, hub *watch.Hub) *SimpleStateMachine {
	ssm := &SimpleStateMachine{
		store: store,
		hub:   hub,
	}
	ssm.handlers = map[byte]simpleHandler{
		kv.OpPut:    ssm.handlePut,
		kv.OpDelete: ssm.handleDelete,
	}
	return ssm
}

// Update applies a batch of entries to the underlying storage engine.
// Each entry is decoded using the kv wire protocol and dispatched to the
// appropriate handler. Results are written to the corresponding index in
// the results slice. An opcode this state machine does not implement is
// refused in the result payload rather than halting Apply
// (statemachine.ResolveApplyError; go-qrdb#520).
func (s *SimpleStateMachine) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd, err := kv.DecodeCommand(entries[i].Cmd)
		if err != nil {
			// A command this state machine cannot decode -- an extended
			// protocol opcode, a guarded write (protocol.OpGuarded), or
			// corrupt bytes -- applied nothing. That is reported as an
			// application error in the result, the encoding every proposer
			// reads a refusal from; the Value:1 this used to report is
			// what a successful write looks like to KVService, so the
			// write was acknowledged and silently dropped.
			results[i] = sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorCorrupt, (&UndecodableCommandError{Err: err}).Error())}
			continue
		}

		var result sm.Result
		handler, ok := s.handlers[cmd.Op]
		if ok {
			result, err = handler(cmd)
		} else {
			err = &UnsupportedOperationError{Op: cmd.Op}
		}

		// #nosec G602 -- i ranges over entries.
		resolved, stop, applyErr := statemachine.ResolveApplyError(slog.Default(), entries[i].Index, result, err)
		if stop {
			// The failure marker predates the classifier and is kept: Raft
			// discards results for a halted Apply, but LocalBackend calls
			// Update directly and reads them.
			// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
			results[i] = sm.Result{Value: 1}
			return applyErr
		}
		// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
		results[i] = resolved
	}
	return nil
}

// Lookup performs a read query against the storage engine. Supported
// query types:
//   - []byte: interpreted as a key for Get
//   - string: interpreted as a key for Get
//   - kv.ListQuery: lists keys matching the prefix
//
// All other types return LookupTypeError.
func (s *SimpleStateMachine) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	switch q := query.(type) {
	case []byte:
		val, err := s.store.Get(q)
		if val == nil {
			return nil, err
		}
		return val, err
	case string:
		val, err := s.store.Get([]byte(q))
		if val == nil {
			return nil, err
		}
		return val, err
	case kv.ListQuery:
		keys, err := s.store.List([]byte(q.Prefix))
		if err != nil {
			return nil, err
		}
		// Convert [][]byte to []string for backward compatibility
		// with service layer expectations.
		result := make([]string, len(keys))
		for i, k := range keys {
			result[i] = string(k)
		}
		return result, nil
	default:
		return nil, &LookupTypeError{Type: fmt.Sprintf("%T", query)}
	}
}

// Close releases all resources held by the underlying storage.
func (s *SimpleStateMachine) Close(_ context.Context) error {
	return s.store.Close()
}

// handlePut stores a key-value pair and optionally emits a watch
// notification via the Hub.
func (s *SimpleStateMachine) handlePut(cmd kv.Command) (sm.Result, error) {
	if err := s.store.Set(cmd.Key, cmd.Value); err != nil {
		return sm.Result{Value: 1}, err
	}
	if s.hub != nil {
		kvVal := &qkv.KeyValue{
			Key:   cmd.Key,
			Value: cmd.Value,
		}
		s.hub.NotifyPut(cmd.Key, kvVal, nil)
	}
	return sm.Result{Value: uint64(len(cmd.Value))}, nil
}

// handleDelete removes a key and optionally emits a watch notification
// via the Hub.
func (s *SimpleStateMachine) handleDelete(cmd kv.Command) (sm.Result, error) {
	if err := s.store.Delete(cmd.Key); err != nil {
		return sm.Result{Value: 1}, err
	}
	if s.hub != nil {
		kvVal := &qkv.KeyValue{
			Key: cmd.Key,
		}
		s.hub.NotifyDelete(cmd.Key, kvVal, nil)
	}
	return sm.Result{}, nil
}
