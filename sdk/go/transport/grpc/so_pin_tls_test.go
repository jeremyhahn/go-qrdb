// Copyright 2026 Jeremy Hahn
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

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestSOPIN_PlaintextConnectionNeverCarriesThePIN: over insecure credentials
// a PIN-bound RPC is refused before it is sent, and the server never sees the
// PIN.
func TestSOPIN_PlaintextConnectionNeverCarriesThePIN(t *testing.T) {
	probe := &sotest.Probe{}
	srv := googlegrpc.NewServer()
	pb.RegisterQRDBServiceServer(srv, probe)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	tr, err := New(transport.WithAddress(lis.Addr().String()), transport.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	ctx, release := sotest.BindPIN(t, sotest.PIN)
	defer release()
	_, err = tr.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3})
	if probe.SawPIN() {
		t.Fatal("the SO PIN crossed a plaintext gRPC connection")
	}
	var tlsErr *transport.SOPINRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("err = %v, want *transport.SOPINRequiresTLSError", err)
	}
	// Without a bound PIN the same plaintext connection still works.
	if _, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 2, Total: 3}); err != nil {
		t.Fatalf("unbound rekey over plaintext: %v", err)
	}
}
