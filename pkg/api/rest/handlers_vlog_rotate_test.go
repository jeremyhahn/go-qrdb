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

package rest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// TestVlogRotateKey_RotatesTheRealLog drives POST /vlog/key/rotate through
// the real middleware and RBAC against the real LogService: an Admin is 403,
// an Operator rotates, the checkpoint signed after names the new key, and the
// one signed before still verifies under the key it named.
func TestVlogRotateKey_RotatesTheRealLog(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogRESTTenant)
	svc := servicetest.NewLogService(t)
	key1 := servicetest.SeedSignedLog(t, svc, vlogRESTTenant, "ledger", "audit")
	do := func(roleName, method, target, body string, out any) int {
		t.Helper()
		rec := serveVlog(t, roles, roleName, vlogRoute{method: method, target: target, body: body}, svc)
		if rec.Code == http.StatusOK && out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("%s %s: decode %s: %v", method, target, rec.Body.String(), err)
			}
		}
		return rec.Code
	}
	sign := func() []byte {
		t.Helper()
		var resp vlogwire.SignHeadResult
		if code := do(role.RoleNameOperator, http.MethodPost, "/vlog/head/sign", `{"log":"audit"}`, &resp); code != http.StatusOK {
			t.Fatalf("sign head: status %d", code)
		}
		return resp.Checkpoint
	}
	const rotate = `{"log":"audit","signing_key_type":"ed25519"}`

	before := sign()
	if code := do(role.RoleNameAdmin, http.MethodPost, "/vlog/key/rotate", rotate, nil); code != http.StatusForbidden {
		t.Fatalf("Admin rotation: status %d, want 403", code)
	}
	var rotated vlogwire.RotateKeyResult
	if code := do(role.RoleNameOperator, http.MethodPost, "/vlog/key/rotate", rotate, &rotated); code != http.StatusOK {
		t.Fatalf("Operator rotation: status %d", code)
	}
	if rotated.Key.Version != key1.Version+1 {
		t.Fatalf("rotated key version = %d, want %d", rotated.Key.Version, key1.Version+1)
	}
	if code := do(role.RoleNameOperator, http.MethodPost, "/vlog/append", `{"log":"audit","entries":["c2Vjb25k"]}`, nil); code != http.StatusOK {
		t.Fatalf("append: status %d", code)
	}
	after := sign()

	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, after, vlog.HeadKey{Version: rotated.Key.Version, PublicKey: rotated.Key.PublicKey})
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, before, key1)
}

// TestVlogRotateKey_RefusesAnUnnamedKeyType checks the real service refuses a
// rotation naming no key type, or one outside RFC 9162, as a bad request.
func TestVlogRotateKey_RefusesAnUnnamedKeyType(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogRESTTenant)
	svc := servicetest.NewLogService(t)
	servicetest.SeedSignedLog(t, svc, vlogRESTTenant, "ledger", "audit")
	for _, body := range []string{`{"log":"audit"}`, `{"log":"audit","signing_key_type":"rsa"}`} {
		rt := vlogRoute{method: http.MethodPost, target: "/vlog/key/rotate", body: body}
		if rec := serveVlog(t, roles, role.RoleNameOperator, rt, svc); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
	}
}
