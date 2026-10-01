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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// recordingKVRegistry records the (tenant, database) every KV resolution asks
// for and resolves each to its own store.
type recordingKVRegistry struct {
	Registry
	asked []string
}

func (r *recordingKVRegistry) KVService(tenantID, db string) (KVServicer, error) {
	r.asked = append(r.asked, tenantID+"/"+db)
	return newRAGMockKV(), nil
}

// On an authenticated composition every transport binds the caller's tenant,
// so a RAG call whose context carries none is refused. It used to resolve for
// the system tenant (tenant.FromContext's fallback), handing a caller whose
// tenant was lost the system tenant's records.
func TestRAGBinding_RefusesACallWithNoTenant(t *testing.T) {
	reg := &recordingKVRegistry{}
	b := NewRAGBinding()
	b.Bind(reg)

	_, err := b.KV(context.Background())
	var required *RAGTenantRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("KV with no tenant = %v, want *RAGTenantRequiredError", err)
	}
	if !strings.Contains(err.Error(), "no tenant") {
		t.Errorf("error %q does not say what is missing", err)
	}
	if _, err := b.Vector(context.Background()); !errors.As(err, &required) {
		t.Fatalf("Vector with no tenant = %v, want *RAGTenantRequiredError", err)
	}
	if len(reg.asked) != 0 {
		t.Fatalf("a call with no tenant resolved %v", reg.asked)
	}

	acme := tenant.WithDatabase(tenant.WithTenantID(context.Background(), "acme"), "app")
	if _, err := b.KV(acme); err != nil {
		t.Fatalf("KV for acme: %v", err)
	}
	if reg.asked[0] != "acme/app" {
		t.Fatalf("resolved %v, want acme/app", reg.asked)
	}
}

// A composition with no request authentication names the tenant calls act
// for; a call that does carry a tenant still acts for its own.
func TestRAGBinding_UnauthenticatedTenant(t *testing.T) {
	reg := &recordingKVRegistry{}
	b := NewRAGBinding()
	b.Bind(reg, RAGUnauthenticatedTenant(tenant.SystemTenantID))

	if _, err := b.KV(context.Background()); err != nil {
		t.Fatalf("KV with no tenant on an unauthenticated composition: %v", err)
	}
	if _, err := b.KV(tenant.WithTenantID(context.Background(), "acme")); err != nil {
		t.Fatalf("KV for acme: %v", err)
	}
	want := []string{tenant.SystemTenantID + "/" + tenant.SystemTenantID, "acme/" + tenant.SystemTenantID}
	if len(reg.asked) != 2 || reg.asked[0] != want[0] || reg.asked[1] != want[1] {
		t.Fatalf("resolved %v, want %v", reg.asked, want)
	}
}
