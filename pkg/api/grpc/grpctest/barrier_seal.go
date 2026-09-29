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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// SealStartFunc starts the transport under test with mw as its auth
// middleware, pins as its barrier PIN backend and barrier as its barrier
// service, and returns a client connection to it.
type SealStartFunc func(t *testing.T, mw *authmw.AuthMiddleware, pins pin.PINBackend, barrier service.BarrierServicer) *grpc.ClientConn

const barrierSealMethod = "/qrdb.v1.QRDBService/BarrierSeal"

// sealRequest is the BarrierSeal request. It is built from well-known types so
// the suite reads the same against the request message and against the
// google.protobuf.Empty it replaced: field 1, a string, is so_pin.
func sealRequest(soPIN string) proto.Message {
	if soPIN == "" {
		return &emptypb.Empty{}
	}
	return wrapperspb.String(soPIN)
}

// BarrierSeal is the conformance suite for sealing the NODE barrier over the
// transports that serve the QRDBService protobuf API:
//
//   - with a barrier SO PIN configured, a seal must present it, as REST and
//     MCP already required -- gRPC and the Unix socket took Empty and could
//     not carry it, so the configured PIN was skipped;
//   - the node barrier is platform scope, so a tenant's Admin may not seal it.
func BarrierSeal(t *testing.T, start SealStartFunc) {
	run := func(t *testing.T, id *auth.Identity, soPIN string) (*authtest.Barrier, error) {
		t.Helper()
		caller := authtest.NewFixedAuthenticator(id)
		roles := authtest.BuiltInRoles(t, tenantid.System, soOtherTenant)
		mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
		barrier := &authtest.Barrier{}
		conn := start(t, mw, authtest.BarrierPINBackend(t, soPIN0), barrier)
		err := conn.Invoke(callCtx(t), barrierSealMethod, sealRequest(soPIN), &emptypb.Empty{})
		return barrier, err
	}
	operator := authtest.AdminIdentity(tenantid.System, "root")

	t.Run("seal with the barrier SO PIN is served", func(t *testing.T) {
		b, err := run(t, operator, soPIN0)
		if err != nil || b.Seals() != 1 {
			t.Fatalf("seal with the PIN: err=%v seals=%d", err, b.Seals())
		}
	})
	t.Run("seal without the barrier SO PIN is refused", func(t *testing.T) {
		b, err := run(t, operator, "")
		if b.Seals() != 0 {
			t.Fatal("the node barrier was sealed without the configured SO PIN")
		}
		wantCode(t, "seal without PIN", err, codes.Unauthenticated)
	})
	t.Run("seal with a wrong barrier SO PIN is refused", func(t *testing.T) {
		b, err := run(t, operator, "wrong")
		if b.Seals() != 0 {
			t.Fatal("the node barrier was sealed with a wrong SO PIN")
		}
		wantCode(t, "seal with wrong PIN", err, codes.Unauthenticated)
	})
	t.Run("a tenant's Admin may not seal the node barrier", func(t *testing.T) {
		b, err := run(t, authtest.AdminIdentity(soOtherTenant, "acme-admin"), soPIN0)
		if b.Seals() != 0 {
			t.Fatal("a tenant Admin sealed the node barrier")
		}
		wantCode(t, "tenant admin seal", err, codes.PermissionDenied)
	})
}

// soPIN0 is the barrier SO PIN the suite configures.
const soPIN0 = "barrier-so-pin-1234"
