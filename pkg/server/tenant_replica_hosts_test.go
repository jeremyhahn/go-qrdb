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

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// identityHost is a service.Host compared by identity only; no method of it
// is called.
type identityHost struct {
	service.Host
	name string
}

// replicaHostsAdmin records the TenantReplicaHosts the server binds.
type replicaHostsAdmin struct {
	service.AdminServicer
	bound service.TenantReplicaHosts
}

func (a *replicaHostsAdmin) SetTenantReplicaHosts(h service.TenantReplicaHosts) { a.bound = h }

func TestTenantHostRegistry_StartedHostsListsBuiltHostsOnly(t *testing.T) {
	acme := &identityHost{name: "acme"}
	reg := NewTenantHostRegistry(nil, nil, nil)
	reg.hosts["acme"] = &tenantHost{tenantID: "acme", adapter: acme}
	reg.hosts["no-adapter"] = &tenantHost{tenantID: "no-adapter"}

	got := reg.StartedHosts()
	if len(got) != 1 || got["acme"] != service.Host(acme) {
		t.Fatalf("StartedHosts = %v, want only acme's adapter", got)
	}
	if len(reg.hosts) != 2 {
		t.Fatalf("StartedHosts changed the registry: %d hosts", len(reg.hosts))
	}
}

func TestTenantHostRegistry_StartedHostsNilAndEmpty(t *testing.T) {
	var nilReg *TenantHostRegistry
	if got := nilReg.StartedHosts(); got != nil {
		t.Fatalf("nil registry StartedHosts = %v, want nil", got)
	}
	if got := NewTenantHostRegistry(nil, nil, nil).StartedHosts(); len(got) != 0 {
		t.Fatalf("empty registry StartedHosts = %v, want none", got)
	}
}

func TestNew_BindsTenantReplicaHosts(t *testing.T) {
	admin := &replicaHostsAdmin{}
	reg := NewTenantHostRegistry(nil, nil, nil)
	New(&config.Config{}, nil, admin, WithTenantHostRegistry(reg))
	if admin.bound != service.TenantReplicaHosts(reg) {
		t.Fatalf("bound = %v, want the server's tenant host registry", admin.bound)
	}
}

func TestNew_NoTenantRegistryBindsNothing(t *testing.T) {
	admin := &replicaHostsAdmin{}
	New(&config.Config{}, nil, admin)
	if admin.bound != nil {
		t.Fatalf("bound = %v without a tenant host registry", admin.bound)
	}
	// An admin service without the wiring is left alone.
	New(&config.Config{}, nil, nil, WithTenantHostRegistry(NewTenantHostRegistry(nil, nil, nil)))
}
