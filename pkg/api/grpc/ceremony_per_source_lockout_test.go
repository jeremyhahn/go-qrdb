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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestGRPC_CeremonyInitialize_PerSourceLockoutDoesNotCrossConnections is
// the go-qrdb#560 review C acceptance test for gRPC: two REAL, independent
// client connections to the same server -- distinct source ports, so
// injectRemoteAddr's auth.WithRemoteAddr records a distinct
// "addr:<remote>" principal for each (go-qrdb#560 review C) -- and a wrong
// SO PIN repeated past auth.SOPINFreeFailures on connection A locks out
// ONLY A's principal; connection B, presenting the identical wrong PIN
// immediately afterward, still gets the ordinary Unauthenticated refusal,
// never ResourceExhausted. Before go-qrdb#560 review C, every
// unauthenticated gRPC caller shared ONE anonymous lockout bucket, so B
// would have been locked out too, by a guess it never made. The Unix
// socket transport shares this same qrdbServiceServer and interceptor
// chain, so this also exercises Unix's gate logic (its own peer address
// has no equivalent per-connection granularity -- see
// pkg/api/unix's sibling test for what IS testable there).
func TestGRPC_CeremonyInitialize_PerSourceLockoutDoesNotCrossConnections(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithTLS(tlsCfg), WithCeremonyService(wrapped), WithCeremonyCA(ca))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	dial := func() pb.QRDBServiceClient {
		t.Helper()
		conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
		if err != nil {
			t.Fatalf("grpc.NewClient: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return pb.NewQRDBServiceClient(conn)
	}

	clientA := dial()
	clientB := dial()

	ceremonyCall := func(client pb.QRDBServiceClient, soPIN string) codes.Code {
		t.Helper()
		_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{SoPin: soPIN, AdminGenerate: true})
		if err == nil {
			return codes.OK
		}
		st, _ := status.FromError(err)
		return st.Code()
	}

	// Drive connection A past the free-failure budget.
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		if code := ceremonyCall(clientA, "wrong-pin"); code != codes.Unauthenticated {
			t.Fatalf("connection A, attempt %d: code = %s, want Unauthenticated", i+1, code)
		}
	}
	// One more wrong guess on A: now locked out (ResourceExhausted).
	if code := ceremonyCall(clientA, "wrong-pin"); code != codes.ResourceExhausted {
		t.Fatalf("connection A after %d wrong guesses: code = %s, want ResourceExhausted", auth.SOPINFreeFailures+1, code)
	}

	// Connection B, a DIFFERENT source, presents the identical wrong PIN
	// immediately afterward: it must see the ordinary Unauthenticated
	// refusal, never A's lockout.
	if code := ceremonyCall(clientB, "wrong-pin"); code != codes.Unauthenticated {
		t.Fatalf("connection B: code = %s, want Unauthenticated; A's lockout leaked to B", code)
	}

	// And B can still succeed with the correct PIN while A remains locked.
	if code := ceremonyCall(clientB, "correct-horse-battery"); code != codes.OK {
		t.Fatalf("connection B with the correct PIN: code = %s, want OK", code)
	}
}
