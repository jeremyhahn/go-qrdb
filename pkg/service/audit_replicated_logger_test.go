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
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// fixedShardResolver always answers one (Host, shardID) pair, for tests that
// do not need SystemShardAuditResolver's isolation-mode logic.
type fixedShardResolver struct {
	host    Host
	shardID uint64
	err     error
}

func (r *fixedShardResolver) ResolveAuditShard(context.Context, string) (Host, uint64, error) {
	return r.host, r.shardID, r.err
}

// TestReplicatedLogger_Emit_RefusedBeforeGateActive is "the opcode is
// refused before the gate activates": a shard whose feature gate has not
// activated audit-append must never see a propose at all.
func TestReplicatedLogger_Emit_RefusedBeforeGateActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	err := logger.Emit(context.Background(), fixedAuditEvent("acme"))
	var refused *AuditFeatureNotActiveError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *AuditFeatureNotActiveError", err)
	}
	if refused.TenantID != "acme" || refused.ShardID != 1 {
		t.Fatalf("refused = %+v, want tenant acme shard 1", refused)
	}
	if len(*cmds) != 0 {
		t.Fatalf("propose was called %d times, want 0 (refused before the gate activates)", len(*cmds))
	}
}

// TestReplicatedLogger_Emit_ProposesWhenGateActive asserts a gate-active
// shard DOES get an OpAuditAppend proposal, encoding the event JSON.
func TestReplicatedLogger_Emit_ProposesWhenGateActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{featuregate.AuditAppend: true}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("propose was called %d times, want 1", len(*cmds))
	}
	cmd := (*cmds)[0]
	if cmd[0] != protocol.OpAuditAppend {
		t.Fatalf("op = %d, want OpAuditAppend (%d)", cmd[0], protocol.OpAuditAppend)
	}
	subtype, tenantID, _, err := protocol.DecodeAuditCommand(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if subtype != protocol.AuditSubtypeAppend {
		t.Fatalf("subtype = %d, want append", subtype)
	}
	if tenantID != "acme" {
		t.Fatalf("tenantID = %q, want acme", tenantID)
	}
}

// TestReplicatedLogger_Emit_SetsAZeroTimestamp asserts the PROPOSER sets a
// zero Timestamp before encoding -- never the state machine at apply time
// (see pkg/audit.Chain.Append) -- so a caller that forgot to set one still
// produces a valid, non-refused command.
func TestReplicatedLogger_Emit_SetsAZeroTimestamp(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{featuregate.AuditAppend: true}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	ev := fixedAuditEvent("acme")
	ev.Timestamp = time.Time{}
	if err := logger.Emit(context.Background(), ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	_, _, body, err := protocol.DecodeAuditCommand((*cmds)[0][1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var decoded audit.AuditEvent
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Timestamp.IsZero() {
		t.Fatal("proposed event still carries a zero Timestamp")
	}
}

// perFeatureHost is a FeatureGatedHost that answers per feature, so a test can
// hold AuditAppend on while VerifiableLog is off or on.
type perFeatureHost struct {
	*mockHost
	on map[feature.Feature]bool
}

func (h *perFeatureHost) FeatureEnabled(_ uint64, f feature.Feature) bool { return h.on[f] }

// TestReplicatedLogger_Emit_ProposesToTheAuditInstance proves the switch:
// once VerifiableLog is on, Emit proposes OpVerifiableLog append to
// tenant/system/audit carrying the marshaled event.
func TestReplicatedLogger_Emit_ProposesToTheAuditInstance(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{
		featuregate.AuditAppend:        true,
		featuregate.VerifiableLog:      true,
		featuregate.VerifiableLogAudit: true,
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	ev := fixedAuditEvent("acme")
	ev.Timestamp = time.Time{}
	if err := logger.Emit(context.Background(), ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("propose was called %d times, want 1", len(*cmds))
	}
	cmd := (*cmds)[0]
	if cmd[0] != protocol.OpVerifiableLog {
		t.Fatalf("op = %d, want OpVerifiableLog (%d)", cmd[0], protocol.OpVerifiableLog)
	}
	subtype, tenantID, body, err := protocol.DecodeVerifiableLogCommand(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if subtype != protocol.VerifiableLogSubtypeAuditAppend || tenantID != "acme" {
		t.Fatalf("subtype %d tenant %q, want audit append on acme", subtype, tenantID)
	}
	id, entries, err := protocol.DecodeVerifiableLogAuditAppend(tenantID, body)
	if err != nil {
		t.Fatalf("decode append: %v", err)
	}
	if id != vlog.AuditLogID("acme") || len(entries) != 1 {
		t.Fatalf("append to %v with %d entries, want acme/system/audit with 1", id, len(entries))
	}
	var decoded audit.AuditEvent
	if err := json.Unmarshal(entries[0], &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TenantID != "acme" || decoded.Timestamp.IsZero() {
		t.Fatalf("proposed event = %+v, want tenant acme with a timestamp", decoded)
	}
}

// TestReplicatedLogger_Emit_UngatedHostProposesToTheAuditInstance pins a
// single-binary host (no FeatureGatedHost: a standalone node's local
// backend) in a build that applies the verifiable-log opcode: every replica
// is this process, so the trail is the tenant's audit instance, the
// AuditAppend sub-type to vlog.AuditLogID(tenant).
func TestReplicatedLogger_Emit_UngatedHostProposesToTheAuditInstance(t *testing.T) {
	if !ungatedHostAppliesAuditInstance {
		t.Skip("a build without qrdb_vlog keeps OpAuditAppend (TestReplicatedLogger_Emit_AuditInstanceNeedsItsOwnGate)")
	}
	host, cmds := recordingHost(sm.Result{Value: 1})
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})
	if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("propose was called %d times, want 1", len(*cmds))
	}
	cmd := (*cmds)[0]
	if cmd[0] != protocol.OpVerifiableLog {
		t.Fatalf("op = %d, want OpVerifiableLog (%d)", cmd[0], protocol.OpVerifiableLog)
	}
	subtype, tenantID, body, err := protocol.DecodeVerifiableLogCommand(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if subtype != protocol.VerifiableLogSubtypeAuditAppend || tenantID != "acme" {
		t.Fatalf("subtype %d tenant %q, want audit append on acme", subtype, tenantID)
	}
	id, entries, err := protocol.DecodeVerifiableLogAuditAppend(tenantID, body)
	if err != nil {
		t.Fatalf("decode append: %v", err)
	}
	if id != vlog.AuditLogID("acme") || len(entries) != 1 {
		t.Fatalf("append to %v with %d entries, want acme/system/audit with 1", id, len(entries))
	}
}

// TestReplicatedLogger_Emit_AuditInstanceNeedsItsOwnGate pins that only
// featuregate.VerifiableLogAudit switches Emit to the audit instance: a
// binary that advertises VerifiableLog but predates the instance would
// refuse the append while its peers chain it, so with VerifiableLog alone,
// as with neither gate, Emit proposes OpAuditAppend. A host that gates
// nothing keeps OpAuditAppend only in a build without qrdb_vlog.
func TestReplicatedLogger_Emit_AuditInstanceNeedsItsOwnGate(t *testing.T) {
	cases := []struct {
		name string
		host func(inner *mockHost) Host
	}{
		{"verifiable-log off", func(inner *mockHost) Host {
			return &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{featuregate.AuditAppend: true}}
		}},
		{"verifiable-log on, verifiable-log-audit off", func(inner *mockHost) Host {
			return &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{
				featuregate.AuditAppend:   true,
				featuregate.VerifiableLog: true,
			}}
		}},
		{"ungated host", func(inner *mockHost) Host { return inner }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, cmds := recordingHost(sm.Result{Value: 1})
			logger := NewReplicatedLogger(&fixedShardResolver{host: tc.host(inner), shardID: 1})
			if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if len(*cmds) != 1 {
				t.Fatalf("propose was called %d times, want 1", len(*cmds))
			}
			want := byte(protocol.OpAuditAppend)
			if tc.name == "ungated host" && ungatedHostAppliesAuditInstance {
				// A single-binary host in the qrdb_vlog build writes the
				// audit instance (TestReplicatedLogger_Emit_UngatedHost...).
				want = protocol.OpVerifiableLog
			}
			if op := (*cmds)[0][0]; op != want {
				t.Fatalf("op = %d, want %d", op, want)
			}
		})
	}
}

func TestReplicatedLogger_Emit_InstanceRefusalIsTyped(t *testing.T) {
	inner, _ := recordingHost(sm.Result{Value: 0, Data: protocol.EncodeLogRefusal(protocol.LogRefusalNotFound, 0, "malformed")})
	host := &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{
		featuregate.AuditAppend:        true,
		featuregate.VerifiableLog:      true,
		featuregate.VerifiableLogAudit: true,
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	err := logger.Emit(context.Background(), fixedAuditEvent("acme"))
	var refused *AuditInstanceRefusedError
	if !errors.As(err, &refused) || refused.TenantID != "acme" || refused.Reason != "malformed" {
		t.Fatalf("err = %v, want *AuditInstanceRefusedError for acme", err)
	}
	if refused.Error() == "" {
		t.Fatal("empty error text")
	}
}

// TestReplicatedLogger_Emit_PropagatesProposeFailure asserts a propose
// failure surfaces as an error -- the fail-closed gate depends on this.
func TestReplicatedLogger_Emit_PropagatesProposeFailure(t *testing.T) {
	host := &gatedHost{
		mockHost: &mockHost{syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("raft: proposal dropped")
		}},
		enabled: map[uint64]bool{1: true},
	}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})
	if err := logger.Emit(context.Background(), fixedAuditEvent("acme")); err == nil {
		t.Fatal("expected an error from a failed propose")
	}
}

// TestReplicatedLogger_Query_RoundTrip asserts Query resolves a shard and
// reads back through audit.QueryRequest.
func TestReplicatedLogger_Query_RoundTrip(t *testing.T) {
	want := &audit.Page{Events: []audit.AuditEvent{fixedAuditEvent("acme")}, Total: -1}
	var gotQuery audit.QueryRequest
	host := &mockHost{syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
		gotQuery = query.(audit.QueryRequest) //nolint:errcheck // test fixture
		return want, nil
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	got, err := logger.Query(context.Background(), audit.Filter{TenantID: "acme", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got != want {
		t.Fatal("Query did not return the page the host answered")
	}
	if gotQuery.TenantID != "acme" || gotQuery.Filter.Limit != 10 {
		t.Fatalf("query forwarded = %+v, want TenantID=acme Limit=10", gotQuery)
	}
}

// TestReplicatedLogger_Query_UnexpectedResultType asserts a host answering
// a result of the wrong type is a typed error, not a panic.
func TestReplicatedLogger_Query_UnexpectedResultType(t *testing.T) {
	host := &mockHost{syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
		return "not a page", nil
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})
	if _, err := logger.Query(context.Background(), audit.Filter{TenantID: "acme"}); err == nil {
		t.Fatal("expected an error for an unexpected result type")
	}
}

// TestReplicatedLogger_RoutesUntenantedEventsToTheSystemTenant asserts a
// platform event (empty TenantID) resolves and encodes under the system
// tenant, per ruling 2's "the system tenant's store holds platform events."
func TestReplicatedLogger_RoutesUntenantedEventsToTheSystemTenant(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	ev := fixedAuditEvent("")
	if err := logger.Emit(context.Background(), ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	_, tenantID, _, err := protocol.DecodeAuditCommand((*cmds)[0][1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tenantID != tenantid.System {
		t.Fatalf("routed tenant = %q, want %q", tenantID, tenantid.System)
	}
}

// TestReplicatedLogger_VerifyTenantChain_RoundTrip asserts
// VerifyTenantChain resolves a shard and reads back through
// audit.VerifyRequest.
func TestReplicatedLogger_VerifyTenantChain_RoundTrip(t *testing.T) {
	want := &audit.ChainContinuityReport{Verified: true, Records: 3, Head: 3, Low: 1, Genesis: true}
	var gotQuery audit.VerifyRequest
	host := &mockHost{syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
		gotQuery = query.(audit.VerifyRequest) //nolint:errcheck // test fixture
		return want, nil
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	got, err := logger.VerifyTenantChain(context.Background(), "acme")
	if err != nil {
		t.Fatalf("VerifyTenantChain: %v", err)
	}
	if got != want {
		t.Fatal("VerifyTenantChain did not return the host's report")
	}
	if gotQuery.TenantID != "acme" {
		t.Fatalf("query forwarded TenantID = %q, want acme", gotQuery.TenantID)
	}
}

// TestReplicatedLogger_VerifyTenantChain_UntenantedUsesSystemTenant mirrors
// Emit/Query's own routing for an empty tenant id.
func TestReplicatedLogger_VerifyTenantChain_UntenantedUsesSystemTenant(t *testing.T) {
	var gotQuery audit.VerifyRequest
	host := &mockHost{syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
		gotQuery = query.(audit.VerifyRequest) //nolint:errcheck // test fixture
		return &audit.ChainContinuityReport{Verified: true, Genesis: true}, nil
	}}
	logger := NewReplicatedLogger(&fixedShardResolver{host: host, shardID: 1})

	if _, err := logger.VerifyTenantChain(context.Background(), ""); err != nil {
		t.Fatalf("VerifyTenantChain: %v", err)
	}
	if gotQuery.TenantID != tenantid.System {
		t.Fatalf("routed tenant = %q, want %q", gotQuery.TenantID, tenantid.System)
	}
}

// TestReplicatedLogger_VerifyTenantChain_PropagatesResolverError asserts a
// resolver failure (e.g. a dedicated-isolation tenant) surfaces rather than
// being swallowed.
func TestReplicatedLogger_VerifyTenantChain_PropagatesResolverError(t *testing.T) {
	resolverErr := errors.New("resolver refused")
	logger := NewReplicatedLogger(&fixedShardResolver{err: resolverErr})
	if _, err := logger.VerifyTenantChain(context.Background(), "acme"); !errors.Is(err, resolverErr) {
		t.Fatalf("err = %v, want %v", err, resolverErr)
	}
}

// TestReplicatedLogger_Close_IsANoOp asserts Close never errors: a
// ReplicatedLogger owns no resource of its own.
func TestReplicatedLogger_Close_IsANoOp(t *testing.T) {
	logger := NewReplicatedLogger(&fixedShardResolver{})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// guardedSystemShard is a system shard with guarded writes on whose
// proposals apply an OpGuarded command's compares against kv, the state
// standing in for the system KV the tenant's isolation epochs live in: a
// compare that fails replies the guard refusal and applies nothing, as
// every replica's state machine does. The first proposal waits on proceed
// after closing proposing; applied holds the inner commands that applied.
type guardedSystemShard struct {
	*chainShard
	mu        sync.Mutex
	kv        map[string][]byte
	applied   [][]byte
	proposing chan struct{}
	proceed   chan struct{}
	paused    sync.Once
}

func newGuardedSystemShard(kv map[string][]byte) *guardedSystemShard {
	on := maps.Clone(auditActive())
	on[featuregate.GuardedWrites] = true
	s := &guardedSystemShard{
		chainShard: newChainShard(0, on),
		kv:         kv,
		proposing:  make(chan struct{}),
		proceed:    make(chan struct{}),
	}
	s.host.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		s.paused.Do(func() {
			close(s.proposing)
			<-s.proceed
		})
		return s.apply(cmd)
	}
	return s
}

func (s *guardedSystemShard) apply(cmd []byte) (sm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(cmd) > 0 && cmd[0] == protocol.OpGuarded {
		compares, inner, err := protocol.DecodeGuarded(cmd[1:])
		if err != nil {
			return sm.Result{}, err
		}
		for _, c := range compares {
			if c.Target != byte(CompareValue) || c.Result != byte(CompareEqual) {
				return sm.Result{}, errors.New("unexpected compare")
			}
			if !bytes.Equal(s.kv[string(c.Key)], c.Value) {
				return sm.Result{Data: protocol.EncodeGuardRefusal(c.Key)}, nil
			}
		}
		cmd = inner
	}
	s.applied = append(s.applied, append([]byte(nil), cmd...))
	return sm.Result{Value: 1}, nil
}

func (s *guardedSystemShard) set(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
}

func (s *guardedSystemShard) appliedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.applied)
}

// TestReplicatedLogger_Emit_SystemRouteAppendInFlightAcrossTighteningIsRefused:
// an append resolved on a recorded system route whose proposal is still in
// flight when a tightening writes the tenant's isolation epoch is refused
// at apply time by its epoch guard: reported as the strict refusal once the
// tenant is strict, else as the retryable guard refusal, which a retry
// clears. An append no tightening overtakes applies.
func TestReplicatedLogger_Emit_SystemRouteAppendInFlightAcrossTighteningIsRefused(t *testing.T) {
	ctx := context.Background()
	epochKey := tenant.IsolationEpochKeys("widgets")[0]
	for name, tc := range map[string]struct {
		tighten bool
		strict  bool
	}{
		"tightened to strict":       {tighten: true, strict: true},
		"epoch changed, not strict": {tighten: true},
		"no tightening":             {},
	} {
		t.Run(name, func(t *testing.T) {
			system := newGuardedSystemShard(map[string][]byte{epochKey: []byte("epoch-1")})
			lookup := newEpochIsolationLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess,
				map[string][]byte{epochKey: []byte("epoch-1")})
			logger := NewReplicatedLogger(routedResolver(t, system.chainShard, lookup, AuditRouteSystem))

			done := make(chan error, 1)
			go func() { done <- logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}) }()
			<-system.proposing
			if tc.tighten {
				// The tightening writes the tenant's epoch, then commits.
				system.set(epochKey, []byte("epoch-2"))
				lookup.setEpoch(epochKey, []byte("epoch-2"))
			}
			if tc.strict {
				lookup.tenants["widgets"] = &tenant.TenantInfo{ID: "widgets", IsolationType: tenant.IsolationBarrier, IsolationPolicy: tenant.TenantIsolationPolicyStrict}
			}
			close(system.proceed)
			err := <-done

			switch {
			case tc.strict:
				var refused *tenant.SystemAuditRouteError
				if !errors.As(err, &refused) || refused.TenantID != "widgets" {
					t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
				}
			case tc.tighten:
				var refused *WriteGuardRefusedError
				if !errors.As(err, &refused) {
					t.Fatalf("Emit = %v, want *WriteGuardRefusedError", err)
				}
			default:
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
			}
			want := 0
			if !tc.tighten {
				want = 1
			}
			if got := system.appliedCount(); got != want {
				t.Fatalf("applied %d audit commands, want %d", got, want)
			}
			if tc.tighten && !tc.strict {
				// The retry reads the new epoch and applies.
				if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
					t.Fatalf("retried Emit: %v", err)
				}
				if got := system.appliedCount(); got != 1 {
					t.Fatalf("applied %d audit commands after the retry, want 1", got)
				}
			}
		})
	}
}
