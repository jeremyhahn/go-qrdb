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

// The table shows this node's key fingerprint when the node reports one, and
// omits it when it does not.
func TestTenantBarrierStatusCmd_Fingerprint(t *testing.T) {
	status := &tenant.TenantBarrierStatus{Initialized: true, Strategy: "passphrase", KeyFingerprint: "0a1b2c"}
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			st := *status
			st.TenantID = tenantID
			return &st, nil
		},
	}
	out, err := executeTenantCmd(t, svc, "barrier-status", "--id", "acme")
	if err != nil {
		t.Fatalf("tenant barrier-status: %v", err)
	}
	if !strings.Contains(out, "Fingerprint: 0a1b2c") {
		t.Fatalf("output %q missing the fingerprint", out)
	}

	status = &tenant.TenantBarrierStatus{Initialized: true, Strategy: "none"}
	out, err = executeTenantCmd(t, svc, "barrier-status", "--id", "acme")
	if err != nil {
		t.Fatalf("tenant barrier-status: %v", err)
	}
	if strings.Contains(out, "Fingerprint") {
		t.Fatalf("output %q shows a fingerprint for a barrier reporting none", out)
	}
}

// The table lists each node serving the barrier, the nodes a barrier init
// fans out to, and lists none when the node reports none.
func TestTenantBarrierStatusCmd_ServingNodes(t *testing.T) {
	status := &tenant.TenantBarrierStatus{Initialized: false, Nodes: []tenant.BarrierNode{
		{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443"}},
		{Node: "n2:7000"},
	}}
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			st := *status
			st.TenantID = tenantID
			return &st, nil
		},
	}
	out, err := executeTenantCmd(t, svc, "barrier-status", "--id", "acme")
	if err != nil {
		t.Fatalf("tenant barrier-status: %v", err)
	}
	if !strings.Contains(out, "Node:        n1:7000\n") || !strings.Contains(out, "Node:        n2:7000\n") {
		t.Fatalf("output %q missing a serving node", out)
	}

	status = &tenant.TenantBarrierStatus{Initialized: true, Strategy: "none"}
	out, err = executeTenantCmd(t, svc, "barrier-status", "--id", "acme")
	if err != nil {
		t.Fatalf("tenant barrier-status: %v", err)
	}
	if strings.Contains(out, "Node:") {
		t.Fatalf("output %q lists a node the status did not report", out)
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
