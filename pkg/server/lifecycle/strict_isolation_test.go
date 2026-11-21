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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestStrictIsolationRequired_FollowsTheHost pins the strict-isolation
// activation check: always a check (never nil), open on a standalone host
// and on a cluster that activated the feature, and a cluster-wide
// NotActiveError on no host and on a cluster that has not.
func TestStrictIsolationRequired_FollowsTheHost(t *testing.T) {
	cases := map[string]struct {
		host   service.Host
		active bool
	}{
		"standalone": {host: newStandaloneHost(), active: true},
		"no host":    {host: nil},
		"not active": {host: gatedHost{enabled: map[feature.Feature]bool{featuregate.PerNodeBarrierKeys: true}}},
		"activated":  {host: gatedHost{enabled: map[feature.Feature]bool{featuregate.StrictIsolation: true}}, active: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			check := strictIsolationRequired(tc.host)
			if check == nil {
				t.Fatal("strictIsolationRequired = nil; the tenant service reads a nil check as not active")
			}
			err := check()
			if tc.active {
				if err != nil {
					t.Fatalf("strictIsolationRequired()() = %v, want nil", err)
				}
				return
			}
			var notActive *feature.NotActiveError
			if !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation || notActive.ShardID != 0 {
				t.Fatalf("strictIsolationRequired()() = %v, want cluster-wide NotActiveError(%s)", err, featuregate.StrictIsolation)
			}
		})
	}
}
