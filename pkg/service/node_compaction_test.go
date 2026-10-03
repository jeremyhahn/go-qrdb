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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func nodeCompactionAdmin(table CompactionStateStore) *AdminService {
	opts := []AdminServiceOption{}
	if table != nil {
		opts = append(opts, WithNodeCompaction(table))
	}
	return NewAdminService(nil, opts...).(*AdminService)
}

// The platform's policy is the NodeScope record of the shared store's
// compaction table, carrying no database binding.
func TestAdminService_SetNodeCompaction_WritesTheNodeScopeRecord(t *testing.T) {
	table := newMemoryCompactionTable()
	svc := nodeCompactionAdmin(table)
	ctx := context.Background()
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 100}

	if err := svc.SetNodeCompaction(ctx, policy); err != nil {
		t.Fatalf("SetNodeCompaction: %v", err)
	}
	st := table.states[compaction.NodeScope]
	if st == nil || st.Policy == nil || st.Policy.RetainRevisions != 100 {
		t.Fatalf("node scope record = %+v; want the revision policy", st)
	}
	if st.Tenant != "" || st.Database != "" || st.Incarnation != "" {
		t.Fatalf("node scope record bound to a database: %+v", st)
	}
	got, err := svc.NodeCompaction(ctx)
	if err != nil || got == nil || got.Mode != compaction.Revision || got.RetainRevisions != 100 {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
}

// Off and nil both remove the record, and reading it back reports no policy.
func TestAdminService_SetNodeCompaction_OffRemovesThePolicy(t *testing.T) {
	for name, off := range map[string]*compaction.Policy{"nil": nil, "off": {Mode: compaction.Off}} {
		t.Run(name, func(t *testing.T) {
			table := newMemoryCompactionTable()
			svc := nodeCompactionAdmin(table)
			ctx := context.Background()
			if err := svc.SetNodeCompaction(ctx, &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 5}); err != nil {
				t.Fatalf("SetNodeCompaction: %v", err)
			}
			if err := svc.SetNodeCompaction(ctx, off); err != nil {
				t.Fatalf("SetNodeCompaction(off): %v", err)
			}
			if _, ok := table.states[compaction.NodeScope]; ok {
				t.Fatal("an off policy left the node scope record in place")
			}
			if got, err := svc.NodeCompaction(ctx); err != nil || got != nil {
				t.Fatalf("NodeCompaction = %+v, %v; want none", got, err)
			}
		})
	}
}

func TestAdminService_SetNodeCompaction_RefusesAnInvalidPolicy(t *testing.T) {
	table := newMemoryCompactionTable()
	svc := nodeCompactionAdmin(table)
	err := svc.SetNodeCompaction(context.Background(), &compaction.Policy{Mode: compaction.Revision})
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("SetNodeCompaction(revision, no retention) = %v; want *compaction.InvalidPolicyError", err)
	}
	if len(table.states) != 0 {
		t.Fatalf("an invalid policy was written: %+v", table.states)
	}
}

// The node keyspace is the platform's: a caller bound to any other tenant is
// refused both verbs, whatever reached the service -- an in-process caller
// scoped to a tenant included -- and nothing is written.
func TestAdminService_NodeCompaction_RefusesATenantCaller(t *testing.T) {
	table := newMemoryCompactionTable()
	svc := nodeCompactionAdmin(table)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	var denied *tenant.CrossTenantDeniedError
	if err := svc.SetNodeCompaction(ctx, &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); !errors.As(err, &denied) {
		t.Fatalf("SetNodeCompaction as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if _, err := svc.NodeCompaction(ctx); !errors.As(err, &denied) {
		t.Fatalf("NodeCompaction as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if len(table.states) != 0 {
		t.Fatalf("a tenant caller wrote the node policy: %+v", table.states)
	}

	sys := tenant.WithTenantID(context.Background(), tenantid.System)
	if err := svc.SetNodeCompaction(sys, &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); err != nil {
		t.Fatalf("SetNodeCompaction as the system tenant: %v", err)
	}
}

func TestAdminService_NodeCompaction_UnavailableWithoutTheTable(t *testing.T) {
	svc := nodeCompactionAdmin(nil)
	var unavailable *NodeCompactionUnavailableError
	if err := svc.SetNodeCompaction(context.Background(), nil); !errors.As(err, &unavailable) {
		t.Fatalf("SetNodeCompaction = %v; want *NodeCompactionUnavailableError", err)
	}
	if _, err := svc.NodeCompaction(context.Background()); !errors.As(err, &unavailable) {
		t.Fatalf("NodeCompaction = %v; want *NodeCompactionUnavailableError", err)
	}
	if unavailable.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestAdminService_NodeCompaction_PropagatesAReadFailure(t *testing.T) {
	table := newMemoryCompactionTable()
	table.readErr = errors.New("read failed")
	if _, err := nodeCompactionAdmin(table).NodeCompaction(context.Background()); !errors.Is(err, table.readErr) {
		t.Fatalf("NodeCompaction = %v; want the read failure", err)
	}
}
