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

package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestVlogRotateKey_RotatesTheRealLog drives key rotation through the
// authorization interceptor against the real LogService: an Admin is refused,
// an Operator rotates, the checkpoint signed after names the new key and
// verifies under it alone, and the one signed before still verifies under the
// key it named.
func TestVlogRotateKey_RotatesTheRealLog(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogTestTenant)
	svc := servicetest.NewLogService(t)
	key1 := servicetest.SeedSignedLog(t, svc, vlogTestTenant, "ledger", "audit")
	call := func(roleName, method string, req any) (any, error) {
		t.Helper()
		return invokeVlog(t, roles, roleName, vlogMethodNamed(t, method, req), svc)
	}
	sign := func() []byte {
		t.Helper()
		resp, err := call(role.RoleNameOperator, "VlogSignHead", &pb.VlogSignHeadRequest{Log: "audit"})
		if err != nil {
			t.Fatalf("VlogSignHead: %v", err)
		}
		return resp.(*pb.VlogSignHeadResponse).GetCheckpoint()
	}
	rotate := &pb.VlogRotateKeyRequest{Log: "audit", SigningKeyType: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519}

	before := sign()
	if _, err := call(role.RoleNameAdmin, "VlogRotateKey", rotate); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Admin rotation: err = %v, want PermissionDenied", err)
	}
	resp, err := call(role.RoleNameOperator, "VlogRotateKey", rotate)
	if err != nil {
		t.Fatalf("Operator rotation: %v", err)
	}
	k := resp.(*pb.VlogRotateKeyResponse).GetKey()
	key2 := vlog.HeadKey{Version: k.GetVersion(), Ref: k.GetRef(), PublicKey: k.GetPublicKey()}
	if key2.Version != key1.Version+1 {
		t.Fatalf("rotated key version = %d, want %d", key2.Version, key1.Version+1)
	}
	if _, err := call(role.RoleNameOperator, "VlogAppend", &pb.VlogAppendRequest{Log: "audit", Entries: [][]byte{[]byte("second")}}); err != nil {
		t.Fatalf("VlogAppend: %v", err)
	}
	after := sign()

	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, after, key2)
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, before, key1)
	c, err := vlog.ParseCheckpoint(after)
	if err != nil {
		t.Fatalf("parse checkpoint: %v", err)
	}
	head := c.Head(c.Signatures[0], key1.Version, vlog.SignatureEd25519)
	if servicetest.VerifyHead(servicetest.LogOrigin, head, key1) == nil {
		t.Fatal("the checkpoint signed after rotation verifies under the retired key")
	}
}
