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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

func TestPendingUserTTLRequired_ActiveWithoutACluster(t *testing.T) {
	if required := PendingUserTTLRequired(nil); required != nil {
		t.Fatalf("pendingUserTTLRequired(nil) = %v, want nil (active)", required())
	}
	if required := PendingUserTTLRequired(newStandaloneHost()); required != nil {
		t.Fatalf("pendingUserTTLRequired(standalone) = %v, want nil (active)", required())
	}
}

func TestPendingUserTTLRequired_RefusedOnAnUngatedCluster(t *testing.T) {
	var notActive *feature.NotActiveError
	if err := PendingUserTTLRequired(&membershipHost{})(); !errors.As(err, &notActive) || notActive.Feature != featuregate.PendingUserTTL {
		t.Fatalf("required on an ungated cluster = %v, want NotActiveError(%s)", err, featuregate.PendingUserTTL)
	}
}

func TestPendingUserTTLRequired_FollowsTheGate(t *testing.T) {
	before := gatedHost{enabled: map[feature.Feature]bool{featuregate.TenantUndo: true}}
	var notActive *feature.NotActiveError
	if err := PendingUserTTLRequired(before)(); !errors.As(err, &notActive) || notActive.Feature != featuregate.PendingUserTTL || notActive.ShardID != 0 {
		t.Fatalf("required before activation = %v, want cluster-wide NotActiveError(%s)", err, featuregate.PendingUserTTL)
	}
	after := gatedHost{enabled: map[feature.Feature]bool{featuregate.PendingUserTTL: true}}
	if err := PendingUserTTLRequired(after)(); err != nil {
		t.Fatalf("required after activation = %v, want nil", err)
	}
}
