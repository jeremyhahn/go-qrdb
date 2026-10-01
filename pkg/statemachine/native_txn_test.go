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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// nativeMachines are the two state machine shapes the native plane serves: the
// Backend-based KVStateMachine and the legacy RegularStateMachine with its own
// map. Both must answer identically.
func nativeMachines() map[string]func() func(cmd []byte) (sm.Result, error) {
	return map[string]func() func(cmd []byte) (sm.Result, error){
		"kv": func() func(cmd []byte) (sm.Result, error) {
			k := NewKVStateMachine(NewStandardBackend())
			idx := uint64(0)
			return func(cmd []byte) (sm.Result, error) {
				idx++
				res := make([]sm.Result, 1)
				err := k.Update([]sm.Entry{{Index: idx, Cmd: cmd}}, res)
				return res[0], err
			}
		},
		"regular": func() func(cmd []byte) (sm.Result, error) {
			r := NewRegularStateMachine()
			idx := uint64(0)
			return func(cmd []byte) (sm.Result, error) {
				idx++
				res := make([]sm.Result, 1)
				err := r.Update(context.Background(), []sm.Entry{{Index: idx, Cmd: cmd}}, res)
				return res[0], err
			}
		},
	}
}

func txnCmd(t *testing.T, compares [][]byte, success, failure [][]byte) []byte {
	t.Helper()
	return mustEncode(protocol.EncodeTransaction(compares, success, failure))
}

func cmpCmd(t *testing.T, key string, target, result byte, value string, version int64) []byte {
	t.Helper()
	var v []byte
	if value != "" {
		v = []byte(value)
	}
	return mustEncode(protocol.EncodeCompare([]byte(key), target, result, v, version, 0, 0))
}

func opCmd(t *testing.T, op byte, key, value string) []byte {
	t.Helper()
	return mustEncode(protocol.EncodeOp(op, []byte(key), []byte(value), nil, 0))
}

// TestNativeTransaction_CompareSemantics is the pebble state machine's compare
// table, applied to both native shapes: value compares under every operator,
// absent-as-empty, and version's honest 0/1.
func TestNativeTransaction_CompareSemantics(t *testing.T) {
	cases := []struct {
		name string
		cmp  func(t *testing.T) []byte
		want bool
	}{
		{"value equal", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, txnResultEqual, "m", 0) }, true},
		{"value not equal", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, txnResultNotEqual, "m", 0) }, false},
		{"value greater", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, txnResultGreater, "a", 0) }, true},
		{"value less", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, txnResultLess, "z", 0) }, true},
		{"value less fails", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, txnResultLess, "a", 0) }, false},
		{"absent equals empty", func(t *testing.T) []byte { return cmpCmd(t, "nope", txnTargetValue, txnResultEqual, "", 0) }, true},
		{"absent is not a value", func(t *testing.T) []byte { return cmpCmd(t, "nope", txnTargetValue, txnResultEqual, "m", 0) }, false},
		{"absent not-equal never holds", func(t *testing.T) []byte { return cmpCmd(t, "nope", txnTargetValue, txnResultNotEqual, "m", 0) }, false},
		{"version 0 on absent", func(t *testing.T) []byte { return cmpCmd(t, "nope", txnTargetVersion, txnResultEqual, "", 0) }, true},
		{"version 0 on present", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetVersion, txnResultEqual, "", 0) }, false},
		{"version 1 on present", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetVersion, txnResultEqual, "", 1) }, true},
		{"version > 0 on present", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetVersion, txnResultGreater, "", 0) }, true},
		{"version < 1 on absent", func(t *testing.T) []byte { return cmpCmd(t, "nope", txnTargetVersion, txnResultLess, "", 1) }, true},
		{"version != 1 on present", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetVersion, txnResultNotEqual, "", 1) }, false},
		{"unknown operator fails closed", func(t *testing.T) []byte { return cmpCmd(t, "k", txnTargetValue, 9, "m", 0) }, false},
		{"unknown target fails closed", func(t *testing.T) []byte { return cmpCmd(t, "k", 9, txnResultEqual, "m", 0) }, false},
	}
	for name, newMachine := range nativeMachines() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				apply := newMachine()
				if _, err := apply(mustEncode(kv.EncodePut([]byte("k"), []byte("m")))); err != nil {
					t.Fatal(err)
				}
				res, err := apply(txnCmd(t, [][]byte{tc.cmp(t)}, [][]byte{opCmd(t, protocol.OpMVCCPut, "branch", "success")},
					[][]byte{opCmd(t, protocol.OpMVCCPut, "branch", "failure")}))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if got := len(res.Data) == 1 && res.Data[0] == 1; got != tc.want {
					t.Fatalf("succeeded = %v; want %v (Data=%v)", got, tc.want, res.Data)
				}
			})
		}
	}
}

// TestNativeTransaction_RefusalsWriteNothing covers the three refusals:
// a revision compare (pebble's application error), an operation these state
// machines cannot execute, and a lease-bearing write. Each leaves the
// keyspace untouched and the apply loop running.
func TestNativeTransaction_RefusalsWriteNothing(t *testing.T) {
	for name, newMachine := range nativeMachines() {
		t.Run(name, func(t *testing.T) {
			apply := newMachine()
			write := opCmd(t, protocol.OpMVCCPut, "written", "x")

			res, err := apply(txnCmd(t, [][]byte{mustEncode(protocol.EncodeCompare([]byte("k"), txnTargetModRevision, txnResultEqual, nil, 0, 0, 0))}, [][]byte{write}, nil))
			if err != nil {
				t.Fatalf("revision compare returned a Go error (would halt apply): %v", err)
			}
			if msg, ok := protocol.DecodeAppError(res.Data); !ok || msg != RevisionCompareUnsupportedMessage {
				t.Fatalf("revision compare result = %q; want pebble's refusal", res.Data)
			}

			res, err = apply(txnCmd(t, nil, [][]byte{write, opCmd(t, protocol.OpMVCCRange, "r", "")}, nil))
			if err != nil {
				t.Fatalf("unsupported op returned a Go error (would halt apply): %v", err)
			}
			if _, ok := protocol.DecodeAppError(res.Data); !ok {
				t.Fatalf("an unsupported op in the branch was not refused: %v", res)
			}

			leased := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("written"), []byte("x"), nil, 7))
			res, err = apply(txnCmd(t, nil, [][]byte{write, leased}, nil))
			if err != nil {
				t.Fatalf("leased write returned a Go error (would halt apply): %v", err)
			}
			if msg, ok := protocol.DecodeAppError(res.Data); !ok || msg == "" {
				t.Fatalf("a leased write was not refused: %v", res)
			}

			res, err = apply(mustEncode(protocol.EncodeBatch([][]byte{write, leased})))
			if err != nil {
				t.Fatalf("leased batch write returned a Go error: %v", err)
			}
			if _, ok := protocol.DecodeAppError(res.Data); !ok {
				t.Fatalf("a leased batch write was not refused: %v", res)
			}

			// Nothing any refusal carried was written, and the machine still
			// applies.
			res, err = apply(txnCmd(t, [][]byte{cmpCmd(t, "written", txnTargetVersion, txnResultEqual, "", 0)}, nil, nil))
			if err != nil || len(res.Data) != 1 || res.Data[0] != 1 {
				t.Fatalf("a refused transaction left a write behind: res=%v err=%v", res, err)
			}
		})
	}
}

// TestNativeBatch_AcceptsBothOpcodeSpellings pins the service batch fix: the
// MVCC opcodes pkg/service encodes, and the KV opcodes, are both a plain put or
// delete of the state machine's own keyspace.
func TestNativeBatch_AcceptsBothOpcodeSpellings(t *testing.T) {
	for name, newMachine := range nativeMachines() {
		t.Run(name, func(t *testing.T) {
			apply := newMachine()
			res, err := apply(mustEncode(protocol.EncodeBatch([][]byte{
				opCmd(t, protocol.OpMVCCPut, "a", "1"),
				opCmd(t, kv.OpPut, "b", "2"),
				opCmd(t, protocol.OpMVCCPut, "gone", "x"),
				opCmd(t, protocol.OpMVCCDelete, "gone", ""),
				opCmd(t, kv.OpPut, "gone2", "x"),
				opCmd(t, kv.OpDelete, "gone2", ""),
			})))
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if msg, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("service batch refused: %s", msg)
			}
			for key, wantPresent := range map[string]bool{"a": true, "b": true, "gone": false, "gone2": false} {
				res, err := apply(txnCmd(t, [][]byte{cmpCmd(t, key, txnTargetVersion, txnResultEqual, "", 1)}, nil, nil))
				if err != nil {
					t.Fatal(err)
				}
				if present := res.Data[0] == 1; present != wantPresent {
					t.Errorf("%s present = %v; want %v", key, present, wantPresent)
				}
			}
		})
	}
}

// TestResolveTypedWithStorage_Refusals pins the resolution-time checks.
func TestResolveTypedWithStorage_Refusals(t *testing.T) {
	_, err := ResolveTypedWithStorage("disk", "", StorageConfig{Kind: "objstore"})
	var unknown *UnknownSMStorageError
	if !errors.As(err, &unknown) {
		t.Fatalf("objstore: %v; want UnknownSMStorageError", err)
	}
	_, err = ResolveTypedWithStorage("memory", "", StorageConfig{Kind: "file"})
	var notApplicable *SMStorageNotApplicableError
	if !errors.As(err, &notApplicable) {
		t.Fatalf("memory over file: %v; want SMStorageNotApplicableError", err)
	}
	if _, err := ResolveTypedWithStorage("memory", "", StorageConfig{Kind: "pebble"}); err != nil {
		t.Fatalf("pebble is the default and applies everywhere: %v", err)
	}
	_, err = ResolveTypedWithStorage("nope", "", StorageConfig{})
	var unknownType *UnknownSMTypeError
	if !errors.As(err, &unknownType) {
		t.Fatalf("unknown type: %v; want UnknownSMTypeError", err)
	}
	for _, e := range []error{&UnknownSMStorageError{Kind: "x"}, &SMStorageNotApplicableError{SMType: "a", Kind: "b"}} {
		if e.Error() == "" {
			t.Errorf("%T has an empty message", e)
		}
	}
}

// TestNarrowCapabilities pins the capability report: a flag the descriptor
// does not declare is turned off, a declared one is left as the profile had
// it (narrowing never widens), locking follows its three dependencies, and a
// flag no state machine decides is untouched.
func TestNarrowCapabilities(t *testing.T) {
	all := capabilities.Capabilities{
		KV: true, Batch: true, Index: true, Transaction: true, MVCC: true, Watch: true,
		Lease: true, Lock: true, TimeSeries: true, Vector: true, Cluster: true,
	}
	got, err := NarrowCapabilities(all, "memory", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.KV || !got.Batch || !got.Transaction {
		t.Errorf("memory lost a capability it serves: %+v", got)
	}
	if got.MVCC || got.Watch || got.Lease || got.Index || got.TimeSeries || got.Lock {
		t.Errorf("memory reports a capability it does not serve: %+v", got)
	}
	if !got.Vector || !got.Cluster {
		t.Errorf("a flag no state machine decides was narrowed: %+v", got)
	}

	full := Descriptor{Capabilities: []Capability{CapabilityKV, CapabilityTransaction, CapabilityLease, CapabilityWatch}}
	if n := full.Narrow(capabilities.Capabilities{KV: true, Lock: true, Transaction: true, Lease: true, Watch: true}); !n.Lock {
		t.Errorf("lock must survive when transactions, leases and watches do: %+v", n)
	}
	if n := full.Narrow(capabilities.Capabilities{}); n.KV || n.Transaction {
		t.Errorf("narrowing widened a flag the profile had off: %+v", n)
	}

	if _, err := NarrowCapabilities(all, "nope", ""); err == nil {
		t.Error("an unknown sm_type must not narrow silently")
	}
}

// TestInvalidKeysAreRefusedNotFatal is go-qrdb#136's review finding B1: an
// empty key reached storage, which refused it with an error Update returned —
// and a Go error from Update halts Apply on every replica. It is now refused
// as an application result on every write path, before anything is evaluated,
// as is a client write to the disk state machine's reserved applied-index
// key. The disk case runs over REAL file storage, where the empty key used to
// fail.
func TestInvalidKeysAreRefusedNotFatal(t *testing.T) {
	machines := nativeMachines()
	machines["disk-over-file"] = func() func(cmd []byte) (sm.Result, error) {
		st, err := filestore.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		a := newDiskAdapter(st)
		idx := uint64(0)
		return func(cmd []byte) (sm.Result, error) {
			idx++
			res := make([]sm.Result, 1)
			err := a.Update(context.Background(), []sm.Entry{{Index: idx, Cmd: cmd}}, res)
			return res[0], err
		}
	}

	good := opCmd(t, protocol.OpMVCCPut, "fine", "v")
	empty := opCmd(t, protocol.OpMVCCPut, "", "v")
	reserved := opCmd(t, protocol.OpMVCCPut, diskMetaKeyLastApplied, "v")
	emptyCmp := cmpCmd(t, "", txnTargetVersion, txnResultEqual, "", 0)
	commands := map[string]func(t *testing.T) []byte{
		"txn empty compare key":         func(t *testing.T) []byte { return txnCmd(t, [][]byte{emptyCmp}, [][]byte{good}, nil) },
		"txn empty success-op key":      func(t *testing.T) []byte { return txnCmd(t, nil, [][]byte{good, empty}, nil) },
		"txn empty key, untaken branch": func(t *testing.T) []byte { return txnCmd(t, nil, [][]byte{good}, [][]byte{empty}) },
		"txn reserved key":              func(t *testing.T) []byte { return txnCmd(t, nil, [][]byte{reserved}, nil) },
		"batch empty key":               func(t *testing.T) []byte { return mustEncode(protocol.EncodeBatch([][]byte{good, empty})) },
		"batch reserved key":            func(t *testing.T) []byte { return mustEncode(protocol.EncodeBatch([][]byte{good, reserved})) },
		"put reserved key": func(t *testing.T) []byte {
			return mustEncode(kv.EncodePut([]byte(diskMetaKeyLastApplied), []byte("v")))
		},
		"put-if-absent reserved key": func(t *testing.T) []byte {
			return mustEncode(kv.EncodePutIfAbsent([]byte(diskMetaKeyLastApplied), []byte("v")))
		},
		"compare-and-swap empty key": func(t *testing.T) []byte {
			return mustEncode(protocol.EncodeCompareAndSwap(nil, []byte("a"), []byte("b")))
		},
		"compare-and-delete empty key": func(t *testing.T) []byte {
			return mustEncode(protocol.EncodeCompareAndDelete(nil, []byte("a")))
		},
	}

	for name, newMachine := range machines {
		for cmdName, cmd := range commands {
			t.Run(name+"/"+cmdName, func(t *testing.T) {
				apply := newMachine()
				res, err := apply(cmd(t))
				if err != nil {
					t.Fatalf("Update returned a Go error, which halts Apply on every replica: %v", err)
				}
				msg, kind, ok := protocol.DecodeAppErrorKind(res.Data)
				if !ok || kind != protocol.AppErrorInvalid {
					t.Fatalf("result = %v (kind %d); want a refusal on the request's merits "+
						"(protocol.AppErrorInvalid), not a capability refusal", res, kind)
				}
				if !strings.Contains(msg, "invalid key") {
					t.Errorf("refusal %q does not say the key was invalid", msg)
				}
				// Nothing the refused command carried was written, and the
				// machine still applies.
				res, err = apply(txnCmd(t, [][]byte{cmpCmd(t, "fine", txnTargetVersion, txnResultEqual, "", 0)}, [][]byte{good}, nil))
				if err != nil || len(res.Data) != 1 || res.Data[0] != 1 {
					t.Fatalf("after the refusal: res=%v err=%v; want a clean apply over an untouched keyspace", res, err)
				}
			})
		}
	}
}
