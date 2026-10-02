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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// compactionStateCommands decodes every recorded proposal as an
// OpCompactionState command.
func compactionStateCommands(t *testing.T, cmds [][]byte) []protocol.CompactionStateCommand {
	t.Helper()
	var out []protocol.CompactionStateCommand
	for _, cmd := range cmds {
		if len(cmd) == 0 || cmd[0] != protocol.OpCompactionState {
			t.Fatalf("proposed %v, want an OpCompactionState command", cmd)
		}
		decoded, err := protocol.DecodeCompactionState(cmd[1:])
		if err != nil {
			t.Fatalf("DecodeCompactionState: %v", err)
		}
		out = append(out, decoded)
	}
	return out
}

// TestCompactionState_WritesAreTheReplicatedCommand: setting a policy,
// clearing it and recording a sample each propose one OpCompactionState
// command through the shard's own group -- the policy as a binding with its
// database's identity -- and nothing node-local.
func TestCompactionState_WritesAreTheReplicatedCommand(t *testing.T) {
	host, cmds := recordingHost(sm.Result{Value: 1})
	svc := NewMVCCKVService(host, 1).(*MVCCKVService)
	ctx := context.Background()

	binding := compaction.Binding{Policy: &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"}, Tenant: "acme", Database: "app", Incarnation: "i1"}
	if err := svc.SetCompactionPolicy(ctx, "acme/app/", binding); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	at := time.Unix(1_700_000_000, 0)
	if err := svc.RecordCompactionSample(ctx, "acme/app/", at, 42); err != nil {
		t.Fatalf("RecordCompactionSample: %v", err)
	}
	if err := svc.ClearCompactionState(ctx, "acme/app/"); err != nil {
		t.Fatalf("ClearCompactionState: %v", err)
	}

	got := compactionStateCommands(t, *cmds)
	if len(got) != 3 {
		t.Fatalf("proposed %d commands, want 3", len(got))
	}
	var sent compaction.Binding
	if err := json.Unmarshal(got[0].Policy, &sent); err != nil || got[0].Kind != protocol.CompactionStateSetPolicy ||
		sent.Incarnation != "i1" || sent.Tenant != "acme" || sent.Database != "app" || sent.Policy.RetainPeriod != "1h" {
		t.Fatalf("set command = %+v (binding %+v, %v)", got[0], sent, err)
	}
	if got[1].Kind != protocol.CompactionStateSample || got[1].At != at.UnixNano() || got[1].Revision != 42 {
		t.Fatalf("sample command = %+v", got[1])
	}
	if got[2].Kind != protocol.CompactionStateClear || got[2].Scope != "acme/app/" {
		t.Fatalf("clear command = %+v", got[2])
	}
}

// TestCompactionState_RefusedBeforeTheGateActivates: a group that may hold a
// replica unable to apply OpCompactionState is never sent it -- not a
// policy, not a sample, not a clear.
func TestCompactionState_RefusedBeforeTheGateActivates(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}
	svc := NewMVCCKVService(host, 1).(*MVCCKVService)
	ctx := context.Background()

	for name, write := range map[string]func() error{
		"set": func() error {
			return svc.SetCompactionPolicy(ctx, "acme/app/", compaction.Binding{Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}})
		},
		"sample": func() error { return svc.RecordCompactionSample(ctx, "acme/app/", time.Now(), 1) },
		"clear":  func() error { return svc.ClearCompactionState(ctx, "acme/app/") },
	} {
		var gate *FeatureNotEnabledError
		if err := write(); !errors.As(err, &gate) || gate.Feature != featuregate.PrefixCompaction {
			t.Fatalf("%s before the gate opened = %v, want *FeatureNotEnabledError for prefix-compaction", name, err)
		}
	}
	if len(*cmds) != 0 {
		t.Fatalf("proposed before the gate opened: %v", *cmds)
	}
}

// TestCompactionState_RefusesAnUnsafeScopeOrPolicy: a scope that is not one
// database's keyspace, and a policy that does not validate, are refused
// before anything is proposed.
func TestCompactionState_RefusesAnUnsafeScopeOrPolicy(t *testing.T) {
	host, cmds := recordingHost(sm.Result{Value: 1})
	svc := NewMVCCKVService(host, 1).(*MVCCKVService)
	ctx := context.Background()

	var scopeErr *CompactPrefixInvalidError
	if err := svc.ClearCompactionState(ctx, "acme/"); !errors.As(err, &scopeErr) {
		t.Fatalf("clear of acme/ = %v", err)
	}
	var policyErr *compaction.InvalidPolicyError
	if err := svc.SetCompactionPolicy(ctx, "acme/app/", compaction.Binding{Policy: &compaction.Policy{Mode: compaction.Revision}}); !errors.As(err, &policyErr) {
		t.Fatalf("an invalid policy = %v", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a refused write proposed %v", *cmds)
	}
}

// TestCompactionState_Reads: the record is read through the shard's
// linearizable read, typed.
func TestCompactionState_Reads(t *testing.T) {
	want := &compaction.State{Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}}
	host := &mockHost{syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
		switch q.(type) {
		case CompactionStateQuery:
			return want, nil
		case CompactionStatesQuery:
			return map[string]*compaction.State{"acme/app/": want}, nil
		}
		return nil, errors.New("unexpected query")
	}}
	svc := NewMVCCKVService(host, 1).(*MVCCKVService)
	if got, err := svc.CompactionState(context.Background(), "acme/app/"); err != nil || got != want {
		t.Fatalf("CompactionState = %+v, %v", got, err)
	}
	if got, err := svc.CompactionStates(context.Background()); err != nil || got["acme/app/"] != want {
		t.Fatalf("CompactionStates = %+v, %v", got, err)
	}

	wrong := &mockHost{syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) { return 7, nil }}
	svc = NewMVCCKVService(wrong, 1).(*MVCCKVService)
	if _, err := svc.CompactionState(context.Background(), "acme/app/"); err == nil {
		t.Fatal("a read answered with the wrong type succeeded")
	}
	if _, err := svc.CompactionStates(context.Background()); err == nil {
		t.Fatal("a read answered with the wrong type succeeded")
	}
}

// A store whose state machine keeps no MVCC history refuses the compaction
// table's query as an unknown query type. It holds no record -- a record is
// written only by a command such a store refuses -- so its table reads as
// empty, and dropping a database on it, or a scheduler pass over it, has
// nothing to clear or run rather than failing.
func TestCompactionState_ANonMVCCStoreHasAnEmptyTable(t *testing.T) {
	host := &mockHost{syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
		return nil, &fakeUnsupportedQueryTypeError{unsupported: true}
	}}
	svc := NewMVCCKVService(host, 1).(*MVCCKVService)
	if got, err := svc.CompactionState(context.Background(), "acme/app/"); err != nil || got != nil {
		t.Fatalf("CompactionState = %+v, %v; want no record", got, err)
	}
	if got, err := svc.CompactionStates(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("CompactionStates = %+v, %v; want an empty table", got, err)
	}

	// Any other read failure still fails.
	failing := &mockHost{syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
		return nil, errors.New("read failed")
	}}
	svc = NewMVCCKVService(failing, 1).(*MVCCKVService)
	if _, err := svc.CompactionStates(context.Background()); err == nil {
		t.Fatal("a failed read reported an empty table")
	}
	if _, err := svc.CompactionState(context.Background(), "acme/app/"); err == nil {
		t.Fatal("a failed read reported no record")
	}
}

// leaderHost is a mockHost that reports shard leadership.
type leaderHost struct {
	*mockHost
	leader bool
	err    error
}

func (h *leaderHost) GetShardInfo(shardID uint64) (*quicraft.ShardInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &quicraft.ShardInfo{ShardID: shardID, IsLeader: h.leader}, nil
}

// TestMVCCKVService_LeadsShard: leadership comes from the host; a host that
// cannot report it leads nothing, so leader-only work never runs from every
// node at once.
func TestMVCCKVService_LeadsShard(t *testing.T) {
	cases := map[string]struct {
		host Host
		want bool
	}{
		"leader":        {&leaderHost{mockHost: &mockHost{}, leader: true}, true},
		"follower":      {&leaderHost{mockHost: &mockHost{}, leader: false}, false},
		"unknown":       {&leaderHost{mockHost: &mockHost{}, err: errors.New("no shard")}, false},
		"cannot report": {&mockHost{}, false},
	}
	for name, c := range cases {
		if got := NewMVCCKVService(c.host, 1).(*MVCCKVService).LeadsShard(); got != c.want {
			t.Fatalf("%s: LeadsShard = %v, want %v", name, got, c.want)
		}
	}
}

// TestCompactionScheduleGuard: a database's record carries its drop fence for
// its incarnation; the node keyspace and a default database carry none.
func TestCompactionScheduleGuard(t *testing.T) {
	guard, ok, err := CompactionScheduleGuard(&compaction.State{Tenant: "acme", Database: "app", Incarnation: "i1"})
	want, werr := DropFenceGuard("acme", "app", "i1")
	if err != nil || werr != nil || !ok || !reflect.DeepEqual(guard, want) {
		t.Fatalf("guard = %+v, %v, %v; want the drop fence for acme/app incarnation i1", guard, ok, err)
	}
	for _, st := range []*compaction.State{{}, {Tenant: "acme", Database: tenant.DefaultDatabase}} {
		if _, ok, err := CompactionScheduleGuard(st); ok || err != nil {
			t.Fatalf("CompactionScheduleGuard(%+v) = %v, %v; want no guard", st, ok, err)
		}
	}
}

func TestCompactionPolicyUnavailableError(t *testing.T) {
	if (&CompactionPolicyUnavailableError{Database: "app"}).Error() == "" {
		t.Fatal("empty message")
	}
}
