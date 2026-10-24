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
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// descriptiveTenants returns the REAL seal-disabled tenant service holding
// one tenant, "acme".
func descriptiveTenants(t *testing.T) tenant.TenantServicer {
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

func TestTenantUpdateCmd_DescriptionAndMetadataRoundTrip(t *testing.T) {
	svc := descriptiveTenants(t)
	out, err := executeTenantCmd(t, svc, "update", "acme",
		"--description", "Acme production", "--metadata", "team=storage", "--metadata", "env=prod")
	if err != nil {
		t.Fatalf("tenant update: %v", err)
	}
	if !strings.Contains(out, "Description: Acme production") || !strings.Contains(out, "Metadata:   env=prod,team=storage") {
		t.Fatalf("update output lacks the descriptive fields:\n%s", out)
	}

	out, err = executeTenantCmd(t, svc, "get", "acme")
	if err != nil {
		t.Fatalf("tenant get: %v", err)
	}
	if !strings.Contains(out, "Description: Acme production") || !strings.Contains(out, "Metadata:   env=prod,team=storage") {
		t.Fatalf("get output lacks the descriptive fields:\n%s", out)
	}
}

func TestTenantUpdateCmd_AbsentFlagsLeaveDescriptiveFields(t *testing.T) {
	svc := descriptiveTenants(t)
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--description", "keep", "--metadata", "env=prod"); err != nil {
		t.Fatalf("tenant update: %v", err)
	}
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--name", "Acme Inc"); err != nil {
		t.Fatalf("tenant update --name: %v", err)
	}
	got, err := svc.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "keep" || !maps.Equal(got.Metadata, map[string]string{"env": "prod"}) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.Description, got.Metadata)
	}
}

func TestTenantUpdateCmd_ClearDescriptionAndMetadata(t *testing.T) {
	svc := descriptiveTenants(t)
	if _, err := executeTenantCmd(t, svc, "update", "acme", "--description", "gone", "--metadata", "env=prod"); err != nil {
		t.Fatalf("tenant update: %v", err)
	}
	out, err := executeTenantCmd(t, svc, "update", "acme", "--description", "", "--clear-metadata")
	if err != nil {
		t.Fatalf("tenant update --clear-metadata: %v", err)
	}
	if strings.Contains(out, "Description:") || strings.Contains(out, "Metadata:") {
		t.Fatalf("cleared tenant still prints descriptive fields:\n%s", out)
	}
	got, err := svc.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "" || len(got.Metadata) != 0 {
		t.Fatalf("cleared tenant = %q, %v; want both empty", got.Description, got.Metadata)
	}
}

func TestTenantUpdateCmd_MetadataAndClearMetadataAreExclusive(t *testing.T) {
	_, err := executeTenantCmd(t, descriptiveTenants(t), "update", "acme", "--metadata", "env=prod", "--clear-metadata")
	if err == nil || !strings.Contains(err.Error(), "clear-metadata") {
		t.Fatalf("--metadata with --clear-metadata = %v; want a mutual-exclusion error", err)
	}
}

func TestTenantUpdateCmd_MalformedMetadataIsRefused(t *testing.T) {
	for _, entry := range []string{"novalue", "=v"} {
		_, err := executeTenantCmd(t, descriptiveTenants(t), "update", "acme", "--metadata", entry)
		var fe *InvalidFlagValueError
		if !errors.As(err, &fe) || fe.Flag != "metadata" {
			t.Fatalf("--metadata %q = %v; want *InvalidFlagValueError for metadata", entry, err)
		}
	}
}

func TestTenantUpdateCmd_OverLimitDescriptionIsRefused(t *testing.T) {
	svc := descriptiveTenants(t)
	_, err := executeTenantCmd(t, svc, "update", "acme", "--description", strings.Repeat("d", tenant.MaxDescriptionBytes+1))
	var fe *tenant.TenantFieldError
	if !errors.As(err, &fe) {
		t.Fatalf("over-limit description = %v; want *tenant.TenantFieldError", err)
	}
}

func TestTenantMetadataFlag(t *testing.T) {
	if md, err := tenantMetadataFlag(nil, false); err != nil || md != nil {
		t.Fatalf("no flags = %v, %v; want nil, nil (unchanged)", md, err)
	}
	if md, err := tenantMetadataFlag(nil, true); err != nil || md == nil || len(md) != 0 {
		t.Fatalf("clear = %v, %v; want a non-nil empty map", md, err)
	}
	md, err := tenantMetadataFlag([]string{"a=1", "b=x=y", "c="}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"a": "1", "b": "x=y", "c": ""}; !maps.Equal(md, want) {
		t.Fatalf("entries = %v; want %v", md, want)
	}
}

func TestFormatTenantMetadata(t *testing.T) {
	if got := formatTenantMetadata(map[string]string{"z": "1", "a": "2"}); got != "a=2,z=1" {
		t.Fatalf("format = %q; want sorted a=2,z=1", got)
	}
	if got := formatTenantMetadata(nil); got != "" {
		t.Fatalf("format(nil) = %q; want empty", got)
	}
}
