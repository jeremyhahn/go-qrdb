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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// errCompactionRecordWrite is the storage fault a compaction-state apply must
// not swallow.
var errCompactionRecordWrite = errors.New("compaction record write refused")

// recordRefusingBatch is a storage batch that refuses every write, so the
// compaction record can be neither staged nor removed.
type recordRefusingBatch struct{ storage.Batch }

func (recordRefusingBatch) Set(_, _ []byte) error { return errCompactionRecordWrite }
func (recordRefusingBatch) Delete(_ []byte) error { return errCompactionRecordWrite }

// A compaction-state command whose record cannot be written or removed is an
// apply FAULT: reporting success would leave this replica's record behind
// its peers', and the replica hash would diverge. Clearing a record, turning
// a policy off (which clears it) and setting one all return the storage
// fault.
func TestMVCCEngine_CompactionState_ReturnsARecordWriteFault(t *testing.T) {
	for name, cmd := range map[string]protocol.CompactionStateCommand{
		"clear":      {Kind: protocol.CompactionStateClear, Scope: "acme/app/"},
		"policy off": {Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"off"}}`)},
		"policy set": {Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":3}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			eng, stg := newMVCCEngine(t)
			defer func() { _ = stg.Close() }()
			defer func() { _ = eng.Close(context.Background()) }()
			b := stg.Batch()
			defer func() { _ = b.Close() }()
			entry := sm.Entry{Index: 9, Cmd: mustEncode(protocol.EncodeCompactionState(cmd))}
			_, err := eng.UpdateEntries(recordRefusingBatch{Batch: b}, []sm.Entry{entry}, make([]sm.Result, 1))
			if !errors.Is(err, errCompactionRecordWrite) {
				t.Fatalf("UpdateEntries = %v, want the record write fault", err)
			}
		})
	}
}
