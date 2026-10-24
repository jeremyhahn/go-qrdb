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

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The update tool carries status, the read returns it, and an absent status
// leaves it unchanged.
func TestHandleTenantUpdate_StatusRoundTrip(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	if got := mcpTenantGet(t, svc).Status; got != tenant.StatusActive {
		t.Fatalf("new tenant status = %q; want active", got)
	}
	for _, want := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled, tenant.StatusActive} {
		if err := mcpTenantUpdate(t, svc, map[string]any{"status": string(want)}); err != nil {
			t.Fatalf("update to %s: %v", want, err)
		}
		if got := mcpTenantGet(t, svc).Status; got != want {
			t.Fatalf("read after %s = %q", want, got)
		}
	}
	if err := mcpTenantUpdate(t, svc, map[string]any{"status": "suspended"}); err != nil {
		t.Fatal(err)
	}
	if err := mcpTenantUpdate(t, svc, map[string]any{"description": "d"}); err != nil {
		t.Fatal(err)
	}
	if got := mcpTenantGet(t, svc).Status; got != tenant.StatusSuspended {
		t.Fatalf("absent status changed it: %q", got)
	}
}

// An unknown status is InvalidArgument; a transition the table does not allow
// is FailedPrecondition; a status that is not a string is a tool error.
// None is stored.
func TestHandleTenantUpdate_StatusRefusals(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	err := mcpTenantUpdate(t, svc, map[string]any{"status": "paused"})
	var ie *tenant.InvalidStatusError
	if !errors.As(err, &ie) || errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("unknown status = %v; want an InvalidArgument *tenant.InvalidStatusError", err)
	}
	if err := mcpTenantUpdate(t, svc, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	err = mcpTenantUpdate(t, svc, map[string]any{"status": "suspended"})
	var te *tenant.StatusTransitionError
	if !errors.As(err, &te) || errclass.Classify(err) != errclass.FailedPrecondition {
		t.Fatalf("disabled -> suspended = %v; want a FailedPrecondition *tenant.StatusTransitionError", err)
	}
	result, err := handleTenantUpdate(svc, nil)(context.Background(),
		buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{"id": "acme", "status": 3}))
	if err != nil || !result.IsError || !strings.Contains(resultText(t, result), "status") {
		t.Fatalf("non-string status = %v, %v; want a tool error naming status", result, err)
	}
	if got := mcpTenantGet(t, svc).Status; got != tenant.StatusDisabled {
		t.Fatalf("refused update was stored: %q", got)
	}
}
