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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestIndexManagerFailure pins the mapping every FSM's index handlers share:
// an ownership refusal is an application result that lets the apply advance,
// anything else stays the StorageError it was (go-qrdb#426).
func TestIndexManagerFailure(t *testing.T) {
	r, err := indexManagerFailure("index-create", &index.OwnershipError{Name: "n", Owner: "o"})
	if err != nil {
		t.Fatalf("an ownership refusal must not fail the apply: %v", err)
	}
	if name, owner, ok := protocol.DecodeIndexOwnershipError(r.Data); !ok || name != "n" || owner != "o" {
		t.Fatalf("result does not carry the refusal: %q %q %v", name, owner, ok)
	}

	_, err = indexManagerFailure("index-drop", errors.New("disk"))
	var se *StorageError
	if !errors.As(err, &se) || se.Op != "index-drop" {
		t.Fatalf("a manager failure stays a StorageError: %v", err)
	}
}

// TestFSMs_IndexOwnershipRefusalIsAnApplicationResult drives an ownership
// refusal from an embedder-supplied IndexManager through each FSM, and checks
// that the drop's owner options reach the manager.
func TestFSMs_IndexOwnershipRefusalIsAnApplicationResult(t *testing.T) {
	refusal := &index.OwnershipError{Name: "idx", Owner: "collection:db/c#1"}
	dropCmd := mustEncode(protocol.EncodeIndexDropOpts("idx", protocol.IndexDropOptions{CheckOwner: true, Owner: "collection:db/c#2"}))
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("idx", "f", protocol.IndexCreateOptions{Owner: "collection:db/c#2"}))

	apply := map[string]func(im *mockIndexManager, cmd []byte) (sm.Result, error){
		// Built from the exported constructors rather than the
		// !integration-constrained helpers in *_subsystem_test.go, so this
		// file carries no build constraint of its own.
		"regular": func(im *mockIndexManager, cmd []byte) (sm.Result, error) {
			rsm := NewRegularStateMachine(WithRegularIndexManager(im))
			results := make([]sm.Result, 1)
			err := rsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
			return results[0], err
		},
		"fast": func(im *mockIndexManager, cmd []byte) (sm.Result, error) {
			fsm, _ := newTestFSM(WithIndexManager(im))
			results := make([]sm.Result, 1)
			err := fsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
			return results[0], err
		},
		"kv": func(im *mockIndexManager, cmd []byte) (sm.Result, error) {
			k := NewKVStateMachine(NewStandardBackend(), WithKVIndexManager(im))
			results := make([]sm.Result, 1)
			err := k.Update([]sm.Entry{{Index: 1, Cmd: cmd}}, results)
			return results[0], err
		},
	}
	for name, run := range apply {
		t.Run(name, func(t *testing.T) {
			for op, cmd := range map[string][]byte{"create": createCmd, "drop": dropCmd} {
				im := &mockIndexManager{createErr: refusal, dropErr: refusal}
				r, err := run(im, cmd)
				if err != nil {
					t.Fatalf("%s: an ownership refusal must not fail the apply: %v", op, err)
				}
				if _, _, ok := protocol.DecodeIndexOwnershipError(r.Data); !ok {
					t.Fatalf("%s: result does not carry the refusal", op)
				}
				if op == "drop" {
					if len(im.dropOpts) != 1 || !im.dropOpts[0].CheckOwner || im.dropOpts[0].Owner != "collection:db/c#2" {
						t.Fatalf("drop options did not reach the manager: %+v", im.dropOpts)
					}
				}
			}
		})
	}
}
