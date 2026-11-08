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

package service

import (
	"bytes"
	"context"
	stderrors "errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	memorystorage "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// rangeDeleter is the method both range-delete paths carry.
type rangeDeleter interface {
	DeleteRange(ctx context.Context, start, end []byte) error
}

// TestKVService_DeleteRange_ProposesTheRangeCommand checks the Raft path
// proposes one OpDeleteRange carrying both bounds, through a host with an
// active gate and through one with no gate to ask.
func TestKVService_DeleteRange_ProposesTheRangeCommand(t *testing.T) {
	for name, tc := range map[string]struct{ start, end []byte }{
		"bounded":   {[]byte("a/"), []byte("a0")},
		"unbounded": {[]byte("a/"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			inner, cmds := recordingHost(sm.Result{})
			for hostName, host := range map[string]Host{
				"gated":   &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}},
				"ungated": inner,
			} {
				*cmds = nil
				if err := NewKVService(host, 1).(rangeDeleter).DeleteRange(context.Background(), tc.start, tc.end); err != nil {
					t.Fatalf("%s: DeleteRange: %v", hostName, err)
				}
				if len(*cmds) != 1 {
					t.Fatalf("%s: proposed %d commands, want one", hostName, len(*cmds))
				}
				op, _, err := protocol.DecodeOp((*cmds)[0])
				if err != nil {
					t.Fatalf("%s: the proposed command does not decode: %v", hostName, err)
				}
				if op.Type != protocol.OpDeleteRange || !bytes.Equal(op.Key, tc.start) || !bytes.Equal(op.RangeEnd, tc.end) {
					t.Fatalf("%s: proposed %+v, want OpDeleteRange over [%q, %q)", hostName, op, tc.start, tc.end)
				}
			}
		})
	}
}

// TestKVService_DeleteRange_RefusedUntilTheGateIsActive checks a group whose
// replicas cannot all apply OpDeleteRange is never sent one: an older binary
// would fail the entry and stall the group.
func TestKVService_DeleteRange_RefusedUntilTheGateIsActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}

	err := NewKVService(host, 1).(rangeDeleter).DeleteRange(context.Background(), []byte("a"), nil)
	var notActive *feature.NotActiveError
	if !stderrors.As(err, &notActive) || notActive.Feature != featuregate.RangeDelete || notActive.ShardID != 1 {
		t.Fatalf("DeleteRange on an inactive gate = %v, want a range-delete NotActiveError for shard 1", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("an inactive gate still proposed %d commands", len(*cmds))
	}
	if len(host.asked) != 1 || host.asked[0] != featuregate.RangeDelete {
		t.Fatalf("the gate was asked %v, want [range-delete]", host.asked)
	}
}

func TestKVService_DeleteRange_Failures(t *testing.T) {
	ctx := context.Background()

	t.Run("an unframeable bound never reaches consensus", func(t *testing.T) {
		inner, cmds := recordingHost(sm.Result{})
		long := make([]byte, protocol.MaxClientKeyLength+1)
		err := NewKVService(inner, 1).(rangeDeleter).DeleteRange(ctx, []byte("a"), long)
		var encodeErr *EncodeRequestError
		if !stderrors.As(err, &encodeErr) || len(*cmds) != 0 {
			t.Fatalf("DeleteRange with an oversize end = %v after %d proposals, want an EncodeRequestError and none", err, len(*cmds))
		}
	})
	t.Run("a NotLeader proposal names the leader", func(t *testing.T) {
		host := &mockHost{syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
		}}
		var notLeader *NotLeaderError
		if err := NewKVService(host, 1).(rangeDeleter).DeleteRange(ctx, []byte("a"), nil); !stderrors.As(err, &notLeader) {
			t.Fatalf("DeleteRange = %T (%v), want *NotLeaderError", err, err)
		}
	})
	t.Run("an encoded application error is surfaced", func(t *testing.T) {
		inner, _ := recordingHost(sm.Result{Data: protocol.EncodeCapabilityError("subsystem not configured")})
		var unsupported *CapabilityUnsupportedError
		if err := NewKVService(inner, 1).(rangeDeleter).DeleteRange(ctx, []byte("a"), nil); !stderrors.As(err, &unsupported) {
			t.Fatalf("DeleteRange = %T (%v), want *CapabilityUnsupportedError", err, err)
		}
	})
}

// TestBackendKVService_DeleteRange checks the dedicated-backend path removes
// [start, end), with an empty end running to the end of the keyspace.
func TestBackendKVService_DeleteRange(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		start, end []byte
		want       []string
	}{
		"bounded keeps its exclusive end":  {[]byte("m"), []byte("z"), []string{"a", "z"}},
		"nil end reaches the keyspace end": {[]byte("m"), nil, []string{"a"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := memorystorage.NewStorage()
			t.Cleanup(func() { _ = store.Close() })
			svc := NewBackendKVService(database.NewStandaloneBackend(store))
			for _, k := range []string{"a", "m", "ma", "z"} {
				if err := svc.Put(ctx, k, []byte(k)); err != nil {
					t.Fatalf("Put(%q): %v", k, err)
				}
			}
			if err := svc.DeleteRange(ctx, tc.start, tc.end); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			got, err := svc.List(ctx, "")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("keys = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("keys = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestBackendKVService_DeleteRange_Refusals(t *testing.T) {
	ctx := context.Background()

	t.Run("a dropping database deletes nothing", func(t *testing.T) {
		store := memorystorage.NewStorage()
		t.Cleanup(func() { _ = store.Close() })
		dropping := &database.DroppingError{Name: "orders"}
		svc := NewNamedBackendKVService("orders", database.NewStandaloneBackend(store), func() error { return dropping })
		if err := store.Set([]byte("a"), []byte("1")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := svc.DeleteRange(ctx, []byte("a"), nil); !stderrors.Is(err, dropping) {
			t.Fatalf("DeleteRange while dropping = %v, want the DroppingError", err)
		}
		if v, err := store.Get([]byte("a")); err != nil || string(v) != "1" {
			t.Fatalf("a refused range delete removed the key: %q, %v", v, err)
		}
	})
	t.Run("a sealed barrier names the database", func(t *testing.T) {
		svc := NewNamedBackendKVService("orders", &errBackend{deleteErr: &database.SealedError{}})
		var sealed *BarrierSealedError
		if err := svc.DeleteRange(ctx, []byte("a"), nil); !stderrors.As(err, &sealed) || sealed.Database != "orders" {
			t.Fatalf("DeleteRange on a sealed backend = %v, want a BarrierSealedError naming orders", err)
		}
	})
	t.Run("another backend failure is returned unchanged", func(t *testing.T) {
		fault := &backendDeleteError{}
		if err := NewBackendKVService(&errBackend{deleteErr: fault}).DeleteRange(ctx, []byte("a"), nil); err != fault {
			t.Fatalf("DeleteRange = %v, want the backend's own error", err)
		}
	})
}

// deleteServices are both Raft-backed implementations of CompareAndDelete,
// driven through one table for the reason swapServices gives.
func deleteServices(host Host) map[string]interface {
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
} {
	return map[string]interface {
		CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
	}{
		"KVService":     NewKVService(host, 1).(*KVService),
		"MVCCKVService": NewMVCCKVService(host, 1).(*MVCCKVService),
	}
}

func TestServices_CompareAndDelete(t *testing.T) {
	ctx := context.Background()
	for name := range deleteServices(&mockHost{}) {
		t.Run(name, func(t *testing.T) {
			inner, cmds := recordingHost(sm.Result{Value: protocol.ResultDeleted})
			deleted, err := deleteServices(inner)[name].CompareAndDelete(ctx, "k", []byte("old"))
			if err != nil || !deleted {
				t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
			}
			if len(*cmds) != 1 || (*cmds)[0][0] != protocol.OpCompareAndDelete {
				t.Fatalf("proposed %x, want one OpCompareAndDelete", *cmds)
			}
			key, expected, err := protocol.DecodeCompareAndDelete((*cmds)[0][1:])
			if err != nil || string(key) != "k" || string(expected) != "old" {
				t.Fatalf("the proposed command decodes as %q, %q, %v", key, expected, err)
			}

			refused, _ := recordingHost(sm.Result{Value: protocol.ResultNotDeleted})
			if deleted, err := deleteServices(refused)[name].CompareAndDelete(ctx, "k", []byte("stale")); err != nil || deleted {
				t.Fatalf("a refused delete = (%v, %v), want (false, nil)", deleted, err)
			}

			appErr, _ := recordingHost(sm.Result{Value: protocol.ResultDeleted, Data: protocol.EncodeCapabilityError("subsystem not configured")})
			var unsupported *CapabilityUnsupportedError
			if deleted, err := deleteServices(appErr)[name].CompareAndDelete(ctx, "k", []byte("old")); !stderrors.As(err, &unsupported) || deleted {
				t.Fatalf("an encoded application error = (%v, %v), want a CapabilityUnsupportedError", deleted, err)
			}

			notLeader := &mockHost{syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
				return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
			}}
			var nle *NotLeaderError
			if deleted, err := deleteServices(notLeader)[name].CompareAndDelete(ctx, "k", []byte("old")); !stderrors.As(err, &nle) || deleted {
				t.Fatalf("a NotLeader proposal = (%v, %v), want a *NotLeaderError", deleted, err)
			}

			unframed, unframedCmds := recordingHost(sm.Result{Value: protocol.ResultDeleted})
			var encodeErr *EncodeRequestError
			if deleted, err := deleteServices(unframed)[name].CompareAndDelete(ctx, string(make([]byte, 65536)), nil); !stderrors.As(err, &encodeErr) || deleted || len(*unframedCmds) != 0 {
				t.Fatalf("an unframeable key = (%v, %v) after %d proposals, want an EncodeRequestError and none", deleted, err, len(*unframedCmds))
			}
		})
	}
}

func TestMVCCKVService_PutIfAbsent(t *testing.T) {
	ctx := context.Background()

	inner, cmds := recordingHost(sm.Result{Value: kv.ResultStored})
	svc := NewMVCCKVService(inner, 1).(*MVCCKVService)
	stored, err := svc.PutIfAbsent(ctx, "k", []byte("v"))
	if err != nil || !stored {
		t.Fatalf("PutIfAbsent = (%v, %v), want (true, nil)", stored, err)
	}
	// The conditional opcode, not a plain MVCC put: a put would overwrite a
	// key another caller created first.
	if len(*cmds) != 1 || (*cmds)[0][0] != protocol.OpMVCCPutIfAbsent {
		t.Fatalf("proposed %x, want one OpMVCCPutIfAbsent", *cmds)
	}
	key, value, leaseID, err := protocol.DecodeMVCCPut((*cmds)[0][1:])
	if err != nil || string(key) != "k" || string(value) != "v" || leaseID != 0 {
		t.Fatalf("the proposed command decodes as %q, %q, %d, %v", key, value, leaseID, err)
	}

	declined, _ := recordingHost(sm.Result{Value: kv.ResultNotStored})
	if stored, err := NewMVCCKVService(declined, 1).(*MVCCKVService).PutIfAbsent(ctx, "k", []byte("v")); err != nil || stored {
		t.Fatalf("a declined write = (%v, %v), want (false, nil)", stored, err)
	}

	appErr, _ := recordingHost(sm.Result{Value: kv.ResultStored, Data: protocol.EncodeCapabilityError("subsystem not configured")})
	var unsupported *CapabilityUnsupportedError
	if stored, err := NewMVCCKVService(appErr, 1).(*MVCCKVService).PutIfAbsent(ctx, "k", []byte("v")); !stderrors.As(err, &unsupported) || stored {
		t.Fatalf("an encoded application error = (%v, %v), want a CapabilityUnsupportedError", stored, err)
	}

	boom := stderrors.New("consensus unavailable")
	failing := &mockHost{syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) { return sm.Result{}, boom }}
	if stored, err := NewMVCCKVService(failing, 1).(*MVCCKVService).PutIfAbsent(ctx, "k", []byte("v")); !stderrors.Is(err, boom) || stored {
		t.Fatalf("a failed proposal = (%v, %v), want the failure wrapped", stored, err)
	}

	unframed, unframedCmds := recordingHost(sm.Result{Value: kv.ResultStored})
	var encodeErr *EncodeRequestError
	if stored, err := NewMVCCKVService(unframed, 1).(*MVCCKVService).PutIfAbsent(ctx, string(make([]byte, 65536)), nil); !stderrors.As(err, &encodeErr) || stored || len(*unframedCmds) != 0 {
		t.Fatalf("an unframeable key = (%v, %v) after %d proposals, want an EncodeRequestError and none", stored, err, len(*unframedCmds))
	}
}
