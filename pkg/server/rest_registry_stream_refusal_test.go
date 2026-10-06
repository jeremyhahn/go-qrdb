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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestRESTRegistry_StreamRefusalNamesEverySealThatKeepsAStreamShut walks the
// reasons a stream on (tenant, database) may not open, re-asked after the
// stream subscribes so a seal landing in between still shuts it: the
// tenant's own barrier sealed on this node, the tenant's cluster seal
// record, this node unable to confirm the seal state, and the node barrier
// sealed. Each gets its typed refusal; an unsealed tenant on a confirmed node
// opens.
func TestRESTRegistry_StreamRefusalNamesEverySealThatKeepsAStreamShut(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "racme", "Acme", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(racme): %v", err)
	}
	cs := newTestClusterSeal()
	r := &restServiceRegistry{barrierReg: reg, clusterSeal: cs, systemTenant: tenantid.System}

	if err := r.StreamRefusal("racme", "app"); err != nil {
		t.Fatalf("StreamRefusal on an unsealed tenant = %v, want nil", err)
	}

	// The tenant's barrier sealed on this node.
	if err := reg.SealTenant("racme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	var tenantSealed *service.BarrierSealedError
	if err := r.StreamRefusal("racme", "app"); !errors.As(err, &tenantSealed) || tenantSealed.TenantID != "racme" {
		t.Fatalf("StreamRefusal on a locally sealed tenant = %v, want *BarrierSealedError for racme", err)
	}

	// A tenant whose cluster seal record is sealed, though no barrier for it is
	// registered on this node.
	if _, err := cs.Seal(ctx, clusterseal.TenantBarrier("rother"), clusterseal.Principal{Name: "admin"}); err != nil {
		t.Fatalf("cluster Seal(rother): %v", err)
	}
	var recordSealed *service.BarrierSealedError
	if err := r.StreamRefusal("rother", "app"); !errors.As(err, &recordSealed) || recordSealed.TenantID != "rother" {
		t.Fatalf("StreamRefusal on a record-sealed tenant = %v, want *BarrierSealedError for rother", err)
	}

	// This node cannot confirm the seal state: every stream is refused, the
	// system tenant's included.
	cs.setRefusing(true)
	if err := r.StreamRefusal(tenantid.System, "app"); !errors.As(err, new(*clusterseal.SealStateUnconfirmedError)) {
		t.Fatalf("StreamRefusal while unconfirmed = %v, want *SealStateUnconfirmedError", err)
	}
	cs.setRefusing(false)

	// The node barrier itself sealed.
	if err := reg.SystemBarrier().Seal(); err != nil {
		t.Fatalf("system Seal: %v", err)
	}
	var nodeSealed *clusterseal.SealedError
	if err := r.StreamRefusal(tenantid.System, "app"); !errors.As(err, &nodeSealed) || nodeSealed.Barrier != clusterseal.NodeBarrier() {
		t.Fatalf("StreamRefusal with the node barrier sealed = %v, want *SealedError for the node barrier", err)
	}
}
