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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// leasedPutHost applies a lease-attached put and answers the revision
// read-back the way the configured state machine does: readBack is what a
// state machine WITH revision history returns, and readErr is how one
// without it refuses the query.
func leasedPutHost(result sm.Result, readBack *KeyValue, readErr error) (*mockHost, *[][]byte, *int) {
	var cmds [][]byte
	reads := 0
	record := func(cmd []byte) { cmds = append(cmds, append([]byte(nil), cmd...)) }
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			record(cmd)
			return result, nil
		},
		proposeWithModeFn: func(_ context.Context, _ uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
			record(cmd)
			return result, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			reads++
			if _, ok := q.(MVCCGetAtRevisionQuery); !ok {
				return nil, errors.New("unexpected query")
			}
			if readErr != nil {
				return nil, readErr
			}
			return readBack, nil
		},
	}, &cmds, &reads
}

// On a state machine the composer declared has no revision history -- the
// default pebble one -- the lease-attached put applies, and PutWithLease
// reports what it wrote without any read-back: the state machine could not
// serve one. This is what lease-backed registrations (dynamic membership,
// feature advertisements) need on that state machine.
func TestMVCCKVService_PutWithLeaseWithoutRevisionHistory(t *testing.T) {
	// The pebble state machine answers a lease-attached put with the
	// value's length, not a revision, and refuses every revision query.
	host, cmds, reads := leasedPutHost(sm.Result{Value: 5}, nil, &fakeUnsupportedQueryTypeError{unsupported: true})
	svc := NewMVCCKVService(host, 1, WithoutRevisionHistory())
	if svc.(RevisionHistoryReporter).KeepsRevisionHistory() {
		t.Fatal("a service declared WithoutRevisionHistory reports revision history")
	}

	for name, put := range map[string]func() (*KeyValue, error){
		"PutWithLease": func() (*KeyValue, error) {
			return svc.PutWithLease(context.Background(), "k", []byte("hello"), 7)
		},
		"PutWithLeaseAndMode": func() (*KeyValue, error) {
			return svc.PutWithLeaseAndMode(context.Background(), "k", []byte("hello"), 7, writemode.Sync)
		},
	} {
		kv, err := put()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(kv.Key) != "k" || !bytes.Equal(kv.Value, []byte("hello")) || kv.LeaseID != 7 {
			t.Fatalf("%s = %+v, want the key, value and lease it wrote", name, kv)
		}
		if kv.ModRevision != 0 || kv.CreateRevision != 0 {
			t.Fatalf("%s = %+v: a state machine with no revision history assigns no revision", name, kv)
		}
		// Its version compare reads presence, so a written key's version is
		// 1: a compare-and-set on the reported version must hold.
		present := protocol.DecodedCompare{Target: protocol.CompareTargetVersion, Result: protocol.CompareResultEqual, Version: kv.Version}
		if kv.Version != 1 || !protocol.CompareHolds(present, kv.Value) {
			t.Fatalf("%s version = %d, want 1, the version its transaction compare reads for a present key", name, kv.Version)
		}
	}
	if len(*cmds) != 2 || (*cmds)[0][0] != protocol.OpMVCCPut {
		t.Fatalf("proposed %x, want two lease-attached puts", *cmds)
	}
	if *reads != 0 {
		t.Fatalf("%d revision read-backs against a state machine declared to have no revisions", *reads)
	}
}

// A service not declared WithoutRevisionHistory never guesses from a failed
// read: a state machine refusing the revision query is the caller's error.
func TestMVCCKVService_PutWithLeaseNeverInfersMissingHistory(t *testing.T) {
	host, _, _ := leasedPutHost(sm.Result{Value: 5}, nil, &fakeUnsupportedQueryTypeError{unsupported: true})
	svc := NewMVCCKVService(host, 1)
	if !svc.(RevisionHistoryReporter).KeepsRevisionHistory() {
		t.Fatal("a service not declared WithoutRevisionHistory must report revision history")
	}
	var required *MVCCBackendRequiredError
	if _, err := svc.PutWithLease(context.Background(), "k", []byte("v"), 7); !errors.As(err, &required) {
		t.Fatalf("PutWithLease = %v, want *MVCCBackendRequiredError", err)
	}
}

// On a state machine with revision history the read-back stays: callers
// get the full revision metadata of the write, exactly as before.
func TestMVCCKVService_PutWithLeaseKeepsRevisionSemantics(t *testing.T) {
	want := &KeyValue{Key: []byte("k"), Value: []byte("v"), CreateRevision: 3, ModRevision: 9, Version: 2, LeaseID: 7}
	host, _, reads := leasedPutHost(sm.Result{Value: 9}, want, nil)
	svc := NewMVCCKVService(host, 1)

	got, err := svc.PutWithLease(context.Background(), "k", []byte("v"), 7)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if got != want || *reads != 1 {
		t.Fatalf("PutWithLease = %+v after %d reads, want the read-back record", got, *reads)
	}
	got, err = svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), 7, writemode.Sync)
	if err != nil || got != want {
		t.Fatalf("PutWithLeaseAndMode = %+v, %v; want the read-back record", got, err)
	}
}

// Any other read-back failure is still the caller's error: only the state
// machine's refusal to serve revisions at all means "no revision history".
func TestMVCCKVService_PutWithLeaseReportsOtherReadBackFailures(t *testing.T) {
	boom := errors.New("read index not ready")
	host, _, _ := leasedPutHost(sm.Result{Value: 9}, nil, boom)
	svc := NewMVCCKVService(host, 1)
	if _, err := svc.PutWithLease(context.Background(), "k", []byte("v"), 7); err == nil {
		t.Fatal("a read-back failure was reported as success")
	}
	if _, err := svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), 7, writemode.Sync); err == nil {
		t.Fatal("a read-back failure was reported as success")
	}
	host, _, _ = leasedPutHost(sm.Result{Value: 9}, nil, &fakeUnsupportedQueryTypeError{unsupported: false})
	if _, err := NewMVCCKVService(host, 1).PutWithLease(context.Background(), "k", []byte("v"), 7); err == nil {
		t.Fatal("an invalid-query refusal that is not 'no such query type' was reported as success")
	}
}
