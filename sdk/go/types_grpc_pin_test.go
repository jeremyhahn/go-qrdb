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

package sdk

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
)

// newSOPINGatedClient registers the qrdb gRPC service through the SDK surface
// only, over an in-memory bufconn listener, with an initialized and unsealed
// barrier service and the given extra options, and returns a client for it
// along with the barrier service it serves.
func newSOPINGatedClient(t *testing.T, opts ...GRPCRegisterOption) (pb.QRDBServiceClient, BarrierServicer) {
	t.Helper()
	barrierSvc := NewBarrierService(newTestBarrier(t))
	if err := barrierSvc.BarrierInitialize(t.Context(), []byte("sdk-grpc-pin-passphrase")); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterGRPCService(srv, nil, nil, append([]GRPCRegisterOption{WithRegisterBarrierService(barrierSvc)}, opts...)...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewQRDBServiceClient(conn), barrierSvc
}

// requireCode fails the test unless err is a gRPC status carrying want.
func requireCode(t *testing.T, op string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("%s: code = %v (%v), want %v", op, got, err, want)
	}
}

// TestWithRegisterPINBackend_BarrierSealGatedOnSOPIN proves the SDK re-export
// reaches the handler: with the option, BarrierSeal accepts the SO PIN and
// refuses a wrong one as Unauthenticated.
func TestWithRegisterPINBackend_BarrierSealGatedOnSOPIN(t *testing.T) {
	client, _ := newSOPINGatedClient(t, WithRegisterPINBackend(pintest.NewSOPINBackend(t)))

	_, err := client.BarrierSeal(t.Context(), &pb.BarrierSealRequest{SoPin: "wrong-so-pin"})
	requireCode(t, "BarrierSeal with a wrong SO PIN", err, codes.Unauthenticated)

	if _, err := client.BarrierSeal(t.Context(), &pb.BarrierSealRequest{SoPin: pintest.ThrottledSOPIN}); err != nil {
		t.Fatalf("BarrierSeal with the SO PIN: %v", err)
	}
}

// TestWithRegisterPINBackend_BarrierSealFailsClosedWithout proves that without
// the option BarrierSeal refuses every caller, even one presenting a PIN, with
// the FailedPrecondition status pin.ErrBackendNotConfigured maps to.
func TestWithRegisterPINBackend_BarrierSealFailsClosedWithout(t *testing.T) {
	client, _ := newSOPINGatedClient(t)

	_, err := client.BarrierSeal(t.Context(), &pb.BarrierSealRequest{SoPin: pintest.ThrottledSOPIN})
	requireCode(t, "BarrierSeal without a PIN backend", err, codes.FailedPrecondition)
}

// TestWithRegisterPINBackend_RecoveryGenerateGatedOnSOPIN covers the second
// SO-PIN-gated RPC: a wrong SO PIN is Unauthenticated, and the SO PIN passes
// the gate and reaches the barrier service. A passphrase barrier has no
// ceremony distributor, so the service itself refuses to generate; the RPC
// must return exactly that refusal, not a PIN-gate status.
func TestWithRegisterPINBackend_RecoveryGenerateGatedOnSOPIN(t *testing.T) {
	client, barrierSvc := newSOPINGatedClient(t, WithRegisterPINBackend(pintest.NewSOPINBackend(t)))

	_, err := client.BarrierRecoveryGenerate(t.Context(), &pb.BarrierRecoveryGenerateRequest{SoPin: "wrong-so-pin", Threshold: 2})
	requireCode(t, "BarrierRecoveryGenerate with a wrong SO PIN", err, codes.Unauthenticated)

	_, svcErr := barrierSvc.BarrierGenerateRecoveryKeys(t.Context(), 2, "")
	if svcErr == nil {
		t.Fatal("BarrierGenerateRecoveryKeys on a passphrase barrier succeeded; the test's premise is stale")
	}
	_, err = client.BarrierRecoveryGenerate(t.Context(), &pb.BarrierRecoveryGenerateRequest{SoPin: pintest.ThrottledSOPIN, Threshold: 2})
	if got := status.Convert(err).Message(); got != svcErr.Error() {
		t.Fatalf("BarrierRecoveryGenerate with the SO PIN = %v, want the barrier service's own error %q", err, svcErr)
	}
}

// TestWithRegisterPINBackend_RecoveryGenerateFailsClosedWithout proves the
// fail-closed default on BarrierRecoveryGenerate.
func TestWithRegisterPINBackend_RecoveryGenerateFailsClosedWithout(t *testing.T) {
	client, _ := newSOPINGatedClient(t)

	_, err := client.BarrierRecoveryGenerate(t.Context(), &pb.BarrierRecoveryGenerateRequest{SoPin: pintest.ThrottledSOPIN, Threshold: 2})
	requireCode(t, "BarrierRecoveryGenerate without a PIN backend", err, codes.FailedPrecondition)
}
