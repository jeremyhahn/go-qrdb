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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// --status moves the tenant; update, get and list all show the status.
func TestTenantUpdateCmd_StatusRoundTrip(t *testing.T) {
	svc := descriptiveTenants(t)
	out, err := executeTenantCmd(t, svc, "get", "acme")
	if err != nil || !strings.Contains(out, "Status:     active") {
		t.Fatalf("get of a new tenant = %v:\n%s\nwant Status: active", err, out)
	}
	out, err = executeTenantCmd(t, svc, "update", "acme", "--status", "suspended")
	if err != nil || !strings.Contains(out, "Status:     suspended") {
		t.Fatalf("update --status suspended = %v:\n%s", err, out)
	}
	got, err := svc.GetTenant(context.Background(), "acme")
	if err != nil || got.Status != tenant.StatusSuspended {
		t.Fatalf("stored status = %q, %v; want suspended", got.Status, err)
	}
	out, err = executeTenantCmd(t, svc, "list")
	if err != nil || !strings.Contains(out, "STATUS") || !strings.Contains(out, "suspended") {
		t.Fatalf("list = %v:\n%s\nwant a STATUS column showing suspended", err, out)
	}
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--status", "active"); err != nil {
		t.Fatalf("update --status active: %v", err)
	}
	if got, _ := svc.GetTenant(context.Background(), "acme"); got.Status != tenant.StatusActive {
		t.Fatalf("reactivated status = %q", got.Status)
	}
}

// Without --status the status is unchanged; an unknown status and a refused
// transition reach the caller as the service's typed errors and store nothing.
func TestTenantUpdateCmd_StatusAbsentAndRefused(t *testing.T) {
	svc := descriptiveTenants(t)
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--status", "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--description", "d"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetTenant(context.Background(), "acme"); got.Status != tenant.StatusDisabled {
		t.Fatalf("update without --status changed it: %q", got.Status)
	}
	_, err := executeTenantCmd(t, svc, "update", "acme", "--status", "paused")
	var ie *tenant.InvalidStatusError
	if !errors.As(err, &ie) {
		t.Fatalf("--status paused = %v; want *tenant.InvalidStatusError", err)
	}
	_, err = executeTenantCmd(t, svc, "update", "acme", "--status", "suspended")
	var te *tenant.StatusTransitionError
	if !errors.As(err, &te) {
		t.Fatalf("disabled -> suspended = %v; want *tenant.StatusTransitionError", err)
	}
	if got, _ := svc.GetTenant(context.Background(), "acme"); got.Status != tenant.StatusDisabled {
		t.Fatalf("refused update was stored: %q", got.Status)
	}
}
