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

package lifecycle

import (
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// strictIsolationRequired is the strict-isolation activation check for host
// (featuregate.StrictIsolation together with featuregate.GuardedWrites). It
// always returns a check, never nil: the tenant service reads a missing check
// as not active. A standalone host is active from the start; a clustered one
// once the cluster has activated BOTH features, judged cluster-wide (group 0,
// so no group is named). Until then the isolation-policy verbs, a strict
// tenant's database create and an isolated tenant's placement are refused with
// *feature.NotActiveError naming the first missing feature: strict-isolation,
// then guarded-writes. A nil host or one that carries no feature gate is never
// active. A mixed-version cluster, whose older members do not advertise a
// feature, never activates it, so there a strict tenant's create and an
// isolated tenant's placement stay refused.
//
// Guarded-writes is required because a group that has not activated it
// proposes every write guard unguarded (service.guardCommand drops it): a
// tightening committed then would leave a shared-store write or a
// system-routed audit append already in flight with only the propose-time
// recheck. Refusing rather than proceeding unguarded follows the barrier-init
// precedent. Activation is permanent, so once this check passes no group
// proposes a guard unguarded again, and every later tightening's in-flight
// writes carry their guard.
func strictIsolationRequired(host service.Host) func() error {
	gated, _ := host.(service.FeatureGatedHost)
	return func() error {
		if standaloneHost(host) {
			return nil
		}
		if gated == nil || !gated.FeatureEnabled(0, featuregate.StrictIsolation) {
			return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
		}
		if !gated.FeatureEnabled(0, featuregate.GuardedWrites) {
			return &feature.NotActiveError{Feature: featuregate.GuardedWrites}
		}
		return nil
	}
}
