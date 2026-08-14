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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantBarrierSeal satisfies the tenant.TenantServicer interface,
// dispatching to the configurable barrierSealFn when set (see
// tenant_barrier_seal_cmd_test.go), or defaulting to a no-op success.
func (m *mockTenantService) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx, tenantID)
	}
	return nil
}

// TenantBarrierStatus returns a default status to satisfy the
// tenant.TenantServicer interface, or dispatches to the configurable
// barrierStatusFn when set.
func (m *mockTenantService) TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx, tenantID)
	}
	return &tenant.TenantBarrierStatus{
		TenantID:    tenantID,
		Sealed:      false,
		Initialized: true,
		Strategy:    "passphrase",
	}, nil
}
