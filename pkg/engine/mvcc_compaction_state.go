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

package engine

import (
	"encoding/json"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// compactionStateAppliers apply each OpCompactionState kind to the scope's
// current state (nil when it has no record) and stage the result into the
// apply batch. A map rather than a switch: the kinds are values.
var compactionStateAppliers = map[protocol.CompactionStateKind]func(batch storage.Batch, key []byte, state *compaction.State, cmd protocol.CompactionStateCommand) (sm.Result, error){
	protocol.CompactionStateSetPolicy: applyCompactionPolicy,
	protocol.CompactionStateClear:     applyCompactionClear,
	protocol.CompactionStateSample:    applyCompactionSample,
}

// handleCompactionState processes an OpCompactionState command: it writes the
// scope's scheduled-compaction record (pkg/compaction) in the state machine's
// reserved keyspace, staged into the apply batch like the lease table, so the
// record is replicated, carried by snapshots and covered by the replica hash.
// It consumes no revision and is never an MVCC key.
//
// A scope that is not one database's keyspace, or a policy that does not
// validate, is a deterministic consequence of the command: it is refused in
// the result as an invalid-kind application error, which every replica makes
// alike, and writes nothing.
func handleCompactionState(_ *mvcc.Store, _ *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	cmd, err := protocol.DecodeCompactionState(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	if !compaction.ValidScope(cmd.Scope) {
		return compactionStateRefusal("compaction state: scope is not one database's keyspace"), nil, nil
	}
	key := compaction.RecordKey(cmd.Scope)
	state, err := readCompactionState(batch, key)
	if err != nil {
		return sm.Result{}, nil, err
	}
	// DecodeCompactionState refuses a kind with no applier, so the lookup
	// cannot miss.
	result, err := compactionStateAppliers[cmd.Kind](batch, key, state, cmd)
	return result, nil, err
}

// applyCompactionPolicy binds the scope's policy (compaction.Binding): the
// policy and the database incarnation it belongs to. An off policy removes
// the record -- off and never-set are one state. Binding another incarnation
// replaces the record outright, samples included (compaction.State.Bind).
func applyCompactionPolicy(batch storage.Batch, key []byte, state *compaction.State, cmd protocol.CompactionStateCommand) (sm.Result, error) {
	var binding compaction.Binding
	if err := json.Unmarshal(cmd.Policy, &binding); err != nil {
		return compactionStateRefusal("compaction state: policy binding is not valid JSON"), nil
	}
	if binding.Policy != nil {
		if err := binding.Policy.Validate(); err != nil {
			return compactionStateRefusal(err.Error()), nil
		}
	}
	binding.Policy = binding.Policy.Normalized()
	if binding.Policy == nil {
		return applyCompactionClear(batch, key, state, cmd)
	}
	if state == nil {
		state = &compaction.State{}
	}
	state.Bind(binding)
	if err := writeCompactionState(batch, key, state); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// applyCompactionClear removes the scope's record.
func applyCompactionClear(batch storage.Batch, key []byte, _ *compaction.State, _ protocol.CompactionStateCommand) (sm.Result, error) {
	if err := batch.Delete(key); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// applyCompactionSample records a revision sample when the scope's period
// policy keeps it (compaction.State.Record). Result Value is 1 when it was
// kept, 0 when it was not -- a duplicate, a sample inside the interval, or a
// scope with no period policy -- which writes nothing: replaying a sample is
// harmless.
func applyCompactionSample(batch storage.Batch, key []byte, state *compaction.State, cmd protocol.CompactionStateCommand) (sm.Result, error) {
	if state == nil || !state.Record(cmd.At, cmd.Revision) {
		return sm.Result{}, nil
	}
	if err := writeCompactionState(batch, key, state); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// compactionStateRefusal is an invalid-kind application error result.
func compactionStateRefusal(msg string) sm.Result {
	return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorInvalid, msg)}
}

// readCompactionState reads the record at key, nil when there is none.
func readCompactionState(r storage.Reader, key []byte) (*compaction.State, error) {
	data, err := r.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	return compaction.DecodeState(data)
}

// writeCompactionState stages state at key.
func writeCompactionState(batch storage.Batch, key []byte, state *compaction.State) error {
	data, err := state.Encode()
	if err != nil {
		return err
	}
	return batch.Set(key, data)
}

// compactionStates reads every scope's record from r.
func compactionStates(r storage.Reader) (map[string]*compaction.State, error) {
	iter := r.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte(compaction.TablePrefix),
		UpperBound: prefixEndBytes([]byte(compaction.TablePrefix)),
	})
	defer func() { _ = iter.Close() }()

	out := make(map[string]*compaction.State)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		scope, ok := compaction.ScopeOf(iter.Key())
		if !ok {
			continue
		}
		state, err := compaction.DecodeState(iter.Value())
		if err != nil {
			return nil, err
		}
		out[scope] = state
	}
	return out, iter.Error()
}
