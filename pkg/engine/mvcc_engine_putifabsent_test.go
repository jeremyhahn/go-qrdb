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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// baseKVPutIfAbsentEntry builds a base KV conditional-put entry.
func baseKVPutIfAbsentEntry(idx uint64, key, value string) sm.Entry {
	cmd, _ := kv.EncodePutIfAbsent([]byte(key), []byte(value))
	return sm.Entry{Index: idx, Cmd: cmd}
}

// TestMVCCEngine_BaseKVPutIfAbsent_StoresWhenAbsent verifies the conditional
// write takes effect on a key that does not exist, and reports it.
func TestMVCCEngine_BaseKVPutIfAbsent_StoresWhenAbsent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "k", "v")})
	if results[0].Value != kv.ResultStored {
		t.Fatalf("result = %d, want ResultStored (%d)", results[0].Value, kv.ResultStored)
	}

	got, err := eng.Store().Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "v" {
		t.Errorf("value = %q, want %q", got.Value, "v")
	}
}

// TestMVCCEngine_BaseKVPutIfAbsent_DeclinesWhenPresent verifies a losing
// conditional write is an answer, not a failure: no error, no overwrite.
func TestMVCCEngine_BaseKVPutIfAbsent_DeclinesWhenPresent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "k", "first")})
	results := applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(2, "k", "second")})

	if results[0].Value != kv.ResultNotStored {
		t.Fatalf("result = %d, want ResultNotStored (%d)", results[0].Value, kv.ResultNotStored)
	}
	got, err := eng.Store().Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "first" {
		t.Errorf("value = %q, want %q (the declined write must not overwrite)", got.Value, "first")
	}
}

// TestMVCCEngine_BaseKVPutIfAbsent_RejectsShortPayload verifies a malformed
// payload is a decode error rather than a silent no-op.
func TestMVCCEngine_BaseKVPutIfAbsent_RejectsShortPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Op byte plus a single byte of what should be a two-byte key length.
	entries := []sm.Entry{{Index: 1, Cmd: []byte{kv.OpPutIfAbsent, 0x01}}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err == nil {
		t.Fatal("expected a decode error for a truncated conditional put")
	}
}

// TestMVCCEngine_MVCCPutIfAbsent_StoresWhenAbsent verifies the MVCC-flavoured
// conditional put stores, allocates a revision, and attaches its lease.
func TestMVCCEngine_MVCCPutIfAbsent_StoresWhenAbsent(t *testing.T) {
	lr := lease.NewLessor()
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lr))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	leaseID, err := lr.Grant(60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	cmd := protocol.EncodeMVCCPutIfAbsent([]byte("mk"), []byte("mv"), leaseID.ID)
	results := applyMVCCEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != kv.ResultStored {
		t.Fatalf("result = %d, want ResultStored (%d)", results[0].Value, kv.ResultStored)
	}

	got, err := eng.Store().Get([]byte("mk"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "mv" {
		t.Errorf("value = %q, want %q", got.Value, "mv")
	}
	if got.ModRevision == 0 {
		t.Error("a stored conditional put must have allocated a revision")
	}
	// Revoke reports the keys attached to the lease, which is how the stored
	// key's attachment is observed.
	attached, err := lr.Revoke(leaseID.ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(attached) != 1 || string(attached[0]) != "mk" {
		t.Errorf("attached keys = %q, want the stored key attached to the lease", attached)
	}
}

// TestMVCCEngine_MVCCPutIfAbsent_DeclinesWhenPresent verifies the MVCC
// conditional put declines without error or overwrite.
func TestMVCCEngine_MVCCPutIfAbsent_DeclinesWhenPresent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("mk"), []byte("first"), 0)})

	cmd := protocol.EncodeMVCCPutIfAbsent([]byte("mk"), []byte("second"), 0)
	results := applyMVCCEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})
	if results[0].Value != kv.ResultNotStored {
		t.Fatalf("result = %d, want ResultNotStored (%d)", results[0].Value, kv.ResultNotStored)
	}

	got, err := eng.Store().Get([]byte("mk"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "first" {
		t.Errorf("value = %q, want %q", got.Value, "first")
	}
}

// TestMVCCEngine_MVCCPutIfAbsent_RejectsShortPayload verifies a malformed
// payload is reported.
func TestMVCCEngine_MVCCPutIfAbsent_RejectsShortPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	entries := []sm.Entry{{Index: 1, Cmd: []byte{protocol.OpMVCCPutIfAbsent, 0x01}}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err == nil {
		t.Fatal("expected a decode error for a truncated MVCC conditional put")
	}
}

// TestMVCCEngine_PutIfAbsent_ReinsertAfterDelete verifies a key deleted (and
// therefore tombstoned) is absent again for a later conditional put -- the
// MVCC-specific case a plain existence check could get wrong.
func TestMVCCEngine_PutIfAbsent_ReinsertAfterDelete(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "k", "first")})
	applyMVCCEntries(t, eng, []sm.Entry{baseKVDeleteEntry(2, "k")})

	results := applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(3, "k", "again")})
	if results[0].Value != kv.ResultStored {
		t.Fatalf("result = %d, want ResultStored (%d) after the key was deleted",
			results[0].Value, kv.ResultStored)
	}
	got, err := eng.Store().Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "again" {
		t.Errorf("value = %q, want %q", got.Value, "again")
	}
}
