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

package unix

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// TestWithSealedWindowListener_RefusesOnceUnsealed pins the boot-time
// sealed-window listener's gate on the Unix socket: it has no
// authentication behind it, so once the barrier is unsealed it refuses every
// method, allowlisted ones (here Health) included, and WithSealedWindowProbe
// applied last restores the regular pass-through gate.
func TestWithSealedWindowListener_RefusesOnceUnsealed(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	probe := &fakeSealedProbe{sealed: false}
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithSealedWindowListener(probe),
		WithTLS(tlsCfg))
	if srv.sealedWindowListener == nil {
		t.Fatal("WithSealedWindowListener did not select the listener gate")
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	_, err := pb.NewQRDBServiceClient(conn).Health(context.Background(), &emptypb.Empty{})
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable || st.Message() != sealedwindow.ClosedMessage {
		t.Fatalf("Health on the unsealed boot listener: %v, want Unavailable %q", err, sealedwindow.ClosedMessage)
	}

	WithSealedWindowProbe(probe)(srv)
	if srv.sealedWindowListener != nil {
		t.Fatal("WithSealedWindowProbe applied last must install the regular gate")
	}
}
