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

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// H-1 — fail closed when a tenant's isolation backend is not wired.
//
// A volume- or raft-isolated tenant must NOT silently fall back to the shared
// system store when its dedicated backend (tenantHosts / raftBackend) is nil:
// that downgrades the isolation guarantee and strands the tenant's data.
// resolve must instead return *rest.IsolationBackendUnavailableError.
// ---------------------------------------------------------------------------

// isolationTenantServicer returns a TenantInfo with a fixed isolation type.
type isolationTenantServicer struct {
	mockTenantServicer
	isolation tenant.IsolationType
}

func (s *isolationTenantServicer) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{
		ID:            tenantID,
		Name:          tenantID,
		IsolationType: s.isolation,
	}, nil
}

func TestRESTServiceRegistry_FailsClosedWhenIsolationBackendUnwired(t *testing.T) {
	cases := []struct {
		name      string
		isolation tenant.IsolationType
	}{
		{"volume_without_tenant_hosts", tenant.IsolationVolume},
		{"raft_without_raft_backend", tenant.IsolationRaft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// No withTenantHostRegistry / withRaftBackendFactory option is set,
			// so the dedicated backend is nil.
			r := NewRESTServiceRegistry(
				newFilledRegistry(),
				nil,
				"system",
				WithTenantServicer(&isolationTenantServicer{isolation: c.isolation}),
			)
			svc, err := r.KVService("acme", "default")
			if svc != nil {
				t.Fatal("must not return a shared-store servicer when the isolation backend is unwired")
			}
			var ibu *rest.IsolationBackendUnavailableError
			if !errors.As(err, &ibu) {
				t.Fatalf("err = %v, want *rest.IsolationBackendUnavailableError", err)
			}
			if ibu.Isolation != string(c.isolation) {
				t.Errorf("Isolation = %q, want %q", ibu.Isolation, string(c.isolation))
			}
		})
	}
}
