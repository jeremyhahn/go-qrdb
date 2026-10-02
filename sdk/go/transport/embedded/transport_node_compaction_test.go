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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionAdmin holds the node-keyspace policy the way the admin
// service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	mockAdminService
	policy *compaction.Policy
	err    error
}

func (a *nodeCompactionAdmin) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return a.policy, a.err
}

func (a *nodeCompactionAdmin) SetNodeCompaction(_ context.Context, p *compaction.Policy) error {
	if a.err != nil {
		return a.err
	}
	if p != nil {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	a.policy = p.Normalized()
	return nil
}

func TestEmbedded_NodeCompaction_SetReadAndClear(t *testing.T) {
	admin := &nodeCompactionAdmin{}
	tr, err := New(newMockKVService(), admin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if got, err := tr.NodeCompaction(ctx); err != nil || got != nil {
		t.Fatalf("NodeCompaction before any policy = %+v, %v; want none", got, err)
	}
	set, err := tr.SetNodeCompaction(ctx, &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 6})
	if err != nil || set == nil || set.RetainRevisions != 6 {
		t.Fatalf("SetNodeCompaction = %+v, %v; want the revision policy", set, err)
	}
	if got, err := tr.NodeCompaction(ctx); err != nil || got == nil || got.Mode != "revision" {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
	if off, err := tr.SetNodeCompaction(ctx, nil); err != nil || off != nil || admin.policy != nil {
		t.Fatalf("SetNodeCompaction(nil) = %+v, %v (held %+v); want off", off, err, admin.policy)
	}
}

// An in-process caller scoped to a tenant is refused by the real admin
// service, exactly as a remote tenant caller is refused by the RBAC entry
// check, and the refusal keeps its type through the transport.
func TestEmbedded_NodeCompaction_RefusesATenantContext(t *testing.T) {
	tr, err := New(newMockKVService(), service.NewAdminService(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	var denied *tenant.CrossTenantDeniedError
	if _, err := tr.SetNodeCompaction(ctx, &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1}); !errors.As(err, &denied) {
		t.Fatalf("SetNodeCompaction as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if _, err := tr.NodeCompaction(ctx); !errors.As(err, &denied) {
		t.Fatalf("NodeCompaction as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
}

func TestEmbedded_NodeCompaction_Failures(t *testing.T) {
	ctx := context.Background()
	tr, err := New(newMockKVService(), &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a node without the table succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a node without the table succeeded")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.NodeCompaction(ctx); !errors.As(err, &closed) {
		t.Fatalf("NodeCompaction on a closed transport = %v", err)
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("SetNodeCompaction on a closed transport = %v", err)
	}
}
