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
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// guardAbsent wraps inner with "fence is absent".
func guardAbsent(t *testing.T, fence string, inner []byte) []byte {
	t.Helper()
	c, err := protocol.EncodeCompare([]byte(fence), protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	cmd, err := protocol.EncodeGuarded([][]byte{c}, inner)
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	return cmd
}

// Every state machine honours the same guarded-write contract: the inner
// write applies when the guard holds, is refused -- successfully, with the
// refusal in Result.Data and nothing written -- when it does not, and a
// command with no guard applies unconditionally exactly as before.
func TestStateMachines_GuardedWriteContract(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("dispatches OpGuarded", func(t *testing.T) {
				if _, ok := build(t).handlers[protocol.OpGuarded]; !ok {
					t.Fatalf("%s has no handler for OpGuarded: a guarded write would be refused as an unknown opcode", name)
				}
			})

			t.Run("applies the write when the guard holds", func(t *testing.T) {
				target := build(t)
				res, err := target.apply(t, guardAbsent(t, "fence", buildKVCmd(kv.OpPut, []byte("k"), []byte("v"))))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if _, refused := protocol.DecodeAppError(res.Data); refused {
					t.Fatalf("a guard that holds was refused: %x", res.Data)
				}
				if got := target.lookup(t, []byte("k")); string(got) != "v" {
					t.Fatalf("k = %q, want %q", got, "v")
				}
			})

			t.Run("refuses the write once the fence is present", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("fence"), []byte("dropping"))); err != nil {
					t.Fatalf("fence Put: %v", err)
				}
				res, err := target.apply(t, guardAbsent(t, "fence", buildKVCmd(kv.OpPut, []byte("k"), []byte("late"))))
				if err != nil {
					t.Fatalf("a refused guard must not be an apply error (it would stall the shard): %v", err)
				}
				key, refused := protocol.DecodeGuardRefusal(res.Data)
				if !refused || string(key) != "fence" {
					t.Fatalf("Result.Data = %x, want a guard refusal naming %q", res.Data, "fence")
				}
				if got := target.lookup(t, []byte("k")); got != nil {
					t.Fatalf("the refused write landed: k = %q", got)
				}
			})

			t.Run("a guard-less command still applies unconditionally", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("fence"), []byte("dropping"))); err != nil {
					t.Fatalf("fence Put: %v", err)
				}
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v"))); err != nil {
					t.Fatalf("unguarded Put: %v", err)
				}
				if got := target.lookup(t, []byte("k")); string(got) != "v" {
					t.Fatalf("k = %q, want %q: an absent guard must mean unconditional", got, "v")
				}
			})

			t.Run("a malformed guard is a decode fault", func(t *testing.T) {
				target := build(t)
				_, err := target.apply(t, []byte{protocol.OpGuarded, 1})
				if err == nil {
					t.Fatal("a malformed guarded entry applied without error")
				}
			})

			t.Run("an inner opcode it does not serve is a refusal", func(t *testing.T) {
				target := build(t)
				res, err := target.apply(t, guardAbsent(t, "fence", []byte{250, 0, 0}))
				if err != nil {
					t.Fatalf("an unknown inner opcode must be answered, not fatal: %v", err)
				}
				if _, refused := protocol.DecodeAppError(res.Data); !refused {
					t.Fatalf("Result.Data = %x, want a capability refusal", res.Data)
				}
			})
		})
	}
}

func TestClassifyGuardError(t *testing.T) {
	boom := errors.New("disk")
	var storageErr *StorageError
	if err := classifyGuardError(&protocol.GuardReadError{Key: []byte("k"), Err: boom}); !errors.As(err, &storageErr) || !errors.Is(err, boom) {
		t.Fatalf("read fault classified as %v (%T), want *StorageError wrapping it", err, err)
	}
	var incomplete *IncompleteDataError
	if err := classifyGuardError(&protocol.InvalidCommandError{Reason: "x"}); !errors.As(err, &incomplete) {
		t.Fatalf("decode fault classified as %T, want *IncompleteDataError", err)
	}
}

func TestLookupReader(t *testing.T) {
	boom := errors.New("disk")
	tests := []struct {
		name    string
		lookup  func([]byte) (interface{}, error)
		want    []byte
		wantErr bool
	}{
		{"present", func([]byte) (interface{}, error) { return []byte("v"), nil }, []byte("v"), false},
		{"absent", func([]byte) (interface{}, error) { return nil, nil }, nil, false},
		{"fault", func([]byte) (interface{}, error) { return nil, boom }, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookupReader(tt.lookup)([]byte("k"))
			if (err != nil) != tt.wantErr || string(got) != string(tt.want) {
				t.Fatalf("lookupReader = (%q, %v)", got, err)
			}
			if !tt.wantErr && tt.want == nil && got != nil {
				t.Fatal("an absent key must read as nil")
			}
		})
	}
}

// A read fault during guard evaluation is a storage error on every state
// machine that can fault -- never a refusal, which would let replicas whose
// reads succeeded apply a write this one refused.
func TestApplyGuardedWrite_ReadFaultIsAStorageError(t *testing.T) {
	boom := errors.New("disk")
	payload := guardAbsent(t, "fence", buildKVCmd(kv.OpPut, []byte("k"), []byte("v")))[1:]
	called := false
	handlers := map[byte]updateHandler{kv.OpPut: func([]byte, uint64) (sm.Result, error) {
		called = true
		return sm.Result{}, nil
	}}

	_, err := applyGuardedWrite(payload, 1, func([]byte) ([]byte, error) { return nil, boom }, handlers)
	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("error = %v (%T), want *StorageError", err, err)
	}
	if called {
		t.Fatal("the inner write ran although the guard could not be evaluated")
	}
}

// countingStorage counts storage reads of one key and can fail them.
type countingStorage struct {
	*memStorage
	key   string
	gets  int
	fault error
}

func (c *countingStorage) Get(key []byte) ([]byte, error) {
	if string(key) == c.key {
		c.gets++
		if c.fault != nil {
			return nil, c.fault
		}
	}
	return c.memStorage.Get(key)
}

// A guard on an absent marker -- a database with no incarnation of its own
// -- reads storage once, not on every write: the miss is remembered in the
// shard cache, and a later write to the marker replaces it.
func TestFastStateMachine_GuardRemembersAnAbsentMarker(t *testing.T) {
	st := &countingStorage{memStorage: newMemStorage(), key: "fence"}
	f := NewFastStateMachine(1, 1, st)
	apply := func(cmd []byte) sm.Result {
		t.Helper()
		results := make([]sm.Result, 1)
		if err := f.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
			t.Fatalf("Update: %v", err)
		}
		return results[0]
	}

	for i := 0; i < 3; i++ {
		res := apply(guardAbsent(t, "fence", buildKVCmd(kv.OpPut, []byte("k"), []byte("v"))))
		if _, refused := protocol.DecodeGuardRefusal(res.Data); refused {
			t.Fatalf("write %d refused while the marker is absent", i)
		}
	}
	if st.gets != 1 {
		t.Fatalf("storage reads of the marker = %d, want 1: the miss must be remembered", st.gets)
	}

	apply(buildKVCmd(kv.OpPut, []byte("fence"), []byte("dropped")))
	res := apply(guardAbsent(t, "fence", buildKVCmd(kv.OpPut, []byte("k"), []byte("late"))))
	if _, refused := protocol.DecodeGuardRefusal(res.Data); !refused {
		t.Fatal("a remembered miss shadowed the marker written after it")
	}
}

// A storage fault is never remembered as a miss, and is reported.
func TestFastStateMachine_GuardReadFaultIsNotCached(t *testing.T) {
	boom := errors.New("disk")
	st := &countingStorage{memStorage: newMemStorage(), key: "fence", fault: boom}
	f := NewFastStateMachine(1, 1, st)
	if _, err := f.guardRead([]byte("fence")); !errors.Is(err, boom) {
		t.Fatalf("guardRead error = %v, want the fault", err)
	}
	st.fault = nil
	if _, err := f.guardRead([]byte("fence")); err != nil {
		t.Fatalf("guardRead: %v", err)
	}
	if st.gets != 2 {
		t.Fatalf("storage reads = %d, want 2: a fault must not be cached as absence", st.gets)
	}
}
