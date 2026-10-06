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

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// errTombstoneWrite is the storage fault the revoke must not swallow.
var errTombstoneWrite = errors.New("tombstone write refused")

// setRefusingBatch is a storage batch whose Set fails, so the tombstone a
// revoke writes for each attached key cannot be staged.
type setRefusingBatch struct{ storage.Batch }

func (setRefusingBatch) Set(_, _ []byte) error { return errTombstoneWrite }

// A revoke whose attached key cannot be deleted is an apply FAULT, as on the
// Pebble state machine. Logging it and carrying on reported success while the
// lessor had already dropped the lease: the key stayed in the store with
// nothing left to expire it, and a replica whose write succeeded deleted it,
// so the two diverged.
func TestMVCCEngine_LeaseRevokeReturnsADeleteFault(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatal(err)
	}
	grant := applyMVCCEntries(t, eng, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	id := int64(grant[0].Value)
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(2, []byte("held"), []byte("v"), id)})

	b := stg.Batch()
	defer func() { _ = b.Close() }()
	revoke := sm.Entry{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseRevoke(id))}
	_, _, err = eng.UpdateEntries(setRefusingBatch{Batch: b}, []sm.Entry{revoke}, make([]sm.Result, 1))
	if !errors.Is(err, errTombstoneWrite) {
		t.Fatalf("UpdateEntries = %v, want the tombstone write fault", err)
	}
}

// TestMVCCEngine_LeaseRevokeOfABoundKeyStillDeletesIt is the happy-path
// control for the fault test above.
func TestMVCCEngine_LeaseRevokeOfABoundKeyStillDeletesIt(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatal(err)
	}
	grant := applyMVCCEntries(t, eng, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	id := int64(grant[0].Value)
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(2, []byte("held"), []byte("v"), id)})
	applyMVCCEntries(t, eng, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseRevoke(id))}})
	got, err := eng.Lookup(context.Background(), []byte("held"))
	if b, ok := got.([]byte); err == nil && ok && b != nil {
		t.Fatalf("the revoked lease's key survived: %q", b)
	}
}
