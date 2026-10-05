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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedSealStore_RequiresSystemTenantSealManagement is SealStore*'s
// ruling (owner security review, item 8): the generic secret store a seal
// strategy keeps for its own state is system-level, ScopeCross, exactly
// like every other node-barrier permission. A tenant-scoped identity --
// even one holding the built-in Admin role of its OWN tenant -- is refused;
// only a system-tenant identity holding seal:store_manage is admitted.
//
// This asserts the DENIAL comes from the authorization decision
// (*rbac.PermissionDeniedError wrapping auth.ErrUnauthorized), not from the
// nil-service fallthrough that would also produce a non-nil error for the
// wrong reason: deleting requireSealStoreManagement's call turns this red
// with *NilServiceError instead.
func TestEmbeddedSealStore_RequiresSystemTenantSealManagement(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme", tenantid.System)
	mw := authmw.New(rbac.New(roles))
	tr := newExtTransport(t, WithAuthMiddleware(mw))

	acmeAdmin := ctxFor(authtest.AdminIdentity("acme", "alice"))
	noIdentity := context.Background()

	assertDenied := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("succeeded, want a permission denial")
		}
		if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrUnauthenticated) {
			return
		}
		t.Fatalf("err = %v (%T); want a denial wrapping ErrUnauthorized/ErrUnauthenticated", err, err)
	}

	assertDenied(t, tr.SealStorePut(acmeAdmin, &transport.SealStorePutRequest{Name: "k", Secret: []byte("v")}))
	assertDenied(t, tr.SealStorePut(noIdentity, &transport.SealStorePutRequest{Name: "k", Secret: []byte("v")}))

	if _, err := tr.SealStoreGet(acmeAdmin, &transport.SealStoreGetRequest{Name: "k"}); true {
		assertDenied(t, err)
	}
	assertDenied(t, tr.SealStoreDelete(acmeAdmin, &transport.SealStoreDeleteRequest{Name: "k"}))
	if _, err := tr.SealStoreList(acmeAdmin); true {
		assertDenied(t, err)
	}
	assertDenied(t, tr.SealStoreReseal(acmeAdmin, &transport.SealStoreResealRequest{Name: "k"}))
	if _, err := tr.SealStoreStatus(acmeAdmin); true {
		assertDenied(t, err)
	}

	// The system tenant's Security Officer holds seal:store_manage (an
	// SO-exclusive grant, like the rest of the node-barrier permission
	// set) and clears the authorization gate; the call then reaches the
	// nil-service fallthrough (no *seal.SealStore injected), which proves
	// the gate admitted it rather than refusing for an unrelated reason.
	systemSO := ctxFor(authtest.SOIdentity(tenantid.System, "root"))
	err := tr.SealStorePut(systemSO, &transport.SealStorePutRequest{Name: "k", Secret: []byte("v")})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("system tenant: err = %v (%T); want *NilServiceError (admitted, then no service configured)", err, err)
	}
}
