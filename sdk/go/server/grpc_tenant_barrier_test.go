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

package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// sealRecordingTenantSvc is a ServiceTenantServicer whose only implemented
// method is TenantBarrierSeal; it records the tenant it sealed. Any other
// method panics through the nil embedded interface, so a test that reaches
// one fails loudly.
type sealRecordingTenantSvc struct {
	sdk.ServiceTenantServicer
	sealed atomic.Pointer[string]
}

func (s *sealRecordingTenantSvc) TenantBarrierSeal(_ context.Context, tenantID string) error {
	s.sealed.Store(&tenantID)
	return nil
}

// dialSDKRegisteredServer serves a *grpc.Server built only through
// RegisterGRPCService, as a composer with its own listener builds one, and
// returns a TenantBarrierService client to it.
func dialSDKRegisteredServer(t *testing.T, opts ...GRPCRegisterOption) pb.TenantBarrierServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterGRPCService(srv, nil, nil, opts...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewTenantBarrierServiceClient(conn)
}

func TestRegisterGRPCService_WithTenantBarrierService_Serves(t *testing.T) {
	svc := &sealRecordingTenantSvc{}
	client := dialSDKRegisteredServer(t,
		WithRegisterTenantBarrierService(svc),
		WithRegisterAuthMiddleware(nil))

	if _, err := client.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got := svc.sealed.Load(); got == nil || *got != "acme" {
		t.Fatalf("sealed tenant = %v; want acme", got)
	}
}

func TestRegisterGRPCService_WithoutTenantBarrierService_Unimplemented(t *testing.T) {
	client := dialSDKRegisteredServer(t)

	_, err := client.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"})
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("Seal code = %s; want Unimplemented (err %v)", got, err)
	}
}
