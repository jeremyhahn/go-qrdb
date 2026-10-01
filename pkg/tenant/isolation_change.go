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
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Isolation-change precondition
//
// Changing a tenant's isolation type moves where its data is stored, so a
// tenant that already holds data would be orphaned by it. UpdateTenant has
// always documented ErrTenantHasData for that case, but only the REST handler
// enforced it -- gRPC, the Unix socket, QUIC, MCP and the embedded transport
// changed isolation on a data-bearing tenant unchecked. The check now lives
// here, in the one place every transport reaches.
//
// Whether a tenant holds data is a data-plane question this package cannot
// answer without importing the data plane, so it asks through a narrow seam,
// TenantDataProbe, wired after construction exactly as the teardown seams
// are (teardown.go). service.TenantDataProbe is the implementation.
// ---------------------------------------------------------------------------

// TenantDataProbe reports whether a tenant holds any stored data.
type TenantDataProbe interface {
	// TenantHasData reports whether tenantID holds any data. An error means
	// the question could not be answered; the caller must not treat it as
	// "empty".
	TenantHasData(ctx context.Context, tenantID string) (bool, error)
}

// IsolationChangeWiring is the post-construction wiring surface for the
// isolation-change precondition, implemented by every TenantServicer this
// package ships. SetTenantDataProbe is safe to call while updates are served.
type IsolationChangeWiring interface {
	// SetTenantDataProbe sets the probe UpdateTenant consults before it
	// changes a tenant's isolation type. With none wired, a change of
	// isolation type is refused (ErrTenantDataUnverifiable): the
	// precondition fails closed.
	SetTenantDataProbe(TenantDataProbe)
}

// Compile-time checks: both tenant services carry the probe seam.
var (
	_ IsolationChangeWiring = (*TenantService)(nil)
	_ IsolationChangeWiring = (*kvTenantService)(nil)
)

// isolationChangeGuard holds UpdateTenant's data probe. It is embedded in both
// tenant services so the precondition is identical with or without the seal
// subsystem. The probe is held atomically so it can be wired or replaced
// without racing an update in flight.
type isolationChangeGuard struct {
	dataProbe atomic.Pointer[dataProbeRef]
}

// dataProbeRef boxes a TenantDataProbe for atomic storage.
type dataProbeRef struct{ probe TenantDataProbe }

// SetTenantDataProbe implements IsolationChangeWiring. A nil probe unwires
// it, which makes an isolation change fail closed again.
func (g *isolationChangeGuard) SetTenantDataProbe(p TenantDataProbe) {
	if p == nil {
		g.dataProbe.Store(nil)
		return
	}
	g.dataProbe.Store(&dataProbeRef{probe: p})
}

// WithTenantDataProbe injects the probe UpdateTenant consults before changing
// a tenant's isolation type. See IsolationChangeWiring.SetTenantDataProbe.
func WithTenantDataProbe(p TenantDataProbe) TenantServiceOption {
	return func(s *TenantService) { s.SetTenantDataProbe(p) }
}

// checkIsolationChange enforces the precondition for applying patch to info.
// A patch that names no isolation type, or names the tenant's current
// effective one, changes nothing and needs no check. Any real change requires
// a probe that answers "no data": ErrTenantHasData when the tenant holds data,
// ErrTenantDataUnverifiable when no probe is wired, and *TenantDataProbeError
// when the probe could not answer.
func (g *isolationChangeGuard) checkIsolationChange(ctx context.Context, info *TenantInfo, patch TenantPatch) error {
	if patch.IsolationType == "" || patch.IsolationType == info.EffectiveIsolationType() {
		return nil
	}
	ref := g.dataProbe.Load()
	if ref == nil {
		return ErrTenantDataUnverifiable
	}
	hasData, err := ref.probe.TenantHasData(ctx, info.ID)
	if err != nil {
		return &TenantDataProbeError{TenantID: info.ID, Err: err}
	}
	if hasData {
		return ErrTenantHasData
	}
	return nil
}
