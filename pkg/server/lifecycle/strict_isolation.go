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
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// strictIsolationRequired is the strict-isolation activation check for host.
// It refuses with *feature.NotActiveError while this binary does not support
// the feature (phase 5's dedicated WALs are not built), on every host,
// standalone included. Once supported, a standalone host is active from the
// start and a clustered one once the cluster has activated the feature.
func strictIsolationRequired(host service.Host) func() error {
	gated, _ := host.(service.FeatureGatedHost)
	return func() error {
		if slices.Contains(featuregate.Supported(), tenant.StrictIsolation) &&
			(standaloneHost(host) || gated != nil && gated.FeatureEnabled(0, tenant.StrictIsolation)) {
			return nil
		}
		return &feature.NotActiveError{Feature: tenant.StrictIsolation}
	}
}
