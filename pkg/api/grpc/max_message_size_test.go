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
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// startSizeTestServer is startTestServer plus caller-supplied ServerOptions,
// so a test can opt into WithMaxImportMessageSize without disturbing the
// shared helper every other grpc_test.go case uses.
func startSizeTestServer(t *testing.T, extra ...ServerOption) (pb.QRDBServiceClient, func()) {
	t.Helper()

	tlsCfg := testServerTLSConfig(t)
	opts := append([]ServerOption{WithTLS(tlsCfg)}, extra...)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	conn, err := grpc.NewClient(
		srv.Addr(),
		grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)),
	)
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}

	client := pb.NewQRDBServiceClient(conn)
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return client, cleanup
}

// oversizedValue is bigger than DefaultMaxRecvMsgSize but far smaller than
// MaxImportMessageSize, so the test allocates a few MiB, not a hundred.
func oversizedValue() []byte {
	return make([]byte, DefaultMaxRecvMsgSize+(1<<20))
}

// TestServer_DefaultMaxRecvMsgSize_RefusesOversizedRequest proves the
// server's default max receive message size is DefaultMaxRecvMsgSize, not the
// 101 MiB BatchImport bound, when WithMaxImportMessageSize is not used
// (go-qrdb#576): an unauthenticated peer can no longer force a 101 MiB
// allocation per call on every RPC just by sending one.
func TestServer_DefaultMaxRecvMsgSize_RefusesOversizedRequest(t *testing.T) {
	client, cleanup := startSizeTestServer(t)
	defer cleanup()

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

// TestServer_WithMaxImportMessageSize_AcceptsOversizedRequest proves the
// opt-in actually raises the bound: the same oversized value that
// TestServer_DefaultMaxRecvMsgSize_RefusesOversizedRequest refuses succeeds
// once the server is started with WithMaxImportMessageSize(MaxImportMessageSize).
func TestServer_WithMaxImportMessageSize_AcceptsOversizedRequest(t *testing.T) {
	client, cleanup := startSizeTestServer(t, WithMaxImportMessageSize(MaxImportMessageSize))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.Put(ctx, &pb.PutRequest{Key: "big", Value: oversizedValue()}); err != nil {
		t.Fatalf("Put with an oversized value under the opt-in bound failed: %v", err)
	}
}

// TestWithMaxImportMessageSize_SetsField and
// TestWithMaxConcurrentStreams_SetsField mirror the other WithX option tests:
// the option lands on the field Start reads, for the one hardening knob
// (concurrent-stream cap) that a unary RPC round-trip cannot observe directly.
func TestWithMaxImportMessageSize_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithMaxImportMessageSize(MaxImportMessageSize))
	if srv.maxRecvMsgSize != MaxImportMessageSize {
		t.Errorf("maxRecvMsgSize = %d, want %d", srv.maxRecvMsgSize, MaxImportMessageSize)
	}
}

func TestWithMaxConcurrentStreams_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithMaxConcurrentStreams(42))
	if srv.maxConcurrentStreams != 42 {
		t.Errorf("maxConcurrentStreams = %d, want 42", srv.maxConcurrentStreams)
	}
}
