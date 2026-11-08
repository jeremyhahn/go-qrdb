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

// StrictIsolationGate is implemented by a tenant service that answers the
// strict-isolation activation check. The transports' isolation-policy verbs
// (the cluster floor's get and set, and a tenant update carrying
// isolation_policy) call it first and refuse with its error, so before
// activation they change nothing.
type StrictIsolationGate interface {
	RequireStrictIsolation() error
}

// strictIsolationGate holds the strict-isolation activation check. Unlike
// the other activation gates it fails closed: without a check the feature is
// not active, because this binary does not implement strict isolation's
// dedicated WALs (featuregate.StrictIsolation is not supported) and a
// composition that forgot the option must not open the verbs.
type strictIsolationGate struct {
	strictIsolationRequired func() error
}

// RequireStrictIsolation returns nil once strict-isolation is active, and
// otherwise the check's error (the composer's *feature.NotActiveError).
func (g strictIsolationGate) RequireStrictIsolation() error {
	if g.strictIsolationRequired == nil {
		return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
	}
	return g.strictIsolationRequired()
}

// RequireStrictIsolation answers the strict-isolation activation check for a
// transport's tenant service: the service's own check, or
// *feature.NotActiveError when the service has none, so a service without
// the gate never opens the isolation-policy verbs.
func RequireStrictIsolation(svc any) error {
	if g, ok := svc.(StrictIsolationGate); ok {
		return g.RequireStrictIsolation()
	}
	return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
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
// while required returns an error (the composer's *feature.NotActiveError),
// RequireStrictIsolation returns it and the isolation-policy verbs are
// refused. Without it the verbs are always refused.
func WithStrictIsolationGate(required func() error) TenantServiceOption {
	return func(s *TenantService) { s.strictIsolationRequired = required }
}
