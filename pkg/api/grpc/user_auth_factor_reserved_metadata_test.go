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

// gRPC half of the go-qrdb#234 cross-transport verification. The Unix-socket
// transport registers this same servicer (pkg/api/unix/server.go calls
// grpcpkg.RegisterUserAuthFactorService), so it is covered by these too.
//
// Driven against the REAL service over a real loopback connection: stubbing
// the service would assert only that the stub returned what it was told to.
package grpc

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// startRealFactorServer registers the PRODUCTION servicer over a real
// loopback gRPC connection, backed by the REAL user auth-factor service.
//
// It builds its own server rather than borrowing startServerWithUserAuthFactor
// from grpc_coverage2_test.go: that helper is constrained to
// "!integration && !qrdb_ts", and a helper's build constraint must be a
// superset of every caller's (go-qrdb#178). newMockKVService is unconstrained,
// so it is safe to use; the server harness is the part that had to be local.
func startRealFactorServer(t *testing.T) (*grpc.ClientConn, service.UserAuthFactorServicer) {
	t.Helper()

	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV: newMockKVService(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	RegisterUserAuthFactorService(srv, svc, nil)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
	})
	return conn, svc
}

// assertReservedRejectedRPC checks the RPC failed with InvalidArgument and that
// the message names the key and the reserved prefix. Before #234 the metadata
// was copied verbatim and the RPC succeeded.
func assertReservedRejectedRPC(t *testing.T, err error, wantKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("RPC succeeded with a server-owned metadata key present")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %s, want %s (a fall-through to Internal is the pre-#234 shape): %v",
			st.Code(), codes.InvalidArgument, err)
	}
	if !strings.Contains(st.Message(), wantKey) {
		t.Errorf("message does not name the offending key %q: %s", wantKey, st.Message())
	}
	if !strings.Contains(st.Message(), auth.ReservedMetadataPrefix) {
		t.Errorf("message does not name the reserved prefix: %s", st.Message())
	}
}

func TestGRPC_AddRefusesReservedMetadata(t *testing.T) {
	conn, _ := startRealFactorServer(t)

	_, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"attest_level": "hardware"},
	})
	assertReservedRejectedRPC(t, err, "attest_level")
}

func TestGRPC_RotateRefusesReservedMetadata(t *testing.T) {
	conn, _ := startRealFactorServer(t)
	client := pb.NewUserAuthFactorServiceClient(conn)

	seed, err := client.Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId: 42, Type: "mtls", Credential: []byte("cred-old"),
	})
	if err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	_, err = client.Rotate(context.Background(), &pb.UserAuthFactorRotateRequest{
		OldFactorId: seed.Factor.Id,
		NewFactor: &pb.UserAuthFactorNewFactor{
			Type:       "mtls",
			Credential: []byte("cred-new"),
			Metadata:   map[string]string{"attest_aaguid": "forged"},
		},
	})
	assertReservedRejectedRPC(t, err, "attest_aaguid")
}

// TestGRPC_AddAllowsOrdinaryMetadata is the control: a namespace, not a ban.
func TestGRPC_AddAllowsOrdinaryMetadata(t *testing.T) {
	conn, _ := startRealFactorServer(t)

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"vendor": "yubico"},
	})
	if err != nil {
		t.Fatalf("Add with ordinary metadata: %v", err)
	}
	if resp.Factor.Metadata["vendor"] != "yubico" {
		t.Errorf("ordinary metadata not round-tripped: %v", resp.Factor.Metadata)
	}
}
