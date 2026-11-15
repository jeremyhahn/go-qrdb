// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"
)

// markerSeeingProbe lists dbs for every tenant and records whether the
// tightening's marker was visible to a create while the probe ran.
type markerSeeingProbe struct {
	svc        IsolationTighteningReader
	dbs        []string
	sawMarker  map[string]bool
	probedWith []string
}

func (p *markerSeeingProbe) NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error) {
	seen, err := p.svc.IsolationTightening(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	p.sawMarker[tenantID] = seen
	p.probedWith = append(p.probedWith, tenantID)
	return p.dbs, nil
}

func newRaftTenantForTightening(t *testing.T, id string) *TenantService {
	t.Helper()
	svc, _ := newPlatformService(t, &bookDouble{}, nil)
	if _, err := svc.CreateTenant(context.Background(), id, id, IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(%s): %v", id, err)
	}
	return svc
}

func TestTightenToStrict_RefusedWhileDatabasesAreOnTheNodeKeyedWAL(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)

	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" || len(nk.Databases) != 1 || nk.Databases[0] != "orders" {
		t.Fatalf("UpdateTenant(strict) = %v, want *NodeKeyLogsError naming orders", err)
	}
	if !probe.sawMarker["acme"] {
		t.Fatal("a create racing the tightening could not see its marker while the databases were listed")
	}
	if info, _ := svc.GetTenant(ctx, "acme"); info == nil || info.IsolationPolicy == TenantIsolationPolicyStrict {
		t.Fatalf("refused tightening committed the policy: %+v", info)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the refusal = (%v, %v), want the marker removed", on, err)
	}
}

func TestTightenToStrict_CommitsWhenNoDatabaseIsOnTheNodeKeyedWAL(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)

	if _, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}); err != nil {
		t.Fatalf("UpdateTenant(strict): %v", err)
	}
	if info, _ := svc.GetTenant(ctx, "acme"); info == nil || info.IsolationPolicy != TenantIsolationPolicyStrict {
		t.Fatalf("tenant after the tightening = %+v, want strict", info)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the commit = (%v, %v), want the marker removed", on, err)
	}
}

func TestTightenToStrict_NoProbeFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	if !errors.Is(err, ErrNodeKeyLogsUnverifiable) {
		t.Fatalf("UpdateTenant(strict) with no probe = %v, want ErrNodeKeyLogsUnverifiable", err)
	}
}

func TestTightenFloorToStrict_RefusedWhileARaftTenantHasNodeKeyedLogs(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)

	err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict)
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" {
		t.Fatalf("SetClusterIsolationPolicy(strict) = %v, want *NodeKeyLogsError for acme", err)
	}
	if !probe.sawMarker["acme"] {
		t.Fatal("a create racing the floor's tightening could not see its marker")
	}
	if got, _ := svc.ClusterIsolationPolicy(ctx); got == ClusterIsolationPolicyStrict {
		t.Fatal("refused floor tightening committed strict")
	}

	probe.dbs = nil
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy(strict) once the logs are gone: %v", err)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the floor commit = (%v, %v), want the marker removed", on, err)
	}
}
