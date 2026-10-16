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
	"bytes"
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
)

// stateDigest answers a statedigest.Query: the SHA-256 state digest of every
// replicated record, with the applied index read from the same view.
//
// The batch lock is taken exclusively, so no apply batch is in flight while
// the records are visited and the applied-index marker read with them names
// exactly the index the records reflect.
func (k *KVStateMachine) stateDigest() (statedigest.Digest, error) {
	k.batch.Lock()
	defer k.batch.Unlock()

	b := statedigest.NewBuilder()
	applied, err := k.visitState(b.Add)
	if err != nil {
		return statedigest.Digest{}, err
	}
	return b.Finish(applied), nil
}

// visitState calls fn for every replicated record in ascending key order and
// returns the applied index recorded beside them; absent, it is zero. The
// applied-index marker is reported, not visited: it is the one record whose
// value is the index rather than state. The caller holds the batch lock
// exclusively.
func (k *KVStateMachine) visitState(fn RecordVisitor) (uint64, error) {
	marker := []byte(diskMetaKeyLastApplied)
	var applied uint64
	err := k.backend.Records(func(key, value []byte) error {
		if !bytes.Equal(key, marker) {
			return fn(key, value)
		}
		if len(value) != 8 {
			return &IncompleteDataError{Reason: "last-applied marker has invalid length"}
		}
		applied = binary.BigEndian.Uint64(value)
		return nil
	})
	return applied, err
}
