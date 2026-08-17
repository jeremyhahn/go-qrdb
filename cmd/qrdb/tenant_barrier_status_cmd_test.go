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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestTenantBarrierStatusCmd_Table verifies "tenant barrier-status" renders
// the sealed/initialized state and strategy in the default table format.
func TestTenantBarrierStatusCmd_Table(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:    tenantID,
				Sealed:      true,
				Initialized: true,
				Strategy:    "passphrase",
			}, nil
		},
	}
	out, err := executeTenantCmd(t, svc, "barrier-status", "--id", "tenant-001")
	if err != nil {
		t.Fatalf("tenant barrier-status: %v", err)
	}
	for _, want := range []string{"tenant-001", "Sealed:      true", "Initialized: true", "passphrase"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q missing %q", out, want)
		}
	}
}

// TestTenantBarrierStatusCmd_JSON verifies --output json emits the raw
// response fields.
func TestTenantBarrierStatusCmd_JSON(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID: tenantID,
				Sealed:   false, Initialized: true, Strategy: "none",
			}, nil
		},
	}
	out, err := executeTenantCmdOut(t, svc, "json", "barrier-status", "--id", "t2")
	if err != nil {
		t.Fatalf("tenant barrier-status --output json: %v", err)
	}
	for _, want := range []string{`"tenant_id"`, `"sealed"`, `"initialized"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json output %q missing %q", out, want)
		}
	}
}

// TestTenantBarrierStatusCmd_ServiceError verifies a service failure is
// surfaced as the command error.
func TestTenantBarrierStatusCmd_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	if _, err := executeTenantCmd(t, svc, "barrier-status", "--id", "missing"); err == nil {
		t.Fatal("expected error from failing service")
	}
}

// TestTenantBarrierStatusCmd_RequiresID verifies --id is mandatory.
func TestTenantBarrierStatusCmd_RequiresID(t *testing.T) {
	svc := &mockTenantService{}
	if _, err := executeTenantCmd(t, svc, "barrier-status"); err == nil {
		t.Fatal("expected error when --id is omitted")
	}
}
