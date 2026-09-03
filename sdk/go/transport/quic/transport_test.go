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

package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// New() tests
// ---------------------------------------------------------------------------

func TestNew_DefaultConfig(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatal("New() returned nil transport")
	}
	if tr.config == nil {
		t.Fatal("New() did not set config")
	}
	if tr.config.Address != "localhost:63002" {
		t.Errorf("expected address localhost:63002, got %q", tr.config.Address)
	}
	if tr.config.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", tr.config.Timeout)
	}
}

func TestNew_WithOptions(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	tr, err := New(
		transport.WithAddress("example.com:9443"),
		transport.WithTimeout(10*time.Second),
		transport.WithTLSConfig(tlsCfg),
	)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if tr.config.Address != "example.com:9443" {
		t.Errorf("expected address example.com:9443, got %q", tr.config.Address)
	}
	if tr.config.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", tr.config.Timeout)
	}
	if tr.config.TLSConfig != tlsCfg {
		t.Error("expected custom TLSConfig to be set")
	}
}

func TestNew_ValidationError(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("New() without address should return validation error")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T: %v", err, err)
	}
}

func TestNew_CertWithoutKeyError(t *testing.T) {
	_, err := New(
		transport.WithAddress("localhost:63002"),
		transport.WithTLS("/cert.pem", "", "/ca.pem"),
	)
	if err == nil {
		t.Fatal("New() with cert but no key should return validation error")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close() tests
// ---------------------------------------------------------------------------

func TestTransport_Close_NotConnected(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	// Close on an unconnected transport should not panic and return nil.
	if err := tr.Close(); err != nil {
		t.Errorf("Close() on unconnected transport returned error: %v", err)
	}
}

func TestTransport_Close_Idempotent(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	// First close.
	if err := tr.Close(); err != nil {
		t.Errorf("first Close() returned error: %v", err)
	}
	// Second close should be a no-op.
	if err := tr.Close(); err != nil {
		t.Errorf("second Close() returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Healthy() tests
// ---------------------------------------------------------------------------

func TestTransport_Healthy_NotConnected(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	ctx := context.Background()
	if tr.Healthy(ctx) {
		t.Error("Healthy() should return false when not connected")
	}
}

func TestTransport_Healthy_AfterClose(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	if tr.Healthy(ctx) {
		t.Error("Healthy() should return false after Close()")
	}
}

// ---------------------------------------------------------------------------
// Request() tests
// ---------------------------------------------------------------------------

func TestTransport_Request_ReturnsNotImplemented(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	ctx := context.Background()
	err = tr.Request(ctx, "kv.Put", nil, nil)
	if err == nil {
		t.Fatal("Request() should return error")
	}
	var notImpl *transport.NotImplementedError
	if !errors.As(err, &notImpl) {
		t.Errorf("expected NotImplementedError, got %T: %v", err, err)
	}
	if notImpl.Method != "Request(kv.Put)" {
		t.Errorf("expected method Request(kv.Put), got %q", notImpl.Method)
	}
}

func TestTransport_Request_DifferentMethod(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	ctx := context.Background()
	err = tr.Request(ctx, "admin.Status", nil, nil)
	if err == nil {
		t.Fatal("Request() should return error")
	}
	var notImpl *transport.NotImplementedError
	if !errors.As(err, &notImpl) {
		t.Errorf("expected NotImplementedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for KV methods
// ---------------------------------------------------------------------------

func TestTransport_Put_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.Put(ctx, "key", []byte("value"))
	if err == nil {
		t.Fatal("Put() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Get_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Get(ctx, "key")
	if err == nil {
		t.Fatal("Get() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Delete_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.Delete(ctx, "key")
	if err == nil {
		t.Fatal("Delete() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_List_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.List(ctx, "prefix")
	if err == nil {
		t.Fatal("List() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Scan_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Scan(ctx, "prefix")
	if err == nil {
		t.Fatal("Scan() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for Admin methods
// ---------------------------------------------------------------------------

func TestTransport_Health_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Health(ctx)
	if err == nil {
		t.Fatal("Health() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Status_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Status(ctx)
	if err == nil {
		t.Fatal("Status() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Peers_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Peers(ctx)
	if err == nil {
		t.Fatal("Peers() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_AddMember_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.AddMember(ctx, 1, 2, "10.0.0.1:63001")
	if err == nil {
		t.Fatal("AddMember() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_RemoveMember_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.RemoveMember(ctx, 1, 2)
	if err == nil {
		t.Fatal("RemoveMember() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_RequestSnapshot_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.RequestSnapshot(ctx, 1)
	if err == nil {
		t.Fatal("RequestSnapshot() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Drain_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.Drain(ctx)
	if err == nil {
		t.Fatal("Drain() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_Capabilities_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.Capabilities(ctx)
	if err == nil {
		t.Fatal("Capabilities() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Connect() tests
// ---------------------------------------------------------------------------

func TestTransport_Connect_AfterClose(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.Connect(ctx)
	if err == nil {
		t.Fatal("Connect() after Close() should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// stripScheme tests
// ---------------------------------------------------------------------------

func TestStripScheme_HTTPS(t *testing.T) {
	got := stripScheme("https://localhost:63002")
	expected := "localhost:63002"
	if got != expected {
		t.Errorf("stripScheme() = %q, want %q", got, expected)
	}
}

func TestStripScheme_HTTP(t *testing.T) {
	got := stripScheme("http://localhost:63002")
	expected := "localhost:63002"
	if got != expected {
		t.Errorf("stripScheme() = %q, want %q", got, expected)
	}
}

func TestStripScheme_NoScheme(t *testing.T) {
	got := stripScheme("localhost:63002")
	expected := "localhost:63002"
	if got != expected {
		t.Errorf("stripScheme() = %q, want %q", got, expected)
	}
}

func TestStripScheme_EmptyString(t *testing.T) {
	got := stripScheme("")
	if got != "" {
		t.Errorf("stripScheme() = %q, want empty string", got)
	}
}

// ---------------------------------------------------------------------------
// mapWireError tests
// ---------------------------------------------------------------------------

func TestMapWireError_StatusError_KeyNotFound(t *testing.T) {
	err := mapWireError("kv.Get", wire.StatusError, []byte("key not found"))
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestMapWireError_StatusError_Generic(t *testing.T) {
	err := mapWireError("kv.Put", wire.StatusError, []byte("internal error"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "kv.Put" {
		t.Errorf("expected method kv.Put, got %q", reqErr.Method)
	}
	if reqErr.StatusCode != int(wire.StatusError) {
		t.Errorf("expected status code %d, got %d", wire.StatusError, reqErr.StatusCode)
	}
}

// StatusUnimplemented is a CAPABILITY refusal, not a generic request failure:
// the node is healthy and simply was not built with this verb, or does not
// register the method at all. It became *transport.CapabilityUnsupportedError
// in go-qrdb#124, mirroring mapGRPCError's codes.Unimplemented case exactly.
//
// Before that a caller could not tell "this node cannot do that" from "this
// node broke", so a client library that retries on failures would retry
// something that can never succeed — the go-qrdb#52 shape that go-qrdb#141
// removed from gRPC and REST but not from here. The message is carried
// verbatim because a refusal that does not say WHAT is missing is little
// better than an untyped one.
func TestMapWireError_StatusUnimplemented(t *testing.T) {
	err := mapWireError("ts.RichQuery", wire.StatusUnimplemented, []byte("not implemented"))
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *transport.CapabilityUnsupportedError, got %T: %v", err, err)
	}
	if unsupported.Message != "not implemented" {
		t.Errorf("message = %q, want the server's own account %q", unsupported.Message, "not implemented")
	}
}

func TestMapWireError_StatusUnauthenticated(t *testing.T) {
	err := mapWireError("admin.Drain", wire.StatusUnauthenticated, []byte("no cert"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != int(wire.StatusUnauthenticated) {
		t.Errorf("expected status code %d, got %d", wire.StatusUnauthenticated, reqErr.StatusCode)
	}
	if reqErr.Err == nil || reqErr.Err.Error() != "unauthenticated: no cert" {
		t.Errorf("expected unauthenticated error message, got %v", reqErr.Err)
	}
}

func TestMapWireError_StatusPermissionDenied(t *testing.T) {
	err := mapWireError("admin.Drain", wire.StatusPermissionDenied, []byte("insufficient perms"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != int(wire.StatusPermissionDenied) {
		t.Errorf("expected status code %d, got %d", wire.StatusPermissionDenied, reqErr.StatusCode)
	}
	if reqErr.Err == nil || reqErr.Err.Error() != "permission denied: insufficient perms" {
		t.Errorf("expected permission denied error message, got %v", reqErr.Err)
	}
}

func TestMapWireError_UnknownStatusCode(t *testing.T) {
	err := mapWireError("test.Method", 99, []byte("unknown error"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != 99 {
		t.Errorf("expected status code 99, got %d", reqErr.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// buildTLSConfig tests
// ---------------------------------------------------------------------------

func TestBuildTLSConfig_WithCustomConfig(t *testing.T) {
	customCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h3"},
	}
	tr, err := New(
		transport.WithAddress("localhost:63002"),
		transport.WithTLSConfig(customCfg),
	)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	tlsCfg, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig() returned unexpected error: %v", err)
	}
	// Verify ALPN is appended.
	found := false
	for _, p := range tlsCfg.NextProtos {
		if p == "qrdb-quic" {
			found = true
			break
		}
	}
	if !found {
		t.Error("buildTLSConfig() should append qrdb-quic to NextProtos")
	}
	// Verify original "h3" is preserved.
	foundH3 := false
	for _, p := range tlsCfg.NextProtos {
		if p == "h3" {
			foundH3 = true
			break
		}
	}
	if !foundH3 {
		t.Error("buildTLSConfig() should preserve existing NextProtos")
	}
}

func TestBuildTLSConfig_WithCustomConfigAlreadyHasALPN(t *testing.T) {
	customCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"qrdb-quic"},
	}
	tr, err := New(
		transport.WithAddress("localhost:63002"),
		transport.WithTLSConfig(customCfg),
	)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	tlsCfg, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig() returned unexpected error: %v", err)
	}
	if len(tlsCfg.NextProtos) != 1 {
		t.Errorf("expected 1 NextProto, got %d", len(tlsCfg.NextProtos))
	}
}

func TestBuildTLSConfig_NoCustomConfig(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	tlsCfg, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig() returned unexpected error: %v", err)
	}
	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected MinVersion TLS 1.3, got %d", tlsCfg.MinVersion)
	}
	if len(tlsCfg.NextProtos) != 1 || tlsCfg.NextProtos[0] != "qrdb-quic" {
		t.Errorf("expected NextProtos [qrdb-quic], got %v", tlsCfg.NextProtos)
	}
}

func TestBuildTLSConfig_NonexistentCAFile(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	tr.config.TLSCAFile = "/nonexistent/ca.pem"

	_, err = tr.buildTLSConfig()
	if err == nil {
		t.Fatal("buildTLSConfig() should return error for nonexistent CA file")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

func TestBuildTLSConfig_NonexistentCertFile(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	tr.config.TLSCertFile = "/nonexistent/cert.pem"
	tr.config.TLSKeyFile = "/nonexistent/key.pem"

	_, err = tr.buildTLSConfig()
	if err == nil {
		t.Fatal("buildTLSConfig() should return error for nonexistent cert/key files")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Interface assertions (compile-time)
// ---------------------------------------------------------------------------

func TestTransport_ImplementsTransportInterface(t *testing.T) {
	var _ transport.Transport = (*Transport)(nil)
}

func TestTransport_ImplementsKVClientInterface(t *testing.T) {
	var _ transport.KVClient = (*Transport)(nil)
}

func TestTransport_ImplementsAdminClientInterface(t *testing.T) {
	var _ transport.AdminClient = (*Transport)(nil)
}

func TestTransport_ImplementsMVCCClientInterface(t *testing.T) {
	var _ transport.MVCCClient = (*Transport)(nil)
}

func TestTransport_ImplementsTransactionClientInterface(t *testing.T) {
	var _ transport.TransactionClient = (*Transport)(nil)
}

func TestTransport_ImplementsBatchClientInterface(t *testing.T) {
	var _ transport.BatchClient = (*Transport)(nil)
}

func TestTransport_ImplementsWatchClientInterface(t *testing.T) {
	var _ transport.WatchClient = (*Transport)(nil)
}

func TestTransport_ImplementsLeaseClientInterface(t *testing.T) {
	var _ transport.LeaseClient = (*Transport)(nil)
}

func TestTransport_ImplementsTimeSeriesClientInterface(t *testing.T) {
	var _ transport.TimeSeriesClient = (*Transport)(nil)
}

func TestTransport_ImplementsVectorClientInterface(t *testing.T) {
	var _ transport.VectorClient = (*Transport)(nil)
}

func TestTransport_ImplementsUserClientInterface(t *testing.T) {
	var _ transport.UserClient = (*Transport)(nil)
}

func TestTransport_ImplementsRoleClientInterface(t *testing.T) {
	var _ transport.RoleClient = (*Transport)(nil)
}

func TestTransport_ImplementsCertClientInterface(t *testing.T) {
	var _ transport.CertClient = (*Transport)(nil)
}

func TestTransport_ImplementsLockClientInterface(t *testing.T) {
	var _ transport.LockClient = (*Transport)(nil)
}

// ---------------------------------------------------------------------------
// Shard management tests
// ---------------------------------------------------------------------------

func TestTransport_StartShard_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.StartShard(ctx, &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
	})
	if err == nil {
		t.Fatal("StartShard() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_StopShard_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.StopShard(ctx, 1)
	if err == nil {
		t.Fatal("StopShard() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetShardState_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetShardState(ctx, 1)
	if err == nil {
		t.Fatal("GetShardState() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetTopology_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetTopology(ctx)
	if err == nil {
		t.Fatal("GetTopology() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}
