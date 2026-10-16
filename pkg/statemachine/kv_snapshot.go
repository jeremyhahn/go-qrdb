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

import "io"

// SaveSnapshot delegates snapshot serialization to the backend.
func (k *KVStateMachine) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	return k.backend.SaveSnapshot(w, stopper)
}

// RecoverSnapshot delegates snapshot recovery to the backend, holding
// batch exclusively for the whole recovery (go-qrdb#584, same class as
// GetHash's own fix): GetHash takes batch.Lock() too, and without this a
// hash could land mid-recovery, a state no replica settles in, same
// reasoning as a hash mid-Update.
//
// The snapshot carries the lease table, so the Lessor is rebuilt from it
// before any entry applies over the recovered state.
func (k *KVStateMachine) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	k.batch.Lock()
	defer k.batch.Unlock()
	if err := k.backend.RecoverSnapshot(r, stopper); err != nil {
		return err
	}
	return k.restoreLessor()
}

// GetHash computes an xxhash64 over the replicated records, between
// batches: it waits for a batch Update is applying, so the hash is of the
// state after the whole batch. The applied-index marker is left out: it
// changes with every entry, and an entry that writes nothing (a lease
// keep-alive) must leave the hash unchanged.
func (k *KVStateMachine) GetHash() (uint64, error) {
	k.batch.Lock()
	defer k.batch.Unlock()
	return xxhashRecords(func(fn RecordVisitor) error {
		_, err := k.visitState(fn)
		return err
	})
}

// Close delegates resource cleanup to the backend.
func (k *KVStateMachine) Close() error {
	return k.backend.Close()
}
