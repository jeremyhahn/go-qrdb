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
// so a test can opt into a size-related option without disturbing the
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

// oversizedValue is bigger than DefaultMaxMessageBytes.
func oversizedValue() []byte {
	return make([]byte, DefaultMaxMessageBytes+(1<<20))
}

// TestServer_DefaultMaxMessageBytes_RefusesOversizedRequest proves the
// server's max message size is DefaultMaxMessageBytes on every RPC, with no
// opt-in to raise it (go-qrdb#576): an unauthenticated peer can no longer
// force an oversized allocation per call just by sending one. A caller with
// a larger payload uses the chunked import session API instead.
func TestServer_DefaultMaxMessageBytes_RefusesOversizedRequest(t *testing.T) {
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

// TestServer_DefaultMaxMessageBytes_DoesNotCapResponses proves the server's
// pre-auth message cap bounds only what a client sends it, not what it
// chooses to send back: a value the server already holds, well over
// DefaultMaxMessageBytes, is served back whole on an ordinary Get
// (go-qrdb#576 also-fix: MaxSendMsgSize used to be set to the same bound,
// which would have truncated this response with codes.ResourceExhausted on
// the SERVER side regardless of what the client was willing to receive).
// The oversized value is written directly through kvSvc, bypassing the
// client's own request path, so this test exercises only the response side;
// TestServer_DefaultMaxMessageBytes_RefusesOversizedRequest above covers the
// request side, which is unchanged.
func TestServer_DefaultMaxMessageBytes_DoesNotCapResponses(t *testing.T) {
	kvSvc := newMockKVService()
	big := oversizedValue()
	if err := kvSvc.Put(context.Background(), "big", big); err != nil {
		t.Fatalf("seeding kvSvc directly: %v", err)
	}

	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(kvSvc, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// The client raises its own receive cap exactly as
	// transport.DefaultClientMaxRecvMessageBytes does in the SDK, so this
	// test fails on the SERVER'S cap alone, not the client default's.
	conn, err := grpc.NewClient(
		srv.Addr(),
		grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(len(big)+(1<<20))),
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
	if len(resp.GetValue()) != len(big) {
		t.Fatalf("Get returned %d bytes, want %d", len(resp.GetValue()), len(big))
	}
}

// TestWithMaxConcurrentStreams_SetsField mirrors the other WithX option
// tests: the option lands on the field Start reads, for the one hardening
// knob (concurrent-stream cap) that a unary RPC round-trip cannot observe
// directly.
func TestWithMaxConcurrentStreams_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithMaxConcurrentStreams(42))
	if srv.maxConcurrentStreams != 42 {
		t.Errorf("maxConcurrentStreams = %d, want 42", srv.maxConcurrentStreams)
	}
}

// TestWithMaxMessageBytes_SetsField mirrors TestWithMaxConcurrentStreams_SetsField
// for the message-size knob.
func TestWithMaxMessageBytes_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithMaxMessageBytes(1<<20))
	if srv.maxMessageBytes != 1<<20 {
		t.Errorf("maxMessageBytes = %d, want %d", srv.maxMessageBytes, 1<<20)
	}
}

// TestWithMaxConnsPerPeer_SetsField mirrors TestWithMaxConcurrentStreams_SetsField
// for the per-peer connection cap.
func TestWithMaxConnsPerPeer_SetsField(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithMaxConnsPerPeer(7))
	if srv.maxConnsPerPeer != 7 {
		t.Errorf("maxConnsPerPeer = %d, want 7", srv.maxConnsPerPeer)
	}
}
