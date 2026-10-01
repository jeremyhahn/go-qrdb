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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// recordingProbeWiring records the probe server.New wires into the tenant
// service. It embeds a nil TenantServicer: server.New calls nothing on it but
// SetTenantDataProbe.
type recordingProbeWiring struct {
	tenant.TenantServicer
	probe tenant.TenantDataProbe
	calls int
}

func (w *recordingProbeWiring) SetTenantDataProbe(p tenant.TenantDataProbe) {
	w.probe = p
	w.calls++
}

var _ tenant.IsolationChangeWiring = (*recordingProbeWiring)(nil)

// server.New wires the isolation-change data probe over the node's service
// registry, so every transport -- the embedded one included -- reaches the
// same "tenant must be empty" check.
func TestNew_WiresTheTenantDataProbe(t *testing.T) {
	w := &recordingProbeWiring{}
	New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithTenantService(w), WithDatabaseServiceRegistry(NewDatabaseServiceRegistry("default")))
	if w.calls != 1 || w.probe == nil {
		t.Fatalf("probe wired %d times (probe %v), want once", w.calls, w.probe)
	}
}

// With no service registry there is nothing to probe with; the probe stays
// unwired and an isolation change fails closed in the tenant service.
func TestNew_NoRegistryLeavesTheProbeUnwired(t *testing.T) {
	w := &recordingProbeWiring{}
	New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithTenantService(w))
	if w.calls != 0 {
		t.Fatalf("probe wired %d times with no registry, want 0", w.calls)
	}
}
