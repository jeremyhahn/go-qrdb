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

//go:build qrdb_vlog

package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestLogService_ReadsTheAuditInstance pins that a caller holding the log
// surface reads its own tenant's audit instance (system/audit) like any named
// log: the head names the leaves appended since the switch and an inclusion
// proof of each verifies against it.
func TestLogService_ReadsTheAuditInstance(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	var events [][]byte
	for i := range 3 {
		ev := fixedAuditEvent("acme")
		ev.Action = []string{"a", "b", "c"}[i]
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, body)
	}
	if _, err := audit.ChainOn(vlog.NewChain()).AppendToInstance(store, "acme", events); err != nil {
		t.Fatalf("AppendToInstance: %v", err)
	}

	var routed []vlog.LogID
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		switch r := q.(type) {
		case vlog.HeadRequest:
			routed = append(routed, r.ID)
		case vlog.InclusionRequest:
			routed = append(routed, r.ID)
		}
		return vlog.Read(store, q.(vlog.Request))
	}), nil)
	ref := LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}

	head, err := svc.Head(acmeCtx(), &HeadRequest{LogRef: ref})
	if err != nil || head.Head.Size != 3 {
		t.Fatalf("Head = %+v, %v; want 3 leaves", head, err)
	}
	leaves, err := vlog.Get(store, vlog.AuditLogID("acme"), 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range leaves {
		resp, err := svc.ProveInclusion(acmeCtx(), &InclusionRequest{LogRef: ref, Index: leaf.Index, Size: head.Head.Size})
		if err != nil {
			t.Fatalf("ProveInclusion(%d): %v", leaf.Index, err)
		}
		if err := proof.VerifyInclusion(rfc6962.DefaultHasher, leaf.Index, head.Head.Size,
			rfc6962.DefaultHasher.HashLeaf(leaf.Body), resp.Hashes, head.Head.Root); err != nil {
			t.Fatalf("VerifyInclusion(%d): %v", leaf.Index, err)
		}
	}
	for _, id := range routed {
		if id != vlog.AuditLogID("acme") {
			t.Fatalf("read routed to %v, want the caller's own audit instance", id)
		}
	}
}

// barrierHomeGroup is a barrier tenant's home group: a state-machine-backed
// host that applies every OpVerifiableLog command with the same
// statemachine.ApplyVerifiableLog both state-machine families run, over one
// store and one vlog.Chain, and answers vlog reads from that store.
func barrierHomeGroup(t *testing.T) *perFeatureHost {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	logs := vlog.NewChain()
	inner := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			if len(cmd) == 0 || cmd[0] != protocol.OpVerifiableLog {
				t.Fatalf("home group got opcode %v, want OpVerifiableLog", cmd)
			}
			logs.BeginUpdate()
			return statemachine.ApplyVerifiableLog(logs, store, store, cmd[1:])
		},
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			req, ok := q.(vlog.Request)
			if !ok {
				t.Fatalf("home group read %T, want a vlog.Request", q)
			}
			return vlog.Read(store, req)
		},
	}
	return &perFeatureHost{mockHost: inner, on: map[feature.Feature]bool{
		featuregate.AuditAppend:        true,
		featuregate.VerifiableLog:      true,
		featuregate.VerifiableLogKeys:  true,
		featuregate.VerifiableLogAudit: true,
	}}
}

// untouchedSystemShard is the system shard a barrier tenant's audit trail
// must never reach: it counts every proposal and read.
func untouchedSystemShard() (*mockHost, *int) {
	calls := 0
	return &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			calls++
			return sm.Result{}, nil
		},
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			calls++
			return nil, nil
		},
	}, &calls
}

// TestLogService_ReadsABarrierTenantsAuditInstanceFromItsHomeGroup: the
// replicated audit logger and the log service share one resolver, so the
// audit records a barrier tenant's events append are the ones its log
// service reads back and proves, in its home group, never the system shard.
func TestLogService_ReadsABarrierTenantsAuditInstanceFromItsHomeGroup(t *testing.T) {
	home := barrierHomeGroup(t)
	system, systemCalls := untouchedSystemShard()
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vault": {ID: "vault", IsolationType: tenant.IsolationBarrier},
	}}
	resolver := NewSystemShardLogResolver(system, 1, lookup,
		&fakeLogGroups{host: home, homes: map[string]uint64{"vault": 9}})
	logger := NewReplicatedLogger(resolver)
	ctx := tenant.WithTenantID(context.Background(), "vault")
	for _, action := range []string{"a", "b"} {
		ev := fixedAuditEvent("vault")
		ev.Action = action
		if err := logger.Emit(ctx, ev); err != nil {
			t.Fatalf("Emit(%s): %v", action, err)
		}
	}

	svc, err := NewLogService(LogServiceConfig{Resolver: resolver, Databases: noDatabases(t)})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	ref := LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}
	head, err := svc.Head(ctx, &HeadRequest{LogRef: ref})
	if err != nil || head.Head.Size != 2 {
		t.Fatalf("Head = %+v, %v; want the 2 audited events", head, err)
	}
	got, err := svc.GetEntries(ctx, &GetEntriesRequest{LogRef: ref, From: 1, Limit: 1})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Index != 1 {
		t.Fatalf("GetEntries(1) = %+v, %v; want entry 1", got, err)
	}
	leaf := got.Entries[0]
	resp, err := svc.ProveInclusion(ctx, &InclusionRequest{LogRef: ref, Index: leaf.Index, Size: head.Head.Size})
	if err != nil {
		t.Fatalf("ProveInclusion: %v", err)
	}
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, leaf.Index, head.Head.Size,
		rfc6962.DefaultHasher.HashLeaf(leaf.Body), resp.Hashes, head.Head.Root); err != nil {
		t.Fatalf("VerifyInclusion: %v", err)
	}
	listed, err := svc.ListLogs(ctx, &ListLogsRequest{Database: vlog.AuditDatabase})
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if !slices.Contains(listed.Logs, vlog.AuditLogID("vault")) {
		t.Fatalf("ListLogs = %v, want it to name %v", listed.Logs, vlog.AuditLogID("vault"))
	}
	if *systemCalls != 0 {
		t.Fatalf("the system shard saw %d calls for a barrier tenant, want none", *systemCalls)
	}
}

// TestLogService_SealedBarrierTenantRefusesItsAuditTrail: a sealed tenant
// barrier refuses the audit append, the audit query and the log read alike,
// with tenant.ErrTenantSealed, and nothing is proposed anywhere.
func TestLogService_SealedBarrierTenantRefusesItsAuditTrail(t *testing.T) {
	system, systemCalls := untouchedSystemShard()
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vault": {ID: "vault", IsolationType: tenant.IsolationBarrier},
	}}
	resolver := NewSystemShardLogResolver(system, 1, lookup,
		&fakeLogGroups{sealed: map[string]bool{"vault": true}})
	logger := NewReplicatedLogger(resolver)
	ctx := tenant.WithTenantID(context.Background(), "vault")
	if err := logger.Emit(ctx, fixedAuditEvent("vault")); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Emit = %v, want tenant.ErrTenantSealed", err)
	}
	if _, err := logger.Query(ctx, audit.Filter{TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Query = %v, want tenant.ErrTenantSealed", err)
	}
	svc, err := NewLogService(LogServiceConfig{Resolver: resolver, Databases: noDatabases(t)})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	ref := LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}
	if _, err := svc.Head(ctx, &HeadRequest{LogRef: ref}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Head = %v, want tenant.ErrTenantSealed", err)
	}
	if *systemCalls != 0 {
		t.Fatalf("the system shard saw %d calls for a sealed barrier tenant, want none", *systemCalls)
	}
}
