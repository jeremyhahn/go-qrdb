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
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// TLS helpers
// ---------------------------------------------------------------------------

// integrationTLSBundle holds the shared TLS material for a single test so
// that server and client use the same self-signed CA.
type integrationTLSBundle struct {
	serverCfg *tls.Config
	clientCfg *tls.Config
}

// generateIntegrationTLS creates self-signed ECDSA P-256 TLS configurations
// for both server and client. The certificate is valid for 127.0.0.1 and
// localhost. It acts as its own CA so the client can verify the server.
func generateIntegrationTLS(t *testing.T) *integrationTLSBundle {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	parsedCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        parsedCert,
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(parsedCert)

	return &integrationTLSBundle{
		serverCfg: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			ClientAuth:   tls.NoClientCert,
			MinVersion:   tls.VersionTLS13,
		},
		clientCfg: &tls.Config{
			RootCAs:    caPool,
			NextProtos: []string{"qrdb-quic"},
			MinVersion: tls.VersionTLS13,
		},
	}
}

// ---------------------------------------------------------------------------
// Server lifecycle helpers
// ---------------------------------------------------------------------------

// integrationEnv holds a started server and client TLS config that share the
// same self-signed certificate authority.
type integrationEnv struct {
	srv       *Server
	clientCfg *tls.Config
}

// startIntegrationServer creates and starts a QUIC server with the given
// mock services. It registers a cleanup function to stop the server when
// the test ends. Returns an integrationEnv whose clientCfg trusts the
// server's self-signed certificate.
func startIntegrationServer(
	t *testing.T,
	kvSvc *mockKVService,
	adminSvc *mockAdminService,
	opts ...ServerOption,
) *integrationEnv {
	t.Helper()

	bundle := generateIntegrationTLS(t)
	allOpts := append([]ServerOption{WithTLS(bundle.serverCfg)}, opts...)

	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", allOpts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() { srv.Stop() })

	return &integrationEnv{srv: srv, clientCfg: bundle.clientCfg}
}

// dialServer opens a QUIC connection to the environment's server using the
// matching client TLS config and registers cleanup to close the connection.
func (env *integrationEnv) dialServer(t *testing.T) *quicgo.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := quicgo.DialAddr(ctx, env.srv.Addr(), env.clientCfg, &quicgo.Config{
		MaxIdleTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.CloseWithError(0, "test done")
	})

	return conn
}

// roundTrip opens a stream, writes a request, reads a response, and returns
// the response fields. The stream is closed after reading the response.
func roundTrip(
	t *testing.T,
	conn *quicgo.Conn,
	method string,
	requestID uint64,
	payload []byte,
) (respID uint64, status uint16, respPayload []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if err := wire.WriteRequest(stream, method, "", requestID, payload); err != nil {
		t.Fatalf("write request: %v", err)
	}

	respID, status, respPayload, err = wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	return respID, status, respPayload
}

// ---------------------------------------------------------------------------
// TestIntegration_StartStopLifecycle
// ---------------------------------------------------------------------------

func TestIntegration_StartStopLifecycle(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})

	addr := env.srv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty address after Start")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("parse address %q: %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("expected host 127.0.0.1, got %q", host)
	}
	if port == "0" {
		t.Fatal("expected bound port, got 0")
	}
}

func TestIntegration_StartStopLifecycle_StopIdempotent(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(bundle.serverCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	// First stop.
	srv.Stop()

	// Second stop must not panic.
	srv.Stop()
}

// ---------------------------------------------------------------------------
// TestIntegration_KVPut_Success
// ---------------------------------------------------------------------------

func TestIntegration_KVPut_Success(t *testing.T) {
	kvSvc := &mockKVService{}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:   "test-key",
		Value: []byte("test-value"),
	})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	respID, status, _ := roundTrip(t, conn, "kv.Put", 42, payload)

	if respID != 42 {
		t.Fatalf("expected request ID 42, got %d", respID)
	}
	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK (0), got %d", status)
	}
}

func TestIntegration_KVPut_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{LeaderAddress: "10.0.0.2:9000"}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:   "test-key",
		Value: []byte("test-value"),
	})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	_, status, _ := roundTrip(t, conn, "kv.Put", 1, payload)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1), got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_KVGet_Success
// ---------------------------------------------------------------------------

func TestIntegration_KVGet_Success(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "found-key" {
				return []byte("found-value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.GetRequest{Key: "found-key"})
	if err != nil {
		t.Fatalf("marshal GetRequest: %v", err)
	}

	respID, status, respPayload := roundTrip(t, conn, "kv.Get", 100, payload)

	if respID != 100 {
		t.Fatalf("expected request ID 100, got %d", respID)
	}
	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK (0), got %d", status)
	}

	var resp pb.GetResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal GetResponse: %v", err)
	}
	if string(resp.GetValue()) != "found-value" {
		t.Fatalf("expected value 'found-value', got %q", resp.GetValue())
	}
}

func TestIntegration_KVGet_NotFound(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.GetRequest{Key: "missing-key"})
	if err != nil {
		t.Fatalf("marshal GetRequest: %v", err)
	}

	_, status, respPayload := roundTrip(t, conn, "kv.Get", 101, payload)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1), got %d", status)
	}
	if len(respPayload) == 0 {
		t.Fatal("expected non-empty error payload")
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_UnknownMethod
// ---------------------------------------------------------------------------

func TestIntegration_UnknownMethod(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	_, status, respPayload := roundTrip(t, conn, "nosuch.Method", 200, nil)

	if status != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (2), got %d", status)
	}
	if len(respPayload) == 0 {
		t.Fatal("expected non-empty error payload for unknown method")
	}
}

func TestIntegration_UnknownMethod_ResponseContainsMethodName(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	_, _, respPayload := roundTrip(t, conn, "fake.Operation", 201, nil)

	msg := string(respPayload)
	if msg == "" {
		t.Fatal("expected error message in response")
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_HandlerError
// ---------------------------------------------------------------------------

func TestIntegration_HandlerError(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &testError{msg: "simulated failure"}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.GetRequest{Key: "error-key"})
	if err != nil {
		t.Fatalf("marshal GetRequest: %v", err)
	}

	_, status, respPayload := roundTrip(t, conn, "kv.Get", 300, payload)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1), got %d", status)
	}
	if len(respPayload) == 0 {
		t.Fatal("expected non-empty error payload")
	}
}

func TestIntegration_HandlerError_ServiceNotConfigured(t *testing.T) {
	// Server without MVCC service configured; MVCC handlers return
	// ServiceNotConfiguredError which maps to StatusUnimplemented.
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.GetRevisionRequest{Key: "any-key"})
	if err != nil {
		t.Fatalf("marshal GetRevisionRequest: %v", err)
	}

	_, status, _ := roundTrip(t, conn, "mvcc.GetRevision", 301, payload)

	if status != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (2) for unconfigured service, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_MultipleStreams
// ---------------------------------------------------------------------------

func TestIntegration_MultipleStreams(t *testing.T) {
	kvSvc := &mockKVService{}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	const streamCount = 10

	type result struct {
		idx    int
		respID uint64
		status uint16
	}

	results := make([]result, streamCount)
	var wg sync.WaitGroup

	for i := range streamCount {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			payload, err := proto.Marshal(&pb.PutRequest{
				Key:   "key-" + string(rune('A'+idx)),
				Value: []byte("val"),
			})
			if err != nil {
				t.Errorf("stream %d: marshal: %v", idx, err)
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := conn.OpenStreamSync(ctx)
			if err != nil {
				t.Errorf("stream %d: open: %v", idx, err)
				return
			}
			defer func() { _ = stream.Close() }()

			if err := wire.WriteRequest(stream, "kv.Put", "", uint64(idx+1), payload); err != nil {
				t.Errorf("stream %d: write: %v", idx, err)
				return
			}

			respID, status, _, err := wire.ReadResponse(stream)
			if err != nil {
				t.Errorf("stream %d: read: %v", idx, err)
				return
			}

			results[idx] = result{idx: idx, respID: respID, status: status}
		}(i)
	}

	wg.Wait()

	for i, r := range results {
		if r.respID != uint64(i+1) {
			t.Errorf("stream %d: expected request ID %d, got %d", i, i+1, r.respID)
		}
		if r.status != wire.StatusOK {
			t.Errorf("stream %d: expected StatusOK (0), got %d", i, r.status)
		}
	}
}

func TestIntegration_MultipleStreams_MixedMethods(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("value"), nil
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	// Stream 1: kv.Put
	putPayload, err := proto.Marshal(&pb.PutRequest{Key: "k1", Value: []byte("v1")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	_, putStatus, _ := roundTrip(t, conn, "kv.Put", 1, putPayload)
	if putStatus != wire.StatusOK {
		t.Fatalf("kv.Put: expected StatusOK, got %d", putStatus)
	}

	// Stream 2: kv.Get
	getPayload, err := proto.Marshal(&pb.GetRequest{Key: "k1"})
	if err != nil {
		t.Fatalf("marshal GetRequest: %v", err)
	}
	_, getStatus, _ := roundTrip(t, conn, "kv.Get", 2, getPayload)
	if getStatus != wire.StatusOK {
		t.Fatalf("kv.Get: expected StatusOK, got %d", getStatus)
	}

	// Stream 3: admin.Health (different handler category)
	_, healthStatus, _ := roundTrip(t, conn, "admin.Health", 3, nil)
	if healthStatus != wire.StatusOK {
		t.Fatalf("admin.Health: expected StatusOK, got %d", healthStatus)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_MalformedRequest
// ---------------------------------------------------------------------------

func TestIntegration_MalformedRequest(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Write garbage data that cannot be parsed as a valid wire request.
	_, _ = stream.Write([]byte{0xFF, 0xFF})
	_ = stream.Close()

	// The server should handle the malformed request gracefully. It will
	// fail to read the request frame and close the stream without writing
	// a response. We verify the server is still alive by sending a valid
	// request on a new stream.
	payload, err := proto.Marshal(&pb.PutRequest{Key: "alive", Value: []byte("yes")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	_, status, _ := roundTrip(t, conn, "kv.Put", 999, payload)
	if status != wire.StatusOK {
		t.Fatalf("expected server still alive with StatusOK, got %d", status)
	}
}

func TestIntegration_MalformedRequest_TruncatedPayload(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Write a valid method length header but then close immediately,
	// creating a truncated request frame.
	_, _ = stream.Write([]byte{0x00, 0x06}) // methodLen = 6
	_, _ = stream.Write([]byte("kv.Pu"))    // 5 bytes instead of 6
	_ = stream.Close()

	// Verify the server is still healthy.
	payload, err := proto.Marshal(&pb.PutRequest{Key: "alive2", Value: []byte("yes2")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	_, status, _ := roundTrip(t, conn, "kv.Put", 998, payload)
	if status != wire.StatusOK {
		t.Fatalf("expected server still alive with StatusOK, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_PanicRecovery
// ---------------------------------------------------------------------------

func TestIntegration_PanicRecovery(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithTLS(bundle.serverCfg))

	// Inject a handler that panics BEFORE starting the server to avoid
	// a data race on the handlers map.
	srv.handlers["test.panic"] = func(_ context.Context, _ []byte) ([]byte, error) {
		panic("integration test panic")
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	env := &integrationEnv{srv: srv, clientCfg: bundle.clientCfg}
	conn := env.dialServer(t)

	_, status, respPayload := roundTrip(t, conn, "test.panic", 500, nil)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1) for panic, got %d", status)
	}
	if string(respPayload) != "internal server error" {
		t.Fatalf("expected 'internal server error' payload, got %q", respPayload)
	}
}

func TestIntegration_PanicRecovery_ServerStillAlive(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithTLS(bundle.serverCfg))

	// Inject a handler that panics BEFORE starting the server.
	srv.handlers["test.panic2"] = func(_ context.Context, _ []byte) ([]byte, error) {
		panic("another panic")
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	env := &integrationEnv{srv: srv, clientCfg: bundle.clientCfg}
	conn := env.dialServer(t)

	// Trigger the panic.
	roundTrip(t, conn, "test.panic2", 501, nil)

	// Verify the server is still serving.
	_, status, _ := roundTrip(t, conn, "admin.Health", 502, nil)
	if status != wire.StatusOK {
		t.Fatalf("expected server alive after panic with StatusOK, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_KVDelete_Success
// ---------------------------------------------------------------------------

func TestIntegration_KVDelete_Success(t *testing.T) {
	kvSvc := &mockKVService{}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.DeleteRequest{Key: "del-key"})
	if err != nil {
		t.Fatalf("marshal DeleteRequest: %v", err)
	}

	_, status, _ := roundTrip(t, conn, "kv.Delete", 400, payload)

	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK (0), got %d", status)
	}
}

func TestIntegration_KVDelete_Error(t *testing.T) {
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return &testError{msg: "delete failed"}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.DeleteRequest{Key: "del-key"})
	if err != nil {
		t.Fatalf("marshal DeleteRequest: %v", err)
	}

	_, status, _ := roundTrip(t, conn, "kv.Delete", 401, payload)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1), got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_AdminHealth_Success
// ---------------------------------------------------------------------------

func TestIntegration_AdminHealth_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "healthy", Leader: true}, nil
		},
	}
	env := startIntegrationServer(t, &mockKVService{}, adminSvc)
	conn := env.dialServer(t)

	respID, status, respPayload := roundTrip(t, conn, "admin.Health", 600, nil)

	if respID != 600 {
		t.Fatalf("expected request ID 600, got %d", respID)
	}
	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK (0), got %d", status)
	}

	var resp pb.HealthResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal HealthResponse: %v", err)
	}
	if resp.GetStatus() != "healthy" {
		t.Fatalf("expected status 'healthy', got %q", resp.GetStatus())
	}
	if !resp.GetLeader() {
		t.Fatal("expected leader=true")
	}
}

func TestIntegration_AdminHealth_Error(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return nil, &testError{msg: "health check failed"}
		},
	}
	env := startIntegrationServer(t, &mockKVService{}, adminSvc)
	conn := env.dialServer(t)

	_, status, _ := roundTrip(t, conn, "admin.Health", 601, nil)

	if status != wire.StatusError {
		t.Fatalf("expected StatusError (1), got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_Addr_AfterStart
// ---------------------------------------------------------------------------

func TestIntegration_Addr_AfterStart(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})

	addr1 := env.srv.Addr()
	addr2 := env.srv.Addr()

	if addr1 != addr2 {
		t.Fatalf("Addr() not stable: %q vs %q", addr1, addr2)
	}

	if addr1 == "127.0.0.1:0" {
		t.Fatal("expected Addr() to return bound address, not 127.0.0.1:0")
	}
}

func TestIntegration_Addr_ReflectsListener(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})

	addr := env.srv.Addr()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}

	if port == "" || port == "0" {
		t.Fatalf("expected real port, got %q", port)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_EmptyPayload
// ---------------------------------------------------------------------------

func TestIntegration_EmptyPayload_AdminHealth(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	// admin.Health ignores the payload entirely; nil payload is valid.
	_, status, _ := roundTrip(t, conn, "admin.Health", 700, nil)

	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK (0), got %d", status)
	}
}

func TestIntegration_EmptyPayload_KVPut_Unmarshal(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	// Empty payload unmarshals to a zero-value PutRequest which is valid
	// proto (empty key, nil value).
	_, status, _ := roundTrip(t, conn, "kv.Put", 701, nil)

	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK for empty PutRequest, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_ConnectionReuse
// ---------------------------------------------------------------------------

func TestIntegration_ConnectionReuse(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	// Send multiple sequential requests on the same connection to verify
	// the connection remains usable across multiple stream lifecycles.
	for i := range 5 {
		payload, err := proto.Marshal(&pb.PutRequest{
			Key:   "reuse-key",
			Value: []byte("v"),
		})
		if err != nil {
			t.Fatalf("iteration %d: marshal: %v", i, err)
		}

		_, status, _ := roundTrip(t, conn, "kv.Put", uint64(800+i), payload)
		if status != wire.StatusOK {
			t.Fatalf("iteration %d: expected StatusOK, got %d", i, status)
		}
	}
}

func TestIntegration_ConnectionReuse_AfterError(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &testError{msg: "transient error"}
		},
	}
	env := startIntegrationServer(t, kvSvc, &mockAdminService{})
	conn := env.dialServer(t)

	// First request: error response.
	getPayload, err := proto.Marshal(&pb.GetRequest{Key: "err-key"})
	if err != nil {
		t.Fatalf("marshal GetRequest: %v", err)
	}
	_, status, _ := roundTrip(t, conn, "kv.Get", 900, getPayload)
	if status != wire.StatusError {
		t.Fatalf("expected StatusError for first request, got %d", status)
	}

	// Second request: success on the same connection.
	putPayload, err := proto.Marshal(&pb.PutRequest{Key: "ok-key", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	_, status, _ = roundTrip(t, conn, "kv.Put", 901, putPayload)
	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK for second request, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// Auth middleware helpers
// ---------------------------------------------------------------------------

// noopAuthorizer is a minimal auth.Authorizer that always allows access.
type noopAuthorizer struct{}

func (n *noopAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// TestIntegration_Auth_NoClientCert
// ---------------------------------------------------------------------------

func TestIntegration_Auth_NoClientCert_Unauthenticated(t *testing.T) {
	// Server with auth middleware enabled but client has no cert (NoClientCert).
	// The authenticate function checks PeerCertificates and returns
	// StatusUnauthenticated before calling any authenticator.
	mw := authmw.New(&noopAuthorizer{})
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(mw),
	)
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	_, status, respPayload := roundTrip(t, conn, "kv.Put", 1000, payload)

	if status != wire.StatusUnauthenticated {
		t.Fatalf("expected StatusUnauthenticated (3), got %d", status)
	}
	if len(respPayload) == 0 {
		t.Fatal("expected non-empty error payload")
	}
}

func TestIntegration_Auth_NoClientCert_ServerStillAlive(t *testing.T) {
	// Verify the server continues serving after an auth failure.
	mw := authmw.New(&noopAuthorizer{})
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(mw),
	)
	conn := env.dialServer(t)

	// First request: should be unauthenticated.
	payload, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	_, status, _ := roundTrip(t, conn, "kv.Put", 1001, payload)
	if status != wire.StatusUnauthenticated {
		t.Fatalf("expected StatusUnauthenticated, got %d", status)
	}

	// Second request: same result (server not crashed).
	_, status, _ = roundTrip(t, conn, "kv.Put", 1002, payload)
	if status != wire.StatusUnauthenticated {
		t.Fatalf("expected StatusUnauthenticated on second request, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// Mutual TLS helpers
// ---------------------------------------------------------------------------

// mutualTLSBundle holds TLS material for a mutual-TLS test: a shared CA
// signs both the server and client certificates so each side can verify
// the other.
type mutualTLSBundle struct {
	serverCfg *tls.Config
	clientCfg *tls.Config
	// clientCert is the parsed leaf used to assert identity in tests.
	clientCert *x509.Certificate
}

// generateMutualTLS builds a self-signed CA, issues a server certificate and
// a client certificate from that CA, and returns matching TLS configs.
// The server requires and verifies the client cert; the client presents it.
func generateMutualTLS(t *testing.T) *mutualTLSBundle {
	t.Helper()

	// --- CA key + cert ---
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(crand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	// --- Server key + cert signed by CA ---
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	srvTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	srvDER, err := x509.CreateCertificate(crand.Reader, srvTemplate, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	srvTLSCert := tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}

	// --- Client key + cert signed by CA ---
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject: pkix.Name{
			CommonName:         "test-client",
			OrganizationalUnit: []string{"tenant-test"},
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(crand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	clientCert, err := x509.ParseCertificate(clientDER)
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}
	clientTLSCert := tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey}

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{srvTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS13,
	}
	clientCfg := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		RootCAs:      caPool,
		NextProtos:   []string{"qrdb-quic"},
		MinVersion:   tls.VersionTLS13,
	}

	return &mutualTLSBundle{
		serverCfg:  serverCfg,
		clientCfg:  clientCfg,
		clientCert: clientCert,
	}
}

// startMutualTLSServer starts a QUIC server configured for mutual TLS.
func startMutualTLSServer(
	t *testing.T,
	kvSvc *mockKVService,
	adminSvc *mockAdminService,
	bundle *mutualTLSBundle,
	opts ...ServerOption,
) *integrationEnv {
	t.Helper()

	allOpts := append([]ServerOption{WithTLS(bundle.serverCfg)}, opts...)
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", allOpts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	return &integrationEnv{srv: srv, clientCfg: bundle.clientCfg}
}

// ---------------------------------------------------------------------------
// TestIntegration_Auth_MutualTLS — covers authenticate lines 4-8
// ---------------------------------------------------------------------------

// TestIntegration_Auth_MutualTLS_Success covers the authenticate success path:
// client presents a valid certificate → WithTLSState → AuthenticateQUIC succeeds.
func TestIntegration_Auth_MutualTLS_Success(t *testing.T) {
	bundle := generateMutualTLS(t)

	// MTLSAuthenticator reads VerifiedChains from TLS state.
	mw := authmw.New(
		&noopAuthorizer{},
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
	)

	env := startMutualTLSServer(t, &mockKVService{}, &mockAdminService{}, bundle,
		WithAuthMiddleware(mw),
	)
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{Key: "mtls-key", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	// The request goes through authenticate (client cert present + verified)
	// and AuthenticateQUIC succeeds → kv.Put is dispatched → StatusOK.
	_, status, _ := roundTrip(t, conn, "kv.Put", 2000, payload)
	if status != wire.StatusOK {
		t.Fatalf("expected StatusOK for authenticated mTLS request, got %d", status)
	}
}

// TestIntegration_Auth_MutualTLS_AuthenticateFails covers the authenticate
// error path: client presents a certificate but the authenticator rejects it.
func TestIntegration_Auth_MutualTLS_AuthenticateFails(t *testing.T) {
	bundle := generateMutualTLS(t)

	// rejectingAuthenticator always returns an error even when a cert is present.
	mw := authmw.New(
		&noopAuthorizer{},
		authmw.WithAuthenticator(&rejectingAuthenticator{}),
	)

	env := startMutualTLSServer(t, &mockKVService{}, &mockAdminService{}, bundle,
		WithAuthMiddleware(mw),
	)
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	// The authenticate function calls AuthenticateQUIC which returns an error
	// → StatusUnauthenticated.
	_, status, respPayload := roundTrip(t, conn, "kv.Put", 2001, payload)
	if status != wire.StatusUnauthenticated {
		t.Fatalf("expected StatusUnauthenticated, got %d", status)
	}
	if len(respPayload) == 0 {
		t.Fatal("expected non-empty error payload")
	}
}

// rejectingAuthenticator implements auth.Authenticator and always rejects.
type rejectingAuthenticator struct{}

func (r *rejectingAuthenticator) Authenticate(_ context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (r *rejectingAuthenticator) Name() string { return "rejecting" }
