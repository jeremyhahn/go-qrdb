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

// TestStrictIsolationRequired_RefusedOnEveryHost pins that this binary, which
// does not support strict-isolation, refuses it on every host: standalone, no
// host, and a clustered host even if its gate claims the feature enabled.
func TestStrictIsolationRequired_RefusedOnEveryHost(t *testing.T) {
	hosts := map[string]service.Host{
		"standalone": newStandaloneHost(),
		"no host":    nil,
		"enabled":    gatedHost{enabled: map[feature.Feature]bool{featuregate.StrictIsolation: true}},
	}
	for name, host := range hosts {
		t.Run(name, func(t *testing.T) {
			err := strictIsolationRequired(host)()
			var notActive *feature.NotActiveError
			if !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation {
				t.Fatalf("strictIsolationRequired()() = %v, want NotActiveError(%s)", err, featuregate.StrictIsolation)
			}
		})
	}
}
