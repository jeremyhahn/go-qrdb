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
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

// The cluster isolation floor's strict mode and the per-tenant
// isolation_policy that opts into it are gated on featuregate.StrictIsolation:
// the cluster.IsolationPolicy get and set verbs and a tenant update carrying
// isolation_policy are refused with *feature.NotActiveError until it is
// active, and before then behaviour is unchanged.

// RequireStrictIsolation answers the strict-isolation activation check for a
// transport's tenant service: nil once the service's injected check passes,
// and otherwise its error, or *feature.NotActiveError when the service
// carries no check (it does not implement StrictIsolationGate, or returns
// nil), so a composition without the gate never opens the isolation-policy
// verbs.
func RequireStrictIsolation(svc any) error {
	if g, ok := svc.(StrictIsolationGate); ok {
		if check := g.StrictIsolationCheck(); check != nil {
			return check()
		}
	}
	return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
}

// StrictIsolationGate is implemented by a tenant service that carries the
// strict-isolation activation check. The transports' isolation-policy verbs
// (the cluster floor's get and set, and a tenant update carrying
// isolation_policy) run it first, through RequireStrictIsolation, and refuse
// with its error, so before activation they change nothing.
type StrictIsolationGate interface {
	// StrictIsolationCheck returns the injected activation check, or nil
	// when none was injected, which the caller treats as not active.
	StrictIsolationCheck() func() error
}

// strictIsolationGate holds the strict-isolation activation check. Unlike
// the other activation gates a missing check means not active: a
// composition that forgot the option must not open the verbs.
type strictIsolationGate struct {
	strictIsolationRequired func() error
}

// StrictIsolationCheck returns the injected activation check, nil if none.
func (g strictIsolationGate) StrictIsolationCheck() func() error {
	return g.strictIsolationRequired
}

// SetStrictIsolationGate injects the strict-isolation activation check on a
// service built without options (NewKVTenantService); see
// WithStrictIsolationGate.
func (g *strictIsolationGate) SetStrictIsolationGate(required func() error) {
	g.strictIsolationRequired = required
}

// StrictIsolationWiring is implemented by the no-seal tenant service, which
// takes its strict-isolation gate after construction rather than as an
// option.
type StrictIsolationWiring interface {
	SetStrictIsolationGate(required func() error)
}

var (
	_ StrictIsolationGate   = (*TenantService)(nil)
	_ StrictIsolationGate   = (*kvTenantService)(nil)
	_ StrictIsolationWiring = (*kvTenantService)(nil)
)

// WithStrictIsolationGate injects the strict-isolation activation check:
// while required returns an error (the composer's *feature.NotActiveError)
// the isolation-policy verbs are refused with it. Without it they are always
// refused.
func WithStrictIsolationGate(required func() error) TenantServiceOption {
	return func(s *TenantService) { s.strictIsolationRequired = required }
}
