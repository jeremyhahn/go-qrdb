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
	"crypto/tls"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// oversizedValue is bigger than grpcpkg.DefaultMaxMessageBytes.
func oversizedValue() []byte {
	return make([]byte, grpcpkg.DefaultMaxMessageBytes+(1<<20))
}

// TestStart_DefaultMaxMessageBytes_RefusesOversizedRequest proves the Unix
// socket server's max message size is grpcpkg.DefaultMaxMessageBytes on
// every RPC, with no opt-in to raise it (go-qrdb#576). A caller with a
// larger payload uses the chunked import session API instead.
func TestStart_DefaultMaxMessageBytes_RefusesOversizedRequest(t *testing.T) {
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

// TestStart_DefaultMaxMessageBytes_DoesNotCapResponses proves the Unix
// socket server's pre-auth message cap bounds only what a client sends it,
// not what it chooses to send back (go-qrdb#576 also-fix): a value the
// server already holds, well over grpcpkg.DefaultMaxMessageBytes, is served
// back whole on an ordinary Get. The oversized value is written directly
// through kvSvc, bypassing the client's own request path, so this test
// exercises only the response side; the request side is unchanged (see
// TestStart_DefaultMaxMessageBytes_RefusesOversizedRequest above).
func TestStart_DefaultMaxMessageBytes_DoesNotCapResponses(t *testing.T) {
	kvSvc := newMockKVService()
	bigValue := oversizedValue()
	if err := kvSvc.Put(context.Background(), "big", bigValue); err != nil {
		t.Fatalf("seeding kvSvc directly: %v", err)
	}

	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(kvSvc, &mockAdminService{}, socketPath, WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// The client raises its own receive cap exactly as
	// transport.DefaultClientMaxRecvMessageBytes does in the SDK, so this
	// test fails on the SERVER'S cap alone, not the client default's.
	clientTLS := &tls.Config{
		RootCAs:    tlsCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(grpccredentials.NewTLS(clientTLS)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(len(bigValue)+(1<<20))),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	client := pb.NewQRDBServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Get(ctx, &pb.GetRequest{Key: "big"})
	if err != nil {
		t.Fatalf("Get an oversized stored value: %v (want it served back whole)", err)
	}
	if len(resp.GetValue()) != len(bigValue) {
		t.Fatalf("Get returned %d bytes, want %d", len(resp.GetValue()), len(bigValue))
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

// TestWithMaxMessageBytes_SetsField mirrors the TCP gRPC server's option
// test: the option lands on the field Start reads.
func TestWithMaxMessageBytes_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithMaxMessageBytes(1<<20))
	if srv.maxMessageBytes != 1<<20 {
		t.Errorf("maxMessageBytes = %d, want %d", srv.maxMessageBytes, 1<<20)
	}
}
