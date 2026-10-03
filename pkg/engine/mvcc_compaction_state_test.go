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
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// applyCompactionState applies one OpCompactionState command and returns
// its result.
func applyCompactionState(t *testing.T, eng *MVCCEngine, index uint64, cmd protocol.CompactionStateCommand) sm.Result {
	t.Helper()
	encoded := mustEncode(protocol.EncodeCompactionState(cmd))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: index, Cmd: encoded}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return results[0]
}

// compactionStateOf reads scope's state through Lookup.
func compactionStateOf(t *testing.T, eng *MVCCEngine, scope string) *compaction.State {
	t.Helper()
	got, err := eng.Lookup(context.Background(), service.CompactionStateQuery{Scope: scope})
	if err != nil {
		t.Fatalf("Lookup(CompactionStateQuery): %v", err)
	}
	state, ok := got.(*compaction.State)
	if !ok {
		t.Fatalf("Lookup(CompactionStateQuery) = %T", got)
	}
	return state
}

// TestMVCCEngine_CompactionState_SetSampleClear: the command writes the
// scope's reserved record -- policy, then samples under the cap -- and the
// record is read back; clearing removes it. Scopes are independent.
func TestMVCCEngine_CompactionState_SetSampleClear(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if s := compactionStateOf(t, eng, "acme/app/"); s != nil {
		t.Fatalf("a fresh store holds state %+v", s)
	}
	r := applyCompactionState(t, eng, 1, protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"period","retainPeriod":"1h"}}`),
	})
	if r.Value != 1 || len(r.Data) != 0 {
		t.Fatalf("set result = %+v", r)
	}
	at := time.Unix(1_700_000_000, 0).UnixNano()
	if r := applyCompactionState(t, eng, 2, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSample, Scope: "acme/app/", At: at, Revision: 7}); r.Value != 1 {
		t.Fatalf("first sample result = %+v", r)
	}
	// A duplicate, and one inside the sample interval, are not kept.
	if r := applyCompactionState(t, eng, 3, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSample, Scope: "acme/app/", At: at, Revision: 7}); r.Value != 0 {
		t.Fatalf("duplicate sample result = %+v", r)
	}
	if r := applyCompactionState(t, eng, 4, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSample, Scope: "acme/app/", At: at + int64(time.Second), Revision: 8}); r.Value != 0 {
		t.Fatalf("sample inside the interval result = %+v", r)
	}

	s := compactionStateOf(t, eng, "acme/app/")
	if s == nil || s.Policy == nil || s.Policy.Mode != compaction.Period || len(s.Samples) != 1 || s.Samples[0].Revision != 7 {
		t.Fatalf("state = %+v", s)
	}
	if other := compactionStateOf(t, eng, "beta/app/"); other != nil {
		t.Fatalf("another scope reads %+v", other)
	}

	all, err := eng.Lookup(context.Background(), service.CompactionStatesQuery{})
	if err != nil {
		t.Fatalf("Lookup(CompactionStatesQuery): %v", err)
	}
	if states := all.(map[string]*compaction.State); len(states) != 1 || states["acme/app/"] == nil {
		t.Fatalf("all states = %v", states)
	}

	if r := applyCompactionState(t, eng, 5, protocol.CompactionStateCommand{Kind: protocol.CompactionStateClear, Scope: "acme/app/"}); r.Value != 1 {
		t.Fatalf("clear result = %+v", r)
	}
	if s := compactionStateOf(t, eng, "acme/app/"); s != nil {
		t.Fatalf("state after clear = %+v", s)
	}
}

// TestMVCCEngine_CompactionState_OffPolicyRemovesTheRecord: setting a policy
// that is off is the same state as never setting one -- no record.
func TestMVCCEngine_CompactionState_OffPolicyRemovesTheRecord(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyCompactionState(t, eng, 1, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSetPolicy, Scope: "", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":3}}`)})
	if s := compactionStateOf(t, eng, ""); s == nil || s.Policy.RetainRevisions != 3 {
		t.Fatalf("state = %+v", s)
	}
	applyCompactionState(t, eng, 2, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSetPolicy, Scope: "", Policy: []byte(`{"policy":{"mode":"off"}}`)})
	if s := compactionStateOf(t, eng, ""); s != nil {
		t.Fatalf("state after an off policy = %+v", s)
	}
	// A sample for a scope with no record is not kept.
	if r := applyCompactionState(t, eng, 3, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSample, Scope: "", At: 1, Revision: 1}); r.Value != 0 {
		t.Fatalf("sample with no policy = %+v", r)
	}
}

// TestMVCCEngine_CompactionState_RefusalsAreResults: an invalid policy or a
// scope that is not one database's keyspace is refused in the result -- a
// deterministic refusal every replica makes alike, never a Go error that
// would halt the group -- and writes nothing.
func TestMVCCEngine_CompactionState_RefusalsAreResults(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	for i, cmd := range []protocol.CompactionStateCommand{
		{Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"revision"}}`)},
		{Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`not json`)},
		{Kind: protocol.CompactionStateSetPolicy, Scope: "acme/", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":1}}`)},
		{Kind: protocol.CompactionStateClear, Scope: "acme/ap"},
	} {
		r := applyCompactionState(t, eng, uint64(i+1), cmd)
		msg, kind, ok := protocol.DecodeAppErrorKind(r.Data)
		if !ok || kind != protocol.AppErrorInvalid || msg == "" {
			t.Fatalf("command %+v result = %+v, want an invalid-kind refusal", cmd, r)
		}
	}
	all, err := eng.Lookup(context.Background(), service.CompactionStatesQuery{})
	if err != nil || len(all.(map[string]*compaction.State)) != 0 {
		t.Fatalf("a refused command wrote state: %v, %v", all, err)
	}
}

// TestMVCCEngine_CompactPrefix_RefusesAPrefixThatIsNotOneDatabase: the state
// machine re-checks the prefix's shape at apply, deterministically, so a
// command that reached the log without the service's check -- "acme/" is
// every database of acme -- compacts nothing on any replica.
func TestMVCCEngine_CompactPrefix_RefusesAPrefixThatIsNotOneDatabase(t *testing.T) {
	for _, prefix := range []string{"acme/", "acme/ap", "acme/app/deeper/", "\x00node"} {
		eng, stg := newMVCCEngine(t)
		applyMVCCEntries(t, eng, []sm.Entry{
			mvccPutEntry(1, []byte("acme/app/k"), []byte("v1"), 0),
			mvccPutEntry(2, []byte("acme/app/k"), []byte("v2"), 0),
		})
		cmd := mustEncode(protocol.EncodeMVCCCompactPrefix([]byte(prefix), 2))
		results := make([]sm.Result, 1)
		if err := eng.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: cmd}}, results); err != nil {
			t.Fatalf("%q: Update: %v", prefix, err)
		}
		if _, kind, ok := protocol.DecodeAppErrorKind(results[0].Data); !ok || kind != protocol.AppErrorInvalid {
			t.Fatalf("%q: compaction result = %+v, want an invalid-kind refusal", prefix, results[0])
		}
		if kv, err := eng.Store().GetAtRevision([]byte("acme/app/k"), 1); err != nil || string(kv.Value) != "v1" {
			t.Fatalf("%q: a refused compaction discarded history: %v, %v", prefix, kv, err)
		}
		_ = eng.Close(context.Background())
		_ = stg.Close()
	}
}

// TestMVCCEngine_CompactionState_IsNotAUserKey: the record lives in the
// reserved raw keyspace, so no MVCC read, range or listing of user keys sees
// it, and it consumes no revision.
func TestMVCCEngine_CompactionState_IsNotAUserKey(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	before := eng.Store().CurrentRevision()
	applyCompactionState(t, eng, 1, protocol.CompactionStateCommand{Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":3}}`)})
	if after := eng.Store().CurrentRevision(); after != before {
		t.Fatalf("the policy write consumed a revision: %d -> %d", before, after)
	}
	kvs, _, _, err := eng.Store().Range([]byte{0x00}, []byte{0xff})
	if err != nil || len(kvs) != 0 {
		t.Fatalf("a range over every user key = %v, %v", kvs, err)
	}

	// The record is at the documented raw key, spelled out here rather than
	// built with compaction.RecordKey, so a change to the key's layout -- a
	// change to what snapshots, backups and the node-local filter see --
	// fails this test instead of moving with it.
	raw, err := stg.Get([]byte("\x00qrdb.compaction/acme/app/"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("point Get of \"\\x00qrdb.compaction/acme/app/\" = %q, %v; want the record", raw, err)
	}
	st, err := compaction.DecodeState(raw)
	if err != nil || st.Policy == nil || st.Policy.RetainRevisions != 3 {
		t.Fatalf("the record at the literal key = %+v, %v; want the revision policy", st, err)
	}
}
