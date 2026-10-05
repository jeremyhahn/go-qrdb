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
	"encoding/binary"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// This file is the regression coverage for go-qrdb#592 F1: a 2-byte
// key-length field's "N + keyLen" arithmetic, computed while keyLen was
// still the field's own uint16, wrapped for a key within a few bytes of
// 65535 -- panicking Apply on every replica for 65,534-65,535-byte keys,
// and silently corrupting the decoded value for 65,526-65,533-byte keys on
// MVCC put. kv_statemachine.go, regular.go, concurrent.go, fast.go,
// putifabsent.go, pkg/engine/mvcc_engine.go and pkg/protocol/protocol.go
// (DecodeMVCCPut/DecodeMVCCDelete) all shared the exact shape; this test
// exercises it through every state machine this package registers, plus
// the one legacy type (ConcurrentStateMachine, concurrent.go) the registry
// no longer reaches under any sm_type name.

// lookuper is Lookup(ctx, query) (interface{}, error): the shape shared by
// every adapter ResolveTyped/ResolveTypedWithStorage returns and by
// RegularStateMachine, ConcurrentStateMachine and FastStateMachine
// directly.
type lookuper interface {
	Lookup(ctx context.Context, query interface{}) (interface{}, error)
}

// rawKeyOp builds an EncodeOp-shaped single op WITHOUT EncodeOp's own
// MaxClientKeyLength check, so a length the client-side encoder would
// refuse before ever proposing can still be driven through Apply directly
// -- the only way to confirm validateClientKey's OWN length branch refuses
// it identically, deterministically, and without halting Apply, on a
// binary that received one anyway (a non-conforming or pre-cap client, or
// any caller that reaches a state machine some other way than the
// encoders).
func rawKeyOp(op byte, key, val []byte) []byte {
	b := []byte{op}
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(key)))
	b = append(b, l[:]...)
	b = append(b, key...)
	binary.LittleEndian.PutUint32(l[:], uint32(len(val)))
	b = append(b, l[:]...)
	b = append(b, val...)
	binary.LittleEndian.PutUint32(l[:], 0) // rangeEnd length
	b = append(b, l[:]...)
	b = append(b, make([]byte, 8)...) // leaseID
	return b
}

func applyOne(t *testing.T, u updater, cmd []byte) sm.Result {
	t.Helper()
	res := make([]sm.Result, 1)
	if err := u.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, res); err != nil {
		t.Fatalf("Update returned a Go error, which halts Apply on every replica: %v", err)
	}
	return res[0]
}

// buildKeyLengthSM lists every state machine kind F1 named, fresh per call
// so a boundary length used in one sub-test never collides with another's
// key in shared state.
func buildKeyLengthSM(t *testing.T, kind string) interface{} {
	t.Helper()
	switch kind {
	case "memory":
		fn, err := ResolveTyped("memory", "")
		if err != nil {
			t.Fatal(err)
		}
		return fn(1, 1)
	case "highperf":
		fn, err := ResolveTyped("highperf", "")
		if err != nil {
			t.Fatal(err)
		}
		return fn(1, 1)
	case "disk-over-pebble":
		fn, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: t.TempDir(), Kind: "pebble"})
		if err != nil {
			t.Fatal(err)
		}
		return fn(1, 1)
	case "disk-over-file":
		fn, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: t.TempDir(), Kind: "file"})
		if err != nil {
			t.Fatal(err)
		}
		return fn(1, 1)
	case "fast-over-file":
		fn, err := ResolveTypedWithStorage("fast", "", StorageConfig{Root: t.TempDir(), Kind: "file"})
		if err != nil {
			t.Fatal(err)
		}
		return fn(1, 1)
	case "regular":
		return NewRegularStateMachine()
	case "concurrent-legacy":
		// concurrent.go's ConcurrentStateMachine -- unreachable through the
		// registry under any sm_type string (the "concurrent" name resolves
		// to KVStateMachine instead), but it carries its OWN copy of the
		// defective decode and must be exercised directly.
		return NewConcurrentStateMachine(1, 1, newMemStorage())
	default:
		t.Fatalf("unknown kind %q", kind)
		return nil
	}
}

func openAndRegisterClose(t *testing.T, s interface{}) {
	t.Helper()
	ctx := context.Background()
	if o, ok := s.(opener); ok {
		if _, err := o.Open(ctx, t.TempDir(), nil); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	if c, ok := s.(closer); ok {
		t.Cleanup(func() {
			if err := c.Close(ctx); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

// TestKeyLengthBoundary_EveryStateMachine_Put drives kv.OpPut through every
// state machine kind at every length in [65525, 65535] -- the exact window
// the uint16 "2 + keyLen" and "2 + keyLen + 8" arithmetic wrapped in -- and
// confirms the key applies without halting Apply or panicking, and that the
// stored VALUE is exactly what was written (the silent-corruption half of
// F1, not only the panic half).
func TestKeyLengthBoundary_EveryStateMachine_Put(t *testing.T) {
	kinds := []string{"memory", "highperf", "disk-over-pebble", "disk-over-file", "fast-over-file", "regular", "concurrent-legacy"}
	lens := []int{65525, 65526, 65527, 65530, 65533, 65534, 65535}
	for _, kind := range kinds {
		for _, n := range lens {
			t.Run(kind+"/"+itoaBoundary(n), func(t *testing.T) {
				s := buildKeyLengthSM(t, kind)
				openAndRegisterClose(t, s)
				u, ok := s.(updater)
				if !ok {
					t.Fatalf("%T is not an updater", s)
				}
				key := make([]byte, n)
				for i := range key {
					key[i] = 'k'
				}
				cmd, err := kv.EncodePut(key, []byte("VAL"))
				if err != nil {
					t.Fatalf("n=%d: kv.EncodePut: %v", n, err)
				}

				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("n=%d: Apply panicked: %v", n, p)
						}
					}()
					res := applyOne(t, u, cmd)
					if msg, refused := protocol.DecodeAppError(res.Data); refused {
						t.Fatalf("n=%d: a %d-byte key was refused at Apply: %s", n, n, msg)
					}
				}()

				lk, ok := s.(lookuper)
				if !ok {
					return
				}
				got, err := lk.Lookup(context.Background(), key)
				if err != nil {
					t.Fatalf("n=%d: Lookup: %v", n, err)
				}
				gb, _ := got.([]byte)
				if string(gb) != "VAL" {
					t.Errorf("n=%d: FINDING stored value is %q, want %q", n, gb, "VAL")
				}
			})
		}
	}
}

// TestKeyLengthBoundary_EveryStateMachine_Delete is the Delete-side twin:
// the identical arithmetic shape appears in every handleDelete this package
// defines.
func TestKeyLengthBoundary_EveryStateMachine_Delete(t *testing.T) {
	kinds := []string{"memory", "highperf", "disk-over-pebble", "disk-over-file", "fast-over-file", "regular", "concurrent-legacy"}
	lens := []int{65525, 65526, 65533, 65534, 65535}
	for _, kind := range kinds {
		for _, n := range lens {
			t.Run(kind+"/"+itoaBoundary(n), func(t *testing.T) {
				s := buildKeyLengthSM(t, kind)
				openAndRegisterClose(t, s)
				u, ok := s.(updater)
				if !ok {
					t.Fatalf("%T is not an updater", s)
				}
				key := make([]byte, n)
				for i := range key {
					key[i] = 'k'
				}
				cmd, err := kv.EncodeDelete(key)
				if err != nil {
					t.Fatalf("n=%d: kv.EncodeDelete: %v", n, err)
				}

				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("n=%d: Apply panicked: %v", n, p)
					}
				}()
				res := applyOne(t, u, cmd)
				if msg, refused := protocol.DecodeAppError(res.Data); refused {
					t.Fatalf("n=%d: a %d-byte key's delete was refused at Apply: %s", n, n, msg)
				}
			})
		}
	}
}

// TestKeyLengthBoundary_OverCapKeyAppliesDeterministically is the F5
// reversal (go-qrdb#592): Apply must be a pure, deterministic function of
// the committed log, with nothing else -- not a feature gate, not a config
// flag, not a length policy -- in the computation. validateClientKey
// enforces NO length cap at all; the cap lives only at proposal time
// (protocol.CheckKeyLen inside EncodeOp/EncodeCompare, and every other
// encoder's own structural frame width). rawKeyOp bypasses every encoder's
// own cap to build the entry a non-conforming client, an old encoder, or
// simply a committed historical log entry could carry: once it is in the
// log, every replica and every sm_type must apply it identically, never
// refuse it for being "too long."
func TestKeyLengthBoundary_OverCapKeyAppliesDeterministically(t *testing.T) {
	kinds := []string{"memory", "highperf", "disk-over-pebble", "disk-over-file", "fast-over-file", "regular"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			s := buildKeyLengthSM(t, kind)
			openAndRegisterClose(t, s)
			u, ok := s.(updater)
			if !ok {
				t.Fatalf("%T is not an updater", s)
			}
			key := make([]byte, protocol.MaxClientKeyLength+1)
			for i := range key {
				key[i] = 'k'
			}
			cmd, err := protocol.EncodeBatch([][]byte{rawKeyOp(protocol.OpMVCCPut, key, []byte("v"))})
			if err != nil {
				t.Fatal(err)
			}
			res := applyOne(t, u, cmd)
			if msg, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("a committed over-cap key was refused at Apply: %q -- "+
					"Apply must be a deterministic function of the log, not a length policy", msg)
			}

			lk, ok := s.(lookuper)
			if !ok {
				return
			}
			got, err := lk.Lookup(context.Background(), key)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if gb, _ := got.([]byte); string(gb) != "v" {
				t.Fatalf("stored value = %q, want %q", gb, "v")
			}

			// The shard must still be healthy afterward.
			healthCmd, err := kv.EncodePut([]byte("health"), []byte("ok"))
			if err != nil {
				t.Fatal(err)
			}
			res = applyOne(t, u, healthCmd)
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("shard unhealthy after the over-cap key")
			}
		})
	}
}

// TestKeyLengthBoundary_OverCapKeySurvivesReplay is the "including during
// replay" half: a committed over-cap key must come back identically after
// the state machine is closed and reopened over its own persisted storage
// -- the disk engine's own recovery path, which is exactly where an
// Apply-time length check would have diverged two replicas running
// different binary versions during a rolling upgrade (the defect the
// dropped feature gate existed to paper over; removing the Apply-time
// check removes the divergence at its root instead).
func TestKeyLengthBoundary_OverCapKeySurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, protocol.MaxClientKeyLength+1)
	for i := range key {
		key[i] = 'k'
	}
	cmd, err := protocol.EncodeBatch([][]byte{rawKeyOp(protocol.OpMVCCPut, key, []byte("v"))})
	if err != nil {
		t.Fatal(err)
	}

	st, err := filestore.New(dir)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	a := newDiskAdapter(st)
	if res := applyOne(t, a, cmd); func() bool { _, refused := protocol.DecodeAppError(res.Data); return refused }() {
		t.Fatal("over-cap key refused before replay")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen over the SAME directory: the disk engine's own recovery path,
	// not a fresh Update call -- the data must already be there.
	st2, err := filestore.New(dir)
	if err != nil {
		t.Fatalf("filestore.New (reopen): %v", err)
	}
	a2 := newDiskAdapter(st2)
	defer func() { _ = a2.Close(context.Background()) }()
	if _, err := a2.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open (replay): %v", err)
	}
	got, err := a2.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup after replay: %v", err)
	}
	if gb, _ := got.([]byte); string(gb) != "v" {
		t.Fatalf("stored value after replay = %q, want %q", gb, "v")
	}
}

func itoaBoundary(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
