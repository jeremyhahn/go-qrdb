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
	"google.golang.org/grpc/metadata"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// SOStartFunc starts the transport under test with mw as its auth
// middleware, gate as its SO dual-factor gate (nil: off) and barrier as its
// barrier service, and returns a client connection to it. It registers its own
// cleanup.
type SOStartFunc func(t *testing.T, mw *authmw.AuthMiddleware, gate *authmw.SOGate, barrier service.BarrierServicer) *grpc.ClientConn

const (
	soTenant      = tenant.SystemTenantID
	soOtherTenant = "acme"
	soPIN         = "correct horse battery staple"
)

type soFixture struct {
	caller  *authtest.FixedAuthenticator
	barrier *authtest.Barrier
	client  pb.QRDBServiceClient
}

func newSOFixture(t *testing.T, start SOStartFunc, gated bool) *soFixture {
	t.Helper()
	caller := authtest.NewFixedAuthenticator(authtest.SOIdentity(soTenant, "officer"))
	roles := authtest.BuiltInRoles(t, soTenant, soOtherTenant)
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	var gate *authmw.SOGate
	if gated {
		gate = authmw.NewSOGate(roles, authtest.SOVerifier(t, soPIN, soTenant, soOtherTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	}
	barrier := &authtest.Barrier{}
	conn := start(t, mw, gate, barrier)
	return &soFixture{caller: caller, barrier: barrier, client: pb.NewQRDBServiceClient(conn)}
}

// rekey issues one BarrierRekey as id, presenting pin in the SO PIN metadata
// entry when pin is non-empty.
func (f *soFixture) rekey(t *testing.T, id *auth.Identity, pin string) error {
	t.Helper()
	f.caller.Set(id)
	ctx := callCtx(t)
	if pin != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, authmw.SOPINMetadataKey, pin)
	}
	_, err := f.client.BarrierRekey(ctx, &pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	return err
}

// SODualFactor is the SO dual-factor conformance suite for the transports that
// serve the QRDBService protobuf API. With the gate on, a barrier rekey
// (seal:rekey, in SOPermissions) needs a caller holding the SO role by
// identifier AND the SO PIN; with it off, the same RBAC-admitted non-SO caller
// is served, which is what makes the gate -- not the authorizer -- the thing
// the gated cases prove.
func SODualFactor(t *testing.T, start SOStartFunc) {
	t.Run("officer with PIN is served", func(t *testing.T) {
		f := newSOFixture(t, start, true)
		if err := f.rekey(t, authtest.SOIdentity(soTenant, "officer"), soPIN); err != nil {
			t.Fatalf("rekey: %v", err)
		}
		if f.barrier.Rekeys() != 1 {
			t.Fatalf("rekeys = %d, want 1", f.barrier.Rekeys())
		}
	})
	t.Run("officer without PIN is refused", func(t *testing.T) {
		f := newSOFixture(t, start, true)
		wantCode(t, "rekey without PIN", f.rekey(t, authtest.SOIdentity(soTenant, "officer"), ""), codes.Unauthenticated)
		if f.barrier.Rekeys() != 0 {
			t.Fatal("a refused rekey reached the service")
		}
	})
	t.Run("officer with wrong PIN is refused", func(t *testing.T) {
		f := newSOFixture(t, start, true)
		wantCode(t, "rekey with wrong PIN", f.rekey(t, authtest.SOIdentity(soTenant, "officer"), "wrong"), codes.Unauthenticated)
		if f.barrier.Rekeys() != 0 {
			t.Fatal("a refused rekey reached the service")
		}
	})
	t.Run("authorized non-SO caller is refused", func(t *testing.T) {
		f := newSOFixture(t, start, true)
		id := authtest.GrantedNonSOIdentity(soTenant, "token", permission.SealRekey)
		wantCode(t, "non-SO rekey", f.rekey(t, id, soPIN), codes.PermissionDenied)
		if f.barrier.Rekeys() != 0 {
			t.Fatal("a refused rekey reached the service")
		}
	})
	t.Run("a tenant's SO is refused on the node barrier", func(t *testing.T) {
		// The node barrier is a platform singleton: an SO of tenant acme,
		// presenting acme's own valid PIN, must not rekey it (go-qrdb#569 N2).
		f := newSOFixture(t, start, true)
		wantCode(t, "tenant SO rekey", f.rekey(t, authtest.SOIdentity(soOtherTenant, "acme-officer"), soPIN), codes.PermissionDenied)
		if f.barrier.Rekeys() != 0 {
			t.Fatal("a tenant SO rekeyed the node barrier")
		}
	})
	t.Run("gate off serves the same non-SO caller", func(t *testing.T) {
		f := newSOFixture(t, start, false)
		id := authtest.GrantedNonSOIdentity(soTenant, "token", permission.SealRekey)
		if err := f.rekey(t, id, ""); err != nil {
			t.Fatalf("rekey with the gate off: %v", err)
		}
		if f.barrier.Rekeys() != 1 {
			t.Fatalf("rekeys = %d, want 1", f.barrier.Rekeys())
		}
	})
}
