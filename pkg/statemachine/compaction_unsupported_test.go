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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestStateMachines_RefuseCompactionAsUnknownOpcode: MVCC compaction is the
// MVCC engine's (pkg/engine), which owns the per-prefix floors and refuses a
// read below them. No state machine in this package compacts, even with an
// MVCCStore attached -- MVCCStore has no Compact -- so each refuses the
// compaction opcode exactly as it refuses any verb it was not built with: an
// application error naming the opcode, never an error returned to Raft and
// never a silent success.
func TestStateMachines_RefuseCompactionAsUnknownOpcode(t *testing.T) {
	cmd := mustEncode(protocol.EncodeMVCCCompactPrefix([]byte("acme/app/"), 5))
	apply := map[string]func(t *testing.T) (sm.Result, error){
		"regular": func(t *testing.T) (sm.Result, error) {
			return regularApplyCmd(t, newRegularWithMVCC(&mockMVCCStore{}), cmd)
		},
		"concurrent": func(t *testing.T) (sm.Result, error) {
			csm, _ := newConcurrentWithMVCC(&mockMVCCStore{})
			return concurrentApplyCmd(t, csm, cmd)
		},
		"fast": func(t *testing.T) (sm.Result, error) {
			fsm, _ := newTestFSMWithMVCC(&mockMVCCStore{})
			results := make([]sm.Result, 1)
			err := fsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
			return results[0], err
		},
		"kv": func(t *testing.T) (sm.Result, error) {
			k := newTestKVSM(WithKVMVCCStore(&kvMockMVCCStore{}))
			results := make([]sm.Result, 1)
			err := k.Update([]sm.Entry{{Cmd: cmd}}, results)
			return results[0], err
		},
	}
	for name, run := range apply {
		t.Run(name, func(t *testing.T) {
			result, err := run(t)
			assertOpcodeRefused(t, err, result)
			assertRefusalNamesOpcode(t, result, protocol.OpMVCCCompactPrefix)
		})
	}
}
