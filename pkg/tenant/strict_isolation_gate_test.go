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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

// strictIsolationService is a tenant service carrying check as its
// strict-isolation activation check.
type strictIsolationService struct{ check func() error }

func (s strictIsolationService) StrictIsolationCheck() func() error { return s.check }

// TestRequireStrictIsolation_FailsClosed pins that a service without the
// check, or with none injected, is refused as not active, and that an
// injected check decides otherwise.
func TestRequireStrictIsolation_FailsClosed(t *testing.T) {
	for name, svc := range map[string]any{"no gate": struct{}{}, "nil service": nil, "nil check": strictIsolationService{}} {
		var notActive *feature.NotActiveError
		if err := RequireStrictIsolation(svc); !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation {
			t.Fatalf("%s: RequireStrictIsolation = %v, want NotActiveError(%s)", name, err, featuregate.StrictIsolation)
		}
	}
	refused := errors.New("not yet")
	assert.ErrorIs(t, RequireStrictIsolation(strictIsolationService{check: func() error { return refused }}), refused)
	assert.NoError(t, RequireStrictIsolation(strictIsolationService{check: func() error { return nil }}))
}

// TestStrictIsolationFeature_MatchesFeatureGate pins the plain value
// pkg/tenant refuses with to the cluster feature gate's name.
func TestStrictIsolationFeature_MatchesFeatureGate(t *testing.T) {
	if strictIsolationFeature != featuregate.StrictIsolation {
		t.Fatalf("strictIsolationFeature = %q, want %q", strictIsolationFeature, featuregate.StrictIsolation)
	}
}
