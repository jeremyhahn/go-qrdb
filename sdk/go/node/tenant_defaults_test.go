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

package node

import (
	"slices"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

// An embedded node's tenant entitlements are the configuration's, as qrdbd
// reads them, unless the embedder set them explicitly.
func TestTenantEntitlements(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Tenant.DefaultEntitlements.AllowedIsolationModes = []string{quota.IsolationModePrefix, quota.IsolationModeRaft}
	cfg.Tenant.DefaultEntitlements.AllowDedicatedRaft = true

	got := (&Node{}).tenantEntitlements(cfg)
	if !got.AllowDedicatedRaft || !slices.Contains(got.AllowedIsolationModes, quota.IsolationModeRaft) {
		t.Fatalf("tenantEntitlements = %+v; want the configuration's", got)
	}

	explicit := &quota.Entitlements{AllowedIsolationModes: []string{quota.IsolationModePrefix}}
	if got := (&Node{defaultTenantEntitlements: explicit}).tenantEntitlements(cfg); got != explicit {
		t.Fatalf("tenantEntitlements = %+v; want WithDefaultTenantEntitlements's", got)
	}
}
