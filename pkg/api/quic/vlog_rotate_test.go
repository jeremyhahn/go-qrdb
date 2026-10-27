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

//go:build qrdb_vlog

package quic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// TestVlogRotateKey_RotatesTheRealLog drives vlog.RotateKey through the
// method gate against the real LogService: an Admin is refused, an Operator
// rotates, the checkpoint signed after names the new key, and the one signed
// before still verifies under the key it named.
func TestVlogRotateKey_RotatesTheRealLog(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	svc := servicetest.NewLogService(t)
	key1 := servicetest.SeedSignedLog(t, svc, "acme", "ledger", "audit")
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(svc),
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))))
	call := func(roleName, method, payload string, out any) error {
		t.Helper()
		id := &auth.Identity{Subject: roleName + "-caller", TenantID: "acme", Roles: []string{role.SystemRoleID("acme", roleName)}}
		ctx := tenant.WithDatabase(tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme"), "ledger")
		if err := srv.authorize(ctx, method); err != nil {
			return err
		}
		b, err := vlogCall(t, srv, ctx, method, []byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: decode: %v", method, err)
			}
		}
		return nil
	}
	sign := func() []byte {
		t.Helper()
		var res vlogwire.SignHeadResult
		if err := call(role.RoleNameOperator, "vlog.SignHead", `{"log":"audit"}`, &res); err != nil {
			t.Fatalf("vlog.SignHead: %v", err)
		}
		return res.Checkpoint
	}
	const rotate = `{"log":"audit","signing_key_type":"ed25519"}`

	before := sign()
	if err := call(role.RoleNameAdmin, "vlog.RotateKey", rotate, nil); err == nil {
		t.Fatal("Admin rotation was admitted")
	}
	var rotated vlogwire.RotateKeyResult
	if err := call(role.RoleNameOperator, "vlog.RotateKey", rotate, &rotated); err != nil {
		t.Fatalf("Operator rotation: %v", err)
	}
	if rotated.Key.Version != key1.Version+1 {
		t.Fatalf("rotated key version = %d, want %d", rotated.Key.Version, key1.Version+1)
	}
	if err := call(role.RoleNameOperator, "vlog.Append", `{"log":"audit","entries":["c2Vjb25k"]}`, nil); err != nil {
		t.Fatalf("vlog.Append: %v", err)
	}
	after := sign()

	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, after, vlog.HeadKey{Version: rotated.Key.Version, PublicKey: rotated.Key.PublicKey})
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, before, key1)
}
