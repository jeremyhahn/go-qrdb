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

package grpctest

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// NodeOfficerScope is the node-officer scope conformance suite for the
// transports that serve the QRDBService protobuf API. The node barrier's
// share-holder registry -- unseal-domain registration, recovery-domain
// registration, the holder list and revocation -- belongs to the platform:
// every rekey, revocation and recovery-key generation hands each registered
// holder a share of the key that protects every tenant. Only the system
// tenant may register, list or revoke those holders. The SO gate is off, so
// the refusals are the authorizer's.
// nodeOfficerSOPIN is the barrier SO PIN a revocation in NodeOfficerScope
// presents; the barrier service checks it.
const nodeOfficerSOPIN = "so-pin-1234"

func NodeOfficerScope(t *testing.T, start SOStartFunc) {
	ops := []struct {
		name string
		call func(c pb.QRDBServiceClient, t *testing.T) error
		hits func(b *authtest.Barrier) int64
	}{
		{"officer register", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.BarrierOfficerRegister(callCtx(t), &pb.BarrierOfficerRegisterRequest{Username: "planted", CsrPem: []byte("csr")})
			return err
		}, (*authtest.Barrier).OfficerRegistrations},
		{"recovery officer register", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.BarrierRecoveryOfficerRegister(callCtx(t), &pb.BarrierOfficerRegisterRequest{Username: "planted", CsrPem: []byte("csr")})
			return err
		}, (*authtest.Barrier).RecoveryOfficerRegistrations},
		{"officer list", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.BarrierOfficerList(callCtx(t), &emptypb.Empty{})
			return err
		}, (*authtest.Barrier).OfficerReads},
		{"officer revoke", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.BarrierOfficerRevoke(callCtx(t), &pb.BarrierOfficerRevokeRequest{Domain: "unseal", Username: "bob", SoPin: nodeOfficerSOPIN})
			return err
		}, (*authtest.Barrier).Revocations},
	}
	for _, op := range ops {
		roles := authtest.BuiltInRoles(t, tenantid.System, authtest.NodeOfficerTenant)
		for _, c := range authtest.NodeOfficerCallers(t, roles) {
			t.Run(op.name+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				pins := authtest.BarrierPINBackend(t, nodeOfficerSOPIN)
				barrier := &authtest.Barrier{PINs: pins}
				conn := start(t, authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller)), nil, pins, barrier)
				err := op.call(pb.NewQRDBServiceClient(conn), t)
				if c.Admitted {
					if err != nil || op.hits(barrier) != 1 {
						t.Fatalf("err=%v, calls=%d; want served once", err, op.hits(barrier))
					}
					return
				}
				wantCode(t, op.name, err, codes.PermissionDenied)
				if op.hits(barrier) != 0 {
					t.Fatalf("a refused %s reached the node barrier", op.name)
				}
			})
		}
	}
}
