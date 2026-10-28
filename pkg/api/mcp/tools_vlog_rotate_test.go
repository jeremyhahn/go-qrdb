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

package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// TestVlogRotateKey_RotatesTheRealLog calls qrdb_vlog_rotate_key over a
// started MCP server against the real LogService: an Admin is refused, an
// Operator rotates, the checkpoint signed after names the new key, and the one
// signed before still verifies under the key it named.
func TestVlogRotateKey_RotatesTheRealLog(t *testing.T) {
	svc := servicetest.NewLogService(t)
	key1 := servicetest.SeedSignedLog(t, svc, "acme", tenant.DefaultDatabase, "audit")
	f := newVlogFixtureOver(t, svc)
	f.as(roleIdentity("acme", role.RoleNameOperator))
	sign := func() []byte {
		t.Helper()
		var signed vlogwire.SignHeadResult
		vlogDecode(t, f.call(t, "qrdb_vlog_sign_head", map[string]any{"log": "audit"}), nil, &signed)
		return signed.Checkpoint
	}
	rotate := map[string]any{"log": "audit", "signing_key_type": "ed25519"}

	before := sign()
	f.as(roleIdentity("acme", role.RoleNameAdmin))
	wantToolCode(t, "Admin rotation", f.call(t, "qrdb_vlog_rotate_key", rotate), wire.CodePermissionDenied)
	f.as(roleIdentity("acme", role.RoleNameOperator))
	var rotated vlogwire.RotateKeyResult
	vlogDecode(t, f.call(t, "qrdb_vlog_rotate_key", rotate), nil, &rotated)
	if rotated.Key.Version != key1.Version+1 {
		t.Fatalf("rotated key version = %d, want %d", rotated.Key.Version, key1.Version+1)
	}
	wantOK(t, "append", f.call(t, "qrdb_vlog_append", map[string]any{"log": "audit", "entries": []any{"c2Vjb25k"}}))
	after := sign()

	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, after, vlog.HeadKey{Version: rotated.Key.Version, PublicKey: rotated.Key.PublicKey})
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, before, key1)
}
