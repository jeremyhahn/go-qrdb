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
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// descriptiveMCPTenants returns the REAL seal-disabled tenant service holding
// one tenant, "acme".
func descriptiveMCPTenants(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return svc
}

// mcpTenantUpdate calls qrdb_tenant_update on tenant "acme" with args.
func mcpTenantUpdate(t *testing.T, svc tenant.TenantServicer, args map[string]any) error {
	t.Helper()
	args["id"] = "acme"
	result, err := handleTenantUpdate(svc, nil)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_update", args))
	if err != nil {
		return err
	}
	if result.IsError {
		t.Fatalf("qrdb_tenant_update(%v) = tool error %s", args, resultText(t, result))
	}
	return nil
}

// mcpTenantGet reads tenant "acme" back through qrdb_tenant_get.
func mcpTenantGet(t *testing.T, svc tenant.TenantServicer) tenant.TenantInfo {
	t.Helper()
	result, err := handleTenantGet(svc, nil)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "acme"}))
	if err != nil || result.IsError {
		t.Fatalf("qrdb_tenant_get = %v, %v", result, err)
	}
	var info tenant.TenantInfo
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestHandleTenantUpdate_DescriptionAndMetadataRoundTrip(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	want := map[string]string{"env": "prod", "team": "storage"}

	if err := mcpTenantUpdate(t, svc, map[string]any{
		"description": "Acme production",
		"metadata":    `{"env":"prod","team":"storage"}`,
	}); err != nil {
		t.Fatal(err)
	}
	got := mcpTenantGet(t, svc)
	if got.Description != "Acme production" || !maps.Equal(got.Metadata, want) {
		t.Fatalf("get = description %q, metadata %v; want %q, %v", got.Description, got.Metadata, "Acme production", want)
	}

	// Absent arguments leave the stored values unchanged.
	if err := mcpTenantUpdate(t, svc, map[string]any{"name": "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	got = mcpTenantGet(t, svc)
	if got.Description != "Acme production" || !maps.Equal(got.Metadata, want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.Description, got.Metadata)
	}
}

func TestHandleTenantUpdate_ClearMetadataClears(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	if err := mcpTenantUpdate(t, svc, map[string]any{
		"description": "to clear",
		"metadata":    map[string]any{"env": "prod"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := mcpTenantUpdate(t, svc, map[string]any{"description": "", "clear_metadata": true}); err != nil {
		t.Fatal(err)
	}
	got := mcpTenantGet(t, svc)
	if got.Description != "" || len(got.Metadata) != 0 {
		t.Fatalf("cleared tenant = description %q, metadata %v; want both empty", got.Description, got.Metadata)
	}
}

func TestHandleTenantUpdate_OverLimitIsInvalidArgument(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	cases := map[string]map[string]any{
		"description":    {"description": strings.Repeat("d", tenant.MaxDescriptionBytes+1)},
		"metadata value": {"metadata": map[string]any{"k": strings.Repeat("v", tenant.MaxMetadataValueBytes+1)}},
	}
	for name, args := range cases {
		err := mcpTenantUpdate(t, svc, args)
		var fe *tenant.TenantFieldError
		if !errors.As(err, &fe) || errclass.Classify(err) != errclass.InvalidArgument {
			t.Errorf("%s over limit = %v; want an InvalidArgument *tenant.TenantFieldError", name, err)
		}
	}
	got := mcpTenantGet(t, svc)
	if got.Description != "" || len(got.Metadata) != 0 {
		t.Fatalf("refused update was stored: %q, %v", got.Description, got.Metadata)
	}
}

func TestHandleTenantUpdate_DescriptiveArgumentsMustBeWellTyped(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	for name, args := range map[string]map[string]any{
		"description": {"id": "acme", "description": 7},
		"metadata":    {"id": "acme", "metadata": "not json"},
	} {
		result, err := handleTenantUpdate(svc, nil)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_update", args))
		if err != nil || !result.IsError {
			t.Errorf("malformed %s = %v, %v; want a tool error", name, result, err)
		}
	}
}
