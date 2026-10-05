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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// oversizedValue is bigger than grpcpkg.DefaultMaxRecvMsgSize.
func oversizedValue() []byte {
	return make([]byte, grpcpkg.DefaultMaxRecvMsgSize+(1<<20))
}

// TestStart_DefaultMaxRecvMsgSize_RefusesOversizedRequest proves the Unix
// socket server's max receive message size is grpcpkg.DefaultMaxRecvMsgSize
// on every RPC, with no opt-in to raise it (go-qrdb#576).
func TestStart_DefaultMaxRecvMsgSize_RefusesOversizedRequest(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()
	client := pb.NewQRDBServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Put(ctx, &pb.PutRequest{Key: "big", Value: oversizedValue()})
	if err == nil {
		t.Fatal("Put with an oversized value succeeded; want codes.ResourceExhausted")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("Put oversized value: code = %v, want codes.ResourceExhausted (err: %v)", got, err)
	}
}

// TestWithMaxConcurrentStreams_SetsField mirrors the TCP gRPC server's
// option test: the option lands on the field Start reads.
func TestWithMaxConcurrentStreams_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithMaxConcurrentStreams(42))
	if srv.maxConcurrentStreams != 42 {
		t.Errorf("maxConcurrentStreams = %d, want 42", srv.maxConcurrentStreams)
	}
}
