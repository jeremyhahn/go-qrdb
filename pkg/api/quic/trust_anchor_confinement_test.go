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

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// TestQUICTrustAnchors_AnotherTenantIsRefusedWithOrWithoutTheHandlerGate:
// add, list and remove on another tenant's anchors are refused by the
// handler's tenancy gate and, with no middleware at all, by the service.
func TestQUICTrustAnchors_AnotherTenantIsRefusedWithOrWithoutTheHandlerGate(t *testing.T) {
	for _, withMW := range []bool{true, false} {
		anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
		opts := []ServerOption{WithCertEnrollment(service.NewCertEnrollmentService(nil, nil, anchors))}
		if withMW {
			roles := &mockRoleService{resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
				return []permission.Permission{permission.TrustAnchorsAdd, permission.TrustAnchorsList, permission.TrustAnchorsRemove}, nil
			}}
			opts = append(opts, WithAuthMiddleware(authmw.New(rbac.New(roles))))
		}
		srv := NewServer(nil, nil, "localhost:0", opts...)

		globexPEM, err := certtest.NewCAPEM("globex-root")
		if err != nil {
			t.Fatal(err)
		}
		seeded, err := anchors.Add(context.Background(), "globex", globexPEM)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		rogue, err := certtest.NewCAPEM("rogue")
		if err != nil {
			t.Fatal(err)
		}
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin"}}), "acme")

		calls := map[string]func() error{
			"add": func() error {
				p, _ := proto.Marshal(&pb.TrustAnchorAddRequest{TenantId: "globex", CertsPem: string(rogue)})
				_, err := srv.handleTrustAnchorAdd(ctx, p)
				return err
			},
			"list": func() error {
				p, _ := proto.Marshal(&pb.TrustAnchorListRequest{TenantId: "globex"})
				_, err := srv.handleTrustAnchorList(ctx, p)
				return err
			},
			"remove": func() error {
				p, _ := proto.Marshal(&pb.TrustAnchorRemoveRequest{TenantId: "globex", Fingerprint: seeded[0].Fingerprint})
				_, err := srv.handleTrustAnchorRemove(ctx, p)
				return err
			},
		}
		for name, call := range calls {
			err := call()
			if err == nil {
				t.Fatalf("middleware=%v %s: tenant acme acted on tenant globex's anchors", withMW, name)
			}
			var denied *tenant.CrossTenantDeniedError
			if !withMW && !errors.As(err, &denied) {
				t.Fatalf("middleware=false %s: err = %v, want the service's *tenant.CrossTenantDeniedError", name, err)
			}
			if withMW && !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("middleware=true %s: err = %v, want the gate's authorization refusal", name, err)
			}
		}
		left, err := anchors.List(context.Background(), "globex")
		if err != nil || len(left) != 1 || left[0].Fingerprint != seeded[0].Fingerprint {
			t.Fatalf("middleware=%v: globex's anchors = %v, %v; want only the seeded one", withMW, left, err)
		}
	}
}
