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

package lifecycle_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// TestBuildSealServicesOnly_DefaultQuotasAndEntitlements exercises the
// DefaultTenantQuotas and DefaultTenantEntitlements branches of
// buildTenantServiceOpts: both options must be threaded into the TenantService
// without error when supplied via Infra.
func TestBuildSealServicesOnly_DefaultQuotasAndEntitlements(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)

	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            slog.Default(),
		DefaultTenantQuotas: &quota.Quotas{
			MaxDatabases: 10,
			MaxKeys:      1000,
		},
		DefaultTenantEntitlements: &quota.Entitlements{
			AllowedIsolationModes: []string{"barrier", "prefix"},
			AllowDedicatedRaft:    true,
			AllowVolumeStorage:    true,
		},
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Tenant == nil {
		t.Error("Tenant is nil; expected TenantService built with default quotas/entitlements")
	}
}
