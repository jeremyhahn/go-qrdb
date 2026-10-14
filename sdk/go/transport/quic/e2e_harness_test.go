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
	"sync/atomic"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// wireResponder is the function signature for E2E test responders. Each
// responder receives the raw request payload and returns the wire status
// code and response payload. This bypasses the production Servicer
// interfaces and tests the SDK call/wire/dispatch path end-to-end.
type wireResponder func(req []byte) (uint16, []byte)

// e2eServer is a lightweight in-process QUIC server that speaks the QRDB
// wire protocol. It dispatches by method name to test-provided responder
// functions. The server is intentionally a separate implementation from
// the production server in pkg/api/quic so the SDK transport tests can
// exercise the full wire round-trip without bringing in every service
// interface mock.
type e2eServer struct {
	listener   *quicgo.Listener
	responders map[string]wireResponder
	mu         sync.RWMutex
	closed     atomic.Bool
	wg         sync.WaitGroup

	// recordedRequests captures (method, payload) for each request the
	// server received. Tests inspect this slice to assert the SDK marshaled
	// the request correctly.
	recordedRequests []recordedRequest
	recordMu         sync.Mutex

	// tlsState is the server half of the most recently accepted
	// connection's TLS state, so a test can check what the SDK bound to the
	// connection (the init proof's exporter) against the server's view.
	tlsState atomic.Pointer[tls.ConnectionState]
}

// recordedRequest is the captured form of a single request handled by
// the E2E server.
type recordedRequest struct {
	Method   string
	Database string
	Payload  []byte
}

// startE2EServer starts a real QUIC server on a random local port, returns
// the SDK transport that targets it, and registers all responders supplied
// via SetResponder. The test cleans up automatically.
func startE2EServer(t *testing.T) (*e2eServer, *Transport) {
	t.Helper()

	serverCfg, clientCfg := e2eTLS(t)

	ln, err := quicgo.ListenAddr("127.0.0.1:0", serverCfg, &quicgo.Config{
		MaxIdleTimeout:     5 * time.Second,
		KeepAlivePeriod:    1 * time.Second,
		MaxIncomingStreams: 1024,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &e2eServer{
		listener:   ln,
		responders: make(map[string]wireResponder),
	}
	// Pre-register admin.Health so the SDK's Connect() health check
	// succeeds. Individual tests can override or rely on this default.
	srv.SetResponder("admin.Health", func(_ []byte) (uint16, []byte) {
		// Marshal a minimal HealthResponse with status="ok" so the SDK's
		// Healthy() check returns true.
		return wire.StatusOK, mustMarshalHealthOK()
	})

	srv.wg.Add(1)
	go srv.acceptLoop()

	t.Cleanup(func() {
		srv.Stop()
	})

	addr := ln.Addr().String()
	tr, err := New(
		transport.WithAddress(addr),
		transport.WithTLSConfig(clientCfg),
		transport.WithTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	t.Cleanup(func() {
		_ = tr.Close()
	})

	return srv, tr
}

// SetResponder installs the given responder for the named wire method.
// Subsequent invocations of method will be routed to fn.
func (s *e2eServer) SetResponder(method string, fn wireResponder) {
	s.mu.Lock()
	s.responders[method] = fn
	s.mu.Unlock()
}

// Recorded returns a copy of all requests recorded by the server.
func (s *e2eServer) Recorded() []recordedRequest {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	out := make([]recordedRequest, len(s.recordedRequests))
	copy(out, s.recordedRequests)
	return out
}

// LastRequest returns the most recent request the server received for the
// given method, or nil if none.
func (s *e2eServer) LastRequest(method string) *recordedRequest {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	for i := len(s.recordedRequests) - 1; i >= 0; i-- {
		if s.recordedRequests[i].Method == method {
			r := s.recordedRequests[i]
			return &r
		}
	}
	return nil
}

// Stop closes the listener and waits for serving goroutines to drain.
func (s *e2eServer) Stop() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.listener.Close()
	s.wg.Wait()
}

// acceptLoop accepts new QUIC connections and dispatches each one.
func (s *e2eServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept(context.Background())
		if err != nil {
			if s.closed.Load() {
				return
			}
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection processes streams on a single QUIC connection.
func (s *e2eServer) handleConnection(conn *quicgo.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.CloseWithError(0, "test done") }()
	cs := conn.ConnectionState().TLS
	s.tlsState.Store(&cs)

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleStream(stream)
	}
}

// handleStream reads one request frame, dispatches it, and writes the
// response frame.
func (s *e2eServer) handleStream(stream *quicgo.Stream) {
	defer s.wg.Done()
	defer func() { _ = stream.Close() }()

	method, database, requestID, payload, err := wire.ReadRequest(stream)
	if err != nil {
		return
	}

	s.recordMu.Lock()
	s.recordedRequests = append(s.recordedRequests, recordedRequest{
		Method:   method,
		Database: database,
		Payload:  append([]byte(nil), payload...),
	})
	s.recordMu.Unlock()

	s.mu.RLock()
	responder, ok := s.responders[method]
	s.mu.RUnlock()

	if !ok {
		writeHarnessResponse(stream, requestID, wire.StatusUnimplemented, []byte("no responder: "+method))
		return
	}

	status, resp := responder(payload)
	writeHarnessResponse(stream, requestID, status, resp)
}

// writeHarnessResponse writes a responder's answer as the real server frames
// it: an OK body as given, and a non-OK body -- which responders write as the
// message text -- as the wire.ErrorPayload every error frame carries.
func writeHarnessResponse(stream *quicgo.Stream, requestID uint64, status uint16, resp []byte) {
	if status != wire.StatusOK {
		body, err := wire.EncodeErrorPayload(status, wire.ErrorPayload{Message: string(resp)})
		if err != nil {
			panic("e2e harness: error payload does not encode: " + err.Error())
		}
		resp = body
	}
	_ = wire.WriteResponse(stream, requestID, status, resp)
}

// e2eTLS builds a self-signed ECDSA P-256 TLS bundle for the E2E tests.
func e2eTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "qrdb-sdk-e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        parsed,
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(parsed)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{wire.ProtocolV3},
	}
	clientCfg := &tls.Config{
		RootCAs:    caPool,
		NextProtos: []string{wire.ProtocolV3},
		MinVersion: tls.VersionTLS13,
	}
	return serverCfg, clientCfg
}
