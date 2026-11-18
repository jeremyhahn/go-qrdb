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

// PendingUserTTLRequired returns the activation check user.WithPendingTTLGate
// takes on host. A nil host, or a standalone node, which is its own whole
// cluster, gets nil: the TTL is active from the first start. A clustered node
// waits for featuregate.PendingUserTTL, judged cluster-wide, because a member
// that predates it drops PendingExpiresAt and ExpiryClaim when it rewrites a
// user record. A clustered host that gates no features cannot tell, so the
// TTL stays refused on it.
func PendingUserTTLRequired(host service.Host) func() error {
	if host == nil || standaloneHost(host) {
		return nil
	}
	gated, ok := host.(service.FeatureGatedHost)
	if !ok {
		return func() error { return &feature.NotActiveError{Feature: featuregate.PendingUserTTL} }
	}
	return func() error {
		if gated.FeatureEnabled(0, featuregate.PendingUserTTL) {
			return nil
		}
		return &feature.NotActiveError{Feature: featuregate.PendingUserTTL}
	}
}
