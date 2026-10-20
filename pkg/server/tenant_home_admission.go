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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// A standalone tenant's home store (tenant.LocalHomes) is its barrier's own
// backend: it has no Host or raft shard MigrateEncryption can stop, so the
// registry admits its operations against the same migrating guard Services
// reads, and MigrateEncryption drains the admitted ones before it touches the
// barrier. A home-store operation therefore either completes before the
// migration re-encrypts the home records, or is refused with the retryable
// ErrTenantMigrationInProgress and, retried after the migration, runs under
// the keyed barrier.

// Compile-time interface checks.
var (
	_ tenant.HomeAdmission = (*TenantHostRegistry)(nil)
	_ tenant.HomeAdmission = (*TenantHostRegistryHolder)(nil)
)

// homeOpGate counts one tenant's admitted home-store operations.
type homeOpGate struct {
	mu       sync.Mutex
	inflight int
	// idle, when non-nil, is closed when inflight next reaches zero; a drain
	// waiting on the tenant creates it.
	idle chan struct{}
}

// release ends one admitted operation.
func (g *homeOpGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	if g.inflight == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// homeOpGate returns tenantID's gate, creating it on first use. op names the
// caller in the TenantHostError wrapping ErrHomeOpGateInvalid it returns when
// the value held for tenantID is not a *homeOpGate.
func (r *TenantHostRegistry) homeOpGate(tenantID, op string) (*homeOpGate, error) {
	v, ok := r.homeOps.Load(tenantID)
	if !ok {
		v, _ = r.homeOps.LoadOrStore(tenantID, &homeOpGate{})
	}
	g, ok := v.(*homeOpGate)
	if !ok {
		return nil, &TenantHostError{TenantID: tenantID, Op: op, Err: ErrHomeOpGateInvalid}
	}
	return g, nil
}

// AdmitHome implements tenant.HomeAdmission. It refuses an operation on
// tenantID's home store while the tenant's encryption migration runs, with
// the TenantHostError wrapping ErrTenantMigrationInProgress that Services
// returns in the same window, and otherwise counts it in flight until its
// release. It refuses with ErrHomeOpGateInvalid when the tenant's gate
// cannot be read. A nil registry admits every operation.
func (r *TenantHostRegistry) AdmitHome(tenantID string) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	g, err := r.homeOpGate(tenantID, "home")
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	if _, fenced := r.migrating.Load(tenantID); fenced {
		g.mu.Unlock()
		return nil, &TenantHostError{TenantID: tenantID, Op: "home", Err: ErrTenantMigrationInProgress}
	}
	g.inflight++
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(g.release) }, nil
}

// drainHomeOps waits until no home-store operation of tenantID is in flight.
// The caller has already set the tenant's migrating guard, so AdmitHome
// admits none after the count is read and the wait is bounded by the
// operations already running. It returns the TenantHostError wrapping
// ctx's error when ctx ends first, and the one wrapping ErrHomeOpGateInvalid
// when the tenant's gate cannot be read.
func (r *TenantHostRegistry) drainHomeOps(ctx context.Context, tenantID string) error {
	g, err := r.homeOpGate(tenantID, "migrate-drain-home")
	if err != nil {
		return err
	}
	g.mu.Lock()
	if g.inflight == 0 {
		g.mu.Unlock()
		return nil
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()
	if r.onHomeDrainWait != nil {
		r.onHomeDrainWait(tenantID)
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return &TenantHostError{TenantID: tenantID, Op: "migrate-drain-home", Err: ctx.Err()}
	}
}

// AdmitHome implements tenant.HomeAdmission over the registry h holds when
// the operation is admitted. Before a registry is stored, no migration can
// run, so every operation is admitted.
func (h *TenantHostRegistryHolder) AdmitHome(tenantID string) (func(), error) {
	return h.Load().AdmitHome(tenantID)
}
