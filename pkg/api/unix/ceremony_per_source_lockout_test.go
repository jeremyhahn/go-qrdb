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

//go:build !integration

package unix

import (
	"context"
	"crypto/tls"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestUnix_CeremonyInitialize_PerSourceLockoutDoesNotCrossToTCPGRPC is the
// go-qrdb#560 review C acceptance test for the Unix socket transport. Unix
// domain sockets carry no routable per-connection remote address -- Go's
// net package reports the constant pseudo-address "@" for every unnamed
// client socket, so unlike REST, gRPC-over-TCP, QUIC and MCP, two DIFFERENT
// Unix connections cannot be told apart by address at all: they
// necessarily share ONE "addr:@" principal and therefore one lockout
// counter, same as before go-qrdb#560 review C. That is not a regression --
// there is no stronger per-source signal this transport can offer -- so the
// property this transport CAN demonstrate, and this test does, is that its
// one shared "addr:@" bucket is still its OWN bucket: a lockout reached by
// guessing over Unix does not also lock out a caller reaching the IDENTICAL
// underlying service over TCP gRPC (its own distinct "addr:<ip>:<port>"
// principal). Before go-qrdb#560 review C, Unix and gRPC callers shared the
// SAME "" anonymous bucket, so this cross-transport isolation did not hold
// either.
func TestUnix_CeremonyInitialize_PerSourceLockoutDoesNotCrossToTCPGRPC(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")

	// The Unix listener.
	socketPath := tempSocketPath(t)
	unixTLS := selfSignedTLSConfig(t)
	unixSrv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(unixTLS),
		WithCeremonyService(wrapped), WithCeremonyCA(ca))
	if err := unixSrv.Start(); err != nil {
		t.Fatalf("unix Start failed: %v", err)
	}
	defer unixSrv.Stop()
	unixConn := dialUnixTLS(t, socketPath, unixTLS)
	defer func() { _ = unixConn.Close() }()
	unixClient := pb.NewQRDBServiceClient(unixConn)

	// A TCP gRPC listener sharing the SAME ceremony/lockout stack.
	grpcTLS := selfSignedTLSConfig(t)
	grpcSrv := grpcpkg.NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0",
		grpcpkg.WithTLS(grpcTLS), grpcpkg.WithCeremonyService(wrapped), grpcpkg.WithCeremonyCA(ca))
	if err := grpcSrv.Start(); err != nil {
		t.Fatalf("grpc Start failed: %v", err)
	}
	defer grpcSrv.Stop()
	grpcClientTLS := &tls.Config{RootCAs: grpcTLS.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	grpcConn, err := grpc.NewClient(grpcSrv.Addr(), grpc.WithTransportCredentials(grpccredentials.NewTLS(grpcClientTLS)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = grpcConn.Close() }()
	grpcClient := pb.NewQRDBServiceClient(grpcConn)

	// Drive the Unix connection past the free-failure budget.
	var lastErr error
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		_, lastErr = unixClient.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
		if st, _ := status.FromError(lastErr); st.Code() != codes.Unauthenticated {
			t.Fatalf("unix attempt %d: code = %s, want Unauthenticated", i+1, st.Code())
		}
	}
	_, lockErr := unixClient.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
	if st, _ := status.FromError(lockErr); st.Code() != codes.ResourceExhausted {
		t.Fatalf("unix after %d wrong guesses: code = %s, want ResourceExhausted", auth.SOPINFreeFailures+1, st.Code())
	}

	// The TCP gRPC caller, a DIFFERENT source, presents the identical wrong
	// PIN immediately afterward: it must see the ordinary Unauthenticated
	// refusal, never Unix's lockout.
	_, grpcErr := grpcClient.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
	if st, _ := status.FromError(grpcErr); st.Code() != codes.Unauthenticated {
		t.Fatalf("grpc: code = %s, want Unauthenticated; unix's lockout leaked to grpc", st.Code())
	}

	// And the gRPC caller can still succeed with the correct PIN while Unix
	// remains locked.
	_, grpcOKErr := grpcClient.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{SoPin: "correct-horse-battery", AdminGenerate: true})
	if grpcOKErr != nil {
		t.Fatalf("grpc with the correct PIN: unexpected error: %v", grpcOKErr)
	}
}
