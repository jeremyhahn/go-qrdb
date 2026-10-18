// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authtest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// inTenant is a context targeting tenantID.
func inTenant(tenantID string) context.Context {
	return tenant.WithTenantID(context.Background(), tenantID)
}

func TestGrantAuthorizer_AllowsOnlyWhatIsGranted(t *testing.T) {
	g := NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}})
	alice := &auth.Identity{Subject: "alice", TenantID: "t1"}
	if err := g.Authorize(inTenant("t1"), alice, "kv", "get"); err != nil {
		t.Fatalf("granted kv:get = %v, want nil", err)
	}
	if err := g.Authorize(inTenant("t1"), alice, "kv", "put"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("ungranted kv:put = %v, want ErrUnauthorized", err)
	}
	bob := &auth.Identity{Subject: "bob", TenantID: "t1"}
	if err := g.Authorize(inTenant("t1"), bob, "kv", "get"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("subject with no grants = %v, want ErrUnauthorized", err)
	}
}

func TestGrantAuthorizer_DeniesAnIdentityOutsideTheTargetedTenant(t *testing.T) {
	g := NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}})
	alice := &auth.Identity{Subject: "alice", TenantID: "t1"}
	if err := g.Authorize(inTenant("t2"), alice, "kv", "get"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("t1 identity in a t2 request = %v, want ErrUnauthorized", err)
	}
	if err := g.Authorize(context.Background(), alice, "kv", "get"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("no targeted tenant = %v, want ErrUnauthorized", err)
	}
}

func TestGrantAuthorizer_RecordsEveryEvaluatedSubject(t *testing.T) {
	g := NewGrantAuthorizer(nil)
	if g.Evaluated("alice") {
		t.Fatal("alice reported evaluated before any call")
	}
	_ = g.Authorize(inTenant("t1"), &auth.Identity{Subject: "alice", TenantID: "t1"}, "kv", "get")
	_ = g.Authorize(inTenant("t2"), &auth.Identity{Subject: "bob", TenantID: "t1"}, "kv", "get")
	if !g.Evaluated("alice") || !g.Evaluated("bob") {
		t.Fatal("an evaluated subject was not recorded, allowed or denied")
	}
	if g.Evaluated("carol") {
		t.Fatal("carol reported evaluated without a call")
	}
}
