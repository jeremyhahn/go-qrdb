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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestRoleSeederAdapter_ReconcileBuiltInRoles_Succeeds proves the adapter
// namespaces the shared store to the tenant's identity namespace and
// seeds the built-in roles there -- the same namespace tenant.StoreResolver
// confines every other prefix-tenant IAM read/write to.
func TestRoleSeederAdapter_ReconcileBuiltInRoles_Succeeds(t *testing.T) {
	shared := kvstore.NewMemoryKVStore()
	a := roleSeederAdapter{shared: shared}

	if err := a.ReconcileBuiltInRoles(context.Background(), "acme"); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	keys, err := shared.List(context.Background(), "acme/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) == 0 {
		t.Error("ReconcileBuiltInRoles wrote nothing under the tenant's identity namespace")
	}
}

// TestRoleSeederAdapter_ReconcileBuiltInRoles_NilSharedStorePropagatesError
// proves a nil shared store is reported as the namespaced store's own
// typed refusal, rather than panicking.
func TestRoleSeederAdapter_ReconcileBuiltInRoles_NilSharedStorePropagatesError(t *testing.T) {
	a := roleSeederAdapter{shared: nil}

	err := a.ReconcileBuiltInRoles(context.Background(), "acme")
	var nilInner *kvstore.NilInnerStoreError
	if !errors.As(err, &nilInner) {
		t.Fatalf("ReconcileBuiltInRoles with a nil shared store = %v (%T), want *kvstore.NilInnerStoreError", err, err)
	}
}
