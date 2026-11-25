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

package auth_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestSORoleRequiredError_MessageAndAs(t *testing.T) {
	err := fmt.Errorf("gate: %w", &auth.SORoleRequiredError{})
	if got := (&auth.SORoleRequiredError{}).Error(); got != "SO role required" {
		t.Fatalf("Error() = %q", got)
	}
	var target *auth.SORoleRequiredError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As did not match a wrapped SORoleRequiredError")
	}
	var conflict *auth.SORoleConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("SORoleRequiredError matched SORoleConflictError")
	}
}

func TestSORoleConflictError_MessageNamesTenant(t *testing.T) {
	for _, tenant := range []string{"t1", "acme-prod", ""} {
		e := &auth.SORoleConflictError{TenantID: tenant}
		want := "separation of duties: the SO role cannot be held with the admin role of tenant " + tenant
		if got := e.Error(); got != want {
			t.Fatalf("Error() = %q, want %q", got, want)
		}
		var target *auth.SORoleConflictError
		if !errors.As(fmt.Errorf("wrap: %w", e), &target) || target.TenantID != tenant {
			t.Fatalf("errors.As lost the conflict or its tenant %q: %+v", tenant, target)
		}
	}
}

func TestSORoleConflictError_MetadataRoundTrip(t *testing.T) {
	for _, tenant := range []string{"t1", "tenant/with spaces", ""} {
		md := (&auth.SORoleConflictError{TenantID: tenant}).Metadata()
		if len(md) != 1 {
			t.Fatalf("Metadata() = %v, want one key", md)
		}
		got := auth.SORoleConflictFromMetadata(md)
		if got == nil || got.TenantID != tenant {
			t.Fatalf("round trip of %q = %+v", tenant, got)
		}
	}
}

func TestSORoleConflictFromMetadata_MissingTenantKeepsType(t *testing.T) {
	for name, md := range map[string]map[string]string{
		"nil":     nil,
		"empty":   {},
		"foreign": {"other": "x"},
	} {
		got := auth.SORoleConflictFromMetadata(md)
		if got == nil {
			t.Fatalf("%s: nil error", name)
		}
		if got.TenantID != "" {
			t.Fatalf("%s: TenantID = %q, want empty", name, got.TenantID)
		}
	}
}

func TestSORefusalReasons_Distinct(t *testing.T) {
	if auth.ReasonSORoleRequired == auth.ReasonSORoleConflict {
		t.Fatalf("SO refusal reasons collide: %q", auth.ReasonSORoleRequired)
	}
	if auth.ReasonSORoleRequired != "SO_ROLE_REQUIRED" || auth.ReasonSORoleConflict != "SO_ROLE_CONFLICT" {
		t.Fatalf("wire reasons changed: %q, %q", auth.ReasonSORoleRequired, auth.ReasonSORoleConflict)
	}
}
