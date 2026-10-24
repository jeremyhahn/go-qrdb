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

package grpc

import (
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// TestGRPCRoundTrip_DatabaseHolders runs the database share-holder suite
// through the SDK gRPC client against the real QRDBService handlers over a
// real database service. The connection carries no client certificate, so
// its claim is refused.
func TestGRPCRoundTrip_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	// A holder's quorum submission carries a barrier secret: it is sent only
	// over TLS. The client presents no certificate, so the claim refusals
	// below are the server's answer to a caller without the holder's.
	tt := newAnonymousTestTLS(t)
	srv := googlegrpc.NewServer(tt.serverOptions()...)
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterDatabaseService(fx.Service))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := googlegrpc.NewClient(lis.Addr().String(), tt.dialOption())
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tr := &Transport{config: tt.config(lis.Addr().String()), conn: conn, client: pb.NewQRDBServiceClient(conn)}
	dbholdertest.Run(t, tr, fx, dbholdertest.ClaimRefusedWithStatus(int(codes.Unauthenticated)))
}
