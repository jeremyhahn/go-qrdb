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

package tenant

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
)

// fixedBarrierNodes is a BarrierNodeSource answering every tenant with the
// same nodes, or err.
type fixedBarrierNodes struct {
	nodes []BarrierNode
	err   error
	asked []string
}

func (f *fixedBarrierNodes) BarrierNodes(_ context.Context, tenantID string) ([]BarrierNode, error) {
	f.asked = append(f.asked, tenantID)
	return f.nodes, f.err
}

var threeBarrierNodes = []BarrierNode{
	{Node: "n1:1", Endpoints: map[string]string{"rest": "n1:2"}},
	{Node: "n2:1", Endpoints: map[string]string{"rest": "n2:2"}},
	{Node: "n3:1"},
}

// TestTenantBarrierStatus_ListsServingNodes pins that both shapes of status
// -- a registered barrier and one not registered on this node yet -- carry
// the nodes a client fans a barrier init out to, read for the tenant asked.
func TestTenantBarrierStatus_ListsServingNodes(t *testing.T) {
	svc, _ := newClusterTenant(t, clustersealtest.New(true), "acme")
	source := &fixedBarrierNodes{nodes: threeBarrierNodes}
	svc.SetBarrierNodeSource(source)
	st, err := svc.TenantBarrierStatus(ownerCtx("acme"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Nodes, threeBarrierNodes) {
		t.Fatalf("registered barrier Nodes = %+v, want %+v", st.Nodes, threeBarrierNodes)
	}

	bare, _ := newTestService(t)
	if _, err := bare.CreateTenant(context.Background(), "bare", "Bare", ""); err != nil {
		t.Fatal(err)
	}
	bare.SetBarrierNodeSource(source)
	st, err = bare.TenantBarrierStatus(ownerCtx("bare"), "bare")
	if err != nil {
		t.Fatal(err)
	}
	if st.Initialized || !reflect.DeepEqual(st.Nodes, threeBarrierNodes) {
		t.Fatalf("unregistered barrier status = %+v, want uninitialized with the serving nodes", st)
	}
	if want := []string{"acme", "bare"}; !reflect.DeepEqual(source.asked, want) {
		t.Fatalf("source asked for %v, want %v", source.asked, want)
	}
}

// TestTenantBarrierStatus_ServingNodesUnreadableOrUnwired pins that the node
// list is not the barrier's own state: a source that fails leaves it out and
// the status still answers, and a service with no source lists none.
func TestTenantBarrierStatus_ServingNodesUnreadableOrUnwired(t *testing.T) {
	svc, _ := newClusterTenant(t, clustersealtest.New(true), "acme")
	st, err := svc.TenantBarrierStatus(ownerCtx("acme"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if st.Nodes != nil {
		t.Fatalf("unwired Nodes = %+v, want none", st.Nodes)
	}

	svc.SetBarrierNodeSource(&fixedBarrierNodes{nodes: threeBarrierNodes, err: errors.New("placements unreadable")})
	st, err = svc.TenantBarrierStatus(ownerCtx("acme"), "acme")
	if err != nil {
		t.Fatalf("status failed on an unreadable node list: %v", err)
	}
	if st.Nodes != nil {
		t.Fatalf("Nodes = %+v from a failing source, want none", st.Nodes)
	}
}
